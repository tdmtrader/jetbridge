package integration_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
)

var _ = Describe("Resource Advanced", func() {
	It("skips the implicit get after put with no_get: true", func() {
		pipelineFile := writePipelineFile("no-get-put.yml", `
resources:
- name: no-get-res
  type: mock
  source:
    create_files:
      data.txt: "no-get-data"

jobs:
- name: no-get-job
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
        - echo "produced" > produced/data.txt
  - put: no-get-res
    no_get: true
    params:
      version: no-get-v1
  - task: verify
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      run:
        path: echo
        args: ["no-get-put-done"]
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("no-get-job")

		session := waitForBuildAndWatch("no-get-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("no-get-put-done"))
	})

	It("restricts put inputs with explicit input list", func() {
		pipelineFile := writePipelineFile("put-inputs.yml", `
resources:
- name: input-res
  type: mock
  source:
    create_files:
      data.txt: "input-data"
- name: output-res
  type: mock
  source: {}

jobs:
- name: put-inputs-job
  plan:
  - get: input-res
    trigger: false
  - task: make-output
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      inputs:
      - name: input-res
      outputs:
      - name: produced
      run:
        path: sh
        args:
        - -c
        - echo "made-it" > produced/data.txt
  - put: output-res
    inputs:
    - produced
    params:
      version: inputs-v1
`)
		setAndUnpausePipeline(pipelineFile)
		newMockVersion("input-res", "v1")
		triggerJob("put-inputs-job")

		session := waitForBuildAndWatch("put-inputs-job")
		Expect(session).To(gexec.Exit(0))

		builds := flyTable("builds", "-j", inPipeline("put-inputs-job"))
		Expect(builds).ToNot(BeEmpty())
		Expect(builds[0]["status"]).To(Equal("succeeded"))
	})

	It("uses detect mode for put inputs", func() {
		pipelineFile := writePipelineFile("put-inputs-detect.yml", `
resources:
- name: detect-res
  type: mock
  source: {}

jobs:
- name: put-detect-job
  plan:
  - task: produce
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      outputs:
      - name: artifacts
      run:
        path: sh
        args:
        - -c
        - echo "detect-data" > artifacts/data.txt
  - put: detect-res
    inputs: detect
    params:
      version: detect-v1
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("put-detect-job")

		session := waitForBuildAndWatch("put-detect-job")
		Expect(session).To(gexec.Exit(0))
	})

	It("uses all mode for put inputs", func() {
		pipelineFile := writePipelineFile("put-inputs-all.yml", `
resources:
- name: all-res
  type: mock
  source: {}

jobs:
- name: put-all-job
  plan:
  - task: produce
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      outputs:
      - name: out-a
      - name: out-b
      run:
        path: sh
        args:
        - -c
        - |
          echo "a-data" > out-a/data.txt
          echo "b-data" > out-b/data.txt
  - put: all-res
    inputs: all
    params:
      version: all-v1
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("put-all-job")

		session := waitForBuildAndWatch("put-all-job")
		Expect(session).To(gexec.Exit(0))
	})

	It("triggers job automatically when resource has new version with trigger: true", func() {
		pipelineFile := writePipelineFile("auto-trigger.yml", `
resources:
- name: auto-res
  type: mock
  source:
    create_files:
      data.txt: "auto-data"

jobs:
- name: auto-trigger-job
  plan:
  - get: auto-res
    trigger: true
  - task: consume
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      inputs:
      - name: auto-res
      run:
        path: echo
        args: ["auto-triggered-done"]
`)
		setAndUnpausePipeline(pipelineFile)

		By("injecting a version to trigger the job automatically")
		newMockVersion("auto-res", "v1")

		By("waiting for the auto-triggered build to appear and complete")
		Eventually(func() string {
			builds := flyTable("builds", "-j", inPipeline("auto-trigger-job"))
			if len(builds) == 0 {
				return ""
			}
			return builds[0]["status"]
		}, 3*time.Minute, 5*time.Second).Should(SatisfyAny(
			Equal("succeeded"),
			Equal("started"),
		))

		session := waitForBuildAndWatch("auto-trigger-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("auto-triggered-done"))
	})

	It("handles check_every: never to disable automatic checking", func() {
		pipelineFile := writePipelineFile("check-never.yml", `
resources:
- name: static-res
  type: mock
  check_every: never
  source:
    create_files:
      data.txt: "static-data"

jobs:
- name: check-never-job
  plan:
  - get: static-res
    trigger: false
  - task: use-it
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      inputs:
      - name: static-res
      run:
        path: echo
        args: ["check-never-done"]
`)
		setAndUnpausePipeline(pipelineFile)

		By("manually checking the resource since auto-check is disabled")
		newMockVersion("static-res", "v1")
		triggerJob("check-never-job")

		session := waitForBuildAndWatch("check-never-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("check-never-done"))
	})

	It("gets every version with version: every", func() {
		pipelineFile := writePipelineFile("version-every.yml", `
resources:
- name: every-res
  type: mock
  source:
    create_files:
      data.txt: "every-data"

jobs:
- name: every-job
  plan:
  - get: every-res
    version: every
    trigger: false
  - task: process
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      inputs:
      - name: every-res
      run:
        path: echo
        args: ["every-version-processed"]
`)
		setAndUnpausePipeline(pipelineFile)

		By("injecting two versions")
		newMockVersion("every-res", "v1")
		newMockVersion("every-res", "v2")

		By("triggering and verifying first build")
		triggerJob("every-job")
		session := waitForBuildAndWatch("every-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("every-version-processed"))
	})

	It("uses get_params on the implicit get after put", func() {
		pipelineFile := writePipelineFile("get-params.yml", `
resources:
- name: params-res
  type: mock
  source:
    create_files:
      data.txt: "params-data"

jobs:
- name: get-params-job
  plan:
  - task: produce
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      outputs:
      - name: out
      run:
        path: sh
        args:
        - -c
        - echo "produced" > out/data.txt
  - put: params-res
    params:
      version: params-v1
    get_params:
      skip_download: true
  - task: after-put
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      run:
        path: echo
        args: ["get-params-done"]
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("get-params-job")

		session := waitForBuildAndWatch("get-params-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("get-params-done"))
	})

	It("renames a resource with old_name", func() {
		By("setting initial pipeline with original resource name")
		initialFile := writePipelineFile("resource-rename-initial.yml", `
resources:
- name: original-res
  type: mock
  source:
    create_files:
      data.txt: "renamed-data"

jobs:
- name: rename-job
  plan:
  - get: original-res
    trigger: false
  - task: use-it
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      inputs:
      - name: original-res
      run:
        path: echo
        args: ["rename-done"]
`)
		setAndUnpausePipeline(initialFile)
		newMockVersion("original-res", "v1")
		triggerJob("rename-job")
		session := waitForBuildAndWatch("rename-job")
		Expect(session).To(gexec.Exit(0))

		By("renaming the resource using old_name")
		renamedFile := writePipelineFile("resource-rename-updated.yml", `
resources:
- name: new-res-name
  old_name: original-res
  type: mock
  source:
    create_files:
      data.txt: "renamed-data"

jobs:
- name: rename-job
  plan:
  - get: new-res-name
    trigger: false
  - task: use-it
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      inputs:
      - name: new-res-name
      run:
        path: echo
        args: ["renamed-resource-done"]
`)
		setPipeline(renamedFile)

		By("triggering with renamed resource")
		triggerJob("rename-job")
		session = waitForBuildAndWatch("rename-job", "2")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("renamed-resource-done"))
	})
})
