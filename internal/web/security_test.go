package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"

	"namedot/internal/config"
	"namedot/internal/db"
)

func newTestWebWithPasswordHash(t *testing.T, hash string, tlsEnabled bool) (*Server, *gin.Engine) {
	t.Helper()
	cfg := &config.Config{
		Admin: config.AdminConfig{
			Enabled:      true,
			Username:     "admin",
			PasswordHash: hash,
		},
	}
	if tlsEnabled {
		cfg.TLSCertFile = "cert.pem"
		cfg.TLSKeyFile = "key.pem"
	}
	db := newTestDB(t)
	s, err := NewServer(cfg, db)
	if err != nil {
		t.Fatalf("new web: %v", err)
	}
	r := gin.New()
	s.RegisterRoutes(r)
	return s, r
}

func TestLoginSubmit_InvalidUsername(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	_, r := newTestWebWithPasswordHash(t, string(hash), false)

	req := httptest.NewRequest("POST", "/admin/login", strings.NewReader("username=bad&password=secret"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status %d", w.Code)
	}
	if cookies := w.Header().Values("Set-Cookie"); len(cookies) != 0 {
		t.Fatalf("expected no session cookie, got %v", cookies)
	}
}

func TestLoginSubmit_InvalidPassword(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	_, r := newTestWebWithPasswordHash(t, string(hash), false)

	req := httptest.NewRequest("POST", "/admin/login", strings.NewReader("username=admin&password=bad"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status %d", w.Code)
	}
	if cookies := w.Header().Values("Set-Cookie"); len(cookies) != 0 {
		t.Fatalf("expected no session cookie, got %v", cookies)
	}
}

func TestLoginSubmit_SetsSecureSessionCookieWhenTLSEnabled(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	_, r := newTestWebWithPasswordHash(t, string(hash), true)

	req := httptest.NewRequest("POST", "/admin/login", strings.NewReader("username=admin&password=secret"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	cookies := w.Header().Values("Set-Cookie")
	if len(cookies) != 1 {
		t.Fatalf("expected 1 cookie, got %v", cookies)
	}
	if !strings.Contains(cookies[0], "session=") {
		t.Fatalf("missing session cookie: %v", cookies[0])
	}
	if !strings.Contains(cookies[0], "HttpOnly") || !strings.Contains(cookies[0], "SameSite=Strict") || !strings.Contains(cookies[0], "Secure") {
		t.Fatalf("cookie missing security flags: %v", cookies[0])
	}
	if !strings.Contains(cookies[0], "Path=/admin") {
		t.Fatalf("cookie missing admin path: %v", cookies[0])
	}
}

func TestAuthMiddleware_ExpiredSessionRedirects(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	s, r := newTestWebWithPasswordHash(t, string(hash), false)

	sid := "expired"
	setTestSession(s, sid, &Session{
		Username:  "admin",
		CreatedAt: time.Now().Add(-2 * time.Hour),
		ExpiresAt: time.Now().Add(-1 * time.Hour),
		CSRFToken: "csrf",
	})

	req := httptest.NewRequest("GET", "/admin/", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: sid, Path: "/admin"})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("status %d", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/admin/login" {
		t.Fatalf("expected redirect to /admin/login, got %q", loc)
	}
	s.sessionsMu.RLock()
	_, exists := s.sessions[sid]
	s.sessionsMu.RUnlock()
	if exists {
		t.Fatalf("expected expired session to be removed")
	}
}

func TestCSRFMiddleware_RejectsMissingToken(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	s, _ := newTestWebWithPasswordHash(t, string(hash), false)

	r := gin.New()
	r.POST("/admin/secure", s.authMiddleware(), s.csrfMiddleware(), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	sid := "sess"
	setTestSession(s, sid, &Session{
		Username:  "admin",
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(time.Hour),
		CSRFToken: "csrf",
	})

	req := httptest.NewRequest("POST", "/admin/secure", nil)
	req.Host = "example.com"
	req.AddCookie(&http.Cookie{Name: "session", Value: sid, Path: "/admin"})
	req.Header.Set("Origin", "http://example.com")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status %d", w.Code)
	}
}

func TestCSRFMiddleware_RejectsInvalidOrigin(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	s, _ := newTestWebWithPasswordHash(t, string(hash), false)

	r := gin.New()
	r.POST("/admin/secure", s.authMiddleware(), s.csrfMiddleware(), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	sid := "sess"
	setTestSession(s, sid, &Session{
		Username:  "admin",
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(time.Hour),
		CSRFToken: "csrf",
	})

	req := httptest.NewRequest("POST", "/admin/secure", nil)
	req.Host = "example.com"
	req.AddCookie(&http.Cookie{Name: "session", Value: sid, Path: "/admin"})
	req.Header.Set("X-CSRF-Token", "csrf")
	req.Header.Set("Origin", "http://evil.example")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status %d", w.Code)
	}
}

func TestCSRFMiddleware_AllowsValidOrigin(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	s, _ := newTestWebWithPasswordHash(t, string(hash), false)

	r := gin.New()
	r.POST("/admin/secure", s.authMiddleware(), s.csrfMiddleware(), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	sid := "sess"
	setTestSession(s, sid, &Session{
		Username:  "admin",
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(time.Hour),
		CSRFToken: "csrf",
	})

	req := httptest.NewRequest("POST", "/admin/secure", nil)
	req.Host = "example.com"
	req.AddCookie(&http.Cookie{Name: "session", Value: sid, Path: "/admin"})
	req.Header.Set("X-CSRF-Token", "csrf")
	req.Header.Set("Origin", "http://example.com")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
}

func TestOnChange_CalledAfterSuccessfulMutation(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	s, r := newTestWebWithPasswordHash(t, string(hash), false)
	calls := 0
	s.OnChange = func() { calls++ }

	sid := "sess"
	setTestSession(s, sid, &Session{
		Username:  "admin",
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(time.Hour),
		CSRFToken: "csrf",
	})
	post := func(body string) {
		req := httptest.NewRequest("POST", "/admin/zones", strings.NewReader(body))
		req.Host = "example.com"
		req.AddCookie(&http.Cookie{Name: "session", Value: sid, Path: "/admin"})
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("HX-Request", "true")
		req.Header.Set("X-CSRF-Token", "csrf")
		req.Header.Set("Origin", "http://example.com")
		r.ServeHTTP(httptest.NewRecorder(), req)
	}

	post("name=")
	if calls != 0 {
		t.Fatalf("OnChange must not run on failed request, calls=%d", calls)
	}
	post("name=onchange.test")
	if calls != 1 {
		t.Fatalf("OnChange must run once after zone create, calls=%d", calls)
	}
}

func TestLogout_DeletesSession(t *testing.T) {
	s, r := newTestWebWithPasswordHash(t, "", false)

	sid := "active"
	setTestSession(s, sid, &Session{
		Username:  "admin",
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(time.Hour),
		CSRFToken: "csrf",
	})

	req := httptest.NewRequest("GET", "/admin/logout", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: sid, Path: "/admin"})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusFound || w.Header().Get("Location") != "/admin/login" {
		t.Fatalf("expected redirect to login, got %d %q", w.Code, w.Header().Get("Location"))
	}
	s.sessionsMu.RLock()
	_, exists := s.sessions[sid]
	s.sessionsMu.RUnlock()
	if exists {
		t.Fatalf("expected session to be removed")
	}
}

func TestListZones_EscapesSearch(t *testing.T) {
	s, r := newTestWebWithPasswordHash(t, "", false)

	sid := "sess"
	setTestSession(s, sid, &Session{
		Username:  "admin",
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(time.Hour),
		CSRFToken: "csrf",
	})

	payload := `"><img src=x onerror=alert(1)>`
	req := httptest.NewRequest("GET", "/admin/zones?search="+url.QueryEscape(payload), nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: sid, Path: "/admin"})
	req.Header.Set("HX-Request", "true")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	body := w.Body.String()
	if strings.Contains(body, "<img src=x") {
		t.Fatalf("search value not escaped: %s", body)
	}
	if !strings.Contains(body, "&lt;img src=x") {
		t.Fatalf("expected escaped payload in body: %s", body)
	}
}

func TestUpdateRecord_IgnoresFormIDs(t *testing.T) {
	s, r := newTestWebWithPasswordHash(t, "", false)
	sid := "sess"
	setTestSession(s, sid, &Session{
		Username:  "admin",
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(time.Hour),
		CSRFToken: "csrf",
	})

	own := db.Zone{Name: "own.test.", RRSets: []db.RRSet{{Name: "www.own.test.", Type: "A", TTL: 300, Records: []db.RData{{Data: "192.0.2.1"}}}}}
	other := db.Zone{Name: "other.test.", RRSets: []db.RRSet{{Name: "www.other.test.", Type: "A", TTL: 300}}}
	for _, z := range []*db.Zone{&own, &other} {
		if err := s.db.Create(z).Error; err != nil {
			t.Fatalf("create zone: %v", err)
		}
	}
	recID := own.RRSets[0].Records[0].ID
	form := url.Values{
		"data":     {"192.0.2.2"},
		"ttl":      {"999"},
		"zone_id":  {strconv.Itoa(int(other.ID))},
		"rrset_id": {strconv.Itoa(int(other.RRSets[0].ID))},
	}
	req := httptest.NewRequest("PUT", "/admin/records/"+strconv.Itoa(int(recID)), strings.NewReader(form.Encode()))
	req.Host = "example.com"
	req.AddCookie(&http.Cookie{Name: "session", Value: sid, Path: "/admin"})
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	req.Header.Set("X-CSRF-Token", "csrf")
	req.Header.Set("Origin", "http://example.com")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "own.test.") {
		t.Fatalf("expected records list of own zone")
	}
	var ownSet, otherSet db.RRSet
	s.db.First(&ownSet, own.RRSets[0].ID)
	s.db.First(&otherSet, other.RRSets[0].ID)
	if ownSet.TTL != 999 || otherSet.TTL != 300 {
		t.Fatalf("TTL went to the wrong rrset: own=%d other=%d", ownSet.TTL, otherSet.TTL)
	}
}

func TestLoginSubmit_PurgesExpiredSessions(t *testing.T) {
	hash, _ := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	s, r := newTestWebWithPasswordHash(t, string(hash), false)
	s.sessions["old"] = &Session{Username: "admin", ExpiresAt: time.Now().Add(-time.Hour)}

	req := httptest.NewRequest("POST", "/admin/login", strings.NewReader("username=admin&password=secret"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.ServeHTTP(httptest.NewRecorder(), req)

	if _, ok := s.sessions["old"]; ok {
		t.Fatal("expired session must be removed on login")
	}
	if len(s.sessions) != 1 {
		t.Fatalf("expected only the new session, got %d", len(s.sessions))
	}
}
