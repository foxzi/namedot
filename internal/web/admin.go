package web

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"html/template"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"namedot/internal/config"
)

//go:embed templates/*.html
var templatesFS embed.FS

type Server struct {
	cfg        *config.Config
	db         *gorm.DB
	tmpl       *template.Template
	sessions   map[string]*Session // sessionID -> Session
	sessionsMu sync.RWMutex
	// OnChange is called after a successful data change (e.g. DNS cache invalidation)
	OnChange func()
}

type Session struct {
	Username  string
	CreatedAt time.Time
	ExpiresAt time.Time
	CSRFToken string
	LastPath  string
}

func NewServer(cfg *config.Config, db *gorm.DB) (*Server, error) {
	if !cfg.Admin.Enabled {
		return nil, nil
	}

	funcMap := template.FuncMap{
		// Usage in templates: {{ t .Lang "Key" }}
		"t": func(lang, key string) string { return tr(lang, key) },
	}
	tmpl, err := template.New("root").Funcs(funcMap).ParseFS(templatesFS, "templates/*.html")
	if err != nil {
		return nil, err
	}

	return &Server{
		cfg:      cfg,
		db:       db,
		tmpl:     tmpl,
		sessions: make(map[string]*Session),
	}, nil
}

func (s *Server) RegisterRoutes(r *gin.Engine) {
	if s == nil || !s.cfg.Admin.Enabled {
		return
	}

	// Public routes
	r.GET("/admin/login", s.loginPage)
	r.POST("/admin/login", s.loginSubmit)
	r.GET("/admin/lang/:code", s.setLang)
	// Logout is outside the protected group: htmxRedirectMiddleware would
	// render the dashboard for this plain link instead of logging out
	r.GET("/admin/logout", s.logout)

	// Protected routes
	admin := r.Group("/admin")
	admin.Use(s.authMiddleware())
	admin.Use(s.htmxRedirectMiddleware())
	admin.Use(s.onChangeMiddleware())
	{
		admin.GET("/", s.dashboard)

		// Zones
		admin.GET("/zones", s.listZones)
		admin.GET("/zones/new", s.newZoneForm)
		admin.POST("/zones", s.csrfMiddleware(), s.createZone)
		admin.DELETE("/zones/delete/:id", s.csrfMiddleware(), s.deleteZone)

		// Records
		admin.GET("/zones/:id/records", s.listRecords)
		admin.GET("/zones/:id/records/new", s.newRecordForm)
		admin.POST("/zones/:id/records", s.csrfMiddleware(), s.createRecord)
		admin.GET("/records/:id/edit", s.editRecordForm)
		admin.PUT("/records/:id", s.csrfMiddleware(), s.updateRecord)
		admin.DELETE("/records/:id", s.csrfMiddleware(), s.deleteRecord)

		// Templates
		admin.GET("/templates", s.listTemplates)
		admin.GET("/templates/new", s.newTemplateForm)
		admin.POST("/templates", s.csrfMiddleware(), s.createTemplate)
		admin.GET("/templates/:id/view", s.viewTemplate)
		admin.GET("/templates/:id/edit", s.editTemplateForm)
		admin.PUT("/templates/:id", s.csrfMiddleware(), s.updateTemplate)
		admin.DELETE("/templates/:id", s.csrfMiddleware(), s.deleteTemplate)
		admin.GET("/templates/:id/records/new", s.newTemplateRecordForm)
		admin.POST("/templates/:id/records", s.csrfMiddleware(), s.createTemplateRecord)
		admin.DELETE("/templates/records/:id", s.csrfMiddleware(), s.deleteTemplateRecord)
		admin.GET("/templates/:id/apply", s.applyTemplateForm)
		admin.POST("/templates/:id/apply", s.csrfMiddleware(), s.applyTemplate)
	}
}

// onChangeMiddleware calls OnChange after successful state-changing requests
func (s *Server) onChangeMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()
		if s.OnChange != nil && c.Request.Method != http.MethodGet && c.Writer.Status() < 400 {
			s.OnChange()
		}
	}
}

// htmxRedirectMiddleware renders dashboard for non-HTMX requests to fragment endpoints
func (s *Server) htmxRedirectMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Request.URL.Path
		// If this is a fragment endpoint (not /admin root) and not an HTMX request
		if path != "/admin" && path != "/admin/" && c.GetHeader("HX-Request") == "" {
			// Save the path to session so dashboard can restore it
			if sessionID, exists := c.Get("session_id"); exists {
				s.sessionsMu.Lock()
				if session, ok := s.sessions[sessionID.(string)]; ok {
					fullPath := path
					if c.Request.URL.RawQuery != "" {
						fullPath += "?" + c.Request.URL.RawQuery
					}
					session.LastPath = fullPath
				}
				s.sessionsMu.Unlock()
			}
			// Render dashboard instead of redirecting (to keep URL intact)
			s.dashboard(c)
			c.Abort()
			return
		}
		c.Next()
	}
}

// authMiddleware checks for valid session
func (s *Server) authMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		cookie, err := c.Cookie("session")
		if err != nil {
			c.Redirect(http.StatusFound, "/admin/login")
			c.Abort()
			return
		}

		s.sessionsMu.RLock()
		session, exists := s.sessions[cookie]
		s.sessionsMu.RUnlock()
		if !exists || time.Now().After(session.ExpiresAt) {
			s.sessionsMu.Lock()
			delete(s.sessions, cookie)
			s.sessionsMu.Unlock()
			c.Redirect(http.StatusFound, "/admin/login")
			c.Abort()
			return
		}

		c.Set("username", session.Username)
		c.Set("csrf_token", session.CSRFToken)
		c.Set("session_id", cookie)

		// Save last visited path in session (except for login/logout/dashboard)
		// Don't save /admin path as it's the restore point, not a real navigation
		path := c.Request.URL.Path
		if path != "/admin" && path != "/admin/" && path != "/admin/login" && path != "/admin/logout" && !strings.HasPrefix(path, "/admin/lang/") {
			s.sessionsMu.Lock()
			session.LastPath = path
			if c.Request.URL.RawQuery != "" {
				session.LastPath += "?" + c.Request.URL.RawQuery
			}
			s.sessionsMu.Unlock()
		}

		c.Next()
	}
}

// Login handlers
func (s *Server) loginPage(c *gin.Context) {
	c.Header("Content-Type", "text/html; charset=utf-8")
	s.tmpl.ExecuteTemplate(c.Writer, "login.html", gin.H{"Lang": s.getLang(c)})
}

func (s *Server) loginSubmit(c *gin.Context) {
	username := c.PostForm("username")
	password := c.PostForm("password")

	// Validate credentials
	if username != s.cfg.Admin.Username {
		c.Header("HX-Retarget", "#error")
		c.Header("HX-Reswap", "innerHTML")
		c.String(http.StatusUnauthorized, `<div class="error">`+s.tr(c, "Invalid username or password")+`</div>`)
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(s.cfg.Admin.PasswordHash), []byte(password)); err != nil {
		c.Header("HX-Retarget", "#error")
		c.Header("HX-Reswap", "innerHTML")
		c.String(http.StatusUnauthorized, `<div class="error">`+s.tr(c, "Invalid username or password")+`</div>`)
		return
	}

	// Create session with CSRF token
	sessionID := s.generateSessionID()
	csrfToken := s.generateSessionID()
	s.sessionsMu.Lock()
	// Drop expired sessions, otherwise abandoned ones stay in memory forever
	now := time.Now()
	for id, sess := range s.sessions {
		if now.After(sess.ExpiresAt) {
			delete(s.sessions, id)
		}
	}
	s.sessions[sessionID] = &Session{
		Username:  username,
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(24 * time.Hour),
		CSRFToken: csrfToken,
	}
	s.sessionsMu.Unlock()

	s.setSecureCookie(c, "session", sessionID, 86400, "/admin")
	c.Header("HX-Redirect", "/admin")
	c.Status(http.StatusOK)
}

func (s *Server) logout(c *gin.Context) {
	cookie, _ := c.Cookie("session")
	s.sessionsMu.Lock()
	delete(s.sessions, cookie)
	s.sessionsMu.Unlock()
	s.setSecureCookie(c, "session", "", -1, "/admin")
	c.Redirect(http.StatusFound, "/admin/login")
}

func (s *Server) dashboard(c *gin.Context) {
	username, _ := c.Get("username")
	csrfToken, _ := c.Get("csrf_token")
	sessionID, _ := c.Get("session_id")

	lastPath := "/admin/zones"
	if sessionID != nil {
		s.sessionsMu.RLock()
		if session, exists := s.sessions[sessionID.(string)]; exists && session.LastPath != "" && session.LastPath != "/admin" && session.LastPath != "/admin/" {
			lastPath = session.LastPath
		}
		s.sessionsMu.RUnlock()
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	s.tmpl.ExecuteTemplate(c.Writer, "dashboard.html", gin.H{
		"Username":  username,
		"Lang":      s.getLang(c),
		"CSRFToken": csrfToken,
		"LastPath":  lastPath,
	})
}

func (s *Server) generateSessionID() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.URLEncoding.EncodeToString(b)
}

// HashPassword generates bcrypt hash for password (utility function)

// i18n helpers
func (s *Server) getLang(c *gin.Context) string {
	if v, err := c.Cookie("lang"); err == nil {
		if v == "ru" || v == "en" {
			return v
		}
	}
	// simple accept-language sniff
	if al := c.GetHeader("Accept-Language"); al != "" {
		if len(al) >= 2 {
			p := al[:2]
			if p == "ru" {
				return "ru"
			}
		}
	}
	return "en"
}

func (s *Server) setLang(c *gin.Context) {
	code := c.Param("code")
	if code != "en" && code != "ru" {
		code = "en"
	}
	// 365 days
	s.setSecureCookie(c, "lang", code, 365*24*3600, "/")
	// Only allow relative redirects to prevent open redirect via Referer header
	ref := c.Request.Referer()
	if ref == "" || !strings.HasPrefix(ref, "/") {
		host := c.Request.Host
		if strings.HasPrefix(ref, "https://"+host+"/") {
			ref = ref[len("https://"+host):]
		} else if strings.HasPrefix(ref, "http://"+host+"/") {
			ref = ref[len("http://"+host):]
		} else {
			ref = "/admin"
		}
	}
	c.Redirect(http.StatusFound, ref)
}

func (s *Server) tr(c *gin.Context, key string) string {
	return tr(s.getLang(c), key)
}

func (s *Server) trf(c *gin.Context, key string, a ...any) string {
	return trf(s.getLang(c), key, a...)
}

// setSecureCookie sets a cookie with secure flags
func (s *Server) setSecureCookie(c *gin.Context, name, value string, maxAge int, path string) {
	secure := s.cfg.IsTLSEnabled()
	sameSite := http.SameSiteStrictMode

	http.SetCookie(c.Writer, &http.Cookie{
		Name:     name,
		Value:    value,
		MaxAge:   maxAge,
		Path:     path,
		Secure:   secure,
		HttpOnly: true,
		SameSite: sameSite,
	})
}

// csrfMiddleware validates CSRF token for state-changing requests
func (s *Server) csrfMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Get CSRF token from session
		expectedToken, exists := c.Get("csrf_token")
		if !exists {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}

		// Get CSRF token from request (header or form)
		token := c.GetHeader("X-CSRF-Token")
		if token == "" {
			token = c.PostForm("csrf_token")
		}

		expected := expectedToken.(string)
		if token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(expected)) != 1 {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}

		// Validate Origin/Referer for additional security
		if !s.validateOrigin(c) {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}

		c.Next()
	}
}

// validateOrigin checks that request comes from the same origin
func (s *Server) validateOrigin(c *gin.Context) bool {
	origin := c.GetHeader("Origin")
	referer := c.GetHeader("Referer")

	// If no Origin or Referer, reject (defense in depth)
	if origin == "" && referer == "" {
		return false
	}

	host := c.Request.Host

	// Check Origin header
	if origin != "" {
		// Extract host from origin (remove scheme)
		if origin != "https://"+host && origin != "http://"+host {
			return false
		}
	}

	// Check Referer header
	if referer != "" {
		// Referer should start with the same scheme and host
		if !strings.HasPrefix(referer, "https://"+host+"/") &&
			!strings.HasPrefix(referer, "http://"+host+"/") {
			return false
		}
	}

	return true
}

func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(hash), err
}
