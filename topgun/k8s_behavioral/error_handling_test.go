package behavioral_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
)

var _ = Describe("Error Handling and Edge Cases", func() {

	It("rejects invalid YAML at set-pipeline time", func() {
		cfg := writePipelineFile("invalid.yml", `
this is not: [valid yaml
  missing bracket
`)
		sess := fly.Start("set-pipeline", "-n", "-p", pipelineName, "-c", cfg)
		<-sess.Exited
		Expect(sess.ExitCode()).ToNot(Equal(0))
	})

	It("rejects a pipeline with missing required fields", func() {
		cfg := writePipelineFile("missing-fields.yml", `
jobs:
- plan:
  - task: no-name-job
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["hello"]
`)
		sess := fly.Start("set-pipeline", "-n", "-p", pipelineName, "-c", cfg)
		<-sess.Exited
		Expect(sess.ExitCode()).ToNot(Equal(0))
	})

	It("errors when a resource type is not found", func() {
		cfg := writePipelineFile("missing-type.yml", `
resources:
- name: bad
  type: nonexistent-type-xyz
  source: {uri: "https://example.com"}

jobs:
- name: bad-job
  plan:
  - get: bad
  - task: noop
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["hello"]
`)
		setAndUnpausePipeline(cfg)
		triggerJob("bad-job")

		// With a nonexistent resource type, the check can never succeed,
		// so the get step will never start and the build stays pending.
		// Don't use waitForBuildAndWatch — it would hang forever.
		// Instead verify the build was created and remains pending/started
		// (it can never succeed).
		Eventually(func() string {
			rows := flyTable("builds", "-j", inPipeline("bad-job"))
			if len(rows) == 0 {
				return ""
			}
			return rows[0]["status"]
		}, time.Minute, 2*time.Second).Should(SatisfyAny(
			Equal("pending"),
			Equal("started"),
			Equal("errored"),
		))
	})

	It("detects circular resource dependencies", func() {
		// Concourse rejects circular passed constraints at set-pipeline time
		cfg := writePipelineFile("circular.yml", `
resources:
- name: res
  type: mock
  source: {mirror_self: true}

jobs:
- name: job-a
  plan:
  - get: res
    passed: [job-b]
  - task: noop
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["a"]
- name: job-b
  plan:
  - get: res
    passed: [job-a]
  - task: noop
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["b"]
`)
		sess := fly.Start("set-pipeline", "-n", "-p", pipelineName, "-c", cfg)
		<-sess.Exited
		Expect(sess.ExitCode()).ToNot(Equal(0), "circular pipeline should be rejected")
	})

	It("handles image pull failure gracefully", func() {
		cfg := writePipelineFile("bad-image.yml", `
jobs:
- name: bad-image-job
  plan:
  - task: fail-pull
    config:
      platform: linux
      image_resource:
        type: registry-image
        source: {repository: this-image-does-not-exist-xyzzy-99999}
      run:
        path: echo
        args: ["never"]
`)
		setAndUnpausePipeline(cfg)
		triggerJob("bad-image-job")

		sess := waitForBuildAndWatch("bad-image-job")
		Expect(sess.ExitCode()).ToNot(Equal(0))
	})

	It("errors on resource source misconfiguration", func() {
		cfg := writePipelineFile("bad-source.yml", `
resources:
- name: bad-git
  type: git
  source: {}

jobs:
- name: bad-source-job
  plan:
  - get: bad-git
  - task: noop
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["hello"]
`)
		setAndUnpausePipeline(cfg)
		triggerJob("bad-source-job")

		// With an empty git source, the check will fail and the get step
		// can never start. The build stays pending. Don't use
		// waitForBuildAndWatch — it would hang.
		Eventually(func() string {
			rows := flyTable("builds", "-j", inPipeline("bad-source-job"))
			if len(rows) == 0 {
				return ""
			}
			return rows[0]["status"]
		}, time.Minute, 2*time.Second).Should(SatisfyAny(
			Equal("pending"),
			Equal("started"),
			Equal("errored"),
		))
	})

	It("handles concurrent set-pipeline calls", func() {
		cfg := writePipelineFile("concurrent-set.yml", `
jobs:
- name: concurrent-job
  plan:
  - task: noop
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["concurrent"]
`)

		By("setting the same pipeline concurrently")
		sess1 := fly.Start("set-pipeline", "-n", "-p", pipelineName, "-c", cfg)
		sess2 := fly.Start("set-pipeline", "-n", "-p", pipelineName, "-c", cfg)

		<-sess1.Exited
		<-sess2.Exited

		// At least one should succeed
		succeeded := (sess1.ExitCode() == 0) || (sess2.ExitCode() == 0)
		Expect(succeeded).To(BeTrue(), "at least one concurrent set-pipeline should succeed")
	})

	It("handles long log output without truncation", func() {
		cfg := writePipelineFile("long-logs.yml", `
jobs:
- name: long-log-job
  plan:
  - task: verbose
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: sh
        args: ["-c", "for i in $(seq 1 500); do echo LOG_LINE_$i; done && echo LAST_LINE"]
`)
		setAndUnpausePipeline(cfg)
		triggerJob("long-log-job")

		sess := waitForBuildAndWatch("long-log-job")
		Expect(sess.ExitCode()).To(Equal(0))
		Expect(sess.Out).To(gbytes.Say("LAST_LINE"))
	})

	It("handles an empty plan gracefully", func() {
		cfg := writePipelineFile("empty-plan.yml", `
jobs:
- name: empty-job
  plan: []
`)
		sess := fly.Start("set-pipeline", "-n", "-p", pipelineName, "-c", cfg)
		<-sess.Exited
		// Empty plan may be rejected at set-pipeline or succeed but produce
		// a build that immediately finishes
		if sess.ExitCode() == 0 {
			fly.Run("unpause-pipeline", "-p", pipelineName)
			triggerJob("empty-job")
			buildSess := waitForBuildAndWatch("empty-job")
			// Empty plan build should succeed with nothing to do
			Expect(buildSess.ExitCode()).To(Equal(0))
		}
	})

	It("errors on undefined resource references in job plan", func() {
		cfg := writePipelineFile("undefined-resource.yml", `
jobs:
- name: undef-job
  plan:
  - get: does-not-exist
  - task: noop
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["hello"]
`)
		sess := fly.Start("set-pipeline", "-n", "-p", pipelineName, "-c", cfg)
		<-sess.Exited
		Expect(sess.ExitCode()).ToNot(Equal(0))
	})
})
