package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes/fake"
)

// setupHostingPlanRouter creates a chi router with the hosting plan handler for testing.
func setupHostingPlanRouter(handler *HostingPlanHandler) *chi.Mux {
	r := chi.NewRouter()
	r.Route("/api/hosting-plans", func(r chi.Router) {
		handler.RegisterRoutes(r)
	})
	return r
}

// makeHostingPlanObj creates an unstructured HostingPlan CRD object for testing.
func makeHostingPlanObj(name, displayName string, websites, databases, emailAccounts int64) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": CRDGroup + "/" + CRDVersion,
			"kind":       "HostingPlan",
			"metadata": map[string]interface{}{
				"name": name,
			},
			"spec": map[string]interface{}{
				"displayName": displayName,
				"limits": map[string]interface{}{
					"websites":      websites,
					"databases":     databases,
					"emailAccounts": emailAccounts,
					"storageGB":     int64(50),
					"cpuMillicores": int64(2000),
					"memoryMB":      int64(4096),
				},
			},
		},
	}
}

func TestCreateHostingPlan_Success(t *testing.T) {
	dynClient := newFakeDynClient()
	fakeClientset := fake.NewSimpleClientset()
	handler := NewHostingPlanHandler(dynClient, fakeClientset)
	router := setupHostingPlanRouter(handler)

	body := CreateHostingPlanRequest{
		Name:        "basic",
		DisplayName: "Basic Plan",
		Limits: HostingPlanLimitsRequest{
			Websites:      5,
			Databases:     10,
			EmailAccounts: 20,
			StorageGB:     50,
			CPUMillicores: 2000,
			MemoryMB:      4096,
		},
	}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/hosting-plans", bytes.NewReader(bodyBytes))
	req = withClaims(req, adminClaims())
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusCreated, w.Body.String())
	}

	var resp HostingPlanResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if resp.Name != "basic" {
		t.Errorf("name = %q, want %q", resp.Name, "basic")
	}
	if resp.DisplayName != "Basic Plan" {
		t.Errorf("displayName = %q, want %q", resp.DisplayName, "Basic Plan")
	}
	if resp.Limits.Websites != 5 {
		t.Errorf("websites limit = %d, want %d", resp.Limits.Websites, 5)
	}
}

func TestCreateHostingPlan_EmptyName(t *testing.T) {
	dynClient := newFakeDynClient()
	fakeClientset := fake.NewSimpleClientset()
	handler := NewHostingPlanHandler(dynClient, fakeClientset)
	router := setupHostingPlanRouter(handler)

	body := CreateHostingPlanRequest{
		Name:        "",
		DisplayName: "No Name Plan",
		Limits:      HostingPlanLimitsRequest{Websites: 5},
	}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/hosting-plans", bytes.NewReader(bodyBytes))
	req = withClaims(req, adminClaims())
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestCreateHostingPlan_Duplicate(t *testing.T) {
	existing := makeHostingPlanObj("basic", "Basic Plan", 5, 10, 20)
	dynClient := newFakeDynClient(existing)
	fakeClientset := fake.NewSimpleClientset()
	handler := NewHostingPlanHandler(dynClient, fakeClientset)
	router := setupHostingPlanRouter(handler)

	body := CreateHostingPlanRequest{
		Name:        "basic",
		DisplayName: "Another Basic",
		Limits:      HostingPlanLimitsRequest{Websites: 3},
	}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/hosting-plans", bytes.NewReader(bodyBytes))
	req = withClaims(req, adminClaims())
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want %d, body: %s", w.Code, http.StatusConflict, w.Body.String())
	}
}

func TestListHostingPlans_Success(t *testing.T) {
	plan1 := makeHostingPlanObj("basic", "Basic Plan", 5, 10, 20)
	plan2 := makeHostingPlanObj("pro", "Pro Plan", 20, 50, 100)
	dynClient := newFakeDynClient(plan1, plan2)
	fakeClientset := fake.NewSimpleClientset()
	handler := NewHostingPlanHandler(dynClient, fakeClientset)
	router := setupHostingPlanRouter(handler)

	req := httptest.NewRequest(http.MethodGet, "/api/hosting-plans", nil)
	req = withClaims(req, adminClaims())
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var plans []HostingPlanResponse
	if err := json.NewDecoder(w.Body).Decode(&plans); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if len(plans) != 2 {
		t.Errorf("got %d plans, want 2", len(plans))
	}
}

func TestGetHostingPlan_Success(t *testing.T) {
	plan := makeHostingPlanObj("basic", "Basic Plan", 5, 10, 20)
	dynClient := newFakeDynClient(plan)
	fakeClientset := fake.NewSimpleClientset()
	handler := NewHostingPlanHandler(dynClient, fakeClientset)
	router := setupHostingPlanRouter(handler)

	req := httptest.NewRequest(http.MethodGet, "/api/hosting-plans/basic", nil)
	req = withClaims(req, adminClaims())
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var resp HostingPlanResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if resp.Name != "basic" {
		t.Errorf("name = %q, want %q", resp.Name, "basic")
	}
	if resp.Limits.Databases != 10 {
		t.Errorf("databases = %d, want %d", resp.Limits.Databases, 10)
	}
}

func TestGetHostingPlan_NotFound(t *testing.T) {
	dynClient := newFakeDynClient()
	fakeClientset := fake.NewSimpleClientset()
	handler := NewHostingPlanHandler(dynClient, fakeClientset)
	router := setupHostingPlanRouter(handler)

	req := httptest.NewRequest(http.MethodGet, "/api/hosting-plans/nonexistent", nil)
	req = withClaims(req, adminClaims())
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestUpdateHostingPlan_Success(t *testing.T) {
	plan := makeHostingPlanObj("basic", "Basic Plan", 5, 10, 20)
	dynClient := newFakeDynClient(plan)
	fakeClientset := fake.NewSimpleClientset()
	handler := NewHostingPlanHandler(dynClient, fakeClientset)
	router := setupHostingPlanRouter(handler)

	body := UpdateHostingPlanRequest{
		DisplayName: "Updated Basic Plan",
		Limits: &HostingPlanLimitsRequest{
			Websites:      10,
			Databases:     20,
			EmailAccounts: 50,
			StorageGB:     100,
			CPUMillicores: 4000,
			MemoryMB:      8192,
		},
	}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPut, "/api/hosting-plans/basic", bytes.NewReader(bodyBytes))
	req = withClaims(req, adminClaims())
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var resp HostingPlanResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if resp.DisplayName != "Updated Basic Plan" {
		t.Errorf("displayName = %q, want %q", resp.DisplayName, "Updated Basic Plan")
	}
	if resp.Limits.Websites != 10 {
		t.Errorf("websites = %d, want %d", resp.Limits.Websites, 10)
	}
}

func TestDeleteHostingPlan_Success(t *testing.T) {
	plan := makeHostingPlanObj("basic", "Basic Plan", 5, 10, 20)
	dynClient := newFakeDynClient(plan)
	fakeClientset := fake.NewSimpleClientset()
	handler := NewHostingPlanHandler(dynClient, fakeClientset)
	router := setupHostingPlanRouter(handler)

	req := httptest.NewRequest(http.MethodDelete, "/api/hosting-plans/basic", nil)
	req = withClaims(req, adminClaims())
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusNoContent, w.Body.String())
	}

	// Verify it's gone
	_, err := dynClient.Resource(HostingPlanGVR).Get(context.Background(), "basic", metav1.GetOptions{})
	if err == nil {
		t.Error("hosting plan should have been deleted")
	}
}

func TestDeleteHostingPlan_NotFound(t *testing.T) {
	dynClient := newFakeDynClient()
	fakeClientset := fake.NewSimpleClientset()
	handler := NewHostingPlanHandler(dynClient, fakeClientset)
	router := setupHostingPlanRouter(handler)

	req := httptest.NewRequest(http.MethodDelete, "/api/hosting-plans/nonexistent", nil)
	req = withClaims(req, adminClaims())
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestAssignPlan_Success(t *testing.T) {
	plan := makeHostingPlanObj("basic", "Basic Plan", 5, 10, 20)
	dynClient := newFakeDynClient(plan)

	// Create a fake namespace for the user
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "hosting-user-testuser",
		},
	}
	fakeClientset := fake.NewSimpleClientset(ns)
	handler := NewHostingPlanHandler(dynClient, fakeClientset)
	router := setupHostingPlanRouter(handler)

	body := AssignPlanRequest{Username: "testuser"}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/hosting-plans/basic/assign", bytes.NewReader(bodyBytes))
	req = withClaims(req, adminClaims())
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	// Verify namespace was labeled
	updatedNS, err := fakeClientset.CoreV1().Namespaces().Get(context.Background(), "hosting-user-testuser", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("failed to get namespace: %v", err)
	}
	if updatedNS.Labels["hosting.panel/plan"] != "basic" {
		t.Errorf("namespace plan label = %q, want %q", updatedNS.Labels["hosting.panel/plan"], "basic")
	}
}

func TestAssignPlan_PlanNotFound(t *testing.T) {
	dynClient := newFakeDynClient()
	fakeClientset := fake.NewSimpleClientset()
	handler := NewHostingPlanHandler(dynClient, fakeClientset)
	router := setupHostingPlanRouter(handler)

	body := AssignPlanRequest{Username: "testuser"}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/hosting-plans/nonexistent/assign", bytes.NewReader(bodyBytes))
	req = withClaims(req, adminClaims())
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestAssignPlan_EmptyUsername(t *testing.T) {
	plan := makeHostingPlanObj("basic", "Basic Plan", 5, 10, 20)
	dynClient := newFakeDynClient(plan)
	fakeClientset := fake.NewSimpleClientset()
	handler := NewHostingPlanHandler(dynClient, fakeClientset)
	router := setupHostingPlanRouter(handler)

	body := AssignPlanRequest{Username: ""}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/hosting-plans/basic/assign", bytes.NewReader(bodyBytes))
	req = withClaims(req, adminClaims())
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}
