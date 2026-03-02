package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// newBackupFakeDynClient creates a fake dynamic client with Job, CronJob, and hosting CRD types.
func newBackupFakeDynClient(objects ...runtime.Object) *dynamicfake.FakeDynamicClient {
	scheme := runtime.NewScheme()
	// batch/v1 Job
	scheme.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"},
		&unstructured.Unstructured{},
	)
	scheme.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "JobList"},
		&unstructured.UnstructuredList{},
	)
	// batch/v1 CronJob
	scheme.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "CronJob"},
		&unstructured.Unstructured{},
	)
	scheme.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "CronJobList"},
		&unstructured.UnstructuredList{},
	)
	// Hosting CRDs for import tests
	scheme.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: CRDGroup, Version: CRDVersion, Kind: "Website"},
		&unstructured.Unstructured{},
	)
	scheme.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: CRDGroup, Version: CRDVersion, Kind: "WebsiteList"},
		&unstructured.UnstructuredList{},
	)
	scheme.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: CRDGroup, Version: CRDVersion, Kind: "Database"},
		&unstructured.Unstructured{},
	)
	scheme.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: CRDGroup, Version: CRDVersion, Kind: "DatabaseList"},
		&unstructured.UnstructuredList{},
	)
	scheme.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: CRDGroup, Version: CRDVersion, Kind: "EmailAccount"},
		&unstructured.Unstructured{},
	)
	scheme.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: CRDGroup, Version: CRDVersion, Kind: "EmailAccountList"},
		&unstructured.UnstructuredList{},
	)
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{
			JobGVR:          "JobList",
			CronJobGVR:      "CronJobList",
			WebsiteGVR:      "WebsiteList",
			DatabaseGVR:     "DatabaseList",
			EmailAccountGVR: "EmailAccountList",
		},
		objects...,
	)
}

func setupBackupRouter(handler *BackupHandler) *chi.Mux {
	r := chi.NewRouter()
	r.Route("/api/backups", func(r chi.Router) {
		handler.RegisterRoutes(r)
	})
	return r
}

func makeBackupJob(name, namespace, username, status string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "batch/v1",
			"kind":       "Job",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
				"labels": map[string]interface{}{
					"hosting.panel/type": "backup",
					"hosting.panel/user": username,
				},
				"annotations": map[string]interface{}{
					"hosting.panel/target":     `{"type":"local","path":"/backups"}`,
					"hosting.panel/components": "web,db,mail,dns,config",
				},
			},
			"spec": map[string]interface{}{},
		},
	}
	switch status {
	case "Completed":
		obj.Object["status"] = map[string]interface{}{"succeeded": int64(1), "completionTime": "2026-02-24T12:00:00Z"}
	case "Failed":
		obj.Object["status"] = map[string]interface{}{"failed": int64(1)}
	case "Running":
		obj.Object["status"] = map[string]interface{}{"active": int64(1)}
	}
	return obj
}

func makeCronJobObj(name, namespace, username, schedule string) *unstructured.Unstructured {
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
				},
				"annotations": map[string]interface{}{
					"hosting.panel/target":    `{"type":"local","path":"/backups"}`,
					"hosting.panel/retention": "7",
				},
			},
			"spec": map[string]interface{}{
				"schedule": schedule,
			},
		},
	}
}


// --- Backup Tests ---

func TestListBackups_AdminSeesAll(t *testing.T) {
	job1 := makeBackupJob("backup-user1-1", "hosting-user-user1", "user1", "Completed")
	job2 := makeBackupJob("backup-user2-1", "hosting-user-user2", "user2", "Running")

	dynClient := newBackupFakeDynClient(job1, job2)
	handler := NewBackupHandler(dynClient)
	router := setupBackupRouter(handler)

	req := httptest.NewRequest(http.MethodGet, "/api/backups", nil)
	req = withClaims(req, adminClaims())
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var backups []BackupResponse
	if err := json.NewDecoder(w.Body).Decode(&backups); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if len(backups) != 2 {
		t.Errorf("got %d backups, want 2", len(backups))
	}
}

func TestListBackups_UserSeesOwn(t *testing.T) {
	job1 := makeBackupJob("backup-testuser-1", "hosting-user-testuser", "testuser", "Completed")
	job2 := makeBackupJob("backup-other-1", "hosting-user-other", "other", "Running")

	dynClient := newBackupFakeDynClient(job1, job2)
	handler := NewBackupHandler(dynClient)
	router := setupBackupRouter(handler)

	req := httptest.NewRequest(http.MethodGet, "/api/backups", nil)
	req = withClaims(req, userClaims("testuser"))
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var backups []BackupResponse
	if err := json.NewDecoder(w.Body).Decode(&backups); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if len(backups) != 1 {
		t.Fatalf("got %d backups, want 1", len(backups))
	}
	if backups[0].ID != "backup-testuser-1" {
		t.Errorf("backup id = %q, want %q", backups[0].ID, "backup-testuser-1")
	}
}

func TestListBackups_EmptyList(t *testing.T) {
	dynClient := newBackupFakeDynClient()
	handler := NewBackupHandler(dynClient)
	router := setupBackupRouter(handler)

	req := httptest.NewRequest(http.MethodGet, "/api/backups", nil)
	req = withClaims(req, userClaims("testuser"))
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var backups []BackupResponse
	if err := json.NewDecoder(w.Body).Decode(&backups); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if len(backups) != 0 {
		t.Errorf("got %d backups, want 0", len(backups))
	}
}

func TestCreateBackup_Success(t *testing.T) {
	dynClient := newBackupFakeDynClient()
	handler := NewBackupHandler(dynClient)
	router := setupBackupRouter(handler)

	body := CreateBackupRequest{
		Target:     BackupTarget{Type: "local", Path: "/backups"},
		Components: []string{"web", "db"},
	}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/backups", bytes.NewReader(bodyBytes))
	req = withClaims(req, userClaims("testuser"))
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusCreated, w.Body.String())
	}

	var resp BackupResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if resp.Status != "Pending" {
		t.Errorf("status = %q, want %q", resp.Status, "Pending")
	}
	if resp.Namespace != "hosting-user-testuser" {
		t.Errorf("namespace = %q, want %q", resp.Namespace, "hosting-user-testuser")
	}
	if len(resp.Components) != 2 {
		t.Errorf("components = %v, want [web db]", resp.Components)
	}
}

func TestCreateBackup_DefaultComponents(t *testing.T) {
	dynClient := newBackupFakeDynClient()
	handler := NewBackupHandler(dynClient)
	router := setupBackupRouter(handler)

	body := CreateBackupRequest{
		Target: BackupTarget{Type: "local", Path: "/backups"},
	}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/backups", bytes.NewReader(bodyBytes))
	req = withClaims(req, userClaims("testuser"))
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusCreated, w.Body.String())
	}

	var resp BackupResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if len(resp.Components) != 5 {
		t.Errorf("components = %v, want all 5 defaults", resp.Components)
	}
}

func TestCreateBackup_InvalidTarget(t *testing.T) {
	dynClient := newBackupFakeDynClient()
	handler := NewBackupHandler(dynClient)
	router := setupBackupRouter(handler)

	body := CreateBackupRequest{
		Target: BackupTarget{Type: "invalid"},
	}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/backups", bytes.NewReader(bodyBytes))
	req = withClaims(req, userClaims("testuser"))
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestCreateBackup_S3MissingBucket(t *testing.T) {
	dynClient := newBackupFakeDynClient()
	handler := NewBackupHandler(dynClient)
	router := setupBackupRouter(handler)

	body := CreateBackupRequest{
		Target: BackupTarget{Type: "s3", Endpoint: "https://s3.example.com"},
	}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/backups", bytes.NewReader(bodyBytes))
	req = withClaims(req, userClaims("testuser"))
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestCreateBackup_InvalidComponents(t *testing.T) {
	dynClient := newBackupFakeDynClient()
	handler := NewBackupHandler(dynClient)
	router := setupBackupRouter(handler)

	body := CreateBackupRequest{
		Target:     BackupTarget{Type: "local", Path: "/backups"},
		Components: []string{"web", "invalid"},
	}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/backups", bytes.NewReader(bodyBytes))
	req = withClaims(req, userClaims("testuser"))
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestRestoreBackup_Success(t *testing.T) {
	completedJob := makeBackupJob("backup-testuser-1", "hosting-user-testuser", "testuser", "Completed")
	dynClient := newBackupFakeDynClient(completedJob)
	handler := NewBackupHandler(dynClient)
	router := setupBackupRouter(handler)

	req := httptest.NewRequest(http.MethodPost, "/api/backups/backup-testuser-1/restore", nil)
	req.ContentLength = 0
	req = withClaims(req, userClaims("testuser"))
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusAccepted, w.Body.String())
	}

	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if resp["status"] != "Pending" {
		t.Errorf("status = %q, want %q", resp["status"], "Pending")
	}
	if resp["backupId"] != "backup-testuser-1" {
		t.Errorf("backupId = %q, want %q", resp["backupId"], "backup-testuser-1")
	}
}

func TestRestoreBackup_NotFound(t *testing.T) {
	dynClient := newBackupFakeDynClient()
	handler := NewBackupHandler(dynClient)
	router := setupBackupRouter(handler)

	req := httptest.NewRequest(http.MethodPost, "/api/backups/nonexistent/restore", nil)
	req.ContentLength = 0
	req = withClaims(req, userClaims("testuser"))
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestRestoreBackup_NotCompleted(t *testing.T) {
	runningJob := makeBackupJob("backup-testuser-1", "hosting-user-testuser", "testuser", "Running")
	dynClient := newBackupFakeDynClient(runningJob)
	handler := NewBackupHandler(dynClient)
	router := setupBackupRouter(handler)

	req := httptest.NewRequest(http.MethodPost, "/api/backups/backup-testuser-1/restore", nil)
	req.ContentLength = 0
	req = withClaims(req, userClaims("testuser"))
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d, body: %s", w.Code, http.StatusBadRequest, w.Body.String())
	}
}

func TestRestoreBackup_SelectiveComponents(t *testing.T) {
	completedJob := makeBackupJob("backup-testuser-1", "hosting-user-testuser", "testuser", "Completed")
	dynClient := newBackupFakeDynClient(completedJob)
	handler := NewBackupHandler(dynClient)
	router := setupBackupRouter(handler)

	body := RestoreRequest{Components: []string{"db", "mail"}}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/backups/backup-testuser-1/restore", bytes.NewReader(bodyBytes))
	req = withClaims(req, userClaims("testuser"))
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusAccepted, w.Body.String())
	}
}

func TestListSchedules_Success(t *testing.T) {
	cj1 := makeCronJobObj("backup-schedule-user1", "hosting-user-user1", "user1", "0 2 * * *")
	cj2 := makeCronJobObj("backup-schedule-user2", "hosting-user-user2", "user2", "0 3 * * *")

	dynClient := newBackupFakeDynClient(cj1, cj2)
	handler := NewBackupHandler(dynClient)
	router := setupBackupRouter(handler)

	req := httptest.NewRequest(http.MethodGet, "/api/backups/schedules", nil)
	req = withClaims(req, adminClaims())
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var schedules []ScheduleResponse
	if err := json.NewDecoder(w.Body).Decode(&schedules); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if len(schedules) != 2 {
		t.Errorf("got %d schedules, want 2", len(schedules))
	}
}

func TestListSchedules_UserSeesOwn(t *testing.T) {
	cj1 := makeCronJobObj("backup-schedule-testuser", "hosting-user-testuser", "testuser", "0 2 * * *")
	cj2 := makeCronJobObj("backup-schedule-other", "hosting-user-other", "other", "0 3 * * *")

	dynClient := newBackupFakeDynClient(cj1, cj2)
	handler := NewBackupHandler(dynClient)
	router := setupBackupRouter(handler)

	req := httptest.NewRequest(http.MethodGet, "/api/backups/schedules", nil)
	req = withClaims(req, userClaims("testuser"))
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var schedules []ScheduleResponse
	if err := json.NewDecoder(w.Body).Decode(&schedules); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if len(schedules) != 1 {
		t.Fatalf("got %d schedules, want 1", len(schedules))
	}
	if schedules[0].Schedule != "0 2 * * *" {
		t.Errorf("schedule = %q, want %q", schedules[0].Schedule, "0 2 * * *")
	}
}

func TestCreateSchedule_Success(t *testing.T) {
	dynClient := newBackupFakeDynClient()
	handler := NewBackupHandler(dynClient)
	router := setupBackupRouter(handler)

	body := CreateScheduleRequest{
		Schedule:       "0 2 * * *",
		Target:         BackupTarget{Type: "local", Path: "/backups"},
		RetentionCount: 7,
	}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/backups/schedules", bytes.NewReader(bodyBytes))
	req = withClaims(req, adminClaims())
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusCreated, w.Body.String())
	}

	var resp ScheduleResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if resp.Schedule != "0 2 * * *" {
		t.Errorf("schedule = %q, want %q", resp.Schedule, "0 2 * * *")
	}
	if resp.RetentionCount != 7 {
		t.Errorf("retention = %d, want %d", resp.RetentionCount, 7)
	}
}

func TestCreateSchedule_AdminOnly(t *testing.T) {
	dynClient := newBackupFakeDynClient()
	handler := NewBackupHandler(dynClient)
	router := setupBackupRouter(handler)

	body := CreateScheduleRequest{
		Schedule: "0 2 * * *",
		Target:   BackupTarget{Type: "local", Path: "/backups"},
	}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/backups/schedules", bytes.NewReader(bodyBytes))
	req = withClaims(req, userClaims("testuser"))
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
}

func TestCreateSchedule_MissingSchedule(t *testing.T) {
	dynClient := newBackupFakeDynClient()
	handler := NewBackupHandler(dynClient)
	router := setupBackupRouter(handler)

	body := CreateScheduleRequest{
		Target: BackupTarget{Type: "local", Path: "/backups"},
	}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/backups/schedules", bytes.NewReader(bodyBytes))
	req = withClaims(req, adminClaims())
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestDeleteSchedule_Success(t *testing.T) {
	cj := makeCronJobObj("backup-schedule-testuser", "hosting-user-testuser", "testuser", "0 2 * * *")
	dynClient := newBackupFakeDynClient(cj)
	handler := NewBackupHandler(dynClient)
	router := setupBackupRouter(handler)

	req := httptest.NewRequest(http.MethodDelete, "/api/backups/schedules/backup-schedule-testuser", nil)
	req = withClaims(req, adminClaims())
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusNoContent, w.Body.String())
	}
}

func TestDeleteSchedule_NotFound(t *testing.T) {
	dynClient := newBackupFakeDynClient()
	handler := NewBackupHandler(dynClient)
	router := setupBackupRouter(handler)

	req := httptest.NewRequest(http.MethodDelete, "/api/backups/schedules/backup-schedule-nonexistent", nil)
	req = withClaims(req, adminClaims())
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestDeleteSchedule_AdminOnly(t *testing.T) {
	cj := makeCronJobObj("backup-schedule-testuser", "hosting-user-testuser", "testuser", "0 2 * * *")
	dynClient := newBackupFakeDynClient(cj)
	handler := NewBackupHandler(dynClient)
	router := setupBackupRouter(handler)

	req := httptest.NewRequest(http.MethodDelete, "/api/backups/schedules/backup-schedule-testuser", nil)
	req = withClaims(req, userClaims("testuser"))
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
}

func TestBackupJobStatus_Completed(t *testing.T) {
	job := makeBackupJob("backup-1", "hosting-user-testuser", "testuser", "Completed")
	resp := jobToBackupResponse(job)
	if resp.Status != "Completed" {
		t.Errorf("status = %q, want %q", resp.Status, "Completed")
	}
}

func TestBackupJobStatus_Failed(t *testing.T) {
	job := makeBackupJob("backup-1", "hosting-user-testuser", "testuser", "Failed")
	resp := jobToBackupResponse(job)
	if resp.Status != "Failed" {
		t.Errorf("status = %q, want %q", resp.Status, "Failed")
	}
}

func TestBackupJobStatus_Running(t *testing.T) {
	job := makeBackupJob("backup-1", "hosting-user-testuser", "testuser", "Running")
	resp := jobToBackupResponse(job)
	if resp.Status != "Running" {
		t.Errorf("status = %q, want %q", resp.Status, "Running")
	}
}

func TestBackupJobStatus_Pending(t *testing.T) {
	job := makeBackupJob("backup-1", "hosting-user-testuser", "testuser", "")
	resp := jobToBackupResponse(job)
	if resp.Status != "Pending" {
		t.Errorf("status = %q, want %q", resp.Status, "Pending")
	}
}
