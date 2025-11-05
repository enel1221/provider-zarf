# provider-zarf

`provider-zarf` is a [Crossplane](https://crossplane.io/) Provider that manages [Zarf](https://zarf.dev/) package deployments in Kubernetes clusters. It enables declarative management of Zarf packages through Kubernetes custom resources.

## Features

- **Declarative Zarf Deployment**: Manage Zarf packages using Kubernetes CRDs
- **OCI Registry Support**: Deploy Zarf packages from OCI registries
- **Component Selection**: Deploy specific components from Zarf packages
- **Variable Injection**: Pass variables to Zarf package deployments
- **Architecture Auto-detection**: Automatically detect cluster architecture (amd64/arm64) when not specified
- **Ordered Component Deployment**: Components are deployed in the order specified
- **Drift Detection**: Automatic detection and reconciliation of configuration changes
- **Integrated CLI Tools**: Includes `kubectl` and `busybox` for debugging and operations

## Installation

### Prerequisites

- Kubernetes cluster (v1.23+)
- [Crossplane](https://crossplane.io/) installed (v1.14+)
- Access to OCI registry containing Zarf packages

### Install the Provider

```yaml
apiVersion: pkg.crossplane.io/v1
kind: Provider
metadata:
  name: provider-zarf
spec:
  package: ghcr.io/enel1221/provider-zarf:latest
```

## Usage

### Basic Example

```yaml
apiVersion: zarf.dev/v1alpha1
kind: ZarfPackage
metadata:
  name: my-app
spec:
  forProvider:
    source: "oci://ghcr.io/my-org/my-zarf-package:v1.0.0"
    namespace: "default"
    components:
      - component-a
      - component-b
    variables:
      domain: "example.com"
      replicas: "3"
```

### Provider Configuration

Create a `ProviderConfig` to configure authentication and defaults:

```yaml
apiVersion: zarf.dev/v1alpha1
kind: ProviderConfig
metadata:
  name: default
spec:
  credentials:
    source: Secret
    secretRef:
      name: zarf-registry-credentials
      namespace: crossplane-system
      key: credentials
```

## Configuration

### Logging

The provider supports configurable logging levels for different operational needs:

#### Environment Variables

- `ZARF_LOG_LEVEL`: Set the log verbosity level
  - `info` (default): Standard operational logs with package name and status
  - `debug`: Verbose logging including Zarf library internals and detailed reconciliation steps
  - `warn`: Only warnings and errors
  - `error`: Only error messages

#### Examples

**Default (Info Level)**:
```yaml
# In provider deployment
env:
  - name: ZARF_LOG_LEVEL
    value: "info"
```

Logs will show:
- Package deployment start/completion
- Component deployment progress
- Status changes and updates
- Error conditions

**Debug Level**:
```yaml
env:
  - name: ZARF_LOG_LEVEL
    value: "debug"
```

Logs will additionally show:
- Zarf library debug output
- Detailed reconciliation logic
- OCI registry interactions
- Component filtering and ordering
- Spec hash computations

#### Command Line

You can also use the `--log-level` flag when running the provider binary directly:

```bash
provider --log-level=debug
```

Or use the legacy `--debug` flag for backward compatibility:

```bash
provider --debug  # Equivalent to --log-level=debug
```

#### Log Format

Logs include:
- Timestamp
- Log level
- Package name (ZarfPackage resource name)
- Contextual information (namespace, components, etc.)
- Message

Example log output:
```
2025-11-05T10:15:30.123Z INFO provider-zarf Reconciling ZarfPackage {"name": "my-app", "namespace": "default"}
2025-11-05T10:15:31.456Z INFO provider-zarf Package deployed successfully {"name": "my-app", "components": ["component-a", "component-b"]}
```

### Resource Specification

#### ZarfPackage Fields

- `source` (required): OCI reference to the Zarf package (e.g., `oci://ghcr.io/org/package:tag`)
- `architecture` (optional): Target architecture (`amd64` or `arm64`). **Auto-detected from cluster nodes if not specified** by querying `kubernetes.io/arch` labels
- `namespace` (optional): Kubernetes namespace for deployment (default: from package)
- `components` (optional): List of components to deploy in the specified order
- `variables` (optional): Map of variables to pass to the Zarf package
- `timeout` (optional): Deployment timeout (default: 15m)
- `skipWebhooks` (optional): Skip Zarf webhooks during deployment
- `adoptExistingResources` (optional): Adopt existing resources during deployment

#### Architecture Auto-Detection

When `architecture` is not specified, the provider automatically:
1. Queries cluster nodes for `kubernetes.io/arch` labels (or fallback `beta.kubernetes.io/arch`)
2. Selects the majority architecture from schedulable nodes (ignoring unschedulable nodes)
3. Defaults to `amd64` if labels are missing or ambiguous

This allows for portable ZarfPackage definitions that work across different cluster architectures without modification.

## Developing

1. Clone this repository
2. Run `make submodules` to initialize the build submodule
3. Run `make reviewable` to run code generation, linters, and tests
4. Run `make build` to build the provider binary
5. Run `make docker-build` to build the container image

### Running Tests

```bash
# Unit tests
make test

# E2E tests (requires kind cluster)
make test-e2e
```

### Building

```bash
# Build binary
make build

# Build and package for Crossplane
make build.all
```

## Architecture

The provider consists of:

- **ZarfPackage Controller**: Reconciles ZarfPackage resources
- **Zarf Client**: Wraps the Zarf library for package operations
- **OCI Integration**: Handles package pulling from OCI registries
- **Drift Detection**: SHA256-based fingerprinting of package specifications

For more details, see the [.github/copilot-instructions.md](.github/copilot-instructions.md) development guide.

## Contributing

Refer to Crossplane's [CONTRIBUTING.md](https://github.com/crossplane/crossplane/blob/master/CONTRIBUTING.md) file for information on how the Crossplane community prefers to work.

The [Provider Development](https://github.com/crossplane/crossplane/blob/master/contributing/guide-provider-development.md) guide may also be useful.

## License

provider-zarf is under the Apache 2.0 license.
