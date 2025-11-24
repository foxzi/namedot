package rest

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"namedot/internal/config"
	dbm "namedot/internal/db"
	"namedot/internal/server/rest/zoneio"
	"namedot/internal/web"
)

// DNSServer interface for cache invalidation
type DNSServer interface {
	InvalidateZoneCache()
}

type Server struct {
	cfg        *config.Config
	db         *gorm.DB
	r          *gin.Engine
	httpServer *http.Server
	tlsStopCh  chan struct{}
	dnsServer  DNSServer
}

func NewServer(cfg *config.Config, db *gorm.DB, dnsServer DNSServer) *Server {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	// Log all API requests to stdout
	r.Use(gin.LoggerWithFormatter(func(param gin.LogFormatterParams) string {
		return fmt.Sprintf("API %s %s %d %s from %s\n",
			param.Method,
			param.Path,
			param.StatusCode,
			param.Latency,
			param.ClientIP,
		)
	}))
	r.Use(gin.Recovery())

	// Apply IP ACL if configured
	if cfg.HasIPACL() {
		r.Use(ipACLMiddleware(cfg.AllowedCIDRs))
	}

	s := &Server{cfg: cfg, db: db, r: r, dnsServer: dnsServer}

	// Public endpoints (no auth)
	r.GET("/health", s.health)

	// Web Admin UI
	webAdmin, err := web.NewServer(cfg, db)
	if err != nil {
		log.Printf("Web admin initialization error: %v", err)
	} else if webAdmin != nil {
		webAdmin.RegisterRoutes(r)
		log.Printf("Web admin panel enabled at /admin")
	}

	auth := func(c *gin.Context) {
		token := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")

		// Check if token is valid using either hash or plain text
		authenticated := false

		// Try hashed token first (recommended)
		if s.cfg.APITokenHash != "" {
			if err := bcrypt.CompareHashAndPassword([]byte(s.cfg.APITokenHash), []byte(token)); err == nil {
				authenticated = true
			}
		} else if s.cfg.APIToken != "" {
			// Fallback to plain text comparison (deprecated)
			if token == s.cfg.APIToken {
				authenticated = true
			}
		} else {
			// No authentication configured, allow all
			authenticated = true
		}

		if !authenticated {
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		c.Next()
	}

	api := r.Group("/")
	api.Use(auth)
	{
		api.POST("/zones", s.createZone)
		api.GET("/zones", s.listZones)
		api.GET("/zones/:id", s.getZone)
		api.DELETE("/zones/:id", s.deleteZone)

		api.POST("/zones/:id/rrsets", s.createRRSet)
		api.PUT("/zones/:id/rrsets/:rid", s.updateRRSet)
		api.PATCH("/zones/:id/rrsets/:rid", s.patchRRSet)
		api.DELETE("/zones/:id/rrsets/:rid", s.deleteRRSet)
		api.GET("/zones/:id/rrsets", s.listRRSets)

		api.GET("/zones/:id/export", s.exportZone)
		api.POST("/zones/:id/import", s.importZone)

		// Replication endpoints
		api.GET("/sync/export", s.syncExport)
		api.POST("/sync/import", s.syncImport)
	}
	return s
}

func (s *Server) Start() error {
	s.httpServer = &http.Server{
		Addr:    s.cfg.RESTListen,
		Handler: s.r,
	}

	if s.cfg.IsTLSEnabled() {
		// Create certificate reloader
		certReloader, err := newCertReloader(s.cfg.TLSCertFile, s.cfg.TLSKeyFile)
		if err != nil {
			return fmt.Errorf("failed to load TLS certificate: %w", err)
		}

		// Configure TLS
		s.httpServer.TLSConfig = &tls.Config{
			GetCertificate: certReloader.getCertificate,
			MinVersion:     tls.VersionTLS12,
		}

		// Start certificate reloader if interval is configured
		if s.cfg.TLSReloadSec > 0 {
			s.tlsStopCh = make(chan struct{})
			go certReloader.startReloading(time.Duration(s.cfg.TLSReloadSec)*time.Second, s.tlsStopCh)
			log.Printf("Starting REST API with HTTPS on %s (cert reload every %d seconds)", s.cfg.RESTListen, s.cfg.TLSReloadSec)
		} else {
			log.Printf("Starting REST API with HTTPS on %s (cert reload disabled)", s.cfg.RESTListen)
		}

		return s.httpServer.ListenAndServeTLS("", "")
	}

	log.Printf("Starting REST API with HTTP on %s", s.cfg.RESTListen)
	return s.httpServer.ListenAndServe()
}

func (s *Server) Shutdown(ctx context.Context) error {
	// Stop TLS certificate reloader if running
	if s.tlsStopCh != nil {
		close(s.tlsStopCh)
	}

	// Shutdown HTTP server gracefully
	if s.httpServer != nil {
		return s.httpServer.Shutdown(ctx)
	}
	return nil
}

// Handlers

// health returns server health status
func (s *Server) health(c *gin.Context) {
	status := "ok"
	dbStatus := "ok"

	// Check database connectivity
	sqlDB, err := s.db.DB()
	if err != nil {
		dbStatus = "error"
		status = "degraded"
	} else if err := sqlDB.Ping(); err != nil {
		dbStatus = "unreachable"
		status = "degraded"
	}

	response := gin.H{
		"status": status,
		"db":     dbStatus,
	}

	if status == "ok" {
		c.JSON(http.StatusOK, response)
	} else {
		c.JSON(http.StatusServiceUnavailable, response)
	}
}

type zoneReq struct {
	Name string `json:"name"`
}

func (s *Server) createZone(c *gin.Context) {
	var req zoneReq
	if err := c.ShouldBindJSON(&req); err != nil || req.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload"})
		return
	}
	z := dbm.Zone{Name: strings.ToLower(req.Name)}
	if err := s.db.Create(&z).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// Invalidate DNS zone cache
	if s.dnsServer != nil {
		s.dnsServer.InvalidateZoneCache()
	}
	c.JSON(http.StatusCreated, z)
}

func (s *Server) listZones(c *gin.Context) {
	var zs []dbm.Zone
	if err := s.db.Find(&zs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, zs)
}

func (s *Server) getZone(c *gin.Context) {
	var z dbm.Zone
	if err := s.db.Preload("RRSets").First(&z, c.Param("id")).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	c.JSON(http.StatusOK, z)
}

func (s *Server) deleteZone(c *gin.Context) {
	var z dbm.Zone
	if err := s.db.First(&z, c.Param("id")).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("zone_id = ?", z.ID).Delete(&dbm.RRSet{}).Error; err != nil {
			return err
		}
		if err := tx.Delete(&z).Error; err != nil {
			return err
		}
		return nil
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// Invalidate DNS zone cache
	if s.dnsServer != nil {
		s.dnsServer.InvalidateZoneCache()
	}
	c.Status(http.StatusNoContent)
}

type rrsetReq struct {
	Name    string      `json:"name"`
	Type    string      `json:"type"`
	TTL     uint32      `json:"ttl"`
	Records []dbm.RData `json:"records"`
}

func fqdn(name, zone string) string {
	n := strings.ToLower(name)
	// Support convenience syntax: trailing ".@" means "relative to zone apex"
	if strings.HasSuffix(n, ".@") {
		n = strings.TrimSuffix(n, ".@")
	}
	n = strings.TrimSuffix(n, ".")
	z := strings.TrimSuffix(strings.ToLower(zone), ".")
	if n == "" || n == "@" {
		return z + "."
	}
	return n + "." + z + "."
}

func (s *Server) createRRSet(c *gin.Context) {
	var z dbm.Zone
	if err := s.db.First(&z, c.Param("id")).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "zone not found"})
		return
	}
	var req rrsetReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload"})
		return
	}

	name := strings.ToLower(fqdn(req.Name, z.Name))
	recordType := strings.ToUpper(req.Type)

	// Check if RRSet already exists (zone_id, name, type must be unique)
	var existing dbm.RRSet
	err := s.db.Where("zone_id = ? AND name = ? AND type = ?", z.ID, name, recordType).First(&existing).Error
	if err == nil {
		// RRSet already exists, return 409 Conflict
		c.JSON(http.StatusConflict, gin.H{
			"error":       "rrset already exists",
			"message":     "A record with this name and type already exists in this zone. Use PUT to update or DELETE first.",
			"existing_id": existing.ID,
		})
		return
	} else if err != gorm.ErrRecordNotFound {
		// Database error
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	set := dbm.RRSet{
		ZoneID:  z.ID,
		Name:    name,
		Type:    recordType,
		TTL:     req.TTL,
		Records: req.recordsNormalized(),
	}
	if set.TTL == 0 && s.cfg.DefaultTTL > 0 {
		set.TTL = s.cfg.DefaultTTL
	}
	// Expand CNAME "@" shorthand in record data to apex FQDN before save
	if strings.EqualFold(set.Type, "CNAME") {
		for i := range set.Records {
			if strings.TrimSpace(set.Records[i].Data) == "@" {
				set.Records[i].Data = fqdn("@", z.Name)
			}
		}
	}
	if err := s.db.Create(&set).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	dbm.BumpSOASerialAuto(s.db, z, s.cfg.SOA.AutoOnMissing, s.cfg.SOA.Primary, s.cfg.SOA.Hostmaster)
	// Invalidate DNS cache after zone record change
	if s.dnsServer != nil {
		s.dnsServer.InvalidateZoneCache()
	}
	c.JSON(http.StatusCreated, set)
}

func (s *Server) updateRRSet(c *gin.Context) {
	var z dbm.Zone
	if err := s.db.First(&z, c.Param("id")).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "zone not found"})
		return
	}
	var set dbm.RRSet
	if err := s.db.Preload("Records").Where("zone_id = ? AND id = ?", z.ID, c.Param("rid")).First(&set).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "rrset not found"})
		return
	}
	var req rrsetReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload"})
		return
	}
	set.Name = strings.ToLower(fqdn(req.Name, z.Name))
	set.Type = strings.ToUpper(req.Type)
	set.TTL = req.TTL
	if set.TTL == 0 && s.cfg.DefaultTTL > 0 {
		set.TTL = s.cfg.DefaultTTL
	}
	// replace records
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("rr_set_id = ?", set.ID).Delete(&dbm.RData{}).Error; err != nil {
			return err
		}
		set.Records = req.recordsNormalized()
		if strings.EqualFold(set.Type, "CNAME") {
			for i := range set.Records {
				if strings.TrimSpace(set.Records[i].Data) == "@" {
					set.Records[i].Data = fqdn("@", z.Name)
				}
			}
		}
		return tx.Save(&set).Error
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	dbm.BumpSOASerialAuto(s.db, z, s.cfg.SOA.AutoOnMissing, s.cfg.SOA.Primary, s.cfg.SOA.Hostmaster)
	// Invalidate DNS cache after zone record change
	if s.dnsServer != nil {
		s.dnsServer.InvalidateZoneCache()
	}
	c.JSON(http.StatusOK, set)
}

func (s *Server) patchRRSet(c *gin.Context) { s.updateRRSet(c) }

func (s *Server) deleteRRSet(c *gin.Context) {
	var z dbm.Zone
	if err := s.db.First(&z, c.Param("id")).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "zone not found"})
		return
	}
	if err := s.db.Delete(&dbm.RRSet{}, "zone_id = ? AND id = ?", z.ID, c.Param("rid")).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	dbm.BumpSOASerial(s.db, z.ID)
	// Invalidate DNS cache after zone record change
	if s.dnsServer != nil {
		s.dnsServer.InvalidateZoneCache()
	}
	c.Status(http.StatusNoContent)
}

func (s *Server) listRRSets(c *gin.Context) {
	var sets []dbm.RRSet
	if err := s.db.Preload("Records").Where("zone_id = ?", c.Param("id")).Find(&sets).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, sets)
}

func (s *Server) exportZone(c *gin.Context) {
	format := strings.ToLower(c.DefaultQuery("format", "json"))
	var z dbm.Zone
	if err := s.db.Preload("RRSets.Records").First(&z, c.Param("id")).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "zone not found"})
		return
	}
	switch format {
	case "json":
		c.JSON(http.StatusOK, z)
	case "bind":
		txt := zoneio.ToBind(&z)
		c.String(http.StatusOK, txt)
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported format"})
	}
}

func (s *Server) importZone(c *gin.Context) {
	format := strings.ToLower(c.DefaultQuery("format", "json"))
	mode := strings.ToLower(c.DefaultQuery("mode", "upsert"))
	if mode != "upsert" && mode != "replace" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported mode"})
		return
	}
	// serial handling is kept simple; bump after import
	var z dbm.Zone
	if err := s.db.Preload("RRSets.Records").First(&z, c.Param("id")).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "zone not found"})
		return
	}
	switch format {
	case "json":
		var raw any
		dec := json.NewDecoder(c.Request.Body)
		dec.UseNumber()
		if err := dec.Decode(&raw); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json"})
			return
		}
		cleaned := stripTimestamps(raw)
		buf, err := json.Marshal(cleaned)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json"})
			return
		}
		var in dbm.Zone
		if err := json.Unmarshal(buf, &in); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json"})
			return
		}
		if err := zoneio.ImportJSON(s.db, &z, &in, mode, s.cfg.DefaultTTL); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		dbm.BumpSOASerialAuto(s.db, z, s.cfg.SOA.AutoOnMissing, s.cfg.SOA.Primary, s.cfg.SOA.Hostmaster)
		// Invalidate DNS cache after zone import
		if s.dnsServer != nil {
			s.dnsServer.InvalidateZoneCache()
		}
		c.Status(http.StatusNoContent)
	case "bind":
		if err := zoneio.ImportBIND(s.db, &z, c.Request.Body, mode, s.cfg.DefaultTTL); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		dbm.BumpSOASerialAuto(s.db, z, s.cfg.SOA.AutoOnMissing, s.cfg.SOA.Primary, s.cfg.SOA.Hostmaster)
		// Invalidate DNS cache after zone import
		if s.dnsServer != nil {
			s.dnsServer.InvalidateZoneCache()
		}
		c.Status(http.StatusNoContent)
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported format"})
	}
}

func (r rrsetReq) recordsNormalized() []dbm.RData {
	out := make([]dbm.RData, 0, len(r.Records))
	for _, x := range r.Records {
		rr := dbm.RData{Data: strings.TrimSpace(x.Data)}
		rr.Country = normalizePtr(x.Country)
		rr.Continent = normalizePtr(x.Continent)
		rr.ASN = x.ASN
		rr.Subnet = normalizePtr(x.Subnet)
		out = append(out, rr)
	}
	return out
}

func normalizePtr[T ~string](p *T) *string {
	if p == nil {
		return nil
	}
	s := strings.TrimSpace(string(*p))
	if s == "" {
		return nil
	}
	lower := strings.ToUpper(s)
	return &lower
}

// stripTimestamps removes created/updated/deleted fields from imported JSON payloads.
func stripTimestamps(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			lk := strings.ToLower(k)
			if lk == "created_at" || lk == "updated_at" || lk == "deleted_at" {
				continue
			}
			out[k] = stripTimestamps(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = stripTimestamps(val)
		}
		return out
	default:
		return v
	}
}

// Sync structures for replication
type SyncData struct {
	Zones     []dbm.Zone     `json:"zones"`
	Templates []dbm.Template `json:"templates"`
}

// syncExport returns all zones and templates for replication
func (s *Server) syncExport(c *gin.Context) {
	var zones []dbm.Zone
	if err := s.db.Preload("RRSets.Records").Find(&zones).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	var templates []dbm.Template
	if err := s.db.Preload("Records").Find(&templates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, SyncData{
		Zones:     zones,
		Templates: templates,
	})
}

// syncImport imports all zones and templates from master
func (s *Server) syncImport(c *gin.Context) {
	var data SyncData
	if err := c.ShouldBindJSON(&data); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload"})
		return
	}

	err := s.db.Transaction(func(tx *gorm.DB) error {
		// Import zones
		for _, zone := range data.Zones {
			var existingZone dbm.Zone
			err := tx.Where("name = ?", zone.Name).First(&existingZone).Error

			if err == gorm.ErrRecordNotFound {
				// Create new zone
				newZone := dbm.Zone{
					Name: zone.Name,
				}
				if err := tx.Create(&newZone).Error; err != nil {
					return fmt.Errorf("create zone %s: %w", zone.Name, err)
				}
				existingZone = newZone
			} else if err != nil {
				return fmt.Errorf("check zone %s: %w", zone.Name, err)
			}

			// Delete old rrsets and their records for this zone (hard delete, not soft delete)
			// First, get all rrset IDs for this zone
			var rrsetIDs []uint
			if err := tx.Model(&dbm.RRSet{}).Where("zone_id = ?", existingZone.ID).Pluck("id", &rrsetIDs).Error; err != nil {
				return fmt.Errorf("get rrset ids for zone %s: %w", zone.Name, err)
			}
			// Delete all r_data records associated with these rrsets
			if len(rrsetIDs) > 0 {
				if err := tx.Unscoped().Where("rr_set_id IN ?", rrsetIDs).Delete(&dbm.RData{}).Error; err != nil {
					return fmt.Errorf("delete old records for zone %s: %w", zone.Name, err)
				}
			}
			// Now delete the rrsets themselves
			if err := tx.Unscoped().Where("zone_id = ?", existingZone.ID).Delete(&dbm.RRSet{}).Error; err != nil {
				return fmt.Errorf("delete old rrsets for zone %s: %w", zone.Name, err)
			}

			// Create new rrsets
			for _, rrset := range zone.RRSets {
				newRRSet := dbm.RRSet{
					ZoneID:  existingZone.ID,
					Name:    rrset.Name,
					Type:    rrset.Type,
					TTL:     rrset.TTL,
					Records: rrset.Records,
				}
				// Clear IDs to avoid conflicts
				for i := range newRRSet.Records {
					newRRSet.Records[i].ID = 0
				}
				if err := tx.Create(&newRRSet).Error; err != nil {
					return fmt.Errorf("create rrset %s/%s: %w", zone.Name, rrset.Name, err)
				}
			}
		}

		// Import templates
		for _, tmpl := range data.Templates {
			var existingTmpl dbm.Template
			err := tx.Where("name = ?", tmpl.Name).First(&existingTmpl).Error

			if err == gorm.ErrRecordNotFound {
				// Create new template
				newTmpl := dbm.Template{
					Name:        tmpl.Name,
					Description: tmpl.Description,
				}
				if err := tx.Create(&newTmpl).Error; err != nil {
					return fmt.Errorf("create template %s: %w", tmpl.Name, err)
				}
				existingTmpl = newTmpl
			} else if err != nil {
				return fmt.Errorf("check template %s: %w", tmpl.Name, err)
			} else {
				// Update existing template
				existingTmpl.Description = tmpl.Description
				if err := tx.Save(&existingTmpl).Error; err != nil {
					return fmt.Errorf("update template %s: %w", tmpl.Name, err)
				}
			}

			// Delete old template records (hard delete, not soft delete)
			if err := tx.Unscoped().Where("template_id = ?", existingTmpl.ID).Delete(&dbm.TemplateRecord{}).Error; err != nil {
				return fmt.Errorf("delete old records for template %s: %w", tmpl.Name, err)
			}

			// Create new template records
			for _, rec := range tmpl.Records {
				newRec := dbm.TemplateRecord{
					TemplateID: existingTmpl.ID,
					Name:       rec.Name,
					Type:       rec.Type,
					TTL:        rec.TTL,
					Data:       rec.Data,
					Country:    rec.Country,
					Continent:  rec.Continent,
					ASN:        rec.ASN,
					Subnet:     rec.Subnet,
				}
				if err := tx.Create(&newRec).Error; err != nil {
					return fmt.Errorf("create template record for %s: %w", tmpl.Name, err)
				}
			}
		}

		return nil
	})

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Invalidate DNS cache after sync import
	if s.dnsServer != nil {
		s.dnsServer.InvalidateZoneCache()
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok", "zones": len(data.Zones), "templates": len(data.Templates)})
}
