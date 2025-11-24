package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"namedot/internal/db"
)

// Helper functions for pointer conversion
func stringPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func intPtr(i int) *int {
	if i == 0 {
		return nil
	}
	return &i
}

func (s *Server) listRecords(c *gin.Context) {
	zoneID, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.String(http.StatusBadRequest, s.tr(c, "Invalid zone ID"))
		return
	}

	var zone db.Zone
	if err := s.db.First(&zone, zoneID).Error; err != nil {
		c.String(http.StatusNotFound, s.tr(c, "Zone not found"))
		return
	}

	// Get pagination, search, and filter parameters
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	perPage := 30
	offset := (page - 1) * perPage

	search := strings.TrimSpace(c.Query("search"))
	filterType := strings.ToUpper(strings.TrimSpace(c.Query("type")))

	// Build query
	query := s.db.Model(&db.RRSet{}).Where("zone_id = ?", zoneID)
	if search != "" {
		query = query.Where("name LIKE ? OR type LIKE ?", "%"+search+"%", "%"+search+"%")
	}
	if filterType != "" && filterType != "ALL" {
		query = query.Where("type = ?", filterType)
	}

	// Get total count
	var total int64
	query.Count(&total)

	// Get rrsets for current page
	var rrsets []db.RRSet
	if err := query.Offset(offset).Limit(perPage).Preload("Records").Find(&rrsets).Error; err != nil {
		c.String(http.StatusInternalServerError, s.tr(c, "Error loading records"))
		return
	}

	// Calculate pagination
	totalPages := int((total + int64(perPage) - 1) / int64(perPage))

	// Build filter and search form
	recordTypes := []string{"ALL", "A", "AAAA", "CNAME", "MX", "TXT", "NS", "SOA", "SRV", "PTR", "CAA"}
	filterForm := fmt.Sprintf(`
	<div style="margin-bottom: 1rem; display: flex; gap: 0.5rem; flex-wrap: wrap;">
		<form hx-get="/admin/zones/%d/records" hx-target="#zones-list" hx-swap="innerHTML" style="display: flex; gap: 0.5rem; flex: 1;">
			<input type="text" name="search" placeholder="`+s.tr(c, "Search records...")+`" value="%s"
				style="flex: 1; padding: 0.5rem; border: 1px solid #cbd5e0; border-radius: 4px;">
			<select name="type" style="padding: 0.5rem; border: 1px solid #cbd5e0; border-radius: 4px;">`,
		zoneID, search)

	for _, rt := range recordTypes {
		selected := ""
		if rt == filterType || (filterType == "" && rt == "ALL") {
			selected = " selected"
		}
		label := rt
		if rt == "ALL" {
			label = s.tr(c, "All Types")
		}
		filterForm += fmt.Sprintf(`<option value="%s"%s>%s</option>`, rt, selected, label)
	}

	filterForm += `</select>
			<button type="submit" class="btn">` + s.tr(c, "Filter") + `</button>
			<button type="button" class="btn" style="background: #718096;"
				hx-get="/admin/zones/` + fmt.Sprintf("%d", zoneID) + `/records" hx-target="#zones-list" hx-swap="innerHTML">
				` + s.tr(c, "Clear") + `
			</button>
		</form>
	</div>`

	html := fmt.Sprintf(`
	<div style="margin-bottom: 1rem;">
		<button class="btn" style="background: #718096;" hx-get="/admin/zones" hx-target="#zones-list" hx-swap="innerHTML">
			%s
		</button>
		<h2 style="margin-top: 1rem;">%s</h2>
	</div>
	<div style="margin-bottom: 1rem; display: flex; gap: 0.5rem;">
		<button class="btn" hx-get="/admin/zones/%d/records/new" hx-target="#records-list" hx-swap="beforebegin">
			%s
		</button>
		<button class="btn" style="background: #48bb78;"
			onclick="showTemplateSelector(%d)">
			%s
		</button>
	</div>
	<div id="template-selector-%d"></div>
	%s
	<div id="records-list">`, s.tr(c, "← Back to Zones"), s.trf(c, "Records for %s", zone.Name), zoneID, s.tr(c, "+ Add Record"), zoneID, s.tr(c, "📋 Apply Template"), zoneID, filterForm)

	if len(rrsets) == 0 {
		if search != "" || filterType != "" {
			html += `<div class="empty-state">` + s.tr(c, "No records found matching your filters") + `</div>`
		} else {
			html += `<div class="empty-state">` + s.tr(c, "No records found. Add your first record!") + `</div>`
		}
	} else {
		html += `<table><thead><tr><th>` + s.tr(c, "Name") + `</th><th>` + s.tr(c, "Type") + `</th><th>` + s.tr(c, "TTL") + `</th><th>` + s.tr(c, "GeoIP") + `</th><th>` + s.tr(c, "Data") + `</th><th>` + s.tr(c, "Actions") + `</th></tr></thead><tbody>`

		for _, rr := range rrsets {
			for _, record := range rr.Records {
				geoInfo := "Default"
				if record.Country != nil && *record.Country != "" {
					geoInfo = s.trf(c, "Country: %s", *record.Country)
				} else if record.Continent != nil && *record.Continent != "" {
					geoInfo = s.trf(c, "Continent: %s", *record.Continent)
				} else if record.ASN != nil && *record.ASN != 0 {
					geoInfo = s.trf(c, "ASN: %d", *record.ASN)
				} else if record.Subnet != nil && *record.Subnet != "" {
					geoInfo = s.trf(c, "Subnet: %s", *record.Subnet)
				}

				html += fmt.Sprintf(`
				<tr>
					<td><strong>%s</strong></td>
					<td><span style="background: #667eea; color: white; padding: 0.25rem 0.5rem; border-radius: 4px; font-size: 0.75rem;">%s</span></td>
					<td>%d</td>
					<td><em>%s</em></td>
					<td><code>%s</code></td>
					<td class="actions">
					<button class="btn btn-sm"
						hx-get="/admin/records/%d/edit"
						hx-target="#zones-list"
						hx-swap="innerHTML">
						%s
					</button>
					<button class="btn btn-sm btn-danger"
						hx-delete="/admin/records/%d"
						hx-confirm="%s"
						hx-target="closest tr"
						hx-swap="outerHTML">
						%s
					</button>
				</td>
				</tr>`, rr.Name, rr.Type, rr.TTL, geoInfo, record.Data, record.ID, s.tr(c, "Edit"), record.ID, s.tr(c, "Delete this record?"), s.tr(c, "Delete"))
			}
		}

		html += `</tbody></table>`
	}

	// Add pagination if needed
	if totalPages > 1 {
		html += `<div style="display: flex; justify-content: center; gap: 0.5rem; margin-top: 1rem; flex-wrap: wrap;">`

		// Build pagination URL params
		params := fmt.Sprintf("search=%s&type=%s", url.QueryEscape(search), url.QueryEscape(filterType))

		// Previous button
		if page > 1 {
			html += fmt.Sprintf(`<button class="btn btn-sm" hx-get="/admin/zones/%d/records?page=%d&%s" hx-target="#zones-list" hx-swap="innerHTML">« `+s.tr(c, "Prev")+`</button>`, zoneID, page-1, params)
		}

		// Page numbers
		for i := 1; i <= totalPages; i++ {
			if i == page {
				html += fmt.Sprintf(`<button class="btn btn-sm" style="background: #667eea; color: white;">%d</button>`, i)
			} else if i == 1 || i == totalPages || (i >= page-2 && i <= page+2) {
				html += fmt.Sprintf(`<button class="btn btn-sm" hx-get="/admin/zones/%d/records?page=%d&%s" hx-target="#zones-list" hx-swap="innerHTML">%d</button>`, zoneID, i, params, i)
			} else if i == page-3 || i == page+3 {
				html += `<span style="padding: 0.25rem 0.5rem;">...</span>`
			}
		}

		// Next button
		if page < totalPages {
			html += fmt.Sprintf(`<button class="btn btn-sm" hx-get="/admin/zones/%d/records?page=%d&%s" hx-target="#zones-list" hx-swap="innerHTML">`+s.tr(c, "Next")+` »</button>`, zoneID, page+1, params)
		}

		html += `</div>`
		html += fmt.Sprintf(`<div style="text-align: center; margin-top: 0.5rem; color: #718096; font-size: 0.875rem;">`+s.tr(c, "Page %d of %d")+` (%d `+s.tr(c, "total")+`)</div>`, page, totalPages, total)
	}

	html += `</div>`
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(http.StatusOK, html)
}

func (s *Server) newRecordForm(c *gin.Context) {
	zoneID := c.Param("id")

	html := fmt.Sprintf(`
    <div style="background: #f7fafc; padding: 1rem; border-radius: 4px; margin-bottom: 1rem;">
        <h3>%s</h3>
        <form hx-post="/admin/zones/%s/records" hx-target="#zones-list" hx-swap="innerHTML"
            style="display: grid; grid-template-columns: 1fr 1fr; gap: 1rem; margin-top: 1rem;">

            <div>
                <label>%s</label>
                <input type="text" name="name" placeholder="www" required
                    style="width: 100%%; padding: 0.5rem; border: 1px solid #cbd5e0; border-radius: 4px;">
                <small style="color: #718096;">%s</small>
            </div>

            <div>
                <label>%s</label>
                <select name="type" required
                    style="width: 100%%; padding: 0.5rem; border: 1px solid #cbd5e0; border-radius: 4px;">
                    <option value="A">A - IPv4 Address</option>
                    <option value="AAAA">AAAA - IPv6 Address</option>
                    <option value="CNAME">CNAME - Canonical Name</option>
                    <option value="MX">MX - Mail Exchange</option>
                    <option value="TXT">TXT - Text Record</option>
                    <option value="NS">NS - Name Server</option>
                    <option value="SRV">SRV - Service Record</option>
                    <option value="PTR">PTR - Pointer Record</option>
                    <option value="CAA">CAA - Certificate Authority</option>
                    <option value="SOA">SOA - Start of Authority</option>
                </select>
            </div>

            <div>
                <label>%s</label>
                <input type="number" name="ttl" value="300" required
                    style="width: 100%%; padding: 0.5rem; border: 1px solid #cbd5e0; border-radius: 4px;">
            </div>

            <div>
                <label>%s</label>
                <input type="text" name="data" placeholder="192.0.2.1" required
                    style="width: 100%%; padding: 0.5rem; border: 1px solid #cbd5e0; border-radius: 4px;">
            </div>

            <div id="mx-priority-wrapper" style="grid-column: span 2;">
                <label>%s</label>
                <input type="number" name="mx_priority" value="10" min="0"
                    style="width: 100%%; max-width: 200px; padding: 0.5rem; border: 1px solid #cbd5e0; border-radius: 4px;">
                <small style="color: #718096;">%s</small>
            </div>

            <div style="grid-column: span 2;">
                <strong>%s</strong>
            </div>

            <div>
                <label>%s</label>
                <input type="text" name="country" placeholder="RU" maxlength="2"
                    style="width: 100%%; padding: 0.5rem; border: 1px solid #cbd5e0; border-radius: 4px;">
            </div>

            <div>
                <label>%s</label>
                <input type="text" name="continent" placeholder="EU" maxlength="2"
                    style="width: 100%%; padding: 0.5rem; border: 1px solid #cbd5e0; border-radius: 4px;">
            </div>

            <div>
                <label>%s</label>
                <input type="number" name="asn" placeholder="65001"
                    style="width: 100%%; padding: 0.5rem; border: 1px solid #cbd5e0; border-radius: 4px;">
            </div>

            <div>
                <label>%s</label>
                <input type="text" name="subnet" placeholder="10.0.0.0/8"
                    style="width: 100%%; padding: 0.5rem; border: 1px solid #cbd5e0; border-radius: 4px;">
            </div>

            <div style="grid-column: span 2; display: flex; gap: 1rem;">
                <button type="submit" class="btn">%s</button>
                <button type="button" class="btn" style="background: #718096;"
                    hx-get="/admin/zones/%s/records" hx-target="#zones-list" hx-swap="innerHTML">
                    %s
                </button>
            </div>
        </form>
    </div>`, s.tr(c, "Add New Record"), zoneID, s.tr(c, "Name"), s.tr(c, "Use '@' for zone apex"), s.tr(c, "Type"), s.tr(c, "TTL (seconds)"), s.tr(c, "Data (IP/Value)"), s.tr(c, "MX Priority"), s.tr(c, "Lower value = higher priority (only for MX)"), s.tr(c, "GeoIP Targeting (optional)"), s.tr(c, "Country Code"), s.tr(c, "Continent Code"), s.tr(c, "ASN"), s.tr(c, "Subnet"), s.tr(c, "Add Record"), zoneID, s.tr(c, "Cancel"))

	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(http.StatusOK, html)
}

func (s *Server) createRecord(c *gin.Context) {
	zoneID, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.String(http.StatusBadRequest, s.tr(c, "Invalid zone ID"))
		return
	}

	// Load zone for FQDN normalization
	var zone db.Zone
	if err := s.db.First(&zone, zoneID).Error; err != nil {
		c.String(http.StatusNotFound, s.tr(c, "Zone not found"))
		return
	}

	name := c.PostForm("name")
	recType := strings.ToUpper(c.PostForm("type"))
	data := c.PostForm("data")
	ttlStr := c.PostForm("ttl")
	mxPriorityStr := c.PostForm("mx_priority")
	country := c.PostForm("country")
	continent := c.PostForm("continent")
	asnStr := c.PostForm("asn")
	subnet := c.PostForm("subnet")

	if name == "" || recType == "" || data == "" {
		c.String(http.StatusBadRequest, `<div class="error">`+s.tr(c, "Name, type, and data are required")+`</div>`)
		return
	}

	// Normalize name to FQDN; handle @/empty as zone apex
	name = toFQDN(name, zone.Name)

	// For CNAME data, treat "@" as zone apex and store FQDN
	if strings.EqualFold(recType, "CNAME") && strings.TrimSpace(data) == "@" {
		data = toFQDN("@", zone.Name)
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

	// Find or create RRSet
	var rrset db.RRSet
	result := s.db.Where("zone_id = ? AND name = ? AND type = ?", zoneID, name, recType).First(&rrset)
	if result.Error != nil {
		// Create new RRSet
		rrset = db.RRSet{
			ZoneID: uint(zoneID),
			Name:   name,
			Type:   recType,
			TTL:    uint32(ttl),
		}
		if err := s.db.Create(&rrset).Error; err != nil {
			c.String(http.StatusInternalServerError, fmt.Sprintf(s.tr(c, "Error creating record set: %s"), err.Error()))
			return
		}
	}

	// Add record data
	if strings.EqualFold(recType, "MX") {
		data = combineMXData(data, mxPriority, zone.Name)
	}
	record := db.RData{
		RRSetID:   rrset.ID,
		Data:      data,
		Country:   stringPtr(country),
		Continent: stringPtr(continent),
		ASN:       intPtr(asn),
		Subnet:    stringPtr(subnet),
	}

	if err := s.db.Create(&record).Error; err != nil {
		c.String(http.StatusInternalServerError, fmt.Sprintf(s.tr(c, "Error creating record: %s"), err.Error()))
		return
	}

	// Ensure SOA exists/updated after change
	db.BumpSOASerialAuto(s.db, zone, s.cfg.SOA.AutoOnMissing, s.cfg.SOA.Primary, s.cfg.SOA.Hostmaster)

	// Return updated records list
	c.Params = append(c.Params, gin.Param{Key: "id", Value: fmt.Sprintf("%d", zoneID)})
	s.listRecords(c)
}

func (s *Server) deleteRecord(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.Status(http.StatusBadRequest)
		return
	}

	var record db.RData
	if err := s.db.First(&record, id).Error; err != nil {
		c.String(http.StatusNotFound, s.tr(c, "Record not found"))
		return
	}

	if err := s.db.Delete(&db.RData{}, id).Error; err != nil {
		c.String(http.StatusInternalServerError, s.tr(c, "Error deleting record"))
		return
	}

	// Ensure SOA exists/updated after change
	var rrset db.RRSet
	if err := s.db.First(&rrset, record.RRSetID).Error; err == nil {
		var zone db.Zone
		if err := s.db.First(&zone, rrset.ZoneID).Error; err == nil {
			db.BumpSOASerialAuto(s.db, zone, s.cfg.SOA.AutoOnMissing, s.cfg.SOA.Primary, s.cfg.SOA.Hostmaster)
		}
	}

	c.Status(http.StatusOK)
}

// toFQDN normalizes a relative name to FQDN within the given zone name.
// If name is empty or "@", returns the zone origin with trailing dot.
func toFQDN(name, zone string) string {
	n := strings.TrimSpace(strings.ToLower(name))
	// Treat trailing ".@" as convenience suffix for "relative to zone apex"
	if strings.HasSuffix(n, ".@") {
		n = strings.TrimSuffix(n, ".@")
	}
	z := strings.TrimSuffix(strings.ToLower(zone), ".")
	if n == "" || n == "@" {
		return z + "."
	}
	if strings.HasSuffix(n, ".") {
		return n
	}
	return n + "." + z + "."
}

// splitMXData extracts priority and target if present, otherwise returns defaults.
func splitMXData(data string) (int, string) {
	fields := strings.Fields(strings.TrimSpace(data))
	if len(fields) >= 2 {
		if p, err := strconv.Atoi(fields[0]); err == nil {
			return p, strings.Join(fields[1:], " ")
		}
	}
	return 10, strings.TrimSpace(data)
}

// combineMXData formats MX data with priority, unless data already includes priority.
func combineMXData(data string, priority int, zoneName string) string {
	d := strings.TrimSpace(data)
	if d == "" {
		return ""
	}
	fields := strings.Fields(d)
	if len(fields) >= 2 {
		if _, err := strconv.Atoi(fields[0]); err == nil {
			// Already contains priority, return normalized string
			return strings.Join(fields, " ")
		}
	}
	if d == "@" {
		d = toFQDN("@", zoneName)
	}
	if priority < 0 {
		priority = 0
	}
	return fmt.Sprintf("%d %s", priority, d)
}
