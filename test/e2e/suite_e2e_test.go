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
	"os"
	"path/filepath"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/provider-zarf/apis/zarf/v1alpha1"
)

var (
	k8sClient  client.Client
	kubeClient *kubernetes.Clientset
	restConfig *rest.Config
	testScheme *runtime.Scheme
	ctx        = context.Background()
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
	restConfig = config

	// Create Kubernetes clientset
	kubeClient, err = kubernetes.NewForConfig(config)
	Expect(err).NotTo(HaveOccurred(), "Failed to create kubernetes clientset")

	// Create scheme and register types
	testScheme = runtime.NewScheme()
	Expect(clientgoscheme.AddToScheme(testScheme)).To(Succeed())
	Expect(v1alpha1.SchemeBuilder.AddToScheme(testScheme)).To(Succeed())

	// Create controller-runtime client
	k8sClient, err = client.New(config, client.Options{Scheme: testScheme})
	Expect(err).NotTo(HaveOccurred(), "Failed to create controller-runtime client")

	// Verify connectivity
	By("verifying cluster connectivity")
	nodes := &corev1.NodeList{}
	err = k8sClient.List(ctx, nodes)
	Expect(err).NotTo(HaveOccurred(), "Failed to list nodes - cluster not accessible")

	GinkgoWriter.Printf("✅ Connected to Kubernetes cluster with %d nodes\n", len(nodes.Items))
})
