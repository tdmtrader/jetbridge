package tracing

import (
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// SamplingConfig holds trace sampling configuration.
type SamplingConfig struct {
	Strategy string  `long:"sampling-strategy" description:"trace sampling strategy: always, never, probability" default:"always"`
	Rate     float64 `long:"sampling-rate"     description:"sampling rate for probability strategy (0.0 to 1.0)" default:"1.0"`
}

// Sampler returns a configured sdktrace.Sampler based on the Config's sampling settings.
func (c Config) Sampler() sdktrace.Sampler {
	switch c.Sampling.Strategy {
	case "never":
		return sdktrace.NeverSample()
	case "probability":
		rate := c.Sampling.Rate
		if rate == 0 {
			rate = 1.0
		}
		return sdktrace.TraceIDRatioBased(rate)
	default:
		return sdktrace.AlwaysSample()
	}
}
