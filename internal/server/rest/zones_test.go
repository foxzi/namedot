package rest

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"namedot/internal/config"
	"namedot/internal/db"
)

func setupZoneTestServer(t *testing.T, cfg *config.Config) (*Server, *gorm.DB, *mockDNSServer) {
	t.Helper()

	gormDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	if err := db.AutoMigrate(gormDB); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	mockDNS := &mockDNSServer{}
	server := NewServer(cfg, gormDB, mockDNS)

	return server, gormDB, mockDNS
}

func TestCreateZone(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name           string
		payload        string
		expectedStatus int
		expectedError  string
		checkCache     bool
		description    string
	}{
		{
			name:           "valid zone creation",
			payload:        `{"name":"example.com"}`,
			expectedStatus: http.StatusCreated,
			checkCache:     true,
			description:    "Should create zone and invalidate cache",
		},
		{
			name:           "zone name normalization",
			payload:        `{"name":"EXAMPLE.ORG"}`,
			expectedStatus: http.StatusCreated,
			checkCache:     true,
			description:    "Should normalize uppercase to lowercase",
		},
		{
			name:           "missing name field",
			payload:        `{"name":""}`,
			expectedStatus: http.StatusBadRequest,
			expectedError:  "invalid payload",
			description:    "Empty name should be rejected",
		},
		{
			name:           "invalid json",
			payload:        `{"name":}`,
			expectedStatus: http.StatusBadRequest,
			expectedError:  "invalid payload",
			description:    "Malformed JSON should be rejected",
		},
		{
			name:           "missing json body",
			payload:        ``,
			expectedStatus: http.StatusBadRequest,
			expectedError:  "invalid payload",
			description:    "Empty payload should be rejected",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{APIToken: "testtoken"}
			server, gormDB, mockDNS := setupZoneTestServer(t, cfg)

			req := httptest.NewRequest("POST", "/zones", bytes.NewBufferString(tt.payload))
			req.Header.Set("Authorization", "Bearer testtoken")
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			server.r.ServeHTTP(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("%s\nExpected status %d, got %d\nBody: %s",
					tt.description, tt.expectedStatus, w.Code, w.Body.String())
			}

			// Check error message if expected
			if tt.expectedError != "" {
				var response map[string]interface{}
				if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
					t.Errorf("Failed to parse JSON response: %v", err)
				}
				if response["error"] != tt.expectedError {
					t.Errorf("Expected error '%s', got '%v'", tt.expectedError, response["error"])
				}
			}

			// Check cache invalidation
			if tt.checkCache && !mockDNS.invalidateCalled {
				t.Error("Expected DNS cache invalidation, but it was not called")
			}

			// Verify zone was created with normalized name
			if w.Code == http.StatusCreated {
				var response db.Zone
				if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
					t.Fatalf("Failed to parse response: %v", err)
				}
				if response.ID == 0 {
					t.Error("Expected zone ID > 0")
				}

				// Verify in database
				var zone db.Zone
				if err := gormDB.First(&zone, response.ID).Error; err != nil {
					t.Errorf("Zone not found in database: %v", err)
				}

				// Check name normalization
				if zone.Name != response.Name {
					t.Errorf("Zone name mismatch: expected %s, got %s", response.Name, zone.Name)
				}
			}
		})
	}
}

func TestCreateZone_DuplicateName(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{APIToken: "testtoken"}
	server, gormDB, _ := setupZoneTestServer(t, cfg)

	// Create first zone (with trailing dot - normalized form)
	zone := db.Zone{Name: "duplicate.com."}
	if err := gormDB.Create(&zone).Error; err != nil {
		t.Fatalf("Failed to create initial zone: %v", err)
	}

	// Try to create duplicate (API will normalize to "duplicate.com.")
	req := httptest.NewRequest("POST", "/zones", bytes.NewBufferString(`{"name":"duplicate.com"}`))
	req.Header.Set("Authorization", "Bearer testtoken")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	server.r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status %d for duplicate zone, got %d", http.StatusBadRequest, w.Code)
	}

	var response map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}
	if response["error"] == nil {
		t.Error("Expected error for duplicate zone name")
	}
}

func TestListZones(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name          string
		setupZones    []string
		expectedCount int
		description   string
	}{
		{
			name:          "empty list",
			setupZones:    []string{},
			expectedCount: 0,
			description:   "Should return empty array for no zones",
		},
		{
			name:          "single zone",
			setupZones:    []string{"single.com"},
			expectedCount: 1,
			description:   "Should return one zone",
		},
		{
			name:          "multiple zones",
			setupZones:    []string{"first.com", "second.com", "third.com"},
			expectedCount: 3,
			description:   "Should return all zones",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{APIToken: "testtoken"}
			server, gormDB, _ := setupZoneTestServer(t, cfg)

			// Create zones
			for _, name := range tt.setupZones {
				zone := db.Zone{Name: name}
				if err := gormDB.Create(&zone).Error; err != nil {
					t.Fatalf("Failed to create zone %s: %v", name, err)
				}
			}

			req := httptest.NewRequest("GET", "/zones", nil)
			req.Header.Set("Authorization", "Bearer testtoken")
			w := httptest.NewRecorder()

			server.r.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Errorf("%s\nExpected status %d, got %d", tt.description, http.StatusOK, w.Code)
			}

			var zones []db.Zone
			if err := json.Unmarshal(w.Body.Bytes(), &zones); err != nil {
				t.Fatalf("Failed to parse response: %v", err)
			}

			if len(zones) != tt.expectedCount {
				t.Errorf("%s\nExpected %d zones, got %d", tt.description, tt.expectedCount, len(zones))
			}
		})
	}
}

func TestGetZoneByName(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name           string
		queryName      string
		setupZones     []string
		expectedStatus int
		expectedName   string
		description    string
	}{
		{
			name:           "exact match",
			queryName:      "test.com",
			setupZones:     []string{"test.com."},
			expectedStatus: http.StatusOK,
			expectedName:   "test.com.",
			description:    "Should find zone by exact name",
		},
		{
			name:           "match without trailing dot",
			queryName:      "example.org",
			setupZones:     []string{"example.org."},
			expectedStatus: http.StatusOK,
			expectedName:   "example.org.",
			description:    "Should normalize name and find zone",
		},
		{
			name:           "case insensitive match",
			queryName:      "EXAMPLE.COM",
			setupZones:     []string{"example.com."},
			expectedStatus: http.StatusOK,
			expectedName:   "example.com.",
			description:    "Should match case-insensitively",
		},
		{
			name:           "zone not found",
			queryName:      "notfound.com",
			setupZones:     []string{"other.com."},
			expectedStatus: http.StatusNotFound,
			description:    "Should return 404 for non-existent zone",
		},
		{
			name:           "with rrsets preloaded",
			queryName:      "withrecords.com",
			setupZones:     []string{"withrecords.com."},
			expectedStatus: http.StatusOK,
			expectedName:   "withrecords.com.",
			description:    "Should return zone with RRSets",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{APIToken: "testtoken"}
			server, gormDB, _ := setupZoneTestServer(t, cfg)

			// Create zones
			for _, zoneName := range tt.setupZones {
				zone := db.Zone{Name: zoneName}
				if err := gormDB.Create(&zone).Error; err != nil {
					t.Fatalf("Failed to create zone %s: %v", zoneName, err)
				}

				// Add RRSet for "withrecords" test
				if zoneName == "withrecords.com." {
					rrset := db.RRSet{
						ZoneID: zone.ID,
						Name:   "www.withrecords.com.",
						Type:   "A",
						TTL:    300,
						Records: []db.RData{
							{Data: "192.0.2.1"},
						},
					}
					if err := gormDB.Create(&rrset).Error; err != nil {
						t.Fatalf("Failed to create rrset: %v", err)
					}
				}
			}

			req := httptest.NewRequest("GET", "/zones?name="+tt.queryName, nil)
			req.Header.Set("Authorization", "Bearer testtoken")
			w := httptest.NewRecorder()

			server.r.ServeHTTP(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("%s\nExpected status %d, got %d\nBody: %s",
					tt.description, tt.expectedStatus, w.Code, w.Body.String())
			}

			if tt.expectedStatus == http.StatusOK {
				var zone db.Zone
				if err := json.Unmarshal(w.Body.Bytes(), &zone); err != nil {
					t.Fatalf("Failed to parse response: %v", err)
				}

				if zone.Name != tt.expectedName {
					t.Errorf("Expected zone name %s, got %s", tt.expectedName, zone.Name)
				}

				// Check RRSets are preloaded for withrecords test
				if tt.queryName == "withrecords.com" && len(zone.RRSets) == 0 {
					t.Error("Expected RRSets to be preloaded")
				}
			}

			if tt.expectedStatus == http.StatusNotFound {
				var response map[string]interface{}
				if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
					t.Fatalf("Failed to parse response: %v", err)
				}
				if response["error"] != "zone not found" {
					t.Errorf("Expected error 'zone not found', got %v", response["error"])
				}
			}
		})
	}
}

func TestGetZone(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name           string
		setupZone      bool
		zoneID         string
		expectedStatus int
		checkRRSets    bool
		description    string
	}{
		{
			name:           "existing zone",
			setupZone:      true,
			zoneID:         "1",
			expectedStatus: http.StatusOK,
			checkRRSets:    true,
			description:    "Should return zone with RRSets",
		},
		{
			name:           "non-existent zone",
			setupZone:      false,
			zoneID:         "999",
			expectedStatus: http.StatusNotFound,
			description:    "Should return 404 for non-existent zone",
		},
		{
			name:           "invalid zone id",
			setupZone:      false,
			zoneID:         "invalid",
			expectedStatus: http.StatusNotFound,
			description:    "Should return 404 for invalid zone ID",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{APIToken: "testtoken"}
			server, gormDB, _ := setupZoneTestServer(t, cfg)

			var zoneID uint
			if tt.setupZone {
				zone := db.Zone{Name: "test.com"}
				if err := gormDB.Create(&zone).Error; err != nil {
					t.Fatalf("Failed to create zone: %v", err)
				}
				zoneID = zone.ID

				// Create an RRSet for testing preload
				rrset := db.RRSet{
					ZoneID: zone.ID,
					Name:   "www.test.com.",
					Type:   "A",
					TTL:    300,
				}
				if err := gormDB.Create(&rrset).Error; err != nil {
					t.Fatalf("Failed to create rrset: %v", err)
				}
			}

			// Use actual ID if zone was created, otherwise use test ID
			requestID := tt.zoneID
			if tt.setupZone {
				requestID = itoa(zoneID)
			}

			req := httptest.NewRequest("GET", "/zones/"+requestID, nil)
			req.Header.Set("Authorization", "Bearer testtoken")
			w := httptest.NewRecorder()

			server.r.ServeHTTP(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("%s\nExpected status %d, got %d", tt.description, tt.expectedStatus, w.Code)
			}

			if tt.expectedStatus == http.StatusOK {
				var zone db.Zone
				if err := json.Unmarshal(w.Body.Bytes(), &zone); err != nil {
					t.Fatalf("Failed to parse response: %v", err)
				}

				if zone.ID == 0 {
					t.Error("Expected zone ID > 0")
				}

				if tt.checkRRSets && len(zone.RRSets) == 0 {
					t.Error("Expected RRSets to be preloaded")
				}
			}

			if tt.expectedStatus == http.StatusNotFound {
				var response map[string]interface{}
				if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
					t.Fatalf("Failed to parse response: %v", err)
				}
				if response["error"] != "not found" {
					t.Errorf("Expected error 'not found', got %v", response["error"])
				}
			}
		})
	}
}

func TestDeleteZone(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name           string
		setupZone      bool
		createRRSets   bool
		zoneID         string
		expectedStatus int
		checkCache     bool
		description    string
	}{
		{
			name:           "delete existing zone",
			setupZone:      true,
			createRRSets:   false,
			expectedStatus: http.StatusNoContent,
			checkCache:     true,
			description:    "Should delete zone and invalidate cache",
		},
		{
			name:           "delete zone with rrsets",
			setupZone:      true,
			createRRSets:   true,
			expectedStatus: http.StatusNoContent,
			checkCache:     true,
			description:    "Should cascade delete RRSets",
		},
		{
			name:           "delete non-existent zone",
			setupZone:      false,
			zoneID:         "999",
			expectedStatus: http.StatusNotFound,
			description:    "Should return 404 for non-existent zone",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{APIToken: "testtoken"}
			server, gormDB, mockDNS := setupZoneTestServer(t, cfg)

			var zoneID uint
			if tt.setupZone {
				zone := db.Zone{Name: "delete.com"}
				if err := gormDB.Create(&zone).Error; err != nil {
					t.Fatalf("Failed to create zone: %v", err)
				}
				zoneID = zone.ID

				if tt.createRRSets {
					rrset := db.RRSet{
						ZoneID: zone.ID,
						Name:   "www.delete.com.",
						Type:   "A",
						TTL:    300,
					}
					if err := gormDB.Create(&rrset).Error; err != nil {
						t.Fatalf("Failed to create rrset: %v", err)
					}
				}
			}

			// Use actual ID if zone was created, otherwise use test ID
			requestID := tt.zoneID
			if tt.setupZone {
				requestID = itoa(zoneID)
			}

			req := httptest.NewRequest("DELETE", "/zones/"+requestID, nil)
			req.Header.Set("Authorization", "Bearer testtoken")
			w := httptest.NewRecorder()

			server.r.ServeHTTP(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("%s\nExpected status %d, got %d\nBody: %s",
					tt.description, tt.expectedStatus, w.Code, w.Body.String())
			}

			// Check cache invalidation
			if tt.checkCache && !mockDNS.invalidateCalled {
				t.Error("Expected DNS cache invalidation, but it was not called")
			}

			// Verify zone was deleted from database
			if tt.setupZone && tt.expectedStatus == http.StatusNoContent {
				var zone db.Zone
				err := gormDB.First(&zone, zoneID).Error
				if err == nil {
					t.Error("Zone should be deleted but still exists in database")
				}

				// Verify RRSets were also deleted
				if tt.createRRSets {
					var rrsets []db.RRSet
					if err := gormDB.Where("zone_id = ?", zoneID).Find(&rrsets).Error; err != nil {
						t.Errorf("Error checking rrsets: %v", err)
					}
					if len(rrsets) > 0 {
						t.Errorf("Expected RRSets to be deleted, but found %d", len(rrsets))
					}
				}
			}
		})
	}
}

func TestZoneOperations_WithoutAuthentication(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name           string
		method         string
		path           string
		payload        string
		expectedStatus int
		description    string
	}{
		{
			name:           "create zone without auth",
			method:         "POST",
			path:           "/zones",
			payload:        `{"name":"test.com"}`,
			expectedStatus: http.StatusUnauthorized,
			description:    "Should reject unauthenticated zone creation",
		},
		{
			name:           "list zones without auth",
			method:         "GET",
			path:           "/zones",
			expectedStatus: http.StatusUnauthorized,
			description:    "Should reject unauthenticated zone listing",
		},
		{
			name:           "get zone without auth",
			method:         "GET",
			path:           "/zones/1",
			expectedStatus: http.StatusUnauthorized,
			description:    "Should reject unauthenticated zone retrieval",
		},
		{
			name:           "delete zone without auth",
			method:         "DELETE",
			path:           "/zones/1",
			expectedStatus: http.StatusUnauthorized,
			description:    "Should reject unauthenticated zone deletion",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{APIToken: "testtoken"}
			server, _, _ := setupZoneTestServer(t, cfg)

			var req *http.Request
			if tt.payload != "" {
				req = httptest.NewRequest(tt.method, tt.path, bytes.NewBufferString(tt.payload))
				req.Header.Set("Content-Type", "application/json")
			} else {
				req = httptest.NewRequest(tt.method, tt.path, nil)
			}
			// No Authorization header

			w := httptest.NewRecorder()
			server.r.ServeHTTP(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("%s\nExpected status %d, got %d", tt.description, tt.expectedStatus, w.Code)
			}
		})
	}
}

func itoa(u uint) string {
	return strconv.FormatUint(uint64(u), 10)
}
