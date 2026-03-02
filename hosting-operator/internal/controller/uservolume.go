package controller

import (
	"context"
	"fmt"
	"os"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	// UserVolumePVCPrefix is the prefix for User_Volume PVC names: uv-{username}
	UserVolumePVCPrefix = "uv-"

	// UserVolumeStorageClass is the default storage class for User_Volumes
	UserVolumeStorageClass = "longhorn"

	// UserVolumeLabel is the label identifying User_Volume PVCs
	UserVolumeLabel = "hosting.panel/user-volume"

	// UserLabel is the label identifying the owning user
	UserLabel = "hosting.panel/user"
)

// UserVolumePVCName returns the PVC name for a given username.
func UserVolumePVCName(username string) string {
	return UserVolumePVCPrefix + username
}

// EnsureUserVolume creates or verifies the User_Volume PVC for a user.
// Each user gets a single Longhorn RWX PVC named uv-{username}.
// The PVC contains: /web/{domain}/ for websites and /mail/{domain}/{account}/ for email.
func EnsureUserVolume(ctx context.Context, c client.Client, namespace, username string, storageGB int32) error {
	logger := log.FromContext(ctx)
	pvcName := UserVolumePVCName(username)

	// Check if PVC already exists
	existing := &corev1.PersistentVolumeClaim{}
	err := c.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: namespace}, existing)
	if err == nil {
		// PVC exists, nothing to do
		logger.V(1).Info("User_Volume PVC already exists", "pvc", pvcName, "user", username)
		return nil
	}
	if !errors.IsNotFound(err) {
		return fmt.Errorf("failed to check User_Volume PVC %s: %w", pvcName, err)
	}

	// Create new PVC
	storageSize := resource.MustParse(fmt.Sprintf("%dGi", storageGB))
	storageClass := os.Getenv("STORAGE_CLASS")
	if storageClass == "" {
		storageClass = UserVolumeStorageClass
	}

	// local-path only supports RWO; Longhorn supports RWX
	accessMode := corev1.ReadWriteMany
	if storageClass == "local-path" {
		accessMode = corev1.ReadWriteOnce
	}

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcName,
			Namespace: namespace,
			Labels: map[string]string{
				UserVolumeLabel: "true",
				UserLabel:       username,
			},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      []corev1.PersistentVolumeAccessMode{accessMode},
			StorageClassName: &storageClass,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: storageSize,
				},
			},
		},
	}

	if err := c.Create(ctx, pvc); err != nil {
		return fmt.Errorf("failed to create User_Volume PVC %s: %w", pvcName, err)
	}

	logger.Info("Created User_Volume PVC", "pvc", pvcName, "user", username, "size", fmt.Sprintf("%dGi", storageGB))
	return nil
}

// DeleteUserVolume deletes the User_Volume PVC for a user.
// Should only be called when the user is being fully removed.
func DeleteUserVolume(ctx context.Context, c client.Client, namespace, username string) error {
	logger := log.FromContext(ctx)
	pvcName := UserVolumePVCName(username)

	pvc := &corev1.PersistentVolumeClaim{}
	err := c.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: namespace}, pvc)
	if errors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to get User_Volume PVC %s: %w", pvcName, err)
	}

	if err := c.Delete(ctx, pvc); err != nil {
		return fmt.Errorf("failed to delete User_Volume PVC %s: %w", pvcName, err)
	}

	logger.Info("Deleted User_Volume PVC", "pvc", pvcName, "user", username)
	return nil
}

// GetUserVolumePVC retrieves the User_Volume PVC for a user.
func GetUserVolumePVC(ctx context.Context, c client.Client, namespace, username string) (*corev1.PersistentVolumeClaim, error) {
	pvcName := UserVolumePVCName(username)
	pvc := &corev1.PersistentVolumeClaim{}
	err := c.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: namespace}, pvc)
	if err != nil {
		return nil, err
	}
	return pvc, nil
}
