# ZarfPackage Examples

This directory contains ready-to-use `ZarfPackage` custom resources that demonstrate how to drive the provider. Each manifest can be applied after the provider and a matching `ProviderConfig` are installed.

- `dos-games.yaml` – deploys the public DOS games demo package.
- `bigbang.yaml` – deploys the Big Bang demo package with Crossplane management policies.
- `private-registry.yaml` – shows how to pull a package from a private OCI registry by referencing Docker credentials via `registrySecretRef`.

## Pulling packages from a private registry

The provider can authenticate to private OCI registries by reading standard Docker credential secrets. The flow has two parts:

1. **Create a Docker config secret** containing the registry credentials. You can reuse an existing secret or create a new one:
   ```shell
   kubectl create secret docker-registry private-registry-auth \
     --namespace crossplane-system \
     --docker-server ghcr.io \
     --docker-username <USERNAME> \
     --docker-password <TOKEN>
   ```

  If the secret lives outside of `crossplane-system` (the default lookup location), set `spec.forProvider.registrySecretRef.namespace` on your `ZarfPackage` so the controller knows where to fetch it from.

2. **Reference the secret from the `ZarfPackage`** using `spec.forProvider.registrySecretRef` (name and optional namespace). When the package is reconciled, the controller materializes a temporary `DOCKER_CONFIG` for the Zarf library so that the OCI download can complete.

See `private-registry.yaml` for a complete manifest that encapsulates both the secret and the `ZarfPackage` resource.
