package integration_test

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
)

var _ = Describe("Error Handling", func() {
	It("runs on_error hook when a step errors (not just fails)", func() {
		// on_error triggers on "errored" state (infrastructure error),
		// which is different from "failed" (non-zero exit). A timeout
		// that expires produces an error, not a failure.
		pipelineFile := writePipelineFile("on-error-hook.yml", `
jobs:
- name: on-error-job
  plan:
  - task: erroring-task
    timeout: 10s
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      run:
        path: sh
        args: ["-c", "echo error-task-started && sleep 120"]
    on_error:
      task: error-hook
      config:
        platform: linux
        rootfs_uri: docker:///busybox
        run:
          path: echo
          args: ["on-error-hook-ran"]
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("on-error-job")

		session := waitForBuildAndWatch("on-error-job")
		Expect(session.ExitCode()).ToNot(Equal(0))
		output := string(session.Out.Contents())
		Expect(output).To(ContainSubstring("error-task-started"))
		// on_error should fire since timeout produces an error state
		Expect(output).To(ContainSubstring("on-error-hook-ran"))
	})

	It("reports errored build status for invalid image", func() {
		pipelineFile := writePipelineFile("bad-image.yml", `
jobs:
- name: bad-image-job
  plan:
  - task: bad-task
    config:
      platform: linux
      rootfs_uri: docker:///nonexistent-image-that-does-not-exist-12345:latest
      run:
        path: echo
        args: ["should-not-run"]
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("bad-image-job")

		session := waitForBuildAndWatch("bad-image-job")
		Expect(session.ExitCode()).ToNot(Equal(0))

		By("verifying the build errored or failed")
		builds := flyTable("builds", "-j", inPipeline("bad-image-job"))
		Expect(builds).ToNot(BeEmpty())
		Expect(builds[0]["status"]).To(SatisfyAny(Equal("errored"), Equal("failed")))
	})

	It("handles set_pipeline with invalid YAML gracefully", func() {
		childPipeline := pipelineName + "-invalid"

		pipelineFile := writePipelineFile("set-pipeline-invalid.yml", fmt.Sprintf(`
jobs:
- name: invalid-pipeline-job
  plan:
  - task: generate-bad-yaml
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      outputs:
      - name: pipeline-config
      run:
        path: sh
        args:
        - -c
        - |
          cat > pipeline-config/pipeline.yml <<'PIPEEOF'
          this is not: valid: yaml: [
          jobs:
          - invalid
          PIPEEOF
          echo "bad-yaml-generated"
  - set_pipeline: %s
    file: pipeline-config/pipeline.yml
`, childPipeline))

		setAndUnpausePipeline(pipelineFile)
		triggerJob("invalid-pipeline-job")

		session := waitForBuildAndWatch("invalid-pipeline-job")
		Expect(session.ExitCode()).ToNot(Equal(0))

		By("verifying the build failed")
		builds := flyTable("builds", "-j", inPipeline("invalid-pipeline-job"))
		Expect(builds).ToNot(BeEmpty())
		Expect(builds[0]["status"]).To(SatisfyAny(Equal("failed"), Equal("errored")))

		By("cleaning up child pipeline if it was partially created")
		sess := fly.Start("destroy-pipeline", "-n", "-p", childPipeline)
		<-sess.Exited
	})

	It("fails build when task references missing input", func() {
		pipelineFile := writePipelineFile("missing-input.yml", `
jobs:
- name: missing-input-job
  plan:
  - task: needs-input
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      inputs:
      - name: does-not-exist
      run:
        path: echo
        args: ["should-not-run"]
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("missing-input-job")

		session := waitForBuildAndWatch("missing-input-job")
		Expect(session.ExitCode()).ToNot(Equal(0))

		builds := flyTable("builds", "-j", inPipeline("missing-input-job"))
		Expect(builds).ToNot(BeEmpty())
		Expect(builds[0]["status"]).To(SatisfyAny(Equal("errored"), Equal("failed")))
	})

	It("handles task that exits with various non-zero codes", func() {
		for _, code := range []int{1, 2, 42, 127, 255} {
			func(exitCode int) {
				pipelineFile := writePipelineFile(fmt.Sprintf("exit-%d.yml", exitCode), fmt.Sprintf(`
jobs:
- name: exit-%d-job
  plan:
  - task: exit-task
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      run:
        path: sh
        args: ["-c", "exit %d"]
`, exitCode, exitCode))
				setAndUnpausePipeline(pipelineFile)
				triggerJob(fmt.Sprintf("exit-%d-job", exitCode))

				session := waitForBuildAndWatch(fmt.Sprintf("exit-%d-job", exitCode))
				Expect(session.ExitCode()).ToNot(Equal(0))

				builds := flyTable("builds", "-j", inPipeline(fmt.Sprintf("exit-%d-job", exitCode)))
				Expect(builds).ToNot(BeEmpty())
				Expect(builds[0]["status"]).To(Equal("failed"))
			}(code)
		}
	})

	It("handles task that produces large stderr output", func() {
		pipelineFile := writePipelineFile("large-stderr.yml", `
jobs:
- name: stderr-job
  plan:
  - task: noisy-task
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      run:
        path: sh
        args:
        - -c
        - |
          for i in $(seq 1 100); do
            echo "stderr-line-$i" >&2
          done
          echo "stderr-task-done"
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("stderr-job")

		session := waitForBuildAndWatch("stderr-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("stderr-task-done"))
	})

	It("handles task with signal handling (SIGTERM on abort)", func() {
		pipelineFile := writePipelineFile("signal-handling.yml", `
jobs:
- name: signal-job
  plan:
  - task: trapping-task
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      run:
        path: sh
        args:
        - -c
        - |
          trap 'echo signal-caught; exit 0' TERM
          echo "signal-task-started"
          sleep 3600 &
          wait
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("signal-job")

		By("waiting for the task to start")
		Eventually(func() string {
			builds := flyTable("builds", "-j", inPipeline("signal-job"))
			if len(builds) == 0 {
				return ""
			}
			return builds[0]["status"]
		}, 2*time.Minute, 2*time.Second).Should(Equal("started"))

		By("aborting the build to send SIGTERM")
		fly.Run("abort-build", "-j", inPipeline("signal-job"), "-b", "1")

		Eventually(func() string {
			builds := flyTable("builds", "-j", inPipeline("signal-job"))
			if len(builds) == 0 {
				return ""
			}
			return builds[0]["status"]
		}, 1*time.Minute, 2*time.Second).Should(Equal("aborted"))
	})

	It("cleans up pods after errored builds", func() {
		pipelineFile := writePipelineFile("error-cleanup.yml", `
jobs:
- name: error-cleanup-job
  plan:
  - task: timeout-task
    timeout: 10s
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      run:
        path: sh
        args: ["-c", "echo error-cleanup-started && sleep 120"]
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("error-cleanup-job")

		session := waitForBuildAndWatch("error-cleanup-job")
		Expect(session.ExitCode()).ToNot(Equal(0))

		By("verifying pods are cleaned up after errored build")
		waitForPodCleanupByPipeline()
	})

	It("recovers from a task that exhausts disk in output", func() {
		pipelineFile := writePipelineFile("disk-pressure.yml", `
jobs:
- name: disk-job
  plan:
  - task: disk-hungry
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      outputs:
      - name: big-output
      run:
        path: sh
        args:
        - -c
        - |
          # Write a moderately large file (don't actually exhaust disk)
          dd if=/dev/zero of=big-output/large.bin bs=1M count=50 2>/dev/null
          echo "disk-task-done"
  - task: verify-after-disk
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      inputs:
      - name: big-output
      run:
        path: sh
        args:
        - -c
        - |
          size=$(wc -c < big-output/large.bin)
          echo "file-size=${size}"
          echo "disk-recovery-done"
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("disk-job")

		session := waitForBuildAndWatch("disk-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("disk-recovery-done"))
	})

	It("handles concurrent hooks executing at same time", func() {
		pipelineFile := writePipelineFile("concurrent-hooks.yml", `
jobs:
- name: concurrent-hooks-job
  on_failure:
    task: job-failure-hook
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      run:
        path: echo
        args: ["concurrent-job-failure-ran"]
  ensure:
    task: job-ensure-hook
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      run:
        path: echo
        args: ["concurrent-job-ensure-ran"]
  plan:
  - in_parallel:
    - task: par-ok
      config:
        platform: linux
        rootfs_uri: docker:///busybox
        run:
          path: echo
          args: ["par-ok-done"]
    - task: par-fail
      config:
        platform: linux
        rootfs_uri: docker:///busybox
        run:
          path: sh
          args: ["-c", "echo par-fail-done && exit 1"]
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("concurrent-hooks-job")

		session := waitForBuildAndWatch("concurrent-hooks-job")
		Expect(session.ExitCode()).ToNot(Equal(0))
		output := string(session.Out.Contents())
		Expect(output).To(ContainSubstring("concurrent-job-ensure-ran"))
	})
})
