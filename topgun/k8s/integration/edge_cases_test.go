package integration_test

import (
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
)

var _ = Describe("Edge Cases", func() {
	It("runs a task with dir pointing to an input", func() {
		pipelineFile := writePipelineFile("task-dir.yml", `
resources:
- name: my-input
  type: mock
  source:
    create_files:
      subdir/file.txt: "dir-test-content"

jobs:
- name: dir-job
  plan:
  - get: my-input
    trigger: false
  - task: use-dir
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      inputs:
      - name: my-input
      run:
        path: sh
        args:
        - -c
        - |
          echo "pwd=$(pwd)"
          echo "content=$(cat subdir/file.txt)"
        dir: my-input
`)
		setAndUnpausePipeline(pipelineFile)
		newMockVersion("my-input", "v1")
		triggerJob("dir-job")

		session := waitForBuildAndWatch("dir-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("content=dir-test-content"))
	})

	It("handles input mapping (renamed inputs)", func() {
		pipelineFile := writePipelineFile("input-mapping.yml", `
resources:
- name: original-name
  type: mock
  source:
    create_files:
      data.txt: "mapped-input-data"

jobs:
- name: input-map-job
  plan:
  - get: original-name
    trigger: false
  - task: read-mapped
    input_mapping:
      renamed-input: original-name
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      inputs:
      - name: renamed-input
      run:
        path: sh
        args:
        - -c
        - |
          echo "mapped-content=$(cat renamed-input/data.txt)"
`)
		setAndUnpausePipeline(pipelineFile)
		newMockVersion("original-name", "v1")
		triggerJob("input-map-job")

		session := waitForBuildAndWatch("input-map-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("mapped-content=mapped-input-data"))
	})

	It("handles output mapping (renamed outputs)", func() {
		pipelineFile := writePipelineFile("output-mapping.yml", `
jobs:
- name: output-map-job
  plan:
  - task: produce
    output_mapping:
      internal-name: mapped-output
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      outputs:
      - name: internal-name
      run:
        path: sh
        args:
        - -c
        - echo "produced-data" > internal-name/data.txt
  - task: consume
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      inputs:
      - name: mapped-output
      run:
        path: sh
        args:
        - -c
        - echo "output-mapped-content=$(cat mapped-output/data.txt)"
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("output-map-job")

		session := waitForBuildAndWatch("output-map-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("output-mapped-content=produced-data"))
	})

	It("handles rapid sequential job triggers without pod conflicts", func() {
		pipelineFile := writePipelineFile("rapid-triggers.yml", `
jobs:
- name: rapid-job
  plan:
  - task: quick-task
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      run:
        path: echo
        args: ["rapid-build-done"]
`)
		setAndUnpausePipeline(pipelineFile)

		By("triggering 5 builds rapidly")
		for i := 0; i < 5; i++ {
			triggerJob("rapid-job")
		}

		By("verifying all 5 builds complete")
		for i := 1; i <= 5; i++ {
			session := waitForBuildAndWatch("rapid-job", fmt.Sprintf("%d", i))
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("rapid-build-done"))
		}

		By("verifying all builds succeeded")
		builds := flyTable("builds", "-j", inPipeline("rapid-job"))
		Expect(len(builds)).To(BeNumerically(">=", 5))
		for _, b := range builds[:5] {
			Expect(b["status"]).To(Equal("succeeded"))
		}
	})

	It("handles multiple tasks sharing the same input name", func() {
		pipelineFile := writePipelineFile("shared-input.yml", `
jobs:
- name: shared-input-job
  plan:
  - task: producer
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      outputs:
      - name: shared
      run:
        path: sh
        args:
        - -c
        - echo "original-data" > shared/file.txt
  - task: consumer-1
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      inputs:
      - name: shared
      run:
        path: sh
        args:
        - -c
        - echo "consumer-1-read=$(cat shared/file.txt)"
  - task: consumer-2
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      inputs:
      - name: shared
      run:
        path: sh
        args:
        - -c
        - echo "consumer-2-read=$(cat shared/file.txt)"
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("shared-input-job")

		session := waitForBuildAndWatch("shared-input-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("consumer-1-read=original-data"))
		Expect(session.Out).To(gbytes.Say("consumer-2-read=original-data"))
	})
})
