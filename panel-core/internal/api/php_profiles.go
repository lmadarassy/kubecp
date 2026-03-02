package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const phpProfileLabel = "hosting.panel/php-profile"

// PHPProfileRequest is the JSON body for creating/updating a PHP config profile.
type PHPProfileRequest struct {
	Name   string            `json:"name"`
	Config map[string]string `json:"config"` // php.ini key-value overrides
}

// PHPProfileResponse is the JSON response for PHP config profile endpoints.
type PHPProfileResponse struct {
	Name      string            `json:"name"`
	Config    map[string]string `json:"config"`
	CreatedAt string            `json:"createdAt,omitempty"`
}

// PHPProfileHandler implements PHP config profile management.
type PHPProfileHandler struct {
	clientset kubernetes.Interface
}

func NewPHPProfileHandler(clientset kubernetes.Interface) *PHPProfileHandler {
	return &PHPProfileHandler{clientset: clientset}
}

func (h *PHPProfileHandler) RegisterRoutes(r chi.Router) {
	r.Get("/", h.ListProfiles)
	r.Post("/", h.CreateProfile)
	r.Get("/{name}", h.GetProfile)
	r.Put("/{name}", h.UpdateProfile)
	r.Delete("/{name}", h.DeleteProfile)
}

func (h *PHPProfileHandler) ListProfiles(w http.ResponseWriter, r *http.Request) {
	cms, err := h.clientset.CoreV1().ConfigMaps(hostingNamespace).List(r.Context(), metav1.ListOptions{
		LabelSelector: phpProfileLabel + "=true",
	})
	if err != nil {
		WriteInternalError(w, "Failed to list profiles: "+err.Error())
		return
	}

	profiles := make([]PHPProfileResponse, 0, len(cms.Items))
	for _, cm := range cms.Items {
		profiles = append(profiles, configMapToProfile(&cm))
	}
	writeJSON(w, http.StatusOK, profiles)
}

func (h *PHPProfileHandler) CreateProfile(w http.ResponseWriter, r *http.Request) {
	var req PHPProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteBadRequest(w, "Invalid request body", nil)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		WriteBadRequest(w, "Profile name is required", nil)
		return
	}

	cmName := fmt.Sprintf("php-profile-%s", req.Name)
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cmName,
			Namespace: hostingNamespace,
			Labels: map[string]string{
				phpProfileLabel:      "true",
				"hosting.panel/name": req.Name,
			},
		},
		Data: req.Config,
	}

	created, err := h.clientset.CoreV1().ConfigMaps(hostingNamespace).Create(r.Context(), cm, metav1.CreateOptions{})
	if err != nil {
		if k8serrors.IsAlreadyExists(err) {
			WriteConflict(w, "Profile already exists", nil)
			return
		}
		WriteInternalError(w, "Failed to create profile: "+err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, configMapToProfile(created))
}

func (h *PHPProfileHandler) GetProfile(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	cmName := fmt.Sprintf("php-profile-%s", name)

	cm, err := h.clientset.CoreV1().ConfigMaps(hostingNamespace).Get(r.Context(), cmName, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			WriteNotFound(w, "Profile not found")
			return
		}
		WriteInternalError(w, "Failed to get profile: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, configMapToProfile(cm))
}

func (h *PHPProfileHandler) UpdateProfile(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	cmName := fmt.Sprintf("php-profile-%s", name)

	var req PHPProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteBadRequest(w, "Invalid request body", nil)
		return
	}

	cm, err := h.clientset.CoreV1().ConfigMaps(hostingNamespace).Get(r.Context(), cmName, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			WriteNotFound(w, "Profile not found")
			return
		}
		WriteInternalError(w, "Failed to get profile: "+err.Error())
		return
	}

	cm.Data = req.Config
	updated, err := h.clientset.CoreV1().ConfigMaps(hostingNamespace).Update(r.Context(), cm, metav1.UpdateOptions{})
	if err != nil {
		WriteInternalError(w, "Failed to update profile: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, configMapToProfile(updated))
}

func (h *PHPProfileHandler) DeleteProfile(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if name == "default" {
		WriteBadRequest(w, "Cannot delete the default profile", nil)
		return
	}
	cmName := fmt.Sprintf("php-profile-%s", name)

	err := h.clientset.CoreV1().ConfigMaps(hostingNamespace).Delete(r.Context(), cmName, metav1.DeleteOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			WriteNotFound(w, "Profile not found")
			return
		}
		WriteInternalError(w, "Failed to delete profile: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func configMapToProfile(cm *corev1.ConfigMap) PHPProfileResponse {
	name := cm.Labels["hosting.panel/name"]
	if name == "" {
		name = strings.TrimPrefix(cm.Name, "php-profile-")
	}
	return PHPProfileResponse{
		Name:      name,
		Config:    cm.Data,
		CreatedAt: cm.CreationTimestamp.Format("2006-01-02T15:04:05Z"),
	}
}
