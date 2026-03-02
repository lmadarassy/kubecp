package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"

	"github.com/hosting-panel/panel-core/internal/middleware"
)

// FileManagerHandler implements the file manager API endpoints.
// Files are accessed via kubectl exec into the website pod.
type FileManagerHandler struct {
	dynClient  dynamic.Interface
	clientset  kubernetes.Interface
	restConfig *rest.Config
}

// FileEntry represents a file or directory in a listing.
type FileEntry struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	IsDir       bool   `json:"isDir"`
	Size        int64  `json:"size"`
	Permissions string `json:"permissions,omitempty"`
	ModTime     string `json:"modTime,omitempty"`
}

// FileManagerRequest is a generic request for file operations.
type FileManagerRequest struct {
	Path    string `json:"path"`
	Content string `json:"content,omitempty"`
	NewPath string `json:"newPath,omitempty"`
	Mode    string `json:"mode,omitempty"`
}

func NewFileManagerHandler(dynClient dynamic.Interface, clientset kubernetes.Interface, rc *rest.Config) *FileManagerHandler {
	return &FileManagerHandler{dynClient: dynClient, clientset: clientset, restConfig: rc}
}

func (h *FileManagerHandler) RegisterRoutes(r chi.Router) {
	r.Get("/list", h.ListFiles)
	r.Get("/read", h.ReadFile)
	r.Post("/write", h.WriteFile)
	r.Post("/mkdir", h.MakeDir)
	r.Delete("/", h.DeleteFile)
	r.Post("/rename", h.RenameFile)
	r.Post("/copy", h.CopyFile)
	r.Post("/chmod", h.ChmodFile)
	r.Post("/upload", h.UploadFile)
	r.Get("/download", h.DownloadFile)
}

// resolveWebsitePod finds the pod name for a website by looking up the deployment.
func (h *FileManagerHandler) resolveWebsitePod(r *http.Request) (string, string, error) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		return "", "", fmt.Errorf("no claims")
	}

	websiteID := r.URL.Query().Get("websiteId")
	if websiteID == "" {
		return "", "", fmt.Errorf("websiteId query parameter required")
	}

	obj, err := h.dynClient.Resource(WebsiteGVR).Namespace(hostingNamespace).Get(r.Context(), websiteID, metav1.GetOptions{})
	if err != nil {
		return "", "", fmt.Errorf("website not found: %w", err)
	}
	if !middleware.IsOwnerByLabel(claims, obj) {
		return "", "", fmt.Errorf("access denied")
	}

	pods, err := h.clientset.CoreV1().Pods(hostingNamespace).List(r.Context(), metav1.ListOptions{
		LabelSelector: fmt.Sprintf("hosting.panel/website=%s", websiteID),
	})
	if err != nil || len(pods.Items) == 0 {
		return "", "", fmt.Errorf("no running pod found for website %s", websiteID)
	}

	return pods.Items[0].Name, hostingNamespace, nil
}

// sanitizePath prevents path traversal attacks.
func sanitizePath(path string) string {
	path = filepath.Clean(path)
	path = strings.TrimPrefix(path, "/")
	if strings.Contains(path, "..") {
		return ""
	}
	return path
}

// ListFiles handles GET /api/files/list?websiteId=X&path=/
func (h *FileManagerHandler) ListFiles(w http.ResponseWriter, r *http.Request) {
	podName, ns, err := h.resolveWebsitePod(r)
	if err != nil {
		WriteBadRequest(w, err.Error(), nil)
		return
	}

	path := sanitizePath(r.URL.Query().Get("path"))
	if path == "" {
		path = "."
	}

	output, err := h.execInPod(r, podName, ns, []string{"ls", "-la", "/var/www/html/" + path})
	if err != nil {
		WriteInternalError(w, "Failed to list files: "+err.Error())
		return
	}

	entries := parseLsOutput(output, path)
	writeJSON(w, http.StatusOK, entries)
}

// ReadFile handles GET /api/files/read?websiteId=X&path=index.html
func (h *FileManagerHandler) ReadFile(w http.ResponseWriter, r *http.Request) {
	podName, ns, err := h.resolveWebsitePod(r)
	if err != nil {
		WriteBadRequest(w, err.Error(), nil)
		return
	}

	path := sanitizePath(r.URL.Query().Get("path"))
	if path == "" {
		WriteBadRequest(w, "path is required", nil)
		return
	}

	output, err := h.execInPod(r, podName, ns, []string{"cat", "/var/www/html/" + path})
	if err != nil {
		WriteInternalError(w, "Failed to read file: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"path": path, "content": output})
}

// WriteFile handles POST /api/files/write
func (h *FileManagerHandler) WriteFile(w http.ResponseWriter, r *http.Request) {
	podName, ns, err := h.resolveWebsitePod(r)
	if err != nil {
		WriteBadRequest(w, err.Error(), nil)
		return
	}

	var req FileManagerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteBadRequest(w, "Invalid request body", nil)
		return
	}
	req.Path = sanitizePath(req.Path)
	if req.Path == "" {
		WriteBadRequest(w, "path is required", nil)
		return
	}

	_, err = h.execInPodWithStdin(r, podName, ns,
		[]string{"tee", "/var/www/html/" + req.Path}, req.Content)
	if err != nil {
		WriteInternalError(w, "Failed to write file: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "written", "path": req.Path})
}

// MakeDir handles POST /api/files/mkdir
func (h *FileManagerHandler) MakeDir(w http.ResponseWriter, r *http.Request) {
	podName, ns, err := h.resolveWebsitePod(r)
	if err != nil {
		WriteBadRequest(w, err.Error(), nil)
		return
	}

	var req FileManagerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteBadRequest(w, "Invalid request body", nil)
		return
	}
	req.Path = sanitizePath(req.Path)
	if req.Path == "" {
		WriteBadRequest(w, "path is required", nil)
		return
	}

	_, err = h.execInPod(r, podName, ns, []string{"mkdir", "-p", "/var/www/html/" + req.Path})
	if err != nil {
		WriteInternalError(w, "Failed to create directory: "+err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{"status": "created", "path": req.Path})
}

// DeleteFile handles DELETE /api/files/?websiteId=X&path=file.txt
func (h *FileManagerHandler) DeleteFile(w http.ResponseWriter, r *http.Request) {
	podName, ns, err := h.resolveWebsitePod(r)
	if err != nil {
		WriteBadRequest(w, err.Error(), nil)
		return
	}

	path := sanitizePath(r.URL.Query().Get("path"))
	if path == "" {
		WriteBadRequest(w, "path is required", nil)
		return
	}

	_, err = h.execInPod(r, podName, ns, []string{"rm", "-rf", "/var/www/html/" + path})
	if err != nil {
		WriteInternalError(w, "Failed to delete: "+err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// RenameFile handles POST /api/files/rename
func (h *FileManagerHandler) RenameFile(w http.ResponseWriter, r *http.Request) {
	podName, ns, err := h.resolveWebsitePod(r)
	if err != nil {
		WriteBadRequest(w, err.Error(), nil)
		return
	}

	var req FileManagerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteBadRequest(w, "Invalid request body", nil)
		return
	}
	req.Path = sanitizePath(req.Path)
	req.NewPath = sanitizePath(req.NewPath)
	if req.Path == "" || req.NewPath == "" {
		WriteBadRequest(w, "path and newPath are required", nil)
		return
	}

	_, err = h.execInPod(r, podName, ns, []string{"mv", "/var/www/html/" + req.Path, "/var/www/html/" + req.NewPath})
	if err != nil {
		WriteInternalError(w, "Failed to rename: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "renamed", "from": req.Path, "to": req.NewPath})
}

// CopyFile handles POST /api/files/copy
func (h *FileManagerHandler) CopyFile(w http.ResponseWriter, r *http.Request) {
	podName, ns, err := h.resolveWebsitePod(r)
	if err != nil {
		WriteBadRequest(w, err.Error(), nil)
		return
	}

	var req FileManagerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteBadRequest(w, "Invalid request body", nil)
		return
	}
	req.Path = sanitizePath(req.Path)
	req.NewPath = sanitizePath(req.NewPath)
	if req.Path == "" || req.NewPath == "" {
		WriteBadRequest(w, "path and newPath are required", nil)
		return
	}

	_, err = h.execInPod(r, podName, ns, []string{"cp", "-r", "/var/www/html/" + req.Path, "/var/www/html/" + req.NewPath})
	if err != nil {
		WriteInternalError(w, "Failed to copy: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "copied", "from": req.Path, "to": req.NewPath})
}

// ChmodFile handles POST /api/files/chmod
func (h *FileManagerHandler) ChmodFile(w http.ResponseWriter, r *http.Request) {
	podName, ns, err := h.resolveWebsitePod(r)
	if err != nil {
		WriteBadRequest(w, err.Error(), nil)
		return
	}

	var req FileManagerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteBadRequest(w, "Invalid request body", nil)
		return
	}
	req.Path = sanitizePath(req.Path)
	if req.Path == "" || req.Mode == "" {
		WriteBadRequest(w, "path and mode are required", nil)
		return
	}

	_, err = h.execInPod(r, podName, ns, []string{"chmod", req.Mode, "/var/www/html/" + req.Path})
	if err != nil {
		WriteInternalError(w, "Failed to chmod: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "changed", "path": req.Path, "mode": req.Mode})
}

// UploadFile handles POST /api/files/upload (multipart form)
func (h *FileManagerHandler) UploadFile(w http.ResponseWriter, r *http.Request) {
	podName, ns, err := h.resolveWebsitePod(r)
	if err != nil {
		WriteBadRequest(w, err.Error(), nil)
		return
	}

	// Max 50MB upload
	if err := r.ParseMultipartForm(50 << 20); err != nil {
		WriteBadRequest(w, "Failed to parse multipart form", nil)
		return
	}

	path := sanitizePath(r.FormValue("path"))
	if path == "" {
		path = "."
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		WriteBadRequest(w, "file field is required", nil)
		return
	}
	defer file.Close()

	content, err := io.ReadAll(file)
	if err != nil {
		WriteInternalError(w, "Failed to read uploaded file")
		return
	}

	targetPath := "/var/www/html/" + path + "/" + header.Filename
	_, err = h.execInPodWithStdin(r, podName, ns, []string{"tee", targetPath}, string(content))
	if err != nil {
		WriteInternalError(w, "Failed to upload file: "+err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{
		"status":   "uploaded",
		"filename": header.Filename,
		"path":     path + "/" + header.Filename,
	})
}

// DownloadFile handles GET /api/files/download?websiteId=X&path=file.txt
func (h *FileManagerHandler) DownloadFile(w http.ResponseWriter, r *http.Request) {
	podName, ns, err := h.resolveWebsitePod(r)
	if err != nil {
		WriteBadRequest(w, err.Error(), nil)
		return
	}

	path := sanitizePath(r.URL.Query().Get("path"))
	if path == "" {
		WriteBadRequest(w, "path is required", nil)
		return
	}

	output, err := h.execInPod(r, podName, ns, []string{"cat", "/var/www/html/" + path})
	if err != nil {
		WriteInternalError(w, "Failed to download file: "+err.Error())
		return
	}

	filename := filepath.Base(path)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Write([]byte(output))
}

// execInPod executes a command in a pod and returns stdout using SPDY exec.
func (h *FileManagerHandler) execInPod(r *http.Request, podName, namespace string, command []string) (string, error) {
	if h.restConfig == nil {
		return "", fmt.Errorf("rest config not available")
	}

	req := h.clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: "php-fpm",
			Command:   command,
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)

	exec, err := remotecommand.NewSPDYExecutor(h.restConfig, "POST", req.URL())
	if err != nil {
		return "", fmt.Errorf("failed to create executor: %w", err)
	}

	var stdout, stderr bytes.Buffer
	err = exec.StreamWithContext(r.Context(), remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	})
	if err != nil {
		return "", fmt.Errorf("exec error: %w (stderr: %s)", err, stderr.String())
	}
	return stdout.String(), nil
}

// execInPodWithStdin executes a command in a pod with stdin using SPDY exec.
func (h *FileManagerHandler) execInPodWithStdin(r *http.Request, podName, namespace string, command []string, stdin string) (string, error) {
	if h.restConfig == nil {
		return "", fmt.Errorf("rest config not available")
	}

	req := h.clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: "php-fpm",
			Command:   command,
			Stdin:     true,
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)

	exec, err := remotecommand.NewSPDYExecutor(h.restConfig, "POST", req.URL())
	if err != nil {
		return "", fmt.Errorf("failed to create executor: %w", err)
	}

	var stdout, stderr bytes.Buffer
	err = exec.StreamWithContext(r.Context(), remotecommand.StreamOptions{
		Stdin:  strings.NewReader(stdin),
		Stdout: &stdout,
		Stderr: &stderr,
	})
	if err != nil {
		return "", fmt.Errorf("exec error: %w (stderr: %s)", err, stderr.String())
	}
	return stdout.String(), nil
}

// parseLsOutput parses the output of `ls -la` (BusyBox compatible) into FileEntry structs.
func parseLsOutput(output, basePath string) []FileEntry {
	entries := make([]FileEntry, 0)
	lines := strings.Split(output, "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "total") {
			continue
		}

		fields := strings.Fields(line)
		// BusyBox ls -la: perms links user group size month day time/year name...
		// GNU ls -la:     perms links user group size month day time/year name...
		// Both have at least 9 fields
		if len(fields) < 9 {
			continue
		}

		name := strings.Join(fields[8:], " ")
		if name == "." || name == ".." {
			continue
		}

		isDir := strings.HasPrefix(fields[0], "d")
		var size int64
		fmt.Sscanf(fields[4], "%d", &size)

		modTime := fields[5] + " " + fields[6] + " " + fields[7]

		path := basePath
		if path == "." || path == "" {
			path = name
		} else {
			path = strings.TrimSuffix(path, "/") + "/" + name
		}

		entries = append(entries, FileEntry{
			Name:        name,
			Path:        path,
			IsDir:       isDir,
			Size:        size,
			Permissions: fields[0],
			ModTime:     modTime,
		})
	}

	return entries
}
