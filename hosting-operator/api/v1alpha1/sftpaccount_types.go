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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SFTPAccountSpec defines the desired state of SFTPAccount.
type SFTPAccountSpec struct {
	// username is the SFTP login username.
	// +kubebuilder:validation:MinLength=1
	Username string `json:"username"`

	// userVolumeName is the name of the User_Volume PVC (uv-{username}).
	// +kubebuilder:validation:MinLength=1
	UserVolumeName string `json:"userVolumeName"`

	// allowedPaths lists the directories this SFTP user can access.
	// +kubebuilder:validation:MinItems=1
	AllowedPaths []string `json:"allowedPaths"`
}

// SFTPAccountStatus defines the observed state of SFTPAccount.
type SFTPAccountStatus struct {
	// phase is the current lifecycle phase.
	// +kubebuilder:validation:Enum=Pending;Active;Error;Terminating
	// +optional
	Phase string `json:"phase,omitempty"`

	// conditions represent the current state of the SFTPAccount resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Username",type=string,JSONPath=`.spec.username`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// SFTPAccount is the Schema for the sftpaccounts API.
type SFTPAccount struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec SFTPAccountSpec `json:"spec"`

	// +optional
	Status SFTPAccountStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// SFTPAccountList contains a list of SFTPAccount.
type SFTPAccountList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []SFTPAccount `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SFTPAccount{}, &SFTPAccountList{})
}
