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
	"k8s.io/client-go/kubernetes/fake"

	"github.com/hosting-panel/panel-core/internal/middleware"
)

func newFakeDynClient(objects ...runtime.Object) *dynamicfake.FakeDynamicClient {
	scheme := runtime.NewScheme()
	for _, gvk := range []schema.GroupVersionKind{
		{Group: CRDGroup, Version: CRDVersion, Kind: "Website"},
		{Group: CRDGroup, Version: CRDVersion, Kind: "HostingPlan"},
		{Group: CRDGroup, Version: CRDVersion, Kind: "Database"},
		{Group: CRDGroup, Version: CRDVersion, Kind: "EmailAccount"},
		{Group: CRDGroup, Version: CRDVersion, Kind: "EmailDomain"},
		{Group: CRDGroup, Version: CRDVersion, Kind: "SFTPAccount"},
	} {
		scheme.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
		listGVK := gvk
		listGVK.Kind += "List"
		scheme.AddKnownTypeWithName(listGVK, &unstructured.UnstructuredList{})
	}
	for _, gvk := range []schema.GroupVersionKind{
		{Group: "", Version: "v1", Kind: "ResourceQuota"},
		{Group: "", Version: "v1", Kind: "LimitRange"},
	} {
		scheme.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
		listGVK := gvk
		listGVK.Kind += "List"
		scheme.AddKnownTypeWithName(listGVK, &unstructured.UnstructuredList{})
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{
			WebsiteGVR:      "WebsiteList",
			HostingPlanGVR:  "HostingPlanList",
			DatabaseGVR:     "DatabaseList",
			EmailAccountGVR: "EmailAccountList",
			EmailDomainGVR:  "EmailDomainList",
			SFTPAccountGVR:  "SFTPAccountList",
			{Group: "", Version: "v1", Resource: "resourcequotas"}: "ResourceQuotaList",
			{Group: "", Version: "v1", Resource: "limitranges"}:    "LimitRangeList",
		},
		objects...,
	)
}

func withClaims(r *http.Request, claims *middleware.TokenClaims) *http.Request {
	ctx := context.WithValue(r.Context(), middleware.ClaimsContextKey, claims)
	return r.WithContext(ctx)
}

func userClaims(username string) *middleware.TokenClaims {
	return &middleware.TokenClaims{
		Subject:  "user-id-" + username,
		Email:    username + "@example.com",
		Username: username,
		Roles:    []string{"user"},
	}
}

func adminClaims() *middleware.TokenClaims {
	return &middleware.TokenClaims{
		Subject:  "admin-id",
		Email:    "admin@example.com",
		Username: "admin",
		Roles:    []string{"admin"},
	}
}

func setupRouter(handler *WebsiteHandler) *chi.Mux {
	r := chi.NewRouter()
	r.Route("/api/websites", func(r chi.Router) {
		handler.RegisterRoutes(r)
	})
	return r
}

// makeWebsiteObj creates an unstructured Website CRD using the new schema.
func makeWebsiteObj(name, namespace, phpVersion string, replicas int64) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": CRDGroup + "/" + CRDVersion,
			"kind":       "Website",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
				"labels": map[string]interface{}{
					"hosting.panel/user": "testuser",
				},
			},
			"spec": map[string]interface{}{
				"primaryDomain":    "example.com",
				"owner":            "testuser",
				"php":              map[string]interface{}{"version": phpVersion},
				"phpConfigProfile": "default",
				"replicas":         replicas,
				"storageSize":      "5Gi",
				"aliases":          []interface{}{"www.example.com"},
			},
		},
	}
}

func makeHostingPlan(name string, websiteLimit int64) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": CRDGroup + "/" + CRDVersion,
			"kind":       "HostingPlan",
			"metadata":   map[string]interface{}{"name": name},
			"spec": map[string]interface{}{
				"displayName": "Test Plan",
				"limits": map[string]interface{}{
					"websites":      websiteLimit,
					"databases":     int64(10),
					"emailAccounts": int64(20),
				},
			},
		},
	}
}

func TestCreateWebsite_Success(t *testing.T) {
	dynClient := newFakeDynClient()
	k8sClient := fake.NewSimpleClientset()
	handler := NewWebsiteHandler(dynClient, k8sClient)
	router := setupRouter(handler)

	body := CreateWebsiteRequest{
		PrimaryDomain: "mysite.com",
		PHP:           WebsitePHP{Version: "8.2"},
	}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/websites", bytes.NewReader(bodyBytes))
	req = withClaims(req, userClaims("testuser"))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusCreated, w.Body.String())
	}

	var resp WebsiteResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.PrimaryDomain != "mysite.com" {
		t.Errorf("primaryDomain = %q, want %q", resp.PrimaryDomain, "mysite.com")
	}
	if resp.PHP.Version != "8.2" {
		t.Errorf("php version = %q, want %q", resp.PHP.Version, "8.2")
	}
}

func TestCreateWebsite_InvalidPHPVersion(t *testing.T) {
	dynClient := newFakeDynClient()
	k8sClient := fake.NewSimpleClientset()
	handler := NewWebsiteHandler(dynClient, k8sClient)
	router := setupRouter(handler)

	body := CreateWebsiteRequest{PrimaryDomain: "test.com", PHP: WebsitePHP{Version: "9.0"}}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/websites", bytes.NewReader(bodyBytes))
	req = withClaims(req, userClaims("testuser"))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestCreateWebsite_EmptyDomain(t *testing.T) {
	dynClient := newFakeDynClient()
	k8sClient := fake.NewSimpleClientset()
	handler := NewWebsiteHandler(dynClient, k8sClient)
	router := setupRouter(handler)

	body := CreateWebsiteRequest{PrimaryDomain: "", PHP: WebsitePHP{Version: "8.2"}}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/websites", bytes.NewReader(bodyBytes))
	req = withClaims(req, userClaims("testuser"))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestListWebsites_UserSeesOwnOnly(t *testing.T) {
	site1 := makeWebsiteObj("site1", hostingNamespace, "8.2", 1)
	site2 := makeWebsiteObj("site2", hostingNamespace, "8.1", 1)
	site2.SetLabels(map[string]string{"hosting.panel/user": "otheruser"})

	dynClient := newFakeDynClient(site1, site2)
	k8sClient := fake.NewSimpleClientset()
	handler := NewWebsiteHandler(dynClient, k8sClient)
	router := setupRouter(handler)

	req := httptest.NewRequest(http.MethodGet, "/api/websites", nil)
	req = withClaims(req, userClaims("testuser"))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var websites []WebsiteResponse
	json.NewDecoder(w.Body).Decode(&websites)
	if len(websites) != 1 {
		t.Fatalf("got %d websites, want 1", len(websites))
	}
}

func TestGetWebsite_Success(t *testing.T) {
	site := makeWebsiteObj("my-site", hostingNamespace, "8.2", 1)
	dynClient := newFakeDynClient(site)
	k8sClient := fake.NewSimpleClientset()
	handler := NewWebsiteHandler(dynClient, k8sClient)
	router := setupRouter(handler)

	req := httptest.NewRequest(http.MethodGet, "/api/websites/my-site", nil)
	req = withClaims(req, userClaims("testuser"))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var resp WebsiteResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.PrimaryDomain != "example.com" {
		t.Errorf("primaryDomain = %q, want %q", resp.PrimaryDomain, "example.com")
	}
}

func TestDeleteWebsite_Success(t *testing.T) {
	site := makeWebsiteObj("my-site", hostingNamespace, "8.2", 1)
	dynClient := newFakeDynClient(site)
	k8sClient := fake.NewSimpleClientset()
	handler := NewWebsiteHandler(dynClient, k8sClient)
	router := setupRouter(handler)

	req := httptest.NewRequest(http.MethodDelete, "/api/websites/my-site", nil)
	req = withClaims(req, userClaims("testuser"))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNoContent)
	}

	_, err := dynClient.Resource(WebsiteGVR).Namespace(hostingNamespace).Get(context.Background(), "my-site", metav1.GetOptions{})
	if err == nil {
		t.Error("website should have been deleted")
	}
}

func TestAddAlias_Success(t *testing.T) {
	site := makeWebsiteObj("my-site", hostingNamespace, "8.2", 1)
	dynClient := newFakeDynClient(site)
	k8sClient := fake.NewSimpleClientset()
	handler := NewWebsiteHandler(dynClient, k8sClient)
	router := setupRouter(handler)

	body := AliasRequest{Alias: "new.example.com"}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/websites/my-site/aliases", bytes.NewReader(bodyBytes))
	req = withClaims(req, userClaims("testuser"))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusCreated, w.Body.String())
	}

	var resp WebsiteResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if len(resp.Aliases) != 2 {
		t.Fatalf("got %d aliases, want 2", len(resp.Aliases))
	}
}

func TestRemoveAlias_Success(t *testing.T) {
	site := makeWebsiteObj("my-site", hostingNamespace, "8.2", 1)
	dynClient := newFakeDynClient(site)
	k8sClient := fake.NewSimpleClientset()
	handler := NewWebsiteHandler(dynClient, k8sClient)
	router := setupRouter(handler)

	body := AliasRequest{Alias: "www.example.com"}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodDelete, "/api/websites/my-site/aliases", bytes.NewReader(bodyBytes))
	req = withClaims(req, userClaims("testuser"))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNoContent)
	}
}

func TestCreateWebsite_DefaultPHPVersion(t *testing.T) {
	dynClient := newFakeDynClient()
	k8sClient := fake.NewSimpleClientset()
	handler := NewWebsiteHandler(dynClient, k8sClient)
	router := setupRouter(handler)

	body := CreateWebsiteRequest{PrimaryDomain: "default-php.com"}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/websites", bytes.NewReader(bodyBytes))
	req = withClaims(req, userClaims("testuser"))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusCreated, w.Body.String())
	}

	var resp WebsiteResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.PHP.Version != "8.2" {
		t.Errorf("php version = %q, want %q", resp.PHP.Version, "8.2")
	}
}
