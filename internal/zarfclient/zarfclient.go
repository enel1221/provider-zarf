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
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	// Logging integration
	"github.com/go-logr/logr"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	// Zarf library imports
	"github.com/zarf-dev/zarf/src/api/v1alpha1"
	"github.com/zarf-dev/zarf/src/config"
	"github.com/zarf-dev/zarf/src/pkg/cluster"
	"github.com/zarf-dev/zarf/src/pkg/logger"
	"github.com/zarf-dev/zarf/src/pkg/packager"
	"github.com/zarf-dev/zarf/src/pkg/packager/filters"
	"github.com/zarf-dev/zarf/src/pkg/packager/layout"
	"github.com/zarf-dev/zarf/src/pkg/state"

	// Kubernetes client
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

var (
	// kubeconfigSetupOnce ensures we only set up the kubeconfig once
	kubeconfigSetupOnce sync.Once
	// kubeconfigSetupErr stores any error from kubeconfig setup
	kubeconfigSetupErr error
)

// =============================================================================
// TYPE DEFINITIONS
// =============================================================================

// DeployOptions contains all configuration needed to deploy a Zarf package.
// These options map directly to the ZarfPackage CRD spec fields.
type DeployOptions struct {
	Source                   string            // Package source (OCI, HTTP, file, cluster name)
	Namespace                string            // Target namespace override
	Components               []string          // Specific components to deploy
	Variables                map[string]string // Deployment variables (KEY=VALUE)
	AdoptExisting            bool              // Adopt existing resources
	SkipSignatureValidation  bool              // Skip package signature validation
	Shasum                   string            // Expected package checksum
	Architecture             string            // Target architecture (amd64, arm64)
	PublicKeyPath            string            // Path to public key for validation
	OCIConcurrency           int               // OCI operation concurrency
	Retries                  *int              // Number of retries for failed operations
	Timeout                  *time.Duration    // Deployment timeout
	PlainHTTP                bool              // Use HTTP instead of HTTPS
	InsecureSkipTLSVerify    bool              // Skip TLS certificate verification
	RegistryDockerConfigJSON []byte            // Docker config JSON with registry credentials
	DesiredSpecHash          string            // Hash of the spec that triggered this deployment
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

var dockerConfigMu sync.Mutex

// =============================================================================
// LOGGER BRIDGE (slog ↔ logr)
// =============================================================================

// logrSlogBridge bridges Zarf's slog logging with the controller's logr logging.
// This ensures all Zarf library logs appear in the controller logs with proper formatting.
type logrSlogBridge struct {
	logger logr.Logger
}

// Write implements io.Writer to capture slog JSON output and forward to logr.
// Parses JSON output from Zarf's slog handler and extracts structured fields
// to pass as logr key-value pairs instead of logging raw JSON strings.
func (w *logrSlogBridge) Write(p []byte) (n int, err error) {
	msg := strings.TrimSpace(string(p))
	if msg == "" {
		return len(p), nil
	}

	// Try to parse as JSON from slog
	if strings.HasPrefix(msg, "{") {
		if w.tryLogJSON(msg) {
			return len(p), nil
		}
		// If JSON parsing failed, fall through to plain text handling
	}

	// Plain text message - log as-is
	w.logger.Info(msg)
	return len(p), nil
}

// tryLogJSON attempts to parse msg as JSON and log it with structured fields.
// Returns true if successful, false if JSON parsing failed.
func (w *logrSlogBridge) tryLogJSON(msg string) bool {
	var record map[string]interface{}
	if err := json.Unmarshal([]byte(msg), &record); err != nil {
		return false
	}

	// Successfully parsed JSON - extract standard fields
	level := getStringField(record, "level")
	message := getStringField(record, "msg")

	// Build key-value pairs from remaining fields (excluding time, level, msg)
	kvPairs := extractKeyValuePairs(record)

	// Route to appropriate log level with structured fields
	w.logAtLevel(level, message, kvPairs)
	return true
}

// logAtLevel routes the message to the appropriate log level.
func (w *logrSlogBridge) logAtLevel(level, message string, kvPairs []interface{}) {
	switch strings.ToUpper(level) {
	case "DEBUG":
		// Map DEBUG to V(1) so it only appears when log level is set to debug
		w.logger.V(1).Info(message, kvPairs...)
	case "ERROR":
		w.logger.Error(nil, message, kvPairs...)
	case "WARN", "WARNING":
		// Include level as a field for warnings
		kvPairs = append([]interface{}{"level", "WARN"}, kvPairs...)
		w.logger.Info(message, kvPairs...)
	default:
		// INFO or unknown level
		w.logger.Info(message, kvPairs...)
	}
}

// extractKeyValuePairs builds a key-value slice from a map, excluding time, level, and msg fields.
func extractKeyValuePairs(record map[string]interface{}) []interface{} {
	var kvPairs []interface{}
	for k, v := range record {
		if k != "time" && k != "level" && k != "msg" {
			kvPairs = append(kvPairs, k, v)
		}
	}
	return kvPairs
}

// getStringField extracts a string value from a map, returning empty string if not found or not a string.
func getStringField(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// =============================================================================
// CONSTANTS
// =============================================================================

const ociPrefix = "oci://"

// kubeconfigPath is the path where we write the generated kubeconfig
const kubeconfigPath = "/tmp/zarf-provider-kubeconfig"

// ensureInClusterConfig configures the environment so the Zarf library can use in-cluster credentials.
// The Zarf library uses clientcmd.NewDefaultClientConfigLoadingRules() which expects a kubeconfig file.
// We generate a kubeconfig file from the in-cluster service account credentials and set KUBECONFIG to point to it.
// This makes the pod environment look like a local developer machine to the Zarf library.
func ensureInClusterConfig() error {
	var err error
	kubeconfigSetupOnce.Do(func() {
		err = setupKubeconfigFromInCluster()
	})
	if err != nil {
		return err
	}
	return kubeconfigSetupErr
}

// setupKubeconfigFromInCluster creates a kubeconfig file from in-cluster credentials
// and sets the KUBECONFIG environment variable to point to it.
func setupKubeconfigFromInCluster() error {
	// Check if we're running in a cluster
	if _, exists := os.LookupEnv("KUBERNETES_SERVICE_HOST"); !exists {
		// Not in a cluster, nothing to do - normal kubeconfig loading will work
		logger.Default().Debug("Not running in cluster, using default kubeconfig loading")
		return nil
	}

	// Get in-cluster config
	inClusterConfig, err := rest.InClusterConfig()
	if err != nil {
		kubeconfigSetupErr = fmt.Errorf("failed to get in-cluster config: %w", err)
		return kubeconfigSetupErr
	}

	// Read the service account token
	tokenPath := "/var/run/secrets/kubernetes.io/serviceaccount/token"
	token, err := os.ReadFile(tokenPath)
	if err != nil {
		kubeconfigSetupErr = fmt.Errorf("failed to read service account token: %w", err)
		return kubeconfigSetupErr
	}

	// Read the CA certificate
	caPath := "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
	caData, err := os.ReadFile(caPath)
	if err != nil {
		kubeconfigSetupErr = fmt.Errorf("failed to read CA certificate: %w", err)
		return kubeconfigSetupErr
	}

	// Build a kubeconfig that looks like a local developer's config
	kubeconfig := clientcmdapi.NewConfig()

	// Create cluster entry
	// Use the server URL from rest.InClusterConfig() which correctly handles:
	// - Standard clusters: uses KUBERNETES_SERVICE_HOST:KUBERNETES_SERVICE_PORT
	serverURL := inClusterConfig.Host

	clusterName := "in-cluster"
	kubeconfig.Clusters[clusterName] = &clientcmdapi.Cluster{
		Server:                   serverURL,
		CertificateAuthorityData: caData,
	}

	// Create auth info (user) entry using the service account token
	userName := "provider-zarf"
	kubeconfig.AuthInfos[userName] = &clientcmdapi.AuthInfo{
		Token: string(token),
	}

	// Create context entry
	contextName := "provider-zarf-context"
	kubeconfig.Contexts[contextName] = &clientcmdapi.Context{
		Cluster:  clusterName,
		AuthInfo: userName,
	}

	// Set current context
	kubeconfig.CurrentContext = contextName

	// Write the kubeconfig to a file
	if err := clientcmd.WriteToFile(*kubeconfig, kubeconfigPath); err != nil {
		kubeconfigSetupErr = fmt.Errorf("failed to write kubeconfig file: %w", err)
		return kubeconfigSetupErr
	}

	// Set KUBECONFIG environment variable to point to our generated file
	if err := os.Setenv("KUBECONFIG", kubeconfigPath); err != nil {
		kubeconfigSetupErr = fmt.Errorf("failed to set KUBECONFIG env var: %w", err)
		return kubeconfigSetupErr
	}

	logger.Default().Debug("Successfully configured kubeconfig for in-cluster access",
		"path", kubeconfigPath,
		"server", inClusterConfig.Host)

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
	// Generate a kubeconfig file from in-cluster credentials so the Zarf library
	// can use it via clientcmd loading rules (which expect a kubeconfig file)
	if _, exists := os.LookupEnv("KUBERNETES_SERVICE_HOST"); exists {
		if err := ensureInClusterConfig(); err != nil {
			ctrlLogger.Error(err, "Failed to set up in-cluster kubeconfig, Zarf operations may fail")
		} else {
			slogLogger.Debug("Configured kubeconfig for in-cluster Kubernetes access")
		}
	}

	return &client{}
}

// IsInstalled returns true if a deployed package secret already exists.
// This is simplified to match the proven Kubebuilder implementation.
// For OCI sources, we extract the package name and check if it exists in cluster state.
func (c *client) IsInstalled(ctx context.Context, src string, namespaceOverride string) (bool, *state.DeployedPackage, error) {
	name, isClusterName := inferClusterPackageName(src)

	// Bridge controller-runtime logger into Zarf slog logger for consistent log output
	ctrlLogger := ctrllog.FromContext(ctx).WithName("zarf-isinstalled")
	bridge := &logrSlogBridge{logger: ctrlLogger}
	slogLogger := slog.New(slog.NewJSONHandler(bridge, &slog.HandlerOptions{Level: slog.LevelDebug}))

	observeCtx := logger.WithContext(ctx, slogLogger)
	zlog := logger.From(observeCtx)

	if !isClusterName && os.Getenv("ZARFCLIENT_SKIP_CLUSTER") == "1" {
		zlog.Debug("zarfclient: skipping cluster lookup in test mode", "source", src)
		return false, nil, nil
	}

	cl, err := cluster.New(observeCtx)
	if err != nil {
		return false, nil, err
	}

	if !isClusterName {
		// For OCI/tar/http sources, extract likely package name from source
		// This follows the same pattern as the Kubebuilder controller
		name = extractPackageNameFromSource(normalizeSource(src))
		zlog.Debug("zarfclient: extracted package name from OCI source", "source", src, "extractedName", name)
	}

	// Simple direct lookup - same as Kubebuilder controller
	dep, err := cl.GetDeployedPackage(observeCtx, name, state.WithPackageNamespaceOverride(namespaceOverride))
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			zlog.Debug("zarfclient: package not found in cluster state", "packageName", name)
			return false, nil, nil
		}
		zlog.Debug("zarfclient: error checking package installation", "packageName", name, "error", err)
		return false, nil, err
	}

	zlog.Debug("zarfclient: package found in cluster state", "packageName", name)
	return true, dep, nil
}

// Deploy performs: load package -> apply component filters -> deploy.
func (c *client) Deploy(ctx context.Context, opts DeployOptions) (Result, error) {
	deployCtx := initializeDeploymentLogging(ctx)
	l := logger.From(deployCtx)
	l.Debug("zarfclient: Deploy called", "source", opts.Source)

	source, err := normalizeAndValidateSource(opts.Source, l)
	if err != nil {
		return Result{}, err
	}

	config.CommonOptions.Confirm = true

	cleanupDockerConfig, err := configureDockerCredentials(opts.RegistryDockerConfigJSON, l)
	if err != nil {
		return Result{}, err
	}
	defer cleanupDockerConfig()

	if err := ensureInClusterConfig(); err != nil {
		l.Error("Failed to configure in-cluster access", "error", err)
		return Result{}, fmt.Errorf("configure in-cluster access: %w", err)
	}
	l.Debug("Configured Zarf library for in-cluster access")

	filter := buildComponentFilter(opts.Components)
	layoutOpts := buildLoadOptions(opts, filter)

	l.Debug("zarfclient: loading package")
	pkgLayout, err := packager.LoadPackage(deployCtx, source, layoutOpts)
	if err != nil {
		l.Error("zarfclient: failed to load package", "error", err)
		return Result{}, fmt.Errorf("load package: %w", err)
	}
	defer pkgLayout.Cleanup() //nolint:errcheck
	l.Info("zarfclient: package loaded successfully", "packageName", pkgLayout.Pkg.Metadata.Name)

	deployOpts := buildDeployOptions(opts)

	if err := ensureClusterConnectivity(deployCtx, deployOpts.Timeout, l); err != nil {
		return Result{}, err
	}

	if err := executePackageDeployment(deployCtx, pkgLayout, deployOpts, l); err != nil {
		return Result{}, err
	}

	l.Info("zarfclient: deployment successful", "packageName", pkgLayout.Pkg.Metadata.Name)
	return Result{PackageName: pkgLayout.Pkg.Metadata.Name, DeployedAt: time.Now()}, nil
}

func initializeDeploymentLogging(ctx context.Context) context.Context {
	ctrlLogger := ctrllog.FromContext(ctx).WithName("zarf-deploy")
	bridge := &logrSlogBridge{logger: ctrlLogger}
	slogLogger := slog.New(slog.NewJSONHandler(bridge, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return logger.WithContext(ctx, slogLogger)
}

func normalizeAndValidateSource(source string, l *slog.Logger) (string, error) {
	if strings.TrimSpace(source) == "" {
		return "", errors.New("source required")
	}
	normalized := normalizeSource(source)
	l.Debug("zarfclient: normalized source", "source", normalized)
	return normalized, nil
}

func configureDockerCredentials(configJSON []byte, l *slog.Logger) (func(), error) {
	if len(configJSON) == 0 {
		return func() {}, nil
	}
	cleanup, err := installDockerConfig(configJSON)
	if err != nil {
		return nil, fmt.Errorf("install docker config: %w", err)
	}
	l.Debug("zarfclient: installed temporary registry credentials for deployment")
	return cleanup, nil
}

// buildComponentFilter creates a filter that preserves the user's requested component order.
// Unlike filters.BySelectState which returns components in package definition order,
// this filter returns components in the exact order specified by the user.
func buildComponentFilter(components []string) filters.ComponentFilterStrategy {
	if len(components) == 0 {
		return filters.Empty()
	}
	return &orderedComponentFilter{requestedComponents: components}
}

// orderedComponentFilter preserves the user's requested component order.
type orderedComponentFilter struct {
	requestedComponents []string
}

// Apply filters and orders components according to the user's requested order.
// Components are returned in the exact order specified in requestedComponents.
func (f *orderedComponentFilter) Apply(pkg v1alpha1.ZarfPackage) ([]v1alpha1.ZarfComponent, error) {
	// Build a map of component name → component for O(1) lookup
	componentMap := make(map[string]v1alpha1.ZarfComponent, len(pkg.Components))
	for _, comp := range pkg.Components {
		componentMap[comp.Name] = comp
	}

	// Build result in the user's requested order
	result := make([]v1alpha1.ZarfComponent, 0, len(f.requestedComponents))
	for _, requestedName := range f.requestedComponents {
		if comp, found := componentMap[requestedName]; found {
			result = append(result, comp)
		} else {
			// Component not found in package - return error for clear feedback
			return nil, fmt.Errorf("requested component %q not found in package", requestedName)
		}
	}

	return result, nil
}

func buildLoadOptions(opts DeployOptions, filter filters.ComponentFilterStrategy) packager.LoadOptions {
	return packager.LoadOptions{
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
}

func buildDeployOptions(opts DeployOptions) packager.DeployOptions {
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
		deployOpts.Timeout = 30 * time.Minute
	}
	return deployOpts
}

func ensureClusterConnectivity(ctx context.Context, deployTimeout time.Duration, l *slog.Logger) error {
	clusterConnectTimeout := 2 * time.Minute
	if deployTimeout > 0 && deployTimeout < clusterConnectTimeout {
		clusterConnectTimeout = deployTimeout / 2
		if clusterConnectTimeout == 0 {
			clusterConnectTimeout = deployTimeout
		}
	}

	clusterCtx, cancel := context.WithTimeout(ctx, clusterConnectTimeout)
	defer cancel()

	if _, err := cluster.New(clusterCtx); err != nil {
		l.Error("zarfclient: cluster connection failed", "error", err, "connectTimeout", clusterConnectTimeout, "deployTimeout", deployTimeout)
		return fmt.Errorf("cluster connection: %w", err)
	}
	l.Debug("zarfclient: cluster connection established", "connectTimeout", clusterConnectTimeout)
	return nil
}

func executePackageDeployment(ctx context.Context, pkgLayout *layout.PackageLayout, deployOpts packager.DeployOptions, l *slog.Logger) error {
	l.Info("zarfclient: starting packager.Deploy", "timeout", deployOpts.Timeout)

	// Run deployment in a goroutine so we can properly check context cancellation
	// Zarf's internal wait loops may not respect context cancellation properly
	type deployResult struct {
		err error
	}
	resultCh := make(chan deployResult, 1)

	go func() {
		_, err := packager.Deploy(ctx, pkgLayout, deployOpts)
		resultCh <- deployResult{err: err}
	}()

	// Wait for either deployment completion or context cancellation
	select {
	case <-ctx.Done():
		l.Warn("zarfclient: context cancelled during deployment, aborting", "error", ctx.Err())
		return fmt.Errorf("deployment cancelled: %w", ctx.Err())
	case result := <-resultCh:
		if result.err != nil {
			l.Error("zarfclient: packager.Deploy failed", "error", result.err, "timeout", deployOpts.Timeout)
			return fmt.Errorf("deploy: %w", result.err)
		}
		return nil
	}
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

func installDockerConfig(dockerConfig []byte) (func(), error) {
	if len(dockerConfig) == 0 {
		return func() {}, nil
	}

	dockerConfigMu.Lock()

	tempDir, err := os.MkdirTemp("", "provider-zarf-docker-config-*")
	if err != nil {
		dockerConfigMu.Unlock()
		return nil, fmt.Errorf("create temp dir for docker config: %w", err)
	}

	cleanup := func() {
		_ = os.RemoveAll(tempDir)
	}

	configPath := filepath.Join(tempDir, "config.json")
	if err := os.WriteFile(configPath, dockerConfig, 0o600); err != nil {
		cleanup()
		dockerConfigMu.Unlock()
		return nil, fmt.Errorf("write docker config: %w", err)
	}

	prevValue, hadPrev := os.LookupEnv("DOCKER_CONFIG")
	if err := os.Setenv("DOCKER_CONFIG", tempDir); err != nil {
		cleanup()
		dockerConfigMu.Unlock()
		return nil, fmt.Errorf("set DOCKER_CONFIG: %w", err)
	}

	return func() {
		if hadPrev {
			_ = os.Setenv("DOCKER_CONFIG", prevValue)
		} else {
			_ = os.Unsetenv("DOCKER_CONFIG")
		}
		cleanup()
		dockerConfigMu.Unlock()
	}, nil
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

// extractPackageNameFromSource attempts to extract a likely package name from an OCI source
// Example: "oci://ghcr.io/enel1221/zarf-operator/podinfo-flux:0.1.0" -> "podinfo-flux"
func extractPackageNameFromSource(source string) string {
	if source == "" {
		return ""
	}

	// Remove oci:// prefix if present
	clean := strings.TrimPrefix(source, ociPrefix)

	// Split by / and take the last part (image name)
	parts := strings.Split(clean, "/")
	if len(parts) == 0 {
		return ""
	}

	imageName := parts[len(parts)-1]

	// Remove tag or digest if present
	// Handle both :tag and @digest formats
	if idx := strings.LastIndex(imageName, ":"); idx != -1 {
		imageName = imageName[:idx]
	}
	if idx := strings.LastIndex(imageName, "@"); idx != -1 {
		imageName = imageName[:idx]
	}

	return imageName
}
