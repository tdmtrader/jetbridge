package testhelpers

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/concourse/concourse/atc/metric"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// StepDuration holds a single step duration observation extracted from OTel metrics.
type StepDuration struct {
	StepType string
	StepName string
	Duration float64 // seconds
}

// NewTestMeterProvider creates an OTel MeterProvider backed by a ManualReader
// and initializes all Concourse OTel metric instruments. Returns the reader
// for collecting metrics in tests.
func NewTestMeterProvider() *sdkmetric.ManualReader {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	otel.SetMeterProvider(mp)

	metric.InitOTelStepDuration()
	metric.InitOTelMetrics()
	metric.InitOTelBuildLifecycle()
	metric.InitOTelStepWaiting()
	metric.InitOTelScheduling()
	metric.InitOTelGC()
	metric.InitOTelDBChecks()

	return reader
}

// CollectStepDurations collects metrics from the reader and extracts
// concourse.step.duration data points, returning structured results.
func CollectStepDurations(reader *sdkmetric.ManualReader) ([]StepDuration, error) {
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		return nil, fmt.Errorf("collect metrics: %w", err)
	}

	var results []StepDuration
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "concourse.step.duration" {
				continue
			}
			hist, ok := m.Data.(metricdata.Histogram[float64])
			if !ok {
				continue
			}
			for _, dp := range hist.DataPoints {
				sd := StepDuration{Duration: dp.Sum}
				if v, ok := dp.Attributes.Value("step.type"); ok {
					sd.StepType = v.AsString()
				}
				if v, ok := dp.Attributes.Value("step.name"); ok {
					sd.StepName = v.AsString()
				}
				results = append(results, sd)
			}
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Duration > results[j].Duration
	})

	return results, nil
}

// PrintPerformanceReport formats step timing data as a table and writes it
// to the provided writer (typically GinkgoWriter or os.Stdout).
func PrintPerformanceReport(w io.Writer, durations []StepDuration) {
	if len(durations) == 0 {
		fmt.Fprintln(w, "No step duration metrics collected.")
		return
	}

	fmt.Fprintln(w, "")
	fmt.Fprintln(w, strings.Repeat("=", 60))
	fmt.Fprintln(w, "  Step Performance Report (OTel)")
	fmt.Fprintln(w, strings.Repeat("=", 60))
	fmt.Fprintf(w, "  %-10s %-30s %10s\n", "TYPE", "NAME", "DURATION")
	fmt.Fprintln(w, strings.Repeat("-", 60))

	var total float64
	for _, d := range durations {
		fmt.Fprintf(w, "  %-10s %-30s %9.3fs\n", d.StepType, d.StepName, d.Duration)
		total += d.Duration
	}

	fmt.Fprintln(w, strings.Repeat("-", 60))
	fmt.Fprintf(w, "  %-10s %-30s %9.3fs\n", "", "TOTAL", total)
	fmt.Fprintln(w, strings.Repeat("=", 60))
	fmt.Fprintln(w, "")
}
