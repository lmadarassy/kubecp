package api

import (
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/hosting-panel/panel-core/internal/keycloak"
	"github.com/hosting-panel/panel-core/internal/middleware"
)

// --- Request/Response types ---

// CreateEmailRequest is the JSON body for POST /api/email-accounts.
type CreateEmailRequest struct {
	Address    string            `json:"address"`
	Domain     string            `json:"domain"`
	Password   string            `json:"password"`
	QuotaMB    int32             `json:"quotaMB,omitempty"`
	Forwarding *EmailForwarding  `json:"forwarding,omitempty"`
}

// EmailForwarding represents forwarding configuration.
type EmailForwarding struct {
	Enabled   bool     `json:"enabled"`
	Addresses []string `json:"addresses,omitempty"`
	KeepCopy  bool     `json:"keepCopy"`
}

// UpdateEmailForwardingRequest is the JSON body for PUT /api/email-accounts/{id}/forwarding.
type UpdateEmailForwardingRequest struct {
	Enabled   bool     `json:"enabled"`
	Addresses []string `json:"addresses,omitempty"`
	KeepCopy  bool     `json:"keepCopy"`
}

// UpdateEmailQuotaRequest is the JSON body for PUT /api/email-accounts/{id}/quota.
type UpdateEmailQuotaRequest struct {
	QuotaMB int32 `json:"quotaMB"`
}

// EmailStatusInfo represents the status section of an EmailAccount CRD.
type EmailStatusInfo struct {
	Phase  string `json:"phase,omitempty"`
	UsedMB int64  `json:"usedMB,omitempty"`
}

// EmailResponse is the JSON response for email account endpoints.
type EmailResponse struct {
	Id          string           `json:"id"`
	Name        string           `json:"name"`
	Namespace   string           `json:"namespace"`
	Address     string           `json:"address"`
	Domain      string           `json:"domain"`
	QuotaMB     int64            `json:"quotaMB"`
	Forwarding  *EmailForwarding `json:"forwarding,omitempty"`
	MaildirPath string           `json:"maildirPath,omitempty"`
	Status      EmailStatusInfo  `json:"status,omitempty"`
	CreatedAt   string           `json:"createdAt,omitempty"`
}

// emailPattern validates email address format.
var emailPattern = regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}$`)

// emailToK8sName converts an email address to a valid Kubernetes resource name.
// e.g. "info@example.com" → "info-at-example-com"
func emailToK8sName(email string) string {
	name := strings.ReplaceAll(email, "@", "-at-")
	name = strings.ReplaceAll(name, ".", "-")
	name = strings.ToLower(name)
	return name
}

// hashEmailPassword generates a Dovecot-compatible SHA512 password hash.
// Format: {SHA512}base64(sha512(password)) — no salt, compatible with Dovecot passwd-file.
func hashEmailPassword(password string) (string, error) {
	h := sha512.New()
	h.Write([]byte(password))
	hash := base64.StdEncoding.EncodeToString(h.Sum(nil))
	return fmt.Sprintf("{SHA512}%s", hash), nil
}


// EmailHandler implements the email account management API endpoints.
type EmailHandler struct {
	dynClient dynamic.Interface
	clientset kubernetes.Interface
	kcAdmin   *keycloak.AdminClient
}

// NewEmailHandler creates a new EmailHandler.
func NewEmailHandler(dynClient dynamic.Interface, clientset kubernetes.Interface, kcAdmin *keycloak.AdminClient) *EmailHandler {
	return &EmailHandler{dynClient: dynClient, clientset: clientset, kcAdmin: kcAdmin}
}

// RegisterRoutes registers email account management routes on the given chi.Router.
func (h *EmailHandler) RegisterRoutes(r chi.Router) {
	r.Get("/", h.ListEmails)
	r.Post("/", h.CreateEmail)
	r.Route("/{id}", func(r chi.Router) {
		r.Get("/", h.GetEmail)
		r.Delete("/", h.DeleteEmail)
		r.Get("/forwarding", h.GetForwarding)
		r.Put("/forwarding", h.UpdateForwarding)
		r.Delete("/forwarding", h.DeleteForwarding)
		r.Put("/quota", h.UpdateQuota)
	})
}

// ListEmails handles GET /api/email-accounts.
// Admin sees all email accounts, user sees only their own (filtered by hosting.panel/user label).
func (h *EmailHandler) ListEmails(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		WriteUnauthorized(w, "Authentication required")
		return
	}

	listOpts := metav1.ListOptions{}
	if !middleware.HasRole(claims, "admin") {
		listOpts.LabelSelector = fmt.Sprintf("hosting.panel/user=%s", claims.Username)
	}

	list, err := h.dynClient.Resource(EmailAccountGVR).Namespace(hostingNamespace).List(r.Context(), listOpts)
	if err != nil {
		WriteInternalError(w, "Failed to list email accounts: "+err.Error())
		return
	}

	emails := make([]EmailResponse, 0, len(list.Items))
	for _, item := range list.Items {
		emails = append(emails, unstructuredToEmailResponse(&item))
	}
	writeJSON(w, http.StatusOK, emails)
}

// CreateEmail handles POST /api/email-accounts.
// Creates an EmailAccount CRD in the user's namespace after checking quota.
func (h *EmailHandler) CreateEmail(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		WriteUnauthorized(w, "Authentication required")
		return
	}

	var req CreateEmailRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteBadRequest(w, "Invalid request body", nil)
		return
	}

	req.Address = strings.TrimSpace(req.Address)
	if req.Address == "" {
		WriteBadRequest(w, "Email address is required", nil)
		return
	}

	if !emailPattern.MatchString(req.Address) {
		WriteBadRequest(w, "Invalid email address format", map[string]interface{}{
			"given": req.Address,
		})
		return
	}

	req.Domain = strings.TrimSpace(req.Domain)
	if req.Domain == "" {
		WriteBadRequest(w, "Domain is required", nil)
		return
	}

	// Apply default quota
	if req.QuotaMB <= 0 {
		req.QuotaMB = 1024
	}

	// Validate and hash password
	if req.Password == "" {
		WriteBadRequest(w, "Password is required", nil)
		return
	}
	if len(req.Password) < 8 {
		WriteBadRequest(w, "Password must be at least 8 characters", nil)
		return
	}

	// Create Keycloak user for this email address (Dovecot authenticates via Keycloak)
	var kcUserID string
	if h.kcAdmin != nil && h.kcAdmin.Configured() {
		kcUser := keycloak.User{
			Username:      req.Address,
			Email:         req.Address,
			FirstName:     strings.Split(req.Address, "@")[0],
			LastName:      "Mail",
			Enabled:       true,
			EmailVerified: true,
			Attributes:    map[string][]string{"email-account": {"true"}, "mail-domain": {req.Domain}},
		}
		var err error
		kcUserID, err = h.kcAdmin.CreateUser(r.Context(), kcUser)
		if err != nil {
			// 409 = already exists, try to continue
			if ae, ok := err.(*keycloak.AdminError); ok && ae.StatusCode == 409 {
				// Find existing user
				users, _ := h.kcAdmin.ListUsers(r.Context(), req.Address, 0, 1)
				if len(users) > 0 {
					kcUserID = users[0].ID
				}
			} else {
				WriteInternalError(w, "Failed to create Keycloak user for email: "+err.Error())
				return
			}
		}
		if kcUserID != "" {
			if err := h.kcAdmin.SetPassword(r.Context(), kcUserID, req.Password, false); err != nil {
				WriteInternalError(w, "Failed to set email password in Keycloak: "+err.Error())
				return
			}
		}
	}

	ns, err := resolveNamespace(r)
	if err != nil {
		WriteInternalError(w, "Failed to resolve namespace")
		return
	}

	// Verify the email domain exists and belongs to this user
	domainResName := sanitizeDomain(req.Domain)
	domainObj, err := h.dynClient.Resource(EmailDomainGVR).Namespace(hostingNamespace).Get(r.Context(), domainResName, metav1.GetOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			WriteBadRequest(w, "Email domain not found — register the domain first", map[string]string{"domain": req.Domain})
			return
		}
		WriteInternalError(w, "Failed to verify email domain: "+err.Error())
		return
	}
	if !middleware.IsOwnerByLabel(claims, domainObj) {
		WriteForbidden(w, "Email domain belongs to another user")
		return
	}

	// Check hosting plan quota
	if err := h.checkEmailQuota(r.Context(), ns); err != nil {
		if qErr, ok := err.(*emailQuotaError); ok {
			WriteQuotaExceeded(w, qErr.Error(), map[string]interface{}{
				"resource": "emailAccount",
				"current":  qErr.current,
				"limit":    qErr.limit,
			})
			return
		}
	}

	// Build unstructured EmailAccount CRD object (password is in Keycloak, not in CRD)
	obj := emailRequestToUnstructured(req, ns, claims.Username, "")

	created, err := h.dynClient.Resource(EmailAccountGVR).Namespace(ns).Create(r.Context(), obj, metav1.CreateOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "already exists") {
			WriteConflict(w, "Email account already exists", map[string]string{"address": req.Address})
			return
		}
		WriteInternalError(w, "Failed to create email account: "+err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, unstructuredToEmailResponse(created))
}

// GetEmail handles GET /api/email-accounts/{id}.
func (h *EmailHandler) GetEmail(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		WriteUnauthorized(w, "Authentication required")
		return
	}

	id := chi.URLParam(r, "id")
	ns, err := resolveNamespace(r)
	if err != nil {
		WriteInternalError(w, "Failed to resolve namespace")
		return
	}

	obj, err := h.dynClient.Resource(EmailAccountGVR).Namespace(ns).Get(r.Context(), id, metav1.GetOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			WriteNotFound(w, "Email account not found")
			return
		}
		WriteInternalError(w, "Failed to get email account: "+err.Error())
		return
	}

	if !middleware.IsOwnerByLabel(claims, obj) {
		WriteForbidden(w, "Access denied: email account belongs to another user")
		return
	}

	writeJSON(w, http.StatusOK, unstructuredToEmailResponse(obj))
}

// DeleteEmail handles DELETE /api/email-accounts/{id}.
func (h *EmailHandler) DeleteEmail(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		WriteUnauthorized(w, "Authentication required")
		return
	}

	id := chi.URLParam(r, "id")
	ns, err := resolveNamespace(r)
	if err != nil {
		WriteInternalError(w, "Failed to resolve namespace")
		return
	}

	// Verify existence and ownership
	existing, err := h.dynClient.Resource(EmailAccountGVR).Namespace(ns).Get(r.Context(), id, metav1.GetOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			WriteNotFound(w, "Email account not found")
			return
		}
		WriteInternalError(w, "Failed to get email account: "+err.Error())
		return
	}

	if !middleware.IsOwnerByLabel(claims, existing) {
		WriteForbidden(w, "Access denied: email account belongs to another user")
		return
	}

	if err := h.dynClient.Resource(EmailAccountGVR).Namespace(ns).Delete(r.Context(), id, metav1.DeleteOptions{}); err != nil {
		WriteInternalError(w, "Failed to delete email account: "+err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// --- Quota checking ---

// emailQuotaError represents an email account quota exceeded error with details.
type emailQuotaError struct {
	current int64
	limit   int64
}

func (e *emailQuotaError) Error() string {
	return fmt.Sprintf("Email account creation would exceed hosting plan limit (%d/%d)", e.current, e.limit)
}

// checkEmailQuota counts existing email accounts in the namespace and compares with the HostingPlan limit.
func (h *EmailHandler) checkEmailQuota(ctx context.Context, namespace string) error {
	list, err := h.dynClient.Resource(EmailAccountGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list email accounts for quota check: %w", err)
	}
	currentCount := int64(len(list.Items))

	limit, err := h.getEmailAccountLimit(ctx, namespace)
	if err != nil {
		return err
	}

	if limit > 0 && currentCount >= limit {
		return &emailQuotaError{current: currentCount, limit: limit}
	}

	return nil
}

// getEmailAccountLimit retrieves the email account limit from the user's HostingPlan.
// Returns 0 if no plan is assigned (unlimited).
func (h *EmailHandler) getEmailAccountLimit(ctx context.Context, namespace string) (int64, error) {
	plans, err := h.dynClient.Resource(HostingPlanGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return 0, nil
	}

	if len(plans.Items) == 0 {
		return 0, nil
	}

	// Use the first plan as default (in production, this would be per-user)
	plan := plans.Items[0]
	spec, _ := plan.Object["spec"].(map[string]interface{})
	if spec == nil {
		return 0, nil
	}

	limits, _ := spec["limits"].(map[string]interface{})
	if limits == nil {
		return 0, nil
	}

	emailLimit, _ := limits["emailAccounts"].(int64)
	if emailLimit == 0 {
		// Try float64 (JSON numbers are float64 by default)
		if f, ok := limits["emailAccounts"].(float64); ok {
			emailLimit = int64(f)
		}
	}

	return emailLimit, nil
}

// --- Conversion helpers ---

// emailRequestToUnstructured converts a CreateEmailRequest to an unstructured Kubernetes object.
func emailRequestToUnstructured(req CreateEmailRequest, namespace, username, passwordHash string) *unstructured.Unstructured {
	spec := map[string]interface{}{
		"address":      req.Address,
		"domain":       req.Domain,
		"quotaMB":      int64(req.QuotaMB),
		"passwordHash": passwordHash,
	}
	if req.Forwarding != nil {
		fwd := map[string]interface{}{
			"enabled":  req.Forwarding.Enabled,
			"keepCopy": req.Forwarding.KeepCopy,
		}
		if len(req.Forwarding.Addresses) > 0 {
			addrs := make([]interface{}, len(req.Forwarding.Addresses))
			for i, a := range req.Forwarding.Addresses {
				addrs[i] = a
			}
			fwd["addresses"] = addrs
		}
		spec["forwarding"] = fwd
	}

	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": CRDGroup + "/" + CRDVersion,
			"kind":       "EmailAccount",
			"metadata": map[string]interface{}{
				"name":      emailToK8sName(req.Address),
				"namespace": namespace,
				"labels": map[string]interface{}{
					"hosting.panel/user":         username,
					"hosting.panel/email-domain": req.Domain,
				},
			},
			"spec": spec,
		},
	}
}

// unstructuredToEmailResponse converts an unstructured EmailAccount CRD to an EmailResponse.
func unstructuredToEmailResponse(obj *unstructured.Unstructured) EmailResponse {
	resp := EmailResponse{
		Id:        obj.GetName(),
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
		CreatedAt: obj.GetCreationTimestamp().Format("2006-01-02T15:04:05Z"),
	}

	spec, _ := obj.Object["spec"].(map[string]interface{})
	if spec != nil {
		resp.Address, _ = spec["address"].(string)
		resp.Domain, _ = spec["domain"].(string)
		if q, ok := spec["quotaMB"].(int64); ok {
			resp.QuotaMB = q
		} else if q, ok := spec["quotaMB"].(float64); ok {
			resp.QuotaMB = int64(q)
		}
		if fwd, ok := spec["forwarding"].(map[string]interface{}); ok {
			resp.Forwarding = &EmailForwarding{}
			resp.Forwarding.Enabled, _ = fwd["enabled"].(bool)
			resp.Forwarding.KeepCopy, _ = fwd["keepCopy"].(bool)
			if addrs, ok := fwd["addresses"].([]interface{}); ok {
				for _, a := range addrs {
					if s, ok := a.(string); ok {
						resp.Forwarding.Addresses = append(resp.Forwarding.Addresses, s)
					}
				}
			}
		}
	}

	status, _ := obj.Object["status"].(map[string]interface{})
	if status != nil {
		resp.Status.Phase, _ = status["phase"].(string)
		resp.MaildirPath, _ = status["maildirPath"].(string)
		if u, ok := status["usedMB"].(int64); ok {
			resp.Status.UsedMB = u
		} else if u, ok := status["usedMB"].(float64); ok {
			resp.Status.UsedMB = int64(u)
		}
	}

	return resp
}

// GetForwarding handles GET /api/email-accounts/{id}/forwarding.
func (h *EmailHandler) GetForwarding(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		WriteUnauthorized(w, "Authentication required")
		return
	}

	id := chi.URLParam(r, "id")
	obj, err := h.dynClient.Resource(EmailAccountGVR).Namespace(hostingNamespace).Get(r.Context(), id, metav1.GetOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			WriteNotFound(w, "Email account not found")
			return
		}
		WriteInternalError(w, "Failed to get email account: "+err.Error())
		return
	}

	if !middleware.IsOwnerByLabel(claims, obj) {
		WriteForbidden(w, "Access denied")
		return
	}

	resp := unstructuredToEmailResponse(obj)
	if resp.Forwarding == nil {
		resp.Forwarding = &EmailForwarding{Enabled: false}
	}
	writeJSON(w, http.StatusOK, resp.Forwarding)
}

// UpdateForwarding handles PUT /api/email-accounts/{id}/forwarding.
func (h *EmailHandler) UpdateForwarding(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		WriteUnauthorized(w, "Authentication required")
		return
	}

	id := chi.URLParam(r, "id")
	var req UpdateEmailForwardingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteBadRequest(w, "Invalid request body", nil)
		return
	}

	obj, err := h.dynClient.Resource(EmailAccountGVR).Namespace(hostingNamespace).Get(r.Context(), id, metav1.GetOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			WriteNotFound(w, "Email account not found")
			return
		}
		WriteInternalError(w, "Failed to get email account: "+err.Error())
		return
	}

	if !middleware.IsOwnerByLabel(claims, obj) {
		WriteForbidden(w, "Access denied")
		return
	}

	spec, _ := obj.Object["spec"].(map[string]interface{})
	fwd := map[string]interface{}{
		"enabled":  req.Enabled,
		"keepCopy": req.KeepCopy,
	}
	if len(req.Addresses) > 0 {
		addrs := make([]interface{}, len(req.Addresses))
		for i, a := range req.Addresses {
			addrs[i] = a
		}
		fwd["addresses"] = addrs
	}
	spec["forwarding"] = fwd
	obj.Object["spec"] = spec

	updated, err := h.dynClient.Resource(EmailAccountGVR).Namespace(hostingNamespace).Update(r.Context(), obj, metav1.UpdateOptions{})
	if err != nil {
		WriteInternalError(w, "Failed to update email account: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, unstructuredToEmailResponse(updated))
}

// DeleteForwarding handles DELETE /api/email-accounts/{id}/forwarding.
func (h *EmailHandler) DeleteForwarding(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		WriteUnauthorized(w, "Authentication required")
		return
	}

	id := chi.URLParam(r, "id")
	obj, err := h.dynClient.Resource(EmailAccountGVR).Namespace(hostingNamespace).Get(r.Context(), id, metav1.GetOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			WriteNotFound(w, "Email account not found")
			return
		}
		WriteInternalError(w, "Failed to get email account: "+err.Error())
		return
	}

	if !middleware.IsOwnerByLabel(claims, obj) {
		WriteForbidden(w, "Access denied")
		return
	}

	spec, _ := obj.Object["spec"].(map[string]interface{})
	delete(spec, "forwarding")
	obj.Object["spec"] = spec

	_, err = h.dynClient.Resource(EmailAccountGVR).Namespace(hostingNamespace).Update(r.Context(), obj, metav1.UpdateOptions{})
	if err != nil {
		WriteInternalError(w, "Failed to update email account: "+err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// UpdateQuota handles PUT /api/email-accounts/{id}/quota.
func (h *EmailHandler) UpdateQuota(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		WriteUnauthorized(w, "Authentication required")
		return
	}

	id := chi.URLParam(r, "id")
	var req UpdateEmailQuotaRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteBadRequest(w, "Invalid request body", nil)
		return
	}
	if req.QuotaMB <= 0 {
		WriteBadRequest(w, "quotaMB must be positive", nil)
		return
	}

	obj, err := h.dynClient.Resource(EmailAccountGVR).Namespace(hostingNamespace).Get(r.Context(), id, metav1.GetOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			WriteNotFound(w, "Email account not found")
			return
		}
		WriteInternalError(w, "Failed to get email account: "+err.Error())
		return
	}

	if !middleware.IsOwnerByLabel(claims, obj) {
		WriteForbidden(w, "Access denied")
		return
	}

	spec, _ := obj.Object["spec"].(map[string]interface{})
	spec["quotaMB"] = int64(req.QuotaMB)
	obj.Object["spec"] = spec

	updated, err := h.dynClient.Resource(EmailAccountGVR).Namespace(hostingNamespace).Update(r.Context(), obj, metav1.UpdateOptions{})
	if err != nil {
		WriteInternalError(w, "Failed to update email account: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, unstructuredToEmailResponse(updated))
}
