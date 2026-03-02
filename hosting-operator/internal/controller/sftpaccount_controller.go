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
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	hostingv1alpha1 "github.com/hosting-panel/hosting-operator/api/v1alpha1"
)

const sftpAccountFinalizer = "hosting.panel/sftpaccount-cleanup"

// SFTPAccountReconciler reconciles an SFTPAccount object
type SFTPAccountReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=hosting.hosting.panel,resources=sftpaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=hosting.hosting.panel,resources=sftpaccounts/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=hosting.hosting.panel,resources=sftpaccounts/finalizers,verbs=update

// Reconcile moves the cluster state toward the desired state for an SFTPAccount resource.
func (r *SFTPAccountReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	sftp := &hostingv1alpha1.SFTPAccount{}
	if err := r.Get(ctx, req.NamespacedName, sftp); err != nil {
		if errors.IsNotFound(err) {
			log.Info("SFTPAccount resource not found, likely deleted")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Handle deletion
	if !sftp.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, sftp)
	}

	// Add finalizer
	if !controllerutil.ContainsFinalizer(sftp, sftpAccountFinalizer) {
		controllerutil.AddFinalizer(sftp, sftpAccountFinalizer)
		if err := r.Update(ctx, sftp); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Provision SFTP access
	if err := r.provisionSFTPAccess(ctx, sftp); err != nil {
		return r.setErrorStatus(ctx, sftp, "ProvisionFailed", err)
	}

	// Update status to Active
	return r.updateActiveStatus(ctx, sftp)
}

// reconcileDelete handles SFTPAccount deletion.
func (r *SFTPAccountReconciler) reconcileDelete(ctx context.Context, sftp *hostingv1alpha1.SFTPAccount) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	sftp.Status.Phase = "Terminating"
	_ = r.Status().Update(ctx, sftp)

	log.Info("Removing SFTP access", "username", sftp.Spec.Username)

	// TODO: Remove SFTP access configuration
	// TODO: Remove chroot directory permissions

	controllerutil.RemoveFinalizer(sftp, sftpAccountFinalizer)
	if err := r.Update(ctx, sftp); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// provisionSFTPAccess configures SFTP access and chroot for the user.
func (r *SFTPAccountReconciler) provisionSFTPAccess(ctx context.Context, sftp *hostingv1alpha1.SFTPAccount) error {
	_ = logf.FromContext(ctx)

	// TODO: Configure SFTP Pod to allow this user
	// TODO: Set up chroot to restrict access to allowedPaths only
	// TODO: Mount the same Longhorn RWX PVs as the Website Pods

	return nil
}

// updateActiveStatus sets the SFTPAccount status to Active.
func (r *SFTPAccountReconciler) updateActiveStatus(ctx context.Context, sftp *hostingv1alpha1.SFTPAccount) (ctrl.Result, error) {
	sftp.Status.Phase = "Active"

	meta.SetStatusCondition(&sftp.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		Reason:             "Provisioned",
		Message:            "SFTP access configured successfully",
		LastTransitionTime: metav1.Now(),
	})

	if err := r.Status().Update(ctx, sftp); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// setErrorStatus updates the SFTPAccount status to Error.
func (r *SFTPAccountReconciler) setErrorStatus(ctx context.Context, sftp *hostingv1alpha1.SFTPAccount, reason string, err error) (ctrl.Result, error) {
	sftp.Status.Phase = "Error"
	meta.SetStatusCondition(&sftp.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            err.Error(),
		LastTransitionTime: metav1.Now(),
	})
	_ = r.Status().Update(ctx, sftp)
	return ctrl.Result{RequeueAfter: 30 * time.Second}, err
}

// SetupWithManager sets up the controller with the Manager.
func (r *SFTPAccountReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&hostingv1alpha1.SFTPAccount{}).
		Named("sftpaccount").
		Complete(r)
}
