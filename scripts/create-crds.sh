#!/bin/bash
# Create CRD APIs with Kubebuilder inside a Go container
# Run this on the k3s machine after setup-operator.sh
# Usage: ./scripts/create-crds.sh
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
OPERATOR_DIR="${PROJECT_DIR}/hosting-operator"

echo "=== Creating CRD APIs with Kubebuilder ==="

docker run --rm \
    -v "${OPERATOR_DIR}:/workspace" \
    -w /workspace \
    golang:latest bash -c '
        # Install kubebuilder
        curl -L -o /usr/local/bin/kubebuilder \
            https://go.kubebuilder.io/dl/latest/linux/amd64 2>/dev/null
        chmod +x /usr/local/bin/kubebuilder

        # Create Website API
        echo "--- Creating Website CRD ---"
        kubebuilder create api \
            --group hosting \
            --version v1alpha1 \
            --kind Website \
            --resource --controller

        # Create Database API
        echo "--- Creating Database CRD ---"
        kubebuilder create api \
            --group hosting \
            --version v1alpha1 \
            --kind Database \
            --resource --controller

        # Create EmailAccount API
        echo "--- Creating EmailAccount CRD ---"
        kubebuilder create api \
            --group hosting \
            --version v1alpha1 \
            --kind EmailAccount \
            --resource --controller

        # Create SFTPAccount API
        echo "--- Creating SFTPAccount CRD ---"
        kubebuilder create api \
            --group hosting \
            --version v1alpha1 \
            --kind SFTPAccount \
            --resource --controller

        # Create HostingPlan API (resource only, no controller)
        echo "--- Creating HostingPlan CRD ---"
        kubebuilder create api \
            --group hosting \
            --version v1alpha1 \
            --kind HostingPlan \
            --resource --controller=false

        echo "=== All CRD APIs created ==="
        ls -la api/v1alpha1/
    '

echo "=== CRD scaffolding complete ==="
echo "Next: edit the type files in hosting-operator/api/v1alpha1/ and run make manifests"
