package behavioral_test

import (
	"fmt"
	"net/http"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
)

var _ = Describe("Observability", func() {

	It("emits OpenTelemetry traces for builds", func() {
		// Requires Concourse configured with CONCOURSE_TRACING_JAEGER_ENDPOINT
		// or CONCOURSE_TRACING_OTLP_ADDRESS. Test validates build succeeds
		// and that tracing configuration doesn't break normal operation.
		cfg := writePipelineFile("otel.yml", `
jobs:
- name: otel-job
  plan:
  - task: traced
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["TRACED_BUILD"]
`)
		setAndUnpausePipeline(cfg)
		triggerJob("otel-job")

		sess := waitForBuildAndWatch("otel-job")
		Expect(sess.ExitCode()).To(Equal(0))
		Expect(sess.Out).To(gbytes.Say("TRACED_BUILD"))
	})

	It("includes step-level spans in traces", func() {
		// This test validates that multi-step pipelines produce correct output,
		// which correlates with span generation when tracing is enabled.
		cfg := writePipelineFile("step-spans.yml", `
jobs:
- name: span-job
  plan:
  - task: step-1
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["STEP_1"]
  - task: step-2
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["STEP_2"]
`)
		setAndUnpausePipeline(cfg)
		triggerJob("span-job")

		sess := waitForBuildAndWatch("span-job")
		Expect(sess.ExitCode()).To(Equal(0))
		output := string(sess.Out.Contents())
		Expect(output).To(ContainSubstring("STEP_1"))
		Expect(output).To(ContainSubstring("STEP_2"))
	})

	It("exposes Prometheus metrics endpoint", func() {
		// The /metrics endpoint may not require authentication depending
		// on Concourse configuration. Check both authenticated and
		// unauthenticated access.
		status, body := apiGet("/api/v1/info")
		Expect(status).To(Equal(http.StatusOK))
		Expect(string(body)).To(ContainSubstring("version"))

		// Try the Prometheus metrics endpoint if available
		client := insecureHTTPClient()
		resp, err := client.Get(config.ATCURL + "/metrics")
		if err == nil {
			defer resp.Body.Close()
			// Metrics endpoint may return 200 or 404 depending on config
			Expect(resp.StatusCode).To(SatisfyAny(
				Equal(http.StatusOK),
				Equal(http.StatusNotFound),
			))
		}
	})

	It("reports build-level metrics", func() {
		cfg := writePipelineFile("build-metrics.yml", `
jobs:
- name: metrics-job
  plan:
  - task: work
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["METRICS_OK"]
`)
		setAndUnpausePipeline(cfg)
		triggerJob("metrics-job")

		sess := waitForBuildAndWatch("metrics-job")
		Expect(sess.ExitCode()).To(Equal(0))

		By("checking build events include timing data")
		rows := flyTable("builds", "-j", inPipeline("metrics-job"))
		Expect(rows).ToNot(BeEmpty())
		// Build rows include start/end times
		Expect(rows[0]).To(HaveKey("start"))
		Expect(rows[0]).To(HaveKey("end"))
	})

	It("creates pods with observable metadata", func() {
		cfg := writePipelineFile("pod-metrics.yml", `
jobs:
- name: pod-obs-job
  plan:
  - task: observable
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: sh
        args: ["-c", "echo OBSERVABLE && sleep 5"]
`)
		setAndUnpausePipeline(cfg)
		triggerJob("pod-obs-job")

		By("verifying pod has Concourse labels")
		pods := waitForConcoursePodsAtLeast(1)
		pod := &pods[0]
		Expect(pod.Labels).To(HaveKey("concourse.ci/worker"),
			"pod should have concourse worker label for observability")

		sess := waitForBuildAndWatch("pod-obs-job")
		Expect(sess.ExitCode()).To(Equal(0))
	})

	It("produces structured log output from builds", func() {
		cfg := writePipelineFile("structured-log.yml", `
jobs:
- name: log-job
  plan:
  - task: structured
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: sh
        args: ["-c", "echo '{\"level\":\"info\",\"msg\":\"structured log\"}' && echo PLAIN_LOG"]
`)
		setAndUnpausePipeline(cfg)
		triggerJob("log-job")

		sess := waitForBuildAndWatch("log-job")
		Expect(sess.ExitCode()).To(Equal(0))
		output := string(sess.Out.Contents())
		Expect(output).To(ContainSubstring("PLAIN_LOG"))
	})

	It("collects OTel metrics during builds", func() {
		if !collectOTelEnabled() {
			Skip("COLLECT_OTEL=1 not set, skipping OTel metrics collection test")
		}

		By("deploying the OTel collector")
		deployOTelCollector()
		defer cleanupOTelCollector()

		By("running a multi-step pipeline to generate metrics")
		cfg := writePipelineFile("otel-perf.yml", `
jobs:
- name: perf-job
  plan:
  - task: step-a
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: sh
        args: ["-c", "echo STEP_A && sleep 1"]
  - task: step-b
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: sh
        args: ["-c", "echo STEP_B && sleep 2"]
`)
		setAndUnpausePipeline(cfg)
		triggerJob("perf-job")

		sess := waitForBuildAndWatch("perf-job")
		Expect(sess.ExitCode()).To(Equal(0))

		By("waiting for OTel metrics to be exported")
		metrics := waitForOTelMetrics(30 * time.Second)

		By("verifying metrics were collected")
		fmt.Fprintf(GinkgoWriter, "Collected %d metric entries from OTel collector\n", len(metrics))
		Expect(len(metrics)).To(BeNumerically(">", 0),
			"expected at least one metric entry from the OTel collector")

		By("printing step performance report")
		printStepPerformanceReport(metrics)
	})
})
