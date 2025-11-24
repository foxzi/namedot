package web

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"namedot/internal/db"
)

func (s *Server) editRecordForm(c *gin.Context) {
	recordID, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.String(http.StatusBadRequest, s.tr(c, "Invalid record ID"))
		return
	}

	var record db.RData
	if err := s.db.First(&record, recordID).Error; err != nil {
		c.String(http.StatusNotFound, s.tr(c, "Record not found"))
		return
	}

	// Load associated RRSet
	var rrset db.RRSet
	if err := s.db.First(&rrset, record.RRSetID).Error; err != nil {
		c.String(http.StatusNotFound, s.tr(c, "RRSet not found"))
		return
	}

	// Get values with nil checks
	country := ""
	if record.Country != nil {
		country = *record.Country
	}
	continent := ""
	if record.Continent != nil {
		continent = *record.Continent
	}
	asn := 0
	if record.ASN != nil {
		asn = *record.ASN
	}
	subnet := ""
	if record.Subnet != nil {
		subnet = *record.Subnet
	}
	// For MX records, split priority and target for a cleaner edit experience
	mxPriority := 10
	dataValue := record.Data
	if strings.EqualFold(rrset.Type, "MX") {
		p, host := splitMXData(record.Data)
		mxPriority = p
		dataValue = host
	}

	html := fmt.Sprintf(`
    <div style="background: #f7fafc; padding: 1rem; border-radius: 4px; margin-bottom: 1rem;">
        <h3>%s</h3>
        <form hx-put="/admin/records/%d" hx-target="#zones-list" hx-swap="innerHTML"
            style="display: grid; grid-template-columns: 1fr 1fr; gap: 1rem; margin-top: 1rem;">

            <div>
                <label>%s</label>
                <input type="text" name="name" value="%s" required readonly
                    style="width: 100%%; padding: 0.5rem; border: 1px solid #cbd5e0; border-radius: 4px; background: #f7fafc;">
                <small style="color: #718096;">%s</small>
            </div>

            <div>
                <label>%s</label>
                <input type="text" name="type" value="%s" readonly
                    style="width: 100%%; padding: 0.5rem; border: 1px solid #cbd5e0; border-radius: 4px; background: #f7fafc;">
                <small style="color: #718096;">%s</small>
            </div>

            <div>
                <label>%s</label>
                <input type="number" name="ttl" value="%d" required
                    style="width: 100%%; padding: 0.5rem; border: 1px solid #cbd5e0; border-radius: 4px;">
            </div>

            <div>
                <label>%s</label>
                <input type="text" name="data" value="%s" required
                    style="width: 100%%; padding: 0.5rem; border: 1px solid #cbd5e0; border-radius: 4px;">
            </div>

            <div id="mx-priority-wrapper" style="grid-column: span 2; display: %s;">
                <label>%s</label>
                <input type="number" name="mx_priority" value="%d" min="0"
                    style="width: 100%%; max-width: 200px; padding: 0.5rem; border: 1px solid #cbd5e0; border-radius: 4px;">
                <small style="color: #718096;">%s</small>
            </div>

            <div style="grid-column: span 2;">
                <strong>%s</strong>
            </div>

            <div>
                <label>%s</label>
                <input type="text" name="country" value="%s" placeholder="RU" maxlength="2"
                    style="width: 100%%; padding: 0.5rem; border: 1px solid #cbd5e0; border-radius: 4px;">
            </div>

            <div>
                <label>%s</label>
                <input type="text" name="continent" value="%s" placeholder="EU" maxlength="2"
                    style="width: 100%%; padding: 0.5rem; border: 1px solid #cbd5e0; border-radius: 4px;">
            </div>

            <div>
                <label>%s</label>
                <input type="number" name="asn" value="%d" placeholder="65001"
                    style="width: 100%%; padding: 0.5rem; border: 1px solid #cbd5e0; border-radius: 4px;">
            </div>

            <div>
                <label>%s</label>
                <input type="text" name="subnet" value="%s" placeholder="10.0.0.0/8"
                    style="width: 100%%; padding: 0.5rem; border: 1px solid #cbd5e0; border-radius: 4px;">
            </div>

			<input type="hidden" name="zone_id" value="%d">
			<input type="hidden" name="rrset_id" value="%d">

            <div style="grid-column: span 2; display: flex; gap: 1rem;">
                <button type="submit" class="btn">%s</button>
                <button type="button" class="btn" style="background: #718096;"
                    hx-get="/admin/zones/%d/records" hx-target="#zones-list" hx-swap="innerHTML">
                    %s
                </button>
            </div>
        </form>
    </div>`,
		s.tr(c, "Edit Record"),
		recordID,
		s.tr(c, "Name"),
		rrset.Name,
		s.tr(c, "Name cannot be changed"),
		s.tr(c, "Type"),
		rrset.Type,
		s.tr(c, "Type cannot be changed"),
		s.tr(c, "TTL (seconds)"),
		rrset.TTL,
		s.tr(c, "Data (IP/Value)"),
		dataValue,
		displayForMX(rrset.Type),
		s.tr(c, "MX Priority"),
		mxPriority,
		s.tr(c, "Lower value = higher priority (only for MX)"),
		s.tr(c, "GeoIP Targeting (optional)"),
		s.tr(c, "Country Code"),
		country,
		s.tr(c, "Continent Code"),
		continent,
		s.tr(c, "ASN"),
		asn,
		s.tr(c, "Subnet"),
		subnet,
		rrset.ZoneID,
		rrset.ID,
		s.tr(c, "Update Record"),
		rrset.ZoneID,
		s.tr(c, "Cancel"),
	)

	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(http.StatusOK, html)
}

func (s *Server) updateRecord(c *gin.Context) {
	recordID, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.String(http.StatusBadRequest, s.tr(c, "Invalid record ID"))
		return
	}

	var record db.RData
	if err := s.db.First(&record, recordID).Error; err != nil {
		c.String(http.StatusNotFound, s.tr(c, "Record not found"))
		return
	}

	// Get form data
	data := c.PostForm("data")
	ttlStr := c.PostForm("ttl")
	country := c.PostForm("country")
	continent := c.PostForm("continent")
	asnStr := c.PostForm("asn")
	subnet := c.PostForm("subnet")
	mxPriorityStr := c.PostForm("mx_priority")
	zoneIDStr := c.PostForm("zone_id")
	rrsetIDStr := c.PostForm("rrset_id")

	if data == "" {
		c.String(http.StatusBadRequest, `<div class="error">`+s.tr(c, "Data is required")+`</div>`)
		return
	}

	ttl, _ := strconv.Atoi(ttlStr)
	if ttl <= 0 {
		ttl = 300
	}

	asn := 0
	if asnStr != "" {
		asn, _ = strconv.Atoi(asnStr)
	}

	mxPriority := 10
	if mxPriorityStr != "" {
		if p, err := strconv.Atoi(mxPriorityStr); err == nil && p >= 0 {
			mxPriority = p
		}
	}

	// If this RRSet is CNAME and data is "@", store apex FQDN
	var rrset db.RRSet
	rrsetID, _ := strconv.ParseUint(rrsetIDStr, 10, 32)
	if err := s.db.First(&rrset, rrsetID).Error; err == nil {
		var zone db.Zone
		_ = s.db.First(&zone, rrset.ZoneID).Error
		if strings.EqualFold(rrset.Type, "CNAME") && strings.TrimSpace(data) == "@" {
			// Need zone name for apex
			if zone.ID != 0 {
				data = toFQDN("@", zone.Name)
			}
		}
		if strings.EqualFold(rrset.Type, "MX") {
			data = combineMXData(data, mxPriority, zone.Name)
		}
	}

	// Update record data
	record.Data = data
	record.Country = stringPtr(country)
	record.Continent = stringPtr(continent)
	record.ASN = intPtr(asn)
	record.Subnet = stringPtr(subnet)

	if err := s.db.Save(&record).Error; err != nil {
		c.String(http.StatusInternalServerError, fmt.Sprintf(s.tr(c, "Error updating record: %s"), err.Error()))
		return
	}

	// Update RRSet TTL if changed
	if err := s.db.First(&rrset, rrsetID).Error; err == nil {
		if uint32(ttl) != rrset.TTL {
			rrset.TTL = uint32(ttl)
			if err := s.db.Save(&rrset).Error; err != nil {
				c.String(http.StatusInternalServerError, fmt.Sprintf(s.tr(c, "Error updating TTL: %s"), err.Error()))
				return
			}
		}
	}

	// Return updated records list
	zoneID, _ := strconv.ParseUint(zoneIDStr, 10, 32)
	c.Params = append(c.Params, gin.Param{Key: "id", Value: fmt.Sprintf("%d", zoneID)})
	s.listRecords(c)
}

func displayForMX(t string) string {
	if strings.EqualFold(t, "MX") {
		return "block"
	}
	return "none"
}
