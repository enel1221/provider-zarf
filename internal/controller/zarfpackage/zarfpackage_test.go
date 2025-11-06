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

package zarfpackage

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/google/go-cmp/cmp"
	"github.com/pkg/errors"
	"github.com/zarf-dev/zarf/src/pkg/state"

	"github.com/crossplane/crossplane-runtime/pkg/event"
	"github.com/crossplane/crossplane-runtime/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/pkg/resource"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/crossplane/provider-zarf/apis/zarf/v1alpha1"
	"github.com/crossplane/provider-zarf/internal/zarfclient"
)

var testScheme = runtime.NewScheme()

const (
	testArchAmd64 = "amd64"
	testArchArm64 = "arm64"
)

func init() {
	_ = v1alpha1.SchemeBuilder.AddToScheme(testScheme)
	_ = corev1.AddToScheme(testScheme)
}

// newExternalForTest creates an external client with all required fields for testing.
func newExternalForTest(zarfClient zarfclient.Client, kubeClient client.Client) *external {
	return &external{
		client:   zarfClient,
		kube:     kubeClient,
		logger:   logr.New(&log.NullLogSink{}),
		recorder: event.NewNopRecorder(),
	}
}

// Unlike many Kubernetes projects Crossplane does not use third party testing
// libraries, per the common Go test review comments. Crossplane encourages the
// use of table driven unit tests. The tests of the crossplane-runtime project
// are representative of the testing style Crossplane encourages.
//
// https://github.com/golang/go/wiki/TestComments
// https://github.com/crossplane/crossplane/blob/master/CONTRIBUTING.md#contributing-code

func TestObserve(t *testing.T) {
	type fields struct {
		client zarfclient.Client
	}

	type args struct {
		ctx context.Context
		mg  resource.Managed
	}

	type want struct {
		o   managed.ExternalObservation
		err error
	}

	cases := map[string]struct {
		reason string
		fields fields
		args   args
		want   want
	}{
		"PackageNotInstalled": {
			reason: "Should return ResourceExists=false when package is not installed",
			fields: fields{
				client: &zarfclient.MockClient{
					MockIsInstalled: func(ctx context.Context, source, namespace string) (bool, *state.DeployedPackage, error) {
						return false, nil, nil
					},
				},
			},
			args: args{
				ctx: context.Background(),
				mg: &v1alpha1.ZarfPackage{
					ObjectMeta: metav1.ObjectMeta{Name: "test-package"},
					Spec: v1alpha1.ZarfPackageSpec{
						ForProvider: v1alpha1.ZarfPackageParameters{
							Source: "oci://defenseunicorns/packages/dos-games:1.0.0",
						},
					},
				},
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists: false,
				},
			},
		},
		"PackageInstalled": {
			reason: "Should return ResourceExists=true and ResourceUpToDate=true when package is installed",
			fields: fields{
				client: &zarfclient.MockClient{
					MockIsInstalled: func(ctx context.Context, source, namespace string) (bool, *state.DeployedPackage, error) {
						return true, &state.DeployedPackage{Name: "dos-games"}, nil
					},
				},
			},
			args: args{
				ctx: context.Background(),
				mg: &v1alpha1.ZarfPackage{
					ObjectMeta: metav1.ObjectMeta{Name: "test-package"},
					Spec: v1alpha1.ZarfPackageSpec{
						ForProvider: v1alpha1.ZarfPackageParameters{
							Source: "oci://defenseunicorns/packages/dos-games:1.0.0",
						},
					},
					Status: v1alpha1.ZarfPackageStatus{
						AtProvider: v1alpha1.ZarfPackageObservation{
							// Hash must be set to simulate successfully deployed package
							LastAppliedSpecHash: computeSpecHash(v1alpha1.ZarfPackageParameters{
								Source: "oci://defenseunicorns/packages/dos-games:1.0.0",
							}),
						},
					},
				},
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:    true,
					ResourceUpToDate:  true,
					ConnectionDetails: managed.ConnectionDetails{},
				},
			},
		},
		"CheckInstalledError": {
			reason: "Should return error when IsInstalled fails",
			fields: fields{
				client: &zarfclient.MockClient{
					MockIsInstalled: func(ctx context.Context, source, namespace string) (bool, *state.DeployedPackage, error) {
						return false, nil, errors.New("cluster connection failed")
					},
				},
			},
			args: args{
				ctx: context.Background(),
				mg: &v1alpha1.ZarfPackage{
					ObjectMeta: metav1.ObjectMeta{Name: "test-package"},
					Spec: v1alpha1.ZarfPackageSpec{
						ForProvider: v1alpha1.ZarfPackageParameters{
							Source: "oci://defenseunicorns/packages/dos-games:1.0.0",
						},
					},
				},
			},
			want: want{
				err: errors.New("failed to check if package is installed: cluster connection failed"),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			pkg, ok := tc.args.mg.(*v1alpha1.ZarfPackage)
			if !ok {
				t.Fatalf("test managed resource is not *v1alpha1.ZarfPackage")
			}
			existing := pkg.DeepCopy()
			fakeClient := fake.NewClientBuilder().WithScheme(testScheme).WithStatusSubresource(&v1alpha1.ZarfPackage{}).WithObjects(existing).Build()
			e := newExternalForTest(tc.fields.client, fakeClient)
			got, err := e.Observe(tc.args.ctx, pkg)
			switch {
			case tc.want.err != nil && err == nil:
				t.Errorf("\n%s\ne.Observe(...): expected error but got nil\n", tc.reason)
			case tc.want.err == nil && err != nil:
				t.Errorf("\n%s\ne.Observe(...): expected no error but got: %v\n", tc.reason, err)
			case tc.want.err != nil && err != nil && !strings.Contains(err.Error(), "cluster connection failed"):
				t.Errorf("\n%s\ne.Observe(...): expected error containing 'cluster connection failed' but got: %v\n", tc.reason, err)
			}
			if diff := cmp.Diff(tc.want.o, got); diff != "" {
				t.Errorf("\n%s\ne.Observe(...): -want, +got:\n%s\n", tc.reason, diff)
			}
		})
	}
}

func TestCreate(t *testing.T) {
	type fields struct {
		client zarfclient.Client
	}

	type args struct {
		ctx context.Context
		mg  resource.Managed
	}

	type want struct {
		c   managed.ExternalCreation
		err error
	}

	cases := map[string]struct {
		reason string
		fields fields
		args   args
		want   want
	}{
		"SuccessfulLaunch": {
			reason: "Should successfully launch deployment in background",
			fields: fields{
				client: &zarfclient.MockClient{
					MockDeploy: func(ctx context.Context, opts zarfclient.DeployOptions) (zarfclient.Result, error) {
						// This will be called in background, so we don't test it here
						return zarfclient.Result{PackageName: "dos-games"}, nil
					},
				},
			},
			args: args{
				ctx: context.Background(),
				mg: &v1alpha1.ZarfPackage{
					ObjectMeta: metav1.ObjectMeta{Name: "test-package"},
					Spec: v1alpha1.ZarfPackageSpec{
						ForProvider: v1alpha1.ZarfPackageParameters{
							Source:     "oci://defenseunicorns/packages/dos-games:1.0.0",
							Namespace:  "games",
							Components: []string{"dos-games"},
							Variables: map[string]string{
								"GAME_COUNT": "3",
							},
						},
					},
				},
			},
			want: want{
				c: managed.ExternalCreation{},
			},
		},
		"PackageBeingDeleted": {
			reason: "Should skip deployment when package is being deleted",
			fields: fields{
				client: &zarfclient.MockClient{},
			},
			args: args{
				ctx: context.Background(),
				mg: &v1alpha1.ZarfPackage{
					ObjectMeta: metav1.ObjectMeta{
						Name:              "test-package",
						Finalizers:        []string{"zarf.dev/finalizer"},
						DeletionTimestamp: &metav1.Time{Time: metav1.Now().Time},
					},
					Spec: v1alpha1.ZarfPackageSpec{
						ForProvider: v1alpha1.ZarfPackageParameters{
							Source: "oci://defenseunicorns/packages/dos-games:1.0.0",
						},
					},
				},
			},
			want: want{
				c: managed.ExternalCreation{},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			pkg, ok := tc.args.mg.(*v1alpha1.ZarfPackage)
			if !ok {
				t.Fatalf("test managed resource is not *v1alpha1.ZarfPackage")
			}
			existing := pkg.DeepCopy()
			fakeClient := fake.NewClientBuilder().WithScheme(testScheme).WithStatusSubresource(&v1alpha1.ZarfPackage{}).WithObjects(existing).Build()
			e := newExternalForTest(tc.fields.client, fakeClient)
			got, err := e.Create(tc.args.ctx, pkg)
			if tc.want.err != nil {
				if err == nil {
					t.Errorf("\n%s\ne.Create(...): expected error but got nil\n", tc.reason)
				}
			} else {
				if err != nil {
					t.Errorf("\n%s\ne.Create(...): expected no error but got: %v\n", tc.reason, err)
				}
			}
			if diff := cmp.Diff(tc.want.c, got); diff != "" {
				t.Errorf("\n%s\ne.Create(...): -want, +got:\n%s\n", tc.reason, diff)
			}
		})
	}
}

func TestDelete(t *testing.T) {
	type fields struct {
		client zarfclient.Client
	}

	type args struct {
		ctx context.Context
		mg  resource.Managed
	}

	type want struct {
		d   managed.ExternalDelete
		err error
	}

	cases := map[string]struct {
		reason string
		fields fields
		args   args
		want   want
	}{
		"SuccessfulRemoval": {
			reason: "Should successfully remove package",
			fields: fields{
				client: &zarfclient.MockClient{
					MockIsInstalled: func(ctx context.Context, source, namespace string) (bool, *state.DeployedPackage, error) {
						// Indicate package is installed so removal is attempted
						return true, &state.DeployedPackage{Name: "dos-games"}, nil
					},
					MockRemove: func(ctx context.Context, packageName, namespace string) error {
						return nil
					},
				},
			},
			args: args{
				ctx: context.Background(),
				mg: &v1alpha1.ZarfPackage{
					Spec: v1alpha1.ZarfPackageSpec{
						ForProvider: v1alpha1.ZarfPackageParameters{
							Source:    "oci://defenseunicorns/packages/dos-games:1.0.0",
							Namespace: "games",
						},
					},
				},
			},
			want: want{
				d: managed.ExternalDelete{},
			},
		},
		"RemovalFailure": {
			reason: "Should return error when removal fails",
			fields: fields{
				client: &zarfclient.MockClient{
					MockIsInstalled: func(ctx context.Context, source, namespace string) (bool, *state.DeployedPackage, error) {
						// Indicate package is installed so removal is attempted
						return true, &state.DeployedPackage{Name: "dos-games"}, nil
					},
					MockRemove: func(ctx context.Context, packageName, namespace string) error {
						return errors.New("removal failed")
					},
				},
			},
			args: args{
				ctx: context.Background(),
				mg: &v1alpha1.ZarfPackage{
					Spec: v1alpha1.ZarfPackageSpec{
						ForProvider: v1alpha1.ZarfPackageParameters{
							Source: "oci://defenseunicorns/packages/dos-games:1.0.0",
						},
					},
				},
			},
			want: want{
				err: errors.New("failed to remove package: removal failed"),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := newExternalForTest(tc.fields.client, nil)
			got, err := e.Delete(tc.args.ctx, tc.args.mg)
			switch {
			case tc.want.err != nil && err == nil:
				t.Errorf("\n%s\ne.Delete(...): expected error but got nil\n", tc.reason)
			case tc.want.err == nil && err != nil:
				t.Errorf("\n%s\ne.Delete(...): expected no error but got: %v\n", tc.reason, err)
			case tc.want.err != nil && err != nil && !strings.Contains(err.Error(), "removal failed"):
				t.Errorf("\n%s\ne.Delete(...): expected error containing 'removal failed' but got: %v\n", tc.reason, err)
			}
			if diff := cmp.Diff(tc.want.d, got); diff != "" {
				t.Errorf("\n%s\ne.Delete(...): -want, +got:\n%s\n", tc.reason, diff)
			}
		})
	}
}

func TestExtractPackageName(t *testing.T) {
	cases := map[string]struct {
		source   string
		expected string
	}{
		"OCI with version": {
			source:   "oci://defenseunicorns/packages/dos-games:1.0.0",
			expected: "dos-games",
		},
		"OCI without version": {
			source:   "oci://registry1.dso.mil/ironbank/big-bang/bigbang",
			expected: "bigbang",
		},
		"Simple name": {
			source:   "dos-games",
			expected: "dos-games",
		},
		"File path": {
			source:   "/path/to/package.tar.zst",
			expected: "package.tar.zst",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := extractPackageName(tc.source)
			if got != tc.expected {
				t.Errorf("extractPackageName(%q) = %q, want %q", tc.source, got, tc.expected)
			}
		})
	}
}

func TestHandleInstalledDrift(t *testing.T) {
	mockClient := &zarfclient.MockClient{}
	e := newExternalForTest(mockClient, nil)
	pkg := &v1alpha1.ZarfPackage{
		Spec: v1alpha1.ZarfPackageSpec{
			ForProvider: v1alpha1.ZarfPackageParameters{
				Source:     "oci://defenseunicorns/packages/dos-games:1.0.0",
				Components: []string{"dos-games"},
			},
		},
		Status: v1alpha1.ZarfPackageStatus{AtProvider: v1alpha1.ZarfPackageObservation{PackageName: "dos-games"}},
	}
	desiredHash := computeSpecHash(pkg.Spec.ForProvider)
	previousHash := desiredHash + "-old"
	pkg.Status.AtProvider.LastAppliedSpecHash = previousHash

	obs, err := e.handleInstalled(pkg, "dos-games")
	if err != nil {
		t.Fatalf("handleInstalled returned error: %v", err)
	}
	if obs.ResourceUpToDate {
		t.Fatalf("expected ResourceUpToDate=false when spec hash differs")
	}
	if pkg.Status.AtProvider.Phase != "Updating" {
		t.Fatalf("expected phase Updating, got %s", pkg.Status.AtProvider.Phase)
	}
	if pkg.Status.AtProvider.LastAppliedSpecHash != previousHash {
		t.Fatalf("expected last applied hash to remain %s until redeploy, got %s", previousHash, pkg.Status.AtProvider.LastAppliedSpecHash)
	}
}

func TestUpdateRedeploysOnSpecChange(t *testing.T) {
	prevTracker := globalDeploymentTracker
	globalDeploymentTracker = newDeploymentTracker()
	defer func() { globalDeploymentTracker = prevTracker }()

	startCh := make(chan struct{})
	unblock := make(chan struct{})
	defer close(unblock)

	pkg := &v1alpha1.ZarfPackage{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-package",
			UID:        types.UID("uid-123"),
			Finalizers: []string{"finalizer.managedresource.crossplane.io"},
		},
		Spec: v1alpha1.ZarfPackageSpec{
			ForProvider: v1alpha1.ZarfPackageParameters{
				Source:     "oci://defenseunicorns/packages/dos-games:1.0.0",
				Components: []string{"dos-games"},
			},
		},
		Status: v1alpha1.ZarfPackageStatus{
			AtProvider: v1alpha1.ZarfPackageObservation{
				LastAppliedSpecHash: "old-hash",
			},
		},
	}

	desiredHash := computeSpecHash(pkg.Spec.ForProvider)

	mockClient := &zarfclient.MockClient{
		MockDeploy: func(ctx context.Context, opts zarfclient.DeployOptions) (zarfclient.Result, error) {
			if opts.DesiredSpecHash != desiredHash {
				t.Errorf("expected desired spec hash %s, got %s", desiredHash, opts.DesiredSpecHash)
			}
			close(startCh)
			<-unblock
			return zarfclient.Result{PackageName: "dos-games", DeployedAt: time.Now()}, nil
		},
	}

	existing := pkg.DeepCopy()
	fakeClient := fake.NewClientBuilder().WithScheme(testScheme).WithStatusSubresource(&v1alpha1.ZarfPackage{}).WithObjects(existing).Build()
	e := newExternalForTest(mockClient, fakeClient)

	if _, err := e.Update(context.Background(), pkg); err != nil {
		t.Fatalf("Update returned error: %v", err)
	}

	select {
	case <-startCh:
	case <-time.After(2 * time.Second):
		t.Fatalf("expected deployment to start")
	}
}

func TestUpdateNoChange(t *testing.T) {
	prevTracker := globalDeploymentTracker
	globalDeploymentTracker = newDeploymentTracker()
	defer func() { globalDeploymentTracker = prevTracker }()

	pkg := &v1alpha1.ZarfPackage{
		ObjectMeta: metav1.ObjectMeta{Name: "test-package"},
		Spec: v1alpha1.ZarfPackageSpec{
			ForProvider: v1alpha1.ZarfPackageParameters{
				Source: "oci://defenseunicorns/packages/dos-games:1.0.0",
			},
		},
	}
	pkg.Status.AtProvider.LastAppliedSpecHash = computeSpecHash(pkg.Spec.ForProvider)

	deployCalled := false
	mockClient := &zarfclient.MockClient{
		MockDeploy: func(ctx context.Context, opts zarfclient.DeployOptions) (zarfclient.Result, error) {
			deployCalled = true
			return zarfclient.Result{}, nil
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(testScheme).WithStatusSubresource(&v1alpha1.ZarfPackage{}).WithObjects(pkg.DeepCopy()).Build()
	e := newExternalForTest(mockClient, fakeClient)

	if _, err := e.Update(context.Background(), pkg); err != nil {
		t.Fatalf("Update returned error: %v", err)
	}
	if deployCalled {
		t.Fatalf("expected no deployment when spec hash unchanged")
	}
}

// TestDetectClusterArchitectureAmd64 verifies auto-detection returns amd64 for amd64 nodes.
func TestDetectClusterArchitectureAmd64(t *testing.T) {
	nodes := []client.Object{
		nodeWithArch("node1", testArchAmd64),
		nodeWithArch("node2", testArchAmd64),
	}
	fakeClient := fake.NewClientBuilder().WithScheme(testScheme).WithObjects(nodes...).Build()
	e := newExternalForTest(&zarfclient.MockClient{}, fakeClient)

	arch, err := e.detectClusterArchitecture(context.Background())
	if err != nil {
		t.Fatalf("detectClusterArchitecture failed: %v", err)
	}
	if arch != testArchAmd64 {
		t.Errorf("expected amd64, got %s", arch)
	}
}

// TestDetectClusterArchitectureArm64 verifies auto-detection returns arm64 for arm64 nodes.
func TestDetectClusterArchitectureArm64(t *testing.T) {
	nodes := []client.Object{
		nodeWithArch("node1", testArchArm64),
		nodeWithArch("node2", testArchArm64),
	}
	fakeClient := fake.NewClientBuilder().WithScheme(testScheme).WithObjects(nodes...).Build()
	e := newExternalForTest(&zarfclient.MockClient{}, fakeClient)

	arch, err := e.detectClusterArchitecture(context.Background())
	if err != nil {
		t.Fatalf("detectClusterArchitecture failed: %v", err)
	}
	if arch != testArchArm64 {
		t.Errorf("expected arm64, got %s", arch)
	}
}

// TestDetectClusterArchitectureMixed verifies majority wins in mixed architecture clusters.
func TestDetectClusterArchitectureMixed(t *testing.T) {
	nodes := []client.Object{
		nodeWithArch("node1", testArchAmd64),
		nodeWithArch("node2", testArchAmd64),
		nodeWithArch("node3", testArchArm64),
	}
	fakeClient := fake.NewClientBuilder().WithScheme(testScheme).WithObjects(nodes...).Build()
	e := newExternalForTest(&zarfclient.MockClient{}, fakeClient)

	arch, err := e.detectClusterArchitecture(context.Background())
	if err != nil {
		t.Fatalf("detectClusterArchitecture failed: %v", err)
	}
	if arch != testArchAmd64 {
		t.Errorf("expected amd64 (majority), got %s", arch)
	}
}

// TestDetectClusterArchitectureNoNodes verifies default amd64 when no nodes exist.
func TestDetectClusterArchitectureNoNodes(t *testing.T) {
	fakeClient := fake.NewClientBuilder().WithScheme(testScheme).Build()
	e := newExternalForTest(&zarfclient.MockClient{}, fakeClient)

	arch, err := e.detectClusterArchitecture(context.Background())
	if err != nil {
		t.Fatalf("detectClusterArchitecture failed: %v", err)
	}
	if arch != testArchAmd64 {
		t.Errorf("expected amd64 default, got %s", arch)
	}
}

// TestDetectClusterArchitectureNoLabels verifies default amd64 when nodes lack arch labels.
func TestDetectClusterArchitectureNoLabels(t *testing.T) {
	nodes := []client.Object{
		nodeWithArch("node1", ""), // No arch label
	}
	fakeClient := fake.NewClientBuilder().WithScheme(testScheme).WithObjects(nodes...).Build()
	e := newExternalForTest(&zarfclient.MockClient{}, fakeClient)

	arch, err := e.detectClusterArchitecture(context.Background())
	if err != nil {
		t.Fatalf("detectClusterArchitecture failed: %v", err)
	}
	if arch != testArchAmd64 {
		t.Errorf("expected amd64 default when no labels, got %s", arch)
	}
}

// TestBuildDeployOptionsExplicitArch verifies explicit architecture overrides auto-detection.
func TestBuildDeployOptionsExplicitArch(t *testing.T) {
	nodes := []client.Object{
		nodeWithArch("node1", testArchAmd64),
	}
	fakeClient := fake.NewClientBuilder().WithScheme(testScheme).WithObjects(nodes...).Build()
	e := newExternalForTest(&zarfclient.MockClient{}, fakeClient)

	pkg := &v1alpha1.ZarfPackage{
		Spec: v1alpha1.ZarfPackageSpec{
			ForProvider: v1alpha1.ZarfPackageParameters{
				Source:       "oci://example.com/test:1.0.0",
				Architecture: testArchArm64, // Explicit override
			},
		},
	}

	opts := e.buildDeployOptions(context.Background(), pkg, 15*time.Minute, nil, "testhash")
	if opts.Architecture != testArchArm64 {
		t.Errorf("expected explicit arm64, got %s", opts.Architecture)
	}
}

// TestBuildDeployOptionsAutoDetect verifies auto-detection when architecture is empty.
func TestBuildDeployOptionsAutoDetect(t *testing.T) {
	nodes := []client.Object{
		nodeWithArch("node1", testArchArm64),
	}
	fakeClient := fake.NewClientBuilder().WithScheme(testScheme).WithObjects(nodes...).Build()
	e := newExternalForTest(&zarfclient.MockClient{}, fakeClient)

	pkg := &v1alpha1.ZarfPackage{
		Spec: v1alpha1.ZarfPackageSpec{
			ForProvider: v1alpha1.ZarfPackageParameters{
				Source:       "oci://example.com/test:1.0.0",
				Architecture: "", // Empty triggers auto-detection
			},
		},
	}

	opts := e.buildDeployOptions(context.Background(), pkg, 15*time.Minute, nil, "testhash")
	if opts.Architecture != testArchArm64 {
		t.Errorf("expected auto-detected arm64, got %s", opts.Architecture)
	}
}

// nodeWithArch is a test helper that creates a Node with architecture label.
func nodeWithArch(name, arch string) *corev1.Node {
	labels := make(map[string]string)
	if arch != "" {
		labels["kubernetes.io/arch"] = arch
	}
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: labels,
		},
	}
}

// TestComputeSpecHash tests the spec fingerprinting function.
func TestComputeSpecHash(t *testing.T) {
	baseParams := v1alpha1.ZarfPackageParameters{
		Source:       "oci://example.com/test:1.0.0",
		Namespace:    "default",
		Architecture: "amd64",
	}

	t.Run("identical specs produce same hash", func(t *testing.T) {
		hash1 := computeSpecHash(baseParams)
		hash2 := computeSpecHash(baseParams)
		if hash1 != hash2 {
			t.Errorf("identical specs produced different hashes: %s vs %s", hash1, hash2)
		}
	})

	t.Run("source change produces different hash", func(t *testing.T) {
		hash1 := computeSpecHash(baseParams)
		modifiedParams := baseParams
		modifiedParams.Source = "oci://example.com/test:2.0.0"
		hash2 := computeSpecHash(modifiedParams)
		if hash1 == hash2 {
			t.Error("source change did not produce different hash")
		}
	})

	t.Run("components change produces different hash", func(t *testing.T) {
		hash1 := computeSpecHash(baseParams)
		modifiedParams := baseParams
		modifiedParams.Components = []string{"component-a", "component-b"}
		hash2 := computeSpecHash(modifiedParams)
		if hash1 == hash2 {
			t.Error("components change did not produce different hash")
		}
	})

	t.Run("component order does not matter", func(t *testing.T) {
		params1 := baseParams
		params1.Components = []string{"b", "a", "c"}
		params2 := baseParams
		params2.Components = []string{"a", "b", "c"}
		hash1 := computeSpecHash(params1)
		hash2 := computeSpecHash(params2)
		if hash1 != hash2 {
			t.Error("component order changed hash (should be sorted internally)")
		}
	})

	t.Run("variables change produces different hash", func(t *testing.T) {
		hash1 := computeSpecHash(baseParams)
		modifiedParams := baseParams
		modifiedParams.Variables = map[string]string{"key1": "value1"}
		hash2 := computeSpecHash(modifiedParams)
		if hash1 == hash2 {
			t.Error("variables change did not produce different hash")
		}
	})

	t.Run("variable order does not matter", func(t *testing.T) {
		params1 := baseParams
		params1.Variables = map[string]string{"z": "1", "a": "2", "m": "3"}
		params2 := baseParams
		params2.Variables = map[string]string{"a": "2", "m": "3", "z": "1"}
		hash1 := computeSpecHash(params1)
		hash2 := computeSpecHash(params2)
		if hash1 != hash2 {
			t.Error("variable order changed hash (should be sorted internally)")
		}
	})

	t.Run("variable value change produces different hash", func(t *testing.T) {
		params1 := baseParams
		params1.Variables = map[string]string{"key": "value1"}
		params2 := baseParams
		params2.Variables = map[string]string{"key": "value2"}
		hash1 := computeSpecHash(params1)
		hash2 := computeSpecHash(params2)
		if hash1 == hash2 {
			t.Error("variable value change did not produce different hash")
		}
	})

	t.Run("timeout change produces different hash", func(t *testing.T) {
		timeout1 := metav1.Duration{Duration: 15 * time.Minute}
		timeout2 := metav1.Duration{Duration: 30 * time.Minute}
		params1 := baseParams
		params1.Timeout = &timeout1
		params2 := baseParams
		params2.Timeout = &timeout2
		hash1 := computeSpecHash(params1)
		hash2 := computeSpecHash(params2)
		if hash1 == hash2 {
			t.Error("timeout change did not produce different hash")
		}
	})

	t.Run("hash is 64 character hex string", func(t *testing.T) {
		hash := computeSpecHash(baseParams)
		if len(hash) != 64 {
			t.Errorf("expected 64 character hash (SHA256), got %d characters", len(hash))
		}
		for _, c := range hash {
			// Apply De Morgan's law: !(A || B) == !A && !B
			if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
				t.Errorf("hash contains non-hex character: %c", c)
			}
		}
	})
}

// TestObserveDetectsSpecDrift tests that Observe correctly detects spec changes via hash comparison.
func TestObserveDetectsSpecDrift(t *testing.T) {
	mockClient := &zarfclient.MockClient{
		MockIsInstalled: func(ctx context.Context, source, namespace string) (bool, *state.DeployedPackage, error) {
			return true, &state.DeployedPackage{Name: "test"}, nil
		},
	}
	fakeKubeClient := fake.NewClientBuilder().WithScheme(testScheme).Build()
	e := newExternalForTest(mockClient, fakeKubeClient)

	pkg := &v1alpha1.ZarfPackage{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-package",
			Namespace: "default",
		},
		Spec: v1alpha1.ZarfPackageSpec{
			ForProvider: v1alpha1.ZarfPackageParameters{
				Source: "oci://example.com/test:1.0.0",
			},
		},
		Status: v1alpha1.ZarfPackageStatus{
			AtProvider: v1alpha1.ZarfPackageObservation{
				PackageName:         "test",
				Phase:               "Installed",
				LastAppliedSpecHash: "old-hash-from-previous-deployment",
			},
		},
	}

	obs, err := e.Observe(context.Background(), pkg)
	if err != nil {
		t.Fatalf("Observe failed: %v", err)
	}

	if obs.ResourceUpToDate {
		t.Error("expected ResourceUpToDate=false when spec hash differs")
	}
	if pkg.Status.AtProvider.Phase != "Updating" {
		t.Errorf("expected Phase=Updating, got %s", pkg.Status.AtProvider.Phase)
	}
}

// TestObserveNoUpdateWhenHashMatches tests that repeated reconciles don't trigger updates.
func TestObserveNoUpdateWhenHashMatches(t *testing.T) {
	mockClient := &zarfclient.MockClient{
		MockIsInstalled: func(ctx context.Context, source, namespace string) (bool, *state.DeployedPackage, error) {
			return true, &state.DeployedPackage{Name: "test"}, nil
		},
	}
	fakeKubeClient := fake.NewClientBuilder().WithScheme(testScheme).Build()
	e := newExternalForTest(mockClient, fakeKubeClient)

	params := v1alpha1.ZarfPackageParameters{
		Source: "oci://example.com/test:1.0.0",
	}
	currentHash := computeSpecHash(params)

	pkg := &v1alpha1.ZarfPackage{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-package",
			Namespace: "default",
		},
		Spec: v1alpha1.ZarfPackageSpec{
			ForProvider: params,
		},
		Status: v1alpha1.ZarfPackageStatus{
			AtProvider: v1alpha1.ZarfPackageObservation{
				PackageName:         "test",
				Phase:               "Installed",
				LastAppliedSpecHash: currentHash,
			},
		},
	}

	// First observe
	obs1, err := e.Observe(context.Background(), pkg)
	if err != nil {
		t.Fatalf("first Observe failed: %v", err)
	}
	if !obs1.ResourceUpToDate {
		t.Error("expected ResourceUpToDate=true when hash matches")
	}

	// Second observe (simulating repeated reconciliation)
	obs2, err := e.Observe(context.Background(), pkg)
	if err != nil {
		t.Fatalf("second Observe failed: %v", err)
	}
	if !obs2.ResourceUpToDate {
		t.Error("expected ResourceUpToDate=true on repeated reconcile")
	}
	if pkg.Status.AtProvider.Phase != "Installed" {
		t.Errorf("expected Phase=Installed, got %s", pkg.Status.AtProvider.Phase)
	}
}

// TestArchitectureDetectionEdgeCases tests edge cases for architecture detection
func TestArchitectureDetectionEdgeCases(t *testing.T) {
	tests := []struct {
		name             string
		nodes            []corev1.Node
		expectedArch     string
		expectedFallback bool
	}{
		{
			name:             "no nodes",
			nodes:            []corev1.Node{},
			expectedArch:     "amd64",
			expectedFallback: true,
		},
		{
			name: "no architecture labels",
			nodes: []corev1.Node{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "node1",
						Labels: map[string]string{},
					},
					Spec: corev1.NodeSpec{},
				},
			},
			expectedArch:     "amd64",
			expectedFallback: true,
		},
		{
			name: "all unschedulable nodes",
			nodes: []corev1.Node{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node1",
						Labels: map[string]string{
							"kubernetes.io/arch": "arm64",
						},
					},
					Spec: corev1.NodeSpec{
						Unschedulable: true,
					},
				},
			},
			expectedArch:     "amd64",
			expectedFallback: true,
		},
		{
			name: "mixed schedulable and unschedulable - use schedulable",
			nodes: []corev1.Node{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node1",
						Labels: map[string]string{
							"kubernetes.io/arch": "arm64",
						},
					},
					Spec: corev1.NodeSpec{
						Unschedulable: true,
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node2",
						Labels: map[string]string{
							"kubernetes.io/arch": "amd64",
						},
					},
					Spec: corev1.NodeSpec{},
				},
			},
			expectedArch:     "amd64",
			expectedFallback: false,
		},
		{
			name: "beta label fallback",
			nodes: []corev1.Node{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node1",
						Labels: map[string]string{
							"beta.kubernetes.io/arch": "arm64",
						},
					},
					Spec: corev1.NodeSpec{},
				},
			},
			expectedArch:     "arm64",
			expectedFallback: false,
		},
		{
			name: "majority wins - 2 amd64 vs 1 arm64",
			nodes: []corev1.Node{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "node1",
						Labels: map[string]string{"kubernetes.io/arch": "amd64"},
					},
					Spec: corev1.NodeSpec{},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "node2",
						Labels: map[string]string{"kubernetes.io/arch": "amd64"},
					},
					Spec: corev1.NodeSpec{},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "node3",
						Labels: map[string]string{"kubernetes.io/arch": "arm64"},
					},
					Spec: corev1.NodeSpec{},
				},
			},
			expectedArch:     "amd64",
			expectedFallback: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clientBuilder := fake.NewClientBuilder().WithScheme(testScheme)
			for i := range tt.nodes {
				clientBuilder = clientBuilder.WithObjects(&tt.nodes[i])
			}
			fakeClient := clientBuilder.Build()

			mockZarfClient := &zarfclient.MockClient{}
			e := newExternalForTest(mockZarfClient, fakeClient)

			arch, err := e.detectClusterArchitecture(context.Background())
			if err != nil {
				t.Fatalf("detectClusterArchitecture failed: %v", err)
			}
			if arch != tt.expectedArch {
				t.Errorf("expected architecture %s, got %s", tt.expectedArch, arch)
			}
		})
	}
}

// TestComponentFilteringEdgeCases tests edge cases for component filtering
func TestComponentFilteringEdgeCases(t *testing.T) {
	tests := []struct {
		name       string
		components []string
		expectErr  bool
	}{
		{
			name:       "nil components list",
			components: nil,
			expectErr:  false,
		},
		{
			name:       "empty components list",
			components: []string{},
			expectErr:  false,
		},
		{
			name:       "single component",
			components: []string{"component-a"},
			expectErr:  false,
		},
		{
			name:       "duplicate components",
			components: []string{"component-a", "component-a"},
			expectErr:  false, // Should deduplicate or handle gracefully
		},
		{
			name:       "empty string component",
			components: []string{"component-a", "", "component-b"},
			expectErr:  false, // Should filter out empty strings
		},
		{
			name:       "whitespace only component",
			components: []string{"component-a", "  ", "component-b"},
			expectErr:  false, // Should handle whitespace
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := v1alpha1.ZarfPackageParameters{
				Source:     "oci://example.com/test:1.0.0",
				Components: tt.components,
			}

			// Hash should not fail with any component list
			hash := computeSpecHash(params)
			if hash == "" {
				t.Error("expected non-empty hash")
			}
		})
	}
}

// TestVariablesEdgeCases tests edge cases for variables handling
func TestVariablesEdgeCases(t *testing.T) {
	tests := []struct {
		name      string
		variables map[string]string
	}{
		{
			name:      "nil variables",
			variables: nil,
		},
		{
			name:      "empty variables",
			variables: map[string]string{},
		},
		{
			name: "empty key",
			variables: map[string]string{
				"":      "value",
				"valid": "value",
			},
		},
		{
			name: "empty value",
			variables: map[string]string{
				"key": "",
			},
		},
		{
			name: "special characters in keys",
			variables: map[string]string{
				"KEY_WITH_UNDERSCORE": "value",
				"KEY-WITH-DASH":       "value",
				"KEY.WITH.DOT":        "value",
			},
		},
		{
			name: "special characters in values",
			variables: map[string]string{
				"url":  "https://example.com/path?query=value",
				"json": `{"key":"value"}`,
				"yaml": "key: value\n",
			},
		},
		{
			name: "large value",
			variables: map[string]string{
				"large": strings.Repeat("x", 10000),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := v1alpha1.ZarfPackageParameters{
				Source:    "oci://example.com/test:1.0.0",
				Variables: tt.variables,
			}

			// Hash should handle any variables map
			hash := computeSpecHash(params)
			if hash == "" {
				t.Error("expected non-empty hash")
			}

			// Hash should be deterministic
			hash2 := computeSpecHash(params)
			if hash != hash2 {
				t.Error("hash should be deterministic")
			}
		})
	}
}

// TestInvalidSourceHandling tests handling of invalid OCI sources
func TestInvalidSourceHandling(t *testing.T) {
	tests := []struct {
		name   string
		source string
	}{
		{
			name:   "empty source",
			source: "",
		},
		{
			name:   "malformed OCI reference",
			source: "not-an-oci-reference",
		},
		{
			name:   "missing tag",
			source: "oci://example.com/package",
		},
		{
			name:   "invalid characters",
			source: "oci://example.com/package:tag with spaces",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := v1alpha1.ZarfPackageParameters{
				Source: tt.source,
			}

			// Hash computation should not panic with invalid sources
			hash := computeSpecHash(params)
			if hash == "" {
				t.Error("expected non-empty hash even for invalid source")
			}
		})
	}
}

// TestNilParametersHandling tests handling of nil or zero-value parameters
func TestNilParametersHandling(t *testing.T) {
	t.Run("zero value parameters", func(t *testing.T) {
		var params v1alpha1.ZarfPackageParameters

		hash := computeSpecHash(params)
		if hash == "" {
			t.Error("expected non-empty hash for zero value params")
		}
	})

	t.Run("partial parameters", func(t *testing.T) {
		params := v1alpha1.ZarfPackageParameters{
			Source: "oci://example.com/test:1.0.0",
			// All other fields zero/nil
		}

		hash := computeSpecHash(params)
		if hash == "" {
			t.Error("expected non-empty hash for partial params")
		}
	})
}

// TestTimeoutEdgeCases tests timeout handling edge cases
func TestTimeoutEdgeCases(t *testing.T) {
	tests := []struct {
		name    string
		timeout *metav1.Duration
	}{
		{
			name:    "nil timeout",
			timeout: nil,
		},
		{
			name:    "zero timeout",
			timeout: &metav1.Duration{Duration: 0},
		},
		{
			name:    "negative timeout",
			timeout: &metav1.Duration{Duration: -1 * time.Hour},
		},
		{
			name:    "very large timeout",
			timeout: &metav1.Duration{Duration: 100 * time.Hour},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := v1alpha1.ZarfPackageParameters{
				Source:  "oci://example.com/test:1.0.0",
				Timeout: tt.timeout,
			}

			// Should not panic with any timeout value
			hash := computeSpecHash(params)
			if hash == "" {
				t.Error("expected non-empty hash")
			}
		})
	}
}

// TestHandleInstalledHashStability tests that handleInstalled does NOT update
// the hash when the package is already installed with a matching spec.
// This prevents infinite redeploy loops (critical bug fix for v1.1.1).
func TestHandleInstalledHashStability(t *testing.T) {
	// Compute the expected hash for a known spec
	testParams := v1alpha1.ZarfPackageParameters{
		Source:     "oci://registry.example.com/pkg:1.0.0",
		Components: []string{"images", "ns", "helm"},
	}
	expectedHash := computeSpecHash(testParams)

	tests := []struct {
		name                   string
		currentHash            string
		components             []string
		source                 string
		expectHashToChange     bool
		expectResourceUpToDate bool
		expectPhase            string
	}{
		{
			name:                   "hash already set - should NOT update",
			currentHash:            expectedHash, // Use the correct hash for the spec
			components:             []string{"images", "ns", "helm"},
			source:                 "oci://registry.example.com/pkg:1.0.0",
			expectHashToChange:     false, // CRITICAL: Hash should remain unchanged
			expectResourceUpToDate: true,
			expectPhase:            "Installed",
		},
		{
			name:                   "hash empty - should trigger update (not initialize in Observe)",
			currentHash:            "",
			components:             []string{"images", "ns", "helm"},
			source:                 "oci://registry.example.com/pkg:1.0.0",
			expectHashToChange:     false,        // Should NOT change in Observe
			expectResourceUpToDate: false,        // Should trigger Create/Update to set hash
			expectPhase:            "Installing", // Will be set to Installing to trigger deployment
		},
		{
			name:                   "spec changed - should detect drift",
			currentHash:            "abc123-old-hash",
			components:             []string{"images", "ns"}, // Different components
			source:                 "oci://registry.example.com/pkg:2.0.0",
			expectHashToChange:     false, // Hash should NOT update in Observe
			expectResourceUpToDate: false, // But should trigger Update()
			expectPhase:            "Updating",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create test package
			cr := &v1alpha1.ZarfPackage{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pkg",
					UID:  types.UID("test-uid-123"),
				},
				Spec: v1alpha1.ZarfPackageSpec{
					ForProvider: v1alpha1.ZarfPackageParameters{
						Source:     tt.source,
						Components: tt.components,
					},
				},
				Status: v1alpha1.ZarfPackageStatus{
					AtProvider: v1alpha1.ZarfPackageObservation{
						LastAppliedSpecHash: tt.currentHash,
						PackageName:         "test-pkg",
					},
				},
			}

			// Create external client
			ext := &external{
				client: &zarfclient.MockClient{
					MockIsInstalled: func(ctx context.Context, source, namespace string) (bool, *state.DeployedPackage, error) {
						return true, &state.DeployedPackage{Name: "test-pkg"}, nil
					},
				},
				kube:     fake.NewClientBuilder().WithScheme(testScheme).Build(),
				logger:   logr.Discard(),
				recorder: event.NewNopRecorder(),
			}

			// Call handleInstalled
			obs, err := ext.handleInstalled(cr, "test-pkg")
			if err != nil {
				t.Fatalf("handleInstalled failed: %v", err)
			}

			// Verify resource up-to-date status
			if obs.ResourceUpToDate != tt.expectResourceUpToDate {
				t.Errorf("Expected ResourceUpToDate=%v, got %v",
					tt.expectResourceUpToDate, obs.ResourceUpToDate)
			}

			// Verify phase
			if cr.Status.AtProvider.Phase != tt.expectPhase {
				t.Errorf("Expected phase=%q, got %q", tt.expectPhase, cr.Status.AtProvider.Phase)
			}

			// CRITICAL TEST: Verify hash behavior
			newHash := cr.Status.AtProvider.LastAppliedSpecHash
			if tt.expectHashToChange {
				if newHash == tt.currentHash && tt.currentHash != "" {
					t.Errorf("Expected hash to change from %q, but it remained the same", tt.currentHash)
				}
			} else {
				// For already-set hashes, they should NEVER change in handleInstalled
				if tt.currentHash != "" && newHash != tt.currentHash {
					t.Errorf("CRITICAL: Hash changed from %q to %q! This causes infinite loops!",
						tt.currentHash, newHash)
				}
			}
		})
	}
}

// TestSpecHashIdempotency verifies that computing the hash multiple times
// for the same spec produces identical results (idempotency test).
func TestSpecHashIdempotency(t *testing.T) {
	params := v1alpha1.ZarfPackageParameters{
		Source:     "oci://registry.example.com/pkg:1.0.0",
		Namespace:  "test-ns",
		Components: []string{"images", "ns", "helm"},
		Variables: map[string]string{
			"foo": "bar",
			"baz": "qux",
		},
	}

	// Compute hash 100 times
	hashes := make(map[string]int)
	for i := 0; i < 100; i++ {
		hash := computeSpecHash(params)
		hashes[hash]++
	}

	// Should have exactly ONE unique hash
	if len(hashes) != 1 {
		t.Errorf("Expected hash to be stable, but got %d different hashes: %v",
			len(hashes), hashes)
	}

	// Verify hash is non-empty
	for hash := range hashes {
		if hash == "" {
			t.Error("Hash should not be empty")
		}
	}
}

// TestMultipleObservationsNoHashChange verifies that multiple consecutive
// Observe() calls do NOT change the hash (prevents infinite loop).
func TestMultipleObservationsNoHashChange(t *testing.T) {
	initialHash := computeSpecHash(v1alpha1.ZarfPackageParameters{
		Source:     "oci://registry.example.com/pkg:1.0.0",
		Components: []string{"images", "ns", "helm"},
	})

	cr := &v1alpha1.ZarfPackage{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-package",
			UID:  types.UID("test-uid-123"),
		},
		Spec: v1alpha1.ZarfPackageSpec{
			ForProvider: v1alpha1.ZarfPackageParameters{
				Source:     "oci://registry.example.com/pkg:1.0.0",
				Components: []string{"images", "ns", "helm"},
			},
		},
		Status: v1alpha1.ZarfPackageStatus{
			AtProvider: v1alpha1.ZarfPackageObservation{
				Phase:               "Installed",
				PackageName:         "test-package",
				LastAppliedSpecHash: initialHash,
			},
		},
	}

	ext := &external{
		client: &zarfclient.MockClient{
			MockIsInstalled: func(ctx context.Context, source, namespace string) (bool, *state.DeployedPackage, error) {
				return true, &state.DeployedPackage{Name: "test-package"}, nil
			},
		},
		kube:     fake.NewClientBuilder().WithScheme(testScheme).Build(),
		logger:   logr.Discard(),
		recorder: event.NewNopRecorder(),
	}

	// Simulate 10 consecutive handleInstalled() calls (like Crossplane would do)
	previousHash := cr.Status.AtProvider.LastAppliedSpecHash
	for i := 0; i < 10; i++ {
		obs, err := ext.handleInstalled(cr, "test-package")
		if err != nil {
			t.Fatalf("handleInstalled iteration %d failed: %v", i, err)
		}

		// Resource should be up-to-date
		if !obs.ResourceUpToDate {
			t.Errorf("Iteration %d: Expected ResourceUpToDate=true, got false. "+
				"This would trigger Update()!", i)
		}

		// Hash should remain absolutely stable
		currentHash := cr.Status.AtProvider.LastAppliedSpecHash
		if currentHash != previousHash {
			t.Errorf("Iteration %d: CRITICAL - Hash changed from %q to %q! "+
				"This causes infinite loops!", i, previousHash, currentHash)
		}
		previousHash = currentHash
	}

	// Final hash should match initial hash
	if cr.Status.AtProvider.LastAppliedSpecHash != initialHash {
		t.Errorf("Final hash %q does not match initial hash %q",
			cr.Status.AtProvider.LastAppliedSpecHash, initialHash)
	}
}

// TestHandleInstalledEmptyHash verifies that handleInstalled does NOT initialize
// the hash during observation. This prevents the race condition where setting the
// hash triggers a status update, causing reconciliation before the background
// deployment completes, leading to deployment cancellation.
func TestHandleInstalledEmptyHash(t *testing.T) {
	cr := &v1alpha1.ZarfPackage{
		Spec: v1alpha1.ZarfPackageSpec{
			ForProvider: v1alpha1.ZarfPackageParameters{
				Source: "oci://ghcr.io/enel1221/packages/cert-manager:1.16.2",
			},
		},
		Status: v1alpha1.ZarfPackageStatus{
			AtProvider: v1alpha1.ZarfPackageObservation{
				PackageName: "cert-manager",
				// Empty hash - package just installed by background goroutine
				LastAppliedSpecHash: "",
			},
		},
	}

	ext := &external{
		logger: logr.Discard(),
	}

	obs, err := ext.handleInstalled(cr, "cert-manager")

	if err != nil {
		t.Fatalf("handleInstalled failed: %v", err)
	}

	// Should return ResourceUpToDate=false to prevent Update() from being called
	if obs.ResourceUpToDate {
		t.Error("Expected ResourceUpToDate=false when hash is empty, got true. " +
			"This would trigger Update() prematurely!")
	}

	// Hash should NOT be set - it will be set by applyDeploymentSuccessStatus()
	if cr.Status.AtProvider.LastAppliedSpecHash != "" {
		t.Errorf("Hash should remain empty but was set to %q. "+
			"This causes status update and premature reconciliation!",
			cr.Status.AtProvider.LastAppliedSpecHash)
	}

	// Phase should indicate installation in progress
	if cr.Status.AtProvider.Phase != "Installing" {
		t.Errorf("Expected phase Installing, got %q", cr.Status.AtProvider.Phase)
	}
}
