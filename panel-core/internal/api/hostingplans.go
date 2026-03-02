package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

// --- Request/Response types ---

// HostingPlanLimitsRequest represents the limits section of a hosting plan.
type HostingPlanLimitsRequest struct {
	Websites      int32 `json:"websites"`
	Databases     int32 `json:"databases"`
	EmailAccounts int32 `json:"emailAccounts"`
	StorageGB     int32 `json:"storageGB"`
	CPUMillicores int32 `json:"cpuMillicores"`
	MemoryMB      int32 `json:"memoryMB"`
}

// CreateHostingPlanRequest is the JSON body for POST /api/hosting-plans.
type CreateHostingPlanRequest struct {
	Name        string                   `json:"name"`
	DisplayName string                   `json:"displayName"`
	Limits      HostingPlanLimitsRequest `json:"limits"`
}

// UpdateHostingPlanRequest is the JSON body for PUT /api/hosting-plans/{id}.
type UpdateHostingPlanRequest struct {
	DisplayName string                    `json:"displayName,omitempty"`
	Limits      *HostingPlanLimitsRequest `json:"limits,omitempty"`
}

// AssignPlanRequest is the JSON body for POST /api/hosting-plans/{id}/assign.
type AssignPlanRequest struct {
	Username string `json:"username"`
}

// HostingPlanLimitsResponse represents the limits in a response.
type HostingPlanLimitsResponse struct {
	Websites      int64 `json:"websites"`
	Databases     int64 `json:"databases"`
	EmailAccounts int64 `json:"emailAccounts"`
	StorageGB     int64 `json:"storageGB"`
	CPUMillicores int64 `json:"cpuMillicores"`
	MemoryMB      int64 `json:"memoryMB"`
}

// HostingPlanResponse is the JSON response for hosting plan endpoints.
type HostingPlanResponse struct {
	Name        string                    `json:"name"`
	DisplayName string                    `json:"displayName"`
	Limits      HostingPlanLimitsResponse `json:"limits"`
	CreatedAt   string                    `json:"createdAt,omitempty"`
}

// HostingPlanHandler implements the hosting plan management API endpoints.
type HostingPlanHandler struct {
	dynClient dynamic.Interface
	clientset kubernetes.Interface
}

// NewHostingPlanHandler creates a new HostingPlanHandler.
func NewHostingPlanHandler(dynClient dynamic.Interface, clientset kubernetes.Interface) *HostingPlanHandler {
	return &HostingPlanHandler{dynClient: dynClient, clientset: clientset}
}

// RegisterRoutes registers hosting plan management routes on the given chi.Router.
func (h *HostingPlanHandler) RegisterRoutes(r chi.Router) {
	r.Get("/", h.ListHostingPlans)
	r.Post("/", h.CreateHostingPlan)
	r.Route("/{id}", func(r chi.Router) {
		r.Get("/", h.GetHostingPlan)
		r.Put("/", h.UpdateHostingPlan)
		r.Delete("/", h.DeleteHostingPlan)
		r.Post("/assign", h.AssignPlan)
	})
}

// ListHostingPlans handles GET /api/hosting-plans.
// Lists all hosting plans (cluster-scoped). Admin only (enforced by router middleware).
func (h *HostingPlanHandler) ListHostingPlans(w http.ResponseWriter, r *http.Request) {
	list, err := h.dynClient.Resource(HostingPlanGVR).List(r.Context(), metav1.ListOptions{})
	if err != nil {
		WriteInternalError(w, "Failed to list hosting plans: "+err.Error())
		return
	}

	plans := make([]HostingPlanResponse, 0, len(list.Items))
	for _, item := range list.Items {
		plans = append(plans, unstructuredToHostingPlanResponse(&item))
	}
	writeJSON(w, http.StatusOK, plans)
}

// CreateHostingPlan handles POST /api/hosting-plans.
func (h *HostingPlanHandler) CreateHostingPlan(w http.ResponseWriter, r *http.Request) {
	var req CreateHostingPlanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteBadRequest(w, "Invalid request body", nil)
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.DisplayName = strings.TrimSpace(req.DisplayName)
	if req.DisplayName == "" {
		WriteBadRequest(w, "Display name is required", nil)
		return
	}
	// Auto-generate slug name from display name if not provided
	if req.Name == "" {
		req.Name = strings.ToLower(regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(strings.ToLower(req.DisplayName), "-"))
		req.Name = strings.Trim(req.Name, "-")
	}
	if req.Name == "" {
		WriteBadRequest(w, "Plan name is required", nil)
		return
	}

	if req.Limits.Websites < 0 || req.Limits.Databases < 0 || req.Limits.EmailAccounts < 0 {
		WriteBadRequest(w, "Limits must be non-negative", nil)
		return
	}

	obj := hostingPlanRequestToUnstructured(req)

	created, err := h.dynClient.Resource(HostingPlanGVR).Create(r.Context(), obj, metav1.CreateOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "already exists") {
			WriteConflict(w, "Hosting plan already exists", map[string]string{"name": req.Name})
			return
		}
		WriteInternalError(w, "Failed to create hosting plan: "+err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, unstructuredToHostingPlanResponse(created))
}

// GetHostingPlan handles GET /api/hosting-plans/{id}.
func (h *HostingPlanHandler) GetHostingPlan(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	obj, err := h.dynClient.Resource(HostingPlanGVR).Get(r.Context(), id, metav1.GetOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			WriteNotFound(w, "Hosting plan not found")
			return
		}
		WriteInternalError(w, "Failed to get hosting plan: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, unstructuredToHostingPlanResponse(obj))
}

// UpdateHostingPlan handles PUT /api/hosting-plans/{id}.
func (h *HostingPlanHandler) UpdateHostingPlan(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	existing, err := h.dynClient.Resource(HostingPlanGVR).Get(r.Context(), id, metav1.GetOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			WriteNotFound(w, "Hosting plan not found")
			return
		}
		WriteInternalError(w, "Failed to get hosting plan: "+err.Error())
		return
	}

	var req UpdateHostingPlanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteBadRequest(w, "Invalid request body", nil)
		return
	}

	spec, _ := existing.Object["spec"].(map[string]interface{})
	if spec == nil {
		spec = make(map[string]interface{})
	}

	if req.DisplayName != "" {
		spec["displayName"] = req.DisplayName
	}

	if req.Limits != nil {
		if req.Limits.Websites < 0 || req.Limits.Databases < 0 || req.Limits.EmailAccounts < 0 {
			WriteBadRequest(w, "Limits must be non-negative", nil)
			return
		}
		spec["limits"] = map[string]interface{}{
			"websites":      int64(req.Limits.Websites),
			"databases":     int64(req.Limits.Databases),
			"emailAccounts": int64(req.Limits.EmailAccounts),
			"storageGB":     int64(req.Limits.StorageGB),
			"cpuMillicores": int64(req.Limits.CPUMillicores),
			"memoryMB":      int64(req.Limits.MemoryMB),
		}
	}

	existing.Object["spec"] = spec

	updated, err := h.dynClient.Resource(HostingPlanGVR).Update(r.Context(), existing, metav1.UpdateOptions{})
	if err != nil {
		WriteInternalError(w, "Failed to update hosting plan: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, unstructuredToHostingPlanResponse(updated))
}

// DeleteHostingPlan handles DELETE /api/hosting-plans/{id}.
func (h *HostingPlanHandler) DeleteHostingPlan(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	_, err := h.dynClient.Resource(HostingPlanGVR).Get(r.Context(), id, metav1.GetOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			WriteNotFound(w, "Hosting plan not found")
			return
		}
		WriteInternalError(w, "Failed to get hosting plan: "+err.Error())
		return
	}

	if err := h.dynClient.Resource(HostingPlanGVR).Delete(r.Context(), id, metav1.DeleteOptions{}); err != nil {
		WriteInternalError(w, "Failed to delete hosting plan: "+err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// AssignPlan handles POST /api/hosting-plans/{id}/assign.
// Assigns a hosting plan to a user by:
// 1. Labeling the user's namespace with the plan name
// 2. Creating/updating ResourceQuota in the namespace
// 3. Creating/updating LimitRange in the namespace
func (h *HostingPlanHandler) AssignPlan(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var req AssignPlanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteBadRequest(w, "Invalid request body", nil)
		return
	}

	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" {
		WriteBadRequest(w, "Username is required", nil)
		return
	}

	// Get the hosting plan
	plan, err := h.dynClient.Resource(HostingPlanGVR).Get(r.Context(), id, metav1.GetOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			WriteNotFound(w, "Hosting plan not found")
			return
		}
		WriteInternalError(w, "Failed to get hosting plan: "+err.Error())
		return
	}

	namespace := fmt.Sprintf("hosting-user-%s", req.Username)

	// Label the namespace with the plan name
	if err := h.labelNamespaceWithPlan(r.Context(), namespace, id); err != nil {
		if strings.Contains(err.Error(), "not found") {
			WriteNotFound(w, "User namespace not found: "+namespace)
			return
		}
		WriteInternalError(w, "Failed to label namespace: "+err.Error())
		return
	}

	// Apply ResourceQuota and LimitRange based on plan limits
	spec, _ := plan.Object["spec"].(map[string]interface{})
	limits, _ := spec["limits"].(map[string]interface{})

	if err := h.applyResourceQuota(r.Context(), namespace, id, limits); err != nil {
		WriteInternalError(w, "Failed to apply ResourceQuota: "+err.Error())
		return
	}

	if err := h.applyLimitRange(r.Context(), namespace, id, limits); err != nil {
		WriteInternalError(w, "Failed to apply LimitRange: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"message":   "Plan assigned successfully",
		"plan":      id,
		"namespace": namespace,
	})
}

// labelNamespaceWithPlan adds the "hosting.panel/plan" label to a namespace.
func (h *HostingPlanHandler) labelNamespaceWithPlan(ctx context.Context, namespace, planName string) error {
	if h.clientset == nil {
		return fmt.Errorf("kubernetes clientset not available")
	}

	ns, err := h.clientset.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
	if err != nil {
		return err
	}

	if ns.Labels == nil {
		ns.Labels = make(map[string]string)
	}
	ns.Labels["hosting.panel/plan"] = planName

	_, err = h.clientset.CoreV1().Namespaces().Update(ctx, ns, metav1.UpdateOptions{})
	return err
}

// applyResourceQuota creates or updates a ResourceQuota in the user's namespace based on plan limits.
func (h *HostingPlanHandler) applyResourceQuota(ctx context.Context, namespace, planName string, limits map[string]interface{}) error {
	if h.clientset == nil {
		return fmt.Errorf("kubernetes clientset not available")
	}

	storageGB := getInt64FromMap(limits, "storageGB")
	cpuMillicores := getInt64FromMap(limits, "cpuMillicores")
	memoryMB := getInt64FromMap(limits, "memoryMB")

	quotaName := "hosting-plan-quota"

	// Build the ResourceQuota spec
	hard := make(map[string]string)
	if storageGB > 0 {
		hard["requests.storage"] = fmt.Sprintf("%dGi", storageGB)
	}
	if cpuMillicores > 0 {
		hard["requests.cpu"] = fmt.Sprintf("%dm", cpuMillicores)
		hard["limits.cpu"] = fmt.Sprintf("%dm", cpuMillicores)
	}
	if memoryMB > 0 {
		hard["requests.memory"] = fmt.Sprintf("%dMi", memoryMB)
		hard["limits.memory"] = fmt.Sprintf("%dMi", memoryMB)
	}

	if len(hard) == 0 {
		return nil // no resource limits to apply
	}

	// Use unstructured to create/update ResourceQuota to avoid typed API version issues
	quotaObj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "ResourceQuota",
			"metadata": map[string]interface{}{
				"name":      quotaName,
				"namespace": namespace,
				"labels": map[string]interface{}{
					"hosting.panel/plan":      planName,
					"hosting.panel/managed-by": "panel-core",
				},
			},
			"spec": map[string]interface{}{
				"hard": stringMapToInterface(hard),
			},
		},
	}

	rqGVR := coreV1GVR("resourcequotas")

	// Try to get existing quota
	existing, err := h.dynClient.Resource(rqGVR).Namespace(namespace).Get(ctx, quotaName, metav1.GetOptions{})
	if err != nil {
		// Create new
		_, err = h.dynClient.Resource(rqGVR).Namespace(namespace).Create(ctx, quotaObj, metav1.CreateOptions{})
		return err
	}

	// Update existing
	quotaObj.SetResourceVersion(existing.GetResourceVersion())
	_, err = h.dynClient.Resource(rqGVR).Namespace(namespace).Update(ctx, quotaObj, metav1.UpdateOptions{})
	return err
}

// applyLimitRange creates or updates a LimitRange in the user's namespace based on plan limits.
func (h *HostingPlanHandler) applyLimitRange(ctx context.Context, namespace, planName string, limits map[string]interface{}) error {
	if h.clientset == nil {
		return fmt.Errorf("kubernetes clientset not available")
	}

	cpuMillicores := getInt64FromMap(limits, "cpuMillicores")
	memoryMB := getInt64FromMap(limits, "memoryMB")

	if cpuMillicores == 0 && memoryMB == 0 {
		return nil // no limits to apply
	}

	lrName := "hosting-plan-limits"

	// Default container limits: fraction of total plan limits
	defaultLimits := make(map[string]interface{})
	defaultRequests := make(map[string]interface{})

	if cpuMillicores > 0 {
		// Default per-container: 25% of total plan CPU
		defaultCPU := cpuMillicores / 4
		if defaultCPU < 100 {
			defaultCPU = 100
		}
		defaultLimits["cpu"] = fmt.Sprintf("%dm", defaultCPU)
		defaultRequests["cpu"] = fmt.Sprintf("%dm", defaultCPU/2)
	}
	if memoryMB > 0 {
		// Default per-container: 25% of total plan memory
		defaultMem := memoryMB / 4
		if defaultMem < 64 {
			defaultMem = 64
		}
		defaultLimits["memory"] = fmt.Sprintf("%dMi", defaultMem)
		defaultRequests["memory"] = fmt.Sprintf("%dMi", defaultMem/2)
	}

	lrObj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "LimitRange",
			"metadata": map[string]interface{}{
				"name":      lrName,
				"namespace": namespace,
				"labels": map[string]interface{}{
					"hosting.panel/plan":      planName,
					"hosting.panel/managed-by": "panel-core",
				},
			},
			"spec": map[string]interface{}{
				"limits": []interface{}{
					map[string]interface{}{
						"type":           "Container",
						"default":        defaultLimits,
						"defaultRequest": defaultRequests,
					},
				},
			},
		},
	}

	lrGVR := coreV1GVR("limitranges")

	existing, err := h.dynClient.Resource(lrGVR).Namespace(namespace).Get(ctx, lrName, metav1.GetOptions{})
	if err != nil {
		_, err = h.dynClient.Resource(lrGVR).Namespace(namespace).Create(ctx, lrObj, metav1.CreateOptions{})
		return err
	}

	lrObj.SetResourceVersion(existing.GetResourceVersion())
	_, err = h.dynClient.Resource(lrGVR).Namespace(namespace).Update(ctx, lrObj, metav1.UpdateOptions{})
	return err
}

// --- Helpers ---

// coreV1GVR returns a GVR for core/v1 resources.
func coreV1GVR(resource string) schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: "", Version: "v1", Resource: resource}
}

// getInt64FromMap extracts an int64 from a map, handling both int64 and float64 JSON types.
func getInt64FromMap(m map[string]interface{}, key string) int64 {
	if m == nil {
		return 0
	}
	if v, ok := m[key].(int64); ok {
		return v
	}
	if v, ok := m[key].(float64); ok {
		return int64(v)
	}
	return 0
}

// stringMapToInterface converts map[string]string to map[string]interface{}.
func stringMapToInterface(m map[string]string) map[string]interface{} {
	result := make(map[string]interface{}, len(m))
	for k, v := range m {
		result[k] = v
	}
	return result
}

// --- Conversion helpers ---

// hostingPlanRequestToUnstructured converts a CreateHostingPlanRequest to an unstructured Kubernetes object.
func hostingPlanRequestToUnstructured(req CreateHostingPlanRequest) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": CRDGroup + "/" + CRDVersion,
			"kind":       "HostingPlan",
			"metadata": map[string]interface{}{
				"name": req.Name,
			},
			"spec": map[string]interface{}{
				"displayName": req.DisplayName,
				"limits": map[string]interface{}{
					"websites":      int64(req.Limits.Websites),
					"databases":     int64(req.Limits.Databases),
					"emailAccounts": int64(req.Limits.EmailAccounts),
					"storageGB":     int64(req.Limits.StorageGB),
					"cpuMillicores": int64(req.Limits.CPUMillicores),
					"memoryMB":      int64(req.Limits.MemoryMB),
				},
			},
		},
	}
}

// unstructuredToHostingPlanResponse converts an unstructured HostingPlan CRD to a HostingPlanResponse.
func unstructuredToHostingPlanResponse(obj *unstructured.Unstructured) HostingPlanResponse {
	resp := HostingPlanResponse{
		Name:      obj.GetName(),
		CreatedAt: obj.GetCreationTimestamp().Format("2006-01-02T15:04:05Z"),
	}

	spec, _ := obj.Object["spec"].(map[string]interface{})
	if spec != nil {
		resp.DisplayName, _ = spec["displayName"].(string)

		limits, _ := spec["limits"].(map[string]interface{})
		if limits != nil {
			resp.Limits.Websites = getInt64FromMap(limits, "websites")
			resp.Limits.Databases = getInt64FromMap(limits, "databases")
			resp.Limits.EmailAccounts = getInt64FromMap(limits, "emailAccounts")
			resp.Limits.StorageGB = getInt64FromMap(limits, "storageGB")
			resp.Limits.CPUMillicores = getInt64FromMap(limits, "cpuMillicores")
			resp.Limits.MemoryMB = getInt64FromMap(limits, "memoryMB")
		}
	}

	return resp
}
