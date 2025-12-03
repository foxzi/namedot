package zoneio

import (
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

// NormalizeRRSetName normalizes record name, expanding @ to zone apex
func NormalizeRRSetName(name, zoneName string) string {
    n := strings.ToLower(strings.TrimSpace(name))
    zone := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(zoneName)), ".")

    // Handle @ as zone apex
    if n == "" || n == "@" {
        return zone + "."
    }

    // Handle trailing .@ (relative to zone apex)
    if strings.HasSuffix(n, ".@") {
        n = strings.TrimSuffix(n, ".@")
    }

    // Already FQDN
    if strings.HasSuffix(n, ".") {
        return n
    }

    // Relative name - append zone
    return n + "." + zone + "."
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
            rs.Name = NormalizeRRSetName(rs.Name, dst.Name)
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
