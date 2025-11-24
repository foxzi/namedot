package zoneio

import (
    "strings"
    "testing"

    "gorm.io/driver/sqlite"
    "gorm.io/gorm"

    dbm "namedot/internal/db"
)

func newTestDB(t *testing.T) *gorm.DB {
    t.Helper()
    db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
    if err != nil { t.Fatalf("open db: %v", err) }
    if err := dbm.AutoMigrate(db); err != nil { t.Fatalf("migrate: %v", err) }
    return db
}

func TestImportBIND_And_ToBind(t *testing.T) {
    db := newTestDB(t)
    z := dbm.Zone{Name: "example.com"}
    if err := db.Create(&z).Error; err != nil { t.Fatalf("create zone: %v", err) }

    zoneTxt := `$ORIGIN example.com.
@ 3600 IN SOA ns1.example.com. hostmaster.example.com. 2025010101 7200 3600 1209600 300
@ 3600 IN NS ns1.example.com.
www 300 IN A 192.0.2.1
www 300 IN A 192.0.2.2
`

    if err := ImportBIND(db, &z, strings.NewReader(zoneTxt), "replace", 300); err != nil {
        t.Fatalf("import bind: %v", err)
    }

    var sets []dbm.RRSet
    if err := db.Preload("Records").Where("zone_id = ?", z.ID).Order("name, type").Find(&sets).Error; err != nil {
        t.Fatalf("load rrsets: %v", err)
    }
    if len(sets) < 2 { t.Fatalf("expected at least 2 rrsets, got %d", len(sets)) }

    // find A rrset for www.example.com.
    var a *dbm.RRSet
    for i := range sets {
        if sets[i].Type == "A" && sets[i].Name == "www.example.com." { a = &sets[i]; break }
    }
    if a == nil { t.Fatalf("A rrset not found") }
    if got := len(a.Records); got != 2 { t.Fatalf("expected 2 A records, got %d", got) }

    // Export back to BIND and check contains lines
    z2 := dbm.Zone{ID: z.ID, Name: z.Name, RRSets: sets}
    out := ToBind(&z2)
    if !strings.Contains(out, "www.example.com 300 IN A 192.0.2.1") {
        t.Fatalf("export missing A record: %s", out)
    }
}

func TestImportJSON_DefaultTTL(t *testing.T) {
    db := newTestDB(t)
    z := dbm.Zone{Name: "example2.com"}
    if err := db.Create(&z).Error; err != nil { t.Fatalf("create zone: %v", err) }

    src := dbm.Zone{RRSets: []dbm.RRSet{{Name: "www.example.com.", Type: "A", TTL: 0, Records: []dbm.RData{{Data: "192.0.2.5"}}}}}
    if err := ImportJSON(db, &z, &src, "replace", 1234); err != nil {
        t.Fatalf("import json: %v", err)
    }
    var set dbm.RRSet
    if err := db.Where("zone_id = ? AND name = ? AND type = ?", z.ID, "www.example.com.", "A").First(&set).Error; err != nil {
        t.Fatalf("load set: %v", err)
    }
    if set.TTL != 1234 { t.Fatalf("expected ttl 1234, got %d", set.TTL) }
}

func TestImportJSON_NoDefaultTTL_KeepsZeroTTL(t *testing.T) {
    db := newTestDB(t)
    z := dbm.Zone{Name: "example3.com"}
    if err := db.Create(&z).Error; err != nil { t.Fatalf("create zone: %v", err) }

    src := dbm.Zone{RRSets: []dbm.RRSet{{Name: "api.example.com.", Type: "A", TTL: 0, Records: []dbm.RData{{Data: "192.0.2.6"}}}}}
    if err := ImportJSON(db, &z, &src, "replace", 0); err != nil {
        t.Fatalf("import json: %v", err)
    }
    var set dbm.RRSet
    if err := db.Where("zone_id = ? AND name = ? AND type = ?", z.ID, "api.example.com.", "A").First(&set).Error; err != nil {
        t.Fatalf("load set: %v", err)
    }
    if set.TTL != 0 { t.Fatalf("expected ttl 0 to be preserved, got %d", set.TTL) }
}

func TestImportJSON_Replace_RemovesMissingRRSet(t *testing.T) {
    db := newTestDB(t)
    z := dbm.Zone{Name: "example4.com"}
    if err := db.Create(&z).Error; err != nil {
        t.Fatalf("create zone: %v", err)
    }
    // initial zone with A and MX
    initial := dbm.Zone{RRSets: []dbm.RRSet{
        {Name: "example4.com.", Type: "A", TTL: 300, Records: []dbm.RData{{Data: "192.0.2.10"}}},
        {Name: "example4.com.", Type: "MX", TTL: 300, Records: []dbm.RData{{Data: "mail.example4.com."}}},
    }}
    if err := ImportJSON(db, &z, &initial, "replace", 0); err != nil {
        t.Fatalf("seed import: %v", err)
    }

    // new payload without MX (should remove it when replace)
    updated := dbm.Zone{RRSets: []dbm.RRSet{
        {Name: "example4.com.", Type: "A", TTL: 300, Records: []dbm.RData{{Data: "192.0.2.20"}}},
    }}
    if err := ImportJSON(db, &z, &updated, "replace", 0); err != nil {
        t.Fatalf("import replace: %v", err)
    }

    var sets []dbm.RRSet
    if err := db.Where("zone_id = ?", z.ID).Find(&sets).Error; err != nil {
        t.Fatalf("load rrsets: %v", err)
    }
    if len(sets) != 1 {
        t.Fatalf("expected 1 rrset after replace, got %d", len(sets))
    }
    if sets[0].Type != "A" {
        t.Fatalf("expected only A rrset, got %s", sets[0].Type)
    }
}
