package api

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/hosting-panel/panel-core/internal/middleware"
)

// LogEntry represents a single log line from a component.
type LogEntry struct {
	Timestamp string `json:"timestamp"`
	Component string `json:"component"`
	Severity  string `json:"severity"`
	Message   string `json:"message"`
}

type MonitoringHandler struct {
	clientset     kubernetes.Interface
	prometheusURL string // internal Prometheus URL for proxying
}

func NewMonitoringHandler(clientset kubernetes.Interface, prometheusURL string) *MonitoringHandler {
	return &MonitoringHandler{clientset: clientset, prometheusURL: prometheusURL}
}

func (h *MonitoringHandler) RegisterRoutes(r chi.Router) {
	r.Get("/metrics/proxy", h.ProxyMetrics)
	r.Get("/logs", h.GetLogs)
	r.Get("/logs/{component}", h.GetComponentLogs)
}

// Known hosting platform components and their label selectors.
var componentSelectors = map[string]string{
	"panel":    "app.kubernetes.io/name=panel-core",
	"operator": "app.kubernetes.io/name=hosting-operator",
	"postfix":  "app=postfix",
	"dovecot":  "app=dovecot",
	"powerdns": "app=powerdns",
	"rspamd":   "app=rspamd",
	"clamav":   "app=clamav",
	"sftp":     "app=sftp-server",
	"envoy":    "app=envoy",
}

// ProxyMetrics handles GET /api/monitoring/metrics/proxy — proxies to Prometheus.
func (h *MonitoringHandler) ProxyMetrics(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil || !middleware.HasRole(claims, "admin") {
		WriteForbidden(w, "admin access required")
		return
	}

	if h.prometheusURL == "" {
		WriteServiceUnavailable(w, "Prometheus not configured")
		return
	}

	// Proxy the query to Prometheus
	query := r.URL.Query().Get("query")
	if query == "" {
		WriteBadRequest(w, "query parameter required", nil)
		return
	}

	promURL := fmt.Sprintf("%s/api/v1/query?query=%s", h.prometheusURL, query)
	resp, err := http.Get(promURL)
	if err != nil {
		WriteServiceUnavailable(w, "Prometheus unavailable: "+err.Error())
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// GetLogs handles GET /api/monitoring/logs — aggregated logs from all components.
func (h *MonitoringHandler) GetLogs(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil || !middleware.HasRole(claims, "admin") {
		WriteForbidden(w, "admin access required")
		return
	}

	severity := r.URL.Query().Get("severity")
	tailLines := parseTailLines(r.URL.Query().Get("lines"), 100)
	sinceStr := r.URL.Query().Get("since")

	allLogs := make([]LogEntry, 0)
	for component, selector := range componentSelectors {
		entries, err := h.fetchPodLogs(r, selector, component, tailLines, sinceStr)
		if err != nil {
			continue // skip unavailable components
		}
		if severity != "" {
			for _, e := range entries {
				if strings.EqualFold(e.Severity, severity) {
					allLogs = append(allLogs, e)
				}
			}
		} else {
			allLogs = append(allLogs, entries...)
		}
	}

	writeJSON(w, http.StatusOK, allLogs)
}

// GetComponentLogs handles GET /api/monitoring/logs/{component}
func (h *MonitoringHandler) GetComponentLogs(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil || !middleware.HasRole(claims, "admin") {
		WriteForbidden(w, "admin access required")
		return
	}

	component := chi.URLParam(r, "component")
	selector, ok := componentSelectors[component]
	if !ok {
		WriteBadRequest(w, "unknown component: "+component, map[string]interface{}{
			"validComponents": componentNames(),
		})
		return
	}

	severity := r.URL.Query().Get("severity")
	tailLines := parseTailLines(r.URL.Query().Get("lines"), 200)
	sinceStr := r.URL.Query().Get("since")

	entries, err := h.fetchPodLogs(r, selector, component, tailLines, sinceStr)
	if err != nil {
		WriteInternalError(w, "Failed to fetch logs: "+err.Error())
		return
	}

	if severity != "" {
		filtered := make([]LogEntry, 0)
		for _, e := range entries {
			if strings.EqualFold(e.Severity, severity) {
				filtered = append(filtered, e)
			}
		}
		entries = filtered
	}

	writeJSON(w, http.StatusOK, entries)
}

// fetchPodLogs retrieves logs from pods matching the given label selector.
func (h *MonitoringHandler) fetchPodLogs(r *http.Request, labelSelector, component string, tailLines int64, sinceStr string) ([]LogEntry, error) {
	pods, err := h.clientset.CoreV1().Pods(hostingNamespace).List(r.Context(), metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil || len(pods.Items) == 0 {
		return nil, fmt.Errorf("no pods found for %s", component)
	}

	opts := &corev1.PodLogOptions{
		TailLines: &tailLines,
	}
	if sinceStr != "" {
		if d, err := time.ParseDuration(sinceStr); err == nil {
			sinceTime := metav1.NewTime(time.Now().Add(-d))
			opts.SinceTime = &sinceTime
		}
	}

	entries := make([]LogEntry, 0)
	for _, pod := range pods.Items {
		stream, err := h.clientset.CoreV1().Pods(hostingNamespace).GetLogs(pod.Name, opts).Stream(r.Context())
		if err != nil {
			continue
		}
		body, err := io.ReadAll(stream)
		stream.Close()
		if err != nil {
			continue
		}

		for _, line := range strings.Split(string(body), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			entries = append(entries, parseLogLine(line, component))
		}
	}

	return entries, nil
}

// parseLogLine attempts to extract timestamp and severity from a log line.
func parseLogLine(line, component string) LogEntry {
	entry := LogEntry{
		Component: component,
		Severity:  "info",
		Message:   line,
	}

	// Try to extract timestamp from beginning of line
	if len(line) > 20 {
		// Common format: 2024-01-15T10:30:00Z ...
		if _, err := time.Parse(time.RFC3339, line[:20]); err == nil {
			entry.Timestamp = line[:20]
			entry.Message = strings.TrimSpace(line[20:])
		}
	}

	// Detect severity from common patterns
	lower := strings.ToLower(line)
	switch {
	case strings.Contains(lower, "error") || strings.Contains(lower, "err"):
		entry.Severity = "error"
	case strings.Contains(lower, "warn"):
		entry.Severity = "warning"
	case strings.Contains(lower, "debug"):
		entry.Severity = "debug"
	case strings.Contains(lower, "fatal") || strings.Contains(lower, "panic"):
		entry.Severity = "critical"
	}

	return entry
}

func parseTailLines(s string, defaultVal int64) int64 {
	if s == "" {
		return defaultVal
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return defaultVal
	}
	return n
}

func componentNames() []string {
	names := make([]string, 0, len(componentSelectors))
	for k := range componentSelectors {
		names = append(names, k)
	}
	return names
}
