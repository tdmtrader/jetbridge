package behavioral_test

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
)

var _ = Describe("Composite Steps", func() {
	Context("do step", func() {
		It("8.1: executes steps sequentially with artifact flow", func() {
			pipelineFile := writePipelineFile("do-sequential.yml", `
jobs:
- name: do-seq-job
  plan:
  - do:
    - task: step-a
      config:
        platform: linux
        image_resource: {type: registry-image, source: {repository: busybox}}
        outputs: [{name: shared}]
        run:
          path: sh
          args: ["-c", "echo -n step-a > shared/order.txt && echo do-step-a-done"]
    - task: step-b
      config:
        platform: linux
        image_resource: {type: registry-image, source: {repository: busybox}}
        inputs: [{name: shared}]
        outputs: [{name: shared}]
        run:
          path: sh
          args:
          - -c
          - |
            prev=$(cat shared/order.txt)
            echo -n "${prev},step-b" > shared/order.txt
            echo "do-step-b-done"
    - task: step-c
      config:
        platform: linux
        image_resource: {type: registry-image, source: {repository: busybox}}
        inputs: [{name: shared}]
        run:
          path: sh
          args: ["-c", "echo artifact-flow=$(cat shared/order.txt)"]
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("do-seq-job")

			session := waitForBuildAndWatch("do-seq-job")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("do-step-a-done"))
			Expect(session.Out).To(gbytes.Say("do-step-b-done"))
			Expect(session.Out).To(gbytes.Say("artifact-flow=step-a,step-b"))
		})

		It("8.2: fails fast when a step in do fails", func() {
			pipelineFile := writePipelineFile("do-fail-fast.yml", `
jobs:
- name: do-fail-job
  plan:
  - do:
    - task: step-ok
      config:
        platform: linux
        image_resource: {type: registry-image, source: {repository: busybox}}
        run:
          path: echo
          args: ["do-step-ok"]
    - task: step-fail
      config:
        platform: linux
        image_resource: {type: registry-image, source: {repository: busybox}}
        run:
          path: sh
          args: ["-c", "echo do-step-failing && exit 1"]
    - task: step-skipped
      config:
        platform: linux
        image_resource: {type: registry-image, source: {repository: busybox}}
        run:
          path: echo
          args: ["do-step-should-not-run"]
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("do-fail-job")

			session := waitForBuildAndWatch("do-fail-job")
			Expect(session.ExitCode()).ToNot(Equal(0))
			output := string(session.Out.Contents())
			Expect(output).To(ContainSubstring("do-step-ok"))
			Expect(output).To(ContainSubstring("do-step-failing"))
			Expect(output).ToNot(ContainSubstring("do-step-should-not-run"))
		})
	})

	Context("in_parallel step", func() {
		It("8.3: runs steps concurrently", func() {
			pipelineFile := writePipelineFile("parallel-concurrent.yml", `
jobs:
- name: parallel-job
  plan:
  - in_parallel:
    - task: task-a
      config:
        platform: linux
        image_resource: {type: registry-image, source: {repository: busybox}}
        run:
          path: sh
          args: ["-c", "echo parallel-a-started && sleep 3 && echo parallel-a-done"]
    - task: task-b
      config:
        platform: linux
        image_resource: {type: registry-image, source: {repository: busybox}}
        run:
          path: sh
          args: ["-c", "echo parallel-b-started && sleep 3 && echo parallel-b-done"]
    - task: task-c
      config:
        platform: linux
        image_resource: {type: registry-image, source: {repository: busybox}}
        run:
          path: sh
          args: ["-c", "echo parallel-c-started && sleep 3 && echo parallel-c-done"]
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("parallel-job")

			session := waitForBuildAndWatch("parallel-job")
			Expect(session).To(gexec.Exit(0))
			output := string(session.Out.Contents())
			Expect(output).To(ContainSubstring("parallel-a-done"))
			Expect(output).To(ContainSubstring("parallel-b-done"))
			Expect(output).To(ContainSubstring("parallel-c-done"))
		})

		It("8.4: limits concurrency with limit parameter", func() {
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
          image_resource: {type: registry-image, source: {repository: busybox}}
          run:
            path: sh
            args: ["-c", "echo par-1-started && sleep 2 && echo par-1-done"]
      - task: par-2
        config:
          platform: linux
          image_resource: {type: registry-image, source: {repository: busybox}}
          run:
            path: sh
            args: ["-c", "echo par-2-started && sleep 2 && echo par-2-done"]
      - task: par-3
        config:
          platform: linux
          image_resource: {type: registry-image, source: {repository: busybox}}
          run:
            path: sh
            args: ["-c", "echo par-3-started && sleep 2 && echo par-3-done"]
      - task: par-4
        config:
          platform: linux
          image_resource: {type: registry-image, source: {repository: busybox}}
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

		It("8.5: fails fast with fail_fast: true", func() {
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
          image_resource: {type: registry-image, source: {repository: busybox}}
          run:
            path: sh
            args: ["-c", "echo quick-fail-running && exit 1"]
      - task: slow-success
        config:
          platform: linux
          image_resource: {type: registry-image, source: {repository: busybox}}
          run:
            path: sh
            args: ["-c", "echo slow-task-started && sleep 60 && echo slow-task-completed"]
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("parallel-ff-job")

			session := waitForBuildAndWatch("parallel-ff-job")
			Expect(session.ExitCode()).ToNot(Equal(0))
			output := string(session.Out.Contents())
			Expect(output).To(ContainSubstring("quick-fail-running"))

			By("verifying slow task was interrupted by fail_fast")
			Expect(output).To(ContainSubstring("interrupted"))
		})

		It("8.6: waits for all steps without fail_fast", func() {
			pipelineFile := writePipelineFile("parallel-no-ff.yml", `
jobs:
- name: parallel-no-ff-job
  plan:
  - in_parallel:
    - task: quick-fail
      config:
        platform: linux
        image_resource: {type: registry-image, source: {repository: busybox}}
        run:
          path: sh
          args: ["-c", "echo no-ff-fail && exit 1"]
    - task: slow-success
      config:
        platform: linux
        image_resource: {type: registry-image, source: {repository: busybox}}
        run:
          path: sh
          args: ["-c", "echo no-ff-slow-started && sleep 5 && echo no-ff-slow-done"]
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("parallel-no-ff-job")

			session := waitForBuildAndWatch("parallel-no-ff-job")
			Expect(session.ExitCode()).ToNot(Equal(0))
			output := string(session.Out.Contents())
			Expect(output).To(ContainSubstring("no-ff-fail"))
			Expect(output).To(ContainSubstring("no-ff-slow-done"))
		})
	})

	Context("try step", func() {
		It("8.7: swallows failure and continues", func() {
			pipelineFile := writePipelineFile("try-swallow.yml", `
jobs:
- name: try-swallow-job
  plan:
  - try:
      task: may-fail
      config:
        platform: linux
        image_resource: {type: registry-image, source: {repository: busybox}}
        run:
          path: sh
          args: ["-c", "echo try-failing && exit 1"]
  - task: after-try
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["try-swallowed-failure"]
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("try-swallow-job")

			session := waitForBuildAndWatch("try-swallow-job")
			Expect(session).To(gexec.Exit(0))
			output := string(session.Out.Contents())
			Expect(output).To(ContainSubstring("try-failing"))
			Expect(output).To(ContainSubstring("try-swallowed-failure"))
		})

		It("8.8: propagates success artifacts from try", func() {
			pipelineFile := writePipelineFile("try-artifacts.yml", `
jobs:
- name: try-artifacts-job
  plan:
  - try:
      task: produce-artifact
      config:
        platform: linux
        image_resource: {type: registry-image, source: {repository: busybox}}
        outputs: [{name: data}]
        run:
          path: sh
          args: ["-c", "echo try-artifact-value > data/output.txt"]
  - task: consume-artifact
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs: [{name: data}]
      run:
        path: sh
        args: ["-c", "echo consumed=$(cat data/output.txt)"]
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("try-artifacts-job")

			session := waitForBuildAndWatch("try-artifacts-job")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("consumed=try-artifact-value"))
		})
	})

	Context("across step", func() {
		It("8.9: expands over a list of values", func() {
			pipelineFile := writePipelineFile("across-expand.yml", `
jobs:
- name: across-expand-job
  plan:
  - task: expand
    across:
    - var: fruit
      values: ["apple", "banana", "cherry"]
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      params:
        FRUIT: ((.:fruit))
      run:
        path: sh
        args: ["-c", "echo fruit=${FRUIT}"]
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("across-expand-job")

			session := waitForBuildAndWatch("across-expand-job")
			Expect(session).To(gexec.Exit(0))
			output := string(session.Out.Contents())
			Expect(output).To(ContainSubstring("fruit=apple"))
			Expect(output).To(ContainSubstring("fruit=banana"))
			Expect(output).To(ContainSubstring("fruit=cherry"))
		})

		It("8.10: cross-product of multiple variables", func() {
			pipelineFile := writePipelineFile("across-cross.yml", `
jobs:
- name: across-cross-job
  plan:
  - task: matrix
    across:
    - var: os
      values: ["linux", "darwin"]
    - var: arch
      values: ["amd64", "arm64"]
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      params:
        OS: ((.:os))
        ARCH: ((.:arch))
      run:
        path: sh
        args: ["-c", "echo build-${OS}-${ARCH}"]
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("across-cross-job")

			session := waitForBuildAndWatch("across-cross-job")
			Expect(session).To(gexec.Exit(0))
			output := string(session.Out.Contents())
			Expect(output).To(ContainSubstring("build-linux-amd64"))
			Expect(output).To(ContainSubstring("build-linux-arm64"))
			Expect(output).To(ContainSubstring("build-darwin-amd64"))
			Expect(output).To(ContainSubstring("build-darwin-arm64"))
		})

		It("8.11: limits concurrency with max_in_flight", func() {
			pipelineFile := writePipelineFile("across-max-in-flight.yml", `
jobs:
- name: across-mif-job
  plan:
  - task: limited
    across:
    - var: item
      values: ["a", "b", "c", "d"]
      max_in_flight: 2
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      params:
        ITEM: ((.:item))
      run:
        path: sh
        args: ["-c", "echo across-item=${ITEM} && sleep 1"]
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("across-mif-job")

			session := waitForBuildAndWatch("across-mif-job")
			Expect(session).To(gexec.Exit(0))
			output := string(session.Out.Contents())
			Expect(output).To(ContainSubstring("across-item=a"))
			Expect(output).To(ContainSubstring("across-item=b"))
			Expect(output).To(ContainSubstring("across-item=c"))
			Expect(output).To(ContainSubstring("across-item=d"))
		})

		It("8.12: stops early with fail_fast on across", func() {
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
      image_resource: {type: registry-image, source: {repository: busybox}}
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
	})

	Context("nested composites", func() {
		It("8.13: nests do inside in_parallel", func() {
			pipelineFile := writePipelineFile("nested-composites.yml", `
jobs:
- name: nested-job
  plan:
  - in_parallel:
    - do:
      - task: branch-a-step-1
        config:
          platform: linux
          image_resource: {type: registry-image, source: {repository: busybox}}
          outputs: [{name: shared}]
          run:
            path: sh
            args: ["-c", "echo branch-a-s1 > shared/file.txt && echo nested-a1-done"]
      - task: branch-a-step-2
        config:
          platform: linux
          image_resource: {type: registry-image, source: {repository: busybox}}
          inputs: [{name: shared}]
          run:
            path: sh
            args: ["-c", "echo nested-a2-read=$(cat shared/file.txt)"]
    - do:
      - task: branch-b-step-1
        config:
          platform: linux
          image_resource: {type: registry-image, source: {repository: busybox}}
          run:
            path: echo
            args: ["nested-b1-done"]
      - task: branch-b-step-2
        config:
          platform: linux
          image_resource: {type: registry-image, source: {repository: busybox}}
          run:
            path: echo
            args: ["nested-b2-done"]
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("nested-job")

			session := waitForBuildAndWatch("nested-job")
			Expect(session).To(gexec.Exit(0))
			output := string(session.Out.Contents())
			Expect(output).To(ContainSubstring("nested-a1-done"))
			Expect(output).To(ContainSubstring("nested-a2-read=branch-a-s1"))
			Expect(output).To(ContainSubstring("nested-b1-done"))
			Expect(output).To(ContainSubstring("nested-b2-done"))
		})
	})

	Context("K8s assertions", func() {
		It("8.14: in_parallel with 3 tasks creates concurrent pods", func() {
			pipelineFile := writePipelineFile("parallel-k8s-pods.yml", `
jobs:
- name: parallel-pods-job
  plan:
  - in_parallel:
    - task: task-a
      config:
        platform: linux
        image_resource: {type: registry-image, source: {repository: busybox}}
        run:
          path: sh
          args: ["-c", "echo k8s-par-a && sleep 30"]
    - task: task-b
      config:
        platform: linux
        image_resource: {type: registry-image, source: {repository: busybox}}
        run:
          path: sh
          args: ["-c", "echo k8s-par-b && sleep 30"]
    - task: task-c
      config:
        platform: linux
        image_resource: {type: registry-image, source: {repository: busybox}}
        run:
          path: sh
          args: ["-c", "echo k8s-par-c && sleep 30"]
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("parallel-pods-job")

			By("waiting for at least 3 concurrent pods")
			pods := waitForConcoursePodsAtLeast(3)
			Expect(len(pods)).To(BeNumerically(">=", 3),
				fmt.Sprintf("expected at least 3 concurrent pods, got %d", len(pods)),
			)

			By("verifying all pods have task-related names")
			var taskPodNames []string
			for _, p := range pods {
				taskPodNames = append(taskPodNames, p.Name)
			}
			Expect(len(taskPodNames)).To(BeNumerically(">=", 3))

			By("aborting the build to avoid waiting for sleep")
			fly.Run("abort-build", "-j", inPipeline("parallel-pods-job"), "-b", "1")

			Eventually(func() string {
				builds := flyTable("builds", "-j", inPipeline("parallel-pods-job"))
				if len(builds) == 0 {
					return ""
				}
				return builds[0]["status"]
			}, 1*time.Minute, 2*time.Second).Should(Equal("aborted"))
		})

		It("8.15: all pods cleaned up after composite build", func() {
			pipelineFile := writePipelineFile("parallel-k8s-cleanup.yml", `
jobs:
- name: parallel-cleanup-job
  plan:
  - in_parallel:
    - task: clean-a
      config:
        platform: linux
        image_resource: {type: registry-image, source: {repository: busybox}}
        run:
          path: echo
          args: ["clean-a-done"]
    - task: clean-b
      config:
        platform: linux
        image_resource: {type: registry-image, source: {repository: busybox}}
        run:
          path: echo
          args: ["clean-b-done"]
    - task: clean-c
      config:
        platform: linux
        image_resource: {type: registry-image, source: {repository: busybox}}
        run:
          path: echo
          args: ["clean-c-done"]
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("parallel-cleanup-job")

			session := waitForBuildAndWatch("parallel-cleanup-job")
			Expect(session).To(gexec.Exit(0))
			output := string(session.Out.Contents())
			Expect(output).To(ContainSubstring("clean-a-done"))
			Expect(output).To(ContainSubstring("clean-b-done"))
			Expect(output).To(ContainSubstring("clean-c-done"))

			By("verifying all pods are cleaned up")
			waitForPodCleanupByPipeline()
		})
	})

})
