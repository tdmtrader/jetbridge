package integration_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
)

var _ = Describe("Parallel Advanced", func() {
	It("limits concurrency with in_parallel limit", func() {
		pipelineFile := writePipelineFile("parallel-limit.yml", `
jobs:
- name: parallel-limit-job
  plan:
  - in_parallel:
      limit: 2
      steps:
      - task: par-1
        config:
          platform: linux
          rootfs_uri: docker:///busybox
          run:
            path: sh
            args: ["-c", "echo par-1-started && sleep 2 && echo par-1-done"]
      - task: par-2
        config:
          platform: linux
          rootfs_uri: docker:///busybox
          run:
            path: sh
            args: ["-c", "echo par-2-started && sleep 2 && echo par-2-done"]
      - task: par-3
        config:
          platform: linux
          rootfs_uri: docker:///busybox
          run:
            path: sh
            args: ["-c", "echo par-3-started && sleep 2 && echo par-3-done"]
      - task: par-4
        config:
          platform: linux
          rootfs_uri: docker:///busybox
          run:
            path: sh
            args: ["-c", "echo par-4-started && sleep 2 && echo par-4-done"]
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("parallel-limit-job")

		session := waitForBuildAndWatch("parallel-limit-job")
		Expect(session).To(gexec.Exit(0))
		output := string(session.Out.Contents())
		Expect(output).To(ContainSubstring("par-1-done"))
		Expect(output).To(ContainSubstring("par-2-done"))
		Expect(output).To(ContainSubstring("par-3-done"))
		Expect(output).To(ContainSubstring("par-4-done"))
	})

	It("fails fast when in_parallel fail_fast is set", func() {
		pipelineFile := writePipelineFile("parallel-fail-fast.yml", `
jobs:
- name: parallel-ff-job
  plan:
  - in_parallel:
      fail_fast: true
      steps:
      - task: quick-fail
        config:
          platform: linux
          rootfs_uri: docker:///busybox
          run:
            path: sh
            args: ["-c", "echo quick-fail-running && exit 1"]
      - task: slow-success
        config:
          platform: linux
          rootfs_uri: docker:///busybox
          run:
            path: sh
            args: ["-c", "echo slow-task-started && sleep 30 && echo slow-task-done"]
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("parallel-ff-job")

		session := waitForBuildAndWatch("parallel-ff-job")
		Expect(session.ExitCode()).ToNot(Equal(0))
		output := string(session.Out.Contents())
		Expect(output).To(ContainSubstring("quick-fail-running"))

		builds := flyTable("builds", "-j", inPipeline("parallel-ff-job"))
		Expect(builds).ToNot(BeEmpty())
		Expect(builds[0]["status"]).To(Equal("failed"))
	})

	It("runs across step with max_in_flight limiting concurrency", func() {
		pipelineFile := writePipelineFile("across-max-in-flight.yml", `
jobs:
- name: across-limited-job
  plan:
  - task: across-task
    across:
    - var: item
      values: ["a", "b", "c", "d"]
      max_in_flight: 2
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      params:
        ITEM: ((.:item))
      run:
        path: sh
        args:
        - -c
        - echo "across-item=${ITEM}" && sleep 1
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("across-limited-job")

		session := waitForBuildAndWatch("across-limited-job")
		Expect(session).To(gexec.Exit(0))
		output := string(session.Out.Contents())
		Expect(output).To(ContainSubstring("across-item=a"))
		Expect(output).To(ContainSubstring("across-item=b"))
		Expect(output).To(ContainSubstring("across-item=c"))
		Expect(output).To(ContainSubstring("across-item=d"))
	})

	It("runs across step with max_in_flight: all for full parallelism", func() {
		pipelineFile := writePipelineFile("across-all.yml", `
jobs:
- name: across-all-job
  plan:
  - task: across-task
    across:
    - var: color
      values: ["red", "green", "blue"]
      max_in_flight: all
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      params:
        COLOR: ((.:color))
      run:
        path: sh
        args:
        - -c
        - echo "all-color=${COLOR}"
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("across-all-job")

		session := waitForBuildAndWatch("across-all-job")
		Expect(session).To(gexec.Exit(0))
		output := string(session.Out.Contents())
		Expect(output).To(ContainSubstring("all-color=red"))
		Expect(output).To(ContainSubstring("all-color=green"))
		Expect(output).To(ContainSubstring("all-color=blue"))
	})

	It("stops across step early with fail_fast on failure", func() {
		pipelineFile := writePipelineFile("across-fail-fast.yml", `
jobs:
- name: across-ff-job
  plan:
  - task: across-task
    across:
    - var: num
      values: ["1", "2", "3"]
      max_in_flight: 1
    fail_fast: true
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      params:
        NUM: ((.:num))
      run:
        path: sh
        args:
        - -c
        - |
          echo "across-num=${NUM}"
          if [ "${NUM}" = "2" ]; then
            exit 1
          fi
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("across-ff-job")

		session := waitForBuildAndWatch("across-ff-job")
		Expect(session.ExitCode()).ToNot(Equal(0))
		output := string(session.Out.Contents())
		Expect(output).To(ContainSubstring("across-num=1"))
		Expect(output).To(ContainSubstring("across-num=2"))
	})

	It("wraps a get step in try", func() {
		pipelineFile := writePipelineFile("try-get.yml", `
resources:
- name: missing-res
  type: mock
  source: {}

jobs:
- name: try-get-job
  plan:
  - try:
      get: missing-res
      trigger: false
  - task: after-try-get
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      run:
        path: echo
        args: ["try-get-continued"]
`)
		setAndUnpausePipeline(pipelineFile)
		// Don't inject a version — get may fail but try catches it
		triggerJob("try-get-job")

		session := waitForBuildAndWatch("try-get-job")
		// Build should succeed because try catches get failure
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("try-get-continued"))
	})

	It("retries a get step on failure", func() {
		pipelineFile := writePipelineFile("retry-get.yml", `
resources:
- name: retry-res
  type: mock
  source:
    create_files:
      data.txt: "retry-data"

jobs:
- name: retry-get-job
  plan:
  - get: retry-res
    attempts: 2
    trigger: false
  - task: use-it
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      inputs:
      - name: retry-res
      run:
        path: echo
        args: ["retry-get-succeeded"]
`)
		setAndUnpausePipeline(pipelineFile)
		newMockVersion("retry-res", "v1")
		triggerJob("retry-get-job")

		session := waitForBuildAndWatch("retry-get-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("retry-get-succeeded"))
	})

	It("supports timeout on get step", func() {
		pipelineFile := writePipelineFile("timeout-get.yml", `
resources:
- name: fast-res
  type: mock
  source:
    create_files:
      data.txt: "fast-data"

jobs:
- name: timeout-get-job
  plan:
  - get: fast-res
    timeout: 2m
    trigger: false
  - task: done
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      inputs:
      - name: fast-res
      run:
        path: echo
        args: ["timeout-get-done"]
`)
		setAndUnpausePipeline(pipelineFile)
		newMockVersion("fast-res", "v1")
		triggerJob("timeout-get-job")

		session := waitForBuildAndWatch("timeout-get-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("timeout-get-done"))
	})

	It("supports timeout on put step", func() {
		pipelineFile := writePipelineFile("timeout-put.yml", `
resources:
- name: put-res
  type: mock
  source: {}

jobs:
- name: timeout-put-job
  plan:
  - put: put-res
    timeout: 2m
    params:
      version: timeout-v1
  - task: done
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      run:
        path: echo
        args: ["timeout-put-done"]
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("timeout-put-job")

		session := waitForBuildAndWatch("timeout-put-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("timeout-put-done"))
	})

	It("runs across with multiple variables (cartesian product)", func() {
		pipelineFile := writePipelineFile("across-multi-var.yml", `
jobs:
- name: across-multi-job
  plan:
  - task: matrix
    across:
    - var: os
      values: ["linux", "darwin"]
    - var: arch
      values: ["amd64", "arm64"]
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      params:
        OS: ((.:os))
        ARCH: ((.:arch))
      run:
        path: sh
        args:
        - -c
        - echo "build-${OS}-${ARCH}"
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("across-multi-job")

		session := waitForBuildAndWatch("across-multi-job")
		Expect(session).To(gexec.Exit(0))
		output := string(session.Out.Contents())
		Expect(output).To(ContainSubstring("build-linux-amd64"))
		Expect(output).To(ContainSubstring("build-linux-arm64"))
		Expect(output).To(ContainSubstring("build-darwin-amd64"))
		Expect(output).To(ContainSubstring("build-darwin-arm64"))
	})
})
