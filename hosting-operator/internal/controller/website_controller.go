/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

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

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	hostingv1alpha1 "github.com/hosting-panel/hosting-operator/api/v1alpha1"
)

const (
	websiteFinalizer = "hosting.panel/website-cleanup"
	phpImagePrefix   = "php"
)

// phpImageTag returns the container image for a given PHP version.
// phpApacheImageTag returns the container image for a given PHP version (Apache variant).
// Uses php:X.Y-apache which includes Apache + mod_php with .htaccess support.
func phpApacheImageTag(version string) string {
	return fmt.Sprintf("%s:%s-apache", phpImagePrefix, version)
}

// WebsiteReconciler reconciles a Website object
type WebsiteReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	PDNSBaseURL string // e.g. http://hosting-panel-powerdns:8081
	PDNSAPIKey  string
	ExternalIP  string // node IP for default DNS records
}

// +kubebuilder:rbac:groups=hosting.hosting.panel,resources=websites,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=hosting.hosting.panel,resources=websites/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=hosting.hosting.panel,resources=websites/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=get;list;watch;create;update;patch;delete

// Reconcile moves the cluster state toward the desired state for a Website resource.
func (r *WebsiteReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Fetch the Website instance
	website := &hostingv1alpha1.Website{}
	if err := r.Get(ctx, req.NamespacedName, website); err != nil {
		if errors.IsNotFound(err) {
			log.Info("Website resource not found, likely deleted")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Handle deletion with finalizer
	if !website.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, website)
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(website, websiteFinalizer) {
		controllerutil.AddFinalizer(website, websiteFinalizer)
		if err := r.Update(ctx, website); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Set phase to Provisioning if Pending or empty
	if website.Status.Phase == "" || website.Status.Phase == "Pending" {
		website.Status.Phase = "Provisioning"
		if err := r.Status().Update(ctx, website); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Handle suspended state — scale deployment to 0
	if website.Status.Phase == "Suspended" {
		return r.reconcileSuspended(ctx, website)
	}

	// Reconcile PVC
	if err := r.reconcilePVC(ctx, website); err != nil {
		return r.setErrorStatus(ctx, website, "PVCFailed", err)
	}

	// Sync SFTP deployment volumes — mount user PVCs so SFTP uploads
	// land on the same volume that website pods serve from.
	if err := SyncSFTPVolumes(ctx, r.Client); err != nil {
		log.Error(err, "Failed to sync SFTP volumes (non-fatal)")
	}

	// Reconcile Deployment
	if err := r.reconcileDeployment(ctx, website); err != nil {
		return r.setErrorStatus(ctx, website, "DeploymentFailed", err)
	}

	// Reconcile Service
	if err := r.reconcileService(ctx, website); err != nil {
		return r.setErrorStatus(ctx, website, "ServiceFailed", err)
	}

	// Reconcile Ingress (Contour HTTPProxy) for primary domain
	if err := r.reconcileIngress(ctx, website); err != nil {
		return r.setErrorStatus(ctx, website, "IngressFailed", err)
	}

	// Reconcile DNS zone in PowerDNS
	if err := r.reconcileDNSZone(ctx, website); err != nil {
		log.Error(err, "Failed to reconcile DNS zone (non-fatal)")
	}

	// Reconcile alias HTTPProxies
	if err := r.reconcileAliasHTTPProxies(ctx, website); err != nil {
		return r.setErrorStatus(ctx, website, "AliasIngressFailed", err)
	}

	// Reconcile self-signed TLS secrets for domains with selfsigned mode
	if err := r.reconcileSelfSignedCerts(ctx, website); err != nil {
		return r.setErrorStatus(ctx, website, "CertFailed", err)
	}

	// Update status to Running
	return r.updateRunningStatus(ctx, website)
}

// reconcileDelete handles Website deletion — removes finalizer after cleanup.
func (r *WebsiteReconciler) reconcileDelete(ctx context.Context, website *hostingv1alpha1.Website) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Update phase to Terminating
	website.Status.Phase = "Terminating"
	_ = r.Status().Update(ctx, website)

	log.Info("Cleaning up Website resources", "name", website.Name)

	// Owned resources (Deployment, Service, PVC) are cleaned up by Kubernetes
	// garbage collection via OwnerReferences. We just remove the finalizer.

	controllerutil.RemoveFinalizer(website, websiteFinalizer)
	if err := r.Update(ctx, website); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// reconcilePVC ensures the User_Volume PVC exists for the website owner.
// Uses the shared User_Volume (uv-{owner}) instead of per-website PVCs.
func (r *WebsiteReconciler) reconcilePVC(ctx context.Context, website *hostingv1alpha1.Website) error {
	// Default storage size from spec or 10Gi
	storageGB := int32(10)
	if website.Spec.StorageSize != "" {
		// Parse storageSize like "5Gi" to get the number
		q := resource.MustParse(website.Spec.StorageSize)
		storageGB = int32(q.Value() / (1024 * 1024 * 1024))
		if storageGB < 1 {
			storageGB = 1
		}
	}

	return EnsureUserVolume(ctx, r.Client, website.Namespace, website.Spec.Owner, storageGB)
}

// reconcileDeployment ensures the Deployment exists with the correct spec.
func (r *WebsiteReconciler) reconcileDeployment(ctx context.Context, website *hostingv1alpha1.Website) error {
	deploy := &appsv1.Deployment{}
	deployName := website.Name

	err := r.Get(ctx, types.NamespacedName{Name: deployName, Namespace: website.Namespace}, deploy)
	if err != nil && !errors.IsNotFound(err) {
		return err
	}

	desired := r.desiredDeployment(website)
	if err := controllerutil.SetControllerReference(website, desired, r.Scheme); err != nil {
		return err
	}

	if errors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}

	// Update existing deployment to match desired state (full template sync)
	deploy.Spec.Replicas = desired.Spec.Replicas
	deploy.Spec.Template.Labels = desired.Spec.Template.Labels
	deploy.Spec.Template.Spec.InitContainers = desired.Spec.Template.Spec.InitContainers
	deploy.Spec.Template.Spec.Containers = desired.Spec.Template.Spec.Containers
	deploy.Spec.Template.Spec.Volumes = desired.Spec.Template.Spec.Volumes
	deploy.Spec.Template.Spec.SecurityContext = desired.Spec.Template.Spec.SecurityContext
	return r.Update(ctx, deploy)
}

// desiredDeployment builds the desired Deployment for a Website.
// Uses User_Volume PVC with subPath: web/{primaryDomain} mount.
// desiredDeployment builds the desired Deployment for a Website.
// Uses a single php:X.Y-apache container with mod_php and .htaccess support.
// The container runs as the owner's UID so PHP file operations match SFTP uploads.
func (r *WebsiteReconciler) desiredDeployment(website *hostingv1alpha1.Website) *appsv1.Deployment {
	replicas := int32(1)
	if website.Spec.Replicas != nil {
		replicas = *website.Spec.Replicas
	}

	phpVersion := website.Spec.PHP.Version
	if phpVersion == "" {
		phpVersion = "8.2"
	}

	labels := websiteLabels(website)

	// User_Volume PVC name: uv-{owner}
	userVolumePVC := UserVolumePVCName(website.Spec.Owner)
	// subPath for this website's webroot: web/{primaryDomain}
	webrootSubPath := fmt.Sprintf("web/%s", website.Spec.PrimaryDomain)

	// PHP config profile ConfigMap name
	phpProfileCM := fmt.Sprintf("hosting-panel-php-%s-profile", website.Spec.PHPConfigProfile)
	if website.Spec.PHPConfigProfile == "" || website.Spec.PHPConfigProfile == "default" {
		phpProfileCM = "hosting-panel-php-default-profile"
	}

	// Owner UID for running PHP as the file owner (matches SFTP upload UID).
	// Default to 2000 (first dynamic user in SFTP entrypoint).
	ownerUID := int64(2000)
	if website.Spec.OwnerUID > 0 {
		ownerUID = int64(website.Spec.OwnerUID)
	}

	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      website.Name,
			Namespace: website.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					SecurityContext: &corev1.PodSecurityContext{
						FSGroup: &ownerUID,
					},
					Containers: []corev1.Container{
						{
							Name:  "apache-php",
							Image: phpApacheImageTag(phpVersion),
							Command: []string{"/bin/bash", "-c"},
							Args: []string{
								fmt.Sprintf(`set -e
# Install PHP extensions for database access
docker-php-ext-install pdo_mysql mysqli > /dev/null 2>&1
# Configure Apache: listen on 8080, enable mod_rewrite, AllowOverride All
sed -i 's/Listen 80/Listen 8080/' /etc/apache2/ports.conf
sed -i 's/:80/:8080/g' /etc/apache2/sites-available/000-default.conf
a2enmod rewrite > /dev/null
cat > /etc/apache2/conf-available/hosting-panel.conf << 'APACHECONF'
<Directory /var/www/html>
    AllowOverride All
    Require all granted
</Directory>
ServerTokens Prod
ServerSignature Off
APACHECONF
a2enconf hosting-panel > /dev/null
# Set Apache to run as owner UID
export APACHE_RUN_USER=#%d
export APACHE_RUN_GROUP=#%d
# Fix webroot ownership
chown -R %d:%d /var/www/html 2>/dev/null || true
# Start Apache in foreground
exec apache2-foreground`, ownerUID, ownerUID, ownerUID, ownerUID),
							},
							Ports: []corev1.ContainerPort{
								{Name: "http", ContainerPort: 8080, Protocol: corev1.ProtocolTCP},
							},
							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									TCPSocket: &corev1.TCPSocketAction{
										Port: intstr.FromInt(8080),
									},
								},
								InitialDelaySeconds: 5,
								PeriodSeconds:       5,
								TimeoutSeconds:      2,
							},
							VolumeMounts: []corev1.VolumeMount{
								{Name: "webroot", MountPath: "/var/www/html", SubPath: webrootSubPath},
								{Name: "php-config", MountPath: "/usr/local/etc/php/conf.d/99-overrides.ini", SubPath: "php-overrides.ini"},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "webroot",
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
									ClaimName: userVolumePVC,
								},
							},
						},
						{
							Name: "php-config",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: phpProfileCM,
									},
									Optional: boolPtr(true),
								},
							},
						},
					},
				},
			},
		},
	}

	// Apply resource requests/limits if specified
	if website.Spec.Resources.Requests != nil || website.Spec.Resources.Limits != nil {
		for i := range deploy.Spec.Template.Spec.Containers {
			deploy.Spec.Template.Spec.Containers[i].Resources = website.Spec.Resources
		}
	}

	return deploy
}

// reconcileService ensures the Service exists for the Website.
func (r *WebsiteReconciler) reconcileService(ctx context.Context, website *hostingv1alpha1.Website) error {
	svc := &corev1.Service{}
	svcName := website.Name

	err := r.Get(ctx, types.NamespacedName{Name: svcName, Namespace: website.Namespace}, svc)
	if err != nil && !errors.IsNotFound(err) {
		return err
	}

	if errors.IsNotFound(err) {
		svc = &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      svcName,
				Namespace: website.Namespace,
				Labels:    websiteLabels(website),
			},
			Spec: corev1.ServiceSpec{
				Selector: websiteLabels(website),
				Ports: []corev1.ServicePort{
					{
						Name:       "http",
						Port:       80,
						TargetPort: intstr.FromString("http"),
						Protocol:   corev1.ProtocolTCP,
					},
				},
			},
		}

		if err := controllerutil.SetControllerReference(website, svc, r.Scheme); err != nil {
			return err
		}
		return r.Create(ctx, svc)
	}

	return nil
}

// reconcileIngress ensures a Contour HTTPProxy resource exists for the website's domains.
// Creates HTTPProxy with virtualhost for primaryDomain, and includes routes for all aliases.
func (r *WebsiteReconciler) reconcileIngress(ctx context.Context, website *hostingv1alpha1.Website) error {
	log := logf.FromContext(ctx)
	httpProxyName := fmt.Sprintf("%s-httpproxy", website.Name)

	if website.Spec.PrimaryDomain == "" {
		log.Info("No primaryDomain configured, skipping HTTPProxy creation", "website", website.Name)
		return nil
	}

	// Build desired HTTPProxy as unstructured
	desired := r.desiredHTTPProxy(website, httpProxyName)
	if err := controllerutil.SetControllerReference(website, desired, r.Scheme); err != nil {
		return err
	}

	// Check if HTTPProxy exists
	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "projectcontour.io",
		Version: "v1",
		Kind:    "HTTPProxy",
	})
	err := r.Get(ctx, types.NamespacedName{Name: httpProxyName, Namespace: website.Namespace}, existing)
	if err != nil && !errors.IsNotFound(err) {
		return err
	}

	if errors.IsNotFound(err) {
		log.Info("Creating HTTPProxy for website", "httpproxy", httpProxyName, "domain", website.Spec.PrimaryDomain)
		return r.Create(ctx, desired)
	}

	// Update existing HTTPProxy
	existing.Object["spec"] = desired.Object["spec"]
	return r.Update(ctx, existing)
}

// desiredHTTPProxy builds the desired Contour HTTPProxy for a Website.
func (r *WebsiteReconciler) desiredHTTPProxy(website *hostingv1alpha1.Website, name string) *unstructured.Unstructured {
	labels := websiteLabels(website)

	// TLS configuration
	tlsConfig := map[string]interface{}{}
	if website.Spec.SSL != nil && website.Spec.SSL.Enabled {
		mode := website.Spec.SSL.Mode
		if mode == "" {
			mode = "letsencrypt"
		}
		switch mode {
		case "letsencrypt", "selfsigned":
			tlsConfig["secretName"] = fmt.Sprintf("%s-tls", website.Name)
		case "custom":
			if website.Spec.SSL.SecretName != "" {
				tlsConfig["secretName"] = website.Spec.SSL.SecretName
			}
		case "none":
			// No TLS
		}
	}

	// Build routes — primary domain route
	routes := []interface{}{
		map[string]interface{}{
			"conditions": []interface{}{
				map[string]interface{}{"prefix": "/"},
			},
			"services": []interface{}{
				map[string]interface{}{
					"name": website.Name,
					"port": int64(80),
				},
			},
		},
	}

	// Build virtualhost spec
	virtualhost := map[string]interface{}{
		"fqdn": website.Spec.PrimaryDomain,
	}
	if len(tlsConfig) > 0 {
		virtualhost["tls"] = tlsConfig
	}

	spec := map[string]interface{}{
		"virtualhost": virtualhost,
		"routes":      routes,
	}

	// Add includes for aliases (each alias gets its own HTTPProxy or we add them as additional FQDNs)
	// Contour supports multiple HTTPProxy resources or we can use a single one with the primary domain
	// For aliases, we create separate HTTPProxy resources in the reconciler

	httpProxy := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "projectcontour.io/v1",
			"kind":       "HTTPProxy",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": website.Namespace,
				"labels":    toStringInterfaceMap(labels),
			},
			"spec": spec,
		},
	}

	return httpProxy
}

// reconcileDNSZone ensures a PowerDNS zone exists for the website's primary domain.
func (r *WebsiteReconciler) reconcileDNSZone(ctx context.Context, website *hostingv1alpha1.Website) error {
	if r.PDNSBaseURL == "" || r.PDNSAPIKey == "" || website.Spec.PrimaryDomain == "" {
		return nil
	}
	log := logf.FromContext(ctx)
	domain := website.Spec.PrimaryDomain
	zoneName := domain + "."

	// Check if zone already exists
	url := fmt.Sprintf("%s/api/v1/servers/localhost/zones/%s", r.PDNSBaseURL, zoneName)
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	req.Header.Set("X-API-Key", r.PDNSAPIKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("PowerDNS API unreachable: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode == 200 {
		return nil // zone exists
	}

	// Create zone with SOA + NS + default records
	rrsets := []map[string]interface{}{}
	if r.ExternalIP != "" {
		rrsets = append(rrsets,
			map[string]interface{}{
				"name": zoneName, "type": "A", "ttl": 3600, "changetype": "REPLACE",
				"records": []map[string]interface{}{{"content": r.ExternalIP, "disabled": false}},
			},
			map[string]interface{}{
				"name": fmt.Sprintf("www.%s", zoneName), "type": "CNAME", "ttl": 3600, "changetype": "REPLACE",
				"records": []map[string]interface{}{{"content": zoneName, "disabled": false}},
			},
			map[string]interface{}{
				"name": fmt.Sprintf("mail.%s", zoneName), "type": "A", "ttl": 3600, "changetype": "REPLACE",
				"records": []map[string]interface{}{{"content": r.ExternalIP, "disabled": false}},
			},
			map[string]interface{}{
				"name": zoneName, "type": "MX", "ttl": 3600, "changetype": "REPLACE",
				"records": []map[string]interface{}{{"content": fmt.Sprintf("10 mail.%s", zoneName), "disabled": false}},
			},
		)
	}
	body, _ := json.Marshal(map[string]interface{}{
		"name":        zoneName,
		"kind":        "Native",
		"nameservers": []string{fmt.Sprintf("ns1.%s", zoneName)},
		"rrsets":      rrsets,
	})
	req, _ = http.NewRequestWithContext(ctx, "POST", fmt.Sprintf("%s/api/v1/servers/localhost/zones", r.PDNSBaseURL), bytes.NewReader(body))
	req.Header.Set("X-API-Key", r.PDNSAPIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to create DNS zone: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode == 201 || resp.StatusCode == 409 {
		log.Info("DNS zone created with default records", "domain", domain, "ip", r.ExternalIP)
		return nil
	}
	return fmt.Errorf("PowerDNS create zone returned %d", resp.StatusCode)
}

// reconcileAliasHTTPProxies creates/updates HTTPProxy resources for each domain alias.
func (r *WebsiteReconciler) reconcileAliasHTTPProxies(ctx context.Context, website *hostingv1alpha1.Website) error {
	for _, alias := range website.Spec.Aliases {
		aliasName := fmt.Sprintf("%s-alias-%s", website.Name, sanitizeDomainForK8s(alias))
		desired := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "projectcontour.io/v1",
				"kind":       "HTTPProxy",
				"metadata": map[string]interface{}{
					"name":      aliasName,
					"namespace": website.Namespace,
					"labels":    toStringInterfaceMap(websiteLabels(website)),
				},
				"spec": map[string]interface{}{
					"virtualhost": map[string]interface{}{
						"fqdn": alias,
					},
					"routes": []interface{}{
						map[string]interface{}{
							"conditions": []interface{}{
								map[string]interface{}{"prefix": "/"},
							},
							"services": []interface{}{
								map[string]interface{}{
									"name": website.Name,
									"port": int64(80),
								},
							},
						},
					},
				},
			},
		}

		if err := controllerutil.SetControllerReference(website, desired, r.Scheme); err != nil {
			return err
		}

		existing := &unstructured.Unstructured{}
		existing.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "projectcontour.io",
			Version: "v1",
			Kind:    "HTTPProxy",
		})
		err := r.Get(ctx, types.NamespacedName{Name: aliasName, Namespace: website.Namespace}, existing)
		if errors.IsNotFound(err) {
			if err := r.Create(ctx, desired); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		existing.Object["spec"] = desired.Object["spec"]
		if err := r.Update(ctx, existing); err != nil {
			return err
		}
	}
	return nil
}

// sanitizeDomainForK8s converts a domain name to a valid K8s resource name suffix.
func sanitizeDomainForK8s(domain string) string {
	return strings.ReplaceAll(domain, ".", "-")
}

// toStringInterfaceMap converts map[string]string to map[string]interface{} for unstructured.
func toStringInterfaceMap(m map[string]string) map[string]interface{} {
	result := make(map[string]interface{}, len(m))
	for k, v := range m {
		result[k] = v
	}
	return result
}

// stringPtr returns a pointer to the given string.
func stringPtr(s string) *string {
	return &s
}

// boolPtr returns a pointer to the given bool.
func boolPtr(b bool) *bool {
	return &b
}

// int64Ptr returns a pointer to the given int64.
func int64Ptr(i int64) *int64 {
	return &i
}

// reconcileSelfSignedCerts creates TLS secrets for domains with selfsigned SSL mode.
func (r *WebsiteReconciler) reconcileSelfSignedCerts(ctx context.Context, website *hostingv1alpha1.Website) error {
	log := logf.FromContext(ctx)

	if website.Spec.SSL == nil || !website.Spec.SSL.Enabled {
		return nil
	}
	mode := website.Spec.SSL.Mode
	if mode == "" {
		mode = "letsencrypt"
	}
	if mode != "selfsigned" {
		return nil
	}

	secretName := fmt.Sprintf("%s-tls", website.Name)
	secret := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Name: secretName, Namespace: website.Namespace}, secret)
	if err == nil {
		// Secret already exists
		return nil
	}
	if !errors.IsNotFound(err) {
		return err
	}

	// Generate cert for primaryDomain + all aliases
	allDomains := []string{website.Spec.PrimaryDomain}
	allDomains = append(allDomains, website.Spec.Aliases...)

	log.Info("Generating self-signed TLS certificate", "domains", allDomains, "secret", secretName)

	certPEM, keyPEM, err := generateSelfSignedCertPEM(website.Spec.PrimaryDomain)
	if err != nil {
		return fmt.Errorf("failed to generate self-signed cert for %s: %w", website.Spec.PrimaryDomain, err)
	}

	tlsSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: website.Namespace,
			Labels: func() map[string]string {
				l := websiteLabels(website)
				l["hosting.panel/type"] = "tls-selfsigned"
				return l
			}(),
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{
			"tls.crt": certPEM,
			"tls.key": keyPEM,
		},
	}

	if err := controllerutil.SetControllerReference(website, tlsSecret, r.Scheme); err != nil {
		return err
	}
	if err := r.Create(ctx, tlsSecret); err != nil {
		return fmt.Errorf("failed to create TLS secret %s: %w", secretName, err)
	}
	return nil
}

// generateSelfSignedCertPEM generates a self-signed TLS certificate for the given domain.
func generateSelfSignedCertPEM(domain string) (certPEM []byte, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}

	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject:      pkix.Name{CommonName: domain, Organization: []string{"Hosting Panel"}},
		NotBefore:    time.Now().Add(-1 * time.Hour),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{domain},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	return certPEM, keyPEM, nil
}

// updateRunningStatus sets the Website status to Running with current replica info.
func (r *WebsiteReconciler) updateRunningStatus(ctx context.Context, website *hostingv1alpha1.Website) (ctrl.Result, error) {
	deploy := &appsv1.Deployment{}
	if err := r.Get(ctx, types.NamespacedName{Name: website.Name, Namespace: website.Namespace}, deploy); err != nil {
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	website.Status.Phase = "Running"
	website.Status.Replicas = deploy.Status.Replicas
	website.Status.ReadyReplicas = deploy.Status.ReadyReplicas

	// Update domain statuses — primaryDomain + aliases
	allDomains := []string{website.Spec.PrimaryDomain}
	allDomains = append(allDomains, website.Spec.Aliases...)

	domainStatuses := make([]hostingv1alpha1.WebsiteDomainStatus, 0, len(allDomains))
	for _, domainName := range allDomains {
		certStatus := "Pending"
		if website.Spec.SSL != nil && website.Spec.SSL.Enabled {
			secretName := fmt.Sprintf("%s-tls", website.Name)
			if website.Spec.SSL.SecretName != "" {
				secretName = website.Spec.SSL.SecretName
			}
			secret := &corev1.Secret{}
			if err := r.Get(ctx, types.NamespacedName{Name: secretName, Namespace: website.Namespace}, secret); err == nil {
				certStatus = "Valid"
			}
		} else {
			certStatus = "Valid"
		}
		domainStatuses = append(domainStatuses, hostingv1alpha1.WebsiteDomainStatus{
			Name:              domainName,
			CertificateStatus: certStatus,
		})
	}
	website.Status.Domains = domainStatuses

	meta.SetStatusCondition(&website.Status.Conditions, metav1.Condition{
		Type:               "Available",
		Status:             metav1.ConditionTrue,
		Reason:             "Reconciled",
		Message:            "All resources created successfully",
		LastTransitionTime: metav1.Now(),
	})

	if err := r.Status().Update(ctx, website); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// setErrorStatus updates the Website status to Error.
func (r *WebsiteReconciler) setErrorStatus(ctx context.Context, website *hostingv1alpha1.Website, reason string, err error) (ctrl.Result, error) {
	website.Status.Phase = "Error"
	meta.SetStatusCondition(&website.Status.Conditions, metav1.Condition{
		Type:               "Available",
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            err.Error(),
		LastTransitionTime: metav1.Now(),
	})
	_ = r.Status().Update(ctx, website)
	return ctrl.Result{RequeueAfter: 30 * time.Second}, err
}

// reconcileSuspended handles a suspended website — scales deployment to 0 replicas.
func (r *WebsiteReconciler) reconcileSuspended(ctx context.Context, website *hostingv1alpha1.Website) (ctrl.Result, error) {
	deploy := &appsv1.Deployment{}
	err := r.Get(ctx, types.NamespacedName{Name: website.Name, Namespace: website.Namespace}, deploy)
	if err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Scale to 0
	zero := int32(0)
	if deploy.Spec.Replicas == nil || *deploy.Spec.Replicas != zero {
		deploy.Spec.Replicas = &zero
		if err := r.Update(ctx, deploy); err != nil {
			return ctrl.Result{}, err
		}
	}

	website.Status.Replicas = 0
	website.Status.ReadyReplicas = 0
	meta.SetStatusCondition(&website.Status.Conditions, metav1.Condition{
		Type:               "Available",
		Status:             metav1.ConditionFalse,
		Reason:             "Suspended",
		Message:            "Website is suspended",
		LastTransitionTime: metav1.Now(),
	})
	_ = r.Status().Update(ctx, website)
	return ctrl.Result{}, nil
}

// websiteLabels returns standard labels for Website-owned resources.
func websiteLabels(website *hostingv1alpha1.Website) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       "website",
		"app.kubernetes.io/instance":   website.Name,
		"app.kubernetes.io/managed-by": "hosting-operator",
		"hosting.panel/website":        website.Name,
		"hosting.panel/user":           website.Spec.Owner,
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *WebsiteReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&hostingv1alpha1.Website{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Named("website").
		Complete(r)
}
