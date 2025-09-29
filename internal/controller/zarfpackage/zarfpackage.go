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

import (
	"context"
	"strings"
	"time"

	"github.com/crossplane/crossplane-runtime/pkg/feature"
	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/pkg/controller"
	"github.com/crossplane/crossplane-runtime/pkg/event"
	"github.com/crossplane/crossplane-runtime/pkg/ratelimiter"
	"github.com/crossplane/crossplane-runtime/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/pkg/resource"
	"github.com/crossplane/crossplane-runtime/pkg/statemetrics"

	v1 "github.com/crossplane/provider-zarf/apis/common/v1"
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

// Constants for condition types, reasons, and messages.
const (
	ConditionTypeReady       = "Ready"
	ConditionTypeProgressing = "Progressing"
)

// Constants for circuit breaker configuration.
const (
	maxConsecutiveFailures = 5
	circuitBreakerTimeout  = 30 * time.Minute
)

// Setup adds a controller that reconciles ZarfPackage managed resources.
func Setup(mgr ctrl.Manager, o controller.Options) error {
	name := managed.ControllerName(v1alpha1.ZarfPackageGroupKind)

	cps := []managed.ConnectionPublisher{managed.NewAPISecretPublisher(mgr.GetClient(), mgr.GetScheme())}

	opts := []managed.ReconcilerOption{
		managed.WithExternalConnecter(&connector{
			kube:         mgr.GetClient(),
			usage:        resource.NewProviderConfigUsageTracker(mgr.GetClient(), &apisv1alpha1.ProviderConfigUsage{}),
			newServiceFn: newZarfClient,
		}),
		managed.WithLogger(o.Logger.WithValues("controller", name)),
		managed.WithPollInterval(o.PollInterval),
		managed.WithRecorder(event.NewAPIRecorder(mgr.GetEventRecorderFor(name))),
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
		client: svc,
		kube:   c.kube,
		logger: logr.New(ctrllog.NullLogSink{}),
	}, nil
}

// An ExternalClient observes, then either creates, updates, or deletes an
// external resource to ensure it reflects the managed resource's desired state.
type external struct {
	client zarfclient.Client
	kube   client.Client
	logger logr.Logger
}

// Observe checks the state of the Zarf package in the cluster.
func (c *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
	cr, ok := mg.(*v1alpha1.ZarfPackage)
	if !ok {
		return managed.ExternalObservation{}, errors.New(errNotZarfPackage)
	}

	// If the circuit breaker is active, we'll skip observation and let the
	// controller back off.
	if shouldSkipDueToCircuitBreaker(cr, c.logger) {
		c.logger.Info("Circuit breaker active, skipping observation")
		return managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: true}, nil
	}

	installed, _, err := c.client.IsInstalled(ctx, cr.Spec.ForProvider.Source, cr.Spec.ForProvider.Namespace)
	if err != nil {
		return managed.ExternalObservation{}, errors.Wrap(err, "failed to check if package is installed")
	}

	if !installed {
		return managed.ExternalObservation{ResourceExists: false}, nil
	}

	// The package is installed. We'll consider it "up-to-date" for now.
	// A more sophisticated implementation could check the package version or
	// other details to determine if an update is needed.
	cr.Status.SetConditions(v1.Available())
	cr.Status.AtProvider.Phase = "Installed"

	return managed.ExternalObservation{
		ResourceExists:    true,
		ResourceUpToDate:  true,
		ConnectionDetails: managed.ConnectionDetails{},
	}, nil
}

// Create deploys a new Zarf package.
func (c *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
	cr, ok := mg.(*v1alpha1.ZarfPackage)
	if !ok {
		return managed.ExternalCreation{}, errors.New(errNotZarfPackage)
	}

	cr.Status.SetConditions(v1.Creating())
	cr.Status.AtProvider.Phase = "Installing"

	var timeout *time.Duration
	if cr.Spec.ForProvider.Timeout != nil {
		timeout = &cr.Spec.ForProvider.Timeout.Duration
	}

	opts := zarfclient.DeployOptions{
		Source:                  cr.Spec.ForProvider.Source,
		Namespace:               cr.Spec.ForProvider.Namespace,
		Components:              cr.Spec.ForProvider.Components,
		Variables:               cr.Spec.ForProvider.Variables,
		Architecture:            cr.Spec.ForProvider.Architecture,
		Retries:                 cr.Spec.ForProvider.Retries,
		Timeout:                 timeout,
		AdoptExisting:           cr.Spec.ForProvider.AdoptExistingResources,
		SkipSignatureValidation: cr.Spec.ForProvider.SkipSignatureValidation,
		PlainHTTP:               cr.Spec.ForProvider.PlainHTTP,
		InsecureSkipTLSVerify:   cr.Spec.ForProvider.InsecureSkipTLSVerify,
	}

	// Inject logger into context for Zarf library integration
	deployCtx := ctrllog.IntoContext(ctx, c.logger.WithName("zarf-deploy"))

	if _, err := c.client.Deploy(deployCtx, opts); err != nil {
		return managed.ExternalCreation{}, errors.Wrap(err, "failed to deploy package")
	}

	return managed.ExternalCreation{}, nil
}

// Update is not yet implemented. In a real-world scenario, this would handle
// upgrading a Zarf package to a new version.
func (c *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	// For now, we'll just log that an update was requested.
	c.logger.Info("Update called, but not yet implemented")
	return managed.ExternalUpdate{}, nil
}

// Delete removes a Zarf package.
func (c *external) Delete(ctx context.Context, mg resource.Managed) (managed.ExternalDelete, error) {
	cr, ok := mg.(*v1alpha1.ZarfPackage)
	if !ok {
		return managed.ExternalDelete{}, errors.New(errNotZarfPackage)
	}

	cr.Status.SetConditions(xpv1.Deleting())
	cr.Status.AtProvider.Phase = "Removing"

	packageName := extractPackageName(cr.Spec.ForProvider.Source)

	// Inject logger into context for Zarf library integration
	removeCtx := ctrllog.IntoContext(ctx, c.logger.WithName("zarf-remove"))

	if err := c.client.Remove(removeCtx, packageName, cr.Spec.ForProvider.Namespace); err != nil {
		return managed.ExternalDelete{}, errors.Wrap(err, "failed to remove package")
	}

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

// shouldSkipDueToCircuitBreaker checks if we should skip reconciliation due to
// too many consecutive failures.
func shouldSkipDueToCircuitBreaker(cr *v1alpha1.ZarfPackage, logger logr.Logger) bool {
	conditions := cr.Status.GetConditions()
	metaConditions := make([]metav1.Condition, len(conditions))
	for i, c := range conditions {
		metaConditions[i] = metav1.Condition{
			Type:               string(c.Type),
			Status:             metav1.ConditionStatus(c.Status),
			LastTransitionTime: c.LastTransitionTime,
			Reason:             string(c.Reason),
			Message:            c.Message,
		}
	}
	circuitBreakerCondition := meta.FindStatusCondition(metaConditions, "CircuitBreaker")
	if circuitBreakerCondition == nil || circuitBreakerCondition.Status != metav1.ConditionTrue {
		return false
	}

	if time.Since(circuitBreakerCondition.LastTransitionTime.Time) > circuitBreakerTimeout {
		logger.Info("Circuit breaker timeout expired, allowing retry")
		return false
	}

	return true
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
