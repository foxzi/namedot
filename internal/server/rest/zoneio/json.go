package zoneio

import (
    "fmt"
    "strings"

    "gorm.io/gorm"

    dbm "namedot/internal/db"
)

// NormalizeFQDN ensures name is lowercase and ends with a dot
func NormalizeFQDN(name string) string {
    n := strings.ToLower(strings.TrimSpace(name))
    if n != "" && !strings.HasSuffix(n, ".") {
        n += "."
    }
    return n
}

// NormalizeRRSetName normalizes record name, expanding @ to zone apex.
// Returns error if the resulting name does not belong to the zone.
func NormalizeRRSetName(name, zoneName string) (string, error) {
    n := strings.ToLower(strings.TrimSpace(name))
    zone := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(zoneName)), ".")
    zoneFQDN := zone + "."

    // Handle @ as zone apex
    if n == "" || n == "@" {
        return zoneFQDN, nil
    }

    // Handle trailing .@ (relative to zone apex)
    if strings.HasSuffix(n, ".@") {
        n = strings.TrimSuffix(n, ".@")
    }

    var result string

    // Already FQDN (ends with dot)
    if strings.HasSuffix(n, ".") {
        result = n
    } else if n == zone {
        // Name equals zone name (FQDN without dot) - treat as apex
        result = zoneFQDN
    } else if strings.HasSuffix(n, "."+zone) {
        // Name ends with zone (FQDN without trailing dot)
        result = n + "."
    } else {
        // Relative name - append zone
        result = n + "." + zoneFQDN
    }

    // Validate: result must be zone apex or subdomain of zone
    if result != zoneFQDN && !strings.HasSuffix(result, "."+zoneFQDN) {
        return "", fmt.Errorf("record name %q does not belong to zone %q", name, zoneName)
    }

    return result, nil
}

// ImportJSON imports RRsets from src into dst zone.
// mode: upsert | replace
func ImportJSON(db *gorm.DB, dst *dbm.Zone, src *dbm.Zone, mode string, defaultTTL uint32) error {
    return db.Transaction(func(tx *gorm.DB) error {
        if mode == "replace" {
            var rrsetIDs []uint
            if err := tx.Model(&dbm.RRSet{}).Where("zone_id = ?", dst.ID).Pluck("id", &rrsetIDs).Error; err != nil {
                return err
            }
            if len(rrsetIDs) > 0 {
                if err := tx.Where("rr_set_id IN ?", rrsetIDs).Delete(&dbm.RData{}).Error; err != nil {
                    return err
                }
            }
            if err := tx.Where("zone_id = ?", dst.ID).Delete(&dbm.RRSet{}).Error; err != nil {
                return err
            }
        }
        for _, rs := range src.RRSets {
            rs.ID = 0                     // ignore incoming rrset ID
            rs.ZoneID = dst.ID
            normalizedName, err := NormalizeRRSetName(rs.Name, dst.Name)
            if err != nil {
                return err
            }
            rs.Name = normalizedName
            rs.Type = strings.ToUpper(rs.Type)
            if rs.TTL == 0 && defaultTTL > 0 {
                rs.TTL = defaultTTL
            }
            // drop record IDs so GORM inserts fresh rows
            for i := range rs.Records {
                rs.Records[i].ID = 0
                rs.Records[i].RRSetID = 0
            }

            // Upsert by name+type
            var existing dbm.RRSet
            if err := tx.Where("zone_id = ? AND name = ? AND type = ?", dst.ID, rs.Name, rs.Type).First(&existing).Error; err == nil {
                // replace records
                if err := tx.Where("rr_set_id = ?", existing.ID).Delete(&dbm.RData{}).Error; err != nil {
                    return err
                }
                existing.TTL = rs.TTL
                existing.Records = rs.Records
                if err := tx.Save(&existing).Error; err != nil {
                    return err
                }
            } else {
                if err := tx.Create(&rs).Error; err != nil {
                    return err
                }
            }
        }
        return nil
    })
}
