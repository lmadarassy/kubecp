package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"

	"github.com/hosting-panel/panel-core/internal/middleware"
)

// --- Request/Response types ---

type CreateEmailDomainRequest struct {
	Domain     string `json:"domain"`
	SpamFilter bool   `json:"spamFilter"`
	CatchAll   string `json:"catchAll,omitempty"`
}

type UpdateEmailDomainRequest struct {
	SpamFilter *bool  `json:"spamFilter,omitempty"`
	CatchAll   string `json:"catchAll,omitempty"`
}

type EmailDomainResponse struct {
	Id             string `json:"id"`
	Domain         string `json:"domain"`
	Owner          string `json:"owner"`
	SpamFilter     bool   `json:"spamFilter"`
	CatchAll       string `json:"catchAll,omitempty"`
	DKIMSecretName string `json:"dkimSecretName,omitempty"`
	AccountCount   int64  `json:"accountCount"`
	Phase          string `json:"phase,omitempty"`
	CreatedAt      string `json:"createdAt,omitempty"`
}

type EmailDomainHandler struct {
	dynClient dynamic.Interface
}

func NewEmailDomainHandler(dynClient dynamic.Interface) *EmailDomainHandler {
	return &EmailDomainHandler{dynClient: dynClient}
}

func (h *EmailDomainHandler) RegisterRoutes(r chi.Router) {
	r.Get("/", h.ListEmailDomains)
	r.Post("/", h.CreateEmailDomain)
	r.Route("/{id}", func(r chi.Router) {
		r.Get("/", h.GetEmailDomain)
		r.Delete("/", h.DeleteEmailDomain)
		r.Post("/dkim/rotate", h.RotateDKIM)
		r.Get("/spam-config", h.GetSpamConfig)
		r.Put("/spam-config", h.UpdateSpamConfig)
		r.Put("/catchall", h.UpdateCatchAll)
	})
}

func (h *EmailDomainHandler) ListEmailDomains(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		WriteUnauthorized(w, "Authentication required")
		return
	}

	listOpts := metav1.ListOptions{}
	if !middleware.HasRole(claims, "admin") {
		listOpts.LabelSelector = fmt.Sprintf("hosting.panel/user=%s", claims.Username)
	}

	list, err := h.dynClient.Resource(EmailDomainGVR).Namespace(hostingNamespace).List(r.Context(), listOpts)
	if err != nil {
		WriteInternalError(w, "Failed to list email domains: "+err.Error())
		return
	}

	domains := make([]EmailDomainResponse, 0, len(list.Items))
	for _, item := range list.Items {
		domains = append(domains, unstructuredToEmailDomainResponse(&item))
	}
	writeJSON(w, http.StatusOK, domains)
}

func (h *EmailDomainHandler) CreateEmailDomain(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		WriteUnauthorized(w, "Authentication required")
		return
	}

	var req CreateEmailDomainRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteBadRequest(w, "Invalid request body", nil)
		return
	}
	req.Domain = strings.TrimSpace(req.Domain)
	if req.Domain == "" {
		WriteBadRequest(w, "domain is required", nil)
		return
	}

	resName := sanitizeDomain(req.Domain)

	// Check domain uniqueness across ALL users — a domain can only belong to one user
	existing, err := h.dynClient.Resource(EmailDomainGVR).Namespace(hostingNamespace).Get(r.Context(), resName, metav1.GetOptions{})
	if err == nil {
		// Domain already exists — check who owns it
		existingLabels := existing.GetLabels()
		existingOwner := ""
		if existingLabels != nil {
			existingOwner = existingLabels["hosting.panel/user"]
		}
		if existingOwner == claims.Username {
			WriteConflict(w, "Email domain already registered to your account", nil)
		} else {
			WriteConflict(w, "Email domain is already registered by another user", nil)
		}
		return
	}

	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": CRDGroup + "/" + CRDVersion,
			"kind":       "EmailDomain",
			"metadata": map[string]interface{}{
				"name":      resName,
				"namespace": hostingNamespace,
				"labels": map[string]interface{}{
					"hosting.panel/user": claims.Username,
				},
			},
			"spec": map[string]interface{}{
				"domain":     req.Domain,
				"owner":      claims.Username,
				"spamFilter": req.SpamFilter,
				"catchAll":   req.CatchAll,
			},
		},
	}

	created, err := h.dynClient.Resource(EmailDomainGVR).Namespace(hostingNamespace).Create(r.Context(), obj, metav1.CreateOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "already exists") {
			WriteConflict(w, "Email domain already exists", nil)
			return
		}
		WriteInternalError(w, "Failed to create email domain: "+err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, unstructuredToEmailDomainResponse(created))
}

func (h *EmailDomainHandler) GetEmailDomain(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		WriteUnauthorized(w, "Authentication required")
		return
	}

	id := chi.URLParam(r, "id")
	obj, err := h.dynClient.Resource(EmailDomainGVR).Namespace(hostingNamespace).Get(r.Context(), id, metav1.GetOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			WriteNotFound(w, "Email domain not found")
			return
		}
		WriteInternalError(w, "Failed to get email domain: "+err.Error())
		return
	}

	if !middleware.IsOwnerByLabel(claims, obj) {
		WriteForbidden(w, "Access denied")
		return
	}

	writeJSON(w, http.StatusOK, unstructuredToEmailDomainResponse(obj))
}

func (h *EmailDomainHandler) DeleteEmailDomain(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		WriteUnauthorized(w, "Authentication required")
		return
	}

	id := chi.URLParam(r, "id")
	obj, err := h.dynClient.Resource(EmailDomainGVR).Namespace(hostingNamespace).Get(r.Context(), id, metav1.GetOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			WriteNotFound(w, "Email domain not found")
			return
		}
		WriteInternalError(w, "Failed to get email domain: "+err.Error())
		return
	}

	if !middleware.IsOwnerByLabel(claims, obj) {
		WriteForbidden(w, "Access denied")
		return
	}

	// Cascade delete: remove all EmailAccounts for this domain
	spec, _ := obj.Object["spec"].(map[string]interface{})
	domain, _ := spec["domain"].(string)
	if domain != "" {
		accounts, _ := h.dynClient.Resource(EmailAccountGVR).Namespace(hostingNamespace).List(r.Context(), metav1.ListOptions{
			LabelSelector: fmt.Sprintf("hosting.panel/email-domain=%s", domain),
		})
		if accounts != nil {
			for _, acc := range accounts.Items {
				_ = h.dynClient.Resource(EmailAccountGVR).Namespace(hostingNamespace).Delete(r.Context(), acc.GetName(), metav1.DeleteOptions{})
			}
		}
	}

	if err := h.dynClient.Resource(EmailDomainGVR).Namespace(hostingNamespace).Delete(r.Context(), id, metav1.DeleteOptions{}); err != nil {
		WriteInternalError(w, "Failed to delete email domain: "+err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *EmailDomainHandler) RotateDKIM(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		WriteUnauthorized(w, "Authentication required")
		return
	}

	id := chi.URLParam(r, "id")
	obj, err := h.dynClient.Resource(EmailDomainGVR).Namespace(hostingNamespace).Get(r.Context(), id, metav1.GetOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			WriteNotFound(w, "Email domain not found")
			return
		}
		WriteInternalError(w, "Failed to get email domain: "+err.Error())
		return
	}

	if !middleware.IsOwnerByLabel(claims, obj) {
		WriteForbidden(w, "Access denied")
		return
	}

	// Clear DKIM secret name in status to trigger re-generation by operator
	status, _, _ := unstructured.NestedMap(obj.Object, "status")
	if status == nil {
		status = map[string]interface{}{}
	}
	status["dkimSecretName"] = ""
	unstructured.SetNestedMap(obj.Object, status, "status")
	_, _ = h.dynClient.Resource(EmailDomainGVR).Namespace(hostingNamespace).UpdateStatus(r.Context(), obj, metav1.UpdateOptions{})

	writeJSON(w, http.StatusOK, map[string]string{"status": "rotating", "message": "DKIM key rotation initiated"})
}

func (h *EmailDomainHandler) GetSpamConfig(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		WriteUnauthorized(w, "Authentication required")
		return
	}

	id := chi.URLParam(r, "id")
	obj, err := h.dynClient.Resource(EmailDomainGVR).Namespace(hostingNamespace).Get(r.Context(), id, metav1.GetOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			WriteNotFound(w, "Email domain not found")
			return
		}
		WriteInternalError(w, "Failed to get email domain: "+err.Error())
		return
	}

	if !middleware.IsOwnerByLabel(claims, obj) {
		WriteForbidden(w, "Access denied")
		return
	}

	spec, _ := obj.Object["spec"].(map[string]interface{})
	spamFilter, _ := spec["spamFilter"].(bool)
	writeJSON(w, http.StatusOK, map[string]interface{}{"spamFilter": spamFilter})
}

func (h *EmailDomainHandler) UpdateSpamConfig(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		WriteUnauthorized(w, "Authentication required")
		return
	}

	id := chi.URLParam(r, "id")
	var req UpdateEmailDomainRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteBadRequest(w, "Invalid request body", nil)
		return
	}

	obj, err := h.dynClient.Resource(EmailDomainGVR).Namespace(hostingNamespace).Get(r.Context(), id, metav1.GetOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			WriteNotFound(w, "Email domain not found")
			return
		}
		WriteInternalError(w, "Failed to get email domain: "+err.Error())
		return
	}

	if !middleware.IsOwnerByLabel(claims, obj) {
		WriteForbidden(w, "Access denied")
		return
	}

	spec, _ := obj.Object["spec"].(map[string]interface{})
	if req.SpamFilter != nil {
		spec["spamFilter"] = *req.SpamFilter
	}
	obj.Object["spec"] = spec

	updated, err := h.dynClient.Resource(EmailDomainGVR).Namespace(hostingNamespace).Update(r.Context(), obj, metav1.UpdateOptions{})
	if err != nil {
		WriteInternalError(w, "Failed to update email domain: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, unstructuredToEmailDomainResponse(updated))
}

func (h *EmailDomainHandler) UpdateCatchAll(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		WriteUnauthorized(w, "Authentication required")
		return
	}

	id := chi.URLParam(r, "id")
	var body struct {
		CatchAll string `json:"catchAll"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteBadRequest(w, "Invalid request body", nil)
		return
	}

	obj, err := h.dynClient.Resource(EmailDomainGVR).Namespace(hostingNamespace).Get(r.Context(), id, metav1.GetOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			WriteNotFound(w, "Email domain not found")
			return
		}
		WriteInternalError(w, "Failed to get email domain: "+err.Error())
		return
	}

	if !middleware.IsOwnerByLabel(claims, obj) {
		WriteForbidden(w, "Access denied")
		return
	}

	spec, _ := obj.Object["spec"].(map[string]interface{})
	spec["catchAll"] = body.CatchAll
	obj.Object["spec"] = spec

	updated, err := h.dynClient.Resource(EmailDomainGVR).Namespace(hostingNamespace).Update(r.Context(), obj, metav1.UpdateOptions{})
	if err != nil {
		WriteInternalError(w, "Failed to update email domain: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, unstructuredToEmailDomainResponse(updated))
}

func unstructuredToEmailDomainResponse(obj *unstructured.Unstructured) EmailDomainResponse {
	resp := EmailDomainResponse{
		Id:        obj.GetName(),
		CreatedAt: obj.GetCreationTimestamp().Format("2006-01-02T15:04:05Z"),
	}
	labels := obj.GetLabels()
	if labels != nil {
		resp.Owner = labels["hosting.panel/user"]
	}
	spec, _ := obj.Object["spec"].(map[string]interface{})
	if spec != nil {
		resp.Domain, _ = spec["domain"].(string)
		resp.Owner, _ = spec["owner"].(string)
		resp.SpamFilter, _ = spec["spamFilter"].(bool)
		resp.CatchAll, _ = spec["catchAll"].(string)
	}
	status, _ := obj.Object["status"].(map[string]interface{})
	if status != nil {
		resp.Phase, _ = status["phase"].(string)
		resp.DKIMSecretName, _ = status["dkimSecretName"].(string)
		if ac, ok := status["accountCount"].(int64); ok {
			resp.AccountCount = ac
		} else if ac, ok := status["accountCount"].(float64); ok {
			resp.AccountCount = int64(ac)
		}
	}
	return resp
}
