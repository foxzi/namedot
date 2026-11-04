package db

import (
	"os"
	"path/filepath"
	"testing"
)

func TestImportZones_ReplaceWithEmptyBackupKeepsData(t *testing.T) {
	db := newMemDB(t)
	if err := db.Create(&Zone{Name: "keep-backup.test."}).Error; err != nil {
		t.Fatalf("create zone: %v", err)
	}
	file := filepath.Join(t.TempDir(), "empty.json")
	if err := os.WriteFile(file, []byte(`{"zones":[]}`), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	if err := ImportZones(db, file, "replace"); err == nil {
		t.Fatalf("expected error for empty backup in replace mode")
	}
	var count int64
	db.Model(&Zone{}).Where("name = ?", "keep-backup.test.").Count(&count)
	if count != 1 {
		t.Fatalf("existing zone was deleted")
	}
}
