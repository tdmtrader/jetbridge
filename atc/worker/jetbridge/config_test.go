package jetbridge_test

import (
	"os"
	"path/filepath"
	"time"

	"github.com/concourse/concourse/atc/worker/jetbridge"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Config", func() {
	Describe("NewConfig", func() {
		It("returns a config with the given namespace", func() {
			cfg := jetbridge.NewConfig("my-namespace", "")
			Expect(cfg.Namespace).To(Equal("my-namespace"))
		})

		It("defaults namespace to 'default' when empty", func() {
			cfg := jetbridge.NewConfig("", "")
			Expect(cfg.Namespace).To(Equal("default"))
		})

		It("stores the kubeconfig path when provided", func() {
			cfg := jetbridge.NewConfig("my-namespace", "/path/to/kubeconfig")
			Expect(cfg.KubeconfigPath).To(Equal("/path/to/kubeconfig"))
		})

		It("defaults PodStartupTimeout to 5 minutes", func() {
			cfg := jetbridge.NewConfig("my-namespace", "")
			Expect(cfg.PodStartupTimeout).To(Equal(5 * time.Minute))
		})
	})

	Describe("CacheBasePath constant", func() {
		It("equals /concourse/cache", func() {
			Expect(jetbridge.CacheBasePath).To(Equal("/concourse/cache"))
		})
	})

	Describe("CacheVolumeClaim field", func() {
		It("defaults to empty when not set", func() {
			cfg := jetbridge.NewConfig("my-namespace", "")
			Expect(cfg.CacheVolumeClaim).To(BeEmpty())
		})

		It("can be set to a PVC name", func() {
			cfg := jetbridge.NewConfig("my-namespace", "")
			cfg.CacheVolumeClaim = "concourse-cache"
			Expect(cfg.CacheVolumeClaim).To(Equal("concourse-cache"))
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
				cfg := jetbridge.NewConfig("my-namespace", kubeconfigPath)
				clientset, err := jetbridge.NewClientset(cfg)
				Expect(err).ToNot(HaveOccurred())
				Expect(clientset).ToNot(BeNil())
			})
		})

		Context("when the kubeconfig path does not exist", func() {
			It("returns an error", func() {
				cfg := jetbridge.NewConfig("my-namespace", "/nonexistent/kubeconfig")
				_, err := jetbridge.NewClientset(cfg)
				Expect(err).To(HaveOccurred())
			})
		})

		Context("when no kubeconfig is provided and not in-cluster", func() {
			It("returns an error", func() {
				// Ensure we're not running in a K8s cluster
				os.Unsetenv("KUBERNETES_SERVICE_HOST")
				os.Unsetenv("KUBERNETES_SERVICE_PORT")

				cfg := jetbridge.NewConfig("my-namespace", "")
				_, err := jetbridge.NewClientset(cfg)
				Expect(err).To(HaveOccurred())
			})
		})
	})
})
