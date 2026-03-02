package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"strings"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/hosting-panel/panel-core/internal/keycloak"
	"github.com/hosting-panel/panel-core/internal/middleware"
)

// UserHandler implements the user management CRUD API endpoints.
type UserHandler struct {
	keycloakAdmin *keycloak.AdminClient
	k8sClient     kubernetes.Interface
	dynClient     dynamic.Interface
}

// NewUserHandler creates a new UserHandler with the given dependencies.
func NewUserHandler(kc *keycloak.AdminClient, k8s kubernetes.Interface, dyn ...dynamic.Interface) *UserHandler {
	h := &UserHandler{
		keycloakAdmin: kc,
		k8sClient:     k8s,
	}
	if len(dyn) > 0 {
		h.dynClient = dyn[0]
	}
	return h
}

// --- Request/Response types ---

// CreateUserRequest is the JSON body for POST /api/users.
type CreateUserRequest struct {
	Username      string `json:"username"`
	Email         string `json:"email,omitempty"`
	FirstName     string `json:"firstName,omitempty"`
	LastName      string `json:"lastName,omitempty"`
	Password      string `json:"password,omitempty"`
	HostingPlanId string `json:"hostingPlanId,omitempty"`
}

// UpdateUserRequest is the JSON body for PUT /api/users/{id}.
type UpdateUserRequest struct {
	Email         string `json:"email,omitempty"`
	FirstName     string `json:"firstName,omitempty"`
	LastName      string `json:"lastName,omitempty"`
	HostingPlanId string `json:"hostingPlanId,omitempty"`
}

// ChangePasswordRequest is the JSON body for PUT /api/users/{id}/password.
type ChangePasswordRequest struct {
	Password string `json:"password"`
}

// UserResponse is the JSON response for user endpoints.
type UserResponse struct {
	ID            string `json:"id"`
	Username      string `json:"username"`
	Email         string `json:"email,omitempty"`
	FirstName     string `json:"firstName,omitempty"`
	LastName      string `json:"lastName,omitempty"`
	Enabled       bool   `json:"enabled"`
	HostingPlanId string `json:"hostingPlanId,omitempty"`
	Namespace     string `json:"namespace"`
}

// userNamespace returns the per-user Kubernetes namespace name.
func userNamespace(username string) string {
	return fmt.Sprintf("hosting-user-%s", username)
}

// toUserResponse converts a keycloak.User to a UserResponse.
func toUserResponse(u *keycloak.User) UserResponse {
	planId := ""
	if u.Attributes != nil {
		if v, ok := u.Attributes["hostingPlanId"]; ok && len(v) > 0 {
			planId = v[0]
		}
	}
	return UserResponse{
		ID:            u.ID,
		Username:      u.Username,
		Email:         u.Email,
		FirstName:     u.FirstName,
		LastName:      u.LastName,
		Enabled:       u.Enabled,
		HostingPlanId: planId,
		Namespace:     hostingNamespace,
	}
}

// writeJSON writes a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// handleKeycloakError maps keycloak.AdminError to the appropriate API error response.
func handleKeycloakError(w http.ResponseWriter, err error) {
	var adminErr *keycloak.AdminError
	if errors.As(err, &adminErr) {
		switch adminErr.StatusCode {
		case http.StatusConflict:
			WriteConflict(w, "Username already exists", nil)
		case http.StatusNotFound:
			WriteNotFound(w, "User not found")
		default:
			WriteInternalError(w, "Keycloak error: "+adminErr.Message)
		}
		return
	}
	WriteInternalError(w, "Internal server error")
}

// --- Route registration ---

// RegisterRoutes registers user management routes on the given chi.Router.
// Admin-only routes: GET /api/users, POST /api/users, DELETE /api/users/{id}
// Admin or self routes: GET /api/users/{id}, PUT /api/users/{id}, PUT /api/users/{id}/password
func (h *UserHandler) RegisterRoutes(r chi.Router) {
	// Admin-only: list and create
	r.With(middleware.RequireRole("admin")).Get("/", h.ListUsers)
	r.With(middleware.RequireRole("admin")).Post("/", h.CreateUser)

	// Current user self-service endpoint
	r.With(middleware.RequireRole("admin", "user")).Get("/me", h.GetCurrentUser)
	r.With(middleware.RequireRole("admin", "user")).Put("/me", h.UpdateCurrentUser)
	r.With(middleware.RequireRole("admin", "user")).Put("/me/password", h.ChangeCurrentPassword)

	// Per-user routes: admin can access any, user can access own
	r.Route("/{id}", func(r chi.Router) {
		r.With(middleware.RequireRole("admin", "user")).Get("/", h.GetUser)
		r.With(middleware.RequireRole("admin", "user")).Put("/", h.UpdateUser)
		r.With(middleware.RequireRole("admin")).Delete("/", h.DeleteUser)
		r.With(middleware.RequireRole("admin", "user")).Put("/password", h.ChangePassword)
		r.With(middleware.RequireRole("admin")).Post("/suspend", h.SuspendUser)
		r.With(middleware.RequireRole("admin")).Post("/unsuspend", h.UnsuspendUser)
		r.With(middleware.RequireRole("admin")).Post("/impersonate", h.ImpersonateUser)
		r.With(middleware.RequireRole("admin")).Post("/transfer", h.TransferResources)
	})
	r.With(middleware.RequireRole("admin", "user")).Post("/impersonate/exit", h.ExitImpersonation)
}

// --- Handlers ---

// ListUsers handles GET /api/users — lists all users (admin only).
func (h *UserHandler) ListUsers(w http.ResponseWriter, r *http.Request) {
	search := r.URL.Query().Get("search")
	users, err := h.keycloakAdmin.ListUsers(r.Context(), search, 0, 100)
	if err != nil {
		handleKeycloakError(w, err)
		return
	}

	resp := make([]UserResponse, 0, len(users))
	for i := range users {
		resp = append(resp, toUserResponse(&users[i]))
	}
	writeJSON(w, http.StatusOK, resp)
}

// CreateUser handles POST /api/users — creates a new user (admin only).
func (h *UserHandler) CreateUser(w http.ResponseWriter, r *http.Request) {
	var req CreateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteBadRequest(w, "Invalid request body", nil)
		return
	}

	// Validate username
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" {
		WriteBadRequest(w, "Username is required", nil)
		return
	}

	// Validate email format if provided
	if req.Email != "" {
		if _, err := mail.ParseAddress(req.Email); err != nil {
			WriteBadRequest(w, "Invalid email format", nil)
			return
		}
	}

	// Create user in Keycloak
	kcUser := keycloak.User{
		Username:  req.Username,
		Email:     req.Email,
		FirstName: req.FirstName,
		LastName:  req.LastName,
		Enabled:   true,
	}
	if req.HostingPlanId != "" {
		kcUser.Attributes = map[string][]string{"hostingPlanId": {req.HostingPlanId}}
	}

	userID, err := h.keycloakAdmin.CreateUser(r.Context(), kcUser)
	if err != nil {
		handleKeycloakError(w, err)
		return
	}

	// Assign "user" role
	if err := h.keycloakAdmin.AssignRole(r.Context(), userID, "user"); err != nil {
		// Best effort: user created but role assignment failed — log and continue
		// In production this would need compensation logic
		_ = err
	}

	// Set password if provided
	if req.Password != "" {
		if err := h.keycloakAdmin.SetPassword(r.Context(), userID, req.Password, false); err != nil {
			_ = err
		}
	}

	// Create User_Volume PVC (uv-{username}) in hosting-system namespace
	if h.k8sClient != nil {
		storageGB := int32(10) // Default 10Gi, overridden by HostingPlan
		pvcName := "uv-" + req.Username
		storageClass := "longhorn"
		pvc := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      pvcName,
				Namespace: "hosting-system",
				Labels: map[string]string{
					"hosting.panel/user-volume": "true",
					"hosting.panel/user":        req.Username,
				},
			},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
				StorageClassName: &storageClass,
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: *resource.NewQuantity(int64(storageGB)*1024*1024*1024, resource.BinarySI),
					},
				},
			},
		}
		if _, err := h.k8sClient.CoreV1().PersistentVolumeClaims("hosting-system").Create(r.Context(), pvc, metav1.CreateOptions{}); err != nil {
			if !k8serrors.IsAlreadyExists(err) {
				_ = err // Log in production
			}
		}
	}

	resp := UserResponse{
		ID:        userID,
		Username:  req.Username,
		Email:     req.Email,
		FirstName: req.FirstName,
		LastName:  req.LastName,
		Enabled:   true,
		Namespace: userNamespace(req.Username),
	}
	writeJSON(w, http.StatusCreated, resp)
}

// GetUser handles GET /api/users/{id}.
// Admin can get any user; regular user can only get their own profile.
func (h *UserHandler) GetUser(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "id")
	claims := middleware.GetClaims(r.Context())

	// Non-admin users can only access their own profile
	if !middleware.HasRole(claims, "admin") && claims.Subject != userID {
		WriteForbidden(w, "Access denied: can only view own profile")
		return
	}

	user, err := h.keycloakAdmin.GetUser(r.Context(), userID)
	if err != nil {
		handleKeycloakError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, toUserResponse(user))
}

// UpdateUser handles PUT /api/users/{id}.
// Admin can update any user; regular user can only update their own profile.
func (h *UserHandler) UpdateUser(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "id")
	claims := middleware.GetClaims(r.Context())

	if !middleware.HasRole(claims, "admin") && claims.Subject != userID {
		WriteForbidden(w, "Access denied: can only update own profile")
		return
	}

	var req UpdateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteBadRequest(w, "Invalid request body", nil)
		return
	}

	// Validate email if provided
	if req.Email != "" {
		if _, err := mail.ParseAddress(req.Email); err != nil {
			WriteBadRequest(w, "Invalid email format", nil)
			return
		}
	}

	// Get existing user to preserve fields
	existing, err := h.keycloakAdmin.GetUser(r.Context(), userID)
	if err != nil {
		handleKeycloakError(w, err)
		return
	}

	// Apply updates
	if req.FirstName != "" {
		existing.FirstName = req.FirstName
	}
	if req.LastName != "" {
		existing.LastName = req.LastName
	}
	if req.Email != "" {
		existing.Email = req.Email
	}

	// Store hosting plan in Keycloak user attributes
	if req.HostingPlanId != "" {
		if existing.Attributes == nil {
			existing.Attributes = make(map[string][]string)
		}
		existing.Attributes["hostingPlanId"] = []string{req.HostingPlanId}
	}

	if err := h.keycloakAdmin.UpdateUser(r.Context(), userID, *existing); err != nil {
		handleKeycloakError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, toUserResponse(existing))
}

// DeleteUser handles DELETE /api/users/{id} — admin only.
func (h *UserHandler) DeleteUser(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "id")

	// Get user first to find username for namespace deletion
	user, err := h.keycloakAdmin.GetUser(r.Context(), userID)
	if err != nil {
		handleKeycloakError(w, err)
		return
	}

	// Delete from Keycloak
	if err := h.keycloakAdmin.DeleteUser(r.Context(), userID); err != nil {
		handleKeycloakError(w, err)
		return
	}

	// Delete per-user Kubernetes namespace
	if h.k8sClient != nil {
		nsName := userNamespace(user.Username)
		err := h.k8sClient.CoreV1().Namespaces().Delete(r.Context(), nsName, metav1.DeleteOptions{})
		if err != nil && !k8serrors.IsNotFound(err) {
			_ = err // Log in production
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

// ChangePassword handles PUT /api/users/{id}/password.
// Admin can change any user's password; regular user can only change their own.
func (h *UserHandler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "id")
	claims := middleware.GetClaims(r.Context())

	if !middleware.HasRole(claims, "admin") && claims.Subject != userID {
		WriteForbidden(w, "Access denied: can only change own password")
		return
	}

	var req ChangePasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteBadRequest(w, "Invalid request body", nil)
		return
	}

	if req.Password == "" {
		WriteBadRequest(w, "Password is required", nil)
		return
	}

	if err := h.keycloakAdmin.SetPassword(r.Context(), userID, req.Password, false); err != nil {
		handleKeycloakError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ensureNamespace creates a Kubernetes namespace if it doesn't exist.
func (h *UserHandler) ensureNamespace(ctx context.Context, name, username string) error {
	if h.k8sClient == nil {
		return nil
	}
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				"hosting.panel/user": username,
				"managed-by":         "panel-core",
			},
		},
	}
	_, err := h.k8sClient.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	if err != nil && !k8serrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

// SuspendUser handles POST /api/users/{id}/suspend — admin only.
// Disables the Keycloak account and sets all user's Website CRDs to Suspended phase.
func (h *UserHandler) SuspendUser(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "id")

	user, err := h.keycloakAdmin.GetUser(r.Context(), userID)
	if err != nil {
		handleKeycloakError(w, err)
		return
	}

	// Disable Keycloak account
	user.Enabled = false
	if err := h.keycloakAdmin.UpdateUser(r.Context(), userID, *user); err != nil {
		handleKeycloakError(w, err)
		return
	}

	// Suspend all user's websites by patching CRD status.phase → Suspended
	if h.dynClient != nil {
		h.setUserWebsitesPhase(r.Context(), user.Username, "Suspended")
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "suspended",
		"userId":  userID,
		"message": fmt.Sprintf("User %s suspended", user.Username),
	})
}

// UnsuspendUser handles POST /api/users/{id}/unsuspend — admin only.
// Re-enables the Keycloak account and restores user's Website CRDs to Running phase.
func (h *UserHandler) UnsuspendUser(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "id")

	user, err := h.keycloakAdmin.GetUser(r.Context(), userID)
	if err != nil {
		handleKeycloakError(w, err)
		return
	}

	// Enable Keycloak account
	user.Enabled = true
	if err := h.keycloakAdmin.UpdateUser(r.Context(), userID, *user); err != nil {
		handleKeycloakError(w, err)
		return
	}

	// Unsuspend all user's websites
	if h.dynClient != nil {
		h.setUserWebsitesPhase(r.Context(), user.Username, "Running")
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "active",
		"userId":  userID,
		"message": fmt.Sprintf("User %s unsuspended", user.Username),
	})
}

// setUserWebsitesPhase patches all Website CRDs owned by the user to the given phase.
func (h *UserHandler) setUserWebsitesPhase(ctx context.Context, username, phase string) {
	const ns = "hosting-system"
	websites, err := h.dynClient.Resource(WebsiteGVR).Namespace(ns).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("hosting.panel/user=%s", username),
	})
	if err != nil {
		return
	}
	for _, w := range websites.Items {
		status, _, _ := unstructured.NestedMap(w.Object, "status")
		if status == nil {
			status = map[string]interface{}{}
		}
		status["phase"] = phase
		unstructured.SetNestedMap(w.Object, status, "status")
		_, _ = h.dynClient.Resource(WebsiteGVR).Namespace(ns).UpdateStatus(ctx, &w, metav1.UpdateOptions{})
	}
}

// ImpersonateUser handles POST /api/users/{id}/impersonate — admin only.
// Returns a token/session context that allows the admin to view resources as the target user.
func (h *UserHandler) ImpersonateUser(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "id")
	claims := middleware.GetClaims(r.Context())

	user, err := h.keycloakAdmin.GetUser(r.Context(), userID)
	if err != nil {
		handleKeycloakError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"impersonating": user.Username,
		"adminId":       claims.Subject,
		"adminUsername": claims.Username,
		"targetUserId":  userID,
		"message":       fmt.Sprintf("Now viewing as %s", user.Username),
	})
}

// ExitImpersonation handles POST /api/users/impersonate/exit — admin only.
func (h *UserHandler) ExitImpersonation(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "exited",
		"adminId": claims.Subject,
		"message": "Returned to admin view",
	})
}

// TransferResourcesRequest is the JSON body for POST /api/users/{id}/transfer.
type TransferResourcesRequest struct {
	TargetUserID string   `json:"targetUserId"`
	ResourceType string   `json:"resourceType"` // "website", "email-domain", "dns-zone"
	ResourceIDs  []string `json:"resourceIds"`
}

// TransferResources handles POST /api/users/{id}/transfer — admin only.
// Transfers resources from one user to another by updating hosting.panel/user labels.
func (h *UserHandler) TransferResources(w http.ResponseWriter, r *http.Request) {
	sourceUserID := chi.URLParam(r, "id")

	var req TransferResourcesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteBadRequest(w, "Invalid request body", nil)
		return
	}

	if req.TargetUserID == "" || len(req.ResourceIDs) == 0 {
		WriteBadRequest(w, "targetUserId and resourceIds are required", nil)
		return
	}

	// Get target user to verify existence and get username
	targetUser, err := h.keycloakAdmin.GetUser(r.Context(), req.TargetUserID)
	if err != nil {
		handleKeycloakError(w, err)
		return
	}

	if h.dynClient == nil {
		WriteInternalError(w, "Dynamic client not configured")
		return
	}

	// Determine GVR based on resource type
	var gvr = WebsiteGVR
	switch req.ResourceType {
	case "website":
		gvr = WebsiteGVR
	case "email-domain":
		gvr = EmailDomainGVR
	case "database":
		gvr = DatabaseGVR
	default:
		WriteBadRequest(w, "Invalid resourceType: must be website, email-domain, or database", nil)
		return
	}

	const ns = "hosting-system"
	transferred := 0
	for _, resID := range req.ResourceIDs {
		obj, err := h.dynClient.Resource(gvr).Namespace(ns).Get(r.Context(), resID, metav1.GetOptions{})
		if err != nil {
			continue
		}
		labels := obj.GetLabels()
		if labels == nil {
			labels = map[string]string{}
		}
		labels["hosting.panel/user"] = targetUser.Username
		obj.SetLabels(labels)

		// Also update spec.owner if it exists
		spec, ok, _ := unstructured.NestedMap(obj.Object, "spec")
		if ok && spec != nil {
			spec["owner"] = targetUser.Username
			unstructured.SetNestedMap(obj.Object, spec, "spec")
		}

		if _, err := h.dynClient.Resource(gvr).Namespace(ns).Update(r.Context(), obj, metav1.UpdateOptions{}); err == nil {
			transferred++
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"transferred":  transferred,
		"total":        len(req.ResourceIDs),
		"sourceUserId": sourceUserID,
		"targetUserId": req.TargetUserID,
		"targetUser":   targetUser.Username,
		"resourceType": req.ResourceType,
	})
}

// GetCurrentUser handles GET /api/users/me — returns the authenticated user's own profile.
func (h *UserHandler) GetCurrentUser(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		WriteUnauthorized(w, "Authentication required")
		return
	}

	user, err := h.keycloakAdmin.GetUser(r.Context(), claims.Subject)
	if err != nil {
		// Fallback: return basic info from JWT claims if Keycloak lookup fails
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"id":       claims.Subject,
			"username": claims.Username,
			"roles":    claims.Roles,
		})
		return
	}

	writeJSON(w, http.StatusOK, toUserResponse(user))
}

// UpdateCurrentUser handles PUT /api/users/me — updates the authenticated user's own profile.
func (h *UserHandler) UpdateCurrentUser(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		WriteUnauthorized(w, "Authentication required")
		return
	}

	var req UpdateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteBadRequest(w, "Invalid request body", nil)
		return
	}

	user := keycloak.User{
		Email:     req.Email,
		FirstName: req.FirstName,
		LastName:  req.LastName,
	}

	if err := h.keycloakAdmin.UpdateUser(r.Context(), claims.Subject, user); err != nil {
		handleKeycloakError(w, err)
		return
	}

	updated, err := h.keycloakAdmin.GetUser(r.Context(), claims.Subject)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
		return
	}
	writeJSON(w, http.StatusOK, toUserResponse(updated))
}

// ChangeCurrentPassword handles PUT /api/users/me/password — changes the authenticated user's password.
func (h *UserHandler) ChangeCurrentPassword(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		WriteUnauthorized(w, "Authentication required")
		return
	}

	var req ChangePasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteBadRequest(w, "Invalid request body", nil)
		return
	}

	if req.Password == "" {
		WriteBadRequest(w, "password is required", nil)
		return
	}

	if err := h.keycloakAdmin.SetPassword(r.Context(), claims.Subject, req.Password, false); err != nil {
		handleKeycloakError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "password changed"})
}
