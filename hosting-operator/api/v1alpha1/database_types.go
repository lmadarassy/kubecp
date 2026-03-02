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

// DatabaseSpec defines the desired state of Database.
type DatabaseSpec struct {
	// name is the database name to create in the Galera cluster.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^[a-zA-Z_][a-zA-Z0-9_]*$`
	Name string `json:"name"`

	// charset is the default character set.
	// +kubebuilder:default="utf8mb4"
	// +optional
	Charset string `json:"charset,omitempty"`

	// collation is the default collation.
	// +kubebuilder:default="utf8mb4_unicode_ci"
	// +optional
	Collation string `json:"collation,omitempty"`
}

// DatabaseStatus defines the observed state of Database.
type DatabaseStatus struct {
	// phase is the current lifecycle phase.
	// +kubebuilder:validation:Enum=Pending;Creating;Ready;Error;Terminating
	// +optional
	Phase string `json:"phase,omitempty"`

	// host is the database server hostname.
	// +optional
	Host string `json:"host,omitempty"`

	// port is the database server port.
	// +optional
	Port int32 `json:"port,omitempty"`

	// databaseName is the actual database name created.
	// +optional
	DatabaseName string `json:"databaseName,omitempty"`

	// username is the dedicated database user.
	// +optional
	Username string `json:"username,omitempty"`

	// password is the generated password for the database user.
	// +optional
	Password string `json:"password,omitempty"`

	// conditions represent the current state of the Database resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Host",type=string,JSONPath=`.status.host`
// +kubebuilder:printcolumn:name="Database",type=string,JSONPath=`.status.databaseName`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Database is the Schema for the databases API.
type Database struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec DatabaseSpec `json:"spec"`

	// +optional
	Status DatabaseStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// DatabaseList contains a list of Database.
type DatabaseList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Database `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Database{}, &DatabaseList{})
}
