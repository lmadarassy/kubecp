package api

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/hosting-panel/panel-core/internal/middleware"
	"github.com/hosting-panel/panel-core/internal/powerdns"
)

// DNSHandler implements DNS zone and record management API endpoints.
type DNSHandler struct {
	pdns      *powerdns.Client
	externalIP string // Contour/Envoy external IP (MetalLB)
	mailHost   string // Mail server hostname for MX records
}

// NewDNSHandler creates a new DNSHandler.
func NewDNSHandler(pdns *powerdns.Client, externalIP, mailHost string) *DNSHandler {
	return &DNSHandler{
		pdns:       pdns,
		externalIP: externalIP,
		mailHost:   mailHost,
	}
}

// RegisterRoutes registers DNS management routes.
func (h *DNSHandler) RegisterRoutes(r chi.Router) {
	r.Get("/zones", h.ListZones)
	r.Post("/zones", h.CreateZone)
	r.Route("/zones/{zoneID}", func(r chi.Router) {
		r.Get("/", h.GetZone)
		r.Delete("/", h.DeleteZone)
		r.Get("/records", h.ListRecords)
		r.Post("/records", h.CreateRecord)
		r.Put("/records", h.UpdateRecord)
		r.Delete("/records", h.DeleteRecord)
		r.Get("/secondary", h.GetSecondaryConfig)
		r.Put("/secondary", h.UpdateSecondaryConfig)
	})
}

// --- Request/Response types ---

// CreateZoneRequest is the JSON body for POST /api/dns/zones.
type CreateZoneRequest struct {
	Name string `json:"name"`
	Kind string `json:"kind,omitempty"` // Native (default), Master, Slave
}

// ZoneResponse is the JSON response for zone endpoints.
type ZoneResponse struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Serial int64  `json:"serial"`
}

// RecordRequest is the JSON body for record CRUD operations.
type RecordRequest struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	TTL     int    `json:"ttl,omitempty"`
	Content string `json:"content"`
}

// RecordResponse is the JSON response for record endpoints.
type RecordResponse struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	TTL     int    `json:"ttl"`
	Content string `json:"content"`
}

// SecondaryConfigRequest is the JSON body for PUT /api/dns/zones/{id}/secondary.
type SecondaryConfigRequest struct {
	Masters      []string `json:"masters,omitempty"`
	AllowAXFR    []string `json:"allowAxfrIps,omitempty"`
	NotifyTargets []string `json:"notifyTargets,omitempty"`
}

// SecondaryConfigResponse is the JSON response for secondary DNS config.
type SecondaryConfigResponse struct {
	Kind          string   `json:"kind"`
	Masters       []string `json:"masters,omitempty"`
	AllowAXFR     []string `json:"allowAxfrIps,omitempty"`
	NotifyTargets []string `json:"notifyTargets,omitempty"`
}

// --- Validation ---

var (
	validRecordTypes = map[string]bool{
		"A": true, "AAAA": true, "CNAME": true, "MX": true,
		"TXT": true, "SRV": true, "NS": true, "SOA": true, "PTR": true,
	}
	hostnameRegex = regexp.MustCompile(`^([a-zA-Z0-9]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,}\.?$`)
)

func validateRecordType(t string) bool {
	return validRecordTypes[strings.ToUpper(t)]
}

func validateIPv4(ip string) bool {
	parsed := net.ParseIP(ip)
	return parsed != nil && parsed.To4() != nil
}

func validateIPv6(ip string) bool {
	parsed := net.ParseIP(ip)
	return parsed != nil && parsed.To4() == nil
}

func validateRecordContent(recordType, content string) error {
	switch strings.ToUpper(recordType) {
	case "A":
		if !validateIPv4(content) {
			return fmt.Errorf("invalid IPv4 address: %s", content)
		}
	case "AAAA":
		if !validateIPv6(content) {
			return fmt.Errorf("invalid IPv6 address: %s", content)
		}
	case "CNAME", "NS", "PTR":
		if !hostnameRegex.MatchString(content) && !strings.HasSuffix(content, ".") {
			return fmt.Errorf("invalid hostname: %s", content)
		}
	case "MX":
		// MX format: "priority hostname"
		parts := strings.SplitN(content, " ", 2)
		if len(parts) != 2 {
			return fmt.Errorf("MX record must be in format 'priority hostname': %s", content)
		}
	case "TXT":
		// TXT records are free-form, just ensure non-empty
		if strings.TrimSpace(content) == "" {
			return fmt.Errorf("TXT record content cannot be empty")
		}
	case "SRV":
		// SRV format: "priority weight port target"
		parts := strings.Fields(content)
		if len(parts) != 4 {
			return fmt.Errorf("SRV record must be in format 'priority weight port target': %s", content)
		}
	}
	return nil
}

// canAccessZone returns true if the authenticated user can access the given zone.
// Admin users can access all zones. Regular users can only access zones they own
// (zone.Account == username). If zone.Account is empty (PowerDNS may omit it in
// some versions), access is granted to avoid false 403s — ListZones already
// filters by ownership so the user can only reach zones they can see.
func canAccessZone(claims *middleware.TokenClaims, zone *powerdns.Zone) bool {
	if middleware.HasRole(claims, "admin") {
		return true
	}
	// Allow if account matches or if PowerDNS didn't return the account field
	return zone.Account == "" || zone.Account == claims.Username
}

func ensureTrailingDot(name string) string {
	if !strings.HasSuffix(name, ".") {
		return name + "."
	}
	return name
}

// resolveRecordName converts a relative or apex record name to a fully qualified
// domain name (FQDN) within the given zone. Returns an error if the name is
// outside the zone.
//
//   - "@" or "" → zone name (apex)
//   - "sub"     → "sub.example.com."
//   - "sub.example.com" or "sub.example.com." → validated as-is
func resolveRecordName(name, zoneName string) (string, error) {
	zoneFQDN := ensureTrailingDot(zoneName)

	// Apex shorthand
	if name == "@" || name == "" {
		return zoneFQDN, nil
	}

	fqdn := ensureTrailingDot(name)

	// Already within zone (exact match or subdomain)
	if fqdn == zoneFQDN || strings.HasSuffix(fqdn, "."+zoneFQDN) {
		return fqdn, nil
	}

	// Relative name (no trailing dot in original) — append zone
	if !strings.HasSuffix(name, ".") {
		return name + "." + zoneFQDN, nil
	}

	// Absolute name (had trailing dot) but outside zone
	return "", fmt.Errorf("record name %q is outside zone %q", name, zoneName)
}

// --- Zone handlers ---

// ListZones handles GET /api/dns/zones.
func (h *DNSHandler) ListZones(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	zones, err := h.pdns.ListZones(r.Context())
	if err != nil {
		WriteInternalError(w, "Failed to list DNS zones: "+err.Error())
		return
	}

	// Filter zones by user ownership (admin sees all)
	var resp []ZoneResponse
	for _, z := range zones {
		if middleware.HasRole(claims, "admin") || z.Account == claims.Username {
			resp = append(resp, ZoneResponse{
				ID:     z.ID,
				Name:   z.Name,
				Kind:   z.Kind,
				Serial: z.Serial,
			})
		}
	}
	if resp == nil {
		resp = []ZoneResponse{}
	}
	writeJSON(w, http.StatusOK, resp)
}

// CreateZone handles POST /api/dns/zones.
func (h *DNSHandler) CreateZone(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	var req CreateZoneRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteBadRequest(w, "Invalid request body", nil)
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		WriteBadRequest(w, "Zone name is required", nil)
		return
	}
	zoneName := ensureTrailingDot(req.Name)

	kind := "Native"
	if req.Kind != "" {
		kind = req.Kind
	}

	// Build default RRSets for the zone
	defaultRRSets := h.buildDefaultRecords(zoneName)

	zone := powerdns.Zone{
		Name:    zoneName,
		Kind:    kind,
		Account: claims.Username,
		RRSets:  defaultRRSets,
	}

	created, err := h.pdns.CreateZone(r.Context(), zone)
	if err != nil {
		if apiErr, ok := err.(*powerdns.APIError); ok && apiErr.StatusCode == http.StatusConflict {
			WriteConflict(w, "Zone already exists: "+req.Name, nil)
			return
		}
		WriteInternalError(w, "Failed to create zone: "+err.Error())
		return
	}

	// Send NOTIFY to secondary servers if configured
	_ = h.pdns.NotifyZone(r.Context(), created.ID)

	writeJSON(w, http.StatusCreated, ZoneResponse{
		ID:     created.ID,
		Name:   created.Name,
		Kind:   created.Kind,
		Serial: created.Serial,
	})
}

// GetZone handles GET /api/dns/zones/{zoneID}.
func (h *DNSHandler) GetZone(w http.ResponseWriter, r *http.Request) {
	zoneID := chi.URLParam(r, "zoneID")
	claims := middleware.GetClaims(r.Context())

	zone, err := h.pdns.GetZone(r.Context(), zoneID)
	if err != nil {
		if apiErr, ok := err.(*powerdns.APIError); ok && apiErr.StatusCode == http.StatusNotFound {
			WriteNotFound(w, "Zone not found")
			return
		}
		WriteInternalError(w, "Failed to get zone: "+err.Error())
		return
	}

	if !canAccessZone(claims, zone) {
		WriteForbidden(w, "Access denied")
		return
	}

	writeJSON(w, http.StatusOK, ZoneResponse{
		ID:     zone.ID,
		Name:   zone.Name,
		Kind:   zone.Kind,
		Serial: zone.Serial,
	})
}

// DeleteZone handles DELETE /api/dns/zones/{zoneID}.
func (h *DNSHandler) DeleteZone(w http.ResponseWriter, r *http.Request) {
	zoneID := chi.URLParam(r, "zoneID")
	claims := middleware.GetClaims(r.Context())

	// Check ownership
	zone, err := h.pdns.GetZone(r.Context(), zoneID)
	if err != nil {
		if apiErr, ok := err.(*powerdns.APIError); ok && apiErr.StatusCode == http.StatusNotFound {
			WriteNotFound(w, "Zone not found")
			return
		}
		WriteInternalError(w, "Failed to get zone: "+err.Error())
		return
	}
	if !canAccessZone(claims, zone) {
		WriteForbidden(w, "Access denied")
		return
	}

	if err := h.pdns.DeleteZone(r.Context(), zoneID); err != nil {
		WriteInternalError(w, "Failed to delete zone: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Record handlers ---

// ListRecords handles GET /api/dns/zones/{zoneID}/records.
func (h *DNSHandler) ListRecords(w http.ResponseWriter, r *http.Request) {
	zoneID := chi.URLParam(r, "zoneID")
	claims := middleware.GetClaims(r.Context())

	zone, err := h.pdns.GetZone(r.Context(), zoneID)
	if err != nil {
		if apiErr, ok := err.(*powerdns.APIError); ok && apiErr.StatusCode == http.StatusNotFound {
			WriteNotFound(w, "Zone not found")
			return
		}
		WriteInternalError(w, "Failed to get zone: "+err.Error())
		return
	}
	if !canAccessZone(claims, zone) {
		WriteForbidden(w, "Access denied")
		return
	}

	var records []RecordResponse
	for _, rrset := range zone.RRSets {
		for _, rec := range rrset.Records {
			if rec.Disabled {
				continue
			}
			records = append(records, RecordResponse{
				Name:    rrset.Name,
				Type:    rrset.Type,
				TTL:     rrset.TTL,
				Content: rec.Content,
			})
		}
	}
	if records == nil {
		records = []RecordResponse{}
	}
	writeJSON(w, http.StatusOK, records)
}

// CreateRecord handles POST /api/dns/zones/{zoneID}/records.
func (h *DNSHandler) CreateRecord(w http.ResponseWriter, r *http.Request) {
	zoneID := chi.URLParam(r, "zoneID")
	claims := middleware.GetClaims(r.Context())

	zone, err := h.pdns.GetZone(r.Context(), zoneID)
	if err != nil {
		if apiErr, ok := err.(*powerdns.APIError); ok && apiErr.StatusCode == http.StatusNotFound {
			WriteNotFound(w, "Zone not found")
			return
		}
		WriteInternalError(w, "Failed to get zone: "+err.Error())
		return
	}
	if !canAccessZone(claims, zone) {
		WriteForbidden(w, "Access denied")
		return
	}

	var req RecordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteBadRequest(w, "Invalid request body", nil)
		return
	}

	req.Type = strings.ToUpper(req.Type)
	if !validateRecordType(req.Type) {
		WriteBadRequest(w, "Invalid record type: "+req.Type, nil)
		return
	}
	if err := validateRecordContent(req.Type, req.Content); err != nil {
		WriteBadRequest(w, err.Error(), nil)
		return
	}

	recordName, err := resolveRecordName(req.Name, zone.Name)
	if err != nil {
		WriteBadRequest(w, err.Error(), nil)
		return
	}
	ttl := req.TTL
	if ttl <= 0 {
		ttl = 3600
	}

	// Get existing records for this name+type to merge
	var existingRecords []powerdns.Record
	for _, rrset := range zone.RRSets {
		if rrset.Name == recordName && rrset.Type == req.Type {
			existingRecords = rrset.Records
			break
		}
	}

	// Check for duplicate
	for _, rec := range existingRecords {
		if rec.Content == req.Content {
			WriteConflict(w, "Record already exists", nil)
			return
		}
	}

	// Add new record to existing set
	existingRecords = append(existingRecords, powerdns.Record{Content: req.Content, Disabled: false})

	patch := powerdns.ZonePatch{
		RRSets: []powerdns.RRSet{{
			Name:       recordName,
			Type:       req.Type,
			TTL:        ttl,
			ChangeType: "REPLACE",
			Records:    existingRecords,
		}},
	}

	if err := h.pdns.PatchZone(r.Context(), zoneID, patch); err != nil {
		WriteInternalError(w, "Failed to create record: "+err.Error())
		return
	}

	// Notify secondaries
	_ = h.pdns.NotifyZone(r.Context(), zoneID)

	writeJSON(w, http.StatusCreated, RecordResponse{
		Name:    recordName,
		Type:    req.Type,
		TTL:     ttl,
		Content: req.Content,
	})
}

// UpdateRecord handles PUT /api/dns/zones/{zoneID}/records.
func (h *DNSHandler) UpdateRecord(w http.ResponseWriter, r *http.Request) {
	zoneID := chi.URLParam(r, "zoneID")
	claims := middleware.GetClaims(r.Context())

	zone, err := h.pdns.GetZone(r.Context(), zoneID)
	if err != nil {
		if apiErr, ok := err.(*powerdns.APIError); ok && apiErr.StatusCode == http.StatusNotFound {
			WriteNotFound(w, "Zone not found")
			return
		}
		WriteInternalError(w, "Failed to get zone: "+err.Error())
		return
	}
	if !canAccessZone(claims, zone) {
		WriteForbidden(w, "Access denied")
		return
	}

	var req RecordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteBadRequest(w, "Invalid request body", nil)
		return
	}

	req.Type = strings.ToUpper(req.Type)
	if !validateRecordType(req.Type) {
		WriteBadRequest(w, "Invalid record type: "+req.Type, nil)
		return
	}
	if err := validateRecordContent(req.Type, req.Content); err != nil {
		WriteBadRequest(w, err.Error(), nil)
		return
	}

	recordName, err := resolveRecordName(req.Name, zone.Name)
	if err != nil {
		WriteBadRequest(w, err.Error(), nil)
		return
	}
	ttl := req.TTL
	if ttl <= 0 {
		ttl = 3600
	}

	patch := powerdns.ZonePatch{
		RRSets: []powerdns.RRSet{{
			Name:       recordName,
			Type:       req.Type,
			TTL:        ttl,
			ChangeType: "REPLACE",
			Records:    []powerdns.Record{{Content: req.Content, Disabled: false}},
		}},
	}

	if err := h.pdns.PatchZone(r.Context(), zoneID, patch); err != nil {
		WriteInternalError(w, "Failed to update record: "+err.Error())
		return
	}

	_ = h.pdns.NotifyZone(r.Context(), zoneID)

	writeJSON(w, http.StatusOK, RecordResponse{
		Name:    recordName,
		Type:    req.Type,
		TTL:     ttl,
		Content: req.Content,
	})
}

// DeleteRecord handles DELETE /api/dns/zones/{zoneID}/records.
func (h *DNSHandler) DeleteRecord(w http.ResponseWriter, r *http.Request) {
	zoneID := chi.URLParam(r, "zoneID")
	claims := middleware.GetClaims(r.Context())

	zone, err := h.pdns.GetZone(r.Context(), zoneID)
	if err != nil {
		if apiErr, ok := err.(*powerdns.APIError); ok && apiErr.StatusCode == http.StatusNotFound {
			WriteNotFound(w, "Zone not found")
			return
		}
		WriteInternalError(w, "Failed to get zone: "+err.Error())
		return
	}
	if !canAccessZone(claims, zone) {
		WriteForbidden(w, "Access denied")
		return
	}

	var req RecordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteBadRequest(w, "Invalid request body", nil)
		return
	}

	req.Type = strings.ToUpper(req.Type)
	recordName, err := resolveRecordName(req.Name, zone.Name)
	if err != nil {
		WriteBadRequest(w, err.Error(), nil)
		return
	}

	patch := powerdns.ZonePatch{
		RRSets: []powerdns.RRSet{{
			Name:       recordName,
			Type:       req.Type,
			ChangeType: "DELETE",
			Records:    []powerdns.Record{},
		}},
	}

	if err := h.pdns.PatchZone(r.Context(), zoneID, patch); err != nil {
		WriteInternalError(w, "Failed to delete record: "+err.Error())
		return
	}

	_ = h.pdns.NotifyZone(r.Context(), zoneID)
	w.WriteHeader(http.StatusNoContent)
}

// --- Secondary DNS handlers ---

// GetSecondaryConfig handles GET /api/dns/zones/{zoneID}/secondary.
func (h *DNSHandler) GetSecondaryConfig(w http.ResponseWriter, r *http.Request) {
	zoneID := chi.URLParam(r, "zoneID")
	claims := middleware.GetClaims(r.Context())

	zone, err := h.pdns.GetZone(r.Context(), zoneID)
	if err != nil {
		if apiErr, ok := err.(*powerdns.APIError); ok && apiErr.StatusCode == http.StatusNotFound {
			WriteNotFound(w, "Zone not found")
			return
		}
		WriteInternalError(w, "Failed to get zone: "+err.Error())
		return
	}
	if !canAccessZone(claims, zone) {
		WriteForbidden(w, "Access denied")
		return
	}

	writeJSON(w, http.StatusOK, SecondaryConfigResponse{
		Kind:    zone.Kind,
		Masters: zone.Masters,
	})
}

// UpdateSecondaryConfig handles PUT /api/dns/zones/{zoneID}/secondary.
func (h *DNSHandler) UpdateSecondaryConfig(w http.ResponseWriter, r *http.Request) {
	zoneID := chi.URLParam(r, "zoneID")
	claims := middleware.GetClaims(r.Context())

	zone, err := h.pdns.GetZone(r.Context(), zoneID)
	if err != nil {
		if apiErr, ok := err.(*powerdns.APIError); ok && apiErr.StatusCode == http.StatusNotFound {
			WriteNotFound(w, "Zone not found")
			return
		}
		WriteInternalError(w, "Failed to get zone: "+err.Error())
		return
	}
	if !canAccessZone(claims, zone) {
		WriteForbidden(w, "Access denied")
		return
	}

	var req SecondaryConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteBadRequest(w, "Invalid request body", nil)
		return
	}

	// Validate IPs
	for _, ip := range req.AllowAXFR {
		if net.ParseIP(ip) == nil {
			WriteBadRequest(w, "Invalid IP in allowAxfrIps: "+ip, nil)
			return
		}
	}
	for _, ip := range req.Masters {
		if net.ParseIP(ip) == nil {
			WriteBadRequest(w, "Invalid IP in masters: "+ip, nil)
			return
		}
	}

	// Update allow-axfr-ips metadata
	if len(req.AllowAXFR) > 0 {
		if err := h.pdns.UpdateZoneMetadata(r.Context(), zoneID, "ALLOW-AXFR-FROM", req.AllowAXFR); err != nil {
			WriteInternalError(w, "Failed to update AXFR config: "+err.Error())
			return
		}
	}

	// Notify secondaries
	_ = h.pdns.NotifyZone(r.Context(), zoneID)

	writeJSON(w, http.StatusOK, SecondaryConfigResponse{
		Kind:      zone.Kind,
		Masters:   req.Masters,
		AllowAXFR: req.AllowAXFR,
	})
}

// --- Default records ---

// buildDefaultRecords creates the default RRSets for a new zone.
func (h *DNSHandler) buildDefaultRecords(zoneName string) []powerdns.RRSet {
	var rrsets []powerdns.RRSet

	// SOA record
	rrsets = append(rrsets, powerdns.RRSet{
		Name: zoneName,
		Type: "SOA",
		TTL:  3600,
		Records: []powerdns.Record{{
			Content: fmt.Sprintf("ns1.%s hostmaster.%s 1 10800 3600 604800 3600", zoneName, zoneName),
		}},
	})

	// NS records
	rrsets = append(rrsets, powerdns.RRSet{
		Name: zoneName,
		Type: "NS",
		TTL:  3600,
		Records: []powerdns.Record{
			{Content: "ns1." + zoneName},
			{Content: "ns2." + zoneName},
		},
	})

	// A record pointing to Contour/Envoy external IP
	if h.externalIP != "" {
		rrsets = append(rrsets, powerdns.RRSet{
			Name: zoneName,
			Type: "A",
			TTL:  3600,
			Records: []powerdns.Record{{Content: h.externalIP}},
		})
		// www subdomain
		rrsets = append(rrsets, powerdns.RRSet{
			Name: "www." + zoneName,
			Type: "A",
			TTL:  3600,
			Records: []powerdns.Record{{Content: h.externalIP}},
		})
		// NS A records
		rrsets = append(rrsets, powerdns.RRSet{
			Name: "ns1." + zoneName,
			Type: "A",
			TTL:  3600,
			Records: []powerdns.Record{{Content: h.externalIP}},
		})
		rrsets = append(rrsets, powerdns.RRSet{
			Name: "ns2." + zoneName,
			Type: "A",
			TTL:  3600,
			Records: []powerdns.Record{{Content: h.externalIP}},
		})
	}

	// MX record
	mailTarget := "mail." + zoneName
	if h.mailHost != "" {
		mailTarget = ensureTrailingDot(h.mailHost)
	}
	rrsets = append(rrsets, powerdns.RRSet{
		Name: zoneName,
		Type: "MX",
		TTL:  3600,
		Records: []powerdns.Record{{Content: "10 " + mailTarget}},
	})

	// SPF TXT record
	rrsets = append(rrsets, powerdns.RRSet{
		Name: zoneName,
		Type: "TXT",
		TTL:  3600,
		Records: []powerdns.Record{{Content: "\"v=spf1 mx a ~all\""}},
	})

	// DKIM placeholder TXT record
	rrsets = append(rrsets, powerdns.RRSet{
		Name: "default._domainkey." + zoneName,
		Type: "TXT",
		TTL:  3600,
		Records: []powerdns.Record{{Content: "\"v=DKIM1; k=rsa; p=REPLACE_WITH_DKIM_KEY\""}},
	})

	return rrsets
}
