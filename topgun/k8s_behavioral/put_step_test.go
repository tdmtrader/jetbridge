package behavioral_test

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
)

var _ = Describe("Put Steps", func() {
	It("7.1: pushes a version to a resource", func() {
		pipelineFile := writePipelineFile("put-basic.yml", `
resources:
- name: output-resource
  type: mock
  source: {create_files: {result.txt: "data"}}
  check_every: never

jobs:
- name: put-job
  plan:
  - task: produce
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      outputs: [{name: output-resource}]
      run:
        path: sh
        args: ["-c", "echo result > output-resource/result.txt"]
  - put: output-resource
    params: {file: output-resource/result.txt}
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("put-job")

		session := waitForBuildAndWatch("put-job")
		Expect(session).To(gexec.Exit(0))

		By("verifying the put step completed")
		output := string(session.Out.Contents())
		Expect(output).To(ContainSubstring("output-resource"))
	})

	It("7.2: puts with params", func() {
		pipelineFile := writePipelineFile("put-params.yml", `
resources:
- name: param-resource
  type: mock
  source: {}
  check_every: never

jobs:
- name: put-params-job
  plan:
  - put: param-resource
    params:
      version: put-params-v1
      file: ""
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("put-params-job")

		session := waitForBuildAndWatch("put-params-job")
		Expect(session).To(gexec.Exit(0))
	})

	It("7.3: puts with inputs: detect", func() {
		pipelineFile := writePipelineFile("put-inputs-detect.yml", `
resources:
- name: detect-resource
  type: mock
  source: {}
  check_every: never

jobs:
- name: put-detect-job
  plan:
  - task: produce
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      outputs: [{name: detect-resource}]
      run:
        path: sh
        args: ["-c", "echo detected > detect-resource/file.txt"]
  - put: detect-resource
    inputs: detect
    params: {file: detect-resource/file.txt}
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("put-detect-job")

		session := waitForBuildAndWatch("put-detect-job")
		Expect(session).To(gexec.Exit(0))
	})

	It("7.4: puts with inputs: [explicit list]", func() {
		pipelineFile := writePipelineFile("put-inputs-list.yml", `
resources:
- name: list-resource
  type: mock
  source: {}
  check_every: never

jobs:
- name: put-list-job
  plan:
  - task: produce
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      outputs:
      - name: needed
      - name: not-needed
      run:
        path: sh
        args:
        - -c
        - |
          echo needed > needed/file.txt
          echo extra > not-needed/file.txt
  - put: list-resource
    inputs:
    - needed
    params: {file: needed/file.txt}
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("put-list-job")

		session := waitForBuildAndWatch("put-list-job")
		Expect(session).To(gexec.Exit(0))
	})

	It("7.5: puts with inputs: all", func() {
		pipelineFile := writePipelineFile("put-inputs-all.yml", `
resources:
- name: all-resource
  type: mock
  source: {}
  check_every: never

jobs:
- name: put-all-job
  plan:
  - task: produce
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      outputs: [{name: all-resource}]
      run:
        path: sh
        args: ["-c", "echo all > all-resource/file.txt"]
  - put: all-resource
    inputs: all
    params: {file: all-resource/file.txt}
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("put-all-job")

		session := waitForBuildAndWatch("put-all-job")
		Expect(session).To(gexec.Exit(0))
	})

	It("7.6: implicit get after put fetches the version", func() {
		pipelineFile := writePipelineFile("put-implicit-get.yml", `
resources:
- name: igp-resource
  type: mock
  source: {create_files: {fetched.txt: "from-implicit-get"}}
  check_every: never

jobs:
- name: put-implicit-get-job
  plan:
  - put: igp-resource
    params: {version: "igp-v1"}
  - task: verify-implicit-get
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs: [{name: igp-resource}]
      run:
        path: sh
        args:
        - -c
        - |
          echo "implicit-get-available"
          ls igp-resource/
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("put-implicit-get-job")

		session := waitForBuildAndWatch("put-implicit-get-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("implicit-get-available"))
	})

	It("7.7: put with no_get: true skips implicit get", func() {
		pipelineFile := writePipelineFile("put-no-get.yml", `
resources:
- name: ng-resource
  type: mock
  source: {}
  check_every: never

jobs:
- name: put-no-get-job
  plan:
  - put: ng-resource
    no_get: true
    params: {version: "ng-v1"}
  - task: after-put
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["no-get-done"]
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("put-no-get-job")

		session := waitForBuildAndWatch("put-no-get-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("no-get-done"))
	})

	It("7.8: put with get_params passes params to implicit get", func() {
		pipelineFile := writePipelineFile("put-get-params.yml", `
resources:
- name: gp-resource
  type: mock
  source: {create_files: {data.txt: "get-params-data"}}
  check_every: never

jobs:
- name: put-get-params-job
  plan:
  - put: gp-resource
    params: {version: "gp-v1"}
    get_params: {mirror_self: true}
  - task: verify
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs: [{name: gp-resource}]
      run:
        path: echo
        args: ["get-params-verified"]
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("put-get-params-job")

		session := waitForBuildAndWatch("put-get-params-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("get-params-verified"))
	})

	It("7.9: put with timeout", func() {
		pipelineFile := writePipelineFile("put-timeout.yml", `
resources:
- name: timeout-resource
  type: mock
  source: {}
  check_every: never

jobs:
- name: put-timeout-job
  plan:
  - put: timeout-resource
    timeout: 2m
    params: {version: "timeout-v1"}
  - task: done
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["put-timeout-done"]
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("put-timeout-job")

		session := waitForBuildAndWatch("put-timeout-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("put-timeout-done"))
	})

	It("7.10: put with attempts retries on failure", func() {
		pipelineFile := writePipelineFile("put-attempts.yml", `
resources:
- name: retry-put-resource
  type: mock
  source: {}
  check_every: never

jobs:
- name: put-attempts-job
  plan:
  - put: retry-put-resource
    attempts: 3
    params: {version: "retry-v1"}
  - task: done
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["put-attempts-done"]
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("put-attempts-job")

		session := waitForBuildAndWatch("put-attempts-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("put-attempts-done"))
	})

	It("7.11: K8s: put creates correct number of pods", func() {
		pipelineFile := writePipelineFile("put-k8s-pods.yml", `
resources:
- name: pod-count-resource
  type: mock
  source: {create_files: {data.txt: "pod-data"}}
  check_every: never

jobs:
- name: put-pod-count-job
  plan:
  - task: produce
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      outputs: [{name: pod-count-resource}]
      run:
        path: sh
        args: ["-c", "echo data > pod-count-resource/data.txt && sleep 15"]
  - put: pod-count-resource
    params: {file: pod-count-resource/data.txt}
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("put-pod-count-job")

		By("waiting for at least 1 task pod to appear during build")
		pods := waitForConcoursePodsAtLeast(1)
		Expect(len(pods)).To(BeNumerically(">=", 1),
			fmt.Sprintf("expected at least 1 pod during put-job, got %d", len(pods)),
		)

		By("waiting for the build to complete")
		session := waitForBuildAndWatch("put-pod-count-job")
		Expect(session).To(gexec.Exit(0))

		By("verifying all pods are cleaned up after build")
		waitForPodCleanupByPipeline()
	})

	It("7.12: put version is visible in resource versions after build", func() {
		pipelineFile := writePipelineFile("put-version-check.yml", `
resources:
- name: versioned-out
  type: mock
  source: {}
  check_every: never

jobs:
- name: put-version-job
  plan:
  - put: versioned-out
    params: {version: "put-created-v1"}
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("put-version-job")

		session := waitForBuildAndWatch("put-version-job")
		Expect(session).To(gexec.Exit(0))

		By("verifying the resource has at least one version after the put")
		Eventually(func() int {
			versions := getResourceVersions(pipelineName, "versioned-out")
			return len(versions)
		}, 1*time.Minute, 2*time.Second).Should(BeNumerically(">=", 1),
			"expected at least one version for versioned-out after put",
		)
	})
})
