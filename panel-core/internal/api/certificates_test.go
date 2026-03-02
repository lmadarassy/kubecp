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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
)

// newCertFakeDynClient creates a fake dynamic client with cert-manager Certificate and Secret types.
func newCertFakeDynClient(objects ...runtime.Object) *dynamicfake.FakeDynamicClient {
	scheme := runtime.NewScheme()
	// cert-manager Certificate
	scheme.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "Certificate"},
		&unstructured.Unstructured{},
	)
	scheme.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "CertificateList"},
		&unstructured.UnstructuredList{},
	)
	// Core v1 Secret
	scheme.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Secret"},
		&unstructured.Unstructured{},
	)
	scheme.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: "", Version: "v1", Kind: "SecretList"},
		&unstructured.UnstructuredList{},
	)
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{
			CertificateGVR:                                        "CertificateList",
			{Group: "", Version: "v1", Resource: "secrets"}: "SecretList",
		},
		objects...,
	)
}

func setupCertRouter(handler *CertificateHandler) *chi.Mux {
	r := chi.NewRouter()
	r.Route("/api/certificates", func(r chi.Router) {
		handler.RegisterRoutes(r)
	})
	return r
}

func makeCertObj(name, namespace, domain, issuer string, status map[string]interface{}) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "cert-manager.io/v1",
			"kind":       "Certificate",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
			},
			"spec": map[string]interface{}{
				"secretName": "tls-" + name,
				"dnsNames":   []interface{}{domain},
				"issuerRef": map[string]interface{}{
					"name": issuer,
					"kind": "ClusterIssuer",
				},
			},
		},
	}
	if status != nil {
		obj.Object["status"] = status
	}
	return obj
}

// --- ListCertificates tests ---

func TestListCertificates_AdminSeesAll(t *testing.T) {
	cert1 := makeCertObj("cert-example-com", "hosting-user-alice", "example.com", "letsencrypt-production", nil)
	cert2 := makeCertObj("cert-test-com", "hosting-user-bob", "test.com", "letsencrypt-production", nil)

	dynClient := newCertFakeDynClient(cert1, cert2)
	k8sClient := kubefake.NewSimpleClientset()
	handler := NewCertificateHandler(dynClient, k8sClient, "letsencrypt-production")
	router := setupCertRouter(handler)

	req := httptest.NewRequest(http.MethodGet, "/api/certificates", nil)
	req = withClaims(req, adminClaims())
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var certs []CertificateResponse
	json.NewDecoder(w.Body).Decode(&certs)
	if len(certs) != 2 {
		t.Errorf("got %d certs, want 2", len(certs))
	}
}

func TestListCertificates_UserSeesOwn(t *testing.T) {
	cert1 := makeCertObj("cert-example-com", "hosting-user-alice", "example.com", "letsencrypt-production", nil)
	cert2 := makeCertObj("cert-test-com", "hosting-user-bob", "test.com", "letsencrypt-production", nil)

	dynClient := newCertFakeDynClient(cert1, cert2)
	k8sClient := kubefake.NewSimpleClientset()
	handler := NewCertificateHandler(dynClient, k8sClient, "letsencrypt-production")
	router := setupCertRouter(handler)

	req := httptest.NewRequest(http.MethodGet, "/api/certificates", nil)
	req = withClaims(req, userClaims("alice"))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var certs []CertificateResponse
	json.NewDecoder(w.Body).Decode(&certs)
	if len(certs) != 1 {
		t.Fatalf("got %d certs, want 1", len(certs))
	}
	if certs[0].Domain != "example.com" {
		t.Errorf("domain = %q, want %q", certs[0].Domain, "example.com")
	}
}

func TestListCertificates_EmptyList(t *testing.T) {
	dynClient := newCertFakeDynClient()
	k8sClient := kubefake.NewSimpleClientset()
	handler := NewCertificateHandler(dynClient, k8sClient, "letsencrypt-production")
	router := setupCertRouter(handler)

	req := httptest.NewRequest(http.MethodGet, "/api/certificates", nil)
	req = withClaims(req, userClaims("nobody"))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var certs []CertificateResponse
	json.NewDecoder(w.Body).Decode(&certs)
	if len(certs) != 0 {
		t.Errorf("got %d certs, want 0", len(certs))
	}
}

// --- UploadCertificate tests ---

func TestUploadCertificate_Success(t *testing.T) {
	dynClient := newCertFakeDynClient()
	k8sClient := kubefake.NewSimpleClientset()
	handler := NewCertificateHandler(dynClient, k8sClient, "letsencrypt-production")
	router := setupCertRouter(handler)

	body := UploadCertificateRequest{
		Name:    "my-cert",
		TLSCert: "-----BEGIN CERTIFICATE-----\nMIIB...\n-----END CERTIFICATE-----",
		TLSKey:  "-----BEGIN PRIVATE KEY-----\nMIIE...\n-----END PRIVATE KEY-----",
	}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/certificates/upload", bytes.NewReader(bodyBytes))
	req = withClaims(req, userClaims("alice"))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusCreated, w.Body.String())
	}

	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["name"] != "tls-my-cert" {
		t.Errorf("name = %q, want %q", resp["name"], "tls-my-cert")
	}
	if resp["namespace"] != "hosting-user-alice" {
		t.Errorf("namespace = %q, want %q", resp["namespace"], "hosting-user-alice")
	}
}

func TestUploadCertificate_MissingName(t *testing.T) {
	dynClient := newCertFakeDynClient()
	k8sClient := kubefake.NewSimpleClientset()
	handler := NewCertificateHandler(dynClient, k8sClient, "")
	router := setupCertRouter(handler)

	body := UploadCertificateRequest{
		TLSCert: "cert-data",
		TLSKey:  "key-data",
	}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/certificates/upload", bytes.NewReader(bodyBytes))
	req = withClaims(req, userClaims("alice"))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestUploadCertificate_MissingCertOrKey(t *testing.T) {
	dynClient := newCertFakeDynClient()
	k8sClient := kubefake.NewSimpleClientset()
	handler := NewCertificateHandler(dynClient, k8sClient, "")
	router := setupCertRouter(handler)

	// Missing key
	body := UploadCertificateRequest{Name: "test", TLSCert: "cert-data"}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/certificates/upload", bytes.NewReader(bodyBytes))
	req = withClaims(req, userClaims("alice"))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("missing key: status = %d, want %d", w.Code, http.StatusBadRequest)
	}

	// Missing cert
	body = UploadCertificateRequest{Name: "test", TLSKey: "key-data"}
	bodyBytes, _ = json.Marshal(body)
	req = httptest.NewRequest(http.MethodPost, "/api/certificates/upload", bytes.NewReader(bodyBytes))
	req = withClaims(req, userClaims("alice"))
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("missing cert: status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestUploadCertificate_ForbiddenNamespace(t *testing.T) {
	dynClient := newCertFakeDynClient()
	k8sClient := kubefake.NewSimpleClientset()
	handler := NewCertificateHandler(dynClient, k8sClient, "")
	router := setupCertRouter(handler)

	body := UploadCertificateRequest{
		Name:      "hack",
		Namespace: "hosting-user-bob",
		TLSCert:   "cert-data",
		TLSKey:    "key-data",
	}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/certificates/upload", bytes.NewReader(bodyBytes))
	req = withClaims(req, userClaims("alice"))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
}

func TestUploadCertificate_AdminCanUploadToAnyNamespace(t *testing.T) {
	dynClient := newCertFakeDynClient()
	k8sClient := kubefake.NewSimpleClientset()
	handler := NewCertificateHandler(dynClient, k8sClient, "")
	router := setupCertRouter(handler)

	body := UploadCertificateRequest{
		Name:      "admin-cert",
		Namespace: "hosting-user-bob",
		TLSCert:   "cert-data",
		TLSKey:    "key-data",
	}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/certificates/upload", bytes.NewReader(bodyBytes))
	req = withClaims(req, adminClaims())
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusCreated, w.Body.String())
	}

	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["namespace"] != "hosting-user-bob" {
		t.Errorf("namespace = %q, want %q", resp["namespace"], "hosting-user-bob")
	}
}

func TestUploadCertificate_Duplicate(t *testing.T) {
	dynClient := newCertFakeDynClient()
	k8sClient := kubefake.NewSimpleClientset()
	handler := NewCertificateHandler(dynClient, k8sClient, "")
	router := setupCertRouter(handler)

	body := UploadCertificateRequest{
		Name:    "dup-cert",
		TLSCert: "cert-data",
		TLSKey:  "key-data",
	}
	bodyBytes, _ := json.Marshal(body)

	// First upload
	req := httptest.NewRequest(http.MethodPost, "/api/certificates/upload", bytes.NewReader(bodyBytes))
	req = withClaims(req, userClaims("alice"))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("first upload: status = %d", w.Code)
	}

	// Second upload — should conflict
	bodyBytes, _ = json.Marshal(body)
	req = httptest.NewRequest(http.MethodPost, "/api/certificates/upload", bytes.NewReader(bodyBytes))
	req = withClaims(req, userClaims("alice"))
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("duplicate: status = %d, want %d, body: %s", w.Code, http.StatusConflict, w.Body.String())
	}
}

// --- CreateCertificateForDomain tests ---

func TestCreateCertificateForDomain_Success(t *testing.T) {
	dynClient := newCertFakeDynClient()
	k8sClient := kubefake.NewSimpleClientset()
	handler := NewCertificateHandler(dynClient, k8sClient, "letsencrypt-staging")

	err := handler.CreateCertificateForDomain(context.Background(), "example.com", "hosting-user-alice")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify Certificate CRD was created
	certGVR := CertificateGVR
	list, err := dynClient.Resource(certGVR).Namespace("hosting-user-alice").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list error: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("got %d certs, want 1", len(list.Items))
	}

	cert := list.Items[0]
	if cert.GetName() != "cert-example-com" {
		t.Errorf("name = %q, want %q", cert.GetName(), "cert-example-com")
	}

	spec, _ := cert.Object["spec"].(map[string]interface{})
	issuerRef, _ := spec["issuerRef"].(map[string]interface{})
	if issuerRef["name"] != "letsencrypt-staging" {
		t.Errorf("issuer = %q, want %q", issuerRef["name"], "letsencrypt-staging")
	}
}

func TestCreateCertificateForDomain_AlreadyExists(t *testing.T) {
	dynClient := newCertFakeDynClient()
	k8sClient := kubefake.NewSimpleClientset()
	handler := NewCertificateHandler(dynClient, k8sClient, "letsencrypt-production")

	// Create twice — second should not error
	err := handler.CreateCertificateForDomain(context.Background(), "test.com", "hosting-user-alice")
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	err = handler.CreateCertificateForDomain(context.Background(), "test.com", "hosting-user-alice")
	if err != nil {
		t.Errorf("second create should not error, got: %v", err)
	}
}

func TestCreateCertificateForDomain_DefaultIssuer(t *testing.T) {
	dynClient := newCertFakeDynClient()
	k8sClient := kubefake.NewSimpleClientset()
	handler := NewCertificateHandler(dynClient, k8sClient, "") // empty → default

	err := handler.CreateCertificateForDomain(context.Background(), "default.com", "hosting-user-alice")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	list, _ := dynClient.Resource(CertificateGVR).Namespace("hosting-user-alice").List(context.Background(), metav1.ListOptions{})
	cert := list.Items[0]
	spec, _ := cert.Object["spec"].(map[string]interface{})
	issuerRef, _ := spec["issuerRef"].(map[string]interface{})
	if issuerRef["name"] != "letsencrypt-production" {
		t.Errorf("issuer = %q, want %q", issuerRef["name"], "letsencrypt-production")
	}
}

// --- determineCertStatus tests ---

func TestDetermineCertStatus_Valid(t *testing.T) {
	status := map[string]interface{}{
		"notAfter": "2027-06-01T00:00:00Z",
		"conditions": []interface{}{
			map[string]interface{}{
				"type":   "Ready",
				"status": "True",
			},
		},
	}
	got := determineCertStatus(status)
	if got != "Valid" {
		t.Errorf("status = %q, want %q", got, "Valid")
	}
}

func TestDetermineCertStatus_Expiring(t *testing.T) {
	status := map[string]interface{}{
		"notAfter": "2026-03-10T00:00:00Z", // ~14 days from now (Feb 24, 2026)
		"conditions": []interface{}{
			map[string]interface{}{
				"type":   "Ready",
				"status": "True",
			},
		},
	}
	got := determineCertStatus(status)
	if got != "Expiring" {
		t.Errorf("status = %q, want %q", got, "Expiring")
	}
}

func TestDetermineCertStatus_Expired(t *testing.T) {
	status := map[string]interface{}{
		"notAfter": "2024-01-01T00:00:00Z",
		"conditions": []interface{}{
			map[string]interface{}{
				"type":   "Ready",
				"status": "True",
			},
		},
	}
	got := determineCertStatus(status)
	if got != "Expired" {
		t.Errorf("status = %q, want %q", got, "Expired")
	}
}

func TestDetermineCertStatus_Error(t *testing.T) {
	status := map[string]interface{}{
		"conditions": []interface{}{
			map[string]interface{}{
				"type":    "Ready",
				"status":  "False",
				"message": "ACME challenge failed",
			},
		},
	}
	got := determineCertStatus(status)
	if got != "Error" {
		t.Errorf("status = %q, want %q", got, "Error")
	}
}

func TestDetermineCertStatus_Pending(t *testing.T) {
	// No conditions at all
	status := map[string]interface{}{}
	got := determineCertStatus(status)
	if got != "Pending" {
		t.Errorf("status = %q, want %q", got, "Pending")
	}
}

// --- certToResponse tests ---

func TestCertToResponse_FullStatus(t *testing.T) {
	obj := makeCertObj("cert-example-com", "hosting-user-alice", "example.com", "letsencrypt-production",
		map[string]interface{}{
			"notBefore": "2026-01-01T00:00:00Z",
			"notAfter":  "2027-01-01T00:00:00Z",
			"conditions": []interface{}{
				map[string]interface{}{
					"type":   "Ready",
					"status": "True",
				},
			},
		},
	)

	resp := certToResponse(obj)
	if resp.Name != "cert-example-com" {
		t.Errorf("name = %q, want %q", resp.Name, "cert-example-com")
	}
	if resp.Domain != "example.com" {
		t.Errorf("domain = %q, want %q", resp.Domain, "example.com")
	}
	if resp.Issuer != "letsencrypt-production" {
		t.Errorf("issuer = %q, want %q", resp.Issuer, "letsencrypt-production")
	}
	if resp.Status != "Valid" {
		t.Errorf("status = %q, want %q", resp.Status, "Valid")
	}
	if resp.NotBefore != "2026-01-01T00:00:00Z" {
		t.Errorf("notBefore = %q", resp.NotBefore)
	}
	if resp.NotAfter != "2027-01-01T00:00:00Z" {
		t.Errorf("notAfter = %q", resp.NotAfter)
	}
}

func TestCertToResponse_NoStatus(t *testing.T) {
	obj := makeCertObj("cert-pending", "hosting-user-alice", "pending.com", "letsencrypt-production", nil)

	resp := certToResponse(obj)
	if resp.Status != "Pending" {
		t.Errorf("status = %q, want %q", resp.Status, "Pending")
	}
}

func TestCertToResponse_ErrorWithMessage(t *testing.T) {
	obj := makeCertObj("cert-err", "hosting-user-alice", "err.com", "letsencrypt-production",
		map[string]interface{}{
			"conditions": []interface{}{
				map[string]interface{}{
					"type":    "Ready",
					"status":  "False",
					"message": "DNS validation failed",
				},
			},
		},
	)

	resp := certToResponse(obj)
	if resp.Status != "Error" {
		t.Errorf("status = %q, want %q", resp.Status, "Error")
	}
	if resp.Message != "DNS validation failed" {
		t.Errorf("message = %q, want %q", resp.Message, "DNS validation failed")
	}
}
