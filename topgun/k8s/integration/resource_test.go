package integration_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
)

var _ = Describe("Resource Lifecycle", func() {
	It("checks and gets a mock resource", func() {
		pipelineFile := writePipelineFile("resource-get.yml", `
resources:
- name: my-resource
  type: mock
  source:
    create_files:
      file1.txt: "hello from mock"

jobs:
- name: get-job
  plan:
  - get: my-resource
    trigger: false
  - task: read-resource
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      inputs:
      - name: my-resource
      run:
        path: cat
        args: ["my-resource/file1.txt"]
`)
		setAndUnpausePipeline(pipelineFile)

		By("injecting a version via check-resource")
		newMockVersion("my-resource", "v1")

		By("triggering the job")
		triggerJob("get-job")

		By("watching the build")
		session := waitForBuildAndWatch("get-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("hello from mock"))
	})

	It("runs a put step against a mock resource", func() {
		pipelineFile := writePipelineFile("resource-put.yml", `
resources:
- name: my-output
  type: mock
  source: {}

jobs:
- name: put-job
  plan:
  - task: produce
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      outputs:
      - name: produced
      run:
        path: sh
        args:
        - -c
        - echo "produced-data" > produced/result.txt
  - put: my-output
    params:
      version: put-v1
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("put-job")

		session := waitForBuildAndWatch("put-job")
		Expect(session).To(gexec.Exit(0))

		builds := flyTable("builds", "-j", inPipeline("put-job"))
		Expect(builds).ToNot(BeEmpty())
		Expect(builds[0]["status"]).To(Equal("succeeded"))
	})

	It("runs a get → task → put pipeline", func() {
		pipelineFile := writePipelineFile("get-task-put.yml", `
resources:
- name: input-resource
  type: mock
  source:
    create_files:
      data.txt: "input-data"

- name: output-resource
  type: mock
  source: {}

jobs:
- name: transform-job
  plan:
  - get: input-resource
    trigger: false
  - task: transform
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      inputs:
      - name: input-resource
      outputs:
      - name: result
      run:
        path: sh
        args:
        - -c
        - |
          content=$(cat input-resource/data.txt)
          echo "transformed: $content" > result/output.txt
          echo "transformation complete"
  - put: output-resource
    params:
      version: transformed-v1
`)
		setAndUnpausePipeline(pipelineFile)

		By("injecting a version")
		newMockVersion("input-resource", "v1")

		By("triggering the job")
		triggerJob("transform-job")

		By("watching the build")
		session := waitForBuildAndWatch("transform-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("transformation complete"))

		builds := flyTable("builds", "-j", inPipeline("transform-job"))
		Expect(builds).ToNot(BeEmpty())
		Expect(builds[0]["status"]).To(Equal("succeeded"))
	})
})
