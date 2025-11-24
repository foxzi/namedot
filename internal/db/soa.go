package db

import (
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
)

// BumpSOASerial finds SOA for zone and increments its serial.
// Uses a non-erroring Find to avoid noisy "record not found" logs.
func BumpSOASerial(db *gorm.DB, zoneID uint) {
	var soa RRSet
	tx := db.Preload("Records").Where("zone_id = ? AND type = ?", zoneID, "SOA").Limit(1).Find(&soa)
	if tx.Error != nil {
		return
	}
	if soa.ID == 0 || len(soa.Records) == 0 {
		return
	}
	parts := strings.Fields(soa.Records[0].Data)
	if len(parts) < 7 {
		return
	}
	if n, err := strconv.ParseInt(parts[2], 10, 64); err == nil {
		n++
		parts[2] = strconv.FormatInt(n, 10)
	} else {
		parts[2] = strconv.FormatInt(time.Now().Unix(), 10)
	}
	newData := strings.Join(parts, " ")
	_ = db.Model(&RData{}).Where("id = ?", soa.Records[0].ID).Update("data", newData).Error
}

func resolveSOAName(input, zone, fallback string) string {
	v := strings.TrimSpace(input)
	if v == "" {
		v = fallback
	}
	z := strings.TrimSuffix(strings.ToLower(zone), ".")
	v = strings.ReplaceAll(v, "{zone}", z)
	v = strings.ToLower(strings.TrimSpace(v))
	if !strings.HasSuffix(v, ".") {
		v += "."
	}
	return v
}

// BumpSOASerialAuto bumps serial or creates a default SOA if missing when auto is true.
// primary/hostmaster can include placeholder {zone} (zone name without trailing dot).
func BumpSOASerialAuto(db *gorm.DB, zone Zone, auto bool, primary, hostmaster string) {
	var soa RRSet
	tx := db.Preload("Records").Where("zone_id = ? AND type = ?", zone.ID, "SOA").Limit(1).Find(&soa)
	if tx.Error != nil {
		return
	}
	if soa.ID == 0 || len(soa.Records) == 0 {
		if !auto {
			return
		}
		zname := strings.TrimSuffix(strings.ToLower(zone.Name), ".")
		origin := zname + "."
		primary = resolveSOAName(primary, zname, "ns1.{zone}")
		hostmaster = resolveSOAName(hostmaster, zname, "hostmaster.{zone}")
		serial := strconv.FormatInt(time.Now().Unix(), 10)
		// Defaults: refresh 7200, retry 3600, expire 1209600, minimum 300, TTL 3600
		data := strings.Join([]string{primary, hostmaster, serial, "7200", "3600", "1209600", "300"}, " ")
		rs := RRSet{ZoneID: zone.ID, Name: origin, Type: "SOA", TTL: 3600,
			Records: []RData{{Data: data}}}
		_ = db.Create(&rs).Error
		return
	}
	// bump existing
	parts := strings.Fields(soa.Records[0].Data)
	if len(parts) < 7 {
		return
	}
	zname := strings.TrimSuffix(strings.ToLower(zone.Name), ".")
	if primary != "" {
		parts[0] = resolveSOAName(primary, zname, parts[0])
	}
	if hostmaster != "" {
		parts[1] = resolveSOAName(hostmaster, zname, parts[1])
	}
	if n, err := strconv.ParseInt(parts[2], 10, 64); err == nil {
		n++
		parts[2] = strconv.FormatInt(n, 10)
	} else {
		parts[2] = strconv.FormatInt(time.Now().Unix(), 10)
	}
	newData := strings.Join(parts, " ")
	_ = db.Model(&RData{}).Where("id = ?", soa.Records[0].ID).Update("data", newData).Error
}
