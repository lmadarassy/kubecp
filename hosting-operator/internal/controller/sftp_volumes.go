package controller

import (
	"context"
	"fmt"
	"sort"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	hostingv1alpha1 "github.com/hosting-panel/hosting-operator/api/v1alpha1"
)

const (
	sftpDeploymentName = "hosting-panel-sftp"
	sftpNamespace      = "hosting-system"
)

// SyncSFTPVolumes updates ONLY the volume mounts on the SFTP deployment.
// It does NOT touch: init containers, container args, config, env vars, or anything else.
// Pure volume + volumeMount management for User_Volume PVCs.
//
// Called from the user create/delete handlers in Panel Core API,
// NOT from the website reconciler (to avoid per-reconcile churn).
func SyncSFTPVolumes(ctx context.Context, c client.Client) error {
	logger := log.FromContext(ctx)

	// Collect unique users from all websites
	allWebsites := &hostingv1alpha1.WebsiteList{}
	if err := c.List(ctx, allWebsites, client.InNamespace(sftpNamespace)); err != nil {
		return fmt.Errorf("list websites: %w", err)
	}

	userSet := make(map[string]bool)
	for _, w := range allWebsites.Items {
		owner := w.Spec.Owner
		if owner == "" {
			owner = w.Labels["hosting.panel/user"]
		}
		if owner != "" {
			userSet[owner] = true
		}
	}

	users := make([]string, 0, len(userSet))
	for u := range userSet {
		users = append(users, u)
	}
	sort.Strings(users)

	// Get SFTP deployment
	deploy := &appsv1.Deployment{}
	if err := c.Get(ctx, types.NamespacedName{Name: sftpDeploymentName, Namespace: sftpNamespace}, deploy); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("SFTP deployment not found, skipping volume sync")
			return nil
		}
		return fmt.Errorf("get SFTP deployment: %w", err)
	}

	podSpec := &deploy.Spec.Template.Spec

	// Separate base volumes (ssh-host-keys) from user volumes (uv-*)
	var baseVolumes []corev1.Volume
	for _, v := range podSpec.Volumes {
		if !strings.HasPrefix(v.Name, "uv-") {
			baseVolumes = append(baseVolumes, v)
		}
	}

	// Build user volumes and mounts
	var userVolumes []corev1.Volume
	var userMounts []corev1.VolumeMount
	for _, username := range users {
		volName := fmt.Sprintf("uv-%s", username)
		userVolumes = append(userVolumes, corev1.Volume{
			Name: volName,
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: UserVolumePVCName(username),
				},
			},
		})
		userMounts = append(userMounts, corev1.VolumeMount{
			Name:      volName,
			MountPath: fmt.Sprintf("/home/%s", username),
		})
	}

	podSpec.Volumes = append(baseVolumes, userVolumes...)

	// Update ONLY the sftp container's volume mounts
	for i := range podSpec.Containers {
		if podSpec.Containers[i].Name == "sftp" {
			// Keep non-user mounts (ssh-host-keys etc)
			var baseMounts []corev1.VolumeMount
			for _, m := range podSpec.Containers[i].VolumeMounts {
				if !strings.HasPrefix(m.Name, "uv-") {
					baseMounts = append(baseMounts, m)
				}
			}
			podSpec.Containers[i].VolumeMounts = append(baseMounts, userMounts...)
			break
		}
	}

	// Annotation — only triggers restart when user list actually changes
	userListHash := strings.Join(users, ",")
	if deploy.Spec.Template.Annotations == nil {
		deploy.Spec.Template.Annotations = make(map[string]string)
	}
	currentHash := deploy.Spec.Template.Annotations["hosting.panel/sftp-users"]
	if currentHash == userListHash {
		logger.V(1).Info("SFTP volumes unchanged, skipping update")
		return nil
	}
	deploy.Spec.Template.Annotations["hosting.panel/sftp-users"] = userListHash

	if err := c.Update(ctx, deploy); err != nil {
		return fmt.Errorf("update SFTP deployment volumes: %w", err)
	}

	logger.Info("Synced SFTP deployment volumes", "users", len(users))
	return nil
}
