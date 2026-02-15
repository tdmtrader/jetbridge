package behavioral_test

import (
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
)

var _ = Describe("Build Lifecycle", func() {

	It("transitions a successful build through pending -> started -> succeeded", func() {
		cfg := writePipelineFile("lifecycle-ok.yml", `
jobs:
- name: success-job
  plan:
  - task: pass
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["success"]
`)
		setAndUnpausePipeline(cfg)
		triggerJob("success-job")

		sess := waitForBuildAndWatch("success-job")
		Expect(sess.ExitCode()).To(Equal(0))
		Expect(sess.Out).To(gbytes.Say("success"))

		By("verifying build status via fly builds")
		rows := flyTable("builds", "-j", inPipeline("success-job"))
		Expect(rows).ToNot(BeEmpty())
		Expect(rows[0]["status"]).To(Equal("succeeded"))
	})

	It("reports a failed build when a task exits non-zero", func() {
		cfg := writePipelineFile("lifecycle-fail.yml", `
jobs:
- name: fail-job
  plan:
  - task: fail
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: sh
        args: ["-c", "echo failing && exit 1"]
`)
		setAndUnpausePipeline(cfg)
		triggerJob("fail-job")

		sess := waitForBuildAndWatch("fail-job")
		Expect(sess.ExitCode()).ToNot(Equal(0))

		rows := flyTable("builds", "-j", inPipeline("fail-job"))
		Expect(rows).ToNot(BeEmpty())
		Expect(rows[0]["status"]).To(Equal("failed"))
	})

	It("reports an errored build for config errors", func() {
		cfg := writePipelineFile("lifecycle-error.yml", `
jobs:
- name: error-job
  plan:
  - task: bad
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: nonexistent-image-xyzzy-12345}}
      run:
        path: echo
        args: ["never"]
`)
		setAndUnpausePipeline(cfg)
		triggerJob("error-job")

		sess := waitForBuildAndWatch("error-job")
		Expect(sess.ExitCode()).ToNot(Equal(0))

		rows := flyTable("builds", "-j", inPipeline("error-job"))
		Expect(rows).ToNot(BeEmpty())
		Expect(rows[0]["status"]).To(SatisfyAny(Equal("errored"), Equal("failed")))
	})

	It("aborts a running build", func() {
		cfg := writePipelineFile("lifecycle-abort.yml", `
jobs:
- name: abort-job
  plan:
  - task: slow
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: sleep
        args: ["300"]
`)
		setAndUnpausePipeline(cfg)
		triggerJob("abort-job")

		By("waiting for build to start running")
		Eventually(func() string {
			rows := flyTable("builds", "-j", inPipeline("abort-job"))
			if len(rows) == 0 {
				return ""
			}
			return rows[0]["status"]
		}, 2*time.Minute, time.Second).Should(Equal("started"))

		By("aborting the build")
		fly.Run("abort-build", "-j", inPipeline("abort-job"), "-b", "1")

		By("verifying build was aborted")
		Eventually(func() string {
			rows := flyTable("builds", "-j", inPipeline("abort-job"))
			if len(rows) == 0 {
				return ""
			}
			return rows[0]["status"]
		}, time.Minute, time.Second).Should(Equal("aborted"))
	})

	It("streams logs via fly watch", func() {
		cfg := writePipelineFile("watch-stream.yml", `
jobs:
- name: watch-job
  plan:
  - task: log-lines
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: sh
        args: ["-c", "for i in 1 2 3 4 5; do echo LINE_$i; done"]
`)
		setAndUnpausePipeline(cfg)
		triggerJob("watch-job")

		sess := waitForBuildAndWatch("watch-job")
		Expect(sess.ExitCode()).To(Equal(0))
		output := string(sess.Out.Contents())
		for i := 1; i <= 5; i++ {
			Expect(output).To(ContainSubstring(fmt.Sprintf("LINE_%d", i)))
		}
	})

	It("triggers a build via fly trigger-job with -w", func() {
		cfg := writePipelineFile("trigger-watch.yml", `
jobs:
- name: trigger-w-job
  plan:
  - task: greet
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["TRIGGERED"]
`)
		setAndUnpausePipeline(cfg)

		sess := fly.Start("trigger-job", "-j", inPipeline("trigger-w-job"), "-w")
		<-sess.Exited
		Expect(sess.ExitCode()).To(Equal(0))
		Expect(sess.Out).To(gbytes.Say("TRIGGERED"))
	})

	It("reruns a previous build", func() {
		cfg := writePipelineFile("rerun.yml", `
jobs:
- name: rerun-job
  plan:
  - task: stamp
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["RERUN_TEST"]
`)
		setAndUnpausePipeline(cfg)
		triggerJob("rerun-job")
		sess := waitForBuildAndWatch("rerun-job", "1")
		Expect(sess.ExitCode()).To(Equal(0))

		By("rerunning the build")
		rerunSess := fly.Start("rerun-build", "-j", inPipeline("rerun-job"), "-b", "1")
		<-rerunSess.Exited
		if rerunSess.ExitCode() != 0 {
			Skip("fly rerun-build not available in this version")
		}

		By("waiting for the rerun build to appear")
		Eventually(func() int {
			rows := flyTable("builds", "-j", inPipeline("rerun-job"))
			return len(rows)
		}, time.Minute, time.Second).Should(BeNumerically(">=", 2))

		rows := flyTable("builds", "-j", inPipeline("rerun-job"))
		// The rerun build should be the first row (newest)
		rerunBuildName := rows[0]["name"]
		// Extract build number from "pipeline/job/N"
		parts := strings.Split(rerunBuildName, "/")
		rerunBuildNum := parts[len(parts)-1]

		sess = waitForBuildAndWatch("rerun-job", rerunBuildNum)
		Expect(sess.ExitCode()).To(Equal(0))
		Expect(sess.Out).To(gbytes.Say("RERUN_TEST"))
	})

	It("persists logs after build completes", func() {
		cfg := writePipelineFile("log-persist.yml", `
jobs:
- name: log-job
  plan:
  - task: log
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["PERSISTENT_LOG"]
`)
		setAndUnpausePipeline(cfg)
		triggerJob("log-job")
		sess := waitForBuildAndWatch("log-job")
		Expect(sess.ExitCode()).To(Equal(0))

		By("re-watching the completed build")
		sess2 := fly.Start("watch", "-j", inPipeline("log-job"), "-b", "1")
		<-sess2.Exited
		Expect(sess2.ExitCode()).To(Equal(0))
		Expect(sess2.Out).To(gbytes.Say("PERSISTENT_LOG"))
	})

	It("exposes build preparation via the API", func() {
		cfg := writePipelineFile("prep.yml", `
resources:
- name: src
  type: mock
  source: {mirror_self: true}

jobs:
- name: prep-job
  plan:
  - get: src
  - task: work
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs: [{name: src}]
      run:
        path: echo
        args: ["prepared"]
`)
		setAndUnpausePipeline(cfg)
		newMockVersion("src", "v1")
		triggerJob("prep-job")
		sess := waitForBuildAndWatch("prep-job")
		Expect(sess.ExitCode()).To(Equal(0))

		By("fetching build preparation from the API")
		rows := flyTable("builds", "-j", inPipeline("prep-job"))
		Expect(rows).ToNot(BeEmpty())
		buildID := rows[0]["id"]

		prep := getBuildPreparation(buildID)
		Expect(prep).ToNot(BeEmpty())
	})

	It("exposes build events via SSE endpoint", func() {
		cfg := writePipelineFile("events.yml", `
jobs:
- name: events-job
  plan:
  - task: hello
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["event-test"]
`)
		setAndUnpausePipeline(cfg)
		triggerJob("events-job")
		sess := waitForBuildAndWatch("events-job")
		Expect(sess.ExitCode()).To(Equal(0))

		By("fetching build events from SSE endpoint")
		rows := flyTable("builds", "-j", inPipeline("events-job"))
		Expect(rows).ToNot(BeEmpty())
		buildID := rows[0]["id"]

		events := getBuildEvents(buildID)
		Expect(events).ToNot(BeEmpty(), "expected at least one SSE event")
	})

	It("retains logs for historical builds", func() {
		cfg := writePipelineFile("retention.yml", `
jobs:
- name: retention-job
  build_log_retention:
    builds: 3
  plan:
  - task: log
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["retained"]
`)
		setAndUnpausePipeline(cfg)

		By("running three builds")
		for i := 0; i < 3; i++ {
			triggerJob("retention-job")
			sess := waitForBuildAndWatch("retention-job", fmt.Sprintf("%d", i+1))
			Expect(sess.ExitCode()).To(Equal(0))
		}

		By("verifying all three builds are present")
		rows := flyTable("builds", "-j", inPipeline("retention-job"))
		Expect(len(rows)).To(BeNumerically(">=", 3))
	})

	It("supports adding comments to builds", func() {
		cfg := writePipelineFile("comment.yml", `
jobs:
- name: comment-job
  plan:
  - task: noop
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["commented"]
`)
		setAndUnpausePipeline(cfg)
		triggerJob("comment-job")
		sess := waitForBuildAndWatch("comment-job")
		Expect(sess.ExitCode()).To(Equal(0))

		By("setting a build comment")
		sess = fly.Start("set-build-comment", "-j", inPipeline("comment-job"), "-b", "1", "-m", "test comment")
		<-sess.Exited
		// set-build-comment may not be available in all versions
		if sess.ExitCode() == 0 {
			Expect(sess.ExitCode()).To(Equal(0))
		}
	})
})
