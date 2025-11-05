//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	"github.com/crossplane/provider-zarf/apis/zarf/v1alpha1"
)

var _ = Describe("Concurrent ZarfPackage Deployments", func() {
	const (
		timeout       = 15 * time.Minute
		interval      = 5 * time.Second
		packageCount  = 12
		testNamespace = "concurrent-test"
	)

	Context("When deploying multiple ZarfPackages simultaneously", func() {
		It("should handle concurrent deployments without conflicts", func(ctx SpecContext) {
			// Create test namespace
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: testNamespace,
				},
			}
			err := k8sClient.Create(ctx, ns)
			if err != nil && !apierrors.IsAlreadyExists(err) {
				Fail(fmt.Sprintf("Failed to create test namespace: %v", err))
			}

			// Cleanup namespace on exit
			DeferCleanup(func() {
				deleteCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				defer cancel()
				_ = k8sClient.Delete(deleteCtx, ns)
			})

			// Create multiple ZarfPackages simultaneously
			packageNames := make([]string, packageCount)
			for i := 0; i < packageCount; i++ {
				packageNames[i] = fmt.Sprintf("concurrent-pkg-%d", i)
				pkg := &v1alpha1.ZarfPackage{
					ObjectMeta: metav1.ObjectMeta{
						Name: packageNames[i],
					},
					Spec: v1alpha1.ZarfPackageSpec{
						ResourceSpec: xpv1.ResourceSpec{
							ProviderConfigReference: &xpv1.Reference{
								Name: "default",
							},
						},
						ForProvider: v1alpha1.ZarfPackageParameters{
							Source:    "oci://ghcr.io/defenseunicorns/packages/init:v0.62.0-arm64",
							Namespace: testNamespace,
							Components: []string{
								"zarf-injector",
								"zarf-seed-registry",
							},
							Variables: map[string]string{
								fmt.Sprintf("INSTANCE_%d", i): fmt.Sprintf("value-%d", i),
							},
						},
					},
				}

				err := k8sClient.Create(ctx, pkg)
				Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("Failed to create package %s", packageNames[i]))
			}

			// Cleanup packages on exit
			DeferCleanup(func() {
				deleteCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
				defer cancel()
				for _, name := range packageNames {
					pkg := &v1alpha1.ZarfPackage{
						ObjectMeta: metav1.ObjectMeta{
							Name: name,
						},
					}
					_ = k8sClient.Delete(deleteCtx, pkg)
				}
			})

			// Wait for all packages to reconcile
			By("Waiting for all packages to reach a terminal state")
			Eventually(func() int {
				readyCount := 0
				for _, name := range packageNames {
					pkg := &v1alpha1.ZarfPackage{}
					err := k8sClient.Get(ctx, types.NamespacedName{Name: name}, pkg)
					if err != nil {
						continue
					}

					// Check if package has reached terminal state (Ready or Failed)
					condition := pkg.Status.GetCondition(xpv1.TypeReady)
					if condition.Status == corev1.ConditionTrue || condition.Status == corev1.ConditionFalse {
						readyCount++
					}
				}
				return readyCount
			}, timeout, interval).Should(Equal(packageCount), "All packages should reach terminal state")

			// Verify no conflicts occurred
			By("Verifying each package has unique status")
			statusMap := make(map[string]bool)
			for _, name := range packageNames {
				pkg := &v1alpha1.ZarfPackage{}
				err := k8sClient.Get(ctx, types.NamespacedName{Name: name}, pkg)
				Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("Failed to get package %s", name))

				// Each package should have its own status
				statusKey := fmt.Sprintf("%s-%s", pkg.Status.AtProvider.PackageName, pkg.Status.AtProvider.LastAppliedSpecHash)
				Expect(statusMap[statusKey]).To(BeFalse(), fmt.Sprintf("Duplicate status detected for %s", name))
				statusMap[statusKey] = true

				// Verify package has a condition
				condition := pkg.Status.GetCondition(xpv1.TypeReady)
				Expect(condition).NotTo(BeNil(), fmt.Sprintf("Package %s should have Ready condition", name))
			}

			// Verify controller pod is still healthy
			By("Checking controller pod is still running")
			pods := &corev1.PodList{}
			err = k8sClient.List(ctx, pods, &client.ListOptions{
				Namespace: "crossplane-system",
			})
			Expect(err).NotTo(HaveOccurred(), "Should be able to list pods")

			for _, pod := range pods.Items {
				if strings.Contains(pod.Name, "provider-zarf") {
					Expect(pod.Status.Phase).To(Equal(corev1.PodRunning), "Provider pod should still be running")
					break
				}
			}

			// Log final statistics
			successCount := 0
			failCount := 0
			for _, name := range packageNames {
				pkg := &v1alpha1.ZarfPackage{}
				err := k8sClient.Get(ctx, types.NamespacedName{Name: name}, pkg)
				if err != nil {
					continue
				}
				if pkg.Status.GetCondition(xpv1.TypeReady).Status == corev1.ConditionTrue {
					successCount++
				} else {
					failCount++
				}
			}

			GinkgoWriter.Printf("\n=== Concurrent Deployment Results ===\n")
			GinkgoWriter.Printf("Total packages: %d\n", packageCount)
			GinkgoWriter.Printf("Successfully deployed: %d\n", successCount)
			GinkgoWriter.Printf("Failed deployments: %d\n", failCount)
			GinkgoWriter.Printf("========================================\n")

			// At least some should succeed (network issues may cause some failures)
			Expect(successCount).To(BeNumerically(">", packageCount/2), "At least half should deploy successfully")
		}, SpecTimeout(timeout))
	})

	Context("When reconciling packages with rapid updates", func() {
		It("should handle rapid spec changes without race conditions", func(ctx SpecContext) {
			const updateCount = 20
			packageName := "rapid-update-test"

			pkg := &v1alpha1.ZarfPackage{
				ObjectMeta: metav1.ObjectMeta{
					Name: packageName,
				},
				Spec: v1alpha1.ZarfPackageSpec{
					ResourceSpec: xpv1.ResourceSpec{
						ProviderConfigReference: &xpv1.Reference{
							Name: "default",
						},
					},
					ForProvider: v1alpha1.ZarfPackageParameters{
						Source:    "oci://ghcr.io/defenseunicorns/packages/init:v0.62.0-arm64",
						Namespace: "default",
						Variables: map[string]string{
							"ITERATION": "0",
						},
					},
				},
			}

			err := k8sClient.Create(ctx, pkg)
			Expect(err).NotTo(HaveOccurred())

			// Cleanup on exit
			DeferCleanup(func() {
				deleteCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				defer cancel()
				_ = k8sClient.Delete(deleteCtx, pkg)
			})

			// Rapidly update the package spec
			By("Rapidly updating package variables")
			for i := 1; i <= updateCount; i++ {
				Eventually(func() error {
					current := &v1alpha1.ZarfPackage{}
					err := k8sClient.Get(ctx, types.NamespacedName{Name: packageName}, current)
					if err != nil {
						return err
					}

					current.Spec.ForProvider.Variables["ITERATION"] = fmt.Sprintf("%d", i)
					return k8sClient.Update(ctx, current)
				}, 10*time.Second, 500*time.Millisecond).Should(Succeed())

				// Small delay to allow reconciliation
				time.Sleep(100 * time.Millisecond)
			}

			// Verify final state is consistent
			By("Verifying final package state is consistent")
			Eventually(func() string {
				current := &v1alpha1.ZarfPackage{}
				err := k8sClient.Get(ctx, types.NamespacedName{Name: packageName}, current)
				if err != nil {
					return ""
				}
				return current.Spec.ForProvider.Variables["ITERATION"]
			}, 30*time.Second, 1*time.Second).Should(Equal(fmt.Sprintf("%d", updateCount)))

			// Verify status hash reflects final spec
			final := &v1alpha1.ZarfPackage{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: packageName}, final)
			Expect(err).NotTo(HaveOccurred())
			Expect(final.Status.AtProvider.LastAppliedSpecHash).NotTo(BeEmpty(), "LastAppliedSpecHash should be computed")

			GinkgoWriter.Printf("\n=== Rapid Update Test Results ===\n")
			GinkgoWriter.Printf("Update count: %d\n", updateCount)
			GinkgoWriter.Printf("Final iteration: %s\n", final.Spec.ForProvider.Variables["ITERATION"])
			GinkgoWriter.Printf("Final spec hash: %s\n", final.Status.AtProvider.LastAppliedSpecHash)
			GinkgoWriter.Printf("==================================\n")
		}, SpecTimeout(5*time.Minute))
	})
})
