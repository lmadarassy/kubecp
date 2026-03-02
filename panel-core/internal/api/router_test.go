package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/hosting-panel/panel-core/internal/keycloak"
)

func TestRouter_HealthEndpoint(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	kcAdmin := keycloak.NewAdminClientWithConfig("http://localhost/admin", "http://localhost/realms/hosting", "admin", "pass")
	router := NewRouter(clientset, kcAdmin)

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp HealthResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if resp.Status != "ok" {
		t.Errorf("health status = %q, want %q", resp.Status, "ok")
	}
}

func TestRouter_MetricsEndpoint(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	kcAdmin := keycloak.NewAdminClientWithConfig("http://localhost/admin", "http://localhost/realms/hosting", "admin", "pass")
	router := NewRouter(clientset, kcAdmin)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	// Prometheus metrics should contain at least the go_ metrics
	body := w.Body.String()
	if len(body) == 0 {
		t.Error("metrics response body is empty")
	}
}

func TestRouter_HealthEndpoint_NilClient(t *testing.T) {
	kcAdmin := keycloak.NewAdminClientWithConfig("http://localhost/admin", "http://localhost/realms/hosting", "admin", "pass")
	router := NewRouter(nil, kcAdmin)

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}

	var resp HealthResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if resp.Status != "degraded" {
		t.Errorf("health status = %q, want %q", resp.Status, "degraded")
	}
}
