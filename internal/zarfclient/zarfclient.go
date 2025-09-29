// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2023-Present The Zarf Operator Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package zarfclient

// Zarf Client - Production-ready wrapper around the Zarf library
//
// This package provides a clean interface between the Kubernetes operator and
// the Zarf library, handling:
//
// CORE RESPONSIBILITIES:
// - Package deployment from various sources (OCI, HTTP, files, cluster)
// - Package removal with proper cleanup
// - Installation status checking
// - Logger integration (slog ↔ logr bridge)
// - In-cluster Kubernetes configuration
//
// DESIGN PRINCIPLES:
// - Minimal API surface (only what the controller needs)
// - Production-ready error handling and logging
// - Proper context propagation for timeouts and cancellation
// - Clean separation between Kubernetes and Zarf concerns

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	// Logging integration
	"github.com/go-logr/logr"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	// Zarf library imports
	"github.com/zarf-dev/zarf/src/config"
	"github.com/zarf-dev/zarf/src/pkg/cluster"
	"github.com/zarf-dev/zarf/src/pkg/logger"
	"github.com/zarf-dev/zarf/src/pkg/packager"
	"github.com/zarf-dev/zarf/src/pkg/packager/filters"
	"github.com/zarf-dev/zarf/src/pkg/state"

	// Kubernetes client
	"k8s.io/client-go/rest"
)

// =============================================================================
// TYPE DEFINITIONS
// =============================================================================

// DeployOptions contains all configuration needed to deploy a Zarf package.
// These options map directly to the ZarfPackage CRD spec fields.
type DeployOptions struct {
	Source                  string            // Package source (OCI, HTTP, file, cluster name)
	Namespace               string            // Target namespace override
	Components              []string          // Specific components to deploy
	Variables               map[string]string // Deployment variables (KEY=VALUE)
	AdoptExisting           bool              // Adopt existing resources
	SkipSignatureValidation bool              // Skip package signature validation
	Shasum                  string            // Expected package checksum
	Architecture            string            // Target architecture (amd64, arm64)
	PublicKeyPath           string            // Path to public key for validation
	OCIConcurrency          int               // OCI operation concurrency
	Retries                 *int              // Number of retries for failed operations
	Timeout                 *time.Duration    // Deployment timeout
	PlainHTTP               bool              // Use HTTP instead of HTTPS
	InsecureSkipTLSVerify   bool              // Skip TLS certificate verification
}

// Result contains information about a successful deployment.
type Result struct {
	PackageName string    // Name of the deployed package
	DeployedAt  time.Time // Timestamp of deployment completion
}

// Client defines the interface for Zarf operations needed by the controller.
type Client interface {
	// IsInstalled checks if a package is already deployed in the cluster
	IsInstalled(ctx context.Context, src string, namespaceOverride string) (bool, *state.DeployedPackage, error)

	// Deploy installs a Zarf package with the given options
	Deploy(ctx context.Context, opts DeployOptions) (Result, error)

	// Remove uninstalls a previously deployed Zarf package
	Remove(ctx context.Context, packageName string, namespaceOverride string) error
}

// =============================================================================
// CLIENT IMPLEMENTATION
// =============================================================================

// client implements the Client interface
type client struct{}

// =============================================================================
// LOGGER BRIDGE (slog ↔ logr)
// =============================================================================

// logrSlogBridge bridges Zarf's slog logging with the controller's logr logging.
// This ensures all Zarf library logs appear in the controller logs with proper formatting.
type logrSlogBridge struct {
	logger logr.Logger
}

// Write implements io.Writer to capture slog JSON output and forward to logr.
func (w *logrSlogBridge) Write(p []byte) (n int, err error) {
	msg := strings.TrimSpace(string(p))
	if msg == "" {
		return len(p), nil
	}

	// Parse JSON output from slog and route to appropriate logr level
	if strings.HasPrefix(msg, "{") {
		// JSON format from slog - extract level
		switch {
		case strings.Contains(msg, `"level":"DEBUG"`) || strings.Contains(msg, `"level":"debug"`):
			w.logger.V(1).Info(msg)
		case strings.Contains(msg, `"level":"ERROR"`) || strings.Contains(msg, `"level":"error"`):
			w.logger.Error(nil, msg)
		case strings.Contains(msg, `"level":"WARN"`) || strings.Contains(msg, `"level":"warn"`):
			w.logger.Info("WARN: " + msg)
		default:
			// INFO or unknown level
			w.logger.Info(msg)
		}
	} else {
		// Plain text message
		w.logger.Info(msg)
	}
	return len(p), nil
}

// =============================================================================
// CONSTANTS
// =============================================================================

const ociPrefix = "oci://"

// ensureInClusterConfig configures the environment to force Zarf library to use in-cluster configuration
// The Zarf library's ClientAndConfig() function uses clientcmd loading rules that check for kubeconfig files
// We force it to use in-cluster config by ensuring no kubeconfig files are found
func ensureInClusterConfig() error {
	// Unset KUBECONFIG environment variable to prevent file-based config
	if err := os.Unsetenv("KUBECONFIG"); err != nil {
		return fmt.Errorf("failed to unset KUBECONFIG: %w", err)
	}

	// Test that we can create in-cluster config
	_, err := rest.InClusterConfig()
	if err != nil {
		return fmt.Errorf("not running in cluster or service account not configured: %w", err)
	}

	// Use the Zarf logger properly
	logger.Default().Debug("Successfully configured for in-cluster Kubernetes access")
	return nil
}

// New creates a new zarf library wrapper client with production-appropriate logging.
func New() Client {
	// Get the controller logger
	ctrlLogger := ctrllog.Log.WithName("zarf-library")

	// Create a bridge to forward Zarf logs to controller logger
	bridge := &logrSlogBridge{logger: ctrlLogger}

	// Configure Zarf logger to output to our bridge
	cfg := logger.ConfigDefault()
	cfg.Level = logger.Debug       // Debug level to see all Zarf operations
	cfg.Format = logger.FormatJSON // Use JSON so we can parse it
	cfg.Destination = bridge       // Send to our bridge instead of stderr
	l, err := logger.New(cfg)
	if err != nil {
		ctrlLogger.Error(err, "Failed to configure Zarf logger, using default")
		l = logger.Default()
	}
	logger.SetDefault(l)

	// Also create a context-aware logger for Zarf operations
	slogLogger := slog.New(slog.NewJSONHandler(bridge, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	// CRITICAL: Configure for in-cluster Kubernetes access
	// When running in a Kubernetes pod, clear KUBECONFIG to force client-go
	// to use in-cluster configuration (service account token)
	if _, exists := os.LookupEnv("KUBERNETES_SERVICE_HOST"); exists {
		// We're running in a pod, ensure KUBECONFIG is not set
		_ = os.Unsetenv("KUBECONFIG")
		slogLogger.Debug("Configured for in-cluster Kubernetes access")
	}

	return &client{}
}

// IsInstalled returns true if a deployed package secret already exists matching the provided source/package name.
// Source can be an OCI ref or a plain package name. We only treat plain names as already-installed lookups (cluster type).
func (c *client) IsInstalled(ctx context.Context, src string, namespaceOverride string) (bool, *state.DeployedPackage, error) {
	name, isClusterName := inferClusterPackageName(src)
	if !isClusterName {
		// For OCI / tar / http sources we cannot conclude installation without deeper inspection.
		// Simplest: attempt lookup by inferred package metadata name after optional load metadata.
		// Optimization: Return false; actual duplicate prevention occurs after first deploy when user switches to name.
		return false, nil, nil
	}
	cl, err := cluster.New(ctx)
	if err != nil {
		return false, nil, err
	}
	dep, err := cl.GetDeployedPackage(ctx, name, state.WithPackageNamespaceOverride(namespaceOverride))
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return false, nil, nil
		}
		return false, nil, err
	}
	return true, dep, nil
}

// Deploy performs: load package -> apply component filters -> deploy.
func (c *client) Deploy(ctx context.Context, opts DeployOptions) (Result, error) {
	// Get controller logger from context
	ctrlLogger := ctrllog.FromContext(ctx).WithName("zarf-deploy")

	// Create slog logger that forwards to controller logger
	bridge := &logrSlogBridge{logger: ctrlLogger}
	slogLogger := slog.New(slog.NewJSONHandler(bridge, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	// Create context with slog logger for Zarf library
	deployCtx := logger.WithContext(ctx, slogLogger)

	l := logger.From(deployCtx)
	l.Debug("zarfclient: Deploy called", "source", opts.Source)

	if opts.Source == "" {
		return Result{}, errors.New("source required")
	}
	source := normalizeSource(opts.Source)
	l.Debug("zarfclient: normalized source", "source", source)

	// Set the global confirmation flag to true to bypass interactive prompts.
	config.CommonOptions.Confirm = true

	// CRITICAL: Force in-cluster configuration usage for Zarf library
	// The Zarf library uses clientcmd.NewDefaultClientConfigLoadingRules() which looks for:
	// 1. KUBECONFIG environment variable
	// 2. ~/.kube/config file
	// 3. In-cluster config as fallback
	// We need to ensure no kubeconfig files exist so it falls back to in-cluster
	if err := ensureInClusterConfig(); err != nil {
		l.Error("Failed to configure in-cluster access", "error", err)
		return Result{}, fmt.Errorf("configure in-cluster access: %w", err)
	}
	l.Debug("Configured Zarf library for in-cluster access")

	// Component selection -> component filter strategy.
	filter := filters.Empty()
	if len(opts.Components) > 0 {
		// Reuse CLI selection semantics via BySelectState (comma separated, supports exclusions/globs per Zarf rules)
		filter = filters.BySelectState(strings.Join(opts.Components, ","))
	}

	layoutOpts := packager.LoadOptions{
		Shasum:                  opts.Shasum,
		Architecture:            opts.Architecture,
		PublicKeyPath:           opts.PublicKeyPath,
		SkipSignatureValidation: opts.SkipSignatureValidation,
		Filter:                  filter,
		OCIConcurrency:          opts.OCIConcurrency,
		RemoteOptions: packager.RemoteOptions{
			PlainHTTP:             opts.PlainHTTP,
			InsecureSkipTLSVerify: opts.InsecureSkipTLSVerify,
		},
	}

	l.Debug("zarfclient: loading package")
	// Use the context with logger for all Zarf operations
	pkgLayout, err := packager.LoadPackage(deployCtx, source, layoutOpts)
	if err != nil {
		l.Error("zarfclient: failed to load package", "error", err)
		return Result{}, fmt.Errorf("load package: %w", err)
	}
	defer pkgLayout.Cleanup() //nolint:errcheck
	l.Info("zarfclient: package loaded successfully", "packageName", pkgLayout.Pkg.Metadata.Name)

	// Map DeployOptions
	deployOpts := packager.DeployOptions{
		SetVariables:           opts.Variables,
		AdoptExistingResources: opts.AdoptExisting,
		NamespaceOverride:      opts.Namespace,
		RemoteOptions: packager.RemoteOptions{
			PlainHTTP:             opts.PlainHTTP,
			InsecureSkipTLSVerify: opts.InsecureSkipTLSVerify,
		},
		OCIConcurrency: opts.OCIConcurrency,
	}
	if opts.Retries != nil {
		deployOpts.Retries = *opts.Retries
	}
	if opts.Timeout != nil {
		deployOpts.Timeout = *opts.Timeout
	} else {
		// Use the same default as Zarf CLI (30 minutes)
		deployOpts.Timeout = 30 * time.Minute
	}

	// Pre-establish cluster connection with extended timeout
	// The Zarf library uses a 30-second hardcoded timeout internally, so we establish
	// the connection first with our full deployment timeout to avoid premature failures
	clusterCtx, clusterCancel := context.WithTimeout(deployCtx, deployOpts.Timeout)
	defer clusterCancel()

	_, clusterErr := cluster.NewWithWait(clusterCtx)
	if clusterErr != nil {
		l.Error("zarfclient: cluster connection failed", "error", clusterErr, "timeout", deployOpts.Timeout)
		return Result{}, fmt.Errorf("cluster connection: %w", clusterErr)
	}
	l.Debug("zarfclient: cluster connection established")

	l.Info("zarfclient: starting packager.Deploy", "timeout", deployOpts.Timeout)
	// Use the context with logger for the actual deployment
	_, err = packager.Deploy(deployCtx, pkgLayout, deployOpts)
	if err != nil {
		l.Error("zarfclient: packager.Deploy failed", "error", err, "timeout", deployOpts.Timeout)
		return Result{}, fmt.Errorf("deploy: %w", err)
	}

	l.Info("zarfclient: deployment successful", "packageName", pkgLayout.Pkg.Metadata.Name)
	return Result{PackageName: pkgLayout.Pkg.Metadata.Name, DeployedAt: time.Now()}, nil

}

// Remove loads metadata for a deployed package and dispatches packager.Remove.
func (c *client) Remove(ctx context.Context, packageName string, namespaceOverride string) error {
	// Get controller logger from context
	ctrlLogger := ctrllog.FromContext(ctx).WithName("zarf-remove")

	// Create slog logger that forwards to controller logger
	bridge := &logrSlogBridge{logger: ctrlLogger}
	slogLogger := slog.New(slog.NewJSONHandler(bridge, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	// Create context with slog logger for Zarf library
	removeCtx := logger.WithContext(ctx, slogLogger)

	l := logger.From(removeCtx)
	l.Debug("zarfclient: Remove called", "packageName", packageName)

	if packageName == "" {
		return errors.New("package name required")
	}

	// Set the global confirmation flag to true to bypass interactive prompts.
	config.CommonOptions.Confirm = true

	normalized := normalizeSource(packageName)
	l.Debug("zarfclient: normalized package name", "normalized", normalized)

	cl, err := cluster.NewWithWait(removeCtx)
	if err != nil {
		return fmt.Errorf("cluster connect: %w", err)
	}

	// Retrieve minimal package metadata from cluster secrets.
	pkgMeta, err := packager.GetPackageFromSourceOrCluster(removeCtx, cl, normalized, namespaceOverride, packager.LoadOptions{})
	if err != nil {
		return fmt.Errorf("lookup deployed package: %w", err)
	}

	l.Info("zarfclient: starting package removal", "packageName", pkgMeta.Metadata.Name)
	err = packager.Remove(removeCtx, pkgMeta, packager.RemoveOptions{Cluster: cl, NamespaceOverride: namespaceOverride, Timeout: 5 * time.Minute})
	if err != nil {
		return fmt.Errorf("remove package: %w", err)
	}

	l.Info("zarfclient: package removal successful", "packageName", pkgMeta.Metadata.Name)
	return nil
}

// inferClusterPackageName returns (name, true) for strings that are valid deployed name style.
func inferClusterPackageName(src string) (string, bool) {
	// Heuristic: OCI refs contain '/' and ':' or '@'. Names for deployed packages are simple (lowercase, digits, hyphen) per lint.
	if strings.Contains(src, "/") || strings.ContainsAny(src, ":@") {
		return src, false
	}
	// If looks like a simple name treat directly as cluster package name.
	return src, true
}

func normalizeSource(src string) string {
	if src == "" {
		return src
	}
	if _, isCluster := inferClusterPackageName(src); isCluster {
		return src
	}
	if strings.HasPrefix(src, ociPrefix) {
		return src
	}
	return ociPrefix + src
}
