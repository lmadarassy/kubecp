package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/hosting-panel/panel-core/internal/middleware"
)

var (
	// Kubernetes batch/v1 GVRs
	JobGVR = schema.GroupVersionResource{
		Group: "batch", Version: "v1", Resource: "jobs",
	}
	CronJobGVR = schema.GroupVersionResource{
		Group: "batch", Version: "v1", Resource: "cronjobs",
	}
	// ConfigMap GVR for backup metadata storage
	ConfigMapGVR = schema.GroupVersionResource{
		Group: "", Version: "v1", Resource: "configmaps",
	}
)

// BackupHandler implements backup management API endpoints.
type BackupHandler struct {
	dynClient dynamic.Interface
}

// NewBackupHandler creates a new BackupHandler.
func NewBackupHandler(dynClient dynamic.Interface) *BackupHandler {
	return &BackupHandler{dynClient: dynClient}
}

// RegisterRoutes registers backup management routes.
func (h *BackupHandler) RegisterRoutes(r chi.Router) {
	r.Get("/", h.ListBackups)
	r.Post("/", h.CreateBackup)
	r.Post("/{id}/restore", h.RestoreBackup)
	r.Get("/schedules", h.ListSchedules)
	r.Post("/schedules", h.CreateSchedule)
	r.Delete("/schedules/{id}", h.DeleteSchedule)
}

// --- Request/Response types ---

// BackupTarget defines where the backup is stored.
type BackupTarget struct {
	Type     string `json:"type"`               // "s3", "sftp", "local"
	Bucket   string `json:"bucket,omitempty"`    // S3 bucket name
	Endpoint string `json:"endpoint,omitempty"`  // S3 endpoint or SFTP host
	Path     string `json:"path,omitempty"`      // Remote path or local PV path
}

// BackupResponse is the JSON response for a backup entry.
type BackupResponse struct {
	ID          string       `json:"id"`
	Namespace   string       `json:"namespace"`
	Status      string       `json:"status"` // Pending, Running, Completed, Failed
	CreatedAt   string       `json:"createdAt"`
	CompletedAt string       `json:"completedAt,omitempty"`
	SizeBytes   int64        `json:"sizeBytes,omitempty"`
	Target      BackupTarget `json:"target"`
	Components  []string     `json:"components,omitempty"` // web, db, mail, dns, config
	Message     string       `json:"message,omitempty"`
}

// CreateBackupRequest is the JSON body for POST /api/backups.
type CreateBackupRequest struct {
	Target     BackupTarget `json:"target"`
	Components []string     `json:"components,omitempty"` // empty = all
}

// RestoreRequest is the JSON body for POST /api/backups/{id}/restore.
type RestoreRequest struct {
	Components []string `json:"components,omitempty"` // empty = full restore
}

// ScheduleResponse is the JSON response for a backup schedule.
type ScheduleResponse struct {
	ID             string       `json:"id"`
	Namespace      string       `json:"namespace"`
	Schedule       string       `json:"schedule"` // cron expression
	Target         BackupTarget `json:"target"`
	RetentionCount int          `json:"retentionCount"`
	LastBackup     string       `json:"lastBackup,omitempty"`
	NextBackup     string       `json:"nextBackup,omitempty"`
}

// CreateScheduleRequest is the JSON body for POST /api/backups/schedules.
type CreateScheduleRequest struct {
	Schedule       string       `json:"schedule"` // cron expression
	Target         BackupTarget `json:"target"`
	RetentionCount int          `json:"retentionCount"`
}

// --- Handlers ---

// ListBackups handles GET /api/backups.
func (h *BackupHandler) ListBackups(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	isAdmin := middleware.HasRole(claims, "admin")

	var allBackups []BackupResponse

	if isAdmin {
		// Admin: list backup Jobs across all hosting namespaces
		jobs, err := h.dynClient.Resource(JobGVR).Namespace("").List(r.Context(), metav1.ListOptions{
			LabelSelector: "hosting.panel/type=backup",
		})
		if err != nil {
			WriteInternalError(w, "Failed to list backups: "+err.Error())
			return
		}
		for _, j := range jobs.Items {
			allBackups = append(allBackups, jobToBackupResponse(&j))
		}
	} else {
		ns := hostingNamespace
		jobs, err := h.dynClient.Resource(JobGVR).Namespace(ns).List(r.Context(), metav1.ListOptions{
			LabelSelector: "hosting.panel/type=backup",
		})
		if err != nil {
			WriteInternalError(w, "Failed to list backups: "+err.Error())
			return
		}
		for _, j := range jobs.Items {
			allBackups = append(allBackups, jobToBackupResponse(&j))
		}
	}

	if allBackups == nil {
		allBackups = []BackupResponse{}
	}

	// Sort by creation time descending
	sort.Slice(allBackups, func(i, j int) bool {
		return allBackups[i].CreatedAt > allBackups[j].CreatedAt
	})

	writeJSON(w, http.StatusOK, allBackups)
}

// CreateBackup handles POST /api/backups.
func (h *BackupHandler) CreateBackup(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())

	var req CreateBackupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteBadRequest(w, "Invalid request body", nil)
		return
	}

	if req.Target.Type == "" {
		req.Target.Type = "local"
		if req.Target.Path == "" {
			req.Target.Path = "/backups"
		}
	}

	if err := validateBackupTarget(req.Target); err != nil {
		WriteBadRequest(w, err.Error(), nil)
		return
	}

	components := req.Components
	if len(components) == 0 {
		components = []string{"web", "db", "mail", "dns", "config"}
	}
	if err := validateComponents(components); err != nil {
		WriteBadRequest(w, err.Error(), nil)
		return
	}

	ns := hostingNamespace
	jobName := fmt.Sprintf("backup-%s-%d", claims.Username, time.Now().Unix())

	job := buildBackupJob(jobName, ns, claims.Username, req.Target, components)

	_, err := h.dynClient.Resource(JobGVR).Namespace(ns).Create(r.Context(), job, metav1.CreateOptions{})
	if err != nil {
		WriteInternalError(w, "Failed to create backup job: "+err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, BackupResponse{
		ID:         jobName,
		Namespace:  ns,
		Status:     "Pending",
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		Target:     req.Target,
		Components: components,
	})
}

// RestoreBackup handles POST /api/backups/{id}/restore.
func (h *BackupHandler) RestoreBackup(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	backupID := chi.URLParam(r, "id")

	ns := hostingNamespace

	// Verify the backup job exists
	backupJob, err := h.dynClient.Resource(JobGVR).Namespace(ns).Get(r.Context(), backupID, metav1.GetOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			WriteNotFound(w, "Backup not found: "+backupID)
			return
		}
		WriteInternalError(w, "Failed to get backup: "+err.Error())
		return
	}

	// Check backup is completed
	backup := jobToBackupResponse(backupJob)
	if backup.Status != "Completed" {
		WriteBadRequest(w, "Cannot restore from backup with status: "+backup.Status, nil)
		return
	}

	var req RestoreRequest
	if r.Body != nil && r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteBadRequest(w, "Invalid request body", nil)
			return
		}
	}

	components := req.Components
	if len(components) == 0 {
		components = []string{"web", "db", "mail", "dns", "config"}
	}

	restoreJobName := fmt.Sprintf("restore-%s-%d", claims.Username, time.Now().Unix())
	restoreJob := buildRestoreJob(restoreJobName, ns, claims.Username, backupID, backup.Target, components)

	_, err = h.dynClient.Resource(JobGVR).Namespace(ns).Create(r.Context(), restoreJob, metav1.CreateOptions{})
	if err != nil {
		WriteInternalError(w, "Failed to create restore job: "+err.Error())
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]string{
		"id":        restoreJobName,
		"namespace": ns,
		"status":    "Pending",
		"backupId":  backupID,
	})
}

// ListSchedules handles GET /api/backups/schedules.
func (h *BackupHandler) ListSchedules(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	isAdmin := middleware.HasRole(claims, "admin")

	var schedules []ScheduleResponse

	if isAdmin {
		cronJobs, err := h.dynClient.Resource(CronJobGVR).Namespace("").List(r.Context(), metav1.ListOptions{
			LabelSelector: "hosting.panel/type=backup-schedule",
		})
		if err != nil {
			WriteInternalError(w, "Failed to list schedules: "+err.Error())
			return
		}
		for _, cj := range cronJobs.Items {
			schedules = append(schedules, cronJobToScheduleResponse(&cj))
		}
	} else {
		ns := hostingNamespace
		cronJobs, err := h.dynClient.Resource(CronJobGVR).Namespace(ns).List(r.Context(), metav1.ListOptions{
			LabelSelector: "hosting.panel/type=backup-schedule",
		})
		if err != nil {
			WriteInternalError(w, "Failed to list schedules: "+err.Error())
			return
		}
		for _, cj := range cronJobs.Items {
			schedules = append(schedules, cronJobToScheduleResponse(&cj))
		}
	}

	if schedules == nil {
		schedules = []ScheduleResponse{}
	}
	writeJSON(w, http.StatusOK, schedules)
}

// CreateSchedule handles POST /api/backups/schedules.
// Admin only — creates a CronJob for scheduled backups.
func (h *BackupHandler) CreateSchedule(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if !middleware.HasRole(claims, "admin") {
		WriteForbidden(w, "Only admins can create backup schedules")
		return
	}

	var req CreateScheduleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteBadRequest(w, "Invalid request body", nil)
		return
	}

	if req.Schedule == "" {
		WriteBadRequest(w, "Schedule (cron expression) is required", nil)
		return
	}
	if err := validateBackupTarget(req.Target); err != nil {
		WriteBadRequest(w, err.Error(), nil)
		return
	}
	if req.RetentionCount < 1 {
		req.RetentionCount = 7 // default retention
	}

	// Schedule is created in the user's namespace specified by query param or admin's own
	targetUser := r.URL.Query().Get("user")
	if targetUser == "" {
		targetUser = claims.Username
	}
	ns := hostingNamespace
	cronJobName := fmt.Sprintf("backup-schedule-%s", targetUser)

	cronJob := buildBackupCronJob(cronJobName, ns, targetUser, req.Schedule, req.Target, req.RetentionCount)

	_, err := h.dynClient.Resource(CronJobGVR).Namespace(ns).Create(r.Context(), cronJob, metav1.CreateOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "already exists") {
			WriteConflict(w, "Backup schedule already exists for user: "+targetUser, nil)
			return
		}
		WriteInternalError(w, "Failed to create backup schedule: "+err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, ScheduleResponse{
		ID:             cronJobName,
		Namespace:      ns,
		Schedule:       req.Schedule,
		Target:         req.Target,
		RetentionCount: req.RetentionCount,
	})
}

// DeleteSchedule handles DELETE /api/backups/schedules/{id}.
func (h *BackupHandler) DeleteSchedule(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if !middleware.HasRole(claims, "admin") {
		WriteForbidden(w, "Only admins can delete backup schedules")
		return
	}

	scheduleID := chi.URLParam(r, "id")
	ns := hostingNamespace

	err := h.dynClient.Resource(CronJobGVR).Namespace(ns).Delete(r.Context(), scheduleID, metav1.DeleteOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			WriteNotFound(w, "Schedule not found: "+scheduleID)
			return
		}
		WriteInternalError(w, "Failed to delete schedule: "+err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// --- Helper functions ---

// validateBackupTarget validates the backup target configuration.
func validateBackupTarget(t BackupTarget) error {
	switch t.Type {
	case "s3":
		if t.Bucket == "" {
			return fmt.Errorf("S3 bucket is required")
		}
		if t.Endpoint == "" {
			return fmt.Errorf("S3 endpoint is required")
		}
	case "sftp":
		if t.Endpoint == "" {
			return fmt.Errorf("SFTP host is required")
		}
		if t.Path == "" {
			return fmt.Errorf("SFTP path is required")
		}
	case "local":
		if t.Path == "" {
			return fmt.Errorf("Local path is required")
		}
	default:
		return fmt.Errorf("Invalid backup target type: %q (must be s3, sftp, or local)", t.Type)
	}
	return nil
}

// validateComponents validates the backup component list.
func validateComponents(components []string) error {
	valid := map[string]bool{"web": true, "db": true, "mail": true, "dns": true, "config": true}
	for _, c := range components {
		if !valid[c] {
			return fmt.Errorf("Invalid backup component: %q (valid: web, db, mail, dns, config)", c)
		}
	}
	return nil
}

// buildBackupJob creates an unstructured Job for a manual backup.
func buildBackupJob(name, namespace, username string, target BackupTarget, components []string) *unstructured.Unstructured {
	targetJSON, _ := json.Marshal(target)
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "batch/v1",
			"kind":       "Job",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
				"labels": map[string]interface{}{
					"hosting.panel/type": "backup",
					"hosting.panel/user": username,
					"managed-by":         "panel-core",
				},
				"annotations": map[string]interface{}{
					"hosting.panel/target":     string(targetJSON),
					"hosting.panel/components": strings.Join(components, ","),
				},
			},
			"spec": map[string]interface{}{
				"backoffLimit": int64(3),
				"template": map[string]interface{}{
					"metadata": map[string]interface{}{
						"labels": map[string]interface{}{
							"hosting.panel/type": "backup",
							"hosting.panel/user": username,
						},
					},
					"spec": backupPodSpec("backup", backupImage(), username, []interface{}{
						map[string]interface{}{"name": "BACKUP_USER", "value": username},
						map[string]interface{}{"name": "BACKUP_NAMESPACE", "value": namespace},
						map[string]interface{}{"name": "BACKUP_TARGET", "value": string(targetJSON)},
						map[string]interface{}{"name": "BACKUP_COMPONENTS", "value": strings.Join(components, ",")},
						map[string]interface{}{"name": "BACKUP_ID", "value": name},
					}),
				},
			},
		},
	}
}

// buildRestoreJob creates an unstructured Job for a restore operation.
func buildRestoreJob(name, namespace, username, backupID string, target BackupTarget, components []string) *unstructured.Unstructured {
	targetJSON, _ := json.Marshal(target)
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "batch/v1",
			"kind":       "Job",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
				"labels": map[string]interface{}{
					"hosting.panel/type":   "restore",
					"hosting.panel/user":   username,
					"hosting.panel/backup": backupID,
					"managed-by":           "panel-core",
				},
				"annotations": map[string]interface{}{
					"hosting.panel/target":     string(targetJSON),
					"hosting.panel/components": strings.Join(components, ","),
				},
			},
			"spec": map[string]interface{}{
				"backoffLimit": int64(3),
				"template": map[string]interface{}{
					"metadata": map[string]interface{}{
						"labels": map[string]interface{}{
							"hosting.panel/type":   "restore",
							"hosting.panel/user":   username,
							"hosting.panel/backup": backupID,
						},
					},
					"spec": backupPodSpec("restore", backupImage(), username, []interface{}{
						map[string]interface{}{"name": "RESTORE_USER", "value": username},
						map[string]interface{}{"name": "RESTORE_NAMESPACE", "value": namespace},
						map[string]interface{}{"name": "RESTORE_BACKUP_ID", "value": backupID},
						map[string]interface{}{"name": "RESTORE_TARGET", "value": string(targetJSON)},
						map[string]interface{}{"name": "RESTORE_COMPONENTS", "value": strings.Join(components, ",")},
					}),
				},
			},
		},
	}
}

// userVolumeSpec returns a volume spec for user data — hostPath or PVC depending on USER_VOLUME_MODE.
func userVolumeSpec(name, username string) map[string]interface{} {
	if os.Getenv("USER_VOLUME_MODE") == "hostpath" {
		return map[string]interface{}{
			"name": name,
			"hostPath": map[string]interface{}{
				"path": "/data/kubecp/uv-" + username,
				"type": "DirectoryOrCreate",
			},
		}
	}
	return map[string]interface{}{
		"name":                   name,
		"persistentVolumeClaim": map[string]interface{}{"claimName": "uv-" + username},
	}
}

// backupPodSpec returns a shared pod spec for backup/restore jobs with volume mounts.
func backupPodSpec(containerName, image, username string, env []interface{}) map[string]interface{} {
	dbEnv := []interface{}{
		map[string]interface{}{"name": "MARIADB_HOST", "value": "hosting-panel-mariadb-galera." + hostingNamespace + ".svc.cluster.local"},
		map[string]interface{}{"name": "MARIADB_PORT", "value": "3306"},
		map[string]interface{}{
			"name": "MARIADB_ROOT_PASSWORD",
			"valueFrom": map[string]interface{}{
				"secretKeyRef": map[string]interface{}{
					"name": "hosting-panel-mariadb-galera",
					"key":  "mariadb-root-password",
				},
			},
		},
	}
	allEnv := append(env, dbEnv...)
	return map[string]interface{}{
		"restartPolicy": "OnFailure",
		"containers": []interface{}{
			map[string]interface{}{
				"name":            containerName,
				"image":           image,
				"imagePullPolicy": "IfNotPresent",
				"env":             allEnv,
				"volumeMounts": []interface{}{
					map[string]interface{}{"name": "web-data", "mountPath": "/data/web"},
					map[string]interface{}{"name": "backups", "mountPath": "/backups"},
				},
			},
		},
		"volumes": []interface{}{
			userVolumeSpec("web-data", username),
			map[string]interface{}{
				"name": "backups",
				"persistentVolumeClaim": map[string]interface{}{"claimName": "hosting-panel-backup-storage"},
			},
		},
	}
}

// buildBackupCronJob creates an unstructured CronJob for scheduled backups.
func buildBackupCronJob(name, namespace, username, schedule string, target BackupTarget, retentionCount int) *unstructured.Unstructured {
	targetJSON, _ := json.Marshal(target)
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "batch/v1",
			"kind":       "CronJob",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
				"labels": map[string]interface{}{
					"hosting.panel/type": "backup-schedule",
					"hosting.panel/user": username,
					"managed-by":         "panel-core",
				},
				"annotations": map[string]interface{}{
					"hosting.panel/target":    string(targetJSON),
					"hosting.panel/retention": fmt.Sprintf("%d", retentionCount),
				},
			},
			"spec": map[string]interface{}{
				"schedule":                   schedule,
				"successfulJobsHistoryLimit": int64(retentionCount),
				"failedJobsHistoryLimit":     int64(3),
				"jobTemplate": map[string]interface{}{
					"metadata": map[string]interface{}{
						"labels": map[string]interface{}{
							"hosting.panel/type": "backup",
							"hosting.panel/user": username,
							"managed-by":         "panel-core",
						},
					},
					"spec": map[string]interface{}{
						"backoffLimit": int64(3),
						"template": map[string]interface{}{
							"spec": backupPodSpec("backup", backupImage(), username, []interface{}{
								map[string]interface{}{"name": "BACKUP_USER", "value": username},
								map[string]interface{}{"name": "BACKUP_NAMESPACE", "value": namespace},
								map[string]interface{}{"name": "BACKUP_TARGET", "value": string(targetJSON)},
								map[string]interface{}{"name": "BACKUP_COMPONENTS", "value": "web,db,mail,dns,config"},
								map[string]interface{}{"name": "BACKUP_RETENTION", "value": fmt.Sprintf("%d", retentionCount)},
							}),
						},
					},
				},
			},
		},
	}
}

// jobToBackupResponse converts a Kubernetes Job to BackupResponse.
func jobToBackupResponse(obj *unstructured.Unstructured) BackupResponse {
	resp := BackupResponse{
		ID:        obj.GetName(),
		Namespace: obj.GetNamespace(),
		CreatedAt: obj.GetCreationTimestamp().UTC().Format(time.RFC3339),
	}

	// Parse target from annotation
	annotations := obj.GetAnnotations()
	if targetStr, ok := annotations["hosting.panel/target"]; ok {
		json.Unmarshal([]byte(targetStr), &resp.Target)
	}
	if compStr, ok := annotations["hosting.panel/components"]; ok {
		resp.Components = strings.Split(compStr, ",")
	}

	// Determine status from Job status
	status, _ := obj.Object["status"].(map[string]interface{})
	if status != nil {
		if succeeded, ok := status["succeeded"].(int64); ok && succeeded > 0 {
			resp.Status = "Completed"
			if ct, ok := status["completionTime"].(string); ok {
				resp.CompletedAt = ct
			}
		} else if failed, ok := status["failed"].(int64); ok && failed > 0 {
			resp.Status = "Failed"
			// Extract failure message from conditions
			if conditions, ok := status["conditions"].([]interface{}); ok {
				for _, c := range conditions {
					cond, _ := c.(map[string]interface{})
					if cond != nil && fmt.Sprintf("%v", cond["type"]) == "Failed" {
						resp.Message = fmt.Sprintf("%v", cond["message"])
					}
				}
			}
		} else if active, ok := status["active"].(int64); ok && active > 0 {
			resp.Status = "Running"
		} else {
			resp.Status = "Pending"
		}
	} else {
		resp.Status = "Pending"
	}

	return resp
}

// cronJobToScheduleResponse converts a Kubernetes CronJob to ScheduleResponse.
func cronJobToScheduleResponse(obj *unstructured.Unstructured) ScheduleResponse {
	resp := ScheduleResponse{
		ID:        obj.GetName(),
		Namespace: obj.GetNamespace(),
	}

	// Parse target from annotation
	annotations := obj.GetAnnotations()
	if targetStr, ok := annotations["hosting.panel/target"]; ok {
		json.Unmarshal([]byte(targetStr), &resp.Target)
	}
	if retStr, ok := annotations["hosting.panel/retention"]; ok {
		fmt.Sscanf(retStr, "%d", &resp.RetentionCount)
	}

	// Extract schedule from spec
	spec, _ := obj.Object["spec"].(map[string]interface{})
	if spec != nil {
		if sched, ok := spec["schedule"].(string); ok {
			resp.Schedule = sched
		}
	}

	// Extract last schedule time from status
	status, _ := obj.Object["status"].(map[string]interface{})
	if status != nil {
		if lastTime, ok := status["lastScheduleTime"].(string); ok {
			resp.LastBackup = lastTime
		}
	}

	return resp
}

func backupImage() string {
	if img := os.Getenv("BACKUP_IMAGE"); img != "" {
		return img
	}
	return "panel-backup:latest"
}
