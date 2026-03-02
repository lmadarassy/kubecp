package api

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/hosting-panel/panel-core/internal/middleware"
)

var (
	// cert-manager Certificate CRD GVR
	CertificateGVR = schema.GroupVersionResource{
		Group:    "cert-manager.io",
		Version:  "v1",
		Resource: "certificates",
	}
)

// CertificateHandler implements certificate management API endpoints.
type CertificateHandler struct {
	dynClient      dynamic.Interface
	k8sClient      kubernetes.Interface
	clusterIssuer  string // default ClusterIssuer name
}

// NewCertificateHandler creates a new CertificateHandler.
func NewCertificateHandler(dynClient dynamic.Interface, k8sClient kubernetes.Interface, clusterIssuer string) *CertificateHandler {
	if clusterIssuer == "" {
		clusterIssuer = "letsencrypt-production"
	}
	return &CertificateHandler{
		dynClient:     dynClient,
		k8sClient:     k8sClient,
		clusterIssuer: clusterIssuer,
	}
}

// RegisterRoutes registers certificate management routes.
func (h *CertificateHandler) RegisterRoutes(r chi.Router) {
	r.Get("/", h.ListCertificates)
	r.Post("/upload", h.UploadCertificate)
	r.Post("/self-signed", h.GenerateSelfSigned)
	r.Put("/{name}", h.UpdateCertificate)
	r.Delete("/{name}", h.DeleteCertificate)
}

// --- Request/Response types ---

// CertificateResponse is the JSON response for certificate endpoints.
type CertificateResponse struct {
	Name       string `json:"name"`
	Namespace  string `json:"namespace"`
	Domain     string `json:"domain"`
	Status     string `json:"status"` // Valid, Expiring, Expired, Error, Pending
	Issuer     string `json:"issuer"`
	Mode       string `json:"mode"`   // letsencrypt, selfsigned, custom
	NotBefore  string `json:"notBefore,omitempty"`
	NotAfter   string `json:"notAfter,omitempty"`
	Message    string `json:"message,omitempty"`
}

// UploadCertificateRequest is the JSON body for POST /api/certificates/upload.
type UploadCertificateRequest struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Domain    string `json:"domain"`             // domain this cert is for
	CACert    string `json:"caCert,omitempty"`    // PEM-encoded CA certificate (optional)
	TLSCert   string `json:"tlsCert"`             // PEM-encoded server certificate
	TLSKey    string `json:"tlsKey"`              // PEM-encoded private key
}

// SelfSignedRequest is the JSON body for POST /api/certificates/self-signed.
type SelfSignedRequest struct {
	Domain    string `json:"domain"`
	Namespace string `json:"namespace"`
}

// --- Handlers ---

// ListCertificates handles GET /api/certificates.
func (h *CertificateHandler) ListCertificates(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	isAdmin := middleware.HasRole(claims, "admin")

	var allCerts []CertificateResponse

	secretGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "secrets"}

	if isAdmin {
		// Admin: list cert-manager Certificates across all hosting namespaces
		certs, err := h.dynClient.Resource(CertificateGVR).Namespace("").List(r.Context(), metav1.ListOptions{})
		if err == nil {
			for _, c := range certs.Items {
				allCerts = append(allCerts, certToResponse(&c))
			}
		}
		// cert-manager CRD may not exist — ignore errors

		// Also list uploaded TLS secrets (custom mode)
		secrets, err := h.dynClient.Resource(secretGVR).Namespace("").List(r.Context(), metav1.ListOptions{
			LabelSelector: "hosting.panel/type=tls-upload",
		})
		if err == nil {
			for _, s := range secrets.Items {
				allCerts = append(allCerts, secretToResponse(&s))
			}
		}

		// Also list self-signed TLS secrets
		selfSignedSecrets, err := h.dynClient.Resource(secretGVR).Namespace("").List(r.Context(), metav1.ListOptions{
			LabelSelector: "hosting.panel/type=tls-selfsigned",
		})
		if err == nil {
			for _, s := range selfSignedSecrets.Items {
				allCerts = append(allCerts, secretToResponse(&s))
			}
		}
	} else {
		// User: list only in own namespace
		ns := hostingNamespace
		certs, err := h.dynClient.Resource(CertificateGVR).Namespace(ns).List(r.Context(), metav1.ListOptions{})
		if err == nil {
			for _, c := range certs.Items {
				allCerts = append(allCerts, certToResponse(&c))
			}
		}
		// cert-manager CRD may not exist — ignore errors

		// Also list uploaded TLS secrets
		secrets, err := h.dynClient.Resource(secretGVR).Namespace(ns).List(r.Context(), metav1.ListOptions{
			LabelSelector: "hosting.panel/type=tls-upload",
		})
		if err == nil {
			for _, s := range secrets.Items {
				allCerts = append(allCerts, secretToResponse(&s))
			}
		}

		// Also list self-signed TLS secrets
		selfSignedSecrets, err := h.dynClient.Resource(secretGVR).Namespace(ns).List(r.Context(), metav1.ListOptions{
			LabelSelector: "hosting.panel/type=tls-selfsigned",
		})
		if err == nil {
			for _, s := range selfSignedSecrets.Items {
				allCerts = append(allCerts, secretToResponse(&s))
			}
		}
	}

	if allCerts == nil {
		allCerts = []CertificateResponse{}
	}
	writeJSON(w, http.StatusOK, allCerts)
}

// UploadCertificate handles POST /api/certificates/upload.
func (h *CertificateHandler) UploadCertificate(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())

	var req UploadCertificateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteBadRequest(w, "Invalid request body", nil)
		return
	}

	if req.Name == "" && req.Domain == "" {
		WriteBadRequest(w, "Name or domain is required", nil)
		return
	}
	if req.TLSCert == "" || req.TLSKey == "" {
		WriteBadRequest(w, "Both tlsCert (server certificate PEM) and tlsKey (private key PEM) are required", nil)
		return
	}

	// Determine namespace
	ns := req.Namespace
	if ns == "" {
		ns = hostingNamespace
	}

	// Non-admin can only upload to own namespace
	if !middleware.HasRole(claims, "admin") {
		WriteForbidden(w, "Access denied: can only upload to own namespace")
		return
	}

	// Build secret name from name or domain
	secretName := req.Name
	if secretName == "" {
		secretName = "tls-" + strings.ReplaceAll(strings.TrimSuffix(req.Domain, "."), ".", "-")
	}
	if !strings.HasPrefix(secretName, "tls-") {
		secretName = "tls-" + secretName
	}

	// Build cert data — if CA cert provided, prepend to tls.crt (full chain)
	certData := req.TLSCert
	if req.CACert != "" {
		certData = req.TLSCert + "\n" + req.CACert
	}

	// Create TLS Secret
	secret := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Secret",
			"metadata": map[string]interface{}{
				"name":      secretName,
				"namespace": ns,
				"labels": map[string]interface{}{
					"hosting.panel/type":    "tls-upload",
					"hosting.panel/user":    claims.Username,
					"managed-by":            "panel-core",
				},
				"annotations": map[string]interface{}{
					"hosting.panel/domain":   req.Domain,
					"hosting.panel/ssl-mode": "custom",
				},
			},
			"type": "kubernetes.io/tls",
			"stringData": map[string]interface{}{
				"tls.crt": certData,
				"tls.key": req.TLSKey,
			},
		},
	}
	if req.CACert != "" {
		secret.Object["stringData"].(map[string]interface{})["ca.crt"] = req.CACert
	}

	secretGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "secrets"}
	_, err := h.dynClient.Resource(secretGVR).Namespace(ns).Create(r.Context(), secret, metav1.CreateOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "already exists") {
			WriteConflict(w, "Certificate secret already exists: "+secretName, nil)
			return
		}
		WriteInternalError(w, "Failed to create TLS secret: "+err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{
		"name":      secretName,
		"namespace": ns,
		"domain":    req.Domain,
		"status":    "Uploaded",
	})
}

// GenerateSelfSigned handles POST /api/certificates/self-signed.
func (h *CertificateHandler) GenerateSelfSigned(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())

	var req SelfSignedRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteBadRequest(w, "Invalid request body", nil)
		return
	}

	if req.Domain == "" {
		WriteBadRequest(w, "Domain is required", nil)
		return
	}

	ns := req.Namespace
	if ns == "" {
		ns = hostingNamespace
	}

	if !middleware.HasRole(claims, "admin") {
		WriteForbidden(w, "Access denied: can only create in own namespace")
		return
	}

	secretName := "tls-" + strings.ReplaceAll(strings.TrimSuffix(req.Domain, "."), ".", "-")

	// Generate self-signed certificate using Go crypto (no cert-manager dependency)
	certPEM, keyPEM, err := generateSelfSignedCert(req.Domain)
	if err != nil {
		WriteInternalError(w, "Failed to generate self-signed certificate: "+err.Error())
		return
	}

	// Create TLS Secret directly
	secret := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Secret",
			"metadata": map[string]interface{}{
				"name":      secretName,
				"namespace": ns,
				"labels": map[string]interface{}{
					"hosting.panel/type": "tls-selfsigned",
					"hosting.panel/user": claims.Username,
					"managed-by":         "panel-core",
				},
				"annotations": map[string]interface{}{
					"hosting.panel/domain":   req.Domain,
					"hosting.panel/ssl-mode": "selfsigned",
				},
			},
			"type": "kubernetes.io/tls",
			"stringData": map[string]interface{}{
				"tls.crt": certPEM,
				"tls.key": keyPEM,
			},
		},
	}

	secretGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "secrets"}
	_, err = h.dynClient.Resource(secretGVR).Namespace(ns).Create(r.Context(), secret, metav1.CreateOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "already exists") {
			WriteConflict(w, "Self-signed certificate already exists for domain: "+req.Domain, nil)
			return
		}
		WriteInternalError(w, "Failed to create TLS secret: "+err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{
		"name":       secretName,
		"secretName": secretName,
		"namespace":  ns,
		"domain":     req.Domain,
		"status":     "Valid",
		"mode":       "selfsigned",
	})
}

// UpdateCertificate handles PUT /api/certificates/{name} — re-upload cert.
func (h *CertificateHandler) UpdateCertificate(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	name := chi.URLParam(r, "name")

	var req UploadCertificateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteBadRequest(w, "Invalid request body", nil)
		return
	}

	if req.TLSCert == "" || req.TLSKey == "" {
		WriteBadRequest(w, "Both tlsCert and tlsKey are required", nil)
		return
	}

	ns := req.Namespace
	if ns == "" {
		ns = hostingNamespace
	}

	if !middleware.HasRole(claims, "admin") {
		WriteForbidden(w, "Access denied")
		return
	}

	secretGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "secrets"}
	secretName := name
	if !strings.HasPrefix(secretName, "tls-") {
		secretName = "tls-" + secretName
	}

	existing, err := h.dynClient.Resource(secretGVR).Namespace(ns).Get(r.Context(), secretName, metav1.GetOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			WriteNotFound(w, "Certificate not found: "+secretName)
			return
		}
		WriteInternalError(w, "Failed to get certificate: "+err.Error())
		return
	}

	// Update the secret data
	certData := req.TLSCert
	if req.CACert != "" {
		certData = req.TLSCert + "\n" + req.CACert
	}

	existing.Object["stringData"] = map[string]interface{}{
		"tls.crt": certData,
		"tls.key": req.TLSKey,
	}
	if req.CACert != "" {
		existing.Object["stringData"].(map[string]interface{})["ca.crt"] = req.CACert
	}
	// Clear data field so stringData takes effect
	delete(existing.Object, "data")

	_, err = h.dynClient.Resource(secretGVR).Namespace(ns).Update(r.Context(), existing, metav1.UpdateOptions{})
	if err != nil {
		WriteInternalError(w, "Failed to update certificate: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"name":   secretName,
		"status": "Updated",
	})
}

// DeleteCertificate handles DELETE /api/certificates/{name}.
func (h *CertificateHandler) DeleteCertificate(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	name := chi.URLParam(r, "name")

	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = hostingNamespace
	}

	if !middleware.HasRole(claims, "admin") {
		WriteForbidden(w, "Access denied")
		return
	}

	secretGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "secrets"}
	secretName := name
	if !strings.HasPrefix(secretName, "tls-") {
		secretName = "tls-" + secretName
	}

	// Delete the TLS secret
	err := h.dynClient.Resource(secretGVR).Namespace(ns).Delete(r.Context(), secretName, metav1.DeleteOptions{})
	if err != nil && !strings.Contains(err.Error(), "not found") {
		WriteInternalError(w, "Failed to delete certificate secret: "+err.Error())
		return
	}

	// Also try to delete any associated cert-manager Certificate CRD
	certName := strings.TrimPrefix(secretName, "tls-")
	if !strings.HasPrefix(certName, "cert-") {
		certName = "cert-" + certName
	}
	_ = h.dynClient.Resource(CertificateGVR).Namespace(ns).Delete(r.Context(), certName, metav1.DeleteOptions{})

	writeJSON(w, http.StatusOK, map[string]string{"status": "Deleted"})
}

// CreateCertificateForDomain creates a cert-manager Certificate CRD for a domain.
// Called internally when a domain is added to a website.
func (h *CertificateHandler) CreateCertificateForDomain(ctx context.Context, domain, namespace string) error {
	certName := "cert-" + strings.ReplaceAll(strings.TrimSuffix(domain, "."), ".", "-")

	cert := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "cert-manager.io/v1",
			"kind":       "Certificate",
			"metadata": map[string]interface{}{
				"name":      certName,
				"namespace": namespace,
				"labels": map[string]interface{}{
					"managed-by": "panel-core",
				},
			},
			"spec": map[string]interface{}{
				"secretName": "tls-" + certName,
				"dnsNames":   []interface{}{domain},
				"issuerRef": map[string]interface{}{
					"name": h.clusterIssuer,
					"kind": "ClusterIssuer",
				},
			},
		},
	}

	_, err := h.dynClient.Resource(CertificateGVR).Namespace(namespace).Create(ctx, cert, metav1.CreateOptions{})
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		return fmt.Errorf("create certificate for %s: %w", domain, err)
	}
	return nil
}

// --- Helper functions ---

// certToResponse converts a cert-manager Certificate unstructured object to CertificateResponse.
func certToResponse(obj *unstructured.Unstructured) CertificateResponse {
	resp := CertificateResponse{
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
	}

	// Determine mode from annotations or labels
	annotations := obj.GetAnnotations()
	if annotations != nil {
		resp.Mode = annotations["hosting.panel/ssl-mode"]
	}
	labels := obj.GetLabels()
	if resp.Mode == "" && labels != nil {
		if labels["hosting.panel/type"] == "tls-selfsigned" {
			resp.Mode = "selfsigned"
		}
	}

	// Extract domain from spec.dnsNames
	spec, _ := obj.Object["spec"].(map[string]interface{})
	if spec != nil {
		if dnsNames, ok := spec["dnsNames"].([]interface{}); ok && len(dnsNames) > 0 {
			resp.Domain = fmt.Sprintf("%v", dnsNames[0])
		}
		if issuerRef, ok := spec["issuerRef"].(map[string]interface{}); ok {
			issuerName := fmt.Sprintf("%v", issuerRef["name"])
			resp.Issuer = issuerName
			// Infer mode from issuer if not set
			if resp.Mode == "" {
				if strings.Contains(issuerName, "letsencrypt") {
					resp.Mode = "letsencrypt"
				} else if issuerName == "selfsigned-issuer" {
					resp.Mode = "selfsigned"
				}
			}
		}
	}

	if resp.Mode == "" {
		resp.Mode = "letsencrypt" // default
	}

	// Extract status
	status, _ := obj.Object["status"].(map[string]interface{})
	if status != nil {
		resp.Status = determineCertStatus(status)

		if notBefore, ok := status["notBefore"].(string); ok {
			resp.NotBefore = notBefore
		}
		if notAfter, ok := status["notAfter"].(string); ok {
			resp.NotAfter = notAfter
		}

		// Extract message from conditions
		if conditions, ok := status["conditions"].([]interface{}); ok {
			for _, c := range conditions {
				cond, _ := c.(map[string]interface{})
				if cond == nil {
					continue
				}
				if fmt.Sprintf("%v", cond["type"]) == "Ready" {
					if fmt.Sprintf("%v", cond["status"]) == "False" {
						resp.Message = fmt.Sprintf("%v", cond["message"])
					}
				}
			}
		}
	} else {
		resp.Status = "Pending"
	}

	return resp
}

// determineCertStatus determines the certificate status from cert-manager status fields.
func determineCertStatus(status map[string]interface{}) string {
	// Check conditions for Ready status
	conditions, _ := status["conditions"].([]interface{})
	for _, c := range conditions {
		cond, _ := c.(map[string]interface{})
		if cond == nil {
			continue
		}
		condType := fmt.Sprintf("%v", cond["type"])
		condStatus := fmt.Sprintf("%v", cond["status"])

		if condType == "Ready" {
			if condStatus == "True" {
				// Check expiry
				if notAfter, ok := status["notAfter"].(string); ok {
					expiry, err := time.Parse(time.RFC3339, notAfter)
					if err == nil {
						daysUntilExpiry := time.Until(expiry).Hours() / 24
						if daysUntilExpiry <= 0 {
							return "Expired"
						}
						if daysUntilExpiry <= 30 {
							return "Expiring"
						}
					}
				}
				return "Valid"
			}
			return "Error"
		}
	}
	return "Pending"
}

// secretToResponse converts a TLS Secret (custom upload) to CertificateResponse.
func secretToResponse(obj *unstructured.Unstructured) CertificateResponse {
	resp := CertificateResponse{
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
		Mode:      "custom",
		Status:    "Valid", // uploaded certs are assumed valid
		Issuer:    "custom-upload",
	}

	annotations := obj.GetAnnotations()
	if annotations != nil {
		if domain, ok := annotations["hosting.panel/domain"]; ok {
			resp.Domain = domain
		}
		if mode, ok := annotations["hosting.panel/ssl-mode"]; ok {
			resp.Mode = mode
		}
	}

	return resp
}

// generateSelfSignedCert creates a self-signed TLS certificate for the given domain.
// Returns PEM-encoded certificate and private key.
func generateSelfSignedCert(domain string) (certPEM string, keyPEM string, err error) {
	// Generate ECDSA private key
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("generate key: %w", err)
	}

	// Create certificate template
	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return "", "", fmt.Errorf("generate serial: %w", err)
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName:   domain,
			Organization: []string{"Hosting Panel Self-Signed"},
		},
		DNSNames:              []string{domain},
		NotBefore:             now,
		NotAfter:              now.Add(365 * 24 * time.Hour), // 1 year
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	// Self-sign the certificate
	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return "", "", fmt.Errorf("create certificate: %w", err)
	}

	// Encode certificate to PEM
	certBuf := &bytes.Buffer{}
	if err := pem.Encode(certBuf, &pem.Block{Type: "CERTIFICATE", Bytes: certDER}); err != nil {
		return "", "", fmt.Errorf("encode cert PEM: %w", err)
	}

	// Encode private key to PEM
	keyDER, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		return "", "", fmt.Errorf("marshal key: %w", err)
	}
	keyBuf := &bytes.Buffer{}
	if err := pem.Encode(keyBuf, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}); err != nil {
		return "", "", fmt.Errorf("encode key PEM: %w", err)
	}

	return certBuf.String(), keyBuf.String(), nil
}
