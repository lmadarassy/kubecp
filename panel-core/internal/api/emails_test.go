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

// setupEmailRouter creates a chi router with the email handler for testing.
func setupEmailRouter(handler *EmailHandler) *chi.Mux {
	r := chi.NewRouter()
	r.Route("/api/email-accounts", func(r chi.Router) {
		handler.RegisterRoutes(r)
	})
	return r
}

// makeEmailAccountObj creates an unstructured EmailAccount CRD object for testing.
func makeEmailAccountObj(name, namespace, address, domain string, quotaMB int64) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": CRDGroup + "/" + CRDVersion,
			"kind":       "EmailAccount",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
				"labels": map[string]interface{}{
					"hosting.panel/user":   "testuser",
					"hosting.panel/domain": domain,
				},
			},
			"spec": map[string]interface{}{
				"address": address,
				"domain":  domain,
				"quotaMB": quotaMB,
			},
		},
	}
}

// makeHostingPlanWithEmailLimit creates a HostingPlan with a specific email account limit.
func makeHostingPlanWithEmailLimit(name string, emailLimit int64) *unstructured.Unstructured {
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
					"databases":     int64(10),
					"emailAccounts": emailLimit,
				},
			},
		},
	}
}

func TestCreateEmail_Success(t *testing.T) {
	dynClient := newFakeDynClient()
	handler := NewEmailHandler(dynClient)
	router := setupEmailRouter(handler)

	body := CreateEmailRequest{
		Address: "info@example.com",
		Domain:  "example.com",
		QuotaMB: 2048,
	}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/email-accounts", bytes.NewReader(bodyBytes))
	req = withClaims(req, userClaims("testuser"))
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusCreated, w.Body.String())
	}

	var resp EmailResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.Name != "info-at-example-com" {
		t.Errorf("name = %q, want %q", resp.Name, "info-at-example-com")
	}
	if resp.Namespace != "hosting-user-testuser" {
		t.Errorf("namespace = %q, want %q", resp.Namespace, "hosting-user-testuser")
	}
	if resp.Address != "info@example.com" {
		t.Errorf("address = %q, want %q", resp.Address, "info@example.com")
	}
	if resp.Domain != "example.com" {
		t.Errorf("domain = %q, want %q", resp.Domain, "example.com")
	}
	if resp.QuotaMB != 2048 {
		t.Errorf("quotaMB = %d, want %d", resp.QuotaMB, 2048)
	}

	// Verify the CRD was actually created in the fake client
	obj, err := dynClient.Resource(EmailAccountGVR).Namespace("hosting-user-testuser").Get(context.Background(), "info-at-example-com", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("email account not found in k8s: %v", err)
	}
	if obj.GetName() != "info-at-example-com" {
		t.Errorf("k8s object name = %q, want %q", obj.GetName(), "info-at-example-com")
	}
}

func TestCreateEmail_DefaultQuota(t *testing.T) {
	dynClient := newFakeDynClient()
	handler := NewEmailHandler(dynClient)
	router := setupEmailRouter(handler)

	body := CreateEmailRequest{
		Address: "user@example.com",
		Domain:  "example.com",
		// QuotaMB not specified — should default to 1024
	}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/email-accounts", bytes.NewReader(bodyBytes))
	req = withClaims(req, userClaims("testuser"))
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusCreated, w.Body.String())
	}

	var resp EmailResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if resp.QuotaMB != 1024 {
		t.Errorf("quotaMB = %d, want %d (default)", resp.QuotaMB, 1024)
	}
}

func TestCreateEmail_EmptyAddress(t *testing.T) {
	dynClient := newFakeDynClient()
	handler := NewEmailHandler(dynClient)
	router := setupEmailRouter(handler)

	body := CreateEmailRequest{
		Address: "",
		Domain:  "example.com",
	}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/email-accounts", bytes.NewReader(bodyBytes))
	req = withClaims(req, userClaims("testuser"))
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestCreateEmail_InvalidFormat(t *testing.T) {
	dynClient := newFakeDynClient()
	handler := NewEmailHandler(dynClient)
	router := setupEmailRouter(handler)

	body := CreateEmailRequest{
		Address: "not-an-email",
		Domain:  "example.com",
	}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/email-accounts", bytes.NewReader(bodyBytes))
	req = withClaims(req, userClaims("testuser"))
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d, body: %s", w.Code, http.StatusBadRequest, w.Body.String())
	}
}

func TestCreateEmail_EmptyDomain(t *testing.T) {
	dynClient := newFakeDynClient()
	handler := NewEmailHandler(dynClient)
	router := setupEmailRouter(handler)

	body := CreateEmailRequest{
		Address: "info@example.com",
		Domain:  "",
	}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/email-accounts", bytes.NewReader(bodyBytes))
	req = withClaims(req, userClaims("testuser"))
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestCreateEmail_QuotaExceeded(t *testing.T) {
	// Create a plan with limit of 1 email account and an existing email
	existingEmail := makeEmailAccountObj("existing-at-example-com", "hosting-user-testuser", "existing@example.com", "example.com", 1024)
	plan := makeHostingPlanWithEmailLimit("basic-plan", 1)

	dynClient := newFakeDynClient(existingEmail, plan)
	handler := NewEmailHandler(dynClient)
	router := setupEmailRouter(handler)

	body := CreateEmailRequest{
		Address: "new@example.com",
		Domain:  "example.com",
	}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/email-accounts", bytes.NewReader(bodyBytes))
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

func TestListEmails_UserSeesOwnOnly(t *testing.T) {
	email1 := makeEmailAccountObj("info-at-example-com", "hosting-user-testuser", "info@example.com", "example.com", 1024)
	email2 := makeEmailAccountObj("admin-at-other-com", "hosting-user-otheruser", "admin@other.com", "other.com", 1024)

	dynClient := newFakeDynClient(email1, email2)
	handler := NewEmailHandler(dynClient)
	router := setupEmailRouter(handler)

	req := httptest.NewRequest(http.MethodGet, "/api/email-accounts", nil)
	req = withClaims(req, userClaims("testuser"))
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var emails []EmailResponse
	if err := json.NewDecoder(w.Body).Decode(&emails); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}

	if len(emails) != 1 {
		t.Fatalf("got %d emails, want 1", len(emails))
	}
	if emails[0].Address != "info@example.com" {
		t.Errorf("email address = %q, want %q", emails[0].Address, "info@example.com")
	}
}

func TestListEmails_AdminSeesAll(t *testing.T) {
	email1 := makeEmailAccountObj("info-at-example-com", "hosting-user-testuser", "info@example.com", "example.com", 1024)
	email2 := makeEmailAccountObj("admin-at-other-com", "hosting-user-otheruser", "admin@other.com", "other.com", 1024)

	dynClient := newFakeDynClient(email1, email2)
	handler := NewEmailHandler(dynClient)
	router := setupEmailRouter(handler)

	req := httptest.NewRequest(http.MethodGet, "/api/email-accounts", nil)
	req = withClaims(req, adminClaims())
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var emails []EmailResponse
	if err := json.NewDecoder(w.Body).Decode(&emails); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}

	if len(emails) != 2 {
		t.Errorf("got %d emails, want 2", len(emails))
	}
}

func TestGetEmail_Success(t *testing.T) {
	email := makeEmailAccountObj("info-at-example-com", "hosting-user-testuser", "info@example.com", "example.com", 1024)
	dynClient := newFakeDynClient(email)
	handler := NewEmailHandler(dynClient)
	router := setupEmailRouter(handler)

	req := httptest.NewRequest(http.MethodGet, "/api/email-accounts/info-at-example-com", nil)
	req = withClaims(req, userClaims("testuser"))
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var resp EmailResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if resp.Name != "info-at-example-com" {
		t.Errorf("name = %q, want %q", resp.Name, "info-at-example-com")
	}
	if resp.Address != "info@example.com" {
		t.Errorf("address = %q, want %q", resp.Address, "info@example.com")
	}
}

func TestGetEmail_NotFound(t *testing.T) {
	dynClient := newFakeDynClient()
	handler := NewEmailHandler(dynClient)
	router := setupEmailRouter(handler)

	req := httptest.NewRequest(http.MethodGet, "/api/email-accounts/nonexistent", nil)
	req = withClaims(req, userClaims("testuser"))
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestDeleteEmail_Success(t *testing.T) {
	email := makeEmailAccountObj("info-at-example-com", "hosting-user-testuser", "info@example.com", "example.com", 1024)
	dynClient := newFakeDynClient(email)
	handler := NewEmailHandler(dynClient)
	router := setupEmailRouter(handler)

	req := httptest.NewRequest(http.MethodDelete, "/api/email-accounts/info-at-example-com", nil)
	req = withClaims(req, userClaims("testuser"))
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusNoContent, w.Body.String())
	}

	// Verify it's gone
	_, err := dynClient.Resource(EmailAccountGVR).Namespace("hosting-user-testuser").Get(context.Background(), "info-at-example-com", metav1.GetOptions{})
	if err == nil {
		t.Error("email account should have been deleted")
	}
}
