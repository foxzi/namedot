package zoneio

import (
    "fmt"
    "io"
    "strings"

    "github.com/miekg/dns"
    "gorm.io/gorm"

    dbm "namedot/internal/db"
)

// ToBind serializes a zone to a simplistic BIND-like zonefile.
func ToBind(z *dbm.Zone) string {
    var b strings.Builder
    b.WriteString("$ORIGIN ")
    b.WriteString(strings.TrimSuffix(z.Name, "."))
    b.WriteString(".\n")
    for _, rs := range z.RRSets {
        for _, r := range rs.Records {
            line := fmt.Sprintf("%s %d IN %s %s\n", strings.TrimSuffix(rs.Name, "."), rs.TTL, strings.ToUpper(rs.Type), r.Data)
            b.WriteString(line)
        }
    }
    return b.String()
}

// ImportBIND parses BIND zone text and merges into zone according to mode.
// mode: upsert | replace
func ImportBIND(db *gorm.DB, zone *dbm.Zone, r io.Reader, mode string, defaultTTL uint32) error {
    origin := dns.Fqdn(zone.Name)
    zp := dns.NewZoneParser(r, origin, "import")

    // accumulate rrsets grouped by name+type
    type key struct{ name, typ string }
    rrsets := map[key]*dbm.RRSet{}

    for rr, ok := zp.Next(); ok; rr, ok = zp.Next() {
        if err := zp.Err(); err != nil { return err }
        if rr == nil { continue }
        hdr := rr.Header()
        name := strings.ToLower(dns.Fqdn(hdr.Name))
        typ := strings.ToUpper(dns.TypeToString[hdr.Rrtype])
        k := key{name: name, typ: typ}
        rs := rrsets[k]
        if rs == nil {
            ttl := hdr.Ttl
            if ttl == 0 && defaultTTL > 0 {
                ttl = defaultTTL
            }
            rs = &dbm.RRSet{ZoneID: zone.ID, Name: name, Type: typ, TTL: ttl}
            rrsets[k] = rs
        }
        data := rdataFromRR(rr)
        rs.Records = append(rs.Records, dbm.RData{Data: data})
        // keep the first TTL if already set
    }

    return db.Transaction(func(tx *gorm.DB) error {
        if strings.ToLower(mode) == "replace" {
            var rrsetIDs []uint
            if err := tx.Model(&dbm.RRSet{}).Where("zone_id = ?", zone.ID).Pluck("id", &rrsetIDs).Error; err != nil {
                return err
            }
            if len(rrsetIDs) > 0 {
                if err := tx.Unscoped().Where("rr_set_id IN ?", rrsetIDs).Delete(&dbm.RData{}).Error; err != nil {
                    return err
                }
            }
            if err := tx.Unscoped().Where("zone_id = ?", zone.ID).Delete(&dbm.RRSet{}).Error; err != nil {
                return err
            }
        }
        for _, rs := range rrsets {
            var existing dbm.RRSet
            _ = tx.Where("zone_id = ? AND name = ? AND type = ?", zone.ID, rs.Name, rs.Type).Limit(1).Find(&existing).Error
            if existing.ID != 0 {
                if err := tx.Unscoped().Where("rr_set_id = ?", existing.ID).Delete(&dbm.RData{}).Error; err != nil {
                    return err
                }
                existing.TTL = rs.TTL
                existing.Records = rs.Records
                if err := tx.Save(&existing).Error; err != nil {
                    return err
                }
            } else {
                if err := tx.Create(rs).Error; err != nil {
                    return err
                }
            }
        }
        return nil
    })
}

func rdataFromRR(rr dns.RR) string {
    // dns.RR.String() => "NAME\tTTL\tCLASS\tTYPE\tRDATA"
    // We split into 5 tokens and return the trailing part as RDATA.
    s := rr.String()
    // normalize whitespace
    fields := strings.Fields(s)
    if len(fields) < 5 {
        return s
    }
    return strings.Join(fields[4:], " ")
}
