package behavioral_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
)

var _ = Describe("End to End Pipeline Scenarios", func() {

	It("runs a simple CI pipeline: get -> test -> report", func() {
		cfg := writePipelineFile("simple-ci.yml", `
resources:
- name: src
  type: mock
  source: {mirror_self: true}

jobs:
- name: test
  plan:
  - get: src
    trigger: true
  - task: run-tests
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs: [{name: src}]
      run:
        path: sh
        args: ["-c", "echo TESTS_PASSED"]
`)
		setAndUnpausePipeline(cfg)
		newMockVersion("src", "v1")
		triggerJob("test")

		sess := waitForBuildAndWatch("test")
		Expect(sess.ExitCode()).To(Equal(0))
		Expect(sess.Out).To(gbytes.Say("TESTS_PASSED"))

		By("verifying pod cleanup after E2E")
		assertPodCleanupForPipeline()
	})

	It("runs a build and push pipeline", func() {
		cfg := writePipelineFile("build-push.yml", `
resources:
- name: src
  type: mock
  source: {mirror_self: true}
- name: image
  type: mock
  source: {mirror_self: true}

jobs:
- name: build-push
  plan:
  - get: src
    trigger: true
  - task: build
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs: [{name: src}]
      outputs: [{name: built}]
      run:
        path: sh
        args: ["-c", "echo IMAGE_BUILT > built/image.tar"]
  - put: image
    params: {file: built/image.tar}
`)
		setAndUnpausePipeline(cfg)
		newMockVersion("src", "v1")
		triggerJob("build-push")

		sess := waitForBuildAndWatch("build-push")
		Expect(sess.ExitCode()).To(Equal(0))
		assertPodCleanupForPipeline()
	})

	It("runs a fan-out/fan-in pipeline", func() {
		cfg := writePipelineFile("fan-out-in.yml", `
resources:
- name: src
  type: mock
  source: {mirror_self: true}

jobs:
- name: unit
  plan:
  - get: src
    trigger: true
  - task: unit-tests
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["UNIT_OK"]

- name: lint
  plan:
  - get: src
    trigger: true
  - task: lint-check
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["LINT_OK"]

- name: integration
  plan:
  - get: src
    passed: [unit, lint]
    trigger: true
  - task: integration-tests
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["INTEGRATION_OK"]
`)
		setAndUnpausePipeline(cfg)
		newMockVersion("src", "v1")

		By("waiting for fan-out jobs")
		triggerJob("unit")
		triggerJob("lint")
		sessUnit := waitForBuildAndWatch("unit")
		Expect(sessUnit.ExitCode()).To(Equal(0))
		sessLint := waitForBuildAndWatch("lint")
		Expect(sessLint.ExitCode()).To(Equal(0))

		By("waiting for fan-in job")
		sessInteg := waitForBuildAndWatch("integration")
		Expect(sessInteg.ExitCode()).To(Equal(0))
		Expect(sessInteg.Out).To(gbytes.Say("INTEGRATION_OK"))
	})

	It("runs a multi-stage pipeline with passed constraints", func() {
		cfg := writePipelineFile("multi-stage.yml", `
resources:
- name: src
  type: mock
  source: {mirror_self: true}

jobs:
- name: build
  plan:
  - get: src
    trigger: true
  - task: compile
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["COMPILED"]

- name: test
  plan:
  - get: src
    passed: [build]
    trigger: true
  - task: test
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["TESTED"]

- name: deploy
  plan:
  - get: src
    passed: [test]
    trigger: true
  - task: deploy
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["DEPLOYED"]
`)
		setAndUnpausePipeline(cfg)
		newMockVersion("src", "v1")
		triggerJob("build")

		sess := waitForBuildAndWatch("build")
		Expect(sess.ExitCode()).To(Equal(0))
		sess = waitForBuildAndWatch("test")
		Expect(sess.ExitCode()).To(Equal(0))
		sess = waitForBuildAndWatch("deploy")
		Expect(sess.ExitCode()).To(Equal(0))
		Expect(sess.Out).To(gbytes.Say("DEPLOYED"))
	})

	It("runs a self-updating pipeline via set_pipeline", func() {
		cfg := writePipelineFile("self-update.yml", `
jobs:
- name: reconfigure
  plan:
  - task: generate-config
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      outputs: [{name: pipeline}]
      run:
        path: sh
        args:
        - "-c"
        - |
          printf '%s\n' 'jobs:' '- name: reconfigure' '  plan:' '  - task: updated' '    config:' '      platform: linux' '      image_resource: {type: registry-image, source: {repository: busybox}}' '      run:' '        path: echo' '        args: ["SELF_UPDATED"]' > pipeline/pipeline.yml
  - set_pipeline: self
    file: pipeline/pipeline.yml
`)
		setAndUnpausePipeline(cfg)
		triggerJob("reconfigure")

		sess := waitForBuildAndWatch("reconfigure")
		Expect(sess.ExitCode()).To(Equal(0))
	})

	It("runs a dynamically generated pipeline", func() {
		cfg := writePipelineFile("dynamic.yml", `
jobs:
- name: generate
  plan:
  - task: gen-pipeline
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      outputs: [{name: generated}]
      run:
        path: sh
        args:
        - "-c"
        - |
          printf '%s\n' 'jobs:' '- name: child-job' '  plan:' '  - task: hello' '    config:' '      platform: linux' '      image_resource: {type: registry-image, source: {repository: busybox}}' '      run:' '        path: echo' '        args: ["DYNAMIC_CHILD"]' > generated/child.yml
  - set_pipeline: dynamic-child
    file: generated/child.yml
`)
		setAndUnpausePipeline(cfg)
		triggerJob("generate")

		sess := waitForBuildAndWatch("generate")
		Expect(sess.ExitCode()).To(Equal(0))

		By("cleaning up the child pipeline")
		childSess := fly.Start("destroy-pipeline", "-n", "-p", pipelineName+"/dynamic-child")
		<-childSess.Exited
	})

	It("runs a matrix-style parallel test pipeline", func() {
		cfg := writePipelineFile("matrix.yml", `
jobs:
- name: test-matrix
  plan:
  - in_parallel:
      limit: 3
      steps:
      - task: test-go
        config:
          platform: linux
          image_resource: {type: registry-image, source: {repository: busybox}}
          run:
            path: echo
            args: ["GO_OK"]
      - task: test-js
        config:
          platform: linux
          image_resource: {type: registry-image, source: {repository: busybox}}
          run:
            path: echo
            args: ["JS_OK"]
      - task: test-py
        config:
          platform: linux
          image_resource: {type: registry-image, source: {repository: busybox}}
          run:
            path: echo
            args: ["PY_OK"]
`)
		setAndUnpausePipeline(cfg)
		triggerJob("test-matrix")

		sess := waitForBuildAndWatch("test-matrix")
		Expect(sess.ExitCode()).To(Equal(0))
		output := string(sess.Out.Contents())
		Expect(output).To(ContainSubstring("GO_OK"))
		Expect(output).To(ContainSubstring("JS_OK"))
		Expect(output).To(ContainSubstring("PY_OK"))
	})

	It("runs a notification-style pipeline with on_success/on_failure", func() {
		cfg := writePipelineFile("notification.yml", `
resources:
- name: notify
  type: mock
  source: {mirror_self: true}

jobs:
- name: notify-job
  plan:
  - task: work
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["WORK_DONE"]
    on_success:
      put: notify
      params: {version: success}
`)
		setAndUnpausePipeline(cfg)
		triggerJob("notify-job")

		sess := waitForBuildAndWatch("notify-job")
		Expect(sess.ExitCode()).To(Equal(0))
		Expect(sess.Out).To(gbytes.Say("WORK_DONE"))
	})

	It("runs a gated deployment pipeline with manual trigger", func() {
		cfg := writePipelineFile("gated.yml", `
resources:
- name: src
  type: mock
  source: {mirror_self: true}

jobs:
- name: build
  plan:
  - get: src
    trigger: true
  - task: compile
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["BUILT"]

- name: deploy-prod
  plan:
  - get: src
    passed: [build]
  - task: deploy
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["DEPLOYED_TO_PROD"]
`)
		setAndUnpausePipeline(cfg)
		newMockVersion("src", "v1")
		triggerJob("build")

		sess := waitForBuildAndWatch("build")
		Expect(sess.ExitCode()).To(Equal(0))

		By("manually triggering the gated deploy")
		triggerJob("deploy-prod")
		sess = waitForBuildAndWatch("deploy-prod")
		Expect(sess.ExitCode()).To(Equal(0))
		Expect(sess.Out).To(gbytes.Say("DEPLOYED_TO_PROD"))
	})

	It("runs a time-triggered pipeline", func() {
		cfg := writePipelineFile("time-trigger.yml", `
resources:
- name: every-minute
  type: time
  source: {interval: 1m}

jobs:
- name: periodic-job
  plan:
  - get: every-minute
    trigger: true
  - task: tick
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["TICK"]
`)
		setAndUnpausePipeline(cfg)

		By("waiting for the time resource to trigger a build")
		Eventually(func() int {
			rows := flyTable("builds", "-j", inPipeline("periodic-job"))
			return len(rows)
		}, 2*time.Minute, time.Second).Should(BeNumerically(">=", 1))

		sess := waitForBuildAndWatch("periodic-job")
		Expect(sess.ExitCode()).To(Equal(0))
		Expect(sess.Out).To(gbytes.Say("TICK"))
	})

	It("runs pipelines in separate teams with isolation", func() {
		teamName := pipelineName + "-e2e-team"
		fly.Run("set-team", "-n", teamName,
			"--local-user", config.ATCUsername,
			"--non-interactive")

		defer func() {
			fly.Login(config.ATCUsername, config.ATCPassword, config.ATCURL)
			fly.Start("destroy-team", "-n", teamName, "--non-interactive")
		}()

		By("logging into the new team and setting a pipeline")
		fly.Login(config.ATCUsername, config.ATCPassword, config.ATCURL, "-n", teamName)

		cfg := writePipelineFile("team-pipeline.yml", `
jobs:
- name: team-job
  plan:
  - task: hello
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["TEAM_ISOLATED"]
`)
		fly.Run("set-pipeline", "-n", "-p", pipelineName, "-c", cfg)
		fly.Run("unpause-pipeline", "-p", pipelineName)
		triggerJob("team-job")

		sess := waitForBuildAndWatch("team-job")
		Expect(sess.ExitCode()).To(Equal(0))
		Expect(sess.Out).To(gbytes.Say("TEAM_ISOLATED"))

		By("logging back to main team")
		fly.Login(config.ATCUsername, config.ATCPassword, config.ATCURL)
	})

	It("runs a long-running build to completion", func() {
		cfg := writePipelineFile("long-running.yml", `
jobs:
- name: long-job
  plan:
  - task: long-task
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: sh
        args: ["-c", "for i in $(seq 1 10); do echo STEP_$i && sleep 2; done && echo LONG_DONE"]
`)
		setAndUnpausePipeline(cfg)
		triggerJob("long-job")

		sess := waitForBuildAndWatch("long-job")
		Expect(sess.ExitCode()).To(Equal(0))
		Expect(sess.Out).To(gbytes.Say("LONG_DONE"))
		assertPodCleanupForPipeline()
	})

	It("runs a pipeline with custom resource types", func() {
		cfg := writePipelineFile("custom-type.yml", `
resource_types:
- name: custom-mock
  type: registry-image
  source: {repository: concourse/mock-resource}

resources:
- name: custom-res
  type: custom-mock
  source: {mirror_self: true}

jobs:
- name: custom-type-job
  plan:
  - get: custom-res
  - task: verify
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs: [{name: custom-res}]
      run:
        path: echo
        args: ["CUSTOM_TYPE_OK"]
`)
		setAndUnpausePipeline(cfg)
		fly.Run("check-resource", "-r", inPipeline("custom-res"))
		triggerJob("custom-type-job")

		sess := waitForBuildAndWatch("custom-type-job")
		Expect(sess.ExitCode()).To(Equal(0))
		Expect(sess.Out).To(gbytes.Say("CUSTOM_TYPE_OK"))
	})

	It("chains image_resource through multiple tasks", func() {
		cfg := writePipelineFile("image-chain.yml", `
jobs:
- name: image-chain-job
  plan:
  - task: alpine
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: alpine, tag: "latest"}}
      run:
        path: echo
        args: ["ALPINE_OK"]
  - task: busybox
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["BUSYBOX_OK"]
`)
		setAndUnpausePipeline(cfg)
		triggerJob("image-chain-job")

		sess := waitForBuildAndWatch("image-chain-job")
		Expect(sess.ExitCode()).To(Equal(0))
		output := string(sess.Out.Contents())
		Expect(output).To(ContainSubstring("ALPINE_OK"))
		Expect(output).To(ContainSubstring("BUSYBOX_OK"))

		By("verifying K8s pod cleanup after image chain")
		assertPodCleanupForPipeline()
	})

	It("creates and cleans up K8s pods for each E2E scenario", func() {
		cfg := writePipelineFile("k8s-e2e.yml", `
jobs:
- name: k8s-e2e-job
  plan:
  - task: work
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: sh
        args: ["-c", "echo K8S_E2E && sleep 5"]
`)
		setAndUnpausePipeline(cfg)
		triggerJob("k8s-e2e-job")

		By("verifying at least one pod was created")
		pods := waitForConcoursePodsAtLeast(1)
		Expect(pods).ToNot(BeEmpty())

		By("waiting for build to complete")
		sess := waitForBuildAndWatch("k8s-e2e-job")
		Expect(sess.ExitCode()).To(Equal(0))

		By("verifying all pods are cleaned up")
		assertPodCleanupForPipeline()
	})
})
