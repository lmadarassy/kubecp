package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/hosting-panel/panel-core/internal/middleware"
)

// CronJobRequest is the JSON body for creating/updating a cron job.
type CronJobRequest struct {
	Name      string `json:"name"`
	Schedule  string `json:"schedule"`  // cron expression
	Command   string `json:"command"`
	WebsiteID string `json:"websiteId"` // which website pod to run in
	Enabled   bool   `json:"enabled"`
}

// CronJobResponse is the JSON response for cron job endpoints.
type CronJobResponse struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Schedule     string `json:"schedule"`
	Command      string `json:"command"`
	WebsiteID    string `json:"websiteId"`
	Enabled      bool   `json:"enabled"`
	LastSchedule string `json:"lastSchedule,omitempty"`
	CreatedAt    string `json:"createdAt,omitempty"`
}

type CronJobHandler struct {
	clientset kubernetes.Interface
	dynClient dynamic.Interface
}

func NewCronJobHandler(clientset kubernetes.Interface, dynClient dynamic.Interface) *CronJobHandler {
	return &CronJobHandler{clientset: clientset, dynClient: dynClient}
}

func (h *CronJobHandler) RegisterRoutes(r chi.Router) {
	r.Get("/", h.ListCronJobs)
	r.Post("/", h.CreateCronJob)
	r.Get("/{id}", h.GetCronJob)
	r.Put("/{id}", h.UpdateCronJob)
	r.Delete("/{id}", h.DeleteCronJob)
}

// cronJobLabelPrefix is the label prefix for hosting panel cron jobs.
const cronJobLabelPrefix = "hosting-cron-"

// Predefined schedule templates.
var scheduleTemplates = map[string]string{
	"every-minute":  "* * * * *",
	"every-5min":    "*/5 * * * *",
	"every-15min":   "*/15 * * * *",
	"every-30min":   "*/30 * * * *",
	"hourly":        "0 * * * *",
	"daily":         "0 0 * * *",
	"weekly":        "0 0 * * 0",
	"monthly":       "0 0 1 * *",
}

// cronExprRegex validates a basic cron expression (5 fields).
var cronExprRegex = regexp.MustCompile(`^(\S+\s+){4}\S+$`)

func validateCronSchedule(schedule string) (string, error) {
	// Check if it's a template name
	if resolved, ok := scheduleTemplates[schedule]; ok {
		return resolved, nil
	}
	// Validate as raw cron expression
	if !cronExprRegex.MatchString(strings.TrimSpace(schedule)) {
		return "", fmt.Errorf("invalid cron expression: %s", schedule)
	}
	return strings.TrimSpace(schedule), nil
}

// ListCronJobs handles GET /api/cron-jobs
func (h *CronJobHandler) ListCronJobs(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		WriteUnauthorized(w, "no claims")
		return
	}

	labelSelector := "hosting.panel/managed-by=hosting-panel"
	if !middleware.HasRole(claims, "admin") {
		labelSelector += ",hosting.panel/user=" + claims.Username
	}

	cronJobs, err := h.clientset.BatchV1().CronJobs(hostingNamespace).List(r.Context(), metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		WriteInternalError(w, "Failed to list cron jobs: "+err.Error())
		return
	}

	results := make([]CronJobResponse, 0, len(cronJobs.Items))
	for _, cj := range cronJobs.Items {
		results = append(results, cronJobToResponse(&cj))
	}

	writeJSON(w, http.StatusOK, results)
}

// CreateCronJob handles POST /api/cron-jobs
func (h *CronJobHandler) CreateCronJob(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		WriteUnauthorized(w, "no claims")
		return
	}

	var req CronJobRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteBadRequest(w, "Invalid request body", nil)
		return
	}

	if req.Name == "" || req.Schedule == "" || req.Command == "" || req.WebsiteID == "" {
		WriteBadRequest(w, "name, schedule, command, and websiteId are required", nil)
		return
	}

	schedule, err := validateCronSchedule(req.Schedule)
	if err != nil {
		WriteBadRequest(w, err.Error(), nil)
		return
	}

	// Verify website ownership
	websiteObj, err := h.dynClient.Resource(WebsiteGVR).Namespace(hostingNamespace).Get(r.Context(), req.WebsiteID, metav1.GetOptions{})
	if err != nil {
		WriteBadRequest(w, "website not found: "+err.Error(), nil)
		return
	}
	if !middleware.HasRole(claims, "admin") && !middleware.IsOwnerByLabel(claims, websiteObj) {
		WriteForbidden(w, "access denied")
		return
	}

	// Check cron job quota from hosting plan
	if err := h.checkCronJobQuota(r, claims); err != nil {
		WriteQuotaExceeded(w, err.Error(), nil)
		return
	}

	// Build K8s CronJob
	cronJobName := cronJobLabelPrefix + claims.Username + "-" + req.Name
	suspend := !req.Enabled
	cronJob := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cronJobName,
			Namespace: hostingNamespace,
			Labels: map[string]string{
				"hosting.panel/managed-by": "hosting-panel",
				"hosting.panel/user":       claims.Username,
				"hosting.panel/website":    req.WebsiteID,
				"hosting.panel/cron-name":  req.Name,
			},
		},
		Spec: batchv1.CronJobSpec{
			Schedule: schedule,
			Suspend:  &suspend,
			JobTemplate: batchv1.JobTemplateSpec{
				Spec: batchv1.JobSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							RestartPolicy: corev1.RestartPolicyNever,
							Containers: []corev1.Container{
								{
									Name:    "cron",
									Image:   "php:8.2-cli",
									Command: []string{"sh", "-c", req.Command},
								},
							},
						},
					},
				},
			},
		},
	}

	created, err := h.clientset.BatchV1().CronJobs(hostingNamespace).Create(r.Context(), cronJob, metav1.CreateOptions{})
	if err != nil {
		if k8serrors.IsAlreadyExists(err) {
			WriteConflict(w, "cron job already exists", nil)
			return
		}
		WriteInternalError(w, "Failed to create cron job: "+err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, cronJobToResponse(created))
}

// GetCronJob handles GET /api/cron-jobs/{id}
func (h *CronJobHandler) GetCronJob(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		WriteUnauthorized(w, "no claims")
		return
	}

	id := chi.URLParam(r, "id")
	cj, err := h.clientset.BatchV1().CronJobs(hostingNamespace).Get(r.Context(), id, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			WriteNotFound(w, "cron job not found")
			return
		}
		WriteInternalError(w, "Failed to get cron job: "+err.Error())
		return
	}

	if !middleware.HasRole(claims, "admin") && cj.Labels["hosting.panel/user"] != claims.Username {
		WriteForbidden(w, "access denied")
		return
	}

	writeJSON(w, http.StatusOK, cronJobToResponse(cj))
}

// UpdateCronJob handles PUT /api/cron-jobs/{id}
func (h *CronJobHandler) UpdateCronJob(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		WriteUnauthorized(w, "no claims")
		return
	}

	id := chi.URLParam(r, "id")
	cj, err := h.clientset.BatchV1().CronJobs(hostingNamespace).Get(r.Context(), id, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			WriteNotFound(w, "cron job not found")
			return
		}
		WriteInternalError(w, "Failed to get cron job: "+err.Error())
		return
	}

	if !middleware.HasRole(claims, "admin") && cj.Labels["hosting.panel/user"] != claims.Username {
		WriteForbidden(w, "access denied")
		return
	}

	var req CronJobRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteBadRequest(w, "Invalid request body", nil)
		return
	}

	if req.Schedule != "" {
		schedule, err := validateCronSchedule(req.Schedule)
		if err != nil {
			WriteBadRequest(w, err.Error(), nil)
			return
		}
		cj.Spec.Schedule = schedule
	}
	if req.Command != "" {
		if len(cj.Spec.JobTemplate.Spec.Template.Spec.Containers) > 0 {
			cj.Spec.JobTemplate.Spec.Template.Spec.Containers[0].Command = []string{"sh", "-c", req.Command}
		}
	}
	suspend := !req.Enabled
	cj.Spec.Suspend = &suspend

	updated, err := h.clientset.BatchV1().CronJobs(hostingNamespace).Update(r.Context(), cj, metav1.UpdateOptions{})
	if err != nil {
		WriteInternalError(w, "Failed to update cron job: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, cronJobToResponse(updated))
}

// DeleteCronJob handles DELETE /api/cron-jobs/{id}
func (h *CronJobHandler) DeleteCronJob(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		WriteUnauthorized(w, "no claims")
		return
	}

	id := chi.URLParam(r, "id")
	cj, err := h.clientset.BatchV1().CronJobs(hostingNamespace).Get(r.Context(), id, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			WriteNotFound(w, "cron job not found")
			return
		}
		WriteInternalError(w, "Failed to get cron job: "+err.Error())
		return
	}

	if !middleware.HasRole(claims, "admin") && cj.Labels["hosting.panel/user"] != claims.Username {
		WriteForbidden(w, "access denied")
		return
	}

	if err := h.clientset.BatchV1().CronJobs(hostingNamespace).Delete(r.Context(), id, metav1.DeleteOptions{}); err != nil {
		WriteInternalError(w, "Failed to delete cron job: "+err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// checkCronJobQuota checks if the user has exceeded their cron job quota.
func (h *CronJobHandler) checkCronJobQuota(r *http.Request, claims *middleware.TokenClaims) error {
	// Count existing cron jobs for this user
	existing, err := h.clientset.BatchV1().CronJobs(hostingNamespace).List(r.Context(), metav1.ListOptions{
		LabelSelector: "hosting.panel/managed-by=hosting-panel,hosting.panel/user=" + claims.Username,
	})
	if err != nil {
		return fmt.Errorf("failed to count cron jobs: %w", err)
	}

	// Look up the user's hosting plan to get the cron job limit
	planLimit, err := h.getUserCronJobLimit(r, claims)
	if err != nil {
		return err
	}

	if planLimit > 0 && int64(len(existing.Items)) >= planLimit {
		return fmt.Errorf("cron job quota exceeded: %d/%d", len(existing.Items), planLimit)
	}
	return nil
}

// getUserCronJobLimit retrieves the cron job limit from the user's hosting plan.
func (h *CronJobHandler) getUserCronJobLimit(r *http.Request, claims *middleware.TokenClaims) (int64, error) {
	if h.dynClient == nil {
		return 0, nil // no limit enforcement without dynamic client
	}

	// List hosting plans and find the one assigned to this user's namespace
	plans, err := h.dynClient.Resource(HostingPlanGVR).Namespace(hostingNamespace).List(r.Context(), metav1.ListOptions{})
	if err != nil {
		return 0, nil // fail open if plans unavailable
	}

	for _, plan := range plans.Items {
		spec, ok := plan.Object["spec"].(map[string]interface{})
		if !ok {
			continue
		}
		limits, ok := spec["limits"].(map[string]interface{})
		if !ok {
			continue
		}
		if cronLimit, ok := limits["cronJobs"]; ok {
			switch v := cronLimit.(type) {
			case int64:
				return v, nil
			case float64:
				return int64(v), nil
			}
		}
	}
	return 0, nil // no limit found
}

// cronJobToResponse converts a K8s CronJob to our API response.
func cronJobToResponse(cj *batchv1.CronJob) CronJobResponse {
	resp := CronJobResponse{
		ID:        cj.Name, // K8s name (e.g. hosting-cron-admin-test-cron)
		Name:      cj.Labels["hosting.panel/cron-name"],
		Schedule:  cj.Spec.Schedule,
		WebsiteID: cj.Labels["hosting.panel/website"],
		Enabled:   cj.Spec.Suspend == nil || !*cj.Spec.Suspend,
		CreatedAt: cj.CreationTimestamp.Format("2006-01-02T15:04:05Z"),
	}

	if len(cj.Spec.JobTemplate.Spec.Template.Spec.Containers) > 0 {
		cmd := cj.Spec.JobTemplate.Spec.Template.Spec.Containers[0].Command
		if len(cmd) > 2 {
			resp.Command = cmd[2] // "sh", "-c", <actual command>
		}
	}

	if cj.Status.LastScheduleTime != nil {
		resp.LastSchedule = cj.Status.LastScheduleTime.Format("2006-01-02T15:04:05Z")
	}

	return resp
}
