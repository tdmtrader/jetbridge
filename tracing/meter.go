package tracing

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"google.golang.org/grpc/credentials"
)

// MetricsConfigured indicates whether OTel metrics have been configured.
var MetricsConfigured bool

// MetricsConfig holds configuration for OTel metrics export.
type MetricsConfig struct {
	OTLPAddress string            `long:"otlp-address"  description:"OTLP gRPC endpoint for metrics export"`
	OTLPHeaders map[string]string `long:"otlp-header"   description:"headers to attach to OTLP metrics requests"`
	OTLPUseTLS  bool              `long:"otlp-use-tls"  description:"use TLS for OTLP metrics connection"`
	GCPProjectID string           `long:"gcp-project-id" description:"GCP project ID for Cloud Monitoring export"`
}

// ConfigureMeterProvider sets the global OTel MeterProvider.
func ConfigureMeterProvider(mp *sdkmetric.MeterProvider) {
	otel.SetMeterProvider(mp)
	MetricsConfigured = true
}

// MeterProvider creates and returns an OTel MeterProvider based on the config.
// Returns (nil, nil, nil) if no metrics export is configured.
// The returned shutdown function should be called on application exit.
func (c MetricsConfig) MeterProvider() (*sdkmetric.MeterProvider, func(context.Context) error, error) {
	switch {
	case c.OTLPAddress != "":
		return c.otlpMeterProvider()
	case c.GCPProjectID != "":
		return c.gcpMeterProvider()
	default:
		return nil, nil, nil
	}
}

func (c MetricsConfig) otlpMeterProvider() (*sdkmetric.MeterProvider, func(context.Context) error, error) {
	opts := []otlpmetricgrpc.Option{
		otlpmetricgrpc.WithEndpoint(c.OTLPAddress),
		otlpmetricgrpc.WithHeaders(c.OTLPHeaders),
	}

	if c.OTLPUseTLS {
		opts = append(opts, otlpmetricgrpc.WithTLSCredentials(credentials.NewClientTLSFromCert(nil, "")))
	} else {
		opts = append(opts, otlpmetricgrpc.WithInsecure())
	}

	exporter, err := otlpmetricgrpc.New(context.Background(), opts...)
	if err != nil {
		return nil, nil, err
	}

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exporter)),
	)
	return mp, mp.Shutdown, nil
}

func (c MetricsConfig) gcpMeterProvider() (*sdkmetric.MeterProvider, func(context.Context) error, error) {
	// Use OTLP exporter pointed at GCP's endpoint as a portable fallback.
	// The google-cloud-go metric exporter requires additional setup;
	// for now we use the GCP OTLP ingestion endpoint which accepts standard OTLP.
	opts := []otlpmetricgrpc.Option{
		otlpmetricgrpc.WithEndpoint("monitoring.googleapis.com:443"),
		otlpmetricgrpc.WithHeaders(map[string]string{
			"x-goog-user-project": c.GCPProjectID,
		}),
		otlpmetricgrpc.WithTLSCredentials(credentials.NewClientTLSFromCert(nil, "")),
	}

	exporter, err := otlpmetricgrpc.New(context.Background(), opts...)
	if err != nil {
		return nil, nil, err
	}

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exporter)),
	)
	return mp, mp.Shutdown, nil
}
