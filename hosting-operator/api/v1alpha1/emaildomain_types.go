/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EmailDomainSpec defines the desired state of EmailDomain.
type EmailDomainSpec struct {
	// domain is the email domain name (e.g. "example.com").
	// +kubebuilder:validation:MinLength=1
	Domain string `json:"domain"`

	// owner is the username who owns this email domain.
	// +kubebuilder:validation:MinLength=1
	Owner string `json:"owner"`

	// catchAll is the email address to receive all unmatched mail for this domain.
	// +optional
	CatchAll string `json:"catchAll,omitempty"`

	// spamFilter enables/disables Rspamd spam filtering for this domain.
	// +kubebuilder:default=true
	// +optional
	SpamFilter bool `json:"spamFilter,omitempty"`
}

// EmailDomainStatus defines the observed state of EmailDomain.
type EmailDomainStatus struct {
	// phase is the current lifecycle phase.
	// +kubebuilder:validation:Enum=Pending;Active;Error;Terminating
	// +optional
	Phase string `json:"phase,omitempty"`

	// dkimSecretName is the name of the Secret containing the DKIM private key.
	// +optional
	DKIMSecretName string `json:"dkimSecretName,omitempty"`

	// accountCount is the number of email accounts under this domain.
	// +optional
	AccountCount int32 `json:"accountCount,omitempty"`

	// conditions represent the current state of the EmailDomain resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Domain",type=string,JSONPath=`.spec.domain`
// +kubebuilder:printcolumn:name="Owner",type=string,JSONPath=`.spec.owner`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Accounts",type=integer,JSONPath=`.status.accountCount`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// EmailDomain is the Schema for the emaildomains API.
type EmailDomain struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec EmailDomainSpec `json:"spec"`

	// +optional
	Status EmailDomainStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// EmailDomainList contains a list of EmailDomain.
type EmailDomainList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []EmailDomain `json:"items"`
}

func init() {
	SchemeBuilder.Register(&EmailDomain{}, &EmailDomainList{})
}
