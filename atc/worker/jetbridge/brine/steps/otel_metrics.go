package steps

import (
	"context"
	"fmt"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/metric"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

type OTelMetricObservation struct{ Value string }

type metricExpectation struct {
	name  string
	kind  string
	value float64
	attrs map[string]string
}

type metricComparison int

const (
	metricAtLeast metricComparison = iota
	metricExact
	metricPresence
)

func OTelMetricDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[brine.Empty, OTelMetricObservation](
			"production OpenTelemetry records profile {string}",
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder) (OTelMetricObservation, error) {
				profile, _ := p.GetString(0)
				if err := recordAndValidateOTel(profile); err != nil {
					return OTelMetricObservation{}, err
				}
				return OTelMetricObservation{Value: "recorded=true"}, nil
			},
		),
		CheckString[OTelMetricObservation]("the OpenTelemetry result is {string}", "OpenTelemetry result", func(in OTelMetricObservation) (string, error) {
			return in.Value, nil
		}),
	}
}

func recordAndValidateOTel(profile string) error {
	reader := sdkmetric.NewManualReader()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	ctx := context.Background()
	expected := []metricExpectation{}

	switch profile {
	case "core-build-duration":
		metric.InitOTelMetrics()
		metric.RecordBuildDuration(ctx, 30*time.Second, "my-team", "my-pipeline", "my-job", "succeeded")
		expected = append(expected, metricExpectation{"concourse.build.duration", "hist", 30, map[string]string{"build.team": "my-team", "build.status": "succeeded"}})
	case "core-http-duration":
		metric.InitOTelMetrics()
		metric.RecordHTTPResponseTime(ctx, 250*time.Millisecond, "GET", "/api/v1/info", 200)
		expected = append(expected, metricExpectation{"concourse.http.response_time", "hist", .25, map[string]string{"http.method": "GET"}})
	case "core-pod-startup":
		metric.InitOTelMetrics()
		metric.RecordK8sPodStartupDuration(ctx, 5*time.Second)
		expected = append(expected, metricExpectation{"concourse.k8s.pod_startup_duration", "hist", 5, nil})
	case "core-containers-created":
		metric.InitOTelMetrics()
		metric.RecordContainersCreated(ctx, 3)
		expected = append(expected, metricExpectation{"concourse.containers.created", "sum", 3, nil})
	case "core-volumes-created":
		metric.InitOTelMetrics()
		metric.RecordVolumesCreated(ctx, 5)
		expected = append(expected, metricExpectation{"concourse.volumes.created", "sum", 5, nil})
	case "core-volume-operation":
		metric.InitOTelMetrics()
		metric.RecordVolumeOperationDuration(ctx, 2*time.Second, "stream_in")
		expected = append(expected, metricExpectation{"concourse.k8s.volume_operation_duration", "hist", 2, map[string]string{"op": "stream_in"}})
	case "core-volume-operations":
		metric.InitOTelMetrics()
		metric.RecordVolumeOperationDuration(ctx, time.Second, "stream_in")
		metric.RecordVolumeOperationDuration(ctx, time.Second, "initialize")
		expected = append(expected,
			metricExpectation{"concourse.k8s.volume_operation_duration", "hist", 1, map[string]string{"op": "stream_in"}},
			metricExpectation{"concourse.k8s.volume_operation_duration", "hist", 1, map[string]string{"op": "initialize"}})
	case "artifact-duration", "artifact-size", "artifact-files", "artifact-phases", "artifact-attributes":
		metric.InitOTelArtifactUpload()
		metric.RecordArtifactUpload(ctx, map[bool]string{true: "cache", false: "output"}[profile == "artifact-attributes"], 3*time.Second, 2097152, 250, time.Second, 2*time.Second)
		switch profile {
		case "artifact-duration":
			expected = append(expected, metricExpectation{"concourse.artifact.upload_duration", "hist", 3, nil})
		case "artifact-size":
			expected = append(expected, metricExpectation{"concourse.artifact.upload_size", "hist", 2097152, nil})
		case "artifact-files":
			expected = append(expected, metricExpectation{"concourse.artifact.file_count", "hist", 250, nil})
		case "artifact-phases":
			expected = append(expected, metricExpectation{"concourse.artifact.tar_duration", "hist", 1, nil}, metricExpectation{"concourse.artifact.transfer_duration", "hist", 2, nil})
		case "artifact-attributes":
			expected = append(expected, metricExpectation{"concourse.artifact.upload_duration", "hist", 3, map[string]string{"artifact.type": "cache"}})
		}
	case "lifecycle-builds-started":
		metric.InitOTelBuildLifecycle()
		metric.RecordBuildsStarted(ctx, 5)
		expected = append(expected, metricExpectation{"concourse.builds.started", "sum", 5, nil})
	case "lifecycle-builds-running":
		metric.InitOTelBuildLifecycle()
		metric.RecordBuildsRunning(ctx, 3)
		expected = append(expected, metricExpectation{"concourse.builds.running", "sum", 3, nil})
	case "lifecycle-build-finished":
		metric.InitOTelBuildLifecycle()
		metric.RecordBuildFinished(ctx, "succeeded")
		expected = append(expected, metricExpectation{"concourse.builds.finished", "sum", 1, map[string]string{"build.status": "succeeded"}})
	case "lifecycle-checks-started":
		metric.InitOTelBuildLifecycle()
		metric.RecordCheckBuildsStarted(ctx, 2)
		expected = append(expected, metricExpectation{"concourse.check_builds.started", "sum", 2, nil})
	case "lifecycle-checks-running":
		metric.InitOTelBuildLifecycle()
		metric.RecordCheckBuildsRunning(ctx, 7)
		expected = append(expected, metricExpectation{"concourse.check_builds.running", "sum", 7, nil})
	case "db-queries":
		metric.InitOTelDBChecks()
		metric.RecordDBQueries(ctx, 42)
		expected = append(expected, metricExpectation{"concourse.db.queries", "sum", 42, nil})
	case "db-connections":
		metric.InitOTelDBChecks()
		metric.RecordDBConnections(ctx, 5, "api")
		expected = append(expected, metricExpectation{"concourse.db.connections", "sum", 5, map[string]string{"db.name": "api"}})
	case "checks-started":
		metric.InitOTelDBChecks()
		metric.RecordChecksStarted(ctx, 10)
		expected = append(expected, metricExpectation{"concourse.checks.started", "sum", 10, nil})
	case "checks-finished":
		metric.InitOTelDBChecks()
		metric.RecordChecksFinished(ctx, 7, "error")
		expected = append(expected, metricExpectation{"concourse.checks.finished", "sum", 7, map[string]string{"status": "error"}})
	case "checks-enqueued":
		metric.InitOTelDBChecks()
		metric.RecordChecksEnqueued(ctx, 3)
		expected = append(expected, metricExpectation{"concourse.checks.enqueued", "sum", 3, nil})
	case "scheduling-scheduled":
		metric.InitOTelScheduling()
		metric.RecordJobsScheduled(ctx, 10)
		expected = append(expected, metricExpectation{"concourse.jobs.scheduled", "sum", 10, nil})
	case "scheduling-running":
		metric.InitOTelScheduling()
		metric.RecordJobsScheduling(ctx, 3)
		expected = append(expected, metricExpectation{"concourse.jobs.scheduling", "sum", 3, nil})
	case "scheduling-duration":
		metric.InitOTelScheduling()
		metric.RecordSchedulingJobDuration(ctx, 1.5, "my-pipeline", "my-job")
		expected = append(expected, metricExpectation{"concourse.jobs.scheduling_duration", "hist", 1.5, map[string]string{"pipeline": "my-pipeline", "job": "my-job"}})
	case "step-duration":
		metric.InitOTelStepDuration()
		metric.RecordStepDuration(ctx, "task", "my-task", 2*time.Second)
		expected = append(expected, metricExpectation{"concourse.step.duration", "hist", 2, nil})
	case "step-duration-attributes":
		metric.InitOTelStepDuration()
		metric.RecordStepDuration(ctx, "get", "my-resource", 500*time.Millisecond)
		expected = append(expected, metricExpectation{"concourse.step.duration", "hist", .5, map[string]string{"step.type": "get", "step.name": "my-resource"}})
	case "waiting-count":
		metric.InitOTelStepWaiting()
		metric.RecordStepsWaiting(ctx, 4, "main", "task")
		expected = append(expected, metricExpectation{"concourse.steps.waiting", "sum", 4, map[string]string{"team.name": "main"}})
	case "waiting-duration":
		metric.InitOTelStepWaiting()
		metric.RecordStepsWaitDuration(ctx, 2.5, "main", "get")
		expected = append(expected, metricExpectation{"concourse.steps.wait_duration", "hist", 2.5, map[string]string{"step.type": "get"}})
	case "gc-duration":
		metric.InitOTelGC()
		metric.RecordGCCollectorDuration(ctx, "build", 123.45)
		expected = append(expected, metricExpectation{"concourse.gc.collector_duration", "hist", 123, nil})
	case "gc-attributes":
		metric.InitOTelGC()
		metric.RecordGCCollectorDuration(ctx, "container", 50)
		expected = append(expected, metricExpectation{"concourse.gc.collector_duration", "hist", 50, map[string]string{"collector.name": "container"}})
	default:
		return fmt.Errorf("unknown OpenTelemetry profile %q", profile)
	}

	var exported metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &exported); err != nil {
		return err
	}
	for _, want := range expected {
		comparison := otelMetricComparison(profile)
		if !otelMetricMatches(exported, want, comparison) {
			return fmt.Errorf("metric %s did not export kind=%s comparison=%d value=%g attrs=%v", want.name, want.kind, comparison, want.value, want.attrs)
		}
	}
	return nil
}

func otelMetricComparison(profile string) metricComparison {
	switch profile {
	case "lifecycle-builds-started", "lifecycle-builds-running", "lifecycle-build-finished",
		"lifecycle-checks-started", "lifecycle-checks-running", "scheduling-scheduled",
		"scheduling-running", "waiting-count":
		return metricExact
	case "core-volume-operations", "artifact-attributes", "db-connections", "checks-finished", "gc-attributes":
		return metricPresence
	default:
		return metricAtLeast
	}
}

func otelMetricMatches(exported metricdata.ResourceMetrics, want metricExpectation, comparison metricComparison) bool {
	for _, scope := range exported.ScopeMetrics {
		for _, got := range scope.Metrics {
			if got.Name != want.name {
				continue
			}
			switch data := got.Data.(type) {
			case metricdata.Sum[float64]:
				if want.kind != "sum" {
					continue
				}
				for _, point := range data.DataPoints {
					if metricValueMatches(point.Value, want.value, comparison) && otelAttrsMatch(point.Attributes, want.attrs) {
						return true
					}
				}
			case metricdata.Histogram[float64]:
				if want.kind != "hist" {
					continue
				}
				for _, point := range data.DataPoints {
					if metricValueMatches(point.Sum, want.value, comparison) && otelAttrsMatch(point.Attributes, want.attrs) {
						return true
					}
				}
			}
		}
	}
	return false
}

func metricValueMatches(got, want float64, comparison metricComparison) bool {
	switch comparison {
	case metricExact:
		return got == want
	case metricPresence:
		return true
	default:
		return got >= want
	}
}

func otelAttrsMatch(set attribute.Set, want map[string]string) bool {
	for key, expected := range want {
		value, ok := set.Value(attribute.Key(key))
		if !ok || value.AsString() != expected {
			return false
		}
	}
	return true
}
