//go:build e2e
// +build e2e

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

package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	xpv1 "github.com/crossplane/crossplane-runtime/pkg/meta"
	"github.com/crossplane/provider-zarf/apis/zarf/v1alpha1"
)

var (
	k8sClient  client.Client
	kubeClient *kubernetes.Clientset
	scheme     *runtime.Scheme
)

func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Provider-Zarf E2E Suite")
}

var _ = BeforeSuite(func() {
	By("setting up kubernetes client")
	
	// Get kubeconfig path
	kubeconfigPath := os.Getenv("KUBECONFIG")
	if kubeconfigPath == "" {
		home := os.Getenv("HOME")
		kubeconfigPath = filepath.Join(home, ".kube", "config")
	}

	// Build config from kubeconfig
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	Expect(err).NotTo(HaveOccurred(), "Failed to build kubeconfig")

	// Create scheme
	scheme = runtime.NewScheme()
	Expect(v1alpha1.SchemeBuilder.AddToScheme(scheme)).To(Succeed())
	Expect(corev1.AddToScheme(scheme)).To(Succeed())

	// Create controller-runtime client
	k8sClient, err = client.New(config, client.Options{Scheme: scheme})
	Expect(err).NotTo(HaveOccurred(), "Failed to create controller-runtime client")

	// Create kubernetes clientset
	kubeClient, err = kubernetes.NewForConfig(config)
	Expect(err).NotTo(HaveOccurred(), "Failed to create kubernetes clientset")

	By("ensuring test namespace exists")
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "component-order-test",
		},
	}
	err = k8sClient.Create(context.Background(), ns)
	if err != nil && !client.IgnoreAlreadyExists(err)(err) {
		Expect(err).NotTo(HaveOccurred(), "Failed to create test namespace")
	}
})

var _ = Describe("Component Deployment Order", func() {
	var (
		ctx             context.Context
		testPackageName string
		packageSource   string
	)

	BeforeEach(func() {
		ctx = context.Background()
		testPackageName = fmt.Sprintf("test-order-%d", time.Now().Unix())
		
		// Use local package path or OCI registry based on environment
		packageSource = os.Getenv("TEST_PACKAGE_SOURCE")
		if packageSource == "" {
			// Default to local package path
			packageSource = filepath.Join("examples", "component-order-test")
		}
	})

	AfterEach(func() {
		By("cleaning up test ZarfPackage")
		pkg := &v1alpha1.ZarfPackage{
			ObjectMeta: metav1.ObjectMeta{
				Name: testPackageName,
			},
		}
		_ = k8sClient.Delete(ctx, pkg)
		
		// Wait for cleanup
		time.Sleep(5 * time.Second)
		
		By("cleaning up test ConfigMaps")
		cmList := &corev1.ConfigMapList{}
		err := k8sClient.List(ctx, cmList, client.InNamespace("component-order-test"), client.MatchingLabels{"test": "component-order"})
		if err == nil {
			for _, cm := range cmList.Items {
				_ = k8sClient.Delete(ctx, &cm)
			}
		}
	})

	It("should deploy components in user-specified order (a, b, c)", func() {
		By("creating ZarfPackage with components in specific order")
		pkg := &v1alpha1.ZarfPackage{
			ObjectMeta: metav1.ObjectMeta{
				Name: testPackageName,
			},
			Spec: v1alpha1.ZarfPackageSpec{
				ForProvider: v1alpha1.ZarfPackageParameters{
					Source: packageSource,
					Components: []string{
						"component-a",
						"component-b",
						"component-c",
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, pkg)).To(Succeed())

		By("waiting for package to be ready")
		Eventually(func() bool {
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(pkg), pkg); err != nil {
				return false
			}
			return xpv1.IsAvailable(pkg)
		}, 5*time.Minute, 10*time.Second).Should(BeTrue(), "Package should become ready")

		By("verifying all three ConfigMaps were created")
		cmList := &corev1.ConfigMapList{}
		Expect(k8sClient.List(ctx, cmList, client.InNamespace("component-order-test"), client.MatchingLabels{"test": "component-order"})).To(Succeed())
		Expect(cmList.Items).To(HaveLen(3), "Should have exactly 3 ConfigMaps")

		By("verifying ConfigMaps exist for all components")
		cmNames := make(map[string]bool)
		for _, cm := range cmList.Items {
			cmNames[cm.Name] = true
		}
		Expect(cmNames).To(HaveKey("order-marker-a"))
		Expect(cmNames).To(HaveKey("order-marker-b"))
		Expect(cmNames).To(HaveKey("order-marker-c"))

		By("verifying component order from creation timestamps")
		var cmA, cmB, cmC *corev1.ConfigMap
		for i := range cmList.Items {
			cm := &cmList.Items[i]
			switch cm.Name {
			case "order-marker-a":
				cmA = cm
			case "order-marker-b":
				cmB = cm
			case "order-marker-c":
				cmC = cm
			}
		}

		Expect(cmA).NotTo(BeNil())
		Expect(cmB).NotTo(BeNil())
		Expect(cmC).NotTo(BeNil())

		// Component A should be created before B, and B before C
		// Note: timestamps may be very close, but order should be preserved
		Expect(cmA.CreationTimestamp.Time.Before(cmB.CreationTimestamp.Time) || 
			cmA.CreationTimestamp.Time.Equal(cmB.CreationTimestamp.Time)).To(BeTrue(), 
			"Component A should deploy before or at same time as B")
		Expect(cmB.CreationTimestamp.Time.Before(cmC.CreationTimestamp.Time) || 
			cmB.CreationTimestamp.Time.Equal(cmC.CreationTimestamp.Time)).To(BeTrue(), 
			"Component B should deploy before or at same time as C")
	})

	It("should deploy components in reverse order (c, b, a) when requested", func() {
		By("creating ZarfPackage with components in reverse order")
		pkg := &v1alpha1.ZarfPackage{
			ObjectMeta: metav1.ObjectMeta{
				Name: testPackageName + "-reverse",
			},
			Spec: v1alpha1.ZarfPackageSpec{
				ForProvider: v1alpha1.ZarfPackageParameters{
					Source: packageSource,
					Components: []string{
						"component-c",
						"component-b",
						"component-a",
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, pkg)).To(Succeed())

		By("waiting for package to be ready")
		Eventually(func() bool {
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(pkg), pkg); err != nil {
				return false
			}
			return xpv1.IsAvailable(pkg)
		}, 5*time.Minute, 10*time.Second).Should(BeTrue(), "Package should become ready")

		By("verifying all three ConfigMaps were created")
		cmList := &corev1.ConfigMapList{}
		Expect(k8sClient.List(ctx, cmList, client.InNamespace("component-order-test"), client.MatchingLabels{"test": "component-order"})).To(Succeed())
		Expect(cmList.Items).To(HaveLen(3), "Should have exactly 3 ConfigMaps")

		By("verifying component order from creation timestamps (C then B then A)")
		var cmA, cmB, cmC *corev1.ConfigMap
		for i := range cmList.Items {
			cm := &cmList.Items[i]
			switch cm.Name {
			case "order-marker-a":
				cmA = cm
			case "order-marker-b":
				cmB = cm
			case "order-marker-c":
				cmC = cm
			}
		}

		Expect(cmA).NotTo(BeNil())
		Expect(cmB).NotTo(BeNil())
		Expect(cmC).NotTo(BeNil())

		// Component C should be created before B, and B before A
		Expect(cmC.CreationTimestamp.Time.Before(cmB.CreationTimestamp.Time) || 
			cmC.CreationTimestamp.Time.Equal(cmB.CreationTimestamp.Time)).To(BeTrue(), 
			"Component C should deploy before or at same time as B")
		Expect(cmB.CreationTimestamp.Time.Before(cmA.CreationTimestamp.Time) || 
			cmB.CreationTimestamp.Time.Equal(cmA.CreationTimestamp.Time)).To(BeTrue(), 
			"Component B should deploy before or at same time as A")
	})

	It("should fail when requesting non-existent component", func() {
		By("creating ZarfPackage with invalid component name")
		pkg := &v1alpha1.ZarfPackage{
			ObjectMeta: metav1.ObjectMeta{
				Name: testPackageName + "-invalid",
			},
			Spec: v1alpha1.ZarfPackageSpec{
				ForProvider: v1alpha1.ZarfPackageParameters{
					Source: packageSource,
					Components: []string{
						"component-a",
						"nonexistent-component",
						"component-b",
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, pkg)).To(Succeed())

		By("waiting for package to report failure")
		Eventually(func() bool {
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(pkg), pkg); err != nil {
				return false
			}
			// Check if package has a failure condition
			for _, cond := range pkg.Status.Conditions {
				if cond.Type == "Ready" && cond.Status == "False" {
					return true
				}
			}
			return false
		}, 2*time.Minute, 5*time.Second).Should(BeTrue(), "Package should report failure for invalid component")

		By("verifying error message mentions missing component")
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pkg), pkg)).To(Succeed())
		var foundError bool
		for _, cond := range pkg.Status.Conditions {
			if cond.Type == "Ready" && cond.Status == "False" {
				Expect(cond.Message).To(ContainSubstring("nonexistent-component"), 
					"Error message should mention the missing component")
				foundError = true
				break
			}
		}
		Expect(foundError).To(BeTrue(), "Should have found error condition")
	})
})
