package k8sruntime_test

import (
	"os"
	"path/filepath"

	"github.com/concourse/concourse/atc/worker/k8sruntime"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Config", func() {
	Describe("NewConfig", func() {
		It("returns a config with the given namespace", func() {
			cfg := k8sruntime.NewConfig("my-namespace", "")
			Expect(cfg.Namespace).To(Equal("my-namespace"))
		})

		It("defaults namespace to 'default' when empty", func() {
			cfg := k8sruntime.NewConfig("", "")
			Expect(cfg.Namespace).To(Equal("default"))
		})

		It("stores the kubeconfig path when provided", func() {
			cfg := k8sruntime.NewConfig("my-namespace", "/path/to/kubeconfig")
			Expect(cfg.KubeconfigPath).To(Equal("/path/to/kubeconfig"))
		})
	})

	Describe("NewClientset", func() {
		Context("when a valid kubeconfig is provided", func() {
			var kubeconfigPath string

			BeforeEach(func() {
				tmpDir := GinkgoT().TempDir()
				kubeconfigPath = filepath.Join(tmpDir, "kubeconfig")
				kubeconfig := `apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://127.0.0.1:6443
    insecure-skip-tls-verify: true
  name: test-cluster
contexts:
- context:
    cluster: test-cluster
    user: test-user
  name: test-context
current-context: test-context
users:
- name: test-user
  user:
    token: fake-token
`
				err := os.WriteFile(kubeconfigPath, []byte(kubeconfig), 0600)
				Expect(err).ToNot(HaveOccurred())
			})

			It("creates a clientset from the kubeconfig file", func() {
				cfg := k8sruntime.NewConfig("my-namespace", kubeconfigPath)
				clientset, err := k8sruntime.NewClientset(cfg)
				Expect(err).ToNot(HaveOccurred())
				Expect(clientset).ToNot(BeNil())
			})
		})

		Context("when the kubeconfig path does not exist", func() {
			It("returns an error", func() {
				cfg := k8sruntime.NewConfig("my-namespace", "/nonexistent/kubeconfig")
				_, err := k8sruntime.NewClientset(cfg)
				Expect(err).To(HaveOccurred())
			})
		})

		Context("when no kubeconfig is provided and not in-cluster", func() {
			It("returns an error", func() {
				// Ensure we're not running in a K8s cluster
				os.Unsetenv("KUBERNETES_SERVICE_HOST")
				os.Unsetenv("KUBERNETES_SERVICE_PORT")

				cfg := k8sruntime.NewConfig("my-namespace", "")
				_, err := k8sruntime.NewClientset(cfg)
				Expect(err).To(HaveOccurred())
			})
		})
	})
})
