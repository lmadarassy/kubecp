package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"k8s.io/client-go/kubernetes/fake"
)

func TestHealthHandler_WithFakeClient(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	handler := HealthHandler(clientset)

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	w := httptest.NewRecorder()

	handler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp HealthResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}

	if resp.Status != "ok" {
		t.Errorf("status = %q, want %q", resp.Status, "ok")
	}
}

func TestHealthHandler_NilClient(t *testing.T) {
	handler := HealthHandler(nil)

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	w := httptest.NewRecorder()

	handler(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}

	var resp HealthResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}

	if resp.Status != "degraded" {
		t.Errorf("status = %q, want %q", resp.Status, "degraded")
	}

	if resp.Components["kubernetes"] != "unavailable: client not initialized" {
		t.Errorf("kubernetes component = %q, want unavailable message", resp.Components["kubernetes"])
	}
}
