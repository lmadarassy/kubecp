package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/hosting-panel/panel-core/internal/middleware"
	"github.com/hosting-panel/panel-core/internal/powerdns"
)

// mockPowerDNS creates a mock PowerDNS API server and returns the server + client.
func mockPowerDNS(t *testing.T) (*httptest.Server, *powerdns.Client) {
	t.Helper()
	var mu sync.Mutex
	zones := make(map[string]powerdns.Zone)

	mux := http.NewServeMux()

	// List zones
	mux.HandleFunc("/api/v1/servers/localhost/zones", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		switch r.Method {
		case http.MethodGet:
			var list []powerdns.Zone
			for _, z := range zones {
				list = append(list, powerdns.Zone{
					ID: z.ID, Name: z.Name, Kind: z.Kind,
					Serial: z.Serial, Account: z.Account,
				})
			}
			if list == nil {
				list = []powerdns.Zone{}
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(list)

		case http.MethodPost:
			var z powerdns.Zone
			if err := json.NewDecoder(r.Body).Decode(&z); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if _, exists := zones[z.Name]; exists {
				w.WriteHeader(http.StatusConflict)
				json.NewEncoder(w).Encode(map[string]string{"error": "Conflict"})
				return
			}
			z.ID = z.Name
			z.Serial = 1
			zones[z.Name] = z
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(z)
		}
	})

	// Single zone operations
	mux.HandleFunc("/api/v1/servers/localhost/zones/", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		path := strings.TrimPrefix(r.URL.Path, "/api/v1/servers/localhost/zones/")

		// Handle notify
		if strings.HasSuffix(path, "/notify") {
			w.WriteHeader(http.StatusOK)
			return
		}
		// Handle metadata
		if strings.Contains(path, "/metadata/") {
			w.WriteHeader(http.StatusOK)
			return
		}

		zoneID := strings.Split(path, "/")[0]

		switch r.Method {
		case http.MethodGet:
			z, ok := zones[zoneID]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				json.NewEncoder(w).Encode(map[string]string{"error": "Not Found"})
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(z)

		case http.MethodDelete:
			if _, ok := zones[zoneID]; !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			delete(zones, zoneID)
			w.WriteHeader(http.StatusNoContent)

		case http.MethodPatch:
			z, ok := zones[zoneID]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			var patch powerdns.ZonePatch
			if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			for _, rrset := range patch.RRSets {
				if rrset.ChangeType == "DELETE" {
					var kept []powerdns.RRSet
					for _, existing := range z.RRSets {
						if !(existing.Name == rrset.Name && existing.Type == rrset.Type) {
							kept = append(kept, existing)
						}
					}
					z.RRSets = kept
				} else {
					found := false
					for i, existing := range z.RRSets {
						if existing.Name == rrset.Name && existing.Type == rrset.Type {
							z.RRSets[i] = rrset
							found = true
							break
						}
					}
					if !found {
						z.RRSets = append(z.RRSets, rrset)
					}
				}
			}
			z.Serial++
			zones[zoneID] = z
			w.WriteHeader(http.StatusNoContent)

		case http.MethodPut:
			// notify endpoint handled above
			w.WriteHeader(http.StatusOK)
		}
	})

	server := httptest.NewServer(mux)
	client := powerdns.NewClient(server.URL, "test-api-key")
	return server, client
}

func setupDNSRouter(pdnsClient *powerdns.Client) *chi.Mux {
	handler := NewDNSHandler(pdnsClient, "192.168.1.100", "mail.example.com")
	r := chi.NewRouter()
	r.Route("/api/dns", func(r chi.Router) { handler.RegisterRoutes(r) })
	return r
}

func TestCreateZone_Success(t *testing.T) {
	server, pdns := mockPowerDNS(t)
	defer server.Close()
	router := setupDNSRouter(pdns)

	body := CreateZoneRequest{Name: "example.com"}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/dns/zones", bytes.NewReader(bodyBytes))
	req = withClaims(req, adminClaims())
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusCreated, w.Body.String())
	}
	var resp ZoneResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Name != "example.com." {
		t.Errorf("name = %q, want %q", resp.Name, "example.com.")
	}
	if resp.Kind != "Native" {
		t.Errorf("kind = %q, want %q", resp.Kind, "Native")
	}
}

func TestCreateZone_EmptyName(t *testing.T) {
	server, pdns := mockPowerDNS(t)
	defer server.Close()
	router := setupDNSRouter(pdns)

	body := CreateZoneRequest{Name: ""}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/dns/zones", bytes.NewReader(bodyBytes))
	req = withClaims(req, adminClaims())
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestCreateZone_Duplicate(t *testing.T) {
	server, pdns := mockPowerDNS(t)
	defer server.Close()
	router := setupDNSRouter(pdns)

	body := CreateZoneRequest{Name: "dup.com"}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/dns/zones", bytes.NewReader(bodyBytes))
	req = withClaims(req, adminClaims())
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("first create: status = %d", w.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/dns/zones", bytes.NewReader(bodyBytes))
	req = withClaims(req, adminClaims())
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want %d", w.Code, http.StatusConflict)
	}
}

func TestCreateZone_DefaultRecords(t *testing.T) {
	server, pdns := mockPowerDNS(t)
	defer server.Close()
	router := setupDNSRouter(pdns)

	body := CreateZoneRequest{Name: "test.com"}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/dns/zones", bytes.NewReader(bodyBytes))
	req = withClaims(req, adminClaims())
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: status = %d, body: %s", w.Code, w.Body.String())
	}

	// List records and verify defaults
	var resp ZoneResponse
	json.NewDecoder(w.Body).Decode(&resp)

	req = httptest.NewRequest(http.MethodGet, "/api/dns/zones/"+resp.ID+"/records", nil)
	req = withClaims(req, adminClaims())
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list records: status = %d", w.Code)
	}

	var records []RecordResponse
	json.NewDecoder(w.Body).Decode(&records)

	// Check for expected default record types
	typeMap := make(map[string]bool)
	for _, r := range records {
		typeMap[r.Type] = true
	}
	for _, expected := range []string{"SOA", "NS", "A", "MX", "TXT"} {
		if !typeMap[expected] {
			t.Errorf("missing default record type: %s", expected)
		}
	}

	// Check A record points to external IP
	for _, r := range records {
		if r.Type == "A" && r.Name == "test.com." {
			if r.Content != "192.168.1.100" {
				t.Errorf("A record content = %q, want %q", r.Content, "192.168.1.100")
			}
		}
	}
}

func TestListZones_AdminSeesAll(t *testing.T) {
	server, pdns := mockPowerDNS(t)
	defer server.Close()
	router := setupDNSRouter(pdns)

	// Create zones with different owners
	for _, name := range []string{"alice.com", "bob.com"} {
		body := CreateZoneRequest{Name: name}
		bodyBytes, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/api/dns/zones", bytes.NewReader(bodyBytes))
		claims := &middleware.TokenClaims{Subject: name, Username: strings.TrimSuffix(name, ".com"), Roles: []string{"admin"}}
		req = withClaims(req, claims)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/dns/zones", nil)
	req = withClaims(req, adminClaims())
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	var zones []ZoneResponse
	json.NewDecoder(w.Body).Decode(&zones)
	if len(zones) != 2 {
		t.Errorf("got %d zones, want 2", len(zones))
	}
}

func TestListZones_UserSeesOwn(t *testing.T) {
	server, pdns := mockPowerDNS(t)
	defer server.Close()
	router := setupDNSRouter(pdns)

	// Create zone as alice
	body := CreateZoneRequest{Name: "alice-zone.com"}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/dns/zones", bytes.NewReader(bodyBytes))
	req = withClaims(req, &middleware.TokenClaims{Subject: "alice-id", Username: "alice", Roles: []string{"user"}})
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// Create zone as bob
	body = CreateZoneRequest{Name: "bob-zone.com"}
	bodyBytes, _ = json.Marshal(body)
	req = httptest.NewRequest(http.MethodPost, "/api/dns/zones", bytes.NewReader(bodyBytes))
	req = withClaims(req, &middleware.TokenClaims{Subject: "bob-id", Username: "bob", Roles: []string{"user"}})
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// Alice should only see her zone
	req = httptest.NewRequest(http.MethodGet, "/api/dns/zones", nil)
	req = withClaims(req, &middleware.TokenClaims{Subject: "alice-id", Username: "alice", Roles: []string{"user"}})
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var zones []ZoneResponse
	json.NewDecoder(w.Body).Decode(&zones)
	if len(zones) != 1 {
		t.Errorf("got %d zones, want 1", len(zones))
	}
	if len(zones) > 0 && zones[0].Name != "alice-zone.com." {
		t.Errorf("zone name = %q, want %q", zones[0].Name, "alice-zone.com.")
	}
}

func TestDeleteZone_Success(t *testing.T) {
	server, pdns := mockPowerDNS(t)
	defer server.Close()
	router := setupDNSRouter(pdns)

	body := CreateZoneRequest{Name: "todelete.com"}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/dns/zones", bytes.NewReader(bodyBytes))
	req = withClaims(req, adminClaims())
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	var created ZoneResponse
	json.NewDecoder(w.Body).Decode(&created)

	req = httptest.NewRequest(http.MethodDelete, "/api/dns/zones/"+created.ID, nil)
	req = withClaims(req, adminClaims())
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNoContent)
	}

	// Verify it's gone
	req = httptest.NewRequest(http.MethodGet, "/api/dns/zones/"+created.ID, nil)
	req = withClaims(req, adminClaims())
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("after delete: status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestDeleteZone_NotFound(t *testing.T) {
	server, pdns := mockPowerDNS(t)
	defer server.Close()
	router := setupDNSRouter(pdns)

	req := httptest.NewRequest(http.MethodDelete, "/api/dns/zones/nonexistent.com.", nil)
	req = withClaims(req, adminClaims())
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestCreateRecord_Success(t *testing.T) {
	server, pdns := mockPowerDNS(t)
	defer server.Close()
	router := setupDNSRouter(pdns)

	// Create zone first
	zBody := CreateZoneRequest{Name: "rec.com"}
	zBytes, _ := json.Marshal(zBody)
	req := httptest.NewRequest(http.MethodPost, "/api/dns/zones", bytes.NewReader(zBytes))
	req = withClaims(req, adminClaims())
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	var zone ZoneResponse
	json.NewDecoder(w.Body).Decode(&zone)

	// Create A record
	recBody := RecordRequest{Name: "sub.rec.com", Type: "A", TTL: 300, Content: "10.0.0.1"}
	recBytes, _ := json.Marshal(recBody)
	req = httptest.NewRequest(http.MethodPost, "/api/dns/zones/"+zone.ID+"/records", bytes.NewReader(recBytes))
	req = withClaims(req, adminClaims())
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusCreated, w.Body.String())
	}
	var rec RecordResponse
	json.NewDecoder(w.Body).Decode(&rec)
	if rec.Content != "10.0.0.1" {
		t.Errorf("content = %q, want %q", rec.Content, "10.0.0.1")
	}
	if rec.Type != "A" {
		t.Errorf("type = %q, want %q", rec.Type, "A")
	}
}

func TestCreateRecord_InvalidType(t *testing.T) {
	server, pdns := mockPowerDNS(t)
	defer server.Close()
	router := setupDNSRouter(pdns)

	zBody := CreateZoneRequest{Name: "inv.com"}
	zBytes, _ := json.Marshal(zBody)
	req := httptest.NewRequest(http.MethodPost, "/api/dns/zones", bytes.NewReader(zBytes))
	req = withClaims(req, adminClaims())
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	var zone ZoneResponse
	json.NewDecoder(w.Body).Decode(&zone)

	recBody := RecordRequest{Name: "sub.inv.com", Type: "INVALID", Content: "1.2.3.4"}
	recBytes, _ := json.Marshal(recBody)
	req = httptest.NewRequest(http.MethodPost, "/api/dns/zones/"+zone.ID+"/records", bytes.NewReader(recBytes))
	req = withClaims(req, adminClaims())
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestCreateRecord_InvalidIPv4(t *testing.T) {
	server, pdns := mockPowerDNS(t)
	defer server.Close()
	router := setupDNSRouter(pdns)

	zBody := CreateZoneRequest{Name: "badip.com"}
	zBytes, _ := json.Marshal(zBody)
	req := httptest.NewRequest(http.MethodPost, "/api/dns/zones", bytes.NewReader(zBytes))
	req = withClaims(req, adminClaims())
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	var zone ZoneResponse
	json.NewDecoder(w.Body).Decode(&zone)

	recBody := RecordRequest{Name: "sub.badip.com", Type: "A", Content: "not-an-ip"}
	recBytes, _ := json.Marshal(recBody)
	req = httptest.NewRequest(http.MethodPost, "/api/dns/zones/"+zone.ID+"/records", bytes.NewReader(recBytes))
	req = withClaims(req, adminClaims())
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestCreateRecord_InvalidIPv6(t *testing.T) {
	server, pdns := mockPowerDNS(t)
	defer server.Close()
	router := setupDNSRouter(pdns)

	zBody := CreateZoneRequest{Name: "badipv6.com"}
	zBytes, _ := json.Marshal(zBody)
	req := httptest.NewRequest(http.MethodPost, "/api/dns/zones", bytes.NewReader(zBytes))
	req = withClaims(req, adminClaims())
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	var zone ZoneResponse
	json.NewDecoder(w.Body).Decode(&zone)

	recBody := RecordRequest{Name: "sub.badipv6.com", Type: "AAAA", Content: "not-ipv6"}
	recBytes, _ := json.Marshal(recBody)
	req = httptest.NewRequest(http.MethodPost, "/api/dns/zones/"+zone.ID+"/records", bytes.NewReader(recBytes))
	req = withClaims(req, adminClaims())
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestUpdateRecord_Success(t *testing.T) {
	server, pdns := mockPowerDNS(t)
	defer server.Close()
	router := setupDNSRouter(pdns)

	zBody := CreateZoneRequest{Name: "upd.com"}
	zBytes, _ := json.Marshal(zBody)
	req := httptest.NewRequest(http.MethodPost, "/api/dns/zones", bytes.NewReader(zBytes))
	req = withClaims(req, adminClaims())
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	var zone ZoneResponse
	json.NewDecoder(w.Body).Decode(&zone)

	// Update A record
	recBody := RecordRequest{Name: "upd.com", Type: "A", TTL: 600, Content: "10.0.0.2"}
	recBytes, _ := json.Marshal(recBody)
	req = httptest.NewRequest(http.MethodPut, "/api/dns/zones/"+zone.ID+"/records", bytes.NewReader(recBytes))
	req = withClaims(req, adminClaims())
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	var rec RecordResponse
	json.NewDecoder(w.Body).Decode(&rec)
	if rec.Content != "10.0.0.2" {
		t.Errorf("content = %q, want %q", rec.Content, "10.0.0.2")
	}
}

func TestDeleteRecord_Success(t *testing.T) {
	server, pdns := mockPowerDNS(t)
	defer server.Close()
	router := setupDNSRouter(pdns)

	zBody := CreateZoneRequest{Name: "delrec.com"}
	zBytes, _ := json.Marshal(zBody)
	req := httptest.NewRequest(http.MethodPost, "/api/dns/zones", bytes.NewReader(zBytes))
	req = withClaims(req, adminClaims())
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	var zone ZoneResponse
	json.NewDecoder(w.Body).Decode(&zone)

	// Delete the A record
	recBody := RecordRequest{Name: "delrec.com", Type: "A"}
	recBytes, _ := json.Marshal(recBody)
	req = httptest.NewRequest(http.MethodDelete, "/api/dns/zones/"+zone.ID+"/records", bytes.NewReader(recBytes))
	req = withClaims(req, adminClaims())
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNoContent)
	}
}

func TestGetSecondaryConfig_Success(t *testing.T) {
	server, pdns := mockPowerDNS(t)
	defer server.Close()
	router := setupDNSRouter(pdns)

	zBody := CreateZoneRequest{Name: "sec.com"}
	zBytes, _ := json.Marshal(zBody)
	req := httptest.NewRequest(http.MethodPost, "/api/dns/zones", bytes.NewReader(zBytes))
	req = withClaims(req, adminClaims())
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	var zone ZoneResponse
	json.NewDecoder(w.Body).Decode(&zone)

	req = httptest.NewRequest(http.MethodGet, "/api/dns/zones/"+zone.ID+"/secondary", nil)
	req = withClaims(req, adminClaims())
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	var cfg SecondaryConfigResponse
	json.NewDecoder(w.Body).Decode(&cfg)
	if cfg.Kind != "Native" {
		t.Errorf("kind = %q, want %q", cfg.Kind, "Native")
	}
}

func TestUpdateSecondaryConfig_Success(t *testing.T) {
	server, pdns := mockPowerDNS(t)
	defer server.Close()
	router := setupDNSRouter(pdns)

	zBody := CreateZoneRequest{Name: "secupd.com"}
	zBytes, _ := json.Marshal(zBody)
	req := httptest.NewRequest(http.MethodPost, "/api/dns/zones", bytes.NewReader(zBytes))
	req = withClaims(req, adminClaims())
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	var zone ZoneResponse
	json.NewDecoder(w.Body).Decode(&zone)

	cfgBody := SecondaryConfigRequest{AllowAXFR: []string{"10.0.0.5", "10.0.0.6"}}
	cfgBytes, _ := json.Marshal(cfgBody)
	req = httptest.NewRequest(http.MethodPut, "/api/dns/zones/"+zone.ID+"/secondary", bytes.NewReader(cfgBytes))
	req = withClaims(req, adminClaims())
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}
}

func TestUpdateSecondaryConfig_InvalidIP(t *testing.T) {
	server, pdns := mockPowerDNS(t)
	defer server.Close()
	router := setupDNSRouter(pdns)

	zBody := CreateZoneRequest{Name: "badsec.com"}
	zBytes, _ := json.Marshal(zBody)
	req := httptest.NewRequest(http.MethodPost, "/api/dns/zones", bytes.NewReader(zBytes))
	req = withClaims(req, adminClaims())
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	var zone ZoneResponse
	json.NewDecoder(w.Body).Decode(&zone)

	cfgBody := SecondaryConfigRequest{AllowAXFR: []string{"not-an-ip"}}
	cfgBytes, _ := json.Marshal(cfgBody)
	req = httptest.NewRequest(http.MethodPut, "/api/dns/zones/"+zone.ID+"/secondary", bytes.NewReader(cfgBytes))
	req = withClaims(req, adminClaims())
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestZone_ForbiddenForOtherUser(t *testing.T) {
	server, pdns := mockPowerDNS(t)
	defer server.Close()
	router := setupDNSRouter(pdns)

	// Create zone as alice
	body := CreateZoneRequest{Name: "private.com"}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/dns/zones", bytes.NewReader(bodyBytes))
	req = withClaims(req, &middleware.TokenClaims{Subject: "alice-id", Username: "alice", Roles: []string{"user"}})
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	var zone ZoneResponse
	json.NewDecoder(w.Body).Decode(&zone)

	// Bob tries to access alice's zone
	req = httptest.NewRequest(http.MethodGet, "/api/dns/zones/"+zone.ID, nil)
	req = withClaims(req, &middleware.TokenClaims{Subject: "bob-id", Username: "bob", Roles: []string{"user"}})
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
}
