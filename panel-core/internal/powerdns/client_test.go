package powerdns

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewClient(t *testing.T) {
	c := NewClient("http://localhost:8081", "secret")
	if !c.Configured() {
		t.Error("expected Configured() = true")
	}
}

func TestNewClient_NotConfigured(t *testing.T) {
	c := NewClient("", "")
	if c.Configured() {
		t.Error("expected Configured() = false")
	}
}

func TestListZones(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != "test-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]Zone{
			{ID: "example.com.", Name: "example.com.", Kind: "Native"},
		})
	}))
	defer server.Close()

	c := NewClient(server.URL, "test-key")
	zones, err := c.ListZones(context.Background())
	if err != nil {
		t.Fatalf("ListZones: %v", err)
	}
	if len(zones) != 1 {
		t.Fatalf("got %d zones, want 1", len(zones))
	}
	if zones[0].Name != "example.com." {
		t.Errorf("name = %q, want %q", zones[0].Name, "example.com.")
	}
}

func TestCreateZone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var z Zone
		json.NewDecoder(r.Body).Decode(&z)
		z.ID = z.Name
		z.Serial = 1
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(z)
	}))
	defer server.Close()

	c := NewClient(server.URL, "test-key")
	created, err := c.CreateZone(context.Background(), Zone{Name: "new.com.", Kind: "Native"})
	if err != nil {
		t.Fatalf("CreateZone: %v", err)
	}
	if created.Name != "new.com." {
		t.Errorf("name = %q, want %q", created.Name, "new.com.")
	}
}

func TestDeleteZone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	c := NewClient(server.URL, "test-key")
	err := c.DeleteZone(context.Background(), "old.com.")
	if err != nil {
		t.Fatalf("DeleteZone: %v", err)
	}
}

func TestAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("zone not found"))
	}))
	defer server.Close()

	c := NewClient(server.URL, "test-key")
	_, err := c.ListZones(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("expected *APIError, got %T", err)
	}
	if apiErr.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want %d", apiErr.StatusCode, http.StatusNotFound)
	}
}
