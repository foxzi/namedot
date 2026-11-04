package replication

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"namedot/internal/config"
	dbm "namedot/internal/db"
)

func setupTestClient(t testing.TB, masterURL string) (*SyncClient, *gorm.DB) {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("Failed to open test database: %v", err)
	}

	if err := dbm.AutoMigrate(db); err != nil {
		t.Fatalf("Failed to migrate: %v", err)
	}

	cfg := &config.Config{
		RESTListen: "localhost:8080",
		Replication: config.ReplicationConfig{
			Mode:            "slave",
			MasterURL:       masterURL,
			APIToken:        "test-token",
			SyncIntervalSec: 60,
		},
	}

	client := NewSyncClient(cfg, db)
	return client, db
}

func TestNewSyncClient(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}

	cfg := &config.Config{
		Replication: config.ReplicationConfig{
			MasterURL: "http://master:8080",
		},
	}

	client := NewSyncClient(cfg, db)
	if client == nil {
		t.Fatal("Expected client, got nil")
	}
	if client.cfg != cfg {
		t.Error("Config not properly set")
	}
	if client.db != db {
		t.Error("Database not properly set")
	}
	if client.client == nil {
		t.Error("HTTP client not initialized")
	}
}

func TestFetchFromMaster_Success(t *testing.T) {
	// Create mock master server
	master := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify endpoint
		if r.URL.Path != "/sync/export" {
			t.Errorf("Expected path /sync/export, got %s", r.URL.Path)
		}

		// Verify authentication
		auth := r.Header.Get("Authorization")
		if auth != "Bearer test-token" {
			t.Errorf("Expected Bearer test-token, got %s", auth)
		}

		// Return mock data
		data := SyncData{
			Zones: []dbm.Zone{
				{Name: "test.com"},
			},
			Templates: []dbm.Template{
				{Name: "template1", Description: "Test template"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(data)
	}))
	defer master.Close()

	client, _ := setupTestClient(t, master.URL)

	ctx := context.Background()
	data, err := client.FetchFromMaster(ctx)
	if err != nil {
		t.Fatalf("FetchFromMaster failed: %v", err)
	}

	if len(data.Zones) != 1 {
		t.Errorf("Expected 1 zone, got %d", len(data.Zones))
	}
	if data.Zones[0].Name != "test.com" {
		t.Errorf("Expected zone name 'test.com', got '%s'", data.Zones[0].Name)
	}

	if len(data.Templates) != 1 {
		t.Errorf("Expected 1 template, got %d", len(data.Templates))
	}
	if data.Templates[0].Name != "template1" {
		t.Errorf("Expected template name 'template1', got '%s'", data.Templates[0].Name)
	}
}

func TestFetchFromMaster_WithoutAuth(t *testing.T) {
	// Create mock master server
	master := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify no authentication header
		auth := r.Header.Get("Authorization")
		if auth != "" {
			t.Errorf("Expected no auth header, got %s", auth)
		}

		data := SyncData{}
		json.NewEncoder(w).Encode(data)
	}))
	defer master.Close()

	db, _ := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	cfg := &config.Config{
		RESTListen: "localhost:8080",
		Replication: config.ReplicationConfig{
			MasterURL: master.URL,
			// No APIToken set
		},
	}

	client := NewSyncClient(cfg, db)
	ctx := context.Background()
	_, err := client.FetchFromMaster(ctx)
	if err != nil {
		t.Fatalf("FetchFromMaster failed: %v", err)
	}
}

func TestFetchFromMaster_ServerError(t *testing.T) {
	// Create mock master server that returns error
	master := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	}))
	defer master.Close()

	client, _ := setupTestClient(t, master.URL)

	ctx := context.Background()
	_, err := client.FetchFromMaster(ctx)
	if err == nil {
		t.Error("Expected error for server error, got nil")
	}
	if err != nil && err.Error() == "" {
		t.Error("Expected non-empty error message")
	}
}

func TestFetchFromMaster_Unauthorized(t *testing.T) {
	// Create mock master server that requires auth
	master := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("unauthorized"))
	}))
	defer master.Close()

	client, _ := setupTestClient(t, master.URL)

	ctx := context.Background()
	_, err := client.FetchFromMaster(ctx)
	if err == nil {
		t.Error("Expected error for unauthorized, got nil")
	}
}

func TestFetchFromMaster_InvalidJSON(t *testing.T) {
	// Create mock master server that returns invalid JSON
	master := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("invalid json"))
	}))
	defer master.Close()

	client, _ := setupTestClient(t, master.URL)

	ctx := context.Background()
	_, err := client.FetchFromMaster(ctx)
	if err == nil {
		t.Error("Expected error for invalid JSON, got nil")
	}
}

func TestFetchFromMaster_ContextCancellation(t *testing.T) {
	// Create mock master server with delay
	master := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		data := SyncData{}
		json.NewEncoder(w).Encode(data)
	}))
	defer master.Close()

	client, _ := setupTestClient(t, master.URL)

	// Cancel context immediately
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := client.FetchFromMaster(ctx)
	if err == nil {
		t.Error("Expected error for cancelled context, got nil")
	}
}

func TestFetchFromMaster_EmptyData(t *testing.T) {
	// Create mock master server with empty data
	master := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data := SyncData{
			Zones:     []dbm.Zone{},
			Templates: []dbm.Template{},
		}
		json.NewEncoder(w).Encode(data)
	}))
	defer master.Close()

	client, _ := setupTestClient(t, master.URL)

	ctx := context.Background()
	data, err := client.FetchFromMaster(ctx)
	if err != nil {
		t.Fatalf("FetchFromMaster failed: %v", err)
	}

	if len(data.Zones) != 0 {
		t.Errorf("Expected 0 zones, got %d", len(data.Zones))
	}
	if len(data.Templates) != 0 {
		t.Errorf("Expected 0 templates, got %d", len(data.Templates))
	}
}

func TestFetchFromMaster_LargeDataset(t *testing.T) {
	// Create mock master server with large dataset
	master := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		zones := make([]dbm.Zone, 100)
		for i := 0; i < 100; i++ {
			zones[i] = dbm.Zone{Name: "zone" + string(rune(i)) + ".com"}
		}

		templates := make([]dbm.Template, 50)
		for i := 0; i < 50; i++ {
			templates[i] = dbm.Template{Name: "template" + string(rune(i))}
		}

		data := SyncData{
			Zones:     zones,
			Templates: templates,
		}
		json.NewEncoder(w).Encode(data)
	}))
	defer master.Close()

	client, _ := setupTestClient(t, master.URL)

	ctx := context.Background()
	data, err := client.FetchFromMaster(ctx)
	if err != nil {
		t.Fatalf("FetchFromMaster failed: %v", err)
	}

	if len(data.Zones) != 100 {
		t.Errorf("Expected 100 zones, got %d", len(data.Zones))
	}
	if len(data.Templates) != 50 {
		t.Errorf("Expected 50 templates, got %d", len(data.Templates))
	}
}

func TestSyncData_Marshaling(t *testing.T) {
	// Test that SyncData can be marshaled and unmarshaled
	original := SyncData{
		Zones: []dbm.Zone{
			{Name: "test.com"},
			{Name: "example.org"},
		},
		Templates: []dbm.Template{
			{Name: "template1", Description: "First"},
			{Name: "template2", Description: "Second"},
		},
	}

	// Marshal
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	// Unmarshal
	var decoded SyncData
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	// Verify
	if len(decoded.Zones) != len(original.Zones) {
		t.Errorf("Zone count mismatch: expected %d, got %d", len(original.Zones), len(decoded.Zones))
	}
	if len(decoded.Templates) != len(original.Templates) {
		t.Errorf("Template count mismatch: expected %d, got %d", len(original.Templates), len(decoded.Templates))
	}
}

func TestFetchFromMaster_FallbackToken(t *testing.T) {
	// Test that APIToken is used when Replication.APIToken is empty
	master := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer fallback-token" {
			t.Errorf("Expected Bearer fallback-token, got %s", auth)
		}
		data := SyncData{}
		json.NewEncoder(w).Encode(data)
	}))
	defer master.Close()

	db, _ := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	cfg := &config.Config{
		APIToken:   "fallback-token",
		RESTListen: "localhost:8080",
		Replication: config.ReplicationConfig{
			MasterURL: master.URL,
			// Replication.APIToken is empty, should use cfg.APIToken
		},
	}

	client := NewSyncClient(cfg, db)
	ctx := context.Background()
	_, err := client.FetchFromMaster(ctx)
	if err != nil {
		t.Fatalf("FetchFromMaster failed: %v", err)
	}
}

func TestFetchFromMaster_Timeout(t *testing.T) {
	// Create mock master server that hangs
	master := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}))
	defer master.Close()

	client, _ := setupTestClient(t, master.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := client.FetchFromMaster(ctx)
	if err == nil {
		t.Error("Expected timeout error, got nil")
	}
}

func BenchmarkFetchFromMaster(b *testing.B) {
	master := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data := SyncData{
			Zones: []dbm.Zone{
				{Name: "test1.com"},
				{Name: "test2.com"},
			},
			Templates: []dbm.Template{
				{Name: "template1"},
			},
		}
		json.NewEncoder(w).Encode(data)
	}))
	defer master.Close()

	client, _ := setupTestClient(b, master.URL)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		client.FetchFromMaster(ctx)
	}
}

// TestApplyData_WithoutAPIToken verifies that ApplyData works without
// any API token configured (as on slave servers).
// This test prevents regression where ApplyData might try to make HTTP
// requests to itself requiring authentication.
func TestApplyData_WithoutAPIToken(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	if err := dbm.AutoMigrate(db); err != nil {
		t.Fatalf("Failed to migrate: %v", err)
	}

	// Config without any API token - simulates slave server setup
	cfg := &config.Config{
		RESTListen:   "127.0.0.1:8080",
		APIToken:     "",           // No plain text token
		APITokenHash: "",           // No hashed token
		Replication: config.ReplicationConfig{
			Mode:            "slave",
			MasterURL:       "https://master:8443",
			APIToken:        "master-token", // Only used for fetching from master
			SyncIntervalSec: 60,
		},
	}

	client := NewSyncClient(cfg, db)

	// Prepare test data
	data := &SyncData{
		Zones: []dbm.Zone{
			{
				Name: "example.com",
				RRSets: []dbm.RRSet{
					{
						Name: "example.com.",
						Type: "A",
						TTL:  300,
						Records: []dbm.RData{
							{Data: "1.2.3.4"},
						},
					},
				},
			},
		},
		Templates: []dbm.Template{
			{
				Name:        "basic",
				Description: "Basic template",
				Records: []dbm.TemplateRecord{
					{Name: "@", Type: "A", TTL: 300, Data: "1.2.3.4"},
				},
			},
		},
	}

	// ApplyData should work without making HTTP requests
	err = client.ApplyData(data)
	if err != nil {
		t.Fatalf("ApplyData failed without API token: %v", err)
	}

	// Verify data was applied to database
	var zones []dbm.Zone
	if err := db.Preload("RRSets.Records").Find(&zones).Error; err != nil {
		t.Fatalf("Failed to query zones: %v", err)
	}

	if len(zones) != 1 {
		t.Fatalf("Expected 1 zone, got %d", len(zones))
	}
	if zones[0].Name != "example.com." {
		t.Errorf("Expected zone name 'example.com.', got '%s'", zones[0].Name)
	}
	if len(zones[0].RRSets) != 1 {
		t.Fatalf("Expected 1 rrset, got %d", len(zones[0].RRSets))
	}
	if len(zones[0].RRSets[0].Records) != 1 {
		t.Fatalf("Expected 1 record, got %d", len(zones[0].RRSets[0].Records))
	}

	var templates []dbm.Template
	if err := db.Preload("Records").Find(&templates).Error; err != nil {
		t.Fatalf("Failed to query templates: %v", err)
	}

	if len(templates) != 1 {
		t.Fatalf("Expected 1 template, got %d", len(templates))
	}
	if templates[0].Name != "basic" {
		t.Errorf("Expected template name 'basic', got '%s'", templates[0].Name)
	}
}

// TestApplyData_UpdatesExistingZones verifies that ApplyData correctly
// updates existing zones instead of creating duplicates.
func TestApplyData_UpdatesExistingZones(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	if err := dbm.AutoMigrate(db); err != nil {
		t.Fatalf("Failed to migrate: %v", err)
	}

	cfg := &config.Config{
		RESTListen: "127.0.0.1:8080",
		Replication: config.ReplicationConfig{
			Mode: "slave",
		},
	}

	client := NewSyncClient(cfg, db)

	// First sync - create zone
	data1 := &SyncData{
		Zones: []dbm.Zone{
			{
				Name: "example.com",
				RRSets: []dbm.RRSet{
					{Name: "example.com.", Type: "A", TTL: 300, Records: []dbm.RData{{Data: "1.1.1.1"}}},
				},
			},
		},
	}

	if err := client.ApplyData(data1); err != nil {
		t.Fatalf("First ApplyData failed: %v", err)
	}

	// Second sync - update zone with different records
	data2 := &SyncData{
		Zones: []dbm.Zone{
			{
				Name: "example.com",
				RRSets: []dbm.RRSet{
					{Name: "example.com.", Type: "A", TTL: 600, Records: []dbm.RData{{Data: "2.2.2.2"}}},
					{Name: "www.example.com.", Type: "A", TTL: 300, Records: []dbm.RData{{Data: "3.3.3.3"}}},
				},
			},
		},
	}

	if err := client.ApplyData(data2); err != nil {
		t.Fatalf("Second ApplyData failed: %v", err)
	}

	// Verify only one zone exists (not duplicated)
	var zones []dbm.Zone
	if err := db.Find(&zones).Error; err != nil {
		t.Fatalf("Failed to query zones: %v", err)
	}
	if len(zones) != 1 {
		t.Fatalf("Expected 1 zone after update, got %d (zone was duplicated)", len(zones))
	}

	// Verify records were updated
	var rrsets []dbm.RRSet
	if err := db.Preload("Records").Where("zone_id = ?", zones[0].ID).Find(&rrsets).Error; err != nil {
		t.Fatalf("Failed to query rrsets: %v", err)
	}
	if len(rrsets) != 2 {
		t.Errorf("Expected 2 rrsets after update, got %d", len(rrsets))
	}
}

// TestApplyData_MultipleZones verifies that ApplyData handles multiple zones.
func TestApplyData_MultipleZones(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	if err := dbm.AutoMigrate(db); err != nil {
		t.Fatalf("Failed to migrate: %v", err)
	}

	cfg := &config.Config{
		RESTListen: "127.0.0.1:8080",
		Replication: config.ReplicationConfig{
			Mode: "slave",
		},
	}

	client := NewSyncClient(cfg, db)

	data := &SyncData{
		Zones: []dbm.Zone{
			{Name: "example.com", RRSets: []dbm.RRSet{{Name: "@", Type: "A", TTL: 300, Records: []dbm.RData{{Data: "1.1.1.1"}}}}},
			{Name: "example.org", RRSets: []dbm.RRSet{{Name: "@", Type: "A", TTL: 300, Records: []dbm.RData{{Data: "2.2.2.2"}}}}},
			{Name: "example.net", RRSets: []dbm.RRSet{{Name: "@", Type: "A", TTL: 300, Records: []dbm.RData{{Data: "3.3.3.3"}}}}},
		},
	}

	if err := client.ApplyData(data); err != nil {
		t.Fatalf("ApplyData failed: %v", err)
	}

	var count int64
	if err := db.Model(&dbm.Zone{}).Count(&count).Error; err != nil {
		t.Fatalf("Failed to count zones: %v", err)
	}
	if count != 3 {
		t.Errorf("Expected 3 zones, got %d", count)
	}
}

// TestApplyData_EmptyData verifies that ApplyData handles empty data.
func TestApplyData_EmptyData(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	if err := dbm.AutoMigrate(db); err != nil {
		t.Fatalf("Failed to migrate: %v", err)
	}

	cfg := &config.Config{
		RESTListen: "127.0.0.1:8080",
		Replication: config.ReplicationConfig{
			Mode: "slave",
		},
	}

	client := NewSyncClient(cfg, db)

	data := &SyncData{
		Zones:     []dbm.Zone{},
		Templates: []dbm.Template{},
	}

	err = client.ApplyData(data)
	if err != nil {
		t.Fatalf("ApplyData failed on empty data: %v", err)
	}
}

func TestApplyData_RemovesZonesDeletedOnMaster(t *testing.T) {
	client, db := setupTestClient(t, "http://unused")
	zone := func(name string) dbm.Zone {
		return dbm.Zone{Name: name, RRSets: []dbm.RRSet{{Name: name + ".", Type: "A", TTL: 60, Records: []dbm.RData{{Data: "192.0.2.1"}}}}}
	}
	first := &SyncData{
		Zones:     []dbm.Zone{zone("keep.test"), zone("gone.test")},
		Templates: []dbm.Template{{Name: "keep"}, {Name: "gone"}},
	}
	if err := client.ApplyData(first); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	second := &SyncData{Zones: []dbm.Zone{zone("keep.test")}, Templates: []dbm.Template{{Name: "keep"}}}
	if err := client.ApplyData(second); err != nil {
		t.Fatalf("second apply: %v", err)
	}

	var zones []string
	db.Model(&dbm.Zone{}).Pluck("name", &zones)
	if len(zones) != 1 || zones[0] != "keep.test." {
		t.Fatalf("zones = %v, want [keep.test.]", zones)
	}
	var rrsets, records, templates int64
	db.Model(&dbm.RRSet{}).Count(&rrsets)
	db.Model(&dbm.RData{}).Count(&records)
	db.Model(&dbm.Template{}).Count(&templates)
	if rrsets != 1 || records != 1 || templates != 1 {
		t.Fatalf("leftovers: rrsets=%d records=%d templates=%d, want 1/1/1", rrsets, records, templates)
	}

	// Empty payload must not wipe the slave
	if err := client.ApplyData(&SyncData{}); err != nil {
		t.Fatalf("empty apply: %v", err)
	}
	var count int64
	db.Model(&dbm.Zone{}).Count(&count)
	if count != 1 {
		t.Fatalf("empty payload wiped zones: count=%d", count)
	}
}

func TestSyncOnce_CallsOnApplied(t *testing.T) {
	master := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"zones":[{"name":"a.test"}],"templates":[]}`))
	}))
	defer master.Close()
	client, _ := setupTestClient(t, master.URL)
	called := false
	client.OnApplied = func() { called = true }
	if err := client.SyncOnce(context.Background()); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if !called {
		t.Fatalf("OnApplied was not called")
	}
}

func TestApplyData_KeepsUnchangedZones(t *testing.T) {
	client, db := setupTestClient(t, "http://unused")
	ru := "RU"
	data := func(ip string) *SyncData {
		return &SyncData{Zones: []dbm.Zone{{Name: "a.test", RRSets: []dbm.RRSet{
			{Name: "@", Type: "A", TTL: 60, Records: []dbm.RData{{Data: ip, Country: &ru}, {Data: "192.0.2.9"}}},
			{Name: "www", Type: "CNAME", TTL: 60, Records: []dbm.RData{{Data: "a.test."}}},
		}}}}
	}
	ids := func() []uint {
		var ids []uint
		db.Model(&dbm.RRSet{}).Order("id").Pluck("id", &ids)
		return ids
	}
	if err := client.ApplyData(data("192.0.2.1")); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	before := ids()
	if err := client.ApplyData(data("192.0.2.1")); err != nil {
		t.Fatalf("same apply: %v", err)
	}
	if after := ids(); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Fatalf("unchanged zone was rewritten: rrset ids %v -> %v", before, after)
	}

	if err := client.ApplyData(data("192.0.2.2")); err != nil {
		t.Fatalf("changed apply: %v", err)
	}
	var n int64
	db.Model(&dbm.RData{}).Where("data = ? AND country = ?", "192.0.2.2", "RU").Count(&n)
	if n != 1 {
		t.Fatalf("changed zone was not applied")
	}
}
