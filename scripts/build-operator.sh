#!/bin/bash
set -e
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
IMAGE_NAME="hosting-panel/hosting-operator"
IMAGE_TAG="${1:-latest}"

echo "Building hosting-operator image: ${IMAGE_NAME}:${IMAGE_TAG}"
DOCKER_BUILDKIT=0 docker build -t "${IMAGE_NAME}:${IMAGE_TAG}" "${PROJECT_DIR}/hosting-operator"

echo "Importing into k3s..."
docker save "${IMAGE_NAME}:${IMAGE_TAG}" | k3s ctr images import -

echo "Done: ${IMAGE_NAME}:${IMAGE_TAG}"
