package api

import (
	"context"
	"log"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
)

const (
	// CRDGroup is the API group for hosting CRDs.
	CRDGroup = "hosting.hosting.panel"
	// CRDVersion is the API version for hosting CRDs.
	CRDVersion = "v1alpha1"
)

// GVR constants for each hosting CRD resource.
var (
	WebsiteGVR = schema.GroupVersionResource{
		Group:    CRDGroup,
		Version:  CRDVersion,
		Resource: "websites",
	}
	DatabaseGVR = schema.GroupVersionResource{
		Group:    CRDGroup,
		Version:  CRDVersion,
		Resource: "databases",
	}
	EmailAccountGVR = schema.GroupVersionResource{
		Group:    CRDGroup,
		Version:  CRDVersion,
		Resource: "emailaccounts",
	}
	EmailDomainGVR = schema.GroupVersionResource{
		Group:    CRDGroup,
		Version:  CRDVersion,
		Resource: "emaildomains",
	}
	HostingPlanGVR = schema.GroupVersionResource{
		Group:    CRDGroup,
		Version:  CRDVersion,
		Resource: "hostingplans",
	}
	SFTPAccountGVR = schema.GroupVersionResource{
		Group:    CRDGroup,
		Version:  CRDVersion,
		Resource: "sftpaccounts",
	}
)


// EnsureNamespace creates the given namespace if it does not already exist.
// It uses the kubernetes clientset to create core/v1 Namespace objects.
func EnsureNamespace(ctx context.Context, clientset kubernetes.Interface, namespace string) error {
	_, err := clientset.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
	if err == nil {
		return nil // already exists
	}
	if !errors.IsNotFound(err) {
		return err
	}

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "hosting-panel",
				"hosting-panel/type":           "user-namespace",
			},
		},
	}
	_, err = clientset.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	}
	log.Printf("Created user namespace: %s", namespace)
	return nil
}
