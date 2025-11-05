#!/bin/bash
# Run E2E tests for provider-zarf component ordering
#
# Prerequisites:
# 1. Kind cluster running (or other k8s cluster accessible via KUBECONFIG)
# 2. Provider-zarf deployed to cluster with CRDs installed
# 3. Zarf CLI installed (to build test package)
#
# Usage:
#   ./run-e2e.sh              # Use local package path
#   TEST_PACKAGE_SOURCE=oci://... ./run-e2e.sh  # Use OCI registry

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
EXAMPLES_DIR="${REPO_ROOT}/examples/component-order-test"

echo "==> Checking prerequisites..."

# Check if kind cluster exists
if ! kind get clusters 2>/dev/null | grep -q .; then
    echo "Warning: No kind clusters found. Ensure you have a Kubernetes cluster available."
fi

# Check if kubectl is available
if ! command -v kubectl &> /dev/null; then
    echo "Error: kubectl not found in PATH"
    exit 1
fi

# Check cluster connectivity
if ! kubectl cluster-info &> /dev/null; then
    echo "Error: Cannot connect to Kubernetes cluster"
    exit 1
fi

echo "==> Building test package..."
cd "${EXAMPLES_DIR}"

# Check if zarf CLI is available
if command -v zarf &> /dev/null; then
    if [ ! -f "zarf-package-component-order-test-multi-0.1.0.tar.zst" ]; then
        echo "Building Zarf package..."
        zarf package create . --confirm --output-dir .
    else
        echo "Package already exists, skipping build"
    fi
else
    echo "Warning: zarf CLI not found. Skipping package build."
    echo "You can:"
    echo "  1. Install zarf CLI and run this script again"
    echo "  2. Set TEST_PACKAGE_SOURCE to an OCI registry URL"
    echo "  3. Build the package manually: cd ${EXAMPLES_DIR} && zarf package create . --confirm"
fi

cd "${REPO_ROOT}"

echo "==> Ensuring provider-zarf CRDs are installed..."
if ! kubectl get crd zarfpackages.zarf.dev &> /dev/null; then
    echo "Warning: ZarfPackage CRD not found. Installing CRDs..."
    kubectl apply -f package/crds/zarf.dev_zarfpackages.yaml
fi

echo "==> Ensuring test namespace exists..."
kubectl create namespace component-order-test --dry-run=client -o yaml | kubectl apply -f -

echo "==> Running E2E tests..."
cd "${REPO_ROOT}"

# Set default test package source to local path if not specified
export TEST_PACKAGE_SOURCE="${TEST_PACKAGE_SOURCE:-${EXAMPLES_DIR}}"

# Run tests with e2e build tag
go test -v -tags=e2e -timeout=10m ./test/e2e/... -ginkgo.v -ginkgo.fail-fast

echo "==> E2E tests completed successfully!"
