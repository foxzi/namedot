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

// cleanZoneSearch cleans up search query from URL protocols and paths
func cleanZoneSearch(query string) string {
	query = strings.TrimSpace(query)
	if query == "" {
		return ""
	}

	// Remove protocol (http://, https://)
	query = strings.TrimPrefix(query, "http://")
	query = strings.TrimPrefix(query, "https://")

	// Parse as URL to extract hostname
	if u, err := url.Parse("http://" + query); err == nil {
		query = u.Hostname()
	}

	// Remove paths, query params, fragments
	if idx := strings.IndexAny(query, "/?#"); idx != -1 {
		query = query[:idx]
	}

	// Normalize to lowercase
	query = strings.ToLower(query)

	return query
}

func (s *Server) listZones(c *gin.Context) {
	// Get pagination and search parameters
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	perPage := 30
	offset := (page - 1) * perPage

	search := cleanZoneSearch(c.Query("search"))

	// Build query
	query := s.db.Model(&db.Zone{})
	if search != "" {
		query = query.Where("name LIKE ?", "%"+search+"%")
	}

	// Get total count
	var total int64
	query.Count(&total)

	// Get zones for current page
	var zones []db.Zone
	if err := query.Offset(offset).Limit(perPage).Find(&zones).Error; err != nil {
		c.String(http.StatusInternalServerError, s.tr(c, "Error loading zones"))
		return
	}

	// Calculate pagination
	totalPages := int((total + int64(perPage) - 1) / int64(perPage))

	// Build search form
	searchForm := fmt.Sprintf(`
	<div style="margin-bottom: 1rem;">
		<form hx-get="/admin/zones" hx-target="#zones-list" hx-swap="innerHTML" style="display: flex; gap: 0.5rem;">
			<input type="text" name="search" placeholder="`+s.tr(c, "Search zones (domain, URL, or name)...")+`" value="%s"
				style="flex: 1; padding: 0.5rem; border: 1px solid #cbd5e0; border-radius: 4px;">
			<button type="submit" class="btn">`+s.tr(c, "Search")+`</button>
			<button type="button" class="btn" style="background: #718096;"
				hx-get="/admin/zones" hx-target="#zones-list" hx-swap="innerHTML">
				`+s.tr(c, "Clear")+`
			</button>
		</form>
	</div>`, search)

	html := searchForm + `<table>
        <thead>
            <tr>
                <th>` + s.tr(c, "Zone Name") + `</th>
                <th>` + s.tr(c, "Records") + `</th>
                <th>` + s.tr(c, "Actions") + `</th>
            </tr>
        </thead>
        <tbody>`

	if len(zones) == 0 {
		if search != "" {
			html += `<tr><td colspan="3" class="empty-state">` + s.tr(c, "No zones found matching your search") + `</td></tr>`
		} else {
			html += `<tr><td colspan="3" class="empty-state">` + s.tr(c, "No zones found. Create your first zone!") + `</td></tr>`
		}
	} else {
		for _, zone := range zones {
			// Load the zone with preloaded RRSets and Records
			var zoneWithRecords db.Zone
			s.db.Preload("RRSets.Records").First(&zoneWithRecords, zone.ID)

			// Count total RData records across all RRSets
			recordCount := 0
			for _, rrset := range zoneWithRecords.RRSets {
				recordCount += len(rrset.Records)
			}

			html += fmt.Sprintf(`
            <tr>
                <td><strong>%s</strong></td>
                <td>%d `+s.tr(c, "Records")+`</td>
                <td class="actions">
                    <button class="btn btn-sm" hx-get="/admin/zones/%d/records" hx-target="#zones-list" hx-swap="innerHTML">
                        %s
                    </button>
                    <button class="btn btn-sm btn-danger"
                        hx-delete="/admin/zones/delete/%d"
                        hx-confirm="%s"
                        hx-target="closest tr"
                        hx-swap="outerHTML">
                        %s
                    </button>
                </td>
            </tr>`, zone.Name, recordCount, zone.ID, s.tr(c, "View Records"), zone.ID, s.trf(c, "Delete zone %s?", zone.Name), s.tr(c, "Delete"))
		}
	}

	html += `</tbody></table>`

	// Add pagination if needed
	if totalPages > 1 {
		html += `<div style="display: flex; justify-content: center; gap: 0.5rem; margin-top: 1rem; flex-wrap: wrap;">`

		// Previous button
		if page > 1 {
			html += fmt.Sprintf(`<button class="btn btn-sm" hx-get="/admin/zones?page=%d&search=%s" hx-target="#zones-list" hx-swap="innerHTML">« `+s.tr(c, "Prev")+`</button>`, page-1, url.QueryEscape(search))
		}

		// Page numbers
		for i := 1; i <= totalPages; i++ {
			if i == page {
				html += fmt.Sprintf(`<button class="btn btn-sm" style="background: #667eea; color: white;">%d</button>`, i)
			} else if i == 1 || i == totalPages || (i >= page-2 && i <= page+2) {
				html += fmt.Sprintf(`<button class="btn btn-sm" hx-get="/admin/zones?page=%d&search=%s" hx-target="#zones-list" hx-swap="innerHTML">%d</button>`, i, url.QueryEscape(search), i)
			} else if i == page-3 || i == page+3 {
				html += `<span style="padding: 0.25rem 0.5rem;">...</span>`
			}
		}

		// Next button
		if page < totalPages {
			html += fmt.Sprintf(`<button class="btn btn-sm" hx-get="/admin/zones?page=%d&search=%s" hx-target="#zones-list" hx-swap="innerHTML">`+s.tr(c, "Next")+` »</button>`, page+1, url.QueryEscape(search))
		}

		html += `</div>`
		html += fmt.Sprintf(`<div style="text-align: center; margin-top: 0.5rem; color: #718096; font-size: 0.875rem;">`+s.tr(c, "Page %d of %d")+` (%d `+s.tr(c, "total")+`)</div>`, page, totalPages, total)
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(http.StatusOK, html)
}

func (s *Server) newZoneForm(c *gin.Context) {
    html := `
    <div style="background: #f7fafc; padding: 1rem; border-radius: 4px; margin-bottom: 1rem;">
        <h3>` + s.tr(c, "Create New Zone") + `</h3>
        <form hx-post="/admin/zones" hx-target="#zones-list" hx-swap="innerHTML" style="display: flex; gap: 1rem; align-items: end; margin-top: 1rem;">
            <div style="flex: 1;">
                <label>` + s.tr(c, "Zone Name") + `</label>
                <input type="text" name="name" placeholder="example.com" required
                    style="width: 100%; padding: 0.5rem; border: 1px solid #cbd5e0; border-radius: 4px;">
            </div>
            <button type="submit" class="btn">` + s.tr(c, "Create") + `</button>
            <button type="button" class="btn" style="background: #718096;"
                hx-get="/admin/zones" hx-target="#zones-list" hx-swap="innerHTML">
                ` + s.tr(c, "Cancel") + `
            </button>
        </form>
    </div>
    <div hx-get="/admin/zones" hx-trigger="load" hx-swap="innerHTML"></div>
    `
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(http.StatusOK, html)
}

func (s *Server) createZone(c *gin.Context) {
	name := c.PostForm("name")
    if name == "" {
        c.String(http.StatusBadRequest, `<div class="error">`+s.tr(c, "Zone name is required")+`</div>`)
        return
    }

	// Normalize zone name: lowercase and trailing dot
	name = strings.ToLower(strings.TrimSpace(name))
	if !strings.HasSuffix(name, ".") {
		name += "."
	}

	zone := db.Zone{Name: name}
    if err := s.db.Create(&zone).Error; err != nil {
        c.String(http.StatusInternalServerError, fmt.Sprintf(`<div class="error">`+s.tr(c, "Error creating zone: %s")+`</div>`, err.Error()))
        return
    }

	// Return updated zones list
	s.listZones(c)
}

func (s *Server) deleteZone(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
    if err != nil {
        c.Status(http.StatusBadRequest)
        return
    }

    if err := s.db.Delete(&db.Zone{}, id).Error; err != nil {
        c.String(http.StatusInternalServerError, s.tr(c, "Error deleting zone"))
        return
    }

    c.Status(http.StatusOK)
}

func (s *Server) editZoneForm(c *gin.Context) {
	// Placeholder for edit functionality
	c.String(http.StatusOK, "Edit zone form")
}

func (s *Server) updateZone(c *gin.Context) {
	// Placeholder for update functionality
	c.Status(http.StatusOK)
}
