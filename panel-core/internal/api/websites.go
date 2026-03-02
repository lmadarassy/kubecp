package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/hosting-panel/panel-core/internal/middleware"
)

// --- Request/Response types ---

// WebsiteDomainSSL represents SSL configuration for a domain.
type WebsiteDomainSSL struct {
	Enabled    bool   `json:"enabled"`
	Mode       string `json:"mode,omitempty"`
	Issuer     string `json:"issuer,omitempty"`
	SecretName string `json:"secretName,omitempty"`
}

// WebsitePHP represents PHP configuration.
type WebsitePHP struct {
	Version string `json:"version"`
}

// WebsiteResources represents Kubernetes resource requirements.
type WebsiteResources struct {
	Requests map[string]string `json:"requests,omitempty"`
	Limits   map[string]string `json:"limits,omitempty"`
}

// CreateWebsiteRequest is the JSON body for POST /api/websites.
// Uses new CRD schema: primaryDomain + aliases instead of domains[].
type CreateWebsiteRequest struct {
	PrimaryDomain    string           `json:"primaryDomain"`
	Aliases          []string         `json:"aliases,omitempty"`
	PHP              WebsitePHP       `json:"php"`
	PHPConfigProfile string           `json:"phpConfigProfile,omitempty"`
	SSL              *WebsiteDomainSSL `json:"ssl,omitempty"`
	Replicas         *int32           `json:"replicas,omitempty"`
	Resources        WebsiteResources `json:"resources,omitempty"`
	StorageSize      string           `json:"storageSize,omitempty"`
	CreateDNSZone    bool             `json:"createDnsZone,omitempty"`
	CreateEmailDomain bool           `json:"createEmailDomain,omitempty"`
}

// UpdateWebsiteRequest is the JSON body for PUT /api/websites/{id}.
type UpdateWebsiteRequest struct {
	Aliases          []string          `json:"aliases,omitempty"`
	PHP              *WebsitePHP       `json:"php,omitempty"`
	PHPConfigProfile string            `json:"phpConfigProfile,omitempty"`
	SSL              *WebsiteDomainSSL `json:"ssl,omitempty"`
	Replicas         *int32            `json:"replicas,omitempty"`
	Resources        *WebsiteResources `json:"resources,omitempty"`
	StorageSize      string            `json:"storageSize,omitempty"`
}

// AliasRequest is the JSON body for POST/DELETE /api/websites/{id}/aliases.
type AliasRequest struct {
	Alias string `json:"alias"`
}

// WebsiteDomainStatus represents the status of a domain.
type WebsiteDomainStatus struct {
	Name              string `json:"name"`
	CertificateStatus string `json:"certificateStatus,omitempty"`
	CertificateExpiry string `json:"certificateExpiry,omitempty"`
}

// WebsiteStatusInfo represents the status section of a Website CRD.
type WebsiteStatusInfo struct {
	Phase         string                `json:"phase,omitempty"`
	Replicas      int64                 `json:"replicas,omitempty"`
	ReadyReplicas int64                 `json:"readyReplicas,omitempty"`
	Domains       []WebsiteDomainStatus `json:"domains,omitempty"`
}

// WebsiteResponse is the JSON response for website endpoints.
type WebsiteResponse struct {
	Id               string            `json:"id"`
	Name             string            `json:"name"`
	Namespace        string            `json:"namespace"`
	PrimaryDomain    string            `json:"primaryDomain"`
	Aliases          []string          `json:"aliases,omitempty"`
	Owner            string            `json:"owner"`
	PHP              WebsitePHP        `json:"php"`
	PHPConfigProfile string            `json:"phpConfigProfile,omitempty"`
	SSL              *WebsiteDomainSSL `json:"ssl,omitempty"`
	Replicas         int64             `json:"replicas"`
	Resources        WebsiteResources  `json:"resources,omitempty"`
	StorageSize      string            `json:"storageSize,omitempty"`
	Status           WebsiteStatusInfo `json:"status,omitempty"`
	CreatedAt        string            `json:"createdAt,omitempty"`
}

var validPHPVersions = map[string]bool{
	"7.4": true, "8.0": true, "8.1": true, "8.2": true, "8.3": true, "8.4": true, "8.5": true,
}

// WebsiteHandler implements the website management API endpoints.
type WebsiteHandler struct {
	dynClient  dynamic.Interface
	clientset  kubernetes.Interface
	pdnsClient PDNSZoneCreator
	externalIP string
	mailHost   string
}

// PDNSZoneCreator is an interface for creating DNS zones (for wizard support).
type PDNSZoneCreator interface {
	CreateZone(ctx context.Context, name string, nameservers []string) error
	PatchRRSets(ctx context.Context, zone string, rrsets interface{}) error
}

func NewWebsiteHandler(dynClient dynamic.Interface, clientset kubernetes.Interface) *WebsiteHandler {
	return &WebsiteHandler{dynClient: dynClient, clientset: clientset}
}

// WithDNS configures DNS zone creation support for the website wizard.
func (h *WebsiteHandler) WithDNS(pdns PDNSZoneCreator, externalIP, mailHost string) *WebsiteHandler {
	h.pdnsClient = pdns
	h.externalIP = externalIP
	h.mailHost = mailHost
	return h
}

func (h *WebsiteHandler) RegisterRoutes(r chi.Router) {
	r.Get("/", h.ListWebsites)
	r.Post("/", h.CreateWebsite)
	r.Route("/{id}", func(r chi.Router) {
		r.Get("/", h.GetWebsite)
		r.Put("/", h.UpdateWebsite)
		r.Delete("/", h.DeleteWebsite)
		r.Get("/aliases", h.ListAliases)
		r.Post("/aliases", h.AddAlias)
		r.Delete("/aliases", h.RemoveAlias)
		r.Post("/suspend", h.SuspendWebsite)
		r.Post("/unsuspend", h.UnsuspendWebsite)
		r.Get("/stats", h.GetWebsiteStats)
	})
}

const hostingNamespace = "hosting-system"

func resolveNamespace(r *http.Request) (string, error) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		return "", fmt.Errorf("no claims in context")
	}
	return hostingNamespace, nil
}

func (h *WebsiteHandler) ListWebsites(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		WriteUnauthorized(w, "Authentication required")
		return
	}

	listOpts := metav1.ListOptions{}
	if !middleware.HasRole(claims, "admin") {
		listOpts.LabelSelector = fmt.Sprintf("hosting.panel/user=%s", claims.Username)
	}

	list, err := h.dynClient.Resource(WebsiteGVR).Namespace(hostingNamespace).List(r.Context(), listOpts)
	if err != nil {
		WriteInternalError(w, "Failed to list websites: "+err.Error())
		return
	}

	websites := make([]WebsiteResponse, 0, len(list.Items))
	for _, item := range list.Items {
		websites = append(websites, unstructuredToWebsiteResponse(&item))
	}
	writeJSON(w, http.StatusOK, websites)
}

func (h *WebsiteHandler) CreateWebsite(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		WriteUnauthorized(w, "Authentication required")
		return
	}

	var req CreateWebsiteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteBadRequest(w, "Invalid request body", nil)
		return
	}

	req.PrimaryDomain = strings.TrimSpace(req.PrimaryDomain)
	if req.PrimaryDomain == "" {
		WriteBadRequest(w, "primaryDomain is required", nil)
		return
	}

	if req.PHP.Version == "" {
		req.PHP.Version = "8.2"
	}
	if !validPHPVersions[req.PHP.Version] {
		WriteBadRequest(w, "Invalid PHP version", map[string]interface{}{
			"allowed": []string{"7.4", "8.0", "8.1", "8.2", "8.3", "8.4", "8.5"},
		})
		return
	}
	if req.PHPConfigProfile == "" {
		req.PHPConfigProfile = "default"
	}

	ns := hostingNamespace

	// Check quota
	if err := h.checkWebsiteQuota(r.Context(), ns); err != nil {
		if qErr, ok := err.(*quotaError); ok {
			WriteQuotaExceeded(w, qErr.Error(), map[string]interface{}{
				"resource": "website", "current": qErr.current, "limit": qErr.limit,
			})
			return
		}
	}

	// Check domain uniqueness
	if err := h.checkDomainUniqueness(r.Context(), req.PrimaryDomain, req.Aliases); err != nil {
		WriteConflict(w, err.Error(), nil)
		return
	}

	if req.Replicas == nil {
		def := int32(1)
		req.Replicas = &def
	}
	if req.StorageSize == "" {
		req.StorageSize = "5Gi"
	}

	// Resource name from domain
	resName := sanitizeDomain(req.PrimaryDomain)

	spec := map[string]interface{}{
		"primaryDomain":    req.PrimaryDomain,
		"owner":            claims.Username,
		"php":              map[string]interface{}{"version": req.PHP.Version},
		"phpConfigProfile": req.PHPConfigProfile,
		"replicas":         int64(*req.Replicas),
		"storageSize":      req.StorageSize,
	}
	if len(req.Aliases) > 0 {
		aliases := make([]interface{}, len(req.Aliases))
		for i, a := range req.Aliases {
			aliases[i] = a
		}
		spec["aliases"] = aliases
	}
	if req.SSL != nil {
		spec["ssl"] = sslToUnstructured(req.SSL)
	}
	if len(req.Resources.Requests) > 0 || len(req.Resources.Limits) > 0 {
		spec["resources"] = resourcesToUnstructured(req.Resources)
	}

	// Resolve ownerUID: reuse existing UID from another website of the same owner,
	// or assign a new one (2000 + sequential).
	ownerUID, err := h.resolveOwnerUID(r.Context(), claims.Username)
	if err == nil && ownerUID > 0 {
		spec["ownerUID"] = int64(ownerUID)
	}

	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": CRDGroup + "/" + CRDVersion,
			"kind":       "Website",
			"metadata": map[string]interface{}{
				"name":      resName,
				"namespace": ns,
				"labels": map[string]interface{}{
					"hosting.panel/user": claims.Username,
				},
			},
			"spec": spec,
		},
	}

	created, err := h.dynClient.Resource(WebsiteGVR).Namespace(ns).Create(r.Context(), obj, metav1.CreateOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "already exists") {
			WriteConflict(w, "Website already exists", map[string]string{"primaryDomain": req.PrimaryDomain})
			return
		}
		WriteInternalError(w, "Failed to create website: "+err.Error())
		return
	}

	// Wizard: auto-create DNS zone if requested
	if req.CreateDNSZone && h.pdnsClient != nil {
		_ = h.pdnsClient.CreateZone(r.Context(), req.PrimaryDomain+".", []string{"ns1." + req.PrimaryDomain + "."})
	}

	// Wizard: auto-create EmailDomain CRD if requested
	if req.CreateEmailDomain && h.dynClient != nil {
		edName := sanitizeDomain(req.PrimaryDomain)
		emailDomainObj := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": CRDGroup + "/" + CRDVersion,
				"kind":       "EmailDomain",
				"metadata": map[string]interface{}{
					"name":      edName,
					"namespace": ns,
					"labels": map[string]interface{}{
						"hosting.panel/user": claims.Username,
					},
				},
				"spec": map[string]interface{}{
					"domain":     req.PrimaryDomain,
					"owner":      claims.Username,
					"spamFilter": true,
				},
			},
		}
		_, _ = h.dynClient.Resource(EmailDomainGVR).Namespace(ns).Create(r.Context(), emailDomainObj, metav1.CreateOptions{})
	}

	writeJSON(w, http.StatusCreated, unstructuredToWebsiteResponse(created))
}

func (h *WebsiteHandler) GetWebsite(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		WriteUnauthorized(w, "Authentication required")
		return
	}

	id := chi.URLParam(r, "id")
	obj, err := h.dynClient.Resource(WebsiteGVR).Namespace(hostingNamespace).Get(r.Context(), id, metav1.GetOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			WriteNotFound(w, "Website not found")
			return
		}
		WriteInternalError(w, "Failed to get website: "+err.Error())
		return
	}

	if !middleware.IsOwnerByLabel(claims, obj) {
		WriteForbidden(w, "Access denied: website belongs to another user")
		return
	}

	writeJSON(w, http.StatusOK, unstructuredToWebsiteResponse(obj))
}

func (h *WebsiteHandler) UpdateWebsite(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		WriteUnauthorized(w, "Authentication required")
		return
	}

	id := chi.URLParam(r, "id")
	existing, err := h.dynClient.Resource(WebsiteGVR).Namespace(hostingNamespace).Get(r.Context(), id, metav1.GetOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			WriteNotFound(w, "Website not found")
			return
		}
		WriteInternalError(w, "Failed to get website: "+err.Error())
		return
	}

	if !middleware.IsOwnerByLabel(claims, existing) {
		WriteForbidden(w, "Access denied")
		return
	}

	var req UpdateWebsiteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteBadRequest(w, "Invalid request body", nil)
		return
	}

	spec, _ := existing.Object["spec"].(map[string]interface{})
	if spec == nil {
		spec = make(map[string]interface{})
	}

	if req.PHP != nil {
		if !validPHPVersions[req.PHP.Version] {
			WriteBadRequest(w, "Invalid PHP version", nil)
			return
		}
		spec["php"] = map[string]interface{}{"version": req.PHP.Version}
	}
	if req.PHPConfigProfile != "" {
		spec["phpConfigProfile"] = req.PHPConfigProfile
	}
	if req.Replicas != nil {
		spec["replicas"] = int64(*req.Replicas)
	}
	if req.Aliases != nil {
		aliases := make([]interface{}, len(req.Aliases))
		for i, a := range req.Aliases {
			aliases[i] = a
		}
		spec["aliases"] = aliases
	}
	if req.SSL != nil {
		spec["ssl"] = sslToUnstructured(req.SSL)
	}
	if req.Resources != nil {
		spec["resources"] = resourcesToUnstructured(*req.Resources)
	}
	if req.StorageSize != "" {
		spec["storageSize"] = req.StorageSize
	}

	existing.Object["spec"] = spec
	updated, err := h.dynClient.Resource(WebsiteGVR).Namespace(hostingNamespace).Update(r.Context(), existing, metav1.UpdateOptions{})
	if err != nil {
		WriteInternalError(w, "Failed to update website: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, unstructuredToWebsiteResponse(updated))
}

func (h *WebsiteHandler) DeleteWebsite(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		WriteUnauthorized(w, "Authentication required")
		return
	}

	id := chi.URLParam(r, "id")
	existing, err := h.dynClient.Resource(WebsiteGVR).Namespace(hostingNamespace).Get(r.Context(), id, metav1.GetOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			WriteNotFound(w, "Website not found")
			return
		}
		WriteInternalError(w, "Failed to get website: "+err.Error())
		return
	}

	if !middleware.IsOwnerByLabel(claims, existing) {
		WriteForbidden(w, "Access denied")
		return
	}

	if err := h.dynClient.Resource(WebsiteGVR).Namespace(hostingNamespace).Delete(r.Context(), id, metav1.DeleteOptions{}); err != nil {
		WriteInternalError(w, "Failed to delete website: "+err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// --- Alias management ---

func (h *WebsiteHandler) ListAliases(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		WriteUnauthorized(w, "Authentication required")
		return
	}

	id := chi.URLParam(r, "id")
	obj, err := h.dynClient.Resource(WebsiteGVR).Namespace(hostingNamespace).Get(r.Context(), id, metav1.GetOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			WriteNotFound(w, "Website not found")
			return
		}
		WriteInternalError(w, "Failed to get website: "+err.Error())
		return
	}

	if !middleware.IsOwnerByLabel(claims, obj) {
		WriteForbidden(w, "Access denied")
		return
	}

	aliases := extractAliases(obj)
	writeJSON(w, http.StatusOK, aliases)
}

func (h *WebsiteHandler) AddAlias(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		WriteUnauthorized(w, "Authentication required")
		return
	}

	id := chi.URLParam(r, "id")
	var req AliasRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteBadRequest(w, "Invalid request body", nil)
		return
	}
	req.Alias = strings.TrimSpace(req.Alias)
	if req.Alias == "" {
		WriteBadRequest(w, "alias is required", nil)
		return
	}

	// Check domain uniqueness
	if err := h.checkDomainUniqueness(r.Context(), req.Alias, nil); err != nil {
		WriteConflict(w, err.Error(), nil)
		return
	}

	obj, err := h.dynClient.Resource(WebsiteGVR).Namespace(hostingNamespace).Get(r.Context(), id, metav1.GetOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			WriteNotFound(w, "Website not found")
			return
		}
		WriteInternalError(w, "Failed to get website: "+err.Error())
		return
	}

	if !middleware.IsOwnerByLabel(claims, obj) {
		WriteForbidden(w, "Access denied")
		return
	}

	spec, _ := obj.Object["spec"].(map[string]interface{})
	if spec == nil {
		spec = make(map[string]interface{})
	}

	aliases := extractAliasStrings(spec)
	for _, a := range aliases {
		if a == req.Alias {
			WriteConflict(w, "Alias already exists", nil)
			return
		}
	}
	aliases = append(aliases, req.Alias)
	iAliases := make([]interface{}, len(aliases))
	for i, a := range aliases {
		iAliases[i] = a
	}
	spec["aliases"] = iAliases
	obj.Object["spec"] = spec

	updated, err := h.dynClient.Resource(WebsiteGVR).Namespace(hostingNamespace).Update(r.Context(), obj, metav1.UpdateOptions{})
	if err != nil {
		WriteInternalError(w, "Failed to update website: "+err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, unstructuredToWebsiteResponse(updated))
}

func (h *WebsiteHandler) RemoveAlias(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		WriteUnauthorized(w, "Authentication required")
		return
	}

	id := chi.URLParam(r, "id")
	var req AliasRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteBadRequest(w, "Invalid request body", nil)
		return
	}
	req.Alias = strings.TrimSpace(req.Alias)
	if req.Alias == "" {
		WriteBadRequest(w, "alias is required", nil)
		return
	}

	obj, err := h.dynClient.Resource(WebsiteGVR).Namespace(hostingNamespace).Get(r.Context(), id, metav1.GetOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			WriteNotFound(w, "Website not found")
			return
		}
		WriteInternalError(w, "Failed to get website: "+err.Error())
		return
	}

	if !middleware.IsOwnerByLabel(claims, obj) {
		WriteForbidden(w, "Access denied")
		return
	}

	spec, _ := obj.Object["spec"].(map[string]interface{})
	aliases := extractAliasStrings(spec)
	found := false
	filtered := make([]interface{}, 0)
	for _, a := range aliases {
		if a == req.Alias {
			found = true
			continue
		}
		filtered = append(filtered, a)
	}
	if !found {
		WriteNotFound(w, "Alias not found")
		return
	}
	spec["aliases"] = filtered
	obj.Object["spec"] = spec

	_, err = h.dynClient.Resource(WebsiteGVR).Namespace(hostingNamespace).Update(r.Context(), obj, metav1.UpdateOptions{})
	if err != nil {
		WriteInternalError(w, "Failed to update website: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Suspend/Unsuspend ---

func (h *WebsiteHandler) SuspendWebsite(w http.ResponseWriter, r *http.Request) {
	h.setWebsitePhase(w, r, "Suspended")
}

func (h *WebsiteHandler) UnsuspendWebsite(w http.ResponseWriter, r *http.Request) {
	h.setWebsitePhase(w, r, "Running")
}

func (h *WebsiteHandler) setWebsitePhase(w http.ResponseWriter, r *http.Request, phase string) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		WriteUnauthorized(w, "Authentication required")
		return
	}

	id := chi.URLParam(r, "id")
	obj, err := h.dynClient.Resource(WebsiteGVR).Namespace(hostingNamespace).Get(r.Context(), id, metav1.GetOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			WriteNotFound(w, "Website not found")
			return
		}
		WriteInternalError(w, "Failed to get website: "+err.Error())
		return
	}

	if !middleware.IsOwnerByLabel(claims, obj) {
		WriteForbidden(w, "Access denied")
		return
	}

	status, _, _ := unstructured.NestedMap(obj.Object, "status")
	if status == nil {
		status = map[string]interface{}{}
	}
	status["phase"] = phase
	unstructured.SetNestedMap(obj.Object, status, "status")

	_, err = h.dynClient.Resource(WebsiteGVR).Namespace(hostingNamespace).UpdateStatus(r.Context(), obj, metav1.UpdateOptions{})
	if err != nil {
		WriteInternalError(w, "Failed to update website status: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": phase, "websiteId": id})
}

// GetWebsiteStats handles GET /api/websites/{id}/stats.
// Returns AWStats statistics for the website (placeholder — actual AWStats integration
// requires access log processing configured in the Website_Pod).
func (h *WebsiteHandler) GetWebsiteStats(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		WriteUnauthorized(w, "Authentication required")
		return
	}

	id := chi.URLParam(r, "id")
	obj, err := h.dynClient.Resource(WebsiteGVR).Namespace(hostingNamespace).Get(r.Context(), id, metav1.GetOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			WriteNotFound(w, "Website not found")
			return
		}
		WriteInternalError(w, "Failed to get website: "+err.Error())
		return
	}

	if !middleware.IsOwnerByLabel(claims, obj) {
		WriteForbidden(w, "Access denied")
		return
	}

	// AWStats data would be read from the User_Volume at web/{domain}/awstats/
	// For now, return a placeholder structure
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"websiteId":    id,
		"period":       r.URL.Query().Get("period"),
		"totalVisits":  0,
		"uniqueVisitors": 0,
		"pageViews":    0,
		"bandwidth":    "0 B",
		"message":      "AWStats data not yet available. Access log processing will populate this.",
	})
}

// --- Owner UID resolution ---

// resolveOwnerUID finds the UID for a user by checking existing websites.
// If the user already has a website, reuse that UID. Otherwise, assign the next
// available UID starting from 2000.
func (h *WebsiteHandler) resolveOwnerUID(ctx context.Context, username string) (int32, error) {
	list, err := h.dynClient.Resource(WebsiteGVR).Namespace(hostingNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("hosting.panel/user=%s", username),
	})
	if err != nil {
		return 2000, nil // default to 2000 on error
	}

	// Check if any existing website of this user has an ownerUID
	for _, item := range list.Items {
		spec, _ := item.Object["spec"].(map[string]interface{})
		if spec == nil {
			continue
		}
		if uid, ok := spec["ownerUID"].(int64); ok && uid >= 1000 {
			return int32(uid), nil
		}
		if uid, ok := spec["ownerUID"].(float64); ok && uid >= 1000 {
			return int32(uid), nil
		}
	}

	// No existing UID — find the max UID across all websites and assign next
	allList, err := h.dynClient.Resource(WebsiteGVR).Namespace(hostingNamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return 2000, nil
	}

	maxUID := int32(1999) // start from 2000
	for _, item := range allList.Items {
		spec, _ := item.Object["spec"].(map[string]interface{})
		if spec == nil {
			continue
		}
		var uid int32
		if v, ok := spec["ownerUID"].(int64); ok {
			uid = int32(v)
		} else if v, ok := spec["ownerUID"].(float64); ok {
			uid = int32(v)
		}
		if uid > maxUID {
			maxUID = uid
		}
	}

	return maxUID + 1, nil
}

// --- Domain uniqueness check ---

// checkDomainUniqueness verifies that a domain (primary or alias) is not already used
// by any other website in the cluster.
func (h *WebsiteHandler) checkDomainUniqueness(ctx context.Context, primary string, aliases []string) error {
	list, err := h.dynClient.Resource(WebsiteGVR).Namespace(hostingNamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil // non-blocking
	}

	domainsToCheck := []string{primary}
	domainsToCheck = append(domainsToCheck, aliases...)

	for _, item := range list.Items {
		spec, _ := item.Object["spec"].(map[string]interface{})
		if spec == nil {
			continue
		}
		existingPrimary, _ := spec["primaryDomain"].(string)
		existingAliases := extractAliasStrings(spec)

		allExisting := append([]string{existingPrimary}, existingAliases...)
		for _, check := range domainsToCheck {
			for _, existing := range allExisting {
				if strings.EqualFold(check, existing) {
					return fmt.Errorf("domain %q is already in use", check)
				}
			}
		}
	}
	return nil
}

// --- Quota ---

type quotaError struct {
	current int64
	limit   int64
}

func (e *quotaError) Error() string {
	return fmt.Sprintf("Website quota exceeded (%d/%d)", e.current, e.limit)
}

func (h *WebsiteHandler) checkWebsiteQuota(ctx context.Context, namespace string) error {
	list, err := h.dynClient.Resource(WebsiteGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil
	}
	currentCount := int64(len(list.Items))

	limit, err := h.getWebsiteLimit(ctx, namespace)
	if err != nil {
		return nil
	}
	if limit > 0 && currentCount >= limit {
		return &quotaError{current: currentCount, limit: limit}
	}
	return nil
}

func (h *WebsiteHandler) getWebsiteLimit(ctx context.Context, namespace string) (int64, error) {
	plans, err := h.dynClient.Resource(HostingPlanGVR).List(ctx, metav1.ListOptions{})
	if err != nil || len(plans.Items) == 0 {
		return 0, nil
	}
	plan := plans.Items[0]
	spec, _ := plan.Object["spec"].(map[string]interface{})
	if spec == nil {
		return 0, nil
	}
	limits, _ := spec["limits"].(map[string]interface{})
	if limits == nil {
		return 0, nil
	}
	if wl, ok := limits["websites"].(int64); ok {
		return wl, nil
	}
	if f, ok := limits["websites"].(float64); ok {
		return int64(f), nil
	}
	return 0, nil
}

// --- Helpers ---

func sanitizeDomain(domain string) string {
	return strings.ReplaceAll(domain, ".", "-")
}

func sslToUnstructured(ssl *WebsiteDomainSSL) map[string]interface{} {
	if ssl == nil {
		return nil
	}
	m := map[string]interface{}{"enabled": ssl.Enabled}
	if ssl.Mode != "" {
		m["mode"] = ssl.Mode
	}
	if ssl.Issuer != "" {
		m["issuer"] = ssl.Issuer
	}
	if ssl.SecretName != "" {
		m["secretName"] = ssl.SecretName
	}
	return m
}

func extractAliases(obj *unstructured.Unstructured) []string {
	spec, _ := obj.Object["spec"].(map[string]interface{})
	return extractAliasStrings(spec)
}

func extractAliasStrings(spec map[string]interface{}) []string {
	if spec == nil {
		return nil
	}
	raw, _ := spec["aliases"].([]interface{})
	result := make([]string, 0, len(raw))
	for _, a := range raw {
		if s, ok := a.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

func resourcesToUnstructured(res WebsiteResources) map[string]interface{} {
	result := make(map[string]interface{})
	if len(res.Requests) > 0 {
		requests := make(map[string]interface{})
		for k, v := range res.Requests {
			requests[k] = v
		}
		result["requests"] = requests
	}
	if len(res.Limits) > 0 {
		limits := make(map[string]interface{})
		for k, v := range res.Limits {
			limits[k] = v
		}
		result["limits"] = limits
	}
	return result
}

// unstructuredToWebsiteResponse converts an unstructured Website CRD to a WebsiteResponse.
func unstructuredToWebsiteResponse(obj *unstructured.Unstructured) WebsiteResponse {
	resp := WebsiteResponse{
		Id:        obj.GetName(),
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
		CreatedAt: obj.GetCreationTimestamp().Format("2006-01-02T15:04:05Z"),
	}

	labels := obj.GetLabels()
	if labels != nil {
		resp.Owner = labels["hosting.panel/user"]
	}

	spec, _ := obj.Object["spec"].(map[string]interface{})
	if spec != nil {
		resp.PrimaryDomain, _ = spec["primaryDomain"].(string)
		resp.Owner, _ = spec["owner"].(string)
		resp.PHPConfigProfile, _ = spec["phpConfigProfile"].(string)
		resp.StorageSize, _ = spec["storageSize"].(string)
		resp.Aliases = extractAliasStrings(spec)

		if php, ok := spec["php"].(map[string]interface{}); ok {
			resp.PHP.Version, _ = php["version"].(string)
		}
		if r, ok := spec["replicas"].(int64); ok {
			resp.Replicas = r
		} else if r, ok := spec["replicas"].(float64); ok {
			resp.Replicas = int64(r)
		}
		if ssl, ok := spec["ssl"].(map[string]interface{}); ok {
			resp.SSL = &WebsiteDomainSSL{}
			resp.SSL.Enabled, _ = ssl["enabled"].(bool)
			resp.SSL.Mode, _ = ssl["mode"].(string)
			resp.SSL.Issuer, _ = ssl["issuer"].(string)
			resp.SSL.SecretName, _ = ssl["secretName"].(string)
		}
		if res, ok := spec["resources"].(map[string]interface{}); ok {
			resp.Resources = unstructuredToResources(res)
		}
	}

	status, _ := obj.Object["status"].(map[string]interface{})
	if status != nil {
		resp.Status.Phase, _ = status["phase"].(string)
		if r, ok := status["replicas"].(int64); ok {
			resp.Status.Replicas = r
		} else if r, ok := status["replicas"].(float64); ok {
			resp.Status.Replicas = int64(r)
		}
		if r, ok := status["readyReplicas"].(int64); ok {
			resp.Status.ReadyReplicas = r
		} else if r, ok := status["readyReplicas"].(float64); ok {
			resp.Status.ReadyReplicas = int64(r)
		}
		if ds, ok := status["domains"].([]interface{}); ok {
			for _, d := range ds {
				dm, _ := d.(map[string]interface{})
				if dm == nil {
					continue
				}
				dStatus := WebsiteDomainStatus{}
				dStatus.Name, _ = dm["name"].(string)
				dStatus.CertificateStatus, _ = dm["certificateStatus"].(string)
				dStatus.CertificateExpiry, _ = dm["certificateExpiry"].(string)
				resp.Status.Domains = append(resp.Status.Domains, dStatus)
			}
		}
	}

	return resp
}

func unstructuredToResources(res map[string]interface{}) WebsiteResources {
	wr := WebsiteResources{}
	if requests, ok := res["requests"].(map[string]interface{}); ok {
		wr.Requests = make(map[string]string)
		for k, v := range requests {
			if s, ok := v.(string); ok {
				wr.Requests[k] = s
			}
		}
	}
	if limits, ok := res["limits"].(map[string]interface{}); ok {
		wr.Limits = make(map[string]string)
		for k, v := range limits {
			if s, ok := v.(string); ok {
				wr.Limits[k] = s
			}
		}
	}
	return wr
}
