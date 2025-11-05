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
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("Provider CLI Tools", func() {
	var (
		ctx              context.Context
		providerPodName  string
		providerPodNS    string
		containerName    string
	)

	BeforeEach(func() {
		ctx = context.Background()
		
		By("finding the provider pod")
		// Provider is typically in crossplane-system namespace
		providerPodNS = "crossplane-system"
		
		// Find pod with provider-zarf label
		podList := &corev1.PodList{}
		Eventually(func() error {
			return k8sClient.List(ctx, podList, 
				client.InNamespace(providerPodNS),
				client.MatchingLabels{"pkg.crossplane.io/provider": "provider-zarf"},
			)
		}, 30*time.Second, 2*time.Second).Should(Succeed(), "Should be able to list provider pods")
		
		Expect(podList.Items).NotTo(BeEmpty(), "Should have at least one provider pod")
		
		// Get first running pod
		var foundPod *corev1.Pod
		for i := range podList.Items {
			pod := &podList.Items[i]
			if pod.Status.Phase == corev1.PodRunning {
				foundPod = pod
				break
			}
		}
		Expect(foundPod).NotTo(BeNil(), "Should find a running provider pod")
		
		providerPodName = foundPod.Name
		
		// Find the package-runtime container
		containerName = "package-runtime"
		var foundContainer bool
		for _, container := range foundPod.Spec.Containers {
			if container.Name == "package-runtime" {
				foundContainer = true
				break
			}
		}
		Expect(foundContainer).To(BeTrue(), "Should find package-runtime container")
		
		GinkgoWriter.Printf("Using provider pod: %s/%s\n", providerPodNS, providerPodName)
	})

	Context("kubectl availability", func() {
		It("should have kubectl binary available", func() {
			stdout, stderr, err := execInPod(ctx, providerPodNS, providerPodName, containerName, []string{"which", "kubectl"})
			Expect(err).NotTo(HaveOccurred(), "kubectl binary should exist: %v\nstderr: %s", err, stderr)
			Expect(stdout).To(ContainSubstring("/usr/local/bin/kubectl"), "kubectl should be in /usr/local/bin")
		})

		It("should run kubectl version --client", func() {
			stdout, stderr, err := execInPod(ctx, providerPodNS, providerPodName, containerName, 
				[]string{"kubectl", "version", "--client", "--output=json"})
			Expect(err).NotTo(HaveOccurred(), "kubectl version should run successfully: %v\nstderr: %s", err, stderr)
			Expect(stdout).To(ContainSubstring("clientVersion"), "kubectl should output version info")
		})

		It("should be able to list namespaces using kubectl", func() {
			stdout, stderr, err := execInPod(ctx, providerPodNS, providerPodName, containerName,
				[]string{"kubectl", "get", "namespaces", "--output=name"})
			Expect(err).NotTo(HaveOccurred(), "kubectl get namespaces should work: %v\nstderr: %s", err, stderr)
			Expect(stdout).To(ContainSubstring("namespace/"), "Should list namespaces")
		})
	})

	Context("busybox utilities", func() {
		It("should have busybox binary available", func() {
			stdout, stderr, err := execInPod(ctx, providerPodNS, providerPodName, containerName, []string{"which", "busybox"})
			Expect(err).NotTo(HaveOccurred(), "busybox binary should exist: %v\nstderr: %s", err, stderr)
			Expect(stdout).To(ContainSubstring("busybox"), "busybox should be found")
		})

		It("should have ls command available", func() {
			stdout, stderr, err := execInPod(ctx, providerPodNS, providerPodName, containerName, []string{"ls", "/"})
			Expect(err).NotTo(HaveOccurred(), "ls command should work: %v\nstderr: %s", err, stderr)
			Expect(stdout).NotTo(BeEmpty(), "ls should produce output")
		})

		It("should have cat command available", func() {
			// Create a test file and read it
			_, _, err := execInPod(ctx, providerPodNS, providerPodName, containerName,
				[]string{"sh", "-c", "echo 'test content' > /tmp/test-file.txt"})
			Expect(err).NotTo(HaveOccurred(), "Should be able to create test file")

			stdout, stderr, err := execInPod(ctx, providerPodNS, providerPodName, containerName,
				[]string{"cat", "/tmp/test-file.txt"})
			Expect(err).NotTo(HaveOccurred(), "cat command should work: %v\nstderr: %s", err, stderr)
			Expect(stdout).To(ContainSubstring("test content"), "cat should read file content")
		})

		It("should have cp command available", func() {
			// Create a file and copy it
			_, _, err := execInPod(ctx, providerPodNS, providerPodName, containerName,
				[]string{"sh", "-c", "echo 'source' > /tmp/source.txt"})
			Expect(err).NotTo(HaveOccurred(), "Should create source file")

			_, stderr, err := execInPod(ctx, providerPodNS, providerPodName, containerName,
				[]string{"cp", "/tmp/source.txt", "/tmp/dest.txt"})
			Expect(err).NotTo(HaveOccurred(), "cp command should work: %v\nstderr: %s", err, stderr)

			stdout, _, err := execInPod(ctx, providerPodNS, providerPodName, containerName,
				[]string{"cat", "/tmp/dest.txt"})
			Expect(err).NotTo(HaveOccurred(), "Should read copied file")
			Expect(stdout).To(ContainSubstring("source"), "Copied file should have same content")
		})

		It("should have mv command available", func() {
			_, _, err := execInPod(ctx, providerPodNS, providerPodName, containerName,
				[]string{"sh", "-c", "echo 'move me' > /tmp/move-source.txt"})
			Expect(err).NotTo(HaveOccurred(), "Should create source file")

			_, stderr, err := execInPod(ctx, providerPodNS, providerPodName, containerName,
				[]string{"mv", "/tmp/move-source.txt", "/tmp/moved.txt"})
			Expect(err).NotTo(HaveOccurred(), "mv command should work: %v\nstderr: %s", err, stderr)

			stdout, _, err := execInPod(ctx, providerPodNS, providerPodName, containerName,
				[]string{"cat", "/tmp/moved.txt"})
			Expect(err).NotTo(HaveOccurred(), "Should read moved file")
			Expect(stdout).To(ContainSubstring("move me"), "Moved file should have same content")
		})

		It("should have rm command available", func() {
			_, _, err := execInPod(ctx, providerPodNS, providerPodName, containerName,
				[]string{"sh", "-c", "echo 'delete me' > /tmp/delete-me.txt"})
			Expect(err).NotTo(HaveOccurred(), "Should create file to delete")

			_, stderr, err := execInPod(ctx, providerPodNS, providerPodName, containerName,
				[]string{"rm", "/tmp/delete-me.txt"})
			Expect(err).NotTo(HaveOccurred(), "rm command should work: %v\nstderr: %s", err, stderr)

			_, _, err = execInPod(ctx, providerPodNS, providerPodName, containerName,
				[]string{"cat", "/tmp/delete-me.txt"})
			Expect(err).To(HaveOccurred(), "File should not exist after deletion")
		})

		It("should have echo command available", func() {
			stdout, stderr, err := execInPod(ctx, providerPodNS, providerPodName, containerName,
				[]string{"echo", "hello world"})
			Expect(err).NotTo(HaveOccurred(), "echo command should work: %v\nstderr: %s", err, stderr)
			Expect(stdout).To(ContainSubstring("hello world"), "echo should output the string")
		})

		It("should have sleep command available", func() {
			start := time.Now()
			_, stderr, err := execInPod(ctx, providerPodNS, providerPodName, containerName,
				[]string{"sleep", "1"})
			duration := time.Since(start)
			
			Expect(err).NotTo(HaveOccurred(), "sleep command should work: %v\nstderr: %s", err, stderr)
			Expect(duration).To(BeNumerically(">=", 1*time.Second), "sleep should actually wait")
		})
	})
})

// execInPod executes a command in a pod container and returns stdout, stderr, and error
func execInPod(ctx context.Context, namespace, podName, containerName string, command []string) (string, string, error) {
	req := kubeClient.CoreV1().RESTClient().
		Post().
		Resource("pods").
		Name(podName).
		Namespace(namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: containerName,
			Command:   command,
			Stdin:     false,
			Stdout:    true,
			Stderr:    true,
			TTY:       false,
		}, scheme.ParameterCodec)

	// Create SPDY executor - we need the raw REST config
	// Get it from the kubeconfig that was used to create kubeClient
	kubeconfigPath := os.Getenv("KUBECONFIG")
	if kubeconfigPath == "" {
		home := os.Getenv("HOME")
		kubeconfigPath = filepath.Join(home, ".kube", "config")
	}
	
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		return "", "", err
	}
	
	exec, err := remotecommand.NewSPDYExecutor(config, "POST", req.URL())
	if err != nil {
		return "", "", err
	}

	var stdout, stderr bytes.Buffer
	err = exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	})

	return strings.TrimSpace(stdout.String()), strings.TrimSpace(stderr.String()), err
}
