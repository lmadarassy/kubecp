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
	"context"
	"fmt"
	"strings"
	"time"

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

const emailAccountFinalizer = "hosting.panel/emailaccount-cleanup"

// EmailAccountReconciler reconciles an EmailAccount object
type EmailAccountReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=hosting.hosting.panel,resources=emailaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=hosting.hosting.panel,resources=emailaccounts/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=hosting.hosting.panel,resources=emailaccounts/finalizers,verbs=update
// +kubebuilder:rbac:groups=hosting.hosting.panel,resources=emaildomains,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch

// Reconcile moves the cluster state toward the desired state for an EmailAccount resource.
func (r *EmailAccountReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	email := &hostingv1alpha1.EmailAccount{}
	if err := r.Get(ctx, req.NamespacedName, email); err != nil {
		if errors.IsNotFound(err) {
			log.Info("EmailAccount resource not found, likely deleted")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Handle deletion
	if !email.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, email)
	}

	// Add finalizer
	if !controllerutil.ContainsFinalizer(email, emailAccountFinalizer) {
		controllerutil.AddFinalizer(email, emailAccountFinalizer)
		if err := r.Update(ctx, email); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Ensure hosting.panel/email-domain label is set
	if err := r.ensureLabels(ctx, email); err != nil {
		return ctrl.Result{}, err
	}

	// Re-fetch the object after ensureLabels may have updated it,
	// so subsequent operations use the latest resourceVersion.
	if err := r.Get(ctx, req.NamespacedName, email); err != nil {
		return ctrl.Result{}, err
	}

	// Validate that the referenced EmailDomain exists
	if err := r.validateEmailDomain(ctx, email); err != nil {
		return r.setErrorStatus(ctx, email, "DomainNotFound", err)
	}

	// Set phase to Creating if empty or Pending
	if email.Status.Phase == "" || email.Status.Phase == "Pending" {
		email.Status.Phase = "Creating"
		if err := r.Status().Update(ctx, email); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Set maildir path on User_Volume
	if err := r.reconcileMaildir(ctx, email); err != nil {
		return r.setErrorStatus(ctx, email, "MaildirFailed", err)
	}

	// Sync Dovecot passwd-file Secret (all accounts)
	// NOTE: Dovecot now authenticates via Keycloak checkpassword — this is a no-op
	// but kept for backward compatibility with deployments still using passwd-file.
	if err := r.reconcileDovecotPasswd(ctx, email.Namespace); err != nil {
		log.V(1).Info("Dovecot passwd sync skipped (Keycloak auth active)", "error", err)
	}

	// Update status to Active
	return r.updateActiveStatus(ctx, email)
}

// ensureLabels ensures the hosting.panel/email-domain label is set on the EmailAccount.
func (r *EmailAccountReconciler) ensureLabels(ctx context.Context, email *hostingv1alpha1.EmailAccount) error {
	labels := email.Labels
	if labels == nil {
		labels = make(map[string]string)
	}

	needsUpdate := false

	// Set email-domain label from spec.domain
	if labels["hosting.panel/email-domain"] != email.Spec.Domain {
		labels["hosting.panel/email-domain"] = email.Spec.Domain
		needsUpdate = true
	}

	// Derive owner from the parent EmailDomain
	if labels[UserLabel] == "" {
		ed := &hostingv1alpha1.EmailDomain{}
		edName := sanitizeDomainForK8s(email.Spec.Domain)
		if err := r.Get(ctx, types.NamespacedName{Name: edName, Namespace: email.Namespace}, ed); err == nil {
			labels[UserLabel] = ed.Spec.Owner
			needsUpdate = true
		}
	}

	if needsUpdate {
		email.Labels = labels
		return r.Update(ctx, email)
	}
	return nil
}

// validateEmailDomain checks that the referenced EmailDomain CRD exists and is Active.
func (r *EmailAccountReconciler) validateEmailDomain(ctx context.Context, email *hostingv1alpha1.EmailAccount) error {
	ed := &hostingv1alpha1.EmailDomain{}
	edName := sanitizeDomainForK8s(email.Spec.Domain)
	if err := r.Get(ctx, types.NamespacedName{Name: edName, Namespace: email.Namespace}, ed); err != nil {
		if errors.IsNotFound(err) {
			return fmt.Errorf("EmailDomain %q not found for domain %q", edName, email.Spec.Domain)
		}
		return err
	}
	if ed.Status.Phase != "Active" && ed.Status.Phase != "" {
		return fmt.Errorf("EmailDomain %q is not Active (phase: %s)", email.Spec.Domain, ed.Status.Phase)
	}
	return nil
}

// reconcileMaildir sets the maildir path in status. The actual directory is created
// by the mail system (Dovecot) on first delivery. The path follows the convention:
// mail/{domain}/{account}/ on the User_Volume.
func (r *EmailAccountReconciler) reconcileMaildir(ctx context.Context, email *hostingv1alpha1.EmailAccount) error {
	// Extract account name from address (part before @)
	accountName := email.Spec.Address
	if idx := strings.Index(email.Spec.Address, "@"); idx > 0 {
		accountName = email.Spec.Address[:idx]
	}

	expectedPath := fmt.Sprintf("mail/%s/%s/", email.Spec.Domain, accountName)
	if email.Status.MaildirPath != expectedPath {
		email.Status.MaildirPath = expectedPath
	}
	return nil
}

// reconcileDelete handles EmailAccount deletion.
func (r *EmailAccountReconciler) reconcileDelete(ctx context.Context, email *hostingv1alpha1.EmailAccount) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	email.Status.Phase = "Terminating"
	_ = r.Status().Update(ctx, email)

	log.Info("Removing email account", "address", email.Spec.Address, "domain", email.Spec.Domain)

	// Sync Dovecot passwd-file Secret after deletion
	_ = r.reconcileDovecotPasswd(ctx, email.Namespace)

	// TODO: Remove maildir from User_Volume (or leave for manual cleanup)
	// TODO: Remove LDAP/Keycloak identity for this email

	controllerutil.RemoveFinalizer(email, emailAccountFinalizer)
	if err := r.Update(ctx, email); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// updateActiveStatus sets the EmailAccount status to Active.
func (r *EmailAccountReconciler) updateActiveStatus(ctx context.Context, email *hostingv1alpha1.EmailAccount) (ctrl.Result, error) {
	email.Status.Phase = "Active"

	meta.SetStatusCondition(&email.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		Reason:             "Provisioned",
		Message:            "Email account provisioned successfully",
		LastTransitionTime: metav1.Now(),
	})

	if err := r.Status().Update(ctx, email); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// setErrorStatus updates the EmailAccount status to Error.
func (r *EmailAccountReconciler) setErrorStatus(ctx context.Context, email *hostingv1alpha1.EmailAccount, reason string, err error) (ctrl.Result, error) {
	email.Status.Phase = "Error"
	meta.SetStatusCondition(&email.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            err.Error(),
		LastTransitionTime: metav1.Now(),
	})
	_ = r.Status().Update(ctx, email)
	return ctrl.Result{RequeueAfter: 30 * time.Second}, err
}

// SetupWithManager sets up the controller with the Manager.
func (r *EmailAccountReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&hostingv1alpha1.EmailAccount{}).
		Named("emailaccount").
		Complete(r)
}
