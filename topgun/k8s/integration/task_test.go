package integration_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
)

var _ = Describe("Task Execution", func() {
	It("runs a pipeline task and streams output", func() {
		pipelineFile := writePipelineFile("task-output.yml", `
jobs:
- name: echo-job
  plan:
  - task: echo-task
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      run:
        path: sh
        args:
        - -c
        - |
          echo "line one"
          echo "line two"
          echo "done"
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("echo-job")

		session := waitForBuildAndWatch("echo-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("line one"))
		Expect(session.Out).To(gbytes.Say("line two"))
		Expect(session.Out).To(gbytes.Say("done"))
	})

	It("runs a one-off task via fly execute", func() {
		taskFile := writeTaskFile("oneoff.yml", `
platform: linux
rootfs_uri: docker:///busybox
run:
  path: echo
  args: ["fly execute works"]
`)
		session := fly.Start("execute", "-c", taskFile)
		Eventually(session).Should(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("fly execute works"))
	})

	It("passes environment variables and params to tasks", func() {
		pipelineFile := writePipelineFile("task-env.yml", `
jobs:
- name: env-job
  plan:
  - task: env-task
    params:
      MY_PARAM: param-value
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      params:
        MY_PARAM: default-value
        TASK_ENV: task-env-value
      run:
        path: sh
        args:
        - -c
        - |
          echo "MY_PARAM=$MY_PARAM"
          echo "TASK_ENV=$TASK_ENV"
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("env-job")

		session := waitForBuildAndWatch("env-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("MY_PARAM=param-value"))
		Expect(session.Out).To(gbytes.Say("TASK_ENV=task-env-value"))
	})

	It("reports non-zero exit codes as build failures", func() {
		pipelineFile := writePipelineFile("task-fail.yml", `
jobs:
- name: fail-job
  plan:
  - task: fail-task
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      run:
        path: sh
        args:
        - -c
        - |
          echo "about to fail"
          exit 42
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("fail-job")

		session := waitForBuildAndWatch("fail-job")
		Expect(session.ExitCode()).ToNot(Equal(0))
		Expect(session.Out).To(gbytes.Say("about to fail"))

		builds := flyTable("builds", "-j", inPipeline("fail-job"))
		Expect(builds).ToNot(BeEmpty())
		Expect(builds[0]["status"]).To(Equal("failed"))
	})
})
