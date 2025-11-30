package web

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"namedot/internal/db"
)

func (s *Server) listTemplates(c *gin.Context) {
	var templates []db.Template
    if err := s.db.Preload("Records").Find(&templates).Error; err != nil {
        c.String(http.StatusInternalServerError, s.tr(c, "Error loading templates"))
        return
    }

    html := `<table>
        <thead>
            <tr>
                <th>` + s.tr(c, "Template Name") + `</th>
                <th>` + s.tr(c, "Description") + `</th>
                <th>` + s.tr(c, "Records") + `</th>
                <th>` + s.tr(c, "Actions") + `</th>
            </tr>
        </thead>
        <tbody>`

    if len(templates) == 0 {
        html += `<tr><td colspan="4" class="empty-state">` + s.tr(c, "No templates found. Create your first template!") + `</td></tr>`
    } else {
        for _, tpl := range templates {
            html += fmt.Sprintf(`
            <tr>
                <td><strong>%s</strong></td>
                <td>%s</td>
                <td>%d `+s.tr(c, "Records")+`</td>
                <td class="actions">
                    <button class="btn btn-sm" hx-get="/admin/templates/%d/view" hx-target="#templates-content" hx-swap="innerHTML">
                        %s
                    </button>
                    <button class="btn btn-sm" hx-get="/admin/templates/%d/edit" hx-target="#templates-content" hx-swap="innerHTML">
                        %s
                    </button>
                    <button class="btn btn-sm btn-danger"
                        hx-delete="/admin/templates/%d"
                        hx-confirm="%s"
                        hx-target="closest tr"
                        hx-swap="outerHTML">
                        %s
                    </button>
                </td>
            </tr>`, tpl.Name, tpl.Description, len(tpl.Records), tpl.ID, s.tr(c, "View"), tpl.ID, s.tr(c, "Edit"), tpl.ID, s.trf(c, "Delete template '%s'?", tpl.Name), s.tr(c, "Delete"))
        }
    }

	html += `</tbody></table>`
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(http.StatusOK, html)
}

func (s *Server) newTemplateForm(c *gin.Context) {
    html := `
    <div style="background: #f7fafc; padding: 1.5rem; border-radius: 4px; margin-bottom: 1rem;">
        <h3>` + s.tr(c, "Create New Template") + `</h3>
        <form hx-post="/admin/templates" hx-target="#templates-content" hx-swap="innerHTML" style="margin-top: 1rem;">
            <div style="display: grid; gap: 1rem;">
                <div>
                    <label>` + s.tr(c, "Template Name") + `</label>
                    <input type="text" name="name" placeholder="e.g., Mail Server" required
                        style="width: 100%; padding: 0.5rem; border: 1px solid #cbd5e0; border-radius: 4px;">
                </div>
                <div>
                    <label>` + s.tr(c, "Description") + `</label>
                    <textarea name="description" rows="2" placeholder="` + s.tr(c, "Brief description of this template") + `"
                        style="width: 100%; padding: 0.5rem; border: 1px solid #cbd5e0; border-radius: 4px;"></textarea>
                </div>
                <div style="display: flex; gap: 1rem;">
                    <button type="submit" class="btn">` + s.tr(c, "Create Template") + `</button>
                    <button type="button" class="btn" style="background: #718096;"
                        hx-get="/admin/templates" hx-target="#templates-content" hx-swap="innerHTML">
                        ` + s.tr(c, "Cancel") + `
                    </button>
                </div>
            </div>
        </form>
    </div>
    <div hx-get="/admin/templates" hx-trigger="load" hx-swap="innerHTML"></div>
    `
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(http.StatusOK, html)
}

func (s *Server) createTemplate(c *gin.Context) {
	name := c.PostForm("name")
	description := c.PostForm("description")

    if name == "" {
        c.String(http.StatusBadRequest, `<div class="error">`+s.tr(c, "Template name is required")+`</div>`)
        return
    }

	template := db.Template{
		Name:        name,
		Description: description,
	}

    if err := s.db.Create(&template).Error; err != nil {
        c.String(http.StatusInternalServerError, fmt.Sprintf(`<div class="error">`+s.tr(c, "Error creating template: %s")+`</div>`, err.Error()))
        return
    }

	// Redirect to edit to add records
	c.Header("HX-Redirect", fmt.Sprintf("/admin/templates/%d/edit", template.ID))
	c.Status(http.StatusOK)
}

func (s *Server) viewTemplate(c *gin.Context) {
    id, err := strconv.ParseUint(c.Param("id"), 10, 32)
    if err != nil {
        c.String(http.StatusBadRequest, s.tr(c, "Invalid template ID"))
        return
    }

	var template db.Template
    if err := s.db.Preload("Records").First(&template, id).Error; err != nil {
        c.String(http.StatusNotFound, s.tr(c, "Template not found"))
        return
    }

    html := fmt.Sprintf(`
    <div style="margin-bottom: 1rem;">
        <button class="btn" style="background: #718096;" hx-get="/admin/templates" hx-target="#templates-content" hx-swap="innerHTML">
            %s
        </button>
    </div>
    <div style="background: white; padding: 1.5rem; border-radius: 4px;">
        <h2>%s</h2>
        <p style="color: #718096; margin-bottom: 1.5rem;">%s</p>

        <h3 style="margin-bottom: 1rem;">%s</h3>`, s.tr(c, "← Back to Templates"), template.Name, template.Description, s.tr(c, "Template Records"))

	if len(template.Records) == 0 {
        html += `<p style="color: #718096;">` + s.tr(c, "No records in this template.") + `</p>`
	} else {
		html += `<table style="margin-top: 1rem;">
			<thead>
				<tr>
					<th>Name</th>
					<th>Type</th>
					<th>TTL</th>
					<th>Data</th>
					<th>GeoIP</th>
				</tr>
			</thead>
			<tbody>`

		for _, rec := range template.Records {
			geoInfo := "Default"
            if rec.Country != nil && *rec.Country != "" {
                geoInfo = s.trf(c, "Country: %s", *rec.Country)
            } else if rec.Continent != nil && *rec.Continent != "" {
                geoInfo = s.trf(c, "Continent: %s", *rec.Continent)
			} else if rec.ASN != nil && *rec.ASN != 0 {
				geoInfo = fmt.Sprintf("ASN: %d", *rec.ASN)
			} else if rec.Subnet != nil && *rec.Subnet != "" {
				geoInfo = fmt.Sprintf("Subnet: %s", *rec.Subnet)
			}

			html += fmt.Sprintf(`
				<tr>
					<td><code>%s</code></td>
					<td><span style="background: #667eea; color: white; padding: 0.25rem 0.5rem; border-radius: 4px; font-size: 0.75rem;">%s</span></td>
					<td>%d</td>
					<td><code>%s</code></td>
					<td><em>%s</em></td>
				</tr>`, rec.Name, rec.Type, rec.TTL, rec.Data, geoInfo)
		}

		html += `</tbody></table>`
	}

	html += `</div>`
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(http.StatusOK, html)
}

func (s *Server) editTemplateForm(c *gin.Context) {
    id, err := strconv.ParseUint(c.Param("id"), 10, 32)
    if err != nil {
        c.String(http.StatusBadRequest, s.tr(c, "Invalid template ID"))
        return
    }

    var template db.Template
    if err := s.db.Preload("Records").First(&template, id).Error; err != nil {
        c.String(http.StatusNotFound, s.tr(c, "Template not found"))
        return
    }

    html := fmt.Sprintf(`
    <!-- Help Banner -->
    <div style="background: linear-gradient(135deg, #667eea 0%%, #764ba2 100%%); color: white; padding: 1rem; border-radius: 4px; margin-bottom: 1rem; box-shadow: 0 2px 4px rgba(0,0,0,0.1);">
        <div style="display: flex; align-items: center; gap: 1rem;">
            <div style="font-size: 2rem;">📋</div>
            <div style="flex: 1;">
                <h4 style="margin: 0 0 0.25rem 0; color: white;">%s</h4>
                <p style="margin: 0; opacity: 0.9; font-size: 0.875rem;">
                    %s <code style="background: rgba(255,255,255,0.2); padding: 0.125rem 0.375rem; border-radius: 3px;">{domain}</code> %s
                </p>
            </div>
        </div>
    </div>

    <div style="background: #f7fafc; padding: 1.5rem; border-radius: 4px; margin-bottom: 1rem;">
        <h3>%s</h3>
        <form hx-put="/admin/templates/%d" hx-target="#templates-content" hx-swap="innerHTML" style="margin-top: 1rem;">
            <div style="display: grid; gap: 1rem;">
                <div>
                    <label>%s</label>
                    <input type="text" name="name" value="%s" required
                        style="width: 100%%; padding: 0.5rem; border: 1px solid #cbd5e0; border-radius: 4px;">
                </div>
                <div>
                    <label>%s</label>
                    <textarea name="description" rows="2"
                        style="width: 100%%; padding: 0.5rem; border: 1px solid #cbd5e0; border-radius: 4px;">%s</textarea>
                </div>
                <div style="display: flex; gap: 1rem;">
                    <button type="submit" class="btn">%s</button>
                    <button type="button" class="btn" style="background: #718096;"
                        hx-get="/admin/templates" hx-target="#templates-content" hx-swap="innerHTML">
                        %s
                    </button>
                </div>
            </div>
        </form>
    </div>

    <div style="background: white; padding: 1.5rem; border-radius: 4px; margin-bottom: 1rem;">
        <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 1rem;">
            <h3>%s</h3>
            <button type="button" class="btn btn-sm" hx-get="/admin/templates/%d/records/new" hx-target="#template-records" hx-swap="afterbegin">
                %s
            </button>
        </div>
        <div id="template-records">`,
        s.tr(c, "Template Placeholders Guide"),
        s.tr(c, "Use"),
        s.tr(c, "in Name and Data fields - it will be replaced with the actual domain when applying the template"),
        s.trf(c, "Edit Template: %s", template.Name), id,
        s.tr(c, "Template Name"), template.Name,
        s.tr(c, "Description"), template.Description,
        s.tr(c, "Update Template"), s.tr(c, "Cancel"),
        s.tr(c, "Template Records"), id, s.tr(c, "+ Add Record"))

    if len(template.Records) == 0 {
        html += `<p style="color: #718096;">` + s.tr(c, "No records yet. Add records to this template.") + `</p>`
    } else {
		html += `<table>
			<thead>
				<tr>
					<th>Name</th>
					<th>Type</th>
					<th>TTL</th>
					<th>Data</th>
					<th>GeoIP</th>
					<th>Actions</th>
				</tr>
			</thead>
			<tbody>`

		for _, rec := range template.Records {
			geoInfo := "Default"
            if rec.Country != nil && *rec.Country != "" {
                geoInfo = s.trf(c, "Country: %s", *rec.Country)
            } else if rec.Continent != nil && *rec.Continent != "" {
                geoInfo = s.trf(c, "Continent: %s", *rec.Continent)
            } else if rec.ASN != nil && *rec.ASN != 0 {
                geoInfo = s.trf(c, "ASN: %d", *rec.ASN)
            } else if rec.Subnet != nil && *rec.Subnet != "" {
                geoInfo = s.trf(c, "Subnet: %s", *rec.Subnet)
            }

			html += fmt.Sprintf(`
				<tr>
					<td><code>%s</code></td>
					<td><span style="background: #667eea; color: white; padding: 0.25rem 0.5rem; border-radius: 4px; font-size: 0.75rem;">%s</span></td>
					<td>%d</td>
					<td><code>%s</code></td>
					<td><em>%s</em></td>
					<td>
                    <button class="btn btn-sm btn-danger"
                        hx-delete="/admin/templates/records/%d"
                        hx-confirm="%s"
                        hx-target="closest tr"
                        hx-swap="outerHTML">
                        %s
                    </button>
                </td>
            </tr>`, rec.Name, rec.Type, rec.TTL, rec.Data, geoInfo, rec.ID, s.tr(c, "Delete this record?"), s.tr(c, "Delete"))
		}

		html += `</tbody></table>`
	}

	html += `</div></div>`
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(http.StatusOK, html)
}

func (s *Server) updateTemplate(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.String(http.StatusBadRequest, "Invalid template ID")
		return
	}

	var template db.Template
    if err := s.db.First(&template, id).Error; err != nil {
        c.String(http.StatusNotFound, s.tr(c, "Template not found"))
        return
    }

	name := c.PostForm("name")
	description := c.PostForm("description")

	if name == "" {
		c.String(http.StatusBadRequest, `<div class="error">Template name is required</div>`)
		return
	}

	template.Name = name
	template.Description = description

    if err := s.db.Save(&template).Error; err != nil {
        c.String(http.StatusInternalServerError, fmt.Sprintf(`<div class="error">`+s.tr(c, "Error updating template: %s")+`</div>`, err.Error()))
        return
    }

	s.editTemplateForm(c)
}

func (s *Server) deleteTemplate(c *gin.Context) {
    id, err := strconv.ParseUint(c.Param("id"), 10, 32)
    if err != nil {
        c.Status(http.StatusBadRequest)
        return
    }

    if err := s.db.Delete(&db.Template{}, id).Error; err != nil {
        c.String(http.StatusInternalServerError, s.tr(c, "Error deleting template"))
        return
    }

	c.Status(http.StatusOK)
}

func (s *Server) newTemplateRecordForm(c *gin.Context) {
	templateID := c.Param("id")

html := fmt.Sprintf(`
    <div style="background: #edf2f7; padding: 1.5rem; border-radius: 4px; margin-bottom: 1rem;">
        <div style="display: flex; gap: 1.5rem; align-items: flex-start;">
            <!-- Left side: Form -->
            <div style="flex: 2;">
                <h4 style="margin-bottom: 1rem;">%s</h4>
                <form hx-post="/admin/templates/%s/records" hx-target="#templates-content" hx-swap="innerHTML">

                    <!-- Basic DNS Record Fields -->
                    <div style="background: white; padding: 1rem; border-radius: 4px; margin-bottom: 1rem;">
                        <h5 style="margin-bottom: 0.75rem; color: #2d3748;">%s</h5>

                        <div style="display: grid; grid-template-columns: 2fr 1fr 1fr; gap: 0.75rem; margin-bottom: 0.75rem;">
                            <div>
                                <label style="display: block; margin-bottom: 0.25rem; font-size: 0.875rem; font-weight: 500;">%s</label>
                                <input type="text" name="name" placeholder="{domain}" required
                                    style="width: 100%%; padding: 0.5rem; border: 1px solid #cbd5e0; border-radius: 4px; font-family: monospace;">
                            </div>

                            <div>
                                <label style="display: block; margin-bottom: 0.25rem; font-size: 0.875rem; font-weight: 500;">Type</label>
                                <select name="type" required
                                    style="width: 100%%; padding: 0.5rem; border: 1px solid #cbd5e0; border-radius: 4px;">
                                    <option value="A">A</option>
                                    <option value="AAAA">AAAA</option>
                                    <option value="CNAME">CNAME</option>
                                    <option value="MX">MX</option>
                                    <option value="TXT">TXT</option>
                                    <option value="NS">NS</option>
                                    <option value="SOA">SOA</option>
                                    <option value="SRV">SRV</option>
                                </select>
                            </div>

                            <div>
                                <label style="display: block; margin-bottom: 0.25rem; font-size: 0.875rem; font-weight: 500;">TTL</label>
                                <input type="number" name="ttl" value="300" required
                                    style="width: 100%%; padding: 0.5rem; border: 1px solid #cbd5e0; border-radius: 4px;">
                            </div>
                        </div>

                        <div>
                            <label style="display: block; margin-bottom: 0.25rem; font-size: 0.875rem; font-weight: 500;">%s</label>
                            <input type="text" name="data" placeholder="192.0.2.1" required
                                style="width: 100%%; padding: 0.5rem; border: 1px solid #cbd5e0; border-radius: 4px; font-family: monospace;">
                        </div>
                    </div>

                    <!-- GeoIP Fields -->
                    <div style="background: white; padding: 1rem; border-radius: 4px; margin-bottom: 1rem;">
                        <h5 style="margin-bottom: 0.75rem; color: #2d3748;">%s</h5>

                        <div style="display: grid; grid-template-columns: repeat(2, 1fr); gap: 0.75rem;">
                            <div>
                                <label style="display: block; margin-bottom: 0.25rem; font-size: 0.875rem;">%s</label>
                                <input type="text" name="country" maxlength="2" placeholder="US"
                                    style="width: 100%%; padding: 0.5rem; border: 1px solid #cbd5e0; border-radius: 4px; text-transform: uppercase;">
                            </div>

                            <div>
                                <label style="display: block; margin-bottom: 0.25rem; font-size: 0.875rem;">%s</label>
                                <input type="text" name="continent" maxlength="2" placeholder="EU"
                                    style="width: 100%%; padding: 0.5rem; border: 1px solid #cbd5e0; border-radius: 4px; text-transform: uppercase;">
                            </div>

                            <div>
                                <label style="display: block; margin-bottom: 0.25rem; font-size: 0.875rem;">ASN</label>
                                <input type="number" name="asn" placeholder="65001"
                                    style="width: 100%%; padding: 0.5rem; border: 1px solid #cbd5e0; border-radius: 4px;">
                            </div>

                            <div>
                                <label style="display: block; margin-bottom: 0.25rem; font-size: 0.875rem;">Subnet</label>
                                <input type="text" name="subnet" placeholder="10.0.0.0/8"
                                    style="width: 100%%; padding: 0.5rem; border: 1px solid #cbd5e0; border-radius: 4px;">
                            </div>
                        </div>
                    </div>

                    <div style="display: flex; gap: 0.75rem;">
                        <button type="submit" class="btn">%s</button>
                        <button type="button" class="btn" style="background: #718096;"
                            hx-get="/admin/templates/%s/edit" hx-target="#templates-content" hx-swap="innerHTML">
                            %s
                        </button>
                    </div>
                </form>
            </div>

            <!-- Right side: Help -->
            <div style="flex: 1; background: #fff3cd; border: 1px solid #ffc107; border-radius: 4px; padding: 1rem;">
                <h5 style="margin: 0 0 0.75rem 0; color: #856404; display: flex; align-items: center; gap: 0.5rem;">
                    <span style="font-size: 1.25rem;">💡</span> %s
                </h5>

                <div style="font-size: 0.875rem; color: #856404; line-height: 1.5;">
                    <p style="margin: 0 0 0.75rem 0;"><strong>%s:</strong></p>
                    <ul style="margin: 0 0 1rem 1.25rem; padding: 0;">
                        <li style="margin-bottom: 0.5rem;">
                            <code style="background: #fff; padding: 0.125rem 0.25rem; border-radius: 2px;">{domain}</code>
                            → example.com
                        </li>
                    </ul>

                    <p style="margin: 0 0 0.5rem 0;"><strong>%s:</strong></p>
                    <div style="background: white; padding: 0.5rem; border-radius: 4px; font-family: monospace; font-size: 0.75rem; margin-bottom: 0.75rem;">
                        <div>Name: <strong>mail.{domain}</strong></div>
                        <div>Type: A</div>
                        <div>Data: 192.0.2.10</div>
                        <div style="margin-top: 0.5rem; padding-top: 0.5rem; border-top: 1px dashed #ccc;">
                            ↓ %s example.com
                        </div>
                        <div style="color: #16a34a;">mail.example.com → 192.0.2.10</div>
                    </div>

                    <p style="margin: 0 0 0.5rem 0;"><strong>MX %s:</strong></p>
                    <div style="background: white; padding: 0.5rem; border-radius: 4px; font-family: monospace; font-size: 0.75rem;">
                        <div>Name: <strong>{domain}</strong></div>
                        <div>Type: MX</div>
                        <div>Data: <strong>10 mail.{domain}</strong></div>
                    </div>
                </div>
            </div>
        </div>
    </div>`,
    s.tr(c, "Add Template Record"),
    templateID,
    s.tr(c, "DNS Record"),
    s.tr(c, "Name"),
    s.tr(c, "Data"),
    s.tr(c, "GeoIP Targeting (optional)"),
    s.tr(c, "Country Code"),
    s.tr(c, "Continent Code"),
    s.tr(c, "Add Record"),
    templateID,
    s.tr(c, "Cancel"),
    s.tr(c, "Help"),
    s.tr(c, "Placeholders"),
    s.tr(c, "Example"),
    s.tr(c, "Applied to"),
    s.tr(c, "record"))

	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(http.StatusOK, html)
}

func (s *Server) createTemplateRecord(c *gin.Context) {
    templateID, err := strconv.ParseUint(c.Param("id"), 10, 32)
    if err != nil {
        c.String(http.StatusBadRequest, s.tr(c, "Invalid template ID"))
        return
    }

	name := c.PostForm("name")
	recType := c.PostForm("type")
	data := c.PostForm("data")
	ttlStr := c.PostForm("ttl")
	country := c.PostForm("country")
	continent := c.PostForm("continent")
	asnStr := c.PostForm("asn")
	subnet := c.PostForm("subnet")

    if name == "" || recType == "" || data == "" {
        c.String(http.StatusBadRequest, `<div class="error">`+s.tr(c, "Name, type, and data are required")+`</div>`)
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

	record := db.TemplateRecord{
		TemplateID: uint(templateID),
		Name:       name,
		Type:       recType,
		TTL:        uint32(ttl),
		Data:       data,
		Country:    stringPtr(country),
		Continent:  stringPtr(continent),
		ASN:        intPtr(asn),
		Subnet:     stringPtr(subnet),
	}

    if err := s.db.Create(&record).Error; err != nil {
        c.String(http.StatusInternalServerError, fmt.Sprintf(s.tr(c, "Error creating record: %s"), err.Error()))
        return
    }

	// Return to edit form
	c.Params = append(c.Params, gin.Param{Key: "id", Value: fmt.Sprintf("%d", templateID)})
	s.editTemplateForm(c)
}

func (s *Server) deleteTemplateRecord(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.Status(http.StatusBadRequest)
		return
	}

    if err := s.db.Delete(&db.TemplateRecord{}, id).Error; err != nil {
        c.String(http.StatusInternalServerError, s.tr(c, "Error deleting record"))
        return
    }

	c.Status(http.StatusOK)
}

// Apply template to zone
func (s *Server) applyTemplateForm(c *gin.Context) {
	templateID := c.Param("id")
	zoneID := c.Query("zone_id")

	var template db.Template
	tid, _ := strconv.ParseUint(templateID, 10, 32)
    if err := s.db.Preload("Records").First(&template, tid).Error; err != nil {
        c.String(http.StatusNotFound, s.tr(c, "Template not found"))
        return
    }

	var zone db.Zone
	zid, _ := strconv.ParseUint(zoneID, 10, 32)
    if err := s.db.First(&zone, zid).Error; err != nil {
        c.String(http.StatusNotFound, s.tr(c, "Zone not found"))
        return
    }

	// Extract domain from zone name (remove trailing dot)
	domain := strings.TrimSuffix(zone.Name, ".")

html := fmt.Sprintf(`
    <div style="background: #f7fafc; padding: 1.5rem; border-radius: 4px;">
        <h3>%s</h3>
        <p style="color: #718096; margin-bottom: 1rem;">%s</p>
        <p style="color: #718096; margin-bottom: 1rem;">%s</p>

        <div style="background: white; padding: 1rem; border-radius: 4px; margin-bottom: 1rem; max-height: 300px; overflow-y: auto;">
            <table style="font-size: 0.875rem;">
                <thead>
                    <tr><th>%s</th><th>%s</th><th>%s</th><th>%s</th></tr>
                </thead>
                <tbody>`, s.trf(c, "Apply Template: %s", template.Name), s.trf(c, "Zone: %s", zone.Name), s.trf(c, "This will create %d records:", len(template.Records)), s.tr(c, "Name"), s.tr(c, "Type"), s.tr(c, "TTL"), s.tr(c, "Data"))

	for _, rec := range template.Records {
        // Preview with placeholders replaced
        previewName := strings.ReplaceAll(rec.Name, "{domain}", domain)
        if previewName == "@" {
            previewName = zone.Name
        } else if !strings.HasSuffix(previewName, ".") {
            previewName = previewName + "."
        }
        previewData := strings.ReplaceAll(rec.Data, "{domain}", domain)

		html += fmt.Sprintf(`
			<tr>
				<td><code>%s</code></td>
				<td>%s</td>
				<td>%d</td>
				<td><code>%s</code></td>
			</tr>`, previewName, rec.Type, rec.TTL, previewData)
	}

html += fmt.Sprintf(`
                </tbody>
            </table>
        </div>

        <form hx-post="/admin/templates/%s/apply?zone_id=%s" hx-target="#zones-list" hx-swap="innerHTML">
            <div style="display: flex; gap: 1rem;">
                <button type="submit" class="btn">%s</button>
                <button type="button" class="btn" style="background: #718096;"
                    hx-get="/admin/zones/%s/records" hx-target="#zones-list" hx-swap="innerHTML">
                    %s
                </button>
            </div>
        </form>
    </div>`, templateID, zoneID, s.tr(c, "Apply Template"), zoneID, s.tr(c, "Cancel"))

	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(http.StatusOK, html)
}

func (s *Server) applyTemplate(c *gin.Context) {
    templateID, err := strconv.ParseUint(c.Param("id"), 10, 32)
    if err != nil {
        c.String(http.StatusBadRequest, s.tr(c, "Invalid template ID"))
        return
    }

    zoneID, err := strconv.ParseUint(c.Query("zone_id"), 10, 32)
    if err != nil {
        c.String(http.StatusBadRequest, s.tr(c, "Invalid zone ID"))
        return
    }

	var template db.Template
    if err := s.db.Preload("Records").First(&template, templateID).Error; err != nil {
        c.String(http.StatusNotFound, s.tr(c, "Template not found"))
        return
    }

	var zone db.Zone
    if err := s.db.First(&zone, zoneID).Error; err != nil {
        c.String(http.StatusNotFound, s.tr(c, "Zone not found"))
        return
    }

	// Extract domain from zone name
	domain := strings.TrimSuffix(zone.Name, ".")

	// Apply each template record
	for _, tplRec := range template.Records {
		// Replace placeholders
		name := strings.ReplaceAll(tplRec.Name, "{domain}", domain)
		data := strings.ReplaceAll(tplRec.Data, "{domain}", domain)

		// Normalize name: lowercase and trailing dot
		name = strings.ToLower(strings.TrimSpace(name))
		if !strings.HasSuffix(name, ".") {
			name += "."
		}

		// Find or create RRSet
		var rrset db.RRSet
		result := s.db.Where("zone_id = ? AND name = ? AND type = ?", zoneID, name, tplRec.Type).First(&rrset)
		if result.Error != nil {
			rrset = db.RRSet{
				ZoneID: uint(zoneID),
				Name:   name,
				Type:   tplRec.Type,
				TTL:    tplRec.TTL,
			}
			if err := s.db.Create(&rrset).Error; err != nil {
				continue
			}
		}

		// Create record data
		record := db.RData{
			RRSetID:   rrset.ID,
			Data:      data,
			Country:   tplRec.Country,
			Continent: tplRec.Continent,
			ASN:       tplRec.ASN,
			Subnet:    tplRec.Subnet,
		}

		s.db.Create(&record)
	}

	// Return to zone records
	c.Params = append(c.Params, gin.Param{Key: "id", Value: fmt.Sprintf("%d", zoneID)})
	s.listRecords(c)
}
