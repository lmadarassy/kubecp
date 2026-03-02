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

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// WebsiteDomainSSL configures TLS for a domain.
type WebsiteDomainSSL struct {
	// enabled indicates whether TLS is enabled.
	// +kubebuilder:default=true
	Enabled bool `json:"enabled"`

	// mode is the SSL provisioning mode: letsencrypt, selfsigned, custom, or none.
	// +kubebuilder:validation:Enum="letsencrypt";"selfsigned";"custom";"none"
	// +kubebuilder:default="letsencrypt"
	// +optional
	Mode string `json:"mode,omitempty"`

	// issuer is the ClusterIssuer name for cert-manager.
	// +kubebuilder:default="letsencrypt-production"
	// +optional
	Issuer string `json:"issuer,omitempty"`

	// secretName is the name of an existing TLS Secret to use (mode=custom).
	// +optional
	SecretName string `json:"secretName,omitempty"`
}

// WebsitePHP configures the PHP runtime.
type WebsitePHP struct {
	// version is the PHP version to use.
	// +kubebuilder:validation:Enum="7.4";"8.0";"8.1";"8.2";"8.3";"8.4";"8.5"
	// +kubebuilder:default="8.2"
	Version string `json:"version"`
}

// WebsiteSpec defines the desired state of Website.
type WebsiteSpec struct {
	// primaryDomain is the main domain for this website (1 Website = 1 primaryDomain).
	// +kubebuilder:validation:MinLength=1
	PrimaryDomain string `json:"primaryDomain"`

	// aliases is the list of additional domain aliases pointing to this website.
	// +optional
	Aliases []string `json:"aliases,omitempty"`

	// ssl configures TLS for the primary domain.
	// +optional
	SSL *WebsiteDomainSSL `json:"ssl,omitempty"`

	// php configures the PHP runtime.
	// +optional
	PHP WebsitePHP `json:"php,omitempty"`

	// phpConfigProfile is the name of the PHP_Config_Profile ConfigMap to mount.
	// +kubebuilder:default="default"
	// +optional
	PHPConfigProfile string `json:"phpConfigProfile,omitempty"`

	// owner is the username who owns this website (maps to hosting.panel/user label).
	// +kubebuilder:validation:MinLength=1
	Owner string `json:"owner"`

	// replicas is the desired number of website pod replicas.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1
	// +optional
	Replicas *int32 `json:"replicas,omitempty"`

	// resources defines CPU/memory requests and limits for the website pods.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// storageSize defines the persistent volume size for website files.
	// +kubebuilder:default="5Gi"
	// +optional
	StorageSize string `json:"storageSize,omitempty"`

	// ownerUID is the Linux UID for the website owner (matches SFTP upload UID).
	// Used to run PHP processes as the file owner for correct permissions.
	// +kubebuilder:validation:Minimum=1000
	// +optional
	OwnerUID int32 `json:"ownerUID,omitempty"`
}

// WebsiteDomainStatus reports the status of a single domain.
type WebsiteDomainStatus struct {
	// name is the domain name.
	Name string `json:"name"`

	// certificateStatus is the TLS certificate status.
	// +kubebuilder:validation:Enum=Pending;Valid;Expiring;Expired;Error
	// +optional
	CertificateStatus string `json:"certificateStatus,omitempty"`

	// certificateExpiry is the certificate expiration timestamp.
	// +optional
	CertificateExpiry *metav1.Time `json:"certificateExpiry,omitempty"`
}

// WebsiteStatus defines the observed state of Website.
type WebsiteStatus struct {
	// phase is the current lifecycle phase.
	// +kubebuilder:validation:Enum=Pending;Provisioning;Running;Suspended;Error;Terminating
	// +optional
	Phase string `json:"phase,omitempty"`

	// replicas is the total number of pod replicas.
	// +optional
	Replicas int32 `json:"replicas,omitempty"`

	// readyReplicas is the number of ready pod replicas.
	// +optional
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`

	// domains reports per-domain status including certificate info.
	// +optional
	Domains []WebsiteDomainStatus `json:"domains,omitempty"`

	// conditions represent the current state of the Website resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Domain",type=string,JSONPath=`.spec.primaryDomain`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Owner",type=string,JSONPath=`.spec.owner`
// +kubebuilder:printcolumn:name="PHP",type=string,JSONPath=`.spec.php.version`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Website is the Schema for the websites API.
type Website struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec WebsiteSpec `json:"spec"`

	// +optional
	Status WebsiteStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// WebsiteList contains a list of Website.
type WebsiteList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Website `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Website{}, &WebsiteList{})
}
