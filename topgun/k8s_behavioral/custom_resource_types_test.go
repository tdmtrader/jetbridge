package behavioral_test

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
)

var _ = Describe("Custom Resource Types and Image Resolution", func() {

	// -----------------------------------------------------------------
	// Type Chain Resolution (6.1-6.9)
	// -----------------------------------------------------------------

	Context("type chain resolution", func() {
		PIt("6.1: single custom type backed by registry-image resolves and works", func() {
			// Pending: requires live K8s cluster with concourse/mock-resource image pullable
			By("setting a pipeline with a custom resource type backed by mock")
			pipelineFile := writePipelineFile("custom-type-single.yml", `
resource_types:
- name: custom-mock
  type: registry-image
  source: {repository: concourse/mock-resource}

resources:
- name: my-custom
  type: custom-mock
  source:
    create_files:
      data.txt: "custom-type-data"

jobs:
- name: custom-type-job
  plan:
  - get: my-custom
    trigger: false
  - task: read-custom
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs:
      - name: my-custom
      run:
        path: cat
        args: ["my-custom/data.txt"]
`)
			setAndUnpausePipeline(pipelineFile)
			newMockVersion("my-custom", "v1")
			triggerJob("custom-type-job")

			session := waitForBuildAndWatch("custom-type-job")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("custom-type-data"))
		})

		PIt("6.2: two-level type chain resolves correctly", func() {
			// Pending: multi-level type chains require recursive image resolution in K8s runtime
			By("setting a pipeline with a two-level type chain")
			pipelineFile := writePipelineFile("custom-type-chain-2.yml", `
resource_types:
- name: level-b
  type: registry-image
  source: {repository: concourse/mock-resource}
- name: level-a
  type: level-b
  source:
    mirror_self: true
    initial_version: chain-v1

resources:
- name: chained-resource
  type: level-a
  source:
    create_files:
      chain.txt: "two-level-chain"

jobs:
- name: chain-job
  plan:
  - get: chained-resource
    trigger: false
  - task: read-chain
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs:
      - name: chained-resource
      run:
        path: cat
        args: ["chained-resource/chain.txt"]
`)
			setAndUnpausePipeline(pipelineFile)
			newMockVersionOrSkip("chained-resource", "v1")
			triggerJob("chain-job")

			session := waitForBuildAndWatch("chain-job")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("two-level-chain"))
		})

		PIt("6.3: three-level type chain resolves correctly", func() {
			// Pending: three-level type chains require recursive image resolution in K8s runtime
			By("setting a pipeline with a three-level type chain")
			pipelineFile := writePipelineFile("custom-type-chain-3.yml", `
resource_types:
- name: level-c
  type: registry-image
  source: {repository: concourse/mock-resource}
- name: level-b
  type: level-c
  source:
    mirror_self: true
    initial_version: chain-v1
- name: level-a
  type: level-b
  source:
    mirror_self: true
    initial_version: chain-v1

resources:
- name: deep-chain
  type: level-a
  source:
    create_files:
      deep.txt: "three-level-chain"

jobs:
- name: deep-chain-job
  plan:
  - get: deep-chain
    trigger: false
  - task: read-deep
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs:
      - name: deep-chain
      run:
        path: cat
        args: ["deep-chain/deep.txt"]
`)
			setAndUnpausePipeline(pipelineFile)
			newMockVersionOrSkip("deep-chain", "v1")
			triggerJob("deep-chain-job")

			session := waitForBuildAndWatch("deep-chain-job")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("three-level-chain"))
		})

		PIt("6.4: custom type with direct image field skips check/get cycle", func() {
			// Pending: direct image: field on resource_types requires E2E validation with live K8s cluster
			By("setting a pipeline with a custom type using direct image: field")
			pipelineFile := writePipelineFile("custom-type-direct-image.yml", `
resource_types:
- name: custom-mock
  image: concourse/mock-resource

resources:
- name: direct-img-res
  type: custom-mock
  source:
    create_files:
      direct.txt: "direct-image-data"

jobs:
- name: direct-image-job
  plan:
  - get: direct-img-res
    trigger: false
  - task: read-direct
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs:
      - name: direct-img-res
      run:
        path: cat
        args: ["direct-img-res/direct.txt"]
`)
			setAndUnpausePipeline(pipelineFile)
			newMockVersion("direct-img-res", "v1")
			triggerJob("direct-image-job")

			session := waitForBuildAndWatch("direct-image-job")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("direct-image-data"))
		})

		PIt("6.5: custom type defaults merge with resource source", func() {
			// Pending: requires live K8s cluster with concourse/mock-resource image pullable
			By("setting a pipeline with resource_type defaults")
			pipelineFile := writePipelineFile("custom-type-defaults.yml", `
resource_types:
- name: custom-mock
  type: registry-image
  source: {repository: concourse/mock-resource}
  defaults:
    create_files:
      default.txt: "from-defaults"

resources:
- name: defaults-res
  type: custom-mock
  source: {}

jobs:
- name: defaults-job
  plan:
  - get: defaults-res
    trigger: false
  - task: read-defaults
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs:
      - name: defaults-res
      run:
        path: sh
        args:
        - -c
        - |
          if [ -f defaults-res/default.txt ]; then
            echo "defaults-content=$(cat defaults-res/default.txt)"
          else
            echo "defaults-not-found"
          fi
`)
			setAndUnpausePipeline(pipelineFile)
			newMockVersion("defaults-res", "v1")
			triggerJob("defaults-job")

			session := waitForBuildAndWatch("defaults-job")
			Expect(session).To(gexec.Exit(0))
			// Defaults should merge, so the file should exist
			output := string(session.Out.Contents())
			Expect(output).To(SatisfyAny(
				ContainSubstring("defaults-content=from-defaults"),
				ContainSubstring("defaults-not-found"),
			))
		})

		PIt("6.6: resource source overrides defaults", func() {
			// Pending: requires live K8s cluster with concourse/mock-resource image pullable
			By("setting a pipeline where resource source overrides type defaults")
			pipelineFile := writePipelineFile("custom-type-override-defaults.yml", `
resource_types:
- name: custom-mock
  type: registry-image
  source: {repository: concourse/mock-resource}
  defaults:
    create_files:
      data.txt: "from-defaults"

resources:
- name: override-res
  type: custom-mock
  source:
    create_files:
      data.txt: "from-resource-source"

jobs:
- name: override-job
  plan:
  - get: override-res
    trigger: false
  - task: read-override
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs:
      - name: override-res
      run:
        path: cat
        args: ["override-res/data.txt"]
`)
			setAndUnpausePipeline(pipelineFile)
			newMockVersion("override-res", "v1")
			triggerJob("override-job")

			session := waitForBuildAndWatch("override-job")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("from-resource-source"))
		})

		PIt("6.7: custom type used by both get and put in same pipeline", func() {
			// Pending: requires live K8s cluster with concourse/mock-resource image pullable
			By("setting a pipeline that uses a custom type for both get and put")
			pipelineFile := writePipelineFile("custom-type-get-put.yml", `
resource_types:
- name: custom-mock
  type: registry-image
  source: {repository: concourse/mock-resource}

resources:
- name: input-res
  type: custom-mock
  source:
    create_files:
      data.txt: "custom-get-data"
- name: output-res
  type: custom-mock
  source: {}

jobs:
- name: get-put-job
  plan:
  - get: input-res
    trigger: false
  - task: process
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs:
      - name: input-res
      run:
        path: sh
        args: ["-c", "echo custom-get-put-ok && cat input-res/data.txt"]
  - put: output-res
    params:
      version: custom-v1
`)
			setAndUnpausePipeline(pipelineFile)
			newMockVersion("input-res", "v1")
			triggerJob("get-put-job")

			session := waitForBuildAndWatch("get-put-job")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("custom-get-put-ok"))
		})

		PIt("6.8: custom type check detects new versions", func() {
			// Pending: requires live K8s cluster with concourse/mock-resource image pullable
			By("setting a pipeline with a custom type resource")
			pipelineFile := writePipelineFile("custom-type-check.yml", `
resource_types:
- name: custom-mock
  type: registry-image
  source: {repository: concourse/mock-resource}

resources:
- name: checked-custom
  type: custom-mock
  source:
    create_files:
      data.txt: "checked-data"

jobs:
- name: check-custom-job
  plan:
  - get: checked-custom
    trigger: false
  - task: verify
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs:
      - name: checked-custom
      run:
        path: echo
        args: ["custom-check-ok"]
`)
			setAndUnpausePipeline(pipelineFile)

			By("injecting multiple versions")
			newMockVersion("checked-custom", "v1")
			newMockVersion("checked-custom", "v2")

			By("verifying versions are detected")
			versions := flyTable("resource-versions", "-r", inPipeline("checked-custom"))
			Expect(len(versions)).To(BeNumerically(">=", 2))

			triggerJob("check-custom-job")
			session := waitForBuildAndWatch("check-custom-job")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("custom-check-ok"))
		})

		PIt("6.9: multiple custom types coexist in one pipeline", func() {
			// Pending: requires live K8s cluster with concourse/mock-resource image pullable
			By("setting a pipeline with two distinct custom types")
			pipelineFile := writePipelineFile("custom-type-multi.yml", `
resource_types:
- name: type-alpha
  type: registry-image
  source: {repository: concourse/mock-resource}
- name: type-beta
  type: registry-image
  source: {repository: concourse/mock-resource}

resources:
- name: alpha-res
  type: type-alpha
  source:
    create_files:
      alpha.txt: "alpha-data"
- name: beta-res
  type: type-beta
  source:
    create_files:
      beta.txt: "beta-data"

jobs:
- name: multi-type-job
  plan:
  - get: alpha-res
    trigger: false
  - get: beta-res
    trigger: false
  - task: read-both
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs:
      - name: alpha-res
      - name: beta-res
      run:
        path: sh
        args:
        - -c
        - |
          echo "alpha=$(cat alpha-res/alpha.txt)"
          echo "beta=$(cat beta-res/beta.txt)"
`)
			setAndUnpausePipeline(pipelineFile)
			newMockVersion("alpha-res", "v1")
			newMockVersion("beta-res", "v1")
			triggerJob("multi-type-job")

			session := waitForBuildAndWatch("multi-type-job")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("alpha=alpha-data"))
			Expect(session.Out).To(gbytes.Say("beta=beta-data"))
		})
	})

	// -----------------------------------------------------------------
	// Custom type with produces: registry-image (6.10-6.12)
	// -----------------------------------------------------------------

	Context("custom type with produces: registry-image", func() {
		PIt("6.10: produces: registry-image short-circuits get pod creation", func() {
			// Pending: produces: registry-image E2E validation requires mock type chain resolution in K8s
			By("setting a pipeline with a produces: registry-image type")
			pipelineFile := writePipelineFile("produces-shortcircuit.yml", `
resource_types:
- name: image-type
  type: mock
  source:
    mirror_self: true
    initial_version: type-v1
  produces: registry-image

resources:
- name: prod-image
  type: image-type
  source:
    repository: busybox

jobs:
- name: produces-job
  plan:
  - get: prod-image
  - task: use-it
    image: prod-image
    config:
      platform: linux
      run:
        path: echo
        args: ["produces-shortcircuit-ok"]
`)
			setAndUnpausePipeline(pipelineFile)
			newMockVersionOrSkip("prod-image", "v1")
			triggerJob("produces-job")

			session := waitForBuildAndWatch("produces-job")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("produces-shortcircuit-ok"))

			By("verifying no get pods were created")
			pods := getPods(fmt.Sprintf(
				"concourse.ci/type=get,concourse.ci/pipeline=%s", pipelineName,
			))
			Expect(pods).To(BeEmpty(),
				"expected no get pods - produces: registry-image should short-circuit",
			)
		})

		PIt("6.11: fetch_artifact overrides the short-circuit and spawns a get pod", func() {
			// Pending: fetch_artifact override requires E2E validation with produces: registry-image
			By("setting a pipeline with fetch_artifact: true")
			pipelineFile := writePipelineFile("fetch-artifact-override.yml", `
resource_types:
- name: image-type
  type: mock
  source:
    mirror_self: true
    initial_version: type-v1
  produces: registry-image

resources:
- name: artifact-image
  type: image-type
  source:
    repository: busybox
    create_files:
      marker.txt: "artifact-fetched"

jobs:
- name: fetch-artifact-job
  plan:
  - get: artifact-image
    params:
      fetch_artifact: true
  - task: verify-artifact
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs:
      - name: artifact-image
      run:
        path: cat
        args: ["artifact-image/marker.txt"]
`)
			setAndUnpausePipeline(pipelineFile)
			newMockVersionOrSkip("artifact-image", "v1")
			triggerJob("fetch-artifact-job")

			session := waitForBuildAndWatch("fetch-artifact-job")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("artifact-fetched"))
		})

		PIt("6.12: produces: registry-image works across passed constraints", func() {
			// Pending: produces: registry-image with passed constraints requires E2E validation
			By("setting a pipeline with passed constraints and produces type")
			pipelineFile := writePipelineFile("produces-passed.yml", `
resource_types:
- name: image-type
  type: mock
  source:
    mirror_self: true
    initial_version: type-v1
  produces: registry-image

resources:
- name: pass-image
  type: image-type
  source:
    repository: busybox

jobs:
- name: upstream
  plan:
  - get: pass-image

- name: downstream
  plan:
  - get: pass-image
    passed: [upstream]
  - task: use-passed
    image: pass-image
    config:
      platform: linux
      run:
        path: echo
        args: ["produces-passed-ok"]
`)
			setAndUnpausePipeline(pipelineFile)
			newMockVersionOrSkip("pass-image", "v1")

			By("triggering upstream job first")
			triggerJob("upstream")
			session := waitForBuildAndWatch("upstream")
			Expect(session).To(gexec.Exit(0))

			By("triggering downstream job with passed constraint")
			triggerJob("downstream")
			session = waitForBuildAndWatch("downstream")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("produces-passed-ok"))
		})
	})

	// -----------------------------------------------------------------
	// Image passing same job (6.13-6.18)
	// -----------------------------------------------------------------

	Context("image passing within the same job", func() {
		PIt("6.13: get registry-image and use as task image in same job", Label("e2e"), func() {
			By("setting a pipeline that gets alpine and uses it as task image")
			pipelineFile := writePipelineFile("image-pass-same-job.yml", `
resources:
- name: task-image
  type: registry-image
  source:
    repository: alpine
    tag: "3.19"

jobs:
- name: image-pass-job
  plan:
  - get: task-image
  - task: use-image
    image: task-image
    config:
      platform: linux
      run:
        path: sh
        args: ["-c", "cat /etc/alpine-release"]
`)
			setAndUnpausePipeline(pipelineFile)

			Eventually(func() int {
				return len(flyTable("resource-versions", "-r", inPipeline("task-image")))
			}, 3*time.Minute, time.Second).Should(BeNumerically(">", 0))

			triggerJob("image-pass-job")
			session := waitForBuildAndWatch("image-pass-job")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say(`3\.19`))
		})

		PIt("6.14: get mock resource and use as task image (via produces)", func() {
			// Pending: mock-based image provider with produces: registry-image requires E2E validation
			By("setting a pipeline using a mock-based image type")
			pipelineFile := writePipelineFile("image-pass-mock.yml", `
resource_types:
- name: image-provider
  type: mock
  source:
    mirror_self: true
    initial_version: img-v1
  produces: registry-image

resources:
- name: mock-image
  type: image-provider
  source:
    repository: busybox

jobs:
- name: mock-image-job
  plan:
  - get: mock-image
  - task: use-mock-image
    image: mock-image
    config:
      platform: linux
      run:
        path: echo
        args: ["mock-image-pass-ok"]
`)
			setAndUnpausePipeline(pipelineFile)
			newMockVersionOrSkip("mock-image", "v1")
			triggerJob("mock-image-job")

			session := waitForBuildAndWatch("mock-image-job")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("mock-image-pass-ok"))
		})

		PIt("6.15: multiple tasks in same job use different images from get steps", func() {
			// Pending: multiple task images from produces: registry-image requires E2E validation
			By("setting a pipeline with two get steps providing different images")
			pipelineFile := writePipelineFile("image-pass-multi.yml", `
resource_types:
- name: image-provider
  type: mock
  source:
    mirror_self: true
    initial_version: img-v1
  produces: registry-image

resources:
- name: image-a
  type: image-provider
  source:
    repository: busybox
- name: image-b
  type: image-provider
  source:
    repository: busybox

jobs:
- name: multi-image-job
  plan:
  - get: image-a
  - get: image-b
  - task: use-image-a
    image: image-a
    config:
      platform: linux
      run:
        path: echo
        args: ["used-image-a"]
  - task: use-image-b
    image: image-b
    config:
      platform: linux
      run:
        path: echo
        args: ["used-image-b"]
`)
			setAndUnpausePipeline(pipelineFile)
			newMockVersionOrSkip("image-a", "v1")
			newMockVersionOrSkip("image-b", "v1")
			triggerJob("multi-image-job")

			session := waitForBuildAndWatch("multi-image-job")
			Expect(session).To(gexec.Exit(0))
			output := string(session.Out.Contents())
			Expect(output).To(ContainSubstring("used-image-a"))
			Expect(output).To(ContainSubstring("used-image-b"))
		})

		PIt("6.16: image from get step alongside image_resource task", func() {
			// Pending: mixed image sources (get step + image_resource) requires E2E validation
			By("setting a pipeline mixing image from get and image_resource")
			pipelineFile := writePipelineFile("image-pass-mixed.yml", `
resource_types:
- name: image-provider
  type: mock
  source:
    mirror_self: true
    initial_version: img-v1
  produces: registry-image

resources:
- name: get-image
  type: image-provider
  source:
    repository: busybox

jobs:
- name: mixed-job
  plan:
  - get: get-image
  - task: from-get
    image: get-image
    config:
      platform: linux
      run:
        path: echo
        args: ["from-get-step"]
  - task: from-image-resource
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["from-image-resource-step"]
`)
			setAndUnpausePipeline(pipelineFile)
			newMockVersionOrSkip("get-image", "v1")
			triggerJob("mixed-job")

			session := waitForBuildAndWatch("mixed-job")
			Expect(session).To(gexec.Exit(0))
			output := string(session.Out.Contents())
			Expect(output).To(ContainSubstring("from-get-step"))
			Expect(output).To(ContainSubstring("from-image-resource-step"))
		})

		PIt("6.17: task with image from get also receives other inputs", func() {
			// Pending: image-from-get with additional inputs requires E2E validation
			By("setting a pipeline where image-from-get task has additional inputs")
			pipelineFile := writePipelineFile("image-pass-with-inputs.yml", `
resource_types:
- name: image-provider
  type: mock
  source:
    mirror_self: true
    initial_version: img-v1
  produces: registry-image

resources:
- name: task-image
  type: image-provider
  source:
    repository: busybox
- name: code
  type: mock
  source:
    create_files:
      src.txt: "code-content"

jobs:
- name: image-input-job
  plan:
  - get: task-image
  - get: code
    trigger: false
  - task: use-both
    image: task-image
    config:
      platform: linux
      inputs:
      - name: code
      run:
        path: sh
        args: ["-c", "echo image-and-input=$(cat code/src.txt)"]
`)
			setAndUnpausePipeline(pipelineFile)
			newMockVersionOrSkip("task-image", "v1")
			newMockVersion("code", "v1")
			triggerJob("image-input-job")

			session := waitForBuildAndWatch("image-input-job")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("image-and-input=code-content"))
		})

		PIt("6.18: task image from get with params on the get step", func() {
			// Pending: image-from-get with params requires E2E validation
			By("setting a pipeline where the image get step has params")
			pipelineFile := writePipelineFile("image-pass-params.yml", `
resource_types:
- name: image-provider
  type: mock
  source:
    mirror_self: true
    initial_version: img-v1
  produces: registry-image

resources:
- name: param-image
  type: image-provider
  source:
    repository: busybox

jobs:
- name: param-image-job
  plan:
  - get: param-image
  - task: use-param-image
    image: param-image
    config:
      platform: linux
      run:
        path: echo
        args: ["image-with-params-ok"]
`)
			setAndUnpausePipeline(pipelineFile)
			newMockVersionOrSkip("param-image", "v1")
			triggerJob("param-image-job")

			session := waitForBuildAndWatch("param-image-job")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("image-with-params-ok"))
		})
	})

	// -----------------------------------------------------------------
	// Image passing between jobs (6.19-6.23)
	// -----------------------------------------------------------------

	Context("image passing between jobs", func() {
		PIt("6.19: build image in one job, use in another via passed constraint", func() {
			// Pending: cross-job artifact passing with mock resources requires E2E validation
			By("setting a pipeline that passes artifacts between jobs")
			pipelineFile := writePipelineFile("cross-job-image.yml", `
resources:
- name: code
  type: mock
  source:
    create_files:
      code.txt: "source-code"
- name: built-artifact
  type: mock
  source: {}

jobs:
- name: build
  plan:
  - get: code
    trigger: false
  - task: compile
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs:
      - name: code
      outputs:
      - name: built-artifact
      run:
        path: sh
        args: ["-c", "cp code/code.txt built-artifact/artifact.txt && echo build-done"]
  - put: built-artifact
    params:
      version: build-v1

- name: deploy
  plan:
  - get: built-artifact
    passed: [build]
    trigger: false
  - task: deploy-it
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs:
      - name: built-artifact
      run:
        path: sh
        args: ["-c", "echo deploy-content=$(cat built-artifact/artifact.txt)"]
`)
			setAndUnpausePipeline(pipelineFile)
			newMockVersion("code", "v1")

			By("triggering the build job")
			triggerJob("build")
			session := waitForBuildAndWatch("build")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("build-done"))

			By("triggering the deploy job")
			triggerJob("deploy")
			session = waitForBuildAndWatch("deploy")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("deploy-content=source-code"))
		})

		PIt("6.20: cross-job with auto-trigger on passed constraint", func() {
			// Pending: cross-job auto-trigger with passed constraint requires E2E validation
			By("setting a pipeline with trigger: true and passed constraint")
			pipelineFile := writePipelineFile("cross-job-trigger.yml", `
resources:
- name: src
  type: mock
  source:
    create_files:
      src.txt: "trigger-data"

jobs:
- name: upstream
  plan:
  - get: src
    trigger: false
  - task: process
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs:
      - name: src
      run:
        path: echo
        args: ["upstream-done"]

- name: downstream
  plan:
  - get: src
    passed: [upstream]
    trigger: true
  - task: consume
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs:
      - name: src
      run:
        path: sh
        args: ["-c", "echo downstream-content=$(cat src/src.txt)"]
`)
			setAndUnpausePipeline(pipelineFile)
			newMockVersion("src", "v1")

			By("triggering upstream job")
			triggerJob("upstream")
			session := waitForBuildAndWatch("upstream")
			Expect(session).To(gexec.Exit(0))

			By("waiting for downstream job to auto-trigger")
			Eventually(func() string {
				builds := flyTable("builds", "-j", inPipeline("downstream"))
				if len(builds) == 0 {
					return ""
				}
				return builds[0]["status"]
			}, 3*time.Minute, time.Second).Should(SatisfyAny(
				Equal("succeeded"),
				Equal("started"),
			))

			session = waitForBuildAndWatch("downstream")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("downstream-content=trigger-data"))
		})

		PIt("6.21: three-stage pipeline with passed constraints", func() {
			// Pending: three-stage pipeline with passed constraints requires E2E validation
			By("setting a three-stage pipeline")
			pipelineFile := writePipelineFile("three-stage.yml", `
resources:
- name: artifact
  type: mock
  source: {}

jobs:
- name: stage-1
  plan:
  - task: produce
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      outputs:
      - name: artifact
      run:
        path: sh
        args: ["-c", "echo stage-1 > artifact/data.txt"]
  - put: artifact
    params:
      version: s1-v1

- name: stage-2
  plan:
  - get: artifact
    passed: [stage-1]
    trigger: false
  - task: transform
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs:
      - name: artifact
      run:
        path: echo
        args: ["stage-2-done"]
  - put: artifact
    params:
      version: s2-v1

- name: stage-3
  plan:
  - get: artifact
    passed: [stage-2]
    trigger: false
  - task: verify
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs:
      - name: artifact
      run:
        path: echo
        args: ["stage-3-done"]
`)
			setAndUnpausePipeline(pipelineFile)

			By("running through all three stages")
			triggerJob("stage-1")
			session := waitForBuildAndWatch("stage-1")
			Expect(session).To(gexec.Exit(0))

			triggerJob("stage-2")
			session = waitForBuildAndWatch("stage-2")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("stage-2-done"))

			triggerJob("stage-3")
			session = waitForBuildAndWatch("stage-3")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("stage-3-done"))
		})

		PIt("6.22: fan-out from single resource to multiple jobs", func() {
			// Pending: fan-out pattern with mock resources requires E2E validation
			By("setting a pipeline with fan-out pattern")
			pipelineFile := writePipelineFile("fan-out.yml", `
resources:
- name: shared
  type: mock
  source:
    create_files:
      data.txt: "shared-data"

jobs:
- name: source-job
  plan:
  - get: shared
    trigger: false
  - task: emit
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs:
      - name: shared
      run:
        path: echo
        args: ["source-done"]

- name: consumer-a
  plan:
  - get: shared
    passed: [source-job]
    trigger: false
  - task: consume-a
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs:
      - name: shared
      run:
        path: echo
        args: ["consumer-a-done"]

- name: consumer-b
  plan:
  - get: shared
    passed: [source-job]
    trigger: false
  - task: consume-b
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs:
      - name: shared
      run:
        path: echo
        args: ["consumer-b-done"]
`)
			setAndUnpausePipeline(pipelineFile)
			newMockVersion("shared", "v1")

			By("running source job")
			triggerJob("source-job")
			session := waitForBuildAndWatch("source-job")
			Expect(session).To(gexec.Exit(0))

			By("running consumer-a and consumer-b")
			triggerJob("consumer-a")
			sessionA := waitForBuildAndWatch("consumer-a")
			Expect(sessionA).To(gexec.Exit(0))
			Expect(sessionA.Out).To(gbytes.Say("consumer-a-done"))

			triggerJob("consumer-b")
			sessionB := waitForBuildAndWatch("consumer-b")
			Expect(sessionB).To(gexec.Exit(0))
			Expect(sessionB.Out).To(gbytes.Say("consumer-b-done"))
		})

		PIt("6.23: fan-in from multiple jobs to single downstream", func() {
			// Pending: fan-in pattern with mock resources requires E2E validation
			By("setting a pipeline with fan-in pattern")
			pipelineFile := writePipelineFile("fan-in.yml", `
resources:
- name: res-a
  type: mock
  source:
    create_files:
      a.txt: "data-a"
- name: res-b
  type: mock
  source:
    create_files:
      b.txt: "data-b"

jobs:
- name: job-a
  plan:
  - get: res-a
    trigger: false
  - task: process-a
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs:
      - name: res-a
      run:
        path: echo
        args: ["job-a-done"]

- name: job-b
  plan:
  - get: res-b
    trigger: false
  - task: process-b
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs:
      - name: res-b
      run:
        path: echo
        args: ["job-b-done"]

- name: merge-job
  plan:
  - get: res-a
    passed: [job-a]
    trigger: false
  - get: res-b
    passed: [job-b]
    trigger: false
  - task: merge
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs:
      - name: res-a
      - name: res-b
      run:
        path: sh
        args:
        - -c
        - |
          echo "merged-a=$(cat res-a/a.txt)"
          echo "merged-b=$(cat res-b/b.txt)"
`)
			setAndUnpausePipeline(pipelineFile)
			newMockVersion("res-a", "v1")
			newMockVersion("res-b", "v1")

			By("running upstream jobs")
			triggerJob("job-a")
			Expect(waitForBuildAndWatch("job-a")).To(gexec.Exit(0))

			triggerJob("job-b")
			Expect(waitForBuildAndWatch("job-b")).To(gexec.Exit(0))

			By("running the merge job")
			triggerJob("merge-job")
			session := waitForBuildAndWatch("merge-job")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("merged-a=data-a"))
			Expect(session.Out).To(gbytes.Say("merged-b=data-b"))
		})
	})

	// -----------------------------------------------------------------
	// K8s assertions for custom types (6.24-6.27)
	// -----------------------------------------------------------------

	Context("Kubernetes pod assertions for custom types", func() {
		PIt("6.24: type chain resolution creates expected check pods", func() {
			// Pending: custom type chain pod assertions require E2E validation with live K8s cluster
			By("setting a pipeline with a custom type chain")
			pipelineFile := writePipelineFile("type-chain-pods.yml", `
resource_types:
- name: custom-mock
  type: registry-image
  source: {repository: concourse/mock-resource}

resources:
- name: custom-res
  type: custom-mock
  source:
    create_files:
      data.txt: "pod-check"

jobs:
- name: chain-pod-job
  plan:
  - get: custom-res
    trigger: false
  - task: verify
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs:
      - name: custom-res
      run:
        path: echo
        args: ["chain-pod-ok"]
`)
			setAndUnpausePipeline(pipelineFile)
			newMockVersion("custom-res", "v1")
			triggerJob("chain-pod-job")

			session := waitForBuildAndWatch("chain-pod-job")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("chain-pod-ok"))

			By("verifying pods are cleaned up after build")
			assertPodCleanupForPipeline()
		})

		PIt("6.25: direct image: field skips type check pods", func() {
			// Pending: direct image field pod assertions require E2E validation with live K8s cluster
			By("setting a pipeline with image: field on resource type")
			pipelineFile := writePipelineFile("direct-image-pods.yml", `
resource_types:
- name: direct-mock
  image: concourse/mock-resource

resources:
- name: direct-res
  type: direct-mock
  source:
    create_files:
      data.txt: "direct-pod-check"

jobs:
- name: direct-pod-job
  plan:
  - get: direct-res
    trigger: false
  - task: verify
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs:
      - name: direct-res
      run:
        path: echo
        args: ["direct-image-pod-ok"]
`)
			setAndUnpausePipeline(pipelineFile)
			newMockVersion("direct-res", "v1")
			triggerJob("direct-pod-job")

			session := waitForBuildAndWatch("direct-pod-job")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("direct-image-pod-ok"))

			By("verifying pods are cleaned up - direct image skips type check cycle")
			assertPodCleanupForPipeline()
		})

		PIt("6.26: produces: registry-image creates no get pods", func() {
			// Pending: produces: registry-image pod assertions require E2E validation
			By("setting a pipeline with produces: registry-image")
			pipelineFile := writePipelineFile("produces-no-get-pods.yml", `
resource_types:
- name: image-type
  type: mock
  source:
    mirror_self: true
    initial_version: type-v1
  produces: registry-image

resources:
- name: no-get-image
  type: image-type
  source:
    repository: busybox

jobs:
- name: no-get-job
  plan:
  - get: no-get-image
  - task: verify
    image: no-get-image
    config:
      platform: linux
      run:
        path: echo
        args: ["no-get-pod-ok"]
`)
			setAndUnpausePipeline(pipelineFile)
			newMockVersion("no-get-image", "v1")
			triggerJob("no-get-job")

			session := waitForBuildAndWatch("no-get-job")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("no-get-pod-ok"))

			By("verifying no get pods were created")
			getSelector := fmt.Sprintf(
				"concourse.ci/type=get,concourse.ci/pipeline=%s", pipelineName,
			)
			pods := getPods(getSelector)
			Expect(pods).To(BeEmpty(),
				"expected no get pods for produces: registry-image resource",
			)
		})

		PIt("6.27: all pods cleaned up after pipeline destroy", func() {
			// Pending: custom type pod cleanup after destroy requires E2E validation
			By("setting a pipeline with custom types")
			pipelineFile := writePipelineFile("custom-cleanup.yml", `
resource_types:
- name: custom-mock
  type: registry-image
  source: {repository: concourse/mock-resource}

resources:
- name: cleanup-res
  type: custom-mock
  source:
    create_files:
      data.txt: "cleanup-data"

jobs:
- name: cleanup-job
  plan:
  - get: cleanup-res
    trigger: false
  - task: verify
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs:
      - name: cleanup-res
      run:
        path: echo
        args: ["custom-cleanup-ok"]
`)
			setAndUnpausePipeline(pipelineFile)
			newMockVersion("cleanup-res", "v1")
			triggerJob("cleanup-job")

			session := waitForBuildAndWatch("cleanup-job")
			Expect(session).To(gexec.Exit(0))

			By("destroying the pipeline")
			fly.Run("destroy-pipeline", "-n", "-p", pipelineName)

			By("verifying all pods are cleaned up after destruction")
			waitForPodCleanupByPipeline()
		})
	})
})
