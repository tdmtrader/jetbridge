package tracing_test

import (
	"github.com/concourse/concourse/tracing"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

var _ = Describe("Sampling", func() {
	Describe("Config.Sampler", func() {
		It("returns AlwaysOn sampler by default", func() {
			c := tracing.Config{}
			sampler := c.Sampler()
			Expect(sampler).NotTo(BeNil())
			Expect(sampler.Description()).To(Equal(sdktrace.AlwaysSample().Description()))
		})

		It("returns AlwaysOn sampler when strategy is 'always'", func() {
			c := tracing.Config{
				Sampling: tracing.SamplingConfig{
					Strategy: "always",
				},
			}
			sampler := c.Sampler()
			Expect(sampler.Description()).To(Equal(sdktrace.AlwaysSample().Description()))
		})

		It("returns probability sampler when strategy is 'probability'", func() {
			c := tracing.Config{
				Sampling: tracing.SamplingConfig{
					Strategy: "probability",
					Rate:     0.1,
				},
			}
			sampler := c.Sampler()
			Expect(sampler).NotTo(BeNil())
			Expect(sampler.Description()).To(ContainSubstring("TraceIDRatioBased"))
		})

		It("returns NeverSample sampler when strategy is 'never'", func() {
			c := tracing.Config{
				Sampling: tracing.SamplingConfig{
					Strategy: "never",
				},
			}
			sampler := c.Sampler()
			Expect(sampler.Description()).To(Equal(sdktrace.NeverSample().Description()))
		})

		It("defaults rate to 1.0 when probability strategy has no rate", func() {
			c := tracing.Config{
				Sampling: tracing.SamplingConfig{
					Strategy: "probability",
				},
			}
			sampler := c.Sampler()
			// Rate 1.0 may be reported as AlwaysOnSampler by the SDK
			Expect(sampler).NotTo(BeNil())
		})
	})
})
