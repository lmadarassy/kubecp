/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package controller

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	hostingv1alpha1 "github.com/hosting-panel/hosting-operator/api/v1alpha1"
)

const (
	emailDomainFinalizer = "hosting.panel/emaildomain-cleanup"
	dkimSelector         = "hosting"
)

// EmailDomainReconciler reconciles an EmailDomain object.
type EmailDomainReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=hosting.hosting.panel,resources=emaildomains,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=hosting.hosting.panel,resources=emaildomains/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=hosting.hosting.panel,resources=emaildomains/finalizers,verbs=update

func (r *EmailDomainReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	emailDomain := &hostingv1alpha1.EmailDomain{}
	if err := r.Get(ctx, req.NamespacedName, emailDomain); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Handle deletion
	if !emailDomain.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, emailDomain)
	}

	// Add finalizer
	if !controllerutil.ContainsFinalizer(emailDomain, emailDomainFinalizer) {
		controllerutil.AddFinalizer(emailDomain, emailDomainFinalizer)
		if err := r.Update(ctx, emailDomain); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Set phase to Pending if empty
	if emailDomain.Status.Phase == "" {
		emailDomain.Status.Phase = "Pending"
		_ = r.Status().Update(ctx, emailDomain)
	}

	// Generate DKIM key pair
	if err := r.reconcileDKIM(ctx, emailDomain); err != nil {
		return r.setError(ctx, emailDomain, "DKIMFailed", err)
	}

	// Count email accounts for this domain
	if err := r.updateAccountCount(ctx, emailDomain); err != nil {
		log.Error(err, "Failed to count email accounts (non-fatal)")
	}

	// Set phase to Active
	emailDomain.Status.Phase = "Active"
	meta.SetStatusCondition(&emailDomain.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		Reason:             "Reconciled",
		Message:            "EmailDomain is active",
		LastTransitionTime: metav1.Now(),
	})
	_ = r.Status().Update(ctx, emailDomain)

	return ctrl.Result{}, nil
}

func (r *EmailDomainReconciler) reconcileDelete(ctx context.Context, ed *hostingv1alpha1.EmailDomain) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	ed.Status.Phase = "Terminating"
	_ = r.Status().Update(ctx, ed)

	log.Info("Cleaning up EmailDomain resources", "domain", ed.Spec.Domain)

	// DKIM secret is owned by EmailDomain, will be garbage collected

	controllerutil.RemoveFinalizer(ed, emailDomainFinalizer)
	if err := r.Update(ctx, ed); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// reconcileDKIM generates a DKIM key pair and stores it in a Secret.
func (r *EmailDomainReconciler) reconcileDKIM(ctx context.Context, ed *hostingv1alpha1.EmailDomain) error {
	log := logf.FromContext(ctx)
	secretName := fmt.Sprintf("dkim-%s", sanitizeDomainForK8s(ed.Spec.Domain))

	// Check if DKIM secret already exists
	existing := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Name: secretName, Namespace: ed.Namespace}, existing)
	if err == nil {
		ed.Status.DKIMSecretName = secretName
		return nil
	}
	if !errors.IsNotFound(err) {
		return err
	}

	log.Info("Generating DKIM key pair", "domain", ed.Spec.Domain)

	// Generate RSA 2048-bit key pair
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return fmt.Errorf("generate DKIM key: %w", err)
	}

	// Encode private key to PEM
	privKeyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
	})

	// Encode public key to DER for DNS TXT record
	pubKeyDER, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		return fmt.Errorf("marshal DKIM public key: %w", err)
	}
	pubKeyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: pubKeyDER,
	})

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: ed.Namespace,
			Labels: map[string]string{
				"hosting.panel/email-domain": ed.Spec.Domain,
				"hosting.panel/user":         ed.Spec.Owner,
				"hosting.panel/dkim":         "true",
			},
		},
		Data: map[string][]byte{
			"private.key": privKeyPEM,
			"public.key":  pubKeyPEM,
			"selector":    []byte(dkimSelector),
			"domain":      []byte(ed.Spec.Domain),
		},
	}

	if err := controllerutil.SetControllerReference(ed, secret, r.Scheme); err != nil {
		return err
	}
	if err := r.Create(ctx, secret); err != nil {
		return fmt.Errorf("create DKIM secret: %w", err)
	}

	ed.Status.DKIMSecretName = secretName
	return nil
}

// updateAccountCount counts EmailAccount CRDs for this domain.
func (r *EmailDomainReconciler) updateAccountCount(ctx context.Context, ed *hostingv1alpha1.EmailDomain) error {
	accounts := &hostingv1alpha1.EmailAccountList{}
	if err := r.List(ctx, accounts, client.InNamespace(ed.Namespace),
		client.MatchingLabels{"hosting.panel/email-domain": ed.Spec.Domain}); err != nil {
		return err
	}
	ed.Status.AccountCount = int32(len(accounts.Items))
	return nil
}

func (r *EmailDomainReconciler) setError(ctx context.Context, ed *hostingv1alpha1.EmailDomain, reason string, err error) (ctrl.Result, error) {
	ed.Status.Phase = "Error"
	meta.SetStatusCondition(&ed.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            err.Error(),
		LastTransitionTime: metav1.Now(),
	})
	_ = r.Status().Update(ctx, ed)
	return ctrl.Result{RequeueAfter: 30 * time.Second}, err
}

func (r *EmailDomainReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&hostingv1alpha1.EmailDomain{}).
		Owns(&corev1.Secret{}).
		Named("emaildomain").
		Complete(r)
}
