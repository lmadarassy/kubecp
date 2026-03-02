package api

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"

	"github.com/hosting-panel/panel-core/internal/middleware"
)

// FirewallRuleRequest is the JSON body for creating/updating a firewall rule.
type FirewallRuleRequest struct {
	Name       string `json:"name"`
	SourceCIDR string `json:"sourceCidr,omitempty"` // e.g. "0.0.0.0/0"
	Port       int32  `json:"port"`
	Protocol   string `json:"protocol"` // TCP or UDP
	Action     string `json:"action"`   // allow or deny
	Template   string `json:"template,omitempty"` // predefined template name
}

// FirewallRuleResponse is the JSON response for firewall rule endpoints.
type FirewallRuleResponse struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	SourceCIDR string `json:"sourceCidr,omitempty"`
	Port       int32  `json:"port"`
	Protocol   string `json:"protocol"`
	Action     string `json:"action"`
	CreatedAt  string `json:"createdAt,omitempty"`
}

// Predefined firewall rule templates.
var firewallTemplates = map[string]FirewallRuleRequest{
	"ssh":   {Name: "allow-ssh", Port: 22, Protocol: "TCP", Action: "allow"},
	"http":  {Name: "allow-http", Port: 80, Protocol: "TCP", Action: "allow"},
	"https": {Name: "allow-https", Port: 443, Protocol: "TCP", Action: "allow"},
	"mysql": {Name: "allow-mysql", Port: 3306, Protocol: "TCP", Action: "allow"},
	"smtp":  {Name: "allow-smtp", Port: 25, Protocol: "TCP", Action: "allow"},
	"imap":  {Name: "allow-imap", Port: 993, Protocol: "TCP", Action: "allow"},
	"pop3":  {Name: "allow-pop3", Port: 995, Protocol: "TCP", Action: "allow"},
}

type FirewallHandler struct {
	clientset kubernetes.Interface
}

func NewFirewallHandler(clientset kubernetes.Interface) *FirewallHandler {
	return &FirewallHandler{clientset: clientset}
}

func (h *FirewallHandler) RegisterRoutes(r chi.Router) {
	r.Get("/rules", h.ListRules)
	r.Post("/rules", h.CreateRule)
	r.Get("/rules/{id}", h.GetRule)
	r.Put("/rules/{id}", h.UpdateRule)
	r.Delete("/rules/{id}", h.DeleteRule)
	r.Get("/templates", h.ListTemplates)
}

const firewallLabelKey = "hosting.panel/firewall-rule"

// ListRules handles GET /api/firewall/rules
func (h *FirewallHandler) ListRules(w http.ResponseWriter, r *http.Request) {
	policies, err := h.clientset.NetworkingV1().NetworkPolicies(hostingNamespace).List(r.Context(), metav1.ListOptions{
		LabelSelector: firewallLabelKey + "=true",
	})
	if err != nil {
		WriteInternalError(w, "Failed to list firewall rules: "+err.Error())
		return
	}

	results := make([]FirewallRuleResponse, 0, len(policies.Items))
	for _, np := range policies.Items {
		results = append(results, networkPolicyToResponse(&np))
	}

	writeJSON(w, http.StatusOK, results)
}

// CreateRule handles POST /api/firewall/rules
func (h *FirewallHandler) CreateRule(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil || !middleware.HasRole(claims, "admin") {
		WriteForbidden(w, "admin access required")
		return
	}

	var req FirewallRuleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteBadRequest(w, "Invalid request body", nil)
		return
	}

	// Apply template if specified
	if req.Template != "" {
		tmpl, ok := firewallTemplates[req.Template]
		if !ok {
			WriteBadRequest(w, "unknown template: "+req.Template, nil)
			return
		}
		if req.Name == "" {
			req.Name = tmpl.Name
		}
		if req.Port == 0 {
			req.Port = tmpl.Port
		}
		if req.Protocol == "" {
			req.Protocol = tmpl.Protocol
		}
		if req.Action == "" {
			req.Action = tmpl.Action
		}
	}

	if req.Name == "" || req.Port == 0 || req.Protocol == "" {
		WriteBadRequest(w, "name, port, and protocol are required", nil)
		return
	}

	np := buildNetworkPolicy(req)
	created, err := h.clientset.NetworkingV1().NetworkPolicies(hostingNamespace).Create(r.Context(), np, metav1.CreateOptions{})
	if err != nil {
		if k8serrors.IsAlreadyExists(err) {
			WriteConflict(w, "firewall rule already exists", nil)
			return
		}
		WriteInternalError(w, "Failed to create firewall rule: "+err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, networkPolicyToResponse(created))
}

// GetRule handles GET /api/firewall/rules/{id}
func (h *FirewallHandler) GetRule(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	np, err := h.clientset.NetworkingV1().NetworkPolicies(hostingNamespace).Get(r.Context(), id, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			WriteNotFound(w, "firewall rule not found")
			return
		}
		WriteInternalError(w, "Failed to get firewall rule: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, networkPolicyToResponse(np))
}

// UpdateRule handles PUT /api/firewall/rules/{id}
func (h *FirewallHandler) UpdateRule(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil || !middleware.HasRole(claims, "admin") {
		WriteForbidden(w, "admin access required")
		return
	}

	id := chi.URLParam(r, "id")
	existing, err := h.clientset.NetworkingV1().NetworkPolicies(hostingNamespace).Get(r.Context(), id, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			WriteNotFound(w, "firewall rule not found")
			return
		}
		WriteInternalError(w, "Failed to get firewall rule: "+err.Error())
		return
	}

	var req FirewallRuleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteBadRequest(w, "Invalid request body", nil)
		return
	}

	// Rebuild the network policy spec
	updated := buildNetworkPolicy(req)
	updated.ObjectMeta = existing.ObjectMeta
	if req.Name != "" {
		updated.Labels["hosting.panel/rule-name"] = req.Name
	}

	result, err := h.clientset.NetworkingV1().NetworkPolicies(hostingNamespace).Update(r.Context(), updated, metav1.UpdateOptions{})
	if err != nil {
		WriteInternalError(w, "Failed to update firewall rule: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, networkPolicyToResponse(result))
}

// DeleteRule handles DELETE /api/firewall/rules/{id}
func (h *FirewallHandler) DeleteRule(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil || !middleware.HasRole(claims, "admin") {
		WriteForbidden(w, "admin access required")
		return
	}

	id := chi.URLParam(r, "id")
	if err := h.clientset.NetworkingV1().NetworkPolicies(hostingNamespace).Delete(r.Context(), id, metav1.DeleteOptions{}); err != nil {
		if k8serrors.IsNotFound(err) {
			WriteNotFound(w, "firewall rule not found")
			return
		}
		WriteInternalError(w, "Failed to delete firewall rule: "+err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ListTemplates handles GET /api/firewall/templates
func (h *FirewallHandler) ListTemplates(w http.ResponseWriter, r *http.Request) {
	templates := make([]map[string]interface{}, 0, len(firewallTemplates))
	for key, tmpl := range firewallTemplates {
		templates = append(templates, map[string]interface{}{
			"id":       key,
			"name":     tmpl.Name,
			"port":     tmpl.Port,
			"protocol": tmpl.Protocol,
		})
	}
	writeJSON(w, http.StatusOK, templates)
}

// buildNetworkPolicy creates a K8s NetworkPolicy from a firewall rule request.
func buildNetworkPolicy(req FirewallRuleRequest) *networkingv1.NetworkPolicy {
	protocol := corev1ProtocolFromString(req.Protocol)
	port := intstr.FromInt32(req.Port)

	np := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "fw-" + req.Name,
			Namespace: hostingNamespace,
			Labels: map[string]string{
				firewallLabelKey:          "true",
				"hosting.panel/rule-name": req.Name,
			},
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{
				{
					Ports: []networkingv1.NetworkPolicyPort{
						{
							Protocol: &protocol,
							Port:     &port,
						},
					},
				},
			},
		},
	}

	// Add source CIDR if specified
	if req.SourceCIDR != "" {
		np.Spec.Ingress[0].From = []networkingv1.NetworkPolicyPeer{
			{
				IPBlock: &networkingv1.IPBlock{
					CIDR: req.SourceCIDR,
				},
			},
		}
	}

	return np
}

func corev1ProtocolFromString(proto string) corev1.Protocol {
	switch proto {
	case "UDP":
		return corev1.ProtocolUDP
	case "SCTP":
		return corev1.ProtocolSCTP
	default:
		return corev1.ProtocolTCP
	}
}

func networkPolicyToResponse(np *networkingv1.NetworkPolicy) FirewallRuleResponse {
	resp := FirewallRuleResponse{
		ID:        np.Name, // K8s name (e.g. fw-test-rule)
		Name:      np.Labels["hosting.panel/rule-name"],
		CreatedAt: np.CreationTimestamp.Format("2006-01-02T15:04:05Z"),
	}

	if len(np.Spec.Ingress) > 0 {
		rule := np.Spec.Ingress[0]
		if len(rule.Ports) > 0 {
			if rule.Ports[0].Port != nil {
				resp.Port = rule.Ports[0].Port.IntVal
			}
			if rule.Ports[0].Protocol != nil {
				resp.Protocol = string(*rule.Ports[0].Protocol)
			}
		}
		if len(rule.From) > 0 && rule.From[0].IPBlock != nil {
			resp.SourceCIDR = rule.From[0].IPBlock.CIDR
		}
	}

	// Determine action from policy type
	resp.Action = "allow"

	return resp
}
