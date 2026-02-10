package config_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/config"
)

var _ = Describe("QAConfig", func() {
	Describe("DefaultQAConfig", func() {
		It("enables test generation and browser plan by default", func() {
			cfg := config.DefaultQAConfig()
			Expect(cfg.GenerateTests).To(BeTrue())
			Expect(cfg.BrowserPlan).To(BeTrue())
			Expect(cfg.Threshold).To(BeNumerically("~", 7.0, 0.01))
		})
	})

	Describe("LoadQAConfig", func() {
		It("parses valid YAML config", func() {
			yaml := []byte(`
threshold: 8.0
generate_tests: false
browser_plan: true
include:
  - "*.go"
exclude:
  - "*_test.go"
`)
			cfg, err := config.LoadQAConfig(yaml)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Threshold).To(BeNumerically("~", 8.0, 0.01))
			Expect(cfg.GenerateTests).To(BeFalse())
			Expect(cfg.BrowserPlan).To(BeTrue())
			Expect(cfg.Include).To(HaveLen(1))
			Expect(cfg.Exclude).To(HaveLen(1))
		})

		It("uses defaults for missing fields", func() {
			yaml := []byte(`threshold: 5.0`)
			cfg, err := config.LoadQAConfig(yaml)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Threshold).To(BeNumerically("~", 5.0, 0.01))
			Expect(cfg.GenerateTests).To(BeTrue()) // default
			Expect(cfg.BrowserPlan).To(BeTrue())   // default
		})

		It("disables test generation when configured", func() {
			yaml := []byte(`generate_tests: false`)
			cfg, err := config.LoadQAConfig(yaml)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.GenerateTests).To(BeFalse())
		})

		It("disables browser plan when configured", func() {
			yaml := []byte(`browser_plan: false`)
			cfg, err := config.LoadQAConfig(yaml)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.BrowserPlan).To(BeFalse())
		})
	})

	Describe("MatchesFile", func() {
		It("matches when no include/exclude patterns", func() {
			cfg := config.DefaultQAConfig()
			Expect(cfg.MatchesFile("anything.go")).To(BeTrue())
		})

		It("filters by include pattern", func() {
			cfg := &config.QAConfig{Include: []string{"*.go"}}
			Expect(cfg.MatchesFile("main.go")).To(BeTrue())
			Expect(cfg.MatchesFile("style.css")).To(BeFalse())
		})

		It("filters by exclude pattern", func() {
			cfg := &config.QAConfig{Exclude: []string{"*_test.go"}}
			Expect(cfg.MatchesFile("main.go")).To(BeTrue())
			Expect(cfg.MatchesFile("main_test.go")).To(BeFalse())
		})

		It("exclude takes precedence over include", func() {
			cfg := &config.QAConfig{
				Include: []string{"*.go"},
				Exclude: []string{"*_test.go"},
			}
			Expect(cfg.MatchesFile("main.go")).To(BeTrue())
			Expect(cfg.MatchesFile("main_test.go")).To(BeFalse())
		})
	})
})
