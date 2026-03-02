package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/hosting-panel/panel-core/internal/middleware"
)

// --- Request/Response types ---

// CreateDatabaseRequest is the JSON body for POST /api/databases.
type CreateDatabaseRequest struct {
	Name      string `json:"name"`
	Charset   string `json:"charset,omitempty"`
	Collation string `json:"collation,omitempty"`
}

// UpdateDatabasePasswordRequest is the JSON body for PUT /api/databases/{id}.
type UpdateDatabasePasswordRequest struct {
	Password string `json:"password"`
}

// DatabaseStatusInfo represents the status section of a Database CRD.
type DatabaseStatusInfo struct {
	Phase        string `json:"phase,omitempty"`
	Host         string `json:"host,omitempty"`
	Port         int64  `json:"port,omitempty"`
	DatabaseName string `json:"databaseName,omitempty"`
	Username     string `json:"username,omitempty"`
	Password     string `json:"password,omitempty"`
}

// DatabaseResponse is the JSON response for database endpoints.
type DatabaseResponse struct {
	Id        string             `json:"id"`
	Name      string             `json:"name"`
	Namespace string             `json:"namespace"`
	DBName    string             `json:"dbName"`
	Charset   string             `json:"charset"`
	Collation string             `json:"collation"`
	Status    DatabaseStatusInfo `json:"status,omitempty"`
	CreatedAt string             `json:"createdAt,omitempty"`
}

// validDBNamePattern validates database names: 1-64 chars, starts with letter or underscore.
var validDBNamePattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]{0,63}$`)

// DatabaseHandler implements the database management API endpoints.
type DatabaseHandler struct {
	dynClient dynamic.Interface
	clientset kubernetes.Interface
}

// NewDatabaseHandler creates a new DatabaseHandler.
func NewDatabaseHandler(dynClient dynamic.Interface, clientset kubernetes.Interface) *DatabaseHandler {
	return &DatabaseHandler{dynClient: dynClient, clientset: clientset}
}

// RegisterRoutes registers database management routes on the given chi.Router.
func (h *DatabaseHandler) RegisterRoutes(r chi.Router) {
	r.Get("/", h.ListDatabases)
	r.Post("/", h.CreateDatabase)
	r.Route("/{id}", func(r chi.Router) {
		r.Get("/", h.GetDatabase)
		r.Put("/", h.UpdateDatabasePassword)
		r.Delete("/", h.DeleteDatabase)
	})
}

// ListDatabases handles GET /api/databases.
// Admin sees all databases, user sees only their own (filtered by hosting.panel/user label).
func (h *DatabaseHandler) ListDatabases(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		WriteUnauthorized(w, "Authentication required")
		return
	}

	listOpts := metav1.ListOptions{}
	if !middleware.HasRole(claims, "admin") {
		listOpts.LabelSelector = fmt.Sprintf("hosting.panel/user=%s", claims.Username)
	}

	list, err := h.dynClient.Resource(DatabaseGVR).Namespace(hostingNamespace).List(r.Context(), listOpts)
	if err != nil {
		WriteInternalError(w, "Failed to list databases: "+err.Error())
		return
	}

	databases := make([]DatabaseResponse, 0, len(list.Items))
	for _, item := range list.Items {
		databases = append(databases, unstructuredToDatabaseResponse(&item))
	}
	writeJSON(w, http.StatusOK, databases)
}

// CreateDatabase handles POST /api/databases.
// Creates a Database CRD in the user's namespace after checking quota.
func (h *DatabaseHandler) CreateDatabase(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		WriteUnauthorized(w, "Authentication required")
		return
	}

	var req CreateDatabaseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteBadRequest(w, "Invalid request body", nil)
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		WriteBadRequest(w, "Database name is required", nil)
		return
	}

	if !validDBNamePattern.MatchString(req.Name) {
		WriteBadRequest(w, "Invalid database name: must be 1-64 characters, start with a letter or underscore, and contain only letters, digits, and underscores", nil)
		return
	}

	// Prefix database name with username to avoid collisions
	req.Name = claims.Username + "_" + req.Name

	// Apply defaults
	if req.Charset == "" {
		req.Charset = "utf8mb4"
	}
	if req.Collation == "" {
		req.Collation = "utf8mb4_unicode_ci"
	}

	ns, err := resolveNamespace(r)
	if err != nil {
		WriteInternalError(w, "Failed to resolve namespace")
		return
	}

	// Check hosting plan quota
	if err := h.checkDatabaseQuota(r.Context(), ns); err != nil {
		if qErr, ok := err.(*databaseQuotaError); ok {
			WriteQuotaExceeded(w, qErr.Error(), map[string]interface{}{
				"resource": "database",
				"current":  qErr.current,
				"limit":    qErr.limit,
			})
			return
		}
		// Non-quota errors (e.g., plan not found) are not blocking — allow creation
	}

	// Build unstructured Database CRD object
	obj := databaseRequestToUnstructured(req, ns, claims.Username)

	created, err := h.dynClient.Resource(DatabaseGVR).Namespace(ns).Create(r.Context(), obj, metav1.CreateOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "already exists") {
			WriteConflict(w, "Database already exists", map[string]string{"name": req.Name})
			return
		}
		WriteInternalError(w, "Failed to create database: "+err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, unstructuredToDatabaseResponse(created))
}

// GetDatabase handles GET /api/databases/{id}.
func (h *DatabaseHandler) GetDatabase(w http.ResponseWriter, r *http.Request) {
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

	obj, err := h.dynClient.Resource(DatabaseGVR).Namespace(ns).Get(r.Context(), id, metav1.GetOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			WriteNotFound(w, "Database not found")
			return
		}
		WriteInternalError(w, "Failed to get database: "+err.Error())
		return
	}

	// Ownership check for non-admin users (label-based)
	if !middleware.IsOwnerByLabel(claims, obj) {
		WriteForbidden(w, "Access denied: database belongs to another user")
		return
	}

	writeJSON(w, http.StatusOK, unstructuredToDatabaseResponse(obj))
}

// DeleteDatabase handles DELETE /api/databases/{id}.
func (h *DatabaseHandler) DeleteDatabase(w http.ResponseWriter, r *http.Request) {
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
	existing, err := h.dynClient.Resource(DatabaseGVR).Namespace(ns).Get(r.Context(), id, metav1.GetOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			WriteNotFound(w, "Database not found")
			return
		}
		WriteInternalError(w, "Failed to get database: "+err.Error())
		return
	}

	if !middleware.IsOwnerByLabel(claims, existing) {
		WriteForbidden(w, "Access denied: database belongs to another user")
		return
	}

	if err := h.dynClient.Resource(DatabaseGVR).Namespace(ns).Delete(r.Context(), id, metav1.DeleteOptions{}); err != nil {
		WriteInternalError(w, "Failed to delete database: "+err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// UpdateDatabasePassword handles PUT /api/databases/{id}.
// Updates the password for the database user by writing it to the CRD status.
// The operator will detect the change and update MariaDB accordingly.
func (h *DatabaseHandler) UpdateDatabasePassword(w http.ResponseWriter, r *http.Request) {
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

	var req UpdateDatabasePasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteBadRequest(w, "Invalid request body", nil)
		return
	}

	req.Password = strings.TrimSpace(req.Password)
	if req.Password == "" {
		WriteBadRequest(w, "Password is required", nil)
		return
	}
	if len(req.Password) < 8 {
		WriteBadRequest(w, "Password must be at least 8 characters", nil)
		return
	}

	existing, err := h.dynClient.Resource(DatabaseGVR).Namespace(ns).Get(r.Context(), id, metav1.GetOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			WriteNotFound(w, "Database not found")
			return
		}
		WriteInternalError(w, "Failed to get database: "+err.Error())
		return
	}

	if !middleware.IsOwnerByLabel(claims, existing) {
		WriteForbidden(w, "Access denied: database belongs to another user")
		return
	}

	// Update the password in the status subresource — the operator will pick it up
	status, _ := existing.Object["status"].(map[string]interface{})
	if status == nil {
		status = make(map[string]interface{})
	}
	status["password"] = req.Password
	// Set a passwordChanged annotation to trigger reconciliation
	annotations := existing.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	annotations["hosting.panel/password-changed"] = fmt.Sprintf("%d", time.Now().Unix())
	existing.SetAnnotations(annotations)

	// Update the main resource (annotations trigger reconcile)
	updated, err := h.dynClient.Resource(DatabaseGVR).Namespace(ns).Update(r.Context(), existing, metav1.UpdateOptions{})
	if err != nil {
		WriteInternalError(w, "Failed to update database: "+err.Error())
		return
	}

	// Also update status subresource — use the freshly returned object to avoid resourceVersion conflict
	updated.Object["status"] = status
	if _, err := h.dynClient.Resource(DatabaseGVR).Namespace(ns).UpdateStatus(r.Context(), updated, metav1.UpdateOptions{}); err != nil {
		WriteInternalError(w, "Failed to update database status: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, unstructuredToDatabaseResponse(updated))
}

// --- Quota checking ---

// databaseQuotaError represents a database quota exceeded error with details.
type databaseQuotaError struct {
	current int64
	limit   int64
}

func (e *databaseQuotaError) Error() string {
	return fmt.Sprintf("Database creation would exceed hosting plan limit (%d/%d)", e.current, e.limit)
}

// checkDatabaseQuota counts existing databases in the namespace and compares with the HostingPlan limit.
func (h *DatabaseHandler) checkDatabaseQuota(ctx context.Context, namespace string) error {
	list, err := h.dynClient.Resource(DatabaseGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list databases for quota check: %w", err)
	}
	currentCount := int64(len(list.Items))

	limit, err := h.getDatabaseLimit(ctx, namespace)
	if err != nil {
		return err
	}

	if limit > 0 && currentCount >= limit {
		return &databaseQuotaError{current: currentCount, limit: limit}
	}

	return nil
}

// getDatabaseLimit retrieves the database limit from the user's HostingPlan.
// Returns 0 if no plan is assigned (unlimited).
func (h *DatabaseHandler) getDatabaseLimit(ctx context.Context, namespace string) (int64, error) {
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

	dbLimit, _ := limits["databases"].(int64)
	if dbLimit == 0 {
		// Try float64 (JSON numbers are float64 by default)
		if f, ok := limits["databases"].(float64); ok {
			dbLimit = int64(f)
		}
	}

	return dbLimit, nil
}

// --- Conversion helpers ---

// databaseRequestToUnstructured converts a CreateDatabaseRequest to an unstructured Kubernetes object.
func databaseRequestToUnstructured(req CreateDatabaseRequest, namespace, username string) *unstructured.Unstructured {
	// K8s metadata.name must be RFC 1123 compliant (no underscores).
	// Replace underscores with dashes for the CRD name, keep original in spec.name.
	k8sName := strings.ReplaceAll(strings.ToLower(req.Name), "_", "-")
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": CRDGroup + "/" + CRDVersion,
			"kind":       "Database",
			"metadata": map[string]interface{}{
				"name":      k8sName,
				"namespace": namespace,
				"labels": map[string]interface{}{
					"hosting.panel/user": username,
				},
			},
			"spec": map[string]interface{}{
				"name":      req.Name,
				"charset":   req.Charset,
				"collation": req.Collation,
			},
		},
	}
}

// unstructuredToDatabaseResponse converts an unstructured Database CRD to a DatabaseResponse.
func unstructuredToDatabaseResponse(obj *unstructured.Unstructured) DatabaseResponse {
	resp := DatabaseResponse{
		Id:        obj.GetName(),
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
		CreatedAt: obj.GetCreationTimestamp().Format("2006-01-02T15:04:05Z"),
	}

	spec, _ := obj.Object["spec"].(map[string]interface{})
	if spec != nil {
		resp.DBName, _ = spec["name"].(string)
		resp.Charset, _ = spec["charset"].(string)
		resp.Collation, _ = spec["collation"].(string)
	}

	status, _ := obj.Object["status"].(map[string]interface{})
	if status != nil {
		resp.Status.Phase, _ = status["phase"].(string)
		resp.Status.Host, _ = status["host"].(string)
		resp.Status.DatabaseName, _ = status["databaseName"].(string)
		resp.Status.Username, _ = status["username"].(string)
		resp.Status.Password, _ = status["password"].(string)
		if p, ok := status["port"].(int64); ok {
			resp.Status.Port = p
		} else if p, ok := status["port"].(float64); ok {
			resp.Status.Port = int64(p)
		}
	}

	return resp
}
