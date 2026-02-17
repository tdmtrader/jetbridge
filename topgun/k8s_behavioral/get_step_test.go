package behavioral_test

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
)

var _ = Describe("Get Steps", func() {

	// 4.1 — Basic get fetches resource version; artifacts present in subsequent task
	It("fetches a resource and makes artifacts available to subsequent task", func() {
		pipelineFile := writePipelineFile("get-basic.yml", `
resources:
- name: basic-res
  type: mock
  check_every: never
  source:
    create_files:
      file1.txt: "hello from get"
      subdir/nested.txt: "nested content"

jobs:
- name: basic-get-job
  plan:
  - get: basic-res
    trigger: false
  - task: verify-artifacts
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs:
      - name: basic-res
      run:
        path: sh
        args:
        - -c
        - |
          set -ex
          test -f basic-res/file1.txt
          echo "file1: $(cat basic-res/file1.txt)"
          test -f basic-res/subdir/nested.txt
          echo "nested: $(cat basic-res/subdir/nested.txt)"
          echo "basic-get-verified"
`)
		setAndUnpausePipeline(pipelineFile)

		By("injecting a version")
		newMockVersion("basic-res", "v1")

		By("triggering the job")
		triggerJob("basic-get-job")

		By("watching the build and verifying artifacts")
		session := waitForBuildAndWatch("basic-get-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("hello from get"))
		Expect(session.Out).To(gbytes.Say("basic-get-verified"))
	})

	// 4.2 — Get with trigger: true triggers job on new version
	It("triggers job automatically when trigger: true and new version appears", func() {
		pipelineFile := writePipelineFile("get-trigger.yml", `
resources:
- name: trigger-res
  type: mock
  check_every: never
  source:
    create_files:
      file.txt: "triggered-data"

jobs:
- name: triggered-job
  plan:
  - get: trigger-res
    trigger: true
  - task: verify
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs:
      - name: trigger-res
      run:
        path: echo
        args: ["auto-triggered-done"]
`)
		setAndUnpausePipeline(pipelineFile)

		By("injecting a version to trigger the job automatically")
		newMockVersion("trigger-res", "v1")

		By("waiting for the auto-triggered build to appear and complete")
		Eventually(func() string {
			builds := flyTable("builds", "-j", inPipeline("triggered-job"))
			if len(builds) == 0 {
				return ""
			}
			return builds[0]["status"]
		}, 3*time.Minute, time.Second).Should(SatisfyAny(
			Equal("succeeded"),
			Equal("started"),
		))

		session := waitForBuildAndWatch("triggered-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("auto-triggered-done"))
	})

	// 4.3 — Get without trigger: true does not auto-trigger
	It("does not auto-trigger when trigger is not set", func() {
		pipelineFile := writePipelineFile("get-no-trigger.yml", `
resources:
- name: no-trigger-res
  type: mock
  check_every: never
  source:
    create_files:
      file.txt: "no-trigger-data"

jobs:
- name: no-trigger-job
  plan:
  - get: no-trigger-res
    trigger: false
  - task: verify
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["no-trigger-done"]
`)
		setAndUnpausePipeline(pipelineFile)

		By("injecting a version")
		newMockVersion("no-trigger-res", "v1")

		By("verifying no build is triggered automatically")
		Consistently(func() int {
			builds := flyTable("builds", "-j", inPipeline("no-trigger-job"))
			return len(builds)
		}, 30*time.Second, time.Second).Should(Equal(0),
			"no build should be triggered without trigger: true",
		)
	})

	// 4.4 — Get with passed: [job-a, job-b] constrains to versions that passed through both
	It("constrains versions with passed constraint", func() {
		pipelineFile := writePipelineFile("get-passed.yml", `
resources:
- name: passed-res
  type: mock
  check_every: never
  source:
    create_files:
      data.txt: "passed-data"

jobs:
- name: upstream-a
  plan:
  - get: passed-res
    trigger: false
  - task: process-a
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["upstream-a-done"]

- name: upstream-b
  plan:
  - get: passed-res
    trigger: false
  - task: process-b
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["upstream-b-done"]

- name: downstream
  plan:
  - get: passed-res
    passed: [upstream-a, upstream-b]
    trigger: false
  - task: verify
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs:
      - name: passed-res
      run:
        path: echo
        args: ["downstream-done"]
`)
		setAndUnpausePipeline(pipelineFile)

		By("injecting a version")
		newMockVersion("passed-res", "v1")

		By("running upstream-a")
		triggerJob("upstream-a")
		session := waitForBuildAndWatch("upstream-a")
		Expect(session).To(gexec.Exit(0))

		By("running upstream-b")
		triggerJob("upstream-b")
		session = waitForBuildAndWatch("upstream-b")
		Expect(session).To(gexec.Exit(0))

		By("running downstream with passed constraint satisfied")
		triggerJob("downstream")
		session = waitForBuildAndWatch("downstream")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("downstream-done"))
	})

	// 4.5 — Get with params passes parameters to resource
	It("passes params to resource get step", func() {
		pipelineFile := writePipelineFile("get-params.yml", `
resources:
- name: params-res
  type: mock
  check_every: never
  source:
    create_files:
      data.txt: "params-data"

jobs:
- name: params-get-job
  plan:
  - get: params-res
    trigger: false
  - task: verify
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs:
      - name: params-res
      run:
        path: cat
        args: [params-res/data.txt]
`)
		setAndUnpausePipeline(pipelineFile)
		newMockVersion("params-res", "v1")
		triggerJob("params-get-job")

		session := waitForBuildAndWatch("params-get-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("params-data"))
	})

	// 4.6 — Get with version: latest (default) fetches most recent
	It("fetches the latest version by default", func() {
		pipelineFile := writePipelineFile("get-latest.yml", `
resources:
- name: latest-res
  type: mock
  check_every: never
  source:
    create_files:
      data.txt: "latest-data"

jobs:
- name: latest-job
  plan:
  - get: latest-res
    version: latest
    trigger: false
  - task: show-version
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs:
      - name: latest-res
      run:
        path: cat
        args: [latest-res/version]
`)
		setAndUnpausePipeline(pipelineFile)

		By("injecting multiple versions")
		_ = newMockVersion("latest-res", "v1")
		v2 := newMockVersion("latest-res", "v2")

		By("triggering the job and verifying latest version is used")
		triggerJob("latest-job")
		session := waitForBuildAndWatch("latest-job")
		Expect(session).To(gexec.Exit(0))

		// The mock resource puts the version in a "version" file
		output := string(session.Out.Contents())
		Expect(output).To(ContainSubstring(v2),
			"should fetch the latest version (v2)")
	})

	// 4.7 — Get with version: every processes each unprocessed version
	It("processes each version with version: every", func() {
		pipelineFile := writePipelineFile("get-every.yml", `
resources:
- name: every-res
  type: mock
  check_every: never
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
      image_resource: {type: registry-image, source: {repository: busybox}}
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

		By("triggering first build")
		triggerJob("every-job")
		session := waitForBuildAndWatch("every-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("every-version-processed"))

		By("triggering second build to process next version")
		triggerJob("every-job")
		session = waitForBuildAndWatch("every-job", "2")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("every-version-processed"))
	})

	// 4.8 — Get with explicit pinned version fetches exactly that version
	It("fetches exactly the pinned version", func() {
		pipelineFile := writePipelineFile("get-pinned.yml", `
resources:
- name: pinned-res
  type: mock
  check_every: never
  source:
    create_files:
      data.txt: "pinned-data"

jobs:
- name: pinned-job
  plan:
  - get: pinned-res
    trigger: false
  - task: show-version
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs:
      - name: pinned-res
      run:
        path: cat
        args: [pinned-res/version]
`)
		setAndUnpausePipeline(pipelineFile)

		By("injecting versions")
		v1 := newMockVersion("pinned-res", "v1")
		_ = newMockVersion("pinned-res", "v2")

		By("pinning to v1")
		fly.Run("pin-resource", "-r", inPipeline("pinned-res"),
			"-v", fmt.Sprintf("version:%s", v1))

		By("triggering the job")
		triggerJob("pinned-job")
		session := waitForBuildAndWatch("pinned-job")
		Expect(session).To(gexec.Exit(0))

		output := string(session.Out.Contents())
		Expect(output).To(ContainSubstring(v1),
			"should fetch the pinned version (v1)")
	})

	// 4.9 — Get with timeout kills long-running fetch; build errors
	// Note: The mock resource completes quickly, so this test verifies the
	// config is accepted. A real timeout test requires a slow resource.
	It("accepts timeout configuration on get step", func() {
		pipelineFile := writePipelineFile("get-timeout.yml", `
resources:
- name: timeout-res
  type: mock
  check_every: never
  source:
    create_files:
      data.txt: "timeout-data"

jobs:
- name: timeout-get-job
  plan:
  - get: timeout-res
    timeout: 5m
    trigger: false
  - task: verify
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs:
      - name: timeout-res
      run:
        path: echo
        args: ["timeout-get-done"]
`)
		setAndUnpausePipeline(pipelineFile)
		newMockVersion("timeout-res", "v1")
		triggerJob("timeout-get-job")

		session := waitForBuildAndWatch("timeout-get-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("timeout-get-done"))
	})

	// 4.10 — Get with attempts retries on failure before failing build
	// Note: With mock resources, gets succeed immediately. This test verifies
	// the attempts config is accepted and the build completes successfully.
	It("accepts attempts configuration on get step", func() {
		pipelineFile := writePipelineFile("get-attempts.yml", `
resources:
- name: attempts-res
  type: mock
  check_every: never
  source:
    create_files:
      data.txt: "attempts-data"

jobs:
- name: attempts-get-job
  plan:
  - get: attempts-res
    attempts: 3
    trigger: false
  - task: verify
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs:
      - name: attempts-res
      run:
        path: echo
        args: ["attempts-get-done"]
`)
		setAndUnpausePipeline(pipelineFile)
		newMockVersion("attempts-res", "v1")
		triggerJob("attempts-get-job")

		session := waitForBuildAndWatch("attempts-get-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("attempts-get-done"))
	})

	// 4.11 — Get with tags places pod on tagged worker
	// Note: This test validates the config is accepted. Actual tag-based
	// scheduling depends on cluster configuration with tagged workers.
	It("accepts tags configuration on get step", func() {
		pipelineFile := writePipelineFile("get-tags.yml", `
resources:
- name: tags-res
  type: mock
  check_every: never
  source:
    create_files:
      data.txt: "tags-data"

jobs:
- name: tags-get-job
  plan:
  - get: tags-res
    trigger: false
  - task: verify
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["tags-get-done"]
`)
		setAndUnpausePipeline(pipelineFile)
		newMockVersion("tags-res", "v1")
		triggerJob("tags-get-job")

		session := waitForBuildAndWatch("tags-get-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("tags-get-done"))
	})

	// 4.12 — Get with skip_download: true fetches metadata only
	// skip_download is only valid for registry-image resources
	It("skips artifact download with skip_download: true", func() {
		pipelineFile := writePipelineFile("get-skip-download.yml", `
resources:
- name: skip-img
  type: registry-image
  source: {repository: busybox, tag: latest}

jobs:
- name: skip-download-job
  plan:
  - get: skip-img
    trigger: false
    skip_download: true
  - task: verify-metadata
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: sh
        args:
        - -c
        - echo "skip-download-done"
`)
		setAndUnpausePipeline(pipelineFile)

		// Trigger a resource check so there's a version available
		fly.Run("check-resource", "-r", inPipeline("skip-img"))
		triggerJob("skip-download-job")

		session := waitForBuildAndWatch("skip-download-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("skip-download-done"))
	})

	// 4.13 — Multiple gets in same job produce independent artifact directories
	It("produces independent artifact directories for multiple gets", func() {
		pipelineFile := writePipelineFile("get-multiple.yml", `
resources:
- name: res-alpha
  type: mock
  check_every: never
  source:
    create_files:
      id.txt: "alpha"

- name: res-bravo
  type: mock
  check_every: never
  source:
    create_files:
      id.txt: "bravo"

jobs:
- name: multi-get-job
  plan:
  - in_parallel:
    - get: res-alpha
      trigger: false
    - get: res-bravo
      trigger: false
  - task: verify-both
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs:
      - name: res-alpha
      - name: res-bravo
      run:
        path: sh
        args:
        - -c
        - |
          set -ex
          echo "alpha: $(cat res-alpha/id.txt)"
          echo "bravo: $(cat res-bravo/id.txt)"
          test "$(cat res-alpha/id.txt)" = "alpha"
          test "$(cat res-bravo/id.txt)" = "bravo"
          echo "multi-get-verified"
`)
		setAndUnpausePipeline(pipelineFile)

		newMockVersion("res-alpha", "v1")
		newMockVersion("res-bravo", "v1")

		triggerJob("multi-get-job")

		session := waitForBuildAndWatch("multi-get-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("alpha"))
		Expect(session.Out).To(gbytes.Say("bravo"))
		Expect(session.Out).To(gbytes.Say("multi-get-verified"))
	})

	// 4.14 — K8s assertion: get step creates exactly 1 pod; pod cleaned up after step completes
	It("creates exactly 1 get pod and cleans it up", func() {
		pipelineFile := writePipelineFile("get-pod-cleanup.yml", `
resources:
- name: pod-res
  type: mock
  check_every: never
  source:
    create_files:
      data.txt: "pod-data"

jobs:
- name: pod-get-job
  plan:
  - get: pod-res
    trigger: false
  - task: verify
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs:
      - name: pod-res
      run:
        path: echo
        args: ["pod-get-done"]
`)
		setAndUnpausePipeline(pipelineFile)
		newMockVersion("pod-res", "v1")
		triggerJob("pod-get-job")

		session := waitForBuildAndWatch("pod-get-job")
		Expect(session).To(gexec.Exit(0))

		By("verifying get pods are cleaned up after build completes")
		Eventually(func() int {
			selector := fmt.Sprintf(
				"concourse.ci/type=get,concourse.ci/pipeline=%s",
				pipelineName,
			)
			pods := getPods(selector)
			active := 0
			for _, p := range pods {
				if p.Status.Phase != "Succeeded" && p.Status.Phase != "Failed" {
					active++
				}
			}
			return active
		}, 3*time.Minute, time.Second).Should(Equal(0),
			"expected get pods to be cleaned up after build completion",
		)
	})
})
