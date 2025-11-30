package db

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"gorm.io/gorm"
)

// normalizeFQDN ensures name is lowercase and ends with a dot
func normalizeFQDN(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	if n != "" && !strings.HasSuffix(n, ".") {
		n += "."
	}
	return n
}

// BackupData represents the complete backup structure
type BackupData struct {
	Version string `json:"version"`
	Zones   []Zone `json:"zones"`
}

// ExportZones exports all zones to a JSON file
func ExportZones(db *gorm.DB, filename string) error {
	var zones []Zone
	if err := db.Preload("RRSets.Records").Find(&zones).Error; err != nil {
		return fmt.Errorf("failed to load zones: %w", err)
	}

	backup := BackupData{
		Version: "1.0",
		Zones:   zones,
	}

	data, err := json.MarshalIndent(backup, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}

	if err := os.WriteFile(filename, data, 0644); err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	return nil
}

// ImportZones imports zones from a JSON file
// mode: "replace" - delete all existing zones, "merge" - keep existing zones
func ImportZones(db *gorm.DB, filename string, mode string) error {
	data, err := os.ReadFile(filename)
	if err != nil {
		return fmt.Errorf("failed to read file: %w", err)
	}

	var backup BackupData
	if err := json.Unmarshal(data, &backup); err != nil {
		return fmt.Errorf("failed to parse JSON: %w", err)
	}

	return db.Transaction(func(tx *gorm.DB) error {
		if mode == "replace" {
			// Delete all existing zones and their data
			if err := tx.Exec("DELETE FROM r_data").Error; err != nil {
				return fmt.Errorf("failed to delete records: %w", err)
			}
			if err := tx.Exec("DELETE FROM rr_sets").Error; err != nil {
				return fmt.Errorf("failed to delete rrsets: %w", err)
			}
			if err := tx.Exec("DELETE FROM zones").Error; err != nil {
				return fmt.Errorf("failed to delete zones: %w", err)
			}
		}

		// Import zones
		for _, zone := range backup.Zones {
			// Normalize zone name
			zoneName := normalizeFQDN(zone.Name)

			var existingZone Zone
			err := tx.Where("name = ?", zoneName).First(&existingZone).Error

			if err == gorm.ErrRecordNotFound {
				// Create new zone
				newZone := Zone{Name: zoneName}
				if err := tx.Create(&newZone).Error; err != nil {
					return fmt.Errorf("failed to create zone %s: %w", zone.Name, err)
				}
				existingZone = newZone
			} else if err != nil {
				return fmt.Errorf("failed to check zone %s: %w", zone.Name, err)
			}

			// Delete existing RRSets for this zone if merge mode
			if mode == "merge" {
				var rrsetIDs []uint
				if err := tx.Model(&RRSet{}).Where("zone_id = ?", existingZone.ID).Pluck("id", &rrsetIDs).Error; err != nil {
					return fmt.Errorf("failed to get rrset ids: %w", err)
				}
				if len(rrsetIDs) > 0 {
					if err := tx.Where("rr_set_id IN ?", rrsetIDs).Delete(&RData{}).Error; err != nil {
						return fmt.Errorf("failed to delete records: %w", err)
					}
				}
				if err := tx.Where("zone_id = ?", existingZone.ID).Delete(&RRSet{}).Error; err != nil {
					return fmt.Errorf("failed to delete rrsets: %w", err)
				}
			}

			// Import RRSets
			for _, rrset := range zone.RRSets {
				newRRSet := RRSet{
					ZoneID:  existingZone.ID,
					Name:    normalizeFQDN(rrset.Name),
					Type:    strings.ToUpper(rrset.Type),
					TTL:     rrset.TTL,
					Records: rrset.Records,
				}

				// Clear IDs from imported records
				for i := range newRRSet.Records {
					newRRSet.Records[i].ID = 0
					newRRSet.Records[i].RRSetID = 0
				}

				if err := tx.Create(&newRRSet).Error; err != nil {
					return fmt.Errorf("failed to create rrset %s/%s: %w", rrset.Name, rrset.Type, err)
				}
			}
		}

		return nil
	})
}
