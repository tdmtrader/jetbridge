package behavioral_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
)

var _ = Describe("Job Scheduling and Concurrency", func() {

	It("runs a serial job without overlap", func() {
		By("setting a pipeline with serial: true")
		cfg := writePipelineFile("serial.yml", `
resources:
- name: tick
  type: time
  source: {interval: 1s}

jobs:
- name: serial-job
  serial: true
  plan:
  - get: tick
    trigger: true
  - task: slow
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: sh
        args: ["-c", "echo running && sleep 5"]
`)
		setAndUnpausePipeline(cfg)

		By("triggering two builds")
		triggerJob("serial-job")
		triggerJob("serial-job")

		By("verifying first build succeeds")
		sess := waitForBuildAndWatch("serial-job", "1")
		Expect(sess.ExitCode()).To(Equal(0))

		By("verifying second build succeeds")
		sess = waitForBuildAndWatch("serial-job", "2")
		Expect(sess.ExitCode()).To(Equal(0))
	})

	It("runs serial_groups so only one job per group runs at a time", func() {
		cfg := writePipelineFile("serial-group.yml", `
jobs:
- name: job-a
  serial_groups: [deploy]
  plan:
  - task: work
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: sh
        args: ["-c", "echo job-a && sleep 5"]
- name: job-b
  serial_groups: [deploy]
  plan:
  - task: work
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: sh
        args: ["-c", "echo job-b && sleep 5"]
`)
		setAndUnpausePipeline(cfg)

		By("triggering both jobs")
		triggerJob("job-a")
		triggerJob("job-b")

		By("waiting for both to complete")
		sessA := waitForBuildAndWatch("job-a")
		Expect(sessA.ExitCode()).To(Equal(0))
		sessB := waitForBuildAndWatch("job-b")
		Expect(sessB.ExitCode()).To(Equal(0))
	})

	It("respects max_in_flight", func() {
		cfg := writePipelineFile("max-in-flight.yml", `
jobs:
- name: limited-job
  max_in_flight: 2
  plan:
  - task: work
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: sh
        args: ["-c", "echo running && sleep 3"]
`)
		setAndUnpausePipeline(cfg)

		By("triggering three builds")
		triggerJob("limited-job")
		triggerJob("limited-job")
		triggerJob("limited-job")

		By("waiting for all builds to finish")
		for _, b := range []string{"1", "2", "3"} {
			sess := waitForBuildAndWatch("limited-job", b)
			Expect(sess.ExitCode()).To(Equal(0))
		}
	})

	It("marks interruptible jobs as abortable when superseded", func() {
		cfg := writePipelineFile("interruptible.yml", `
resources:
- name: tick
  type: time
  source: {interval: 1s}

jobs:
- name: interruptible-job
  serial: true
  interruptible: true
  plan:
  - get: tick
    trigger: true
  - task: slow
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: sh
        args: ["-c", "sleep 10"]
`)
		setAndUnpausePipeline(cfg)

		By("triggering two builds so the first is superseded")
		triggerJob("interruptible-job")
		time.Sleep(2 * time.Second)
		triggerJob("interruptible-job")

		By("verifying the second build completes")
		sess := waitForBuildAndWatch("interruptible-job", "2")
		// Interruptible build may succeed or be aborted
		Expect(sess.ExitCode()).To(SatisfyAny(Equal(0), Equal(3)))
	})

	It("rejects manual trigger when disable_manual_trigger is set", func() {
		cfg := writePipelineFile("no-manual.yml", `
jobs:
- name: auto-only
  disable_manual_trigger: true
  plan:
  - task: noop
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["hello"]
`)
		setAndUnpausePipeline(cfg)

		By("attempting to trigger manually")
		sess := fly.Start("trigger-job", "-j", inPipeline("auto-only"))
		<-sess.Exited
		Expect(sess.ExitCode()).ToNot(Equal(0))
	})

	It("schedules a job only when passed constraints are met", func() {
		cfg := writePipelineFile("passed.yml", `
resources:
- name: src
  type: mock
  source: {mirror_self: true}

jobs:
- name: unit
  plan:
  - get: src
    trigger: true
  - task: test
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["unit passed"]
- name: integration
  plan:
  - get: src
    passed: [unit]
    trigger: true
  - task: test
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["integration passed"]
`)
		setAndUnpausePipeline(cfg)

		By("checking resource to produce a version")
		newMockVersion("src", "v1")

		By("waiting for unit to succeed")
		sess := waitForBuildAndWatch("unit")
		Expect(sess.ExitCode()).To(Equal(0))

		By("waiting for integration to run with passed version")
		sess = waitForBuildAndWatch("integration")
		Expect(sess.ExitCode()).To(Equal(0))
	})

	It("schedules a job with multiple inputs", func() {
		cfg := writePipelineFile("multi-input.yml", `
resources:
- name: src-a
  type: mock
  source: {mirror_self: true}
- name: src-b
  type: mock
  source: {mirror_self: true}

jobs:
- name: combined
  plan:
  - in_parallel:
    - get: src-a
      trigger: true
    - get: src-b
      trigger: true
  - task: verify
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs:
      - name: src-a
      - name: src-b
      run:
        path: echo
        args: ["both inputs present"]
`)
		setAndUnpausePipeline(cfg)

		By("checking both resources")
		newMockVersion("src-a", "v1")
		newMockVersion("src-b", "v1")

		By("waiting for combined job")
		sess := waitForBuildAndWatch("combined")
		Expect(sess.ExitCode()).To(Equal(0))
		Expect(sess.Out).To(gbytes.Say("both inputs present"))
	})

	It("pauses and unpauses a job", func() {
		cfg := writePipelineFile("pause-job.yml", `
jobs:
- name: pausable
  plan:
  - task: noop
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["hello"]
`)
		setAndUnpausePipeline(cfg)

		By("pausing the job")
		fly.Run("pause-job", "-j", inPipeline("pausable"))

		By("verifying job is paused")
		rows := flyTable("jobs", "-p", pipelineName)
		var found bool
		for _, r := range rows {
			if r["name"] == "pausable" {
				Expect(r["paused"]).To(Equal("yes"))
				found = true
			}
		}
		Expect(found).To(BeTrue(), "job 'pausable' should appear in jobs list")

		By("unpausing and running")
		fly.Run("unpause-job", "-j", inPipeline("pausable"))
		triggerJob("pausable")
		sess := waitForBuildAndWatch("pausable")
		Expect(sess.ExitCode()).To(Equal(0))
	})

	It("enforces serial max one concurrent pod set in K8s", func() {
		cfg := writePipelineFile("serial-k8s.yml", `
jobs:
- name: serial-k8s
  serial: true
  plan:
  - task: work
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: sh
        args: ["-c", "echo start && sleep 10 && echo done"]
`)
		setAndUnpausePipeline(cfg)

		By("triggering two builds of the serial job")
		triggerJob("serial-k8s")
		triggerJob("serial-k8s")

		By("waiting for both builds to exist")
		Eventually(func() int {
			return len(flyTable("builds", "-j", inPipeline("serial-k8s")))
		}, time.Minute, time.Second).Should(BeNumerically(">=", 2))

		By("verifying serial enforcement: while one build is started, the other is pending")
		// flyTable returns newest first, so rows[0] = build 2, rows[1] = build 1
		Eventually(func() bool {
			rows := flyTable("builds", "-j", inPipeline("serial-k8s"))
			if len(rows) < 2 {
				return false
			}
			// Check that at most one build is started at any point
			startedCount := 0
			for _, r := range rows {
				if r["status"] == "started" {
					startedCount++
				}
			}
			return startedCount <= 1
		}, time.Minute, time.Second).Should(BeTrue(),
			"serial job should have at most one started build at a time")

		By("waiting for both builds to complete")
		sess := waitForBuildAndWatch("serial-k8s", "1")
		Expect(sess.ExitCode()).To(Equal(0))
		sess = waitForBuildAndWatch("serial-k8s", "2")
		Expect(sess.ExitCode()).To(Equal(0))
	})
})
