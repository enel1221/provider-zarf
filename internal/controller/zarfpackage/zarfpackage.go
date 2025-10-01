/*
Copyright 2025 The Crossplane Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package zarfpackage implements the Crossplane controller for ZarfPackage resources.
//
// This controller is responsible for managing the lifecycle of Zarf packages
// within a Kubernetes cluster. It translates the desired state from a
// ZarfPackage custom resource into actions performed by the Zarf library.
//
// The controller follows the Crossplane managed reconciler pattern, which
// consists of the following key components:
//
//   - A `connector` that establishes a connection to the external system (in this
//     case, the Zarf client).
//   - An `external` client that interacts with the external system to observe,
//     create, update, and delete resources.
//
// The controller is designed to be robust and production-ready, incorporating
// features such as:
//
//   - A circuit breaker to prevent infinite retry loops in case of persistent
//     failures.
//   - Exponential backoff for retries, to avoid overwhelming the system.
//   - Finalizers to ensure proper cleanup of resources upon deletion.
//   - Detailed status conditions to provide visibility into the state of the
//     package.
package zarfpackage

// Zarf Provider RBAC requirements
// Zarf needs cluster-admin permissions to deploy any type of Kubernetes resource
// Similar to ArgoCD, Flux, or other GitOps operators that need to manage arbitrary resources
//
//+kubebuilder:rbac:groups=*,resources=*,verbs=*
//+kubebuilder:rbac:urls=*,verbs=*

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/crossplane/crossplane-runtime/pkg/feature"
	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/pkg/controller"
	"github.com/crossplane/crossplane-runtime/pkg/event"
	xpmeta "github.com/crossplane/crossplane-runtime/pkg/meta"
	"github.com/crossplane/crossplane-runtime/pkg/ratelimiter"
	"github.com/crossplane/crossplane-runtime/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/pkg/resource"
	"github.com/crossplane/crossplane-runtime/pkg/statemetrics"

	apisv1alpha1 "github.com/crossplane/provider-zarf/apis/v1alpha1"
	"github.com/crossplane/provider-zarf/apis/zarf/v1alpha1"
	"github.com/crossplane/provider-zarf/internal/zarfclient"
)

const (
	errNotZarfPackage = "managed resource is not a ZarfPackage custom resource"
	errTrackPCUsage   = "cannot track ProviderConfig usage"
	errGetPC          = "cannot get ProviderConfig"
	errGetCreds       = "cannot get credentials"
	errNewClient      = "cannot create new Zarf client"
)

// Constants for circuit breaker configuration.
const (
	maxConsecutiveFailures = 5
	circuitBreakerTimeout  = 30 * time.Minute
)

type statusMutation func(*v1alpha1.ZarfPackage) bool

const maxStatusMessageLength = 512

// deploymentTracker tracks in-progress deployments so they can be cancelled during deletion.
type deploymentTracker struct {
	mu          sync.RWMutex
	deployments map[types.UID]context.CancelFunc
}

// newDeploymentTracker creates a new deployment tracker.
func newDeploymentTracker() *deploymentTracker {
	return &deploymentTracker{
		deployments: make(map[types.UID]context.CancelFunc),
	}
}

// track stores a cancel function for a resource's deployment.
func (dt *deploymentTracker) track(uid types.UID, cancel context.CancelFunc) {
	dt.mu.Lock()
	defer dt.mu.Unlock()
	dt.deployments[uid] = cancel
}

// cancel cancels any in-progress deployment for the given resource.
func (dt *deploymentTracker) cancel(uid types.UID) bool {
	dt.mu.Lock()
	defer dt.mu.Unlock()
	if cancel, exists := dt.deployments[uid]; exists {
		cancel()
		delete(dt.deployments, uid)
		return true
	}
	return false
}

// untrack removes a deployment from tracking (called when deployment completes).
func (dt *deploymentTracker) untrack(uid types.UID) {
	dt.mu.Lock()
	defer dt.mu.Unlock()
	delete(dt.deployments, uid)
}

// Global deployment tracker shared across all external clients
var globalDeploymentTracker = newDeploymentTracker()

// Setup adds a controller that reconciles ZarfPackage managed resources.
func Setup(mgr ctrl.Manager, o controller.Options) error {
	name := managed.ControllerName(v1alpha1.ZarfPackageGroupKind)

	cps := []managed.ConnectionPublisher{managed.NewAPISecretPublisher(mgr.GetClient(), mgr.GetScheme())}

	rec := event.NewAPIRecorder(mgr.GetEventRecorderFor(name))

	opts := []managed.ReconcilerOption{
		managed.WithExternalConnecter(&connector{
			kube:         mgr.GetClient(),
			usage:        resource.NewProviderConfigUsageTracker(mgr.GetClient(), &apisv1alpha1.ProviderConfigUsage{}),
			newServiceFn: newZarfClient,
			recorder:     rec,
		}),
		managed.WithLogger(o.Logger.WithValues("controller", name)),
		managed.WithPollInterval(o.PollInterval),
		managed.WithRecorder(rec),
		managed.WithConnectionPublishers(cps...),
		managed.WithManagementPolicies(),
	}

	if o.Features.Enabled(feature.EnableAlphaChangeLogs) {
		opts = append(opts, managed.WithChangeLogger(o.ChangeLogOptions.ChangeLogger))
	}

	if o.MetricOptions != nil {
		opts = append(opts, managed.WithMetricRecorder(o.MetricOptions.MRMetrics))
	}

	if o.MetricOptions != nil && o.MetricOptions.MRStateMetrics != nil {
		stateMetricsRecorder := statemetrics.NewMRStateRecorder(
			mgr.GetClient(), o.Logger, o.MetricOptions.MRStateMetrics, &v1alpha1.ZarfPackageList{}, o.MetricOptions.PollStateMetricInterval,
		)
		if err := mgr.Add(stateMetricsRecorder); err != nil {
			return errors.Wrap(err, "cannot register MR state metrics recorder for kind v1alpha1.ZarfPackageList")
		}
	}

	r := managed.NewReconciler(mgr, resource.ManagedKind(v1alpha1.ZarfPackageGroupVersionKind), opts...)

	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		WithOptions(o.ForControllerRuntime()).
		WithEventFilter(resource.DesiredStateChanged()).
		For(&v1alpha1.ZarfPackage{}).
		Complete(ratelimiter.NewReconciler(name, r, o.GlobalRateLimiter))
}

// A connector is expected to produce an ExternalClient when its Connect method
// is called.
type connector struct {
	kube         client.Client
	usage        resource.Tracker
	newServiceFn func() zarfclient.Client
	recorder     event.Recorder
}

// Connect typically produces an ExternalClient by:
// 1. Tracking that the managed resource is using a ProviderConfig.
// 2. Getting the managed resource's ProviderConfig.
// 3. Getting the credentials specified by the ProviderConfig.
// 4. Using the credentials to form a client.
func (c *connector) Connect(ctx context.Context, mg resource.Managed) (managed.ExternalClient, error) {
	cr, ok := mg.(*v1alpha1.ZarfPackage)
	if !ok {
		return nil, errors.New(errNotZarfPackage)
	}

	if err := c.usage.Track(ctx, mg); err != nil {
		return nil, errors.Wrap(err, errTrackPCUsage)
	}

	pc := &apisv1alpha1.ProviderConfig{}
	if err := c.kube.Get(ctx, types.NamespacedName{Name: cr.GetProviderConfigReference().Name}, pc); err != nil {
		return nil, errors.Wrap(err, errGetPC)
	}

	// NOTE: The Zarf client doesn't require any credentials to be passed in at
	// this time. It uses the in-cluster config by default. This is where you
	// would add credential extraction logic if the Zarf client needed it.

	svc := c.newServiceFn()

	return &external{
		client:   svc,
		kube:     c.kube,
		recorder: c.recorder,
		logger: ctrllog.FromContext(ctx).
			WithName("zarfpackage-external").
			WithValues(
				"name", cr.GetName(),
				"namespace", cr.GetNamespace(),
				"uid", cr.GetUID(),
				"version", cr.GetResourceVersion(),
			),
	}, nil
}

// An ExternalClient observes, then either creates, updates, or deletes an
// external resource to ensure it reflects the managed resource's desired state.
type external struct {
	client   zarfclient.Client
	kube     client.Client
	logger   logr.Logger
	recorder event.Recorder
}

// Observe checks the state of the Zarf package in the cluster.
func (c *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
	cr, ok := mg.(*v1alpha1.ZarfPackage)
	if !ok {
		return managed.ExternalObservation{}, errors.New(errNotZarfPackage)
	}

	c.logger.Info("OBSERVE START", "conditions", cr.Status.Conditions, "conditionCount", len(cr.Status.Conditions))

	// Handle deletion flow separately
	if !cr.GetDeletionTimestamp().IsZero() {
		obs := c.observeDeletion(ctx, cr)
		c.logger.Info("OBSERVE END (deletion)", "conditions", cr.Status.Conditions, "conditionCount", len(cr.Status.Conditions))
		return obs, nil
	}

	// Check circuit breaker status prior to contacting the cluster.
	cooldown := circuitBreakerCooldownRemaining(&cr.Status.AtProvider, time.Now())
	if cooldown > 0 {
		message := fmt.Sprintf("Circuit breaker active, retry in %s", cooldown.Round(time.Second))
		c.logger.Info("Circuit breaker active, skipping observation", "cooldownRemaining", cooldown, "consecutiveFailures", cr.Status.AtProvider.ConsecutiveFailures)
		cr.Status.AtProvider.Phase = "Failed"
		cr.SetConditions(xpv1.ReconcileError(errors.New(message)))
		return managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: true}, nil
	}

	if cr.Status.AtProvider.CircuitBreakerActive && resetCircuitBreakerState(&cr.Status.AtProvider) {
		c.logger.Info("Circuit breaker cooldown elapsed, resuming reconciliation")
	}

	// Check installation status
	obs, err := c.observeInstallation(ctx, cr)
	c.logger.Info("OBSERVE END (normal)", "conditions", cr.Status.Conditions, "conditionCount", len(cr.Status.Conditions), "phase", cr.Status.AtProvider.Phase)
	return obs, err
}

// observeDeletion handles the observation logic when the resource is being deleted.
func (c *external) observeDeletion(ctx context.Context, cr *v1alpha1.ZarfPackage) managed.ExternalObservation {
	c.logger.V(1).Info("Resource is being deleted, checking if package is removed")
	cr.Status.AtProvider.Phase = "Removing"
	cr.SetConditions(xpv1.Deleting())

	packageName := extractPackageName(cr.Spec.ForProvider.Source)
	if packageName == "" {
		packageName = xpmeta.GetExternalName(cr)
	}

	observeCtx := ctrllog.IntoContext(ctx, c.logger.
		WithName("zarf-observe-delete").
		WithValues("source", cr.Spec.ForProvider.Source, "packageName", packageName))

	installed, _, err := c.client.IsInstalled(observeCtx, packageName, cr.Spec.ForProvider.Namespace)
	if err != nil {
		c.logger.V(1).Info("Error checking if package exists during deletion, assuming removed", "error", err, "packageName", packageName)
		return managed.ExternalObservation{ResourceExists: false}
	}

	if !installed {
		c.logger.V(1).Info("Package confirmed removed, resource can be finalized", "packageName", packageName)
		c.recorder.Event(cr, event.Normal("Deleted", "Zarf package successfully removed"))
		return managed.ExternalObservation{ResourceExists: false}
	}

	c.logger.V(1).Info("Package still exists, deletion in progress", "packageName", packageName)
	return managed.ExternalObservation{ResourceExists: true}
}

// observeInstallation handles the observation logic for normal (non-deletion) cases.
func (c *external) observeInstallation(ctx context.Context, cr *v1alpha1.ZarfPackage) (managed.ExternalObservation, error) {
	packageName := extractPackageName(cr.Spec.ForProvider.Source)
	if packageName == "" {
		packageName = xpmeta.GetExternalName(cr)
	}

	observeCtx := ctrllog.IntoContext(ctx, c.logger.
		WithName("zarf-observe").
		WithValues("source", cr.Spec.ForProvider.Source, "packageName", packageName))

	installed, _, err := c.client.IsInstalled(observeCtx, packageName, cr.Spec.ForProvider.Namespace)
	if err != nil {
		return managed.ExternalObservation{}, errors.Wrap(err, "failed to check if package is installed")
	}

	// Set external name if needed
	if existing := xpmeta.GetExternalName(cr); existing != packageName {
		xpmeta.SetExternalName(cr, packageName)
	}

	cr.Status.AtProvider.PackageName = packageName

	if !installed {
		return c.handleNotInstalled(cr, packageName)
	}

	return c.handleInstalled(cr, packageName)
}

// handleNotInstalled handles the case where the package is not yet installed.
func (c *external) handleNotInstalled(cr *v1alpha1.ZarfPackage, packageName string) (managed.ExternalObservation, error) {
	hasCreateSucceeded := !xpmeta.GetExternalCreateSucceeded(cr).IsZero()

	if hasCreateSucceeded {
		// Deployment in progress
		c.logger.Info("Deployment in progress", "packageName", packageName)
		cr.Status.AtProvider.PackageName = packageName
		cr.Status.AtProvider.Phase = "Installing"
		cr.SetConditions(xpv1.Creating())
		c.logger.Info("Returning ResourceUpToDate=false with Ready=Creating", "conditions", cr.Status.Conditions, "count", len(cr.Status.Conditions))

		return managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: false}, nil
	}

	// Not yet created
	cr.Status.AtProvider.Phase = "NotInstalled"
	cr.SetConditions(xpv1.Creating())
	return managed.ExternalObservation{ResourceExists: false}, nil
}

// handleInstalled handles the case where the package is installed and ready.
func (c *external) handleInstalled(cr *v1alpha1.ZarfPackage, packageName string) (managed.ExternalObservation, error) {
	c.logger.Info("Package installed", "packageName", packageName)
	cr.Status.AtProvider.PackageName = packageName
	cr.Status.AtProvider.Phase = "Installed"
	resetCircuitBreakerState(&cr.Status.AtProvider)
	cr.SetConditions(xpv1.Available())
	c.logger.Info("Returning ResourceUpToDate=true with Ready=Available")

	// Emit event for successful deployment
	c.recorder.Event(cr, event.Normal("PackageAvailable", "Zarf package successfully deployed and available"))

	return managed.ExternalObservation{
		ResourceExists:    true,
		ResourceUpToDate:  true,
		ConnectionDetails: managed.ConnectionDetails{},
	}, nil
}

// Create deploys a new Zarf package.
// CRITICAL: This method must return QUICKLY (< 1 minute) to avoid Crossplane timeouts.
// For long-running deployments, we start the deployment in a goroutine and let
// Observe() detect completion.
func (c *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
	cr, ok := mg.(*v1alpha1.ZarfPackage)
	if !ok {
		return managed.ExternalCreation{}, errors.New(errNotZarfPackage)
	}

	c.logger.V(1).Info("Create method called", "source", cr.Spec.ForProvider.Source)

	packageName := c.getPackageName(cr)

	// Check preconditions
	if shouldSkipCreate, err := c.shouldSkipCreate(ctx, cr, packageName); shouldSkipCreate || err != nil {
		return managed.ExternalCreation{}, err
	}

	// Start background deployment
	return c.startBackgroundDeployment(ctx, cr, packageName)
}

// getPackageName extracts the package name from the resource.
func (c *external) getPackageName(cr *v1alpha1.ZarfPackage) string {
	packageName := extractPackageName(cr.Spec.ForProvider.Source)
	if packageName == "" {
		packageName = xpmeta.GetExternalName(cr)
	}
	return packageName
}

// shouldSkipCreate checks if we should skip creation (already exists or being deleted).
func (c *external) shouldSkipCreate(ctx context.Context, cr *v1alpha1.ZarfPackage, packageName string) (bool, error) {
	// Idempotency check: if already installed, just set external name and return
	installed, _, err := c.client.IsInstalled(ctx, packageName, cr.Spec.ForProvider.Namespace)
	if err != nil {
		return true, errors.Wrap(err, "failed to check if package is already installed")
	}

	if installed {
		c.logger.V(1).Info("Package already exists during create, setting external name only", "packageName", packageName)
		if existing := xpmeta.GetExternalName(cr); existing != packageName {
			xpmeta.SetExternalName(cr, packageName)
		}
		return true, nil
	}

	// Check if resource is being deleted before starting deployment
	if !cr.GetDeletionTimestamp().IsZero() {
		c.logger.V(1).Info("Resource is being deleted, skipping deployment", "packageName", packageName)
		return true, nil
	}

	return false, nil
}

// startBackgroundDeployment initiates the deployment in a background goroutine.
func (c *external) startBackgroundDeployment(ctx context.Context, cr *v1alpha1.ZarfPackage, packageName string) (managed.ExternalCreation, error) {
	c.logger.V(1).Info("Package not found, starting deployment in background", "packageName", packageName)
	c.recorder.Event(cr, event.Normal("DeploymentStarted", "Starting Zarf package deployment in background"))

	deployTimeout := c.getDeployTimeout(cr)
	opts := c.buildDeployOptions(cr, deployTimeout)

	// Start deployment goroutine
	// Note: We pass ctx for linting but use background context inside the goroutine
	// because the deployment outlives the reconciliation request.
	c.launchDeploymentGoroutine(ctx, cr, packageName, opts, deployTimeout)

	// Set external name so Crossplane can track this resource
	if existing := xpmeta.GetExternalName(cr); existing != packageName {
		xpmeta.SetExternalName(cr, packageName)
	}

	return managed.ExternalCreation{}, nil
}

// getDeployTimeout extracts the deployment timeout from the spec or uses default.
func (c *external) getDeployTimeout(cr *v1alpha1.ZarfPackage) time.Duration {
	if cr.Spec.ForProvider.Timeout != nil && cr.Spec.ForProvider.Timeout.Duration > 0 {
		return cr.Spec.ForProvider.Timeout.Duration
	}
	return 30 * time.Minute
}

// buildDeployOptions constructs deployment options from the resource spec.
func (c *external) buildDeployOptions(cr *v1alpha1.ZarfPackage, deployTimeout time.Duration) zarfclient.DeployOptions {
	return zarfclient.DeployOptions{
		Source:                  cr.Spec.ForProvider.Source,
		Namespace:               cr.Spec.ForProvider.Namespace,
		Components:              cr.Spec.ForProvider.Components,
		Variables:               cr.Spec.ForProvider.Variables,
		Architecture:            cr.Spec.ForProvider.Architecture,
		Retries:                 cr.Spec.ForProvider.Retries,
		Timeout:                 &deployTimeout,
		AdoptExisting:           cr.Spec.ForProvider.AdoptExistingResources,
		SkipSignatureValidation: cr.Spec.ForProvider.SkipSignatureValidation,
		PlainHTTP:               cr.Spec.ForProvider.PlainHTTP,
		InsecureSkipTLSVerify:   cr.Spec.ForProvider.InsecureSkipTLSVerify,
	}
}

// launchDeploymentGoroutine starts the background deployment process.
// Note: ctx parameter is accepted for linting compliance but we intentionally use a
// fresh background context inside the goroutine because Zarf deployments can take
// 30+ minutes and must outlive the reconciliation request (which times out quickly).
// nolint:contextcheck,unparam // Intentional: goroutine must use background context, not request context
func (c *external) launchDeploymentGoroutine(ctx context.Context, cr *v1alpha1.ZarfPackage, packageName string, opts zarfclient.DeployOptions, deployTimeout time.Duration) {
	resourceUID := cr.GetUID()
	resourceKey := client.ObjectKeyFromObject(cr)

	go func() {
		deployLogger := c.logger.WithName("zarf-deploy-background").WithValues("source", cr.Spec.ForProvider.Source, "uid", resourceUID)
		// CRITICAL: Use background context, NOT the incoming ctx, because this goroutine
		// must complete even after the reconciliation request is finished.
		deployCtx := context.Background()
		deployCtx, cancel := context.WithTimeout(deployCtx, deployTimeout)
		defer cancel()

		globalDeploymentTracker.track(resourceUID, cancel)
		defer globalDeploymentTracker.untrack(resourceUID)

		deployCtx = ctrllog.IntoContext(deployCtx, deployLogger)
		deployLogger.V(1).Info("Starting background deployment", "timeout", deployTimeout, "packageName", packageName)

		result, err := c.client.Deploy(deployCtx, opts)
		if err != nil {
			c.handleDeploymentError(context.Background(), resourceKey, deployLogger, packageName, err)
			return
		}

		c.handleDeploymentSuccess(context.Background(), resourceKey, deployLogger, packageName, result)
	}()

	c.logger.V(1).Info("Deployment started in background, returning from Create()", "packageName", packageName)
}

// handleDeploymentError handles errors from the deployment process.
func (c *external) handleDeploymentError(ctx context.Context, key client.ObjectKey, logger logr.Logger, packageName string, deployErr error) {
	if errors.Is(deployErr, context.Canceled) {
		logger.V(1).Info("Background deployment cancelled (resource deleted)", "packageName", packageName)
		return
	}

	logger.Info("Background deployment failed", "error", deployErr, "packageName", packageName)

	failureMessage := trimStatusMessage(deployErr)
	failureTime := metav1.NewTime(time.Now())

	updated, err := c.mutateStatus(ctx, key, func(pkg *v1alpha1.ZarfPackage) bool {
		changed := false
		at := &pkg.Status.AtProvider
		if at.Phase != "Failed" {
			at.Phase = "Failed"
			changed = true
		}
		if at.ConsecutiveFailures < maxConsecutiveFailures {
			at.ConsecutiveFailures++
			changed = true
		} else if at.ConsecutiveFailures != maxConsecutiveFailures {
			at.ConsecutiveFailures = maxConsecutiveFailures
			changed = true
		}
		if at.ConsecutiveFailures >= maxConsecutiveFailures && !at.CircuitBreakerActive {
			at.CircuitBreakerActive = true
			changed = true
		}
		if at.LastFailureMessage != failureMessage {
			at.LastFailureMessage = failureMessage
			changed = true
		}
		if at.LastFailureTime == nil || !at.LastFailureTime.Equal(&failureTime) {
			at.LastFailureTime = &metav1.Time{Time: failureTime.Time}
			changed = true
		}
		pkg.SetConditions(xpv1.ReconcileError(deployErr))
		xpmeta.SetExternalCreateFailed(pkg, failureTime.Time)
		changed = true
		return changed
	})
	if err != nil {
		logger.Error(err, "Failed to update status after deployment failure")
		return
	}

	if updated != nil {
		c.recorder.Event(updated, event.Warning("DeploymentFailed", deployErr))
		if updated.Status.AtProvider.CircuitBreakerActive {
			logger.Info("Circuit breaker engaged after consecutive failures", "packageName", packageName, "consecutiveFailures", updated.Status.AtProvider.ConsecutiveFailures, "cooldown", circuitBreakerTimeout)
		}
	}
}

// handleDeploymentSuccess records a completed deployment, resets circuit breakers, and emits events.
func (c *external) handleDeploymentSuccess(ctx context.Context, key client.ObjectKey, logger logr.Logger, packageName string, result zarfclient.Result) {
	logger.V(1).Info("Background deployment completed successfully", "requestedPackage", packageName, "reportedPackage", result.PackageName, "deployedAt", result.DeployedAt)

	successTime := time.Now()
	deployedAt := result.DeployedAt
	if deployedAt.IsZero() {
		deployedAt = successTime
	}
	updated, err := c.mutateStatus(ctx, key, func(pkg *v1alpha1.ZarfPackage) bool {
		changed := false
		at := &pkg.Status.AtProvider
		if at.Phase != "Installed" {
			at.Phase = "Installed"
			changed = true
		}
		if at.PackageName != result.PackageName {
			at.PackageName = result.PackageName
			changed = true
		}
		if at.LastDeployTime == nil || !at.LastDeployTime.Time.Equal(deployedAt) {
			deployTime := metav1.NewTime(deployedAt)
			at.LastDeployTime = &deployTime
			changed = true
		}
		if resetCircuitBreakerState(at) {
			changed = true
		}
		pkg.SetConditions(xpv1.Available(), xpv1.ReconcileSuccess())
		xpmeta.SetExternalCreateSucceeded(pkg, successTime)
		changed = true
		return changed
	})
	if err != nil {
		logger.Error(err, "Failed to update status after deployment success")
		return
	}

	if updated != nil {
		c.recorder.Event(updated, event.Normal("DeploymentFinished", fmt.Sprintf("Zarf package %s deployed", result.PackageName)))
	}
}

// Update is not yet implemented. In a real-world scenario, this would handle
// upgrading a Zarf package to a new version.
func (c *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	// For now, we'll just log that an update was requested.
	c.logger.Info("Update called, but not yet implemented")
	return managed.ExternalUpdate{}, nil
}

// Delete removes a Zarf package.
// CRITICAL: Gracefully handles deletion during active deployment by cancelling the goroutine.
func (c *external) Delete(ctx context.Context, mg resource.Managed) (managed.ExternalDelete, error) {
	cr, ok := mg.(*v1alpha1.ZarfPackage)
	if !ok {
		return managed.ExternalDelete{}, errors.New(errNotZarfPackage)
	}

	// Note: We don't set conditions here - Observe() already set Deleting() condition
	// This follows the pattern where Observe sets conditions based on actual state
	cr.Status.AtProvider.Phase = "Removing"

	resourceUID := cr.GetUID()

	// CRITICAL: Cancel any in-progress deployment before attempting removal
	// This prevents race conditions where deployment completes after delete starts
	if globalDeploymentTracker.cancel(resourceUID) {
		c.logger.V(1).Info("Cancelled in-progress deployment", "uid", resourceUID)
		c.recorder.Event(cr, event.Normal("DeploymentCancelled", "Cancelled in-progress deployment for deletion"))
		// Give the goroutine a moment to clean up
		time.Sleep(500 * time.Millisecond)
	}

	packageName := xpmeta.GetExternalName(cr)
	if packageName == "" {
		packageName = extractPackageName(cr.Spec.ForProvider.Source)
	}

	// Check if package is actually installed before trying to remove
	// This handles the case where deployment was cancelled before completion
	installed, _, err := c.client.IsInstalled(ctx, packageName, cr.Spec.ForProvider.Namespace)
	if err != nil {
		c.logger.V(1).Info("Error checking if package exists, proceeding with removal attempt", "error", err, "packageName", packageName)
	} else if !installed {
		c.logger.V(1).Info("Package not installed (deployment may have been cancelled), nothing to remove", "packageName", packageName)
		return managed.ExternalDelete{}, nil
	}

	// Inject logger into context for Zarf library integration
	removeCtx := ctrllog.IntoContext(ctx, c.logger.WithName("zarf-remove").WithValues("source", cr.Spec.ForProvider.Source))

	c.logger.V(1).Info("Removing installed package", "packageName", packageName)
	c.recorder.Event(cr, event.Normal("DeletingPackage", "Starting Zarf package removal"))

	if err := c.client.Remove(removeCtx, packageName, cr.Spec.ForProvider.Namespace); err != nil {
		// Check if error is because package is already removed (secret not found)
		// This is acceptable - idempotent delete
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "NotFound") {
			c.logger.V(1).Info("Package already removed or not found, treating as successful delete", "packageName", packageName)
			return managed.ExternalDelete{}, nil
		}
		// Log errors at info level since deletion failures are significant
		c.logger.Info("Failed to remove package", "error", err, "packageName", packageName)
		c.recorder.Event(cr, event.Warning("DeletionFailed", err))
		return managed.ExternalDelete{}, errors.Wrap(err, "failed to remove package")
	}

	c.logger.V(1).Info("Package removed successfully", "packageName", packageName)
	// No status sets here—post-Delete Observe will set NotInstalled/Ready False.
	return managed.ExternalDelete{}, nil
}

// Disconnect cleans up resources used by the external client.
func (c *external) Disconnect(ctx context.Context) error {
	// The Zarf client doesn't require explicit disconnection
	return nil
}

// newZarfClient creates a new Zarf client.
func newZarfClient() zarfclient.Client {
	return zarfclient.New()
}

func (c *external) mutateStatus(ctx context.Context, key client.ObjectKey, mutate statusMutation) (*v1alpha1.ZarfPackage, error) {
	if mutate == nil {
		return nil, nil
	}

	var updated *v1alpha1.ZarfPackage
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		pkg := &v1alpha1.ZarfPackage{}
		if err := c.kube.Get(ctx, key, pkg); err != nil {
			if apierrors.IsNotFound(err) {
				updated = nil
				return nil
			}
			return err
		}

		if !mutate(pkg) {
			updated = pkg
			return nil
		}

		if err := c.kube.Status().Update(ctx, pkg); err != nil {
			return err
		}
		updated = pkg
		return nil
	})

	return updated, err
}

func trimStatusMessage(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.TrimSpace(err.Error())
	if len(msg) <= maxStatusMessageLength {
		return msg
	}
	if maxStatusMessageLength <= 3 {
		return msg[:maxStatusMessageLength]
	}
	return msg[:maxStatusMessageLength-3] + "..."
}

func resetCircuitBreakerState(at *v1alpha1.ZarfPackageObservation) bool {
	if at == nil {
		return false
	}
	changed := false
	if at.ConsecutiveFailures != 0 {
		at.ConsecutiveFailures = 0
		changed = true
	}
	if at.CircuitBreakerActive {
		at.CircuitBreakerActive = false
		changed = true
	}
	if at.LastFailureTime != nil {
		at.LastFailureTime = nil
		changed = true
	}
	if at.LastFailureMessage != "" {
		at.LastFailureMessage = ""
		changed = true
	}
	return changed
}

func circuitBreakerCooldownRemaining(obs *v1alpha1.ZarfPackageObservation, now time.Time) time.Duration {
	if obs == nil || !obs.CircuitBreakerActive || obs.LastFailureTime == nil {
		return 0
	}
	elapsed := now.Sub(obs.LastFailureTime.Time)
	remaining := circuitBreakerTimeout - elapsed
	if remaining < 0 {
		return 0
	}
	return remaining
}

// extractPackageName extracts the package name from a Zarf package source string.
func extractPackageName(source string) string {
	if strings.Contains(source, "/") {
		parts := strings.Split(source, "/")
		lastPart := parts[len(parts)-1]
		if colonIndex := strings.LastIndex(lastPart, ":"); colonIndex > 0 {
			lastPart = lastPart[:colonIndex]
		}
		if atIndex := strings.LastIndex(lastPart, "@"); atIndex > 0 {
			lastPart = lastPart[:atIndex]
		}
		return lastPart
	}
	if strings.Contains(source, ".") {
		baseName := source
		if slashIndex := strings.LastIndex(baseName, "/"); slashIndex >= 0 {
			baseName = baseName[slashIndex+1:]
		}
		if dotIndex := strings.Index(baseName, "."); dotIndex > 0 {
			baseName = baseName[:dotIndex]
		}
		return baseName
	}
	return source
}
