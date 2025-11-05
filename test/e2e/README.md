# E2E Tests for Provider-Zarf

This directory contains end-to-end integration tests for provider-zarf that run against a real Kubernetes cluster.

## Prerequisites

### Required
- **Kubernetes cluster**: Kind, minikube, or any k8s cluster with KUBECONFIG set
- **kubectl**: Kubernetes CLI tool
- **Go 1.21+**: To run the test suite
- **Crossplane**: Installed in the cluster (provider-zarf is a Crossplane provider)

### Optional
- **Zarf CLI**: Required only if building test packages locally
- **Kind**: For local testing with ephemeral clusters

## Test Structure

### Component Order Tests (`component_order_test.go`)

Validates that the provider deploys Zarf package components in the exact order specified by users, not the order they appear in the package definition.

**Test scenarios:**
1. Deploy components in forward order (a → b → c)
2. Deploy components in reverse order (c → b → a)
3. Fail gracefully when requesting non-existent components

**Test package:** `examples/component-order-test/`
- Contains 3 components defined in order: c, b, a
- Each component creates a ConfigMap with order markers
- Tests verify ConfigMaps are created in user-requested order

## Running Tests

### Quick Start (Local Kind Cluster)

```bash
# 1. Create a Kind cluster
kind create cluster --name provider-zarf-test

# 2. Install Crossplane
helm repo add crossplane-stable https://charts.crossplane.io/stable
helm repo update
helm install crossplane crossplane-stable/crossplane \
  --namespace crossplane-system --create-namespace

# 3. Build and install provider-zarf
cd /path/to/provider-zarf
make build
make install  # Install CRDs

# 4. Deploy provider (adjust image as needed)
kubectl apply -f deploy/provider.yaml

# 5. Run E2E tests
./test/e2e/run-e2e.sh
```

### Using the Test Runner Script

The `run-e2e.sh` script automates test execution:

```bash
# Use local package path (default)
./test/e2e/run-e2e.sh

# Use OCI registry
TEST_PACKAGE_SOURCE=oci://ghcr.io/enel1221/test-packages/component-order-test:0.1.0 ./test/e2e/run-e2e.sh

# Use specific kubeconfig
KUBECONFIG=/path/to/kubeconfig ./test/e2e/run-e2e.sh
```

### Manual Test Execution

```bash
# Set package source (optional, defaults to local path)
export TEST_PACKAGE_SOURCE=examples/component-order-test

# Run tests with e2e build tag
go test -v -tags=e2e -timeout=10m ./test/e2e/... -ginkgo.v
```

### Makefile Integration

```bash
# Run E2E tests via Make (coming soon)
make test-e2e
```

## Building Test Packages

The component order test requires a Zarf package to be available. You can:

### Option 1: Use Local Package (Development)

Build the test package locally:

```bash
cd examples/component-order-test
zarf package create . --confirm --output-dir .
```

The test will use the local package path automatically.

### Option 2: Use OCI Registry (CI/CD)

Publish the test package to a registry:

```bash
# Build
cd examples/component-order-test
zarf package create . --confirm --output-dir .

# Publish to GHCR
zarf package publish zarf-package-component-order-test-multi-0.1.0.tar.zst \
  oci://ghcr.io/enel1221/test-packages

# Run tests pointing to OCI
TEST_PACKAGE_SOURCE=oci://ghcr.io/enel1221/test-packages/component-order-test:0.1.0 \
  go test -v -tags=e2e -timeout=10m ./test/e2e/...
```

## Test Architecture

### Test Flow

1. **Setup** (BeforeSuite):
   - Initialize Kubernetes clients
   - Create test namespace (`component-order-test`)

2. **Test Execution**:
   - Create `ZarfPackage` CR with specific component order
   - Wait for package to become ready (5min timeout)
   - Verify ConfigMaps created in correct namespace
   - Validate component order via creation timestamps

3. **Cleanup** (AfterEach):
   - Delete test ZarfPackage
   - Clean up test ConfigMaps

### Debugging Tests

Enable verbose Ginkgo output:

```bash
go test -v -tags=e2e ./test/e2e/... -ginkgo.v -ginkgo.progress
```

View test failure details:

```bash
# Check ZarfPackage status
kubectl get zarfpackage -o yaml

# Check controller logs
kubectl logs -n crossplane-system deployment/provider-zarf -f

# Check test namespace resources
kubectl get all,configmaps -n component-order-test
```

## CI/CD Integration

### GitHub Actions Example

```yaml
name: E2E Tests

on: [push, pull_request]

jobs:
  e2e:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      
      - name: Set up Go
        uses: actions/setup-go@v5
        with:
          go-version: '1.21'
      
      - name: Create Kind Cluster
        uses: helm/kind-action@v1.8.0
      
      - name: Install Crossplane
        run: |
          helm repo add crossplane-stable https://charts.crossplane.io/stable
          helm install crossplane crossplane-stable/crossplane \
            --namespace crossplane-system --create-namespace --wait
      
      - name: Build and Deploy Provider
        run: |
          make build
          make install
          kubectl apply -f deploy/provider.yaml
          kubectl wait --for=condition=Available deployment/provider-zarf \
            -n crossplane-system --timeout=300s
      
      - name: Run E2E Tests
        run: ./test/e2e/run-e2e.sh
```

## Troubleshooting

### Tests Timeout

**Problem**: Tests timeout waiting for package to become ready

**Solutions**:
- Check provider logs: `kubectl logs -n crossplane-system deployment/provider-zarf`
- Verify Zarf package is accessible: check image pull secrets for OCI sources
- Increase timeout in test code (default: 5 minutes)

### ConfigMaps Not Found

**Problem**: Tests can't find deployed ConfigMaps

**Solutions**:
- Check namespace: `kubectl get cm -n component-order-test`
- Verify package deployed: `kubectl get zarfpackage -o yaml`
- Check component filter logic in provider logs

### Package Creation Fails

**Problem**: Test package build fails

**Solutions**:
- Ensure Zarf CLI is installed: `zarf version`
- Check package definition: `cd examples/component-order-test && zarf package inspect .`
- Verify all manifest files exist

## Adding New E2E Tests

1. Create test file: `test/e2e/myfeature_test.go`
2. Add build tag: `//go:build e2e`
3. Use Ginkgo/Gomega framework
4. Follow existing test patterns
5. Add documentation to this README

## References

- [Ginkgo Testing Framework](https://onsi.github.io/ginkgo/)
- [Gomega Matchers](https://onsi.github.io/gomega/)
- [Zarf Documentation](https://docs.zarf.dev/)
- [Crossplane Provider Development](https://docs.crossplane.io/latest/contributing/provider-development-guide/)
