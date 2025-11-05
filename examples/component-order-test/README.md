# Component Order Test Package

This test package validates that the provider-zarf controller deploys Zarf package components in the exact order specified by the user, not the order they appear in the package definition.

## Package Structure

The `zarf.yaml` defines three components in this order:
1. `component-c` (defined first)
2. `component-b` (defined second)
3. `component-a` (defined third)

Each component creates a ConfigMap with labels indicating its order.

## Testing Strategy

When requesting components in the order `["component-a", "component-b", "component-c"]`, the provider should deploy them in that exact order (A → B → C), regardless of their definition order in the package.

## Building the Package

From this directory:

```bash
# Build the package (requires zarf CLI)
zarf package create . --confirm --output-dir .

# This creates: zarf-package-component-order-test-multi-0.1.0.tar.zst
```

## Publishing to OCI Registry

```bash
# Publish to a registry
zarf package publish zarf-package-component-order-test-multi-0.1.0.tar.zst oci://ghcr.io/enel1221/test-packages

# Or use docker registry for local testing
zarf package publish zarf-package-component-order-test-multi-0.1.0.tar.zst oci://localhost:5000/test
```

## Using in Integration Tests

The integration test (`test/e2e/component_order_test.go`) will:
1. Deploy this package with components in reverse order: `["component-a", "component-b", "component-c"]`
2. Wait for deployment completion
3. Verify ConfigMaps were created in the correct namespace
4. Validate that components deployed in the requested order (not package definition order)

## Manual Testing

```yaml
apiVersion: zarf.dev/v1alpha1
kind: ZarfPackage
metadata:
  name: test-component-order
spec:
  forProvider:
    source: oci://ghcr.io/enel1221/test-packages/component-order-test:0.1.0
    components:
      - component-a
      - component-b
      - component-c
```

Apply this and check the logs/status to verify deployment order.
