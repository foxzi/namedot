package rest

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"namedot/internal/config"
	dbm "namedot/internal/db"
)

func stringPtr(s string) *string {
	return &s
}

func setupTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open in-memory db: %v", err)
	}

	// Auto-migrate tables
	if err := db.AutoMigrate(
		&dbm.Zone{},
		&dbm.RRSet{},
		&dbm.RData{},
		&dbm.Template{},
		&dbm.TemplateRecord{},
	); err != nil {
		t.Fatalf("failed to migrate db: %v", err)
	}

	return db
}

func TestSyncExport(t *testing.T) {
	tests := []struct {
		name           string
		setupData      func(*gorm.DB)
		expectedZones  int
		expectedTmpls  int
		checkZones     func(*testing.T, []dbm.Zone)
		checkTemplates func(*testing.T, []dbm.Template)
	}{
		{
			name: "export empty database",
			setupData: func(db *gorm.DB) {
				// No data
			},
			expectedZones: 0,
			expectedTmpls: 0,
		},
		{
			name: "export single zone with records",
			setupData: func(db *gorm.DB) {
				zone := dbm.Zone{Name: "example.com"}
				db.Create(&zone)

				rrset := dbm.RRSet{
					ZoneID: zone.ID,
					Name:   "@",
					Type:   "A",
					TTL:    300,
				}
				db.Create(&rrset)

				record := dbm.RData{
					RRSetID: rrset.ID,
					Data:    "192.168.1.1",
				}
				db.Create(&record)
			},
			expectedZones: 1,
			expectedTmpls: 0,
			checkZones: func(t *testing.T, zones []dbm.Zone) {
				if len(zones) != 1 {
					t.Fatalf("expected 1 zone, got %d", len(zones))
				}
				// Export normalizes names with trailing dot
				if zones[0].Name != "example.com." {
					t.Errorf("expected zone name 'example.com.', got '%s'", zones[0].Name)
				}
				if len(zones[0].RRSets) != 1 {
					t.Fatalf("expected 1 rrset, got %d", len(zones[0].RRSets))
				}
				if len(zones[0].RRSets[0].Records) != 1 {
					t.Errorf("expected 1 record, got %d", len(zones[0].RRSets[0].Records))
				}
			},
		},
		{
			name: "export multiple zones with multiple records",
			setupData: func(db *gorm.DB) {
				for i := 1; i <= 3; i++ {
					zone := dbm.Zone{Name: "example" + string(rune('0'+i)) + ".com"}
					db.Create(&zone)

					// Create first RRSet with type A
					rrset1 := dbm.RRSet{
						ZoneID: zone.ID,
						Name:   "@",
						Type:   "A",
						TTL:    300,
					}
					db.Create(&rrset1)

					record1 := dbm.RData{
						RRSetID: rrset1.ID,
						Data:    "192.168.1.1",
					}
					db.Create(&record1)

					// Create second RRSet with type MX
					rrset2 := dbm.RRSet{
						ZoneID: zone.ID,
						Name:   "@",
						Type:   "MX",
						TTL:    300,
					}
					db.Create(&rrset2)

					record2 := dbm.RData{
						RRSetID: rrset2.ID,
						Data:    "10 mail.example" + string(rune('0'+i)) + ".com",
					}
					db.Create(&record2)
				}
			},
			expectedZones: 3,
			expectedTmpls: 0,
			checkZones: func(t *testing.T, zones []dbm.Zone) {
				if len(zones) != 3 {
					t.Fatalf("expected 3 zones, got %d", len(zones))
				}
				for _, zone := range zones {
					if len(zone.RRSets) != 2 {
						t.Errorf("expected 2 rrsets for zone %s, got %d", zone.Name, len(zone.RRSets))
					}
				}
			},
		},
		{
			name: "export templates with records",
			setupData: func(db *gorm.DB) {
				tmpl := dbm.Template{
					Name:        "web-server",
					Description: "Web server template",
				}
				db.Create(&tmpl)

				records := []dbm.TemplateRecord{
					{TemplateID: tmpl.ID, Name: "@", Type: "A", TTL: 300, Data: "192.168.1.1"},
					{TemplateID: tmpl.ID, Name: "www", Type: "A", TTL: 300, Data: "192.168.1.2"},
					{TemplateID: tmpl.ID, Name: "@", Type: "MX", TTL: 300, Data: "10 mail.example.com"},
				}
				for _, rec := range records {
					db.Create(&rec)
				}
			},
			expectedZones: 0,
			expectedTmpls: 1,
			checkTemplates: func(t *testing.T, tmpls []dbm.Template) {
				if len(tmpls) != 1 {
					t.Fatalf("expected 1 template, got %d", len(tmpls))
				}
				if tmpls[0].Name != "web-server" {
					t.Errorf("expected template name 'web-server', got '%s'", tmpls[0].Name)
				}
				if len(tmpls[0].Records) != 3 {
					t.Errorf("expected 3 records, got %d", len(tmpls[0].Records))
				}
			},
		},
		{
			name: "export zones and templates together",
			setupData: func(db *gorm.DB) {
				// Create zones
				zone := dbm.Zone{Name: "test.com"}
				db.Create(&zone)

				// Create template
				tmpl := dbm.Template{Name: "basic-template"}
				db.Create(&tmpl)
			},
			expectedZones: 1,
			expectedTmpls: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := setupTestDB(t)
			tt.setupData(db)

			cfg := &config.Config{}
			server := NewServer(cfg, db, &mockDNSServer{})

			req := httptest.NewRequest("GET", "/sync/export", nil)
			w := httptest.NewRecorder()
			server.r.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
			}

			var result SyncData
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatalf("failed to unmarshal response: %v", err)
			}

			if len(result.Zones) != tt.expectedZones {
				t.Errorf("expected %d zones, got %d", tt.expectedZones, len(result.Zones))
			}

			if len(result.Templates) != tt.expectedTmpls {
				t.Errorf("expected %d templates, got %d", tt.expectedTmpls, len(result.Templates))
			}

			if tt.checkZones != nil {
				tt.checkZones(t, result.Zones)
			}

			if tt.checkTemplates != nil {
				tt.checkTemplates(t, result.Templates)
			}
		})
	}
}

func TestSyncImport(t *testing.T) {
	tests := []struct {
		name          string
		setupExisting func(*gorm.DB)
		importData    SyncData
		verify        func(*testing.T, *gorm.DB)
		wantStatus    int
		description   string
	}{
		{
			name: "import new zone",
			setupExisting: func(db *gorm.DB) {
				// No existing data
			},
			importData: SyncData{
				Zones: []dbm.Zone{
					{
						Name: "new.com",
						RRSets: []dbm.RRSet{
							{
								Name: "@",
								Type: "A",
								TTL:  300,
								Records: []dbm.RData{
									{Data: "192.168.1.1"},
								},
							},
						},
					},
				},
			},
			verify: func(t *testing.T, db *gorm.DB) {
				var zones []dbm.Zone
				db.Preload("RRSets.Records").Find(&zones)

				if len(zones) != 1 {
					t.Fatalf("expected 1 zone, got %d", len(zones))
				}
				if zones[0].Name != "new.com." {
					t.Errorf("expected zone name 'new.com.', got '%s'", zones[0].Name)
				}
				if len(zones[0].RRSets) != 1 {
					t.Errorf("expected 1 rrset, got %d", len(zones[0].RRSets))
				}
			},
			wantStatus:  http.StatusOK,
			description: "Should create new zone with records",
		},
		{
			name: "import conflicting zone - replaces old records",
			setupExisting: func(db *gorm.DB) {
				zone := dbm.Zone{Name: "existing.com."}
				db.Create(&zone)

				rrset := dbm.RRSet{
					ZoneID: zone.ID,
					Name:   "@",
					Type:   "A",
					TTL:    300,
				}
				db.Create(&rrset)

				record := dbm.RData{
					RRSetID: rrset.ID,
					Data:    "10.0.0.1", // Old IP
				}
				db.Create(&record)
			},
			importData: SyncData{
				Zones: []dbm.Zone{
					{
						Name: "existing.com",
						RRSets: []dbm.RRSet{
							{
								Name: "@",
								Type: "A",
								TTL:  600, // Different TTL
								Records: []dbm.RData{
									{Data: "192.168.1.1"}, // New IP
								},
							},
						},
					},
				},
			},
			verify: func(t *testing.T, db *gorm.DB) {
				var zones []dbm.Zone
				db.Preload("RRSets.Records").Find(&zones)

				if len(zones) != 1 {
					t.Fatalf("expected 1 zone, got %d", len(zones))
				}

				// Check old records are deleted
				if len(zones[0].RRSets) != 1 {
					t.Errorf("expected 1 rrset (old deleted), got %d", len(zones[0].RRSets))
				}

				if len(zones[0].RRSets[0].Records) != 1 {
					t.Fatalf("expected 1 record, got %d", len(zones[0].RRSets[0].Records))
				}

				// Verify new data
				if zones[0].RRSets[0].Records[0].Data != "192.168.1.1" {
					t.Errorf("expected new IP '192.168.1.1', got '%s'",
						zones[0].RRSets[0].Records[0].Data)
				}

				if zones[0].RRSets[0].TTL != 600 {
					t.Errorf("expected TTL 600, got %d", zones[0].RRSets[0].TTL)
				}

				// Verify old records are deleted
				var oldRecords []dbm.RData
				db.Where("data = ?", "10.0.0.1").Find(&oldRecords)
				if len(oldRecords) != 0 {
					t.Errorf("expected old records to be deleted, found %d", len(oldRecords))
				}
			},
			wantStatus:  http.StatusOK,
			description: "Should replace existing zone records (hard delete old)",
		},
		{
			name: "import new template",
			setupExisting: func(db *gorm.DB) {
				// No existing data
			},
			importData: SyncData{
				Templates: []dbm.Template{
					{
						Name:        "new-template",
						Description: "New template description",
						Records: []dbm.TemplateRecord{
							{Name: "@", Type: "A", TTL: 300, Data: "192.168.1.1"},
							{Name: "www", Type: "A", TTL: 300, Data: "192.168.1.2"},
						},
					},
				},
			},
			verify: func(t *testing.T, db *gorm.DB) {
				var templates []dbm.Template
				db.Preload("Records").Find(&templates)

				if len(templates) != 1 {
					t.Fatalf("expected 1 template, got %d", len(templates))
				}
				if templates[0].Name != "new-template" {
					t.Errorf("expected template name 'new-template', got '%s'", templates[0].Name)
				}
				if len(templates[0].Records) != 2 {
					t.Errorf("expected 2 records, got %d", len(templates[0].Records))
				}
			},
			wantStatus:  http.StatusOK,
			description: "Should create new template with records",
		},
		{
			name: "import conflicting template - updates and replaces records",
			setupExisting: func(db *gorm.DB) {
				tmpl := dbm.Template{
					Name:        "existing-template",
					Description: "Old description",
				}
				db.Create(&tmpl)

				oldRec := dbm.TemplateRecord{
					TemplateID: tmpl.ID,
					Name:       "@",
					Type:       "A",
					TTL:        300,
					Data:       "10.0.0.1", // Old IP
				}
				db.Create(&oldRec)
			},
			importData: SyncData{
				Templates: []dbm.Template{
					{
						Name:        "existing-template",
						Description: "Updated description",
						Records: []dbm.TemplateRecord{
							{Name: "@", Type: "A", TTL: 600, Data: "192.168.1.1"}, // New IP
							{Name: "www", Type: "A", TTL: 600, Data: "192.168.1.2"}, // Additional
						},
					},
				},
			},
			verify: func(t *testing.T, db *gorm.DB) {
				var templates []dbm.Template
				db.Preload("Records").Find(&templates)

				if len(templates) != 1 {
					t.Fatalf("expected 1 template, got %d", len(templates))
				}

				// Check description updated
				if templates[0].Description != "Updated description" {
					t.Errorf("expected description 'Updated description', got '%s'",
						templates[0].Description)
				}

				// Check old records deleted and new ones created
				if len(templates[0].Records) != 2 {
					t.Errorf("expected 2 records (old deleted, new created), got %d",
						len(templates[0].Records))
				}

				// Verify old records are deleted
				var oldRecords []dbm.TemplateRecord
				db.Where("data = ?", "10.0.0.1").Find(&oldRecords)
				if len(oldRecords) != 0 {
					t.Errorf("expected old records to be deleted, found %d", len(oldRecords))
				}
			},
			wantStatus:  http.StatusOK,
			description: "Should update template and replace records (hard delete old)",
		},
		{
			name: "import multiple zones and templates",
			setupExisting: func(db *gorm.DB) {
				// Create existing zone to test conflict
				zone := dbm.Zone{Name: "conflict.com."}
				db.Create(&zone)

				// Create existing template to test update
				tmpl := dbm.Template{Name: "conflict-template"}
				db.Create(&tmpl)
			},
			importData: SyncData{
				Zones: []dbm.Zone{
					{Name: "new1.com", RRSets: []dbm.RRSet{}},
					{Name: "conflict.com", RRSets: []dbm.RRSet{}},
					{Name: "new2.com", RRSets: []dbm.RRSet{}},
				},
				Templates: []dbm.Template{
					{Name: "new-template", Records: []dbm.TemplateRecord{}},
					{Name: "conflict-template", Records: []dbm.TemplateRecord{}},
				},
			},
			verify: func(t *testing.T, db *gorm.DB) {
				var zones []dbm.Zone
				db.Find(&zones)
				if len(zones) != 3 {
					t.Errorf("expected 3 zones, got %d", len(zones))
				}

				var templates []dbm.Template
				db.Find(&templates)
				if len(templates) != 2 {
					t.Errorf("expected 2 templates, got %d", len(templates))
				}
			},
			wantStatus:  http.StatusOK,
			description: "Should handle multiple zones and templates with conflicts",
		},
		{
			name: "import with geo-aware template records",
			setupExisting: func(db *gorm.DB) {
				// No existing data
			},
			importData: SyncData{
				Templates: []dbm.Template{
					{
						Name: "geo-template",
						Records: []dbm.TemplateRecord{
							{Name: "@", Type: "A", TTL: 300, Data: "192.168.1.1", Country: stringPtr("US")},
							{Name: "@", Type: "A", TTL: 300, Data: "192.168.1.2", Country: stringPtr("EU")},
							{Name: "@", Type: "A", TTL: 300, Data: "10.0.0.0/8", Subnet: stringPtr("10.0.0.0/8")},
						},
					},
				},
			},
			verify: func(t *testing.T, db *gorm.DB) {
				var templates []dbm.Template
				db.Preload("Records").Find(&templates)

				if len(templates) != 1 {
					t.Fatalf("expected 1 template, got %d", len(templates))
				}

				if len(templates[0].Records) != 3 {
					t.Fatalf("expected 3 records, got %d", len(templates[0].Records))
				}

				// Verify geo attributes are preserved
				countryRecords := 0
				subnetRecords := 0
				for _, rec := range templates[0].Records {
					if rec.Country != nil && *rec.Country != "" {
						countryRecords++
					}
					if rec.Subnet != nil && *rec.Subnet != "" {
						subnetRecords++
					}
				}

				if countryRecords != 2 {
					t.Errorf("expected 2 country-based records, got %d", countryRecords)
				}
				if subnetRecords != 1 {
					t.Errorf("expected 1 subnet-based record, got %d", subnetRecords)
				}
			},
			wantStatus:  http.StatusOK,
			description: "Should preserve geo-aware attributes (Country, Continent, ASN, Subnet)",
		},
		{
			name: "import invalid JSON",
			setupExisting: func(db *gorm.DB) {
				// No setup needed
			},
			importData: SyncData{}, // Will send invalid JSON manually
			verify: func(t *testing.T, db *gorm.DB) {
				// No verification needed
			},
			wantStatus:  http.StatusBadRequest,
			description: "Should reject invalid JSON payload",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := setupTestDB(t)
			tt.setupExisting(db)

			cfg := &config.Config{}
			server := NewServer(cfg, db, &mockDNSServer{})

			var body []byte
			var err error

			if tt.name == "import invalid JSON" {
				body = []byte(`{"invalid json"}`)
			} else {
				body, err = json.Marshal(tt.importData)
				if err != nil {
					t.Fatalf("failed to marshal import data: %v", err)
				}
			}

			req := httptest.NewRequest("POST", "/sync/import", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			server.r.ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Fatalf("%s\nExpected status %d, got %d\nResponse: %s",
					tt.description, tt.wantStatus, w.Code, w.Body.String())
			}

			if tt.verify != nil && w.Code == http.StatusOK {
				tt.verify(t, db)
			}
		})
	}
}

