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

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
)

// ZarfPackageParameters defines the desired state of a ZarfPackage.
// These are the parameters that can be configured when creating a ZarfPackage resource.
type ZarfPackageParameters struct {
	// Source is the OCI reference to the Zarf package. This field is required.
	// It specifies the location of the Zarf package in an OCI registry.
	// The format should be an OCI URL, e.g., oci://ghcr.io/defenseunicorns/packages/dos-games:1.0.0
	// +kubebuilder:validation:Pattern=`^(?:oci://)?((?:[a-zA-Z0-9]+(?:[._-][a-zA-Z0-9]+)*)(?::[0-9]+)?\/)?([a-z0-9]+(?:[._-][a-z0-9]+)*(?:\/[a-z0-9]+(?:[._-][a-z0-9]+)*)*\/)?([a-z0-9]+(?:[._-][a-z0-9]+)*)(?::[a-zA-Z0-9_][a-zA-Z0-9._-]{0,127})?(?:@sha256:[A-Fa-f0-9]{64})?$`
	// +required
	Source string `json:"source"`

	// Namespace specifies the namespace where the Zarf package should be deployed.
	// If not provided, the default namespace will be used.
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// Components is a list of components to deploy from the Zarf package.
	// If not provided, all components will be deployed.
	// +optional
	Components []string `json:"components,omitempty"`

	// Variables is a map of variables to be used during the deployment of the Zarf package.
	// These variables can be used to customize the deployment.
	// +optional
	Variables map[string]string `json:"variables,omitempty"`

	// Features is a map of features to enable or disable during the deployment.
	// +optional
	Features map[string]bool `json:"features,omitempty"`

	// Architecture specifies the architecture to use for the deployment (e.g., amd64, arm64).
	// +kubebuilder:validation:Enum=amd64;arm64
	// +optional
	Architecture string `json:"architecture,omitempty"`

	// Retries specifies the number of times to retry Zarf operations.
	// If not provided, the default number of retries will be used.
	// +kubebuilder:validation:Minimum=0
	// +optional
	Retries *int `json:"retries,omitempty"`

	// Timeout specifies the timeout for Zarf operations (e.g., 15m, 5m30s).
	// +optional
	Timeout *metav1.Duration `json:"timeout,omitempty"`

	// Confirm specifies whether to skip interactive confirmations during deployment.
	// Defaults to true.
	// +kubebuilder:default=true
	// +optional
	Confirm bool `json:"confirm,omitempty"`

	// AdoptExistingResources specifies whether to adopt existing resources during deployment.
	// This corresponds to the --adopt-existing-resources flag in the Zarf CLI.
	// +optional
	AdoptExistingResources bool `json:"adoptExistingResources,omitempty"`

	// SkipSignatureValidation specifies whether to skip signature validation of the Zarf package.
	// This corresponds to the --skip-signature-validation flag in the Zarf CLI.
	// +optional
	SkipSignatureValidation bool `json:"skipSignatureValidation,omitempty"`

	// Key specifies the path or K8s secret reference for signature validation.
	// +optional
	Key string `json:"key,omitempty"`

	// PlainHTTP specifies whether to use plain HTTP for the registry.
	// This is insecure and should only be used for testing.
	// +optional
	PlainHTTP bool `json:"plainHttp,omitempty"`

	// InsecureSkipTLSVerify specifies whether to disable TLS verification for the registry.
	// This is insecure and should only be used for testing.
	// +optional
	InsecureSkipTLSVerify bool `json:"insecureSkipTlsVerify,omitempty"`

	// RegistrySecretRef specifies a reference to a secret containing Docker registry credentials.
	// This can be used to pull Zarf packages from private registries.
	// +optional
	RegistrySecretRef *corev1.LocalObjectReference `json:"registrySecretRef,omitempty"`
}

// ZarfPackageObservation are the observable fields of a ZarfPackage.
// These are fields that are observed from the external resource after it has been created.
type ZarfPackageObservation struct {
	// Phase represents the current phase of the ZarfPackage.
	// Valid phases are: Pending, Installing, Installed, Failed, Removing.
	Phase string `json:"phase,omitempty"`

	// LastDeployTime represents the time when the package was last successfully deployed.
	LastDeployTime *metav1.Time `json:"lastDeployTime,omitempty"`

	// PackageName is the name of the deployed Zarf package.
	PackageName string `json:"packageName,omitempty"`
}

// A ZarfPackageSpec defines the desired state of a ZarfPackage.
type ZarfPackageSpec struct {
	xpv1.ResourceSpec `json:",inline"`
	ForProvider       ZarfPackageParameters `json:"forProvider"`
}

// ZarfPackageStatus defines the observed state of ZarfPackage.
type ZarfPackageStatus struct {
	xpv1.ResourceStatus `json:",inline"`
	AtProvider          ZarfPackageObservation `json:"atProvider,omitempty"`
}

// GetConditions returns the resource's conditions.
func (s *ZarfPackageStatus) GetConditions() []xpv1.Condition {
	return s.Conditions
}

// SetConditions sets the resource's conditions.
func (s *ZarfPackageStatus) SetConditions(c ...xpv1.Condition) {
	s.Conditions = c
}

// +kubebuilder:object:root=true

// A ZarfPackage is a managed resource that represents a Zarf package.
// It allows you to declaratively manage the deployment of Zarf packages in a Kubernetes cluster.
// +kubebuilder:printcolumn:name="READY",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="SYNCED",type="string",JSONPath=".status.conditions[?(@.type=='Synced')].status"
// +kubebuilder:printcolumn:name="EXTERNAL-NAME",type="string",JSONPath=".metadata.annotations.crossplane\\.io/external-name"
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories={crossplane,managed,zarf}
type ZarfPackage struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ZarfPackageSpec   `json:"spec"`
	Status ZarfPackageStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ZarfPackageList contains a list of ZarfPackage
type ZarfPackageList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ZarfPackage `json:"items"`
}

// ZarfPackage type metadata.
var (
	ZarfPackageKind             = "ZarfPackage"
	ZarfPackageGroupKind        = schema.GroupKind{Group: Group, Kind: ZarfPackageKind}.String()
	ZarfPackageKindAPIVersion   = ZarfPackageKind + "." + SchemeGroupVersion.String()
	ZarfPackageGroupVersionKind = SchemeGroupVersion.WithKind(ZarfPackageKind)
)

func init() {
	SchemeBuilder.Register(&ZarfPackage{}, &ZarfPackageList{})
}
