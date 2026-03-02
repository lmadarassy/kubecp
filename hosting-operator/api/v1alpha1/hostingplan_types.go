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

// HostingPlanLimits defines resource limits for a hosting plan.
type HostingPlanLimits struct {
	// websites is the maximum number of websites.
	// +kubebuilder:validation:Minimum=0
	Websites int32 `json:"websites"`

	// databases is the maximum number of databases.
	// +kubebuilder:validation:Minimum=0
	Databases int32 `json:"databases"`

	// emailAccounts is the maximum number of email accounts (total across all domains).
	// +kubebuilder:validation:Minimum=0
	EmailAccounts int32 `json:"emailAccounts"`

	// emailDomains is the maximum number of email domains.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=5
	// +optional
	EmailDomains int32 `json:"emailDomains,omitempty"`

	// emailAccountsPerDomain is the maximum number of email accounts per domain.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=50
	// +optional
	EmailAccountsPerDomain int32 `json:"emailAccountsPerDomain,omitempty"`

	// cronJobs is the maximum number of cron jobs.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=10
	// +optional
	CronJobs int32 `json:"cronJobs,omitempty"`

	// storageGB is the total storage quota in gigabytes (User_Volume size).
	// +kubebuilder:validation:Minimum=1
	StorageGB int32 `json:"storageGB"`

	// cpuMillicores is the total CPU quota in millicores.
	// +kubebuilder:validation:Minimum=100
	CPUMillicores int32 `json:"cpuMillicores"`

	// memoryMB is the total memory quota in megabytes.
	// +kubebuilder:validation:Minimum=128
	MemoryMB int32 `json:"memoryMB"`
}

// HostingPlanDefaults defines default settings for new resources.
type HostingPlanDefaults struct {
	// php configures default PHP settings.
	// +optional
	PHP *HostingPlanPHPDefaults `json:"php,omitempty"`

	// website configures default website settings.
	// +optional
	Website *WebsiteDefaults `json:"website,omitempty"`
}

// HostingPlanPHPDefaults defines default PHP settings for a plan.
type HostingPlanPHPDefaults struct {
	// version is the default PHP version.
	// +kubebuilder:validation:Enum="7.4";"8.0";"8.1";"8.2";"8.3"
	// +kubebuilder:default="8.2"
	// +optional
	Version string `json:"version,omitempty"`

	// configProfile is the default PHP_Config_Profile name.
	// +kubebuilder:default="default"
	// +optional
	ConfigProfile string `json:"configProfile,omitempty"`
}

// WebsiteDefaults defines default website pod settings.
type WebsiteDefaults struct {
	// replicas is the default replica count.
	// +kubebuilder:default=2
	// +optional
	Replicas *int32 `json:"replicas,omitempty"`

	// resources defines default CPU/memory for website pods.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
}

// HostingPlanSpec defines the desired state of HostingPlan.
type HostingPlanSpec struct {
	// displayName is the human-readable plan name.
	// +kubebuilder:validation:MinLength=1
	DisplayName string `json:"displayName"`

	// limits defines the resource limits for this plan.
	Limits HostingPlanLimits `json:"limits"`

	// defaults defines default settings for new resources under this plan.
	// +optional
	Defaults *HostingPlanDefaults `json:"defaults,omitempty"`
}

// HostingPlanStatus defines the observed state of HostingPlan.
type HostingPlanStatus struct {
	// conditions represent the current state of the HostingPlan resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Display Name",type=string,JSONPath=`.spec.displayName`
// +kubebuilder:printcolumn:name="Websites",type=integer,JSONPath=`.spec.limits.websites`
// +kubebuilder:printcolumn:name="Databases",type=integer,JSONPath=`.spec.limits.databases`
// +kubebuilder:printcolumn:name="Storage(GB)",type=integer,JSONPath=`.spec.limits.storageGB`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// HostingPlan is the Schema for the hostingplans API.
type HostingPlan struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec HostingPlanSpec `json:"spec"`

	// +optional
	Status HostingPlanStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// HostingPlanList contains a list of HostingPlan.
type HostingPlanList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []HostingPlan `json:"items"`
}

func init() {
	SchemeBuilder.Register(&HostingPlan{}, &HostingPlanList{})
}
