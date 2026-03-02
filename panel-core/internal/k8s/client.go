package k8s

import (
	"fmt"
	"os"
	"path/filepath"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// NewClientset creates a Kubernetes clientset.
// It first tries in-cluster config (for running inside k8s),
// then falls back to kubeconfig from KUBECONFIG env var or ~/.kube/config.
func NewClientset() (kubernetes.Interface, error) {
	config, err := getRESTConfig()
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(config)
}

// NewDynamicClient creates a Kubernetes dynamic client for working with CRDs.
// It uses the same config resolution as NewClientset.
func NewDynamicClient() (dynamic.Interface, error) {
	config, err := getRESTConfig()
	if err != nil {
		return nil, err
	}
	return dynamic.NewForConfig(config)
}

// getRESTConfig resolves the Kubernetes REST config.
// It first tries in-cluster config, then falls back to kubeconfig.
func getRESTConfig() (*rest.Config, error) {
	// Try in-cluster config first
	config, err := rest.InClusterConfig()
	if err == nil {
		return config, nil
	}

	// Fall back to kubeconfig
	kubeconfigPath := os.Getenv("KUBECONFIG")
	if kubeconfigPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to determine home directory: %w", err)
		}
		kubeconfigPath = filepath.Join(home, ".kube", "config")
	}

	config, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("failed to build kubeconfig: %w", err)
	}

	return config, nil
}
