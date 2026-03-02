package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/hosting-panel/panel-core/internal/middleware"
)

const (
	settingsConfigMapName = "hosting-panel-settings"
	timezoneKey           = "timezone"
)

// TimezoneRequest is the JSON body for setting the timezone.
type TimezoneRequest struct {
	Timezone string `json:"timezone"` // IANA timezone, e.g. "Europe/Budapest"
}

// VersionResponse is the JSON response for the version endpoint.
type VersionResponse struct {
	PlatformVersion  string            `json:"platformVersion"`
	HelmChartVersion string            `json:"helmChartVersion"`
	Components       map[string]string `json:"components,omitempty"`
}

type SettingsHandler struct {
	clientset        kubernetes.Interface
	platformVersion  string
	helmChartVersion string
}

func NewSettingsHandler(clientset kubernetes.Interface, platformVersion, helmChartVersion string) *SettingsHandler {
	return &SettingsHandler{
		clientset:        clientset,
		platformVersion:  platformVersion,
		helmChartVersion: helmChartVersion,
	}
}

func (h *SettingsHandler) RegisterRoutes(r chi.Router) {
	r.Get("/timezone", h.GetTimezone)
	r.Put("/timezone", h.SetTimezone)
	r.Get("/version", h.GetVersion)
}

// GetTimezone handles GET /api/settings/timezone
func (h *SettingsHandler) GetTimezone(w http.ResponseWriter, r *http.Request) {
	cm, err := h.clientset.CoreV1().ConfigMaps(hostingNamespace).Get(r.Context(), settingsConfigMapName, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			writeJSON(w, http.StatusOK, map[string]string{"timezone": "UTC"})
			return
		}
		WriteInternalError(w, "Failed to get settings: "+err.Error())
		return
	}

	tz := cm.Data[timezoneKey]
	if tz == "" {
		tz = "UTC"
	}

	writeJSON(w, http.StatusOK, map[string]string{"timezone": tz})
}

// SetTimezone handles PUT /api/settings/timezone
func (h *SettingsHandler) SetTimezone(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil || !middleware.HasRole(claims, "admin") {
		WriteForbidden(w, "admin access required")
		return
	}

	var req TimezoneRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteBadRequest(w, "Invalid request body", nil)
		return
	}

	// Validate timezone
	if _, err := time.LoadLocation(req.Timezone); err != nil {
		WriteBadRequest(w, "Invalid timezone: "+req.Timezone, nil)
		return
	}

	cm, err := h.clientset.CoreV1().ConfigMaps(hostingNamespace).Get(r.Context(), settingsConfigMapName, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			// Create the settings ConfigMap
			cm = &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      settingsConfigMapName,
					Namespace: hostingNamespace,
				},
				Data: map[string]string{timezoneKey: req.Timezone},
			}
			_, err = h.clientset.CoreV1().ConfigMaps(hostingNamespace).Create(r.Context(), cm, metav1.CreateOptions{})
			if err != nil {
				WriteInternalError(w, "Failed to create settings: "+err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"timezone": req.Timezone})
			return
		}
		WriteInternalError(w, "Failed to get settings: "+err.Error())
		return
	}

	if cm.Data == nil {
		cm.Data = make(map[string]string)
	}
	cm.Data[timezoneKey] = req.Timezone

	_, err = h.clientset.CoreV1().ConfigMaps(hostingNamespace).Update(r.Context(), cm, metav1.UpdateOptions{})
	if err != nil {
		WriteInternalError(w, "Failed to update settings: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"timezone": req.Timezone})
}

// GetVersion handles GET /api/settings/version
func (h *SettingsHandler) GetVersion(w http.ResponseWriter, r *http.Request) {
	resp := VersionResponse{
		PlatformVersion:  h.platformVersion,
		HelmChartVersion: h.helmChartVersion,
		Components: map[string]string{
			"panel":    h.platformVersion,
			"operator": h.platformVersion,
		},
	}

	writeJSON(w, http.StatusOK, resp)
}
