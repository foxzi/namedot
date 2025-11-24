package db

import (
	"strconv"
	"strings"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newMemDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := AutoMigrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestBumpSOASerialAuto_CreatesDefaultSOA(t *testing.T) {
	db := newMemDB(t)
	z := Zone{Name: "example.com"}
	if err := db.Create(&z).Error; err != nil {
		t.Fatalf("create zone: %v", err)
	}

	// No SOA present initially
	var cnt int64
	if err := db.Model(&RRSet{}).Where("zone_id = ? AND type = ?", z.ID, "SOA").Count(&cnt).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if cnt != 0 {
		t.Fatalf("expected no SOA, got %d", cnt)
	}

	// Auto-create
	BumpSOASerialAuto(db, z, true, "", "")

	var soa RRSet
	if err := db.Preload("Records").Where("zone_id = ? AND type = ?", z.ID, "SOA").First(&soa).Error; err != nil {
		t.Fatalf("soa not created: %v", err)
	}
	if soa.TTL != 3600 {
		t.Fatalf("unexpected TTL: %d", soa.TTL)
	}
	if len(soa.Records) != 1 {
		t.Fatalf("expected 1 soa record, got %d", len(soa.Records))
	}
	parts := strings.Fields(soa.Records[0].Data)
	if len(parts) < 7 {
		t.Fatalf("invalid SOA rdata: %q", soa.Records[0].Data)
	}
	if parts[0] != "ns1.example.com." || parts[1] != "hostmaster.example.com." {
		t.Fatalf("unexpected MNAME/RNAME: %q %q", parts[0], parts[1])
	}
	if _, err := strconv.ParseInt(parts[2], 10, 64); err != nil {
		t.Fatalf("serial not int: %q", parts[2])
	}
	if parts[3] != "7200" || parts[4] != "3600" || parts[5] != "1209600" || parts[6] != "300" {
		t.Fatalf("unexpected timers: %v", parts[3:7])
	}

	// Bump again should increment serial
	oldSerial := parts[2]
	BumpSOASerialAuto(db, z, true, "", "")
	var soa2 RRSet
	if err := db.Preload("Records").Where("zone_id = ? AND type = ?", z.ID, "SOA").First(&soa2).Error; err != nil {
		t.Fatalf("soa not found on bump: %v", err)
	}
	parts2 := strings.Fields(soa2.Records[0].Data)
	if len(parts2) < 7 {
		t.Fatalf("invalid SOA rdata after bump: %q", soa2.Records[0].Data)
	}
	// serial should change (usually increase); compare as integers
	n1, _ := strconv.ParseInt(oldSerial, 10, 64)
	n2, _ := strconv.ParseInt(parts2[2], 10, 64)
	if n2 <= n1 {
		t.Fatalf("serial did not increase: %d -> %d", n1, n2)
	}
}
