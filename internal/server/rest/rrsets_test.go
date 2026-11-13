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

func setupRRSetTestServer(t *testing.T) (*Server, *gorm.DB, uint) {
	t.Helper()

cfg := &config.Config{
	APIToken:         "testtoken",
	DefaultTTL:       300,
	SOA:              config.SOAConfig{AutoOnMissing: true},
}

	gormDB := openTestDB(t)

	if err := db.AutoMigrate(gormDB); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Create a test zone
	zone := db.Zone{Name: "test.com"}
	if err := gormDB.Create(&zone).Error; err != nil {
		t.Fatalf("create zone: %v", err)
	}

	mockDNS := &mockDNSServer{}
	server := NewServer(cfg, gormDB, mockDNS)

	return server, gormDB, zone.ID
}

// openTestDB opens an in-memory SQLite database with foreign keys enabled,
// as production DSNs do (_foreign_keys=on), so FK violations surface in tests.
func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	gormDB, err := gorm.Open(sqlite.Open("file::memory:?_foreign_keys=on"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	sqlDB, err := gormDB.DB()
	if err != nil {
		t.Fatalf("sql db: %v", err)
	}
	sqlDB.SetMaxOpenConns(1) // one connection = one in-memory database
	var fk int
	if err := gormDB.Raw("PRAGMA foreign_keys").Scan(&fk).Error; err != nil || fk != 1 {
		t.Fatalf("foreign keys not enabled (fk=%d, err=%v)", fk, err)
	}
	return gormDB
}

func TestCreateRRSet(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name           string
		zoneID         string
		payload        string
		expectedStatus int
		expectedError  string
		validateResult func(*testing.T, *db.RRSet)
		description    string
	}{
		{
			name:           "create A record",
			zoneID:         "1",
			payload:        `{"name":"www","type":"A","ttl":300,"records":[{"data":"192.0.2.1"}]}`,
			expectedStatus: http.StatusCreated,
			validateResult: func(t *testing.T, rr *db.RRSet) {
				if rr.Name != "www.test.com." {
					t.Errorf("Expected FQDN 'www.test.com.', got '%s'", rr.Name)
				}
				if rr.Type != "A" {
					t.Errorf("Expected type 'A', got '%s'", rr.Type)
				}
				if len(rr.Records) != 1 {
					t.Errorf("Expected 1 record, got %d", len(rr.Records))
				}
			},
			description: "Should create A record with FQDN",
		},
		{
			name:           "reject invalid A data",
			zoneID:         "1",
			payload:        `{"name":"bad","type":"A","ttl":300,"records":[{"data":"not-an-ip"}]}`,
			expectedStatus: http.StatusBadRequest,
			description:    "Should reject data that is not a valid IPv4 address",
		},
		{
			name:           "create AAAA record",
			zoneID:         "1",
			payload:        `{"name":"ipv6","type":"AAAA","ttl":300,"records":[{"data":"2001:db8::1"}]}`,
			expectedStatus: http.StatusCreated,
			description:    "Should create AAAA record",
		},
		{
			name:           "create CNAME record",
			zoneID:         "1",
			payload:        `{"name":"alias","type":"CNAME","ttl":300,"records":[{"data":"target.example.com."}]}`,
			expectedStatus: http.StatusCreated,
			description:    "Should create CNAME record",
		},
		{
			name:           "create CNAME with @ shorthand",
			zoneID:         "1",
			payload:        `{"name":"apex-alias","type":"CNAME","ttl":300,"records":[{"data":"@"}]}`,
			expectedStatus: http.StatusCreated,
			validateResult: func(t *testing.T, rr *db.RRSet) {
				if len(rr.Records) == 0 {
					t.Fatal("No records created")
				}
				if rr.Records[0].Data != "test.com." {
					t.Errorf("Expected CNAME data 'test.com.', got '%s'", rr.Records[0].Data)
				}
			},
			description: "Should expand @ to zone apex FQDN",
		},
		{
			name:           "create MX record",
			zoneID:         "1",
			payload:        `{"name":"@","type":"MX","ttl":300,"records":[{"data":"10 mail.test.com."}]}`,
			expectedStatus: http.StatusCreated,
			validateResult: func(t *testing.T, rr *db.RRSet) {
				if rr.Name != "test.com." {
					t.Errorf("Expected apex FQDN 'test.com.', got '%s'", rr.Name)
				}
			},
			description: "Should handle @ as zone apex",
		},
		{
			name:           "create TXT record",
			zoneID:         "1",
			payload:        `{"name":"_dmarc","type":"TXT","ttl":300,"records":[{"data":"v=DMARC1; p=none"}]}`,
			expectedStatus: http.StatusCreated,
			description:    "Should create TXT record",
		},
		{
			name:           "create record with multiple RData",
			zoneID:         "1",
			payload:        `{"name":"multi","type":"A","ttl":300,"records":[{"data":"192.0.2.1"},{"data":"192.0.2.2"}]}`,
			expectedStatus: http.StatusCreated,
			validateResult: func(t *testing.T, rr *db.RRSet) {
				if len(rr.Records) != 2 {
					t.Errorf("Expected 2 records, got %d", len(rr.Records))
				}
			},
			description: "Should create RRSet with multiple records",
		},
		{
			name:           "create geo-aware record with country",
			zoneID:         "1",
			payload:        `{"name":"geo","type":"A","ttl":300,"records":[{"data":"192.0.2.1","country":"US"}]}`,
			expectedStatus: http.StatusCreated,
			description:    "Should create record with country selector",
		},
		{
			name:           "create geo-aware record with continent",
			zoneID:         "1",
			payload:        `{"name":"geo2","type":"A","ttl":300,"records":[{"data":"192.0.2.2","continent":"EU"}]}`,
			expectedStatus: http.StatusCreated,
			description:    "Should create record with continent selector",
		},
		{
			name:           "create geo-aware record with ASN",
			zoneID:         "1",
			payload:        `{"name":"geo3","type":"A","ttl":300,"records":[{"data":"192.0.2.3","asn":15169}]}`,
			expectedStatus: http.StatusCreated,
			description:    "Should create record with ASN selector",
		},
		{
			name:           "create geo-aware record with subnet",
			zoneID:         "1",
			payload:        `{"name":"geo4","type":"A","ttl":300,"records":[{"data":"192.0.2.4","subnet":"8.8.8.0/24"}]}`,
			expectedStatus: http.StatusCreated,
			description:    "Should create record with subnet selector",
		},
		{
			name:           "use default TTL when not specified",
			zoneID:         "1",
			payload:        `{"name":"default-ttl","type":"A","ttl":0,"records":[{"data":"192.0.2.5"}]}`,
			expectedStatus: http.StatusCreated,
			validateResult: func(t *testing.T, rr *db.RRSet) {
				if rr.TTL != 300 {
					t.Errorf("Expected default TTL 300, got %d", rr.TTL)
				}
			},
			description: "Should use default TTL when TTL is 0",
		},
		{
			name:           "invalid zone id",
			zoneID:         "999",
			payload:        `{"name":"test","type":"A","ttl":300,"records":[{"data":"192.0.2.1"}]}`,
			expectedStatus: http.StatusNotFound,
			expectedError:  "zone not found",
			description:    "Should reject non-existent zone",
		},
		{
			name:           "invalid json payload",
			zoneID:         "1",
			payload:        `{"name":}`,
			expectedStatus: http.StatusBadRequest,
			expectedError:  "invalid payload",
			description:    "Should reject malformed JSON",
		},
		{
			name:           "empty records array",
			zoneID:         "1",
			payload:        `{"name":"empty","type":"A","ttl":300,"records":[]}`,
			expectedStatus: http.StatusCreated,
			description:    "Should allow empty records array",
		},
		{
			name:           "fully qualified name without trailing dot",
			zoneID:         "1",
			payload:        `{"name":"_dmarc.test.com","type":"TXT","ttl":300,"records":[{"data":"\"v=DMARC1; p=none\""}]}`,
			expectedStatus: http.StatusCreated,
			validateResult: func(t *testing.T, rr *db.RRSet) {
				if rr.Name != "_dmarc.test.com." {
					t.Errorf("Expected '_dmarc.test.com.', got '%s'", rr.Name)
				}
			},
			description: "Should not append the zone to a name that already ends with it",
		},
		{
			name:           "absolute apex name",
			zoneID:         "1",
			payload:        `{"name":"test.com.","type":"TXT","ttl":300,"records":[{"data":"\"v=spf1 -all\""}]}`,
			expectedStatus: http.StatusCreated,
			validateResult: func(t *testing.T, rr *db.RRSet) {
				if rr.Name != "test.com." {
					t.Errorf("Expected 'test.com.', got '%s'", rr.Name)
				}
			},
			description: "Should keep an absolute name as is",
		},
		{
			name:           "absolute name outside zone",
			zoneID:         "1",
			payload:        `{"name":"other.org.","type":"A","ttl":300,"records":[{"data":"192.0.2.1"}]}`,
			expectedStatus: http.StatusBadRequest,
			expectedError:  `record name "other.org." does not belong to zone "test.com"`,
			description:    "Should reject a name that does not belong to the zone",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, gormDB, zoneID := setupRRSetTestServer(t)

			// Use actual zone ID
			requestZoneID := tt.zoneID
			if tt.zoneID == "1" {
				requestZoneID = strconv.FormatUint(uint64(zoneID), 10)
			}

			req := httptest.NewRequest("POST", "/zones/"+requestZoneID+"/rrsets", bytes.NewBufferString(tt.payload))
			req.Header.Set("Authorization", "Bearer testtoken")
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			server.r.ServeHTTP(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("%s\nExpected status %d, got %d\nBody: %s",
					tt.description, tt.expectedStatus, w.Code, w.Body.String())
			}

			if tt.expectedError != "" {
				var response map[string]interface{}
				if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
					t.Errorf("Failed to parse JSON response: %v", err)
				}
				if response["error"] != tt.expectedError {
					t.Errorf("Expected error '%s', got '%v'", tt.expectedError, response["error"])
				}
			}

			if tt.validateResult != nil && w.Code == http.StatusCreated {
				var rrset db.RRSet
				if err := json.Unmarshal(w.Body.Bytes(), &rrset); err != nil {
					t.Fatalf("Failed to parse response: %v", err)
				}

				// Reload from DB to get all fields
				var dbRRSet db.RRSet
				if err := gormDB.Preload("Records").First(&dbRRSet, rrset.ID).Error; err != nil {
					t.Fatalf("Failed to load RRSet from DB: %v", err)
				}

				tt.validateResult(t, &dbRRSet)
			}
		})
	}
}

func TestListRRSets(t *testing.T) {
	gin.SetMode(gin.TestMode)

	server, gormDB, zoneID := setupRRSetTestServer(t)

	// Create multiple RRSets
	rrsets := []db.RRSet{
		{ZoneID: zoneID, Name: "www.test.com.", Type: "A", TTL: 300},
		{ZoneID: zoneID, Name: "mail.test.com.", Type: "A", TTL: 300},
		{ZoneID: zoneID, Name: "test.com.", Type: "MX", TTL: 300},
	}

	for _, rr := range rrsets {
		if err := gormDB.Create(&rr).Error; err != nil {
			t.Fatalf("Failed to create rrset: %v", err)
		}
	}

	req := httptest.NewRequest("GET", "/zones/"+strconv.FormatUint(uint64(zoneID), 10)+"/rrsets", nil)
	req.Header.Set("Authorization", "Bearer testtoken")
	w := httptest.NewRecorder()

	server.r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status %d, got %d", http.StatusOK, w.Code)
	}

	var result []db.RRSet
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if len(result) != 3 {
		t.Errorf("Expected 3 RRSets, got %d", len(result))
	}
}

func TestUpdateRRSet(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name           string
		initialType    string
		initialData    string
		updatePayload  string
		expectedStatus int
		validateResult func(*testing.T, *db.RRSet)
		description    string
	}{
		{
			name:           "update A record data",
			initialType:    "A",
			initialData:    "192.0.2.1",
			updatePayload:  `{"name":"www","type":"A","ttl":600,"records":[{"data":"192.0.2.2"}]}`,
			expectedStatus: http.StatusOK,
			validateResult: func(t *testing.T, rr *db.RRSet) {
				if rr.TTL != 600 {
					t.Errorf("Expected TTL 600, got %d", rr.TTL)
				}
				if len(rr.Records) != 1 {
					t.Fatalf("Expected 1 record, got %d", len(rr.Records))
				}
				if rr.Records[0].Data != "192.0.2.2" {
					t.Errorf("Expected data '192.0.2.2', got '%s'", rr.Records[0].Data)
				}
			},
			description: "Should update record data and TTL",
		},
		{
			name:           "change record type",
			initialType:    "A",
			initialData:    "192.0.2.1",
			updatePayload:  `{"name":"www","type":"AAAA","ttl":300,"records":[{"data":"2001:db8::1"}]}`,
			expectedStatus: http.StatusOK,
			validateResult: func(t *testing.T, rr *db.RRSet) {
				if rr.Type != "AAAA" {
					t.Errorf("Expected type 'AAAA', got '%s'", rr.Type)
				}
			},
			description: "Should allow changing record type",
		},
		{
			name:           "replace single with multiple records",
			initialType:    "A",
			initialData:    "192.0.2.1",
			updatePayload:  `{"name":"www","type":"A","ttl":300,"records":[{"data":"192.0.2.2"},{"data":"192.0.2.3"}]}`,
			expectedStatus: http.StatusOK,
			validateResult: func(t *testing.T, rr *db.RRSet) {
				if len(rr.Records) != 2 {
					t.Errorf("Expected 2 records, got %d", len(rr.Records))
				}
			},
			description: "Should replace records completely",
		},
		{
			name:           "update CNAME with @ shorthand",
			initialType:    "CNAME",
			initialData:    "old.example.com.",
			updatePayload:  `{"name":"alias","type":"CNAME","ttl":300,"records":[{"data":"@"}]}`,
			expectedStatus: http.StatusOK,
			validateResult: func(t *testing.T, rr *db.RRSet) {
				if len(rr.Records) == 0 {
					t.Fatal("No records found")
				}
				if rr.Records[0].Data != "test.com." {
					t.Errorf("Expected CNAME data 'test.com.', got '%s'", rr.Records[0].Data)
				}
			},
			description: "Should expand @ in CNAME updates",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, gormDB, zoneID := setupRRSetTestServer(t)

			// Create initial RRSet
			rrset := db.RRSet{
				ZoneID: zoneID,
				Name:   "www.test.com.",
				Type:   tt.initialType,
				TTL:    300,
				Records: []db.RData{
					{Data: tt.initialData},
				},
			}
			if err := gormDB.Create(&rrset).Error; err != nil {
				t.Fatalf("Failed to create initial rrset: %v", err)
			}

			req := httptest.NewRequest("PUT",
				"/zones/"+strconv.FormatUint(uint64(zoneID), 10)+"/rrsets/"+strconv.FormatUint(uint64(rrset.ID), 10),
				bytes.NewBufferString(tt.updatePayload))
			req.Header.Set("Authorization", "Bearer testtoken")
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			server.r.ServeHTTP(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("%s\nExpected status %d, got %d\nBody: %s",
					tt.description, tt.expectedStatus, w.Code, w.Body.String())
			}

			if tt.validateResult != nil {
				var updated db.RRSet
				if err := gormDB.Preload("Records").First(&updated, rrset.ID).Error; err != nil {
					t.Fatalf("Failed to load updated RRSet: %v", err)
				}
				tt.validateResult(t, &updated)
			}
		})
	}
}

func TestUpdateRRSet_NotFound(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name        string
		zoneID      string
		rrsetID     string
		expectedErr string
		description string
	}{
		{
			name:        "non-existent zone",
			zoneID:      "999",
			rrsetID:     "1",
			expectedErr: "zone not found",
			description: "Should return 404 for non-existent zone",
		},
		{
			name:        "non-existent rrset",
			zoneID:      "1",
			rrsetID:     "999",
			expectedErr: "rrset not found",
			description: "Should return 404 for non-existent RRSet",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, _, zoneID := setupRRSetTestServer(t)

			requestZoneID := tt.zoneID
			if tt.zoneID == "1" {
				requestZoneID = strconv.FormatUint(uint64(zoneID), 10)
			}

			req := httptest.NewRequest("PUT",
				"/zones/"+requestZoneID+"/rrsets/"+tt.rrsetID,
				bytes.NewBufferString(`{"name":"test","type":"A","ttl":300,"records":[{"data":"192.0.2.1"}]}`))
			req.Header.Set("Authorization", "Bearer testtoken")
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			server.r.ServeHTTP(w, req)

			if w.Code != http.StatusNotFound {
				t.Errorf("%s\nExpected status %d, got %d", tt.description, http.StatusNotFound, w.Code)
			}

			var response map[string]interface{}
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatalf("Failed to parse response: %v", err)
			}
			if response["error"] != tt.expectedErr {
				t.Errorf("Expected error '%s', got '%v'", tt.expectedErr, response["error"])
			}
		})
	}
}

func TestPatchRRSet(t *testing.T) {
	gin.SetMode(gin.TestMode)

	server, gormDB, zoneID := setupRRSetTestServer(t)

	// Create initial RRSet
	rrset := db.RRSet{
		ZoneID: zoneID,
		Name:   "www.test.com.",
		Type:   "A",
		TTL:    300,
		Records: []db.RData{
			{Data: "192.0.2.1"},
		},
	}
	if err := gormDB.Create(&rrset).Error; err != nil {
		t.Fatalf("Failed to create rrset: %v", err)
	}

	// PATCH should behave the same as PUT
	req := httptest.NewRequest("PATCH",
		"/zones/"+strconv.FormatUint(uint64(zoneID), 10)+"/rrsets/"+strconv.FormatUint(uint64(rrset.ID), 10),
		bytes.NewBufferString(`{"name":"www","type":"A","ttl":600,"records":[{"data":"192.0.2.2"}]}`))
	req.Header.Set("Authorization", "Bearer testtoken")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	server.r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status %d, got %d", http.StatusOK, w.Code)
	}

	var updated db.RRSet
	if err := gormDB.Preload("Records").First(&updated, rrset.ID).Error; err != nil {
		t.Fatalf("Failed to load updated RRSet: %v", err)
	}

	if updated.TTL != 600 {
		t.Errorf("Expected TTL 600, got %d", updated.TTL)
	}
}

func TestDeleteRRSet(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name           string
		setupRRSet     bool
		zoneID         string
		rrsetID        string
		expectedStatus int
		description    string
	}{
		{
			name:           "delete existing rrset",
			setupRRSet:     true,
			expectedStatus: http.StatusNoContent,
			description:    "Should delete RRSet successfully",
		},
		{
			name:           "delete non-existent rrset",
			setupRRSet:     false,
			zoneID:         "1",
			rrsetID:        "999",
			expectedStatus: http.StatusNoContent,
			description:    "Should return 204 even for non-existent RRSet",
		},
		{
			name:           "delete from non-existent zone",
			setupRRSet:     false,
			zoneID:         "999",
			rrsetID:        "1",
			expectedStatus: http.StatusNotFound,
			description:    "Should return 404 for non-existent zone",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, gormDB, zoneID := setupRRSetTestServer(t)

			var rrsetID uint
			if tt.setupRRSet {
				rrset := db.RRSet{
					ZoneID: zoneID,
					Name:   "delete.test.com.",
					Type:   "A",
					TTL:    300,
				}
				if err := gormDB.Create(&rrset).Error; err != nil {
					t.Fatalf("Failed to create rrset: %v", err)
				}
				rrsetID = rrset.ID
			}

			requestZoneID := tt.zoneID
			requestRRSetID := tt.rrsetID
			if tt.setupRRSet {
				requestZoneID = strconv.FormatUint(uint64(zoneID), 10)
				requestRRSetID = strconv.FormatUint(uint64(rrsetID), 10)
			} else if tt.zoneID == "1" {
				requestZoneID = strconv.FormatUint(uint64(zoneID), 10)
			}

			req := httptest.NewRequest("DELETE", "/zones/"+requestZoneID+"/rrsets/"+requestRRSetID, nil)
			req.Header.Set("Authorization", "Bearer testtoken")
			w := httptest.NewRecorder()

			server.r.ServeHTTP(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("%s\nExpected status %d, got %d\nBody: %s",
					tt.description, tt.expectedStatus, w.Code, w.Body.String())
			}

			// Verify deletion
			if tt.setupRRSet && tt.expectedStatus == http.StatusNoContent {
				var deleted db.RRSet
				err := gormDB.First(&deleted, rrsetID).Error
				if err == nil {
					t.Error("RRSet should be deleted but still exists")
				}
			}
		})
	}
}

func TestDeleteRRSet_WithRecords(t *testing.T) {
	gin.SetMode(gin.TestMode)
	server, gormDB, zoneID := setupRRSetTestServer(t)
	mock := server.dnsServer.(*mockDNSServer)

	set := db.RRSet{
		ZoneID:  zoneID,
		Name:    "www.test.com.",
		Type:    "A",
		TTL:     300,
		Records: []db.RData{{Data: "192.0.2.1"}, {Data: "192.0.2.2"}},
	}
	if err := gormDB.Create(&set).Error; err != nil {
		t.Fatalf("create rrset: %v", err)
	}

	w := doRRSetRequest(server, "DELETE", zoneID, set.ID, "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}
	var n int64
	gormDB.Model(&db.RRSet{}).Where("id = ?", set.ID).Count(&n)
	if n != 0 {
		t.Errorf("rrset still exists")
	}
	gormDB.Model(&db.RData{}).Where("rr_set_id = ?", set.ID).Count(&n)
	if n != 0 {
		t.Errorf("%d records of the deleted rrset remain", n)
	}
	if !mock.invalidateCalled {
		t.Errorf("DNS cache was not invalidated after delete")
	}
}

func TestDeleteRRSet_Missing_KeepsSOAAndCache(t *testing.T) {
	gin.SetMode(gin.TestMode)
	server, gormDB, zoneID := setupRRSetTestServer(t)
	mock := server.dnsServer.(*mockDNSServer)

	const soaData = "ns1.test.com. hostmaster.test.com. 100 7200 3600 1209600 300"
	soa := db.RRSet{ZoneID: zoneID, Name: "test.com.", Type: "SOA", TTL: 300, Records: []db.RData{{Data: soaData}}}
	if err := gormDB.Create(&soa).Error; err != nil {
		t.Fatalf("create soa: %v", err)
	}

	w := doRRSetRequest(server, "DELETE", zoneID, 99999, "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}
	var rd db.RData
	gormDB.Where("rr_set_id = ?", soa.ID).First(&rd)
	if rd.Data != soaData {
		t.Errorf("SOA changed on a no-op delete: %q", rd.Data)
	}
	if mock.invalidateCalled {
		t.Errorf("DNS cache invalidated on a no-op delete")
	}
}

func TestUpdateRRSet_NameRules(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for _, method := range []string{"PUT", "PATCH"} {
		t.Run(method, func(t *testing.T) {
			server, gormDB, zoneID := setupRRSetTestServer(t)
			txt := func(name string) db.RRSet {
				rs := db.RRSet{ZoneID: zoneID, Name: name, Type: "TXT", TTL: 300, Records: []db.RData{{Data: `"x"`}}}
				if err := gormDB.Create(&rs).Error; err != nil {
					t.Fatalf("create rrset: %v", err)
				}
				return rs
			}
			dmarc := txt("_dmarc.test.com.")
			other := txt("other.test.com.")

			// fully qualified name without the trailing dot is not doubled
			w := doRRSetRequest(server, method, zoneID, other.ID, `{"name":"spf.test.com","type":"TXT","records":[{"data":"\"v=spf1 -all\""}]}`)
			if w.Code != http.StatusOK {
				t.Fatalf("rename: expected 200, got %d: %s", w.Code, w.Body.String())
			}
			var got db.RRSet
			gormDB.First(&got, other.ID)
			if got.Name != "spf.test.com." {
				t.Errorf("expected 'spf.test.com.', got %q", got.Name)
			}

			// name outside the zone
			w = doRRSetRequest(server, method, zoneID, other.ID, `{"name":"other.org.","type":"TXT","records":[{"data":"\"x\""}]}`)
			if w.Code != http.StatusBadRequest {
				t.Errorf("outside zone: expected 400, got %d: %s", w.Code, w.Body.String())
			}

			// renaming onto an existing name+type is a conflict, not a DB error
			w = doRRSetRequest(server, method, zoneID, other.ID, `{"name":"_dmarc","type":"TXT","records":[{"data":"\"x\""}]}`)
			if w.Code != http.StatusConflict {
				t.Fatalf("conflict: expected 409, got %d: %s", w.Code, w.Body.String())
			}
			var resp map[string]interface{}
			_ = json.Unmarshal(w.Body.Bytes(), &resp)
			if resp["existing_id"] != float64(dmarc.ID) {
				t.Errorf("expected existing_id %d, got %v", dmarc.ID, resp["existing_id"])
			}

			// keeping its own name is not a conflict
			w = doRRSetRequest(server, method, zoneID, dmarc.ID, `{"name":"_dmarc.test.com.","type":"TXT","records":[{"data":"\"v=DMARC1; p=none\""}]}`)
			if w.Code != http.StatusOK {
				t.Errorf("same name: expected 200, got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

func doRRSetRequest(server *Server, method string, zoneID, rrsetID uint, body string) *httptest.ResponseRecorder {
	url := "/zones/" + strconv.FormatUint(uint64(zoneID), 10) + "/rrsets/" + strconv.FormatUint(uint64(rrsetID), 10)
	req := httptest.NewRequest(method, url, bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer testtoken")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	server.r.ServeHTTP(w, req)
	return w
}

func TestHealth_HEAD(t *testing.T) {
	gin.SetMode(gin.TestMode)
	server, _, _ := setupRRSetTestServer(t)

	req := httptest.NewRequest("HEAD", "/health", nil)
	w := httptest.NewRecorder()
	server.r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("HEAD /health: expected 200, got %d", w.Code)
	}
}

func TestHealth_HEAD_DatabaseDown(t *testing.T) {
	gin.SetMode(gin.TestMode)
	server, gormDB, _ := setupRRSetTestServer(t)
	sqlDB, _ := gormDB.DB()
	_ = sqlDB.Close()

	req := httptest.NewRequest("HEAD", "/health", nil)
	w := httptest.NewRecorder()
	server.r.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("HEAD /health with DB down: expected 503, got %d", w.Code)
	}
}

func TestRRSetOperations_WithoutAuthentication(t *testing.T) {
	gin.SetMode(gin.TestMode)

	server, _, zoneID := setupRRSetTestServer(t)
	zoneIDStr := strconv.FormatUint(uint64(zoneID), 10)

	tests := []struct {
		name           string
		method         string
		path           string
		payload        string
		expectedStatus int
		description    string
	}{
		{
			name:           "create rrset without auth",
			method:         "POST",
			path:           "/zones/" + zoneIDStr + "/rrsets",
			payload:        `{"name":"test","type":"A","ttl":300,"records":[{"data":"192.0.2.1"}]}`,
			expectedStatus: http.StatusUnauthorized,
			description:    "Should reject unauthenticated RRSet creation",
		},
		{
			name:           "list rrsets without auth",
			method:         "GET",
			path:           "/zones/" + zoneIDStr + "/rrsets",
			expectedStatus: http.StatusUnauthorized,
			description:    "Should reject unauthenticated RRSet listing",
		},
		{
			name:           "update rrset without auth",
			method:         "PUT",
			path:           "/zones/" + zoneIDStr + "/rrsets/1",
			payload:        `{"name":"test","type":"A","ttl":300,"records":[{"data":"192.0.2.1"}]}`,
			expectedStatus: http.StatusUnauthorized,
			description:    "Should reject unauthenticated RRSet update",
		},
		{
			name:           "delete rrset without auth",
			method:         "DELETE",
			path:           "/zones/" + zoneIDStr + "/rrsets/1",
			expectedStatus: http.StatusUnauthorized,
			description:    "Should reject unauthenticated RRSet deletion",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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
