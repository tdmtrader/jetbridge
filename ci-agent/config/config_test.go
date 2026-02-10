package config_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/config"
)

var _ = Describe("ReviewConfig", func() {
	Describe("LoadConfig", func() {
		It("parses a valid review.yml", func() {
			yaml := []byte(`
version: "1"
severity_weights:
  critical: 5.0
  high: 2.0
  medium: 1.0
  low: 0.25
categories:
  security:
    enabled: true
  correctness:
    enabled: true
  performance:
    enabled: false
include:
  - "**/*.go"
exclude:
  - "vendor/**"
`)
			cfg, err := config.LoadConfig(yaml)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.SeverityWeights.Critical).To(Equal(5.0))
			Expect(cfg.SeverityWeights.High).To(Equal(2.0))
			Expect(cfg.SeverityWeights.Low).To(Equal(0.25))
			Expect(cfg.Categories["security"].Enabled).To(BeTrue())
			Expect(cfg.Categories["performance"].Enabled).To(BeFalse())
			Expect(cfg.Include).To(ConsistOf("**/*.go"))
			Expect(cfg.Exclude).To(ConsistOf("vendor/**"))
		})

		It("uses defaults for missing severity weights", func() {
			yaml := []byte(`
version: "1"
severity_weights:
  critical: 4.0
`)
			cfg, err := config.LoadConfig(yaml)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.SeverityWeights.Critical).To(Equal(4.0))
			Expect(cfg.SeverityWeights.High).To(Equal(1.5))
			Expect(cfg.SeverityWeights.Medium).To(Equal(1.0))
			Expect(cfg.SeverityWeights.Low).To(Equal(0.5))
		})

		It("returns default profile for empty config", func() {
			cfg, err := config.LoadConfig([]byte(""))
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg).NotTo(BeNil())
			Expect(cfg.SeverityWeights.Critical).To(Equal(3.0))
		})
	})

	Describe("LoadProfile", func() {
		It("loads built-in 'default' profile", func() {
			cfg, err := config.LoadProfile("default")
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.SeverityWeights.Critical).To(Equal(3.0))
			Expect(cfg.SeverityWeights.High).To(Equal(1.5))
		})

		It("loads built-in 'security' profile", func() {
			cfg, err := config.LoadProfile("security")
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.SeverityWeights.Critical).To(BeNumerically(">", 3.0))
		})

		It("loads built-in 'strict' profile", func() {
			cfg, err := config.LoadProfile("strict")
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.SeverityWeights.Low).To(BeNumerically(">", 0.5))
		})

		It("returns error for unknown profile", func() {
			_, err := config.LoadProfile("nonexistent")
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("DefaultConfig", func() {
		It("returns a valid default config", func() {
			cfg := config.DefaultConfig()
			Expect(cfg.SeverityWeights.Critical).To(Equal(3.0))
			Expect(cfg.SeverityWeights.High).To(Equal(1.5))
			Expect(cfg.SeverityWeights.Medium).To(Equal(1.0))
			Expect(cfg.SeverityWeights.Low).To(Equal(0.5))
		})
	})

	Describe("ShouldReview", func() {
		It("matches include patterns", func() {
			cfg := &config.ReviewConfig{
				Include: []string{"**/*.go"},
			}
			Expect(cfg.ShouldReview("main.go")).To(BeTrue())
			Expect(cfg.ShouldReview("pkg/util.go")).To(BeTrue())
			Expect(cfg.ShouldReview("README.md")).To(BeFalse())
		})

		It("excludes matching patterns", func() {
			cfg := &config.ReviewConfig{
				Include: []string{"**/*.go"},
				Exclude: []string{"vendor/**", "**/*_test.go"},
			}
			Expect(cfg.ShouldReview("main.go")).To(BeTrue())
			Expect(cfg.ShouldReview("vendor/lib/foo.go")).To(BeFalse())
			Expect(cfg.ShouldReview("pkg/util_test.go")).To(BeFalse())
		})

		It("reviews everything when no include patterns specified", func() {
			cfg := &config.ReviewConfig{}
			Expect(cfg.ShouldReview("anything.go")).To(BeTrue())
			Expect(cfg.ShouldReview("dir/file.py")).To(BeTrue())
		})
	})
})
