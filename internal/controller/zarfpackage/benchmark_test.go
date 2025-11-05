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
	"testing"

	"github.com/zarf-dev/zarf/src/pkg/state"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/crossplane/provider-zarf/apis/zarf/v1alpha1"
	"github.com/crossplane/provider-zarf/internal/zarfclient"
)

// BenchmarkObserveNoOp benchmarks the Observe function when package is already installed
// and spec hash matches (no-op scenario). Target: <100ms
func BenchmarkObserveNoOp(b *testing.B) {
	// Setup: package already installed, hash matches
	mockClient := &zarfclient.MockClient{
		MockIsInstalled: func(ctx context.Context, source, namespace string) (bool, *state.DeployedPackage, error) {
			return true, &state.DeployedPackage{
				Name: "test-package",
			}, nil
		},
	}

	fakeKubeClient := fake.NewClientBuilder().WithScheme(testScheme).Build()
	e := newExternalForTest(mockClient, fakeKubeClient)

	params := v1alpha1.ZarfPackageParameters{
		Source:     "oci://example.com/test:1.0.0",
		Components: []string{"component-a"},
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
				PackageName:         "test-package",
				Phase:               "Installed",
				LastAppliedSpecHash: currentHash,
			},
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := e.Observe(context.Background(), pkg)
		if err != nil {
			b.Fatalf("Observe failed: %v", err)
		}
	}
}

// BenchmarkObserveWithArchDetection benchmarks Observe with architecture detection
func BenchmarkObserveWithArchDetection(b *testing.B) {
	mockClient := &zarfclient.MockClient{
		MockIsInstalled: func(ctx context.Context, source, namespace string) (bool, *state.DeployedPackage, error) {
			return false, nil, nil
		},
	}

	// Create fake nodes for architecture detection
	fakeKubeClient := fake.NewClientBuilder().
		WithScheme(testScheme).
		WithObjects(
			&corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "node1",
					Labels: map[string]string{"kubernetes.io/arch": "amd64"},
				},
			},
			&corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "node2",
					Labels: map[string]string{"kubernetes.io/arch": "amd64"},
				},
			},
		).
		Build()

	e := newExternalForTest(mockClient, fakeKubeClient)

	pkg := &v1alpha1.ZarfPackage{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-package",
			Namespace: "default",
		},
		Spec: v1alpha1.ZarfPackageSpec{
			ForProvider: v1alpha1.ZarfPackageParameters{
				Source: "oci://example.com/test:1.0.0",
				// Architecture not specified - will trigger detection
			},
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := e.Observe(context.Background(), pkg)
		if err != nil {
			b.Fatalf("Observe failed: %v", err)
		}
	}
}

// BenchmarkComputeSpecHash benchmarks the spec hash computation
func BenchmarkComputeSpecHash(b *testing.B) {
	params := v1alpha1.ZarfPackageParameters{
		Source:     "oci://example.com/test:1.0.0",
		Components: []string{"component-a", "component-b", "component-c"},
		Variables: map[string]string{
			"VAR1": "value1",
			"VAR2": "value2",
			"VAR3": "value3",
		},
		Timeout: &metav1.Duration{Duration: 900000000000}, // 15m
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = computeSpecHash(params)
	}
}

// BenchmarkComputeSpecHashLarge benchmarks spec hash with large data
func BenchmarkComputeSpecHashLarge(b *testing.B) {
	// Simulate large component and variable lists
	components := make([]string, 50)
	for i := range components {
		components[i] = "component-" + string(rune(i))
	}

	variables := make(map[string]string, 100)
	for i := 0; i < 100; i++ {
		variables["VAR"+string(rune(i))] = "value" + string(rune(i))
	}

	params := v1alpha1.ZarfPackageParameters{
		Source:     "oci://example.com/test:1.0.0",
		Components: components,
		Variables:  variables,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = computeSpecHash(params)
	}
}

// BenchmarkArchitectureDetection benchmarks the architecture detection function
func BenchmarkArchitectureDetection(b *testing.B) {
	mockClient := &zarfclient.MockClient{}

	// Create fake cluster with 10 nodes
	nodes := make([]corev1.Node, 10)
	for i := range nodes {
		nodes[i] = corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "node" + string(rune(i)),
				Labels: map[string]string{"kubernetes.io/arch": "amd64"},
			},
		}
	}

	builder := fake.NewClientBuilder().WithScheme(testScheme)
	for i := range nodes {
		builder = builder.WithObjects(&nodes[i])
	}
	fakeKubeClient := builder.Build()

	e := newExternalForTest(mockClient, fakeKubeClient)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := e.detectClusterArchitecture(context.Background())
		if err != nil {
			b.Fatalf("detectClusterArchitecture failed: %v", err)
		}
	}
}

// BenchmarkConcurrentObserve benchmarks concurrent Observe calls
func BenchmarkConcurrentObserve(b *testing.B) {
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

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, err := e.Observe(context.Background(), pkg)
			if err != nil {
				b.Fatalf("Observe failed: %v", err)
			}
		}
	})
}
