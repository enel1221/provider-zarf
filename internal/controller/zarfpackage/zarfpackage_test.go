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
