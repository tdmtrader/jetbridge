package integration_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
)

var _ = Describe("Artifact Passing", func() {
	It("passes artifacts from get to task", func() {
		pipelineFile := writePipelineFile("get-to-task.yml", `
resources:
- name: src
  type: mock
  source:
    create_files:
      readme.txt: "content from resource"

jobs:
- name: read-job
  plan:
  - get: src
    trigger: false
  - task: read-file
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      inputs:
      - name: src
      run:
        path: sh
        args:
        - -c
        - |
          echo "artifact contains:"
          cat src/readme.txt
`)
		setAndUnpausePipeline(pipelineFile)
		newMockVersion("src", "v1")
		triggerJob("read-job")

		session := waitForBuildAndWatch("read-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("content from resource"))
	})

	It("passes artifacts between chained tasks", func() {
		pipelineFile := writePipelineFile("task-chain.yml", `
jobs:
- name: chain-job
  plan:
  - task: producer
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      outputs:
      - name: intermediate
      run:
        path: sh
        args:
        - -c
        - echo "produced-by-task-1" > intermediate/data.txt
  - task: consumer
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      inputs:
      - name: intermediate
      run:
        path: sh
        args:
        - -c
        - |
          echo "consumer received:"
          cat intermediate/data.txt
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("chain-job")

		session := waitForBuildAndWatch("chain-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("produced-by-task-1"))
	})

	It("passes artifacts from multiple get steps to a single task", func() {
		pipelineFile := writePipelineFile("multi-input.yml", `
resources:
- name: src-a
  type: mock
  source:
    create_files:
      a.txt: "data-from-a"

- name: src-b
  type: mock
  source:
    create_files:
      b.txt: "data-from-b"

jobs:
- name: multi-job
  plan:
  - in_parallel:
    - get: src-a
      trigger: false
    - get: src-b
      trigger: false
  - task: read-both
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      inputs:
      - name: src-a
      - name: src-b
      run:
        path: sh
        args:
        - -c
        - |
          echo "a says:"
          cat src-a/a.txt
          echo "b says:"
          cat src-b/b.txt
`)
		setAndUnpausePipeline(pipelineFile)
		newMockVersion("src-a", "v1")
		newMockVersion("src-b", "v1")
		triggerJob("multi-job")

		session := waitForBuildAndWatch("multi-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("data-from-a"))
		Expect(session.Out).To(gbytes.Say("data-from-b"))
	})
})
