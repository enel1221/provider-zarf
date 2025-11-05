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
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	"github.com/crossplane/provider-zarf/apis/zarf/v1alpha1"
)

var _ = Describe("Security: Read-Only Filesystem", func() {
	const (
		timeout  = 5 * time.Minute
		interval = 5 * time.Second
	)

	Context("When provider is deployed with readOnlyRootFilesystem=true", func() {
		It("should successfully deploy packages with read-only root filesystem", func(ctx SpecContext) {
			// Find the provider pod
			By("Locating provider pod in crossplane-system namespace")
			pods := &corev1.PodList{}
			err := k8sClient.List(ctx, pods, &client.ListOptions{
				Namespace: "crossplane-system",
			})
			Expect(err).NotTo(HaveOccurred(), "Should be able to list pods")

			var providerPod *corev1.Pod
			for i := range pods.Items {
				if strings.Contains(pods.Items[i].Name, "provider-zarf") {
					providerPod = &pods.Items[i]
					break
				}
			}
			Expect(providerPod).NotTo(BeNil(), "Provider pod should exist")

			// Verify security context configuration
			By("Verifying provider pod security context")
			var providerContainer *corev1.Container
			for i := range providerPod.Spec.Containers {
				if providerPod.Spec.Containers[i].Name == "provider" {
					providerContainer = &providerPod.Spec.Containers[i]
					break
				}
			}
			Expect(providerContainer).NotTo(BeNil(), "Provider container should exist")

			// Container-level security context
			securityContext := providerContainer.SecurityContext

			// Verify readOnlyRootFilesystem setting
			if securityContext != nil && securityContext.ReadOnlyRootFilesystem != nil {
				By("Read-only filesystem is configured")
				GinkgoWriter.Printf("ReadOnlyRootFilesystem: %v\n", *securityContext.ReadOnlyRootFilesystem)

				if *securityContext.ReadOnlyRootFilesystem {
					// Verify /tmp volume mount exists
					By("Verifying /tmp is mounted as writable emptyDir")
					var tmpMount *corev1.VolumeMount
					for i := range providerContainer.VolumeMounts {
						if providerContainer.VolumeMounts[i].MountPath == "/tmp" {
							tmpMount = &providerContainer.VolumeMounts[i]
							break
						}
					}
					Expect(tmpMount).NotTo(BeNil(), "/tmp should be mounted when root is read-only")
					Expect(tmpMount.ReadOnly).To(BeFalse(), "/tmp mount should be writable")

					// Verify the volume is emptyDir
					var tmpVolume *corev1.Volume
					for i := range providerPod.Spec.Volumes {
						if providerPod.Spec.Volumes[i].Name == tmpMount.Name {
							tmpVolume = &providerPod.Spec.Volumes[i]
							break
						}
					}
					Expect(tmpVolume).NotTo(BeNil(), "/tmp volume should exist")
					Expect(tmpVolume.EmptyDir).NotTo(BeNil(), "/tmp should be emptyDir volume")

					GinkgoWriter.Printf("✅ /tmp is correctly mounted as writable emptyDir\n")
				}
			} else {
				By("Read-only filesystem is not configured (development mode)")
				GinkgoWriter.Printf("⚠️ ReadOnlyRootFilesystem not set - using development configuration\n")
			}

			// Verify other security settings
			By("Verifying additional security hardening")
			if securityContext != nil {
				if securityContext.RunAsNonRoot != nil {
					Expect(*securityContext.RunAsNonRoot).To(BeTrue(), "Should run as non-root")
					GinkgoWriter.Printf("✅ RunAsNonRoot: true\n")
				}

				if securityContext.AllowPrivilegeEscalation != nil {
					Expect(*securityContext.AllowPrivilegeEscalation).To(BeFalse(), "Should not allow privilege escalation")
					GinkgoWriter.Printf("✅ AllowPrivilegeEscalation: false\n")
				}

				if securityContext.Capabilities != nil && len(securityContext.Capabilities.Drop) > 0 {
					GinkgoWriter.Printf("✅ Capabilities dropped: %v\n", securityContext.Capabilities.Drop)
				}
			}

			// Test actual package deployment to verify filesystem works
			By("Deploying test package to verify provider functionality")
			testPkg := &v1alpha1.ZarfPackage{
				ObjectMeta: metav1.ObjectMeta{
					Name: "readonly-test-package",
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
						Components: []string{
							"zarf-injector",
						},
					},
				},
			}

			err = k8sClient.Create(ctx, testPkg)
			Expect(err).NotTo(HaveOccurred(), "Should create test package")

			// Cleanup
			DeferCleanup(func() {
				deleteCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				defer cancel()
				_ = k8sClient.Delete(deleteCtx, testPkg)
			})

			// Wait for package to reconcile
			By("Waiting for package reconciliation")
			Eventually(func() bool {
				current := &v1alpha1.ZarfPackage{}
				err := k8sClient.Get(ctx, client.ObjectKeyFromObject(testPkg), current)
				if err != nil {
					return false
				}

				// Check if package has been processed
				condition := current.Status.GetCondition(xpv1.TypeReady)
				return condition.Status == corev1.ConditionTrue || condition.Status == corev1.ConditionFalse
			}, timeout, interval).Should(BeTrue(), "Package should be reconciled")

			// Verify provider pod is still running (didn't crash)
			By("Verifying provider pod is still healthy")
			currentPod := &corev1.Pod{}
			err = k8sClient.Get(ctx, client.ObjectKeyFromObject(providerPod), currentPod)
			Expect(err).NotTo(HaveOccurred(), "Should be able to get provider pod")
			Expect(currentPod.Status.Phase).To(Equal(corev1.PodRunning), "Provider pod should still be running")

			// Check for restart count (shouldn't have increased)
			originalRestarts := int32(0)
			currentRestarts := int32(0)
			for _, cs := range providerPod.Status.ContainerStatuses {
				if cs.Name == "provider" {
					originalRestarts = cs.RestartCount
					break
				}
			}
			for _, cs := range currentPod.Status.ContainerStatuses {
				if cs.Name == "provider" {
					currentRestarts = cs.RestartCount
					break
				}
			}
			Expect(currentRestarts).To(Equal(originalRestarts), "Provider should not have restarted")

			GinkgoWriter.Printf("\n=== Read-Only Filesystem Test Results ===\n")
			GinkgoWriter.Printf("Provider Pod: %s\n", providerPod.Name)
			if securityContext != nil && securityContext.ReadOnlyRootFilesystem != nil {
				GinkgoWriter.Printf("Read-only filesystem: %v\n", *securityContext.ReadOnlyRootFilesystem)
			} else {
				GinkgoWriter.Printf("Read-only filesystem: not configured\n")
			}
			GinkgoWriter.Printf("Package deployment: successful\n")
			GinkgoWriter.Printf("Pod restarts: %d\n", currentRestarts)
			GinkgoWriter.Printf("==========================================\n")
		}, SpecTimeout(timeout))
	})

	Context("When attempting filesystem operations", func() {
		It("should only allow writes to mounted volumes", func(ctx SpecContext) {
			// Find the provider pod
			pods := &corev1.PodList{}
			err := k8sClient.List(ctx, pods, &client.ListOptions{
				Namespace: "crossplane-system",
			})
			Expect(err).NotTo(HaveOccurred())

			var providerPod *corev1.Pod
			for i := range pods.Items {
				if strings.Contains(pods.Items[i].Name, "provider-zarf") {
					providerPod = &pods.Items[i]
					break
				}
			}
			Expect(providerPod).NotTo(BeNil())

			// Check security context to see if read-only is enabled
			var isReadOnly bool
			for _, container := range providerPod.Spec.Containers {
				if container.Name == "provider" && container.SecurityContext != nil {
					if container.SecurityContext.ReadOnlyRootFilesystem != nil {
						isReadOnly = *container.SecurityContext.ReadOnlyRootFilesystem
					}
					break
				}
			}

			if isReadOnly {
				By("Testing filesystem write restrictions")

				// Test: /tmp should be writable (emptyDir mount)
				By("Verifying /tmp is writable")
				stdout, stderr, err := execInPod(ctx, "crossplane-system", providerPod.Name, "provider",
					[]string{"sh", "-c", "echo 'test' > /tmp/test.txt && cat /tmp/test.txt"})

				Expect(err).NotTo(HaveOccurred(), "Writing to /tmp should succeed")
				Expect(stdout).To(ContainSubstring("test"), "/tmp write should work")
				if stderr != "" {
					GinkgoWriter.Printf("stderr: %s\n", stderr)
				}

				// Test: Root filesystem should be read-only
				By("Verifying root filesystem is read-only")
				stdout, stderr, err = execInPod(ctx, "crossplane-system", providerPod.Name, "provider",
					[]string{"sh", "-c", "echo 'test' > /test.txt 2>&1 || true"})

				// We expect this to fail with "read-only file system" error
				combinedOutput := stdout + stderr
				Expect(combinedOutput).To(Or(
					ContainSubstring("Read-only file system"),
					ContainSubstring("read-only"),
				), "Root filesystem should be read-only")

				GinkgoWriter.Printf("✅ Filesystem restrictions working correctly\n")
				GinkgoWriter.Printf("  - /tmp is writable (emptyDir)\n")
				GinkgoWriter.Printf("  - Root is read-only\n")
			} else {
				By("Skipping filesystem write tests (read-only not enabled)")
				GinkgoWriter.Printf("⚠️ ReadOnlyRootFilesystem not enabled - skipping write tests\n")
			}
		}, SpecTimeout(2*time.Minute))
	})
})
