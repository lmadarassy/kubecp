package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// setupDatabaseRouter creates a chi router with the database handler for testing.
func setupDatabaseRouter(handler *DatabaseHandler) *chi.Mux {
	r := chi.NewRouter()
	r.Route("/api/databases", func(r chi.Router) {
		handler.RegisterRoutes(r)
	})
	return r
}

// makeDatabaseObj creates an unstructured Database CRD object for testing.
func makeDatabaseObj(name, namespace string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": CRDGroup + "/" + CRDVersion,
			"kind":       "Database",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
				"labels": map[string]interface{}{
					"hosting.panel/user": "testuser",
				},
			},
			"spec": map[string]interface{}{
				"name":      name,
				"charset":   "utf8mb4",
				"collation": "utf8mb4_unicode_ci",
			},
		},
	}
}

// makeHostingPlanWithDBLimit creates a HostingPlan with a specific database limit.
func makeHostingPlanWithDBLimit(name string, dbLimit int64) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": CRDGroup + "/" + CRDVersion,
			"kind":       "HostingPlan",
			"metadata": map[string]interface{}{
				"name": name,
			},
			"spec": map[string]interface{}{
				"displayName": "Test Plan",
				"limits": map[string]interface{}{
					"websites":      int64(5),
					"databases":     dbLimit,
					"emailAccounts": int64(20),
				},
			},
		},
	}
}

func TestCreateDatabase_Success(t *testing.T) {
	dynClient := newFakeDynClient()
	handler := NewDatabaseHandler(dynClient)
	router := setupDatabaseRouter(handler)

	body := CreateDatabaseRequest{Name: "my_db"}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/databases", bytes.NewReader(bodyBytes))
	req = withClaims(req, userClaims("testuser"))
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusCreated, w.Body.String())
	}

	var resp DatabaseResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.Name != "my_db" {
		t.Errorf("name = %q, want %q", resp.Name, "my_db")
	}
	if resp.Namespace != "hosting-user-testuser" {
		t.Errorf("namespace = %q, want %q", resp.Namespace, "hosting-user-testuser")
	}
	if resp.Charset != "utf8mb4" {
		t.Errorf("charset = %q, want %q", resp.Charset, "utf8mb4")
	}
	if resp.Collation != "utf8mb4_unicode_ci" {
		t.Errorf("collation = %q, want %q", resp.Collation, "utf8mb4_unicode_ci")
	}

	// Verify the CRD was actually created in the fake client
	obj, err := dynClient.Resource(DatabaseGVR).Namespace("hosting-user-testuser").Get(context.Background(), "my_db", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("database not found in k8s: %v", err)
	}
	if obj.GetName() != "my_db" {
		t.Errorf("k8s object name = %q, want %q", obj.GetName(), "my_db")
	}
}

func TestCreateDatabase_EmptyName(t *testing.T) {
	dynClient := newFakeDynClient()
	handler := NewDatabaseHandler(dynClient)
	router := setupDatabaseRouter(handler)

	body := CreateDatabaseRequest{Name: ""}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/databases", bytes.NewReader(bodyBytes))
	req = withClaims(req, userClaims("testuser"))
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestCreateDatabase_InvalidName(t *testing.T) {
	dynClient := newFakeDynClient()
	handler := NewDatabaseHandler(dynClient)
	router := setupDatabaseRouter(handler)

	body := CreateDatabaseRequest{Name: "123invalid"}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/databases", bytes.NewReader(bodyBytes))
	req = withClaims(req, userClaims("testuser"))
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d, body: %s", w.Code, http.StatusBadRequest, w.Body.String())
	}
}

func TestCreateDatabase_QuotaExceeded(t *testing.T) {
	existingDB := makeDatabaseObj("existing_db", "hosting-user-testuser")
	plan := makeHostingPlanWithDBLimit("basic-plan", 1)

	dynClient := newFakeDynClient(existingDB, plan)
	handler := NewDatabaseHandler(dynClient)
	router := setupDatabaseRouter(handler)

	body := CreateDatabaseRequest{Name: "new_db"}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/databases", bytes.NewReader(bodyBytes))
	req = withClaims(req, userClaims("testuser"))
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusUnprocessableEntity, w.Body.String())
	}

	var resp APIError
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if resp.Error.Code != ErrCodeQuotaExceeded {
		t.Errorf("error code = %q, want %q", resp.Error.Code, ErrCodeQuotaExceeded)
	}
}

func TestListDatabases_UserSeesOwnOnly(t *testing.T) {
	db1 := makeDatabaseObj("db1", "hosting-user-testuser")
	db2 := makeDatabaseObj("db2", "hosting-user-otheruser")

	dynClient := newFakeDynClient(db1, db2)
	handler := NewDatabaseHandler(dynClient)
	router := setupDatabaseRouter(handler)

	req := httptest.NewRequest(http.MethodGet, "/api/databases", nil)
	req = withClaims(req, userClaims("testuser"))
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var databases []DatabaseResponse
	if err := json.NewDecoder(w.Body).Decode(&databases); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}

	if len(databases) != 1 {
		t.Fatalf("got %d databases, want 1", len(databases))
	}
	if databases[0].Name != "db1" {
		t.Errorf("database name = %q, want %q", databases[0].Name, "db1")
	}
}

func TestListDatabases_AdminSeesAll(t *testing.T) {
	db1 := makeDatabaseObj("db1", "hosting-user-testuser")
	db2 := makeDatabaseObj("db2", "hosting-user-otheruser")

	dynClient := newFakeDynClient(db1, db2)
	handler := NewDatabaseHandler(dynClient)
	router := setupDatabaseRouter(handler)

	req := httptest.NewRequest(http.MethodGet, "/api/databases", nil)
	req = withClaims(req, adminClaims())
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var databases []DatabaseResponse
	if err := json.NewDecoder(w.Body).Decode(&databases); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}

	if len(databases) != 2 {
		t.Errorf("got %d databases, want 2", len(databases))
	}
}

func TestGetDatabase_Success(t *testing.T) {
	db := makeDatabaseObj("my_db", "hosting-user-testuser")
	dynClient := newFakeDynClient(db)
	handler := NewDatabaseHandler(dynClient)
	router := setupDatabaseRouter(handler)

	req := httptest.NewRequest(http.MethodGet, "/api/databases/my_db", nil)
	req = withClaims(req, userClaims("testuser"))
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var resp DatabaseResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if resp.Name != "my_db" {
		t.Errorf("name = %q, want %q", resp.Name, "my_db")
	}
	if resp.Charset != "utf8mb4" {
		t.Errorf("charset = %q, want %q", resp.Charset, "utf8mb4")
	}
}

func TestGetDatabase_NotFound(t *testing.T) {
	dynClient := newFakeDynClient()
	handler := NewDatabaseHandler(dynClient)
	router := setupDatabaseRouter(handler)

	req := httptest.NewRequest(http.MethodGet, "/api/databases/nonexistent", nil)
	req = withClaims(req, userClaims("testuser"))
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestDeleteDatabase_Success(t *testing.T) {
	db := makeDatabaseObj("my_db", "hosting-user-testuser")
	dynClient := newFakeDynClient(db)
	handler := NewDatabaseHandler(dynClient)
	router := setupDatabaseRouter(handler)

	req := httptest.NewRequest(http.MethodDelete, "/api/databases/my_db", nil)
	req = withClaims(req, userClaims("testuser"))
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusNoContent, w.Body.String())
	}

	// Verify it's gone
	_, err := dynClient.Resource(DatabaseGVR).Namespace("hosting-user-testuser").Get(context.Background(), "my_db", metav1.GetOptions{})
	if err == nil {
		t.Error("database should have been deleted")
	}
}
