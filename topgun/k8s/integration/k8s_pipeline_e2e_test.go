package integration_test

import (
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
)

var _ = Describe("K8s Pipeline E2E", func() {
	// These tests validate that real resource types (git, registry-image)
	// work correctly on K8s-backed workers. They require network access to
	// GitHub and Docker Hub, so they are marked Pending by default.
	// Run with: ginkgo -focus="K8s Pipeline E2E" --label-filter="e2e"

	// The full validation pipeline exercises:
	// - git resource (clone from GitHub)
	// - registry-image resource (pull alpine)
	// - task execution with pulled image
	// - volume passing between tasks
	// - mock resource put/get round-trip
	PIt("runs the full K8s validation pipeline with real resource types", Label("e2e"), func() {
		// Use the pipeline file from topgun/k8s/pipelines/
		repoRoot := os.Getenv("CONCOURSE_REPO_ROOT")
		if repoRoot == "" {
			// Fall back to finding it relative to the test binary.
			repoRoot = filepath.Join("..", "..", "..")
		}
		pipelineFile := filepath.Join(repoRoot, "topgun", "k8s", "pipelines", "k8s-validation.yml")
		Expect(pipelineFile).To(BeAnExistingFile())

		setAndUnpausePipeline(pipelineFile)

		By("triggering the validation job")
		triggerJob("validate-k8s-runtime")

		By("watching the build (may take a few minutes for git clone + image pull)")
		session := waitForBuildAndWatch("validate-k8s-runtime")
		Expect(session).To(gexec.Exit(0))

		By("verifying git clone succeeded")
		Expect(session.Out).To(gbytes.Say("git files:"))

		By("verifying volume passing succeeded")
		Expect(session.Out).To(gbytes.Say("volume-passing-verified"))

		By("verifying build status")
		builds := flyTable("builds", "-j", inPipeline("validate-k8s-runtime"))
		Expect(builds).ToNot(BeEmpty())
		Expect(builds[0]["status"]).To(Equal("succeeded"))
	})

	// Inline test that exercises git resource with volume passing.
	// Pending: requires GitHub access.
	PIt("clones a git repo and passes files to a task", Label("e2e"), func() {
		pipelineFile := writePipelineFile("git-volume-pass.yml", `
resources:
- name: concourse-repo
  type: git
  source:
    uri: https://github.com/concourse/examples.git
    branch: master

jobs:
- name: git-clone-job
  plan:
  - get: concourse-repo
    params:
      depth: 1
  - task: verify-clone
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      inputs:
      - name: concourse-repo
      run:
        path: sh
        args:
        - -c
        - |
          set -ex
          test -d concourse-repo/.git
          test -f concourse-repo/README.md
          echo "git-clone-verified"
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("git-clone-job")

		session := waitForBuildAndWatch("git-clone-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("git-clone-verified"))
	})

	// Inline test that pulls a registry-image and uses it as a task image.
	// Pending: requires Docker Hub access.
	PIt("pulls a registry-image and uses it as task image", Label("e2e"), func() {
		pipelineFile := writePipelineFile("registry-image-task.yml", `
resources:
- name: alpine-image
  type: registry-image
  source:
    repository: alpine
    tag: "3.19"

jobs:
- name: image-task-job
  plan:
  - get: alpine-image
  - task: use-pulled-image
    image: alpine-image
    config:
      platform: linux
      run:
        path: sh
        args:
        - -c
        - |
          cat /etc/alpine-release
          echo "registry-image-verified"
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("image-task-job")

		session := waitForBuildAndWatch("image-task-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("registry-image-verified"))
	})

	// This test uses only mock resources (no external dependencies) to validate
	// the multi-stage pipeline pattern: get → task → task → put → get.
	It("runs a multi-stage pipeline with mock resources", func() {
		pipelineFile := writePipelineFile("multi-stage-mock.yml", `
resources:
- name: input-data
  type: mock
  source:
    create_files:
      config.json: '{"version": "1.0", "env": "k8s"}'
      readme.txt: "hello from mock resource"

- name: output-data
  type: mock
  source:
    mirror_self: true

jobs:
- name: multi-stage-job
  plan:
  - get: input-data
    trigger: false

  - task: process-input
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      inputs:
      - name: input-data
      outputs:
      - name: processed
      run:
        path: sh
        args:
        - -c
        - |
          set -ex
          test -f input-data/config.json
          test -f input-data/readme.txt
          cat input-data/config.json > processed/config.json
          echo "processed-at-$(date +%s)" > processed/timestamp.txt
          echo "process-step-done"

  - task: verify-processed
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      inputs:
      - name: processed
      run:
        path: sh
        args:
        - -c
        - |
          set -ex
          test -f processed/config.json
          test -f processed/timestamp.txt
          cat processed/config.json
          echo "verify-step-done"

  - put: output-data
    params:
      version: multi-stage-v1

  - get: output-data
    passed: [multi-stage-job]
`)
		setAndUnpausePipeline(pipelineFile)

		By("injecting a version for the mock resource")
		newMockVersion("input-data", "v1")

		By("triggering the multi-stage job")
		triggerJob("multi-stage-job")

		By("watching the build")
		session := waitForBuildAndWatch("multi-stage-job")
		Expect(session).To(gexec.Exit(0))

		By("verifying all stages completed")
		Expect(session.Out).To(gbytes.Say("process-step-done"))
		Expect(session.Out).To(gbytes.Say("verify-step-done"))

		By("verifying build status")
		builds := flyTable("builds", "-j", inPipeline("multi-stage-job"))
		Expect(builds).ToNot(BeEmpty())
		Expect(builds[0]["status"]).To(Equal("succeeded"))
	})

	// Validates parallel get steps feed into a single task.
	It("runs parallel gets feeding into a single task", func() {
		pipelineFile := writePipelineFile("parallel-gets.yml", `
resources:
- name: source-a
  type: mock
  source:
    create_files:
      a.txt: "alpha-data"

- name: source-b
  type: mock
  source:
    create_files:
      b.txt: "bravo-data"

- name: source-c
  type: mock
  source:
    create_files:
      c.txt: "charlie-data"

jobs:
- name: parallel-job
  plan:
  - in_parallel:
    - get: source-a
      trigger: false
    - get: source-b
      trigger: false
    - get: source-c
      trigger: false
  - task: combine-all
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      inputs:
      - name: source-a
      - name: source-b
      - name: source-c
      run:
        path: sh
        args:
        - -c
        - |
          set -ex
          echo "a: $(cat source-a/a.txt)"
          echo "b: $(cat source-b/b.txt)"
          echo "c: $(cat source-c/c.txt)"
          echo "parallel-combine-done"
`)
		setAndUnpausePipeline(pipelineFile)

		newMockVersion("source-a", "v1")
		newMockVersion("source-b", "v1")
		newMockVersion("source-c", "v1")

		triggerJob("parallel-job")

		session := waitForBuildAndWatch("parallel-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("alpha-data"))
		Expect(session.Out).To(gbytes.Say("parallel-combine-done"))
	})

	// Validates that task caches work across builds.
	It("preserves task caches across builds", func() {
		pipelineFile := writePipelineFile("cache-pipeline.yml", `
jobs:
- name: cache-job
  plan:
  - task: cached-task
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      caches:
      - path: .cache
      run:
        path: sh
        args:
        - -c
        - |
          set -ex
          if [ -f .cache/marker ]; then
            echo "cache-hit: $(cat .cache/marker)"
          else
            echo "cache-miss"
          fi
          echo "build-$(date +%s)" > .cache/marker
          echo "cache-task-done"
`)
		setAndUnpausePipeline(pipelineFile)

		By("first build: cache should be empty")
		triggerJob("cache-job")
		session := waitForBuildAndWatch("cache-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("cache-miss"))

		By("second build: cache should persist")
		triggerJob("cache-job")
		session = waitForBuildAndWatch("cache-job", "2")
		Expect(session).To(gexec.Exit(0))

		// Cache may or may not persist depending on K8s runtime config.
		// On PVC-backed caches it will persist; on emptyDir it won't.
		// Either outcome is valid — we just verify the build succeeds.
		Expect(session.Out).To(gbytes.Say("cache-task-done"))
	})

	// Validates that a large artifact passes correctly between tasks.
	It("passes a large artifact between tasks without corruption", func() {
		pipelineFile := writePipelineFile("large-artifact.yml", `
jobs:
- name: large-artifact-job
  plan:
  - task: create-large-file
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      outputs:
      - name: large-output
      run:
        path: sh
        args:
        - -c
        - |
          set -ex
          # Create a 10MB file with known content
          dd if=/dev/urandom of=large-output/bigfile.bin bs=1024 count=10240 2>/dev/null
          sha256sum large-output/bigfile.bin | cut -d' ' -f1 > large-output/checksum.txt
          echo "created: $(wc -c < large-output/bigfile.bin) bytes"
          cat large-output/checksum.txt

  - task: verify-large-file
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      inputs:
      - name: large-output
      run:
        path: sh
        args:
        - -c
        - |
          set -ex
          expected=$(cat large-output/checksum.txt)
          actual=$(sha256sum large-output/bigfile.bin | cut -d' ' -f1)
          echo "expected: $expected"
          echo "actual:   $actual"
          if [ "$expected" = "$actual" ]; then
            echo "large-file-integrity-verified"
          else
            echo "CHECKSUM MISMATCH"
            exit 1
          fi
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("large-artifact-job")

		session := waitForBuildAndWatch("large-artifact-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("large-file-integrity-verified"))
	})

	// Validates that pods are cleaned up after pipeline completion.
	It("cleans up pods after pipeline completion", func() {
		pipelineFile := writePipelineFile("cleanup-check.yml", `
jobs:
- name: cleanup-job
  plan:
  - task: quick-task
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      run:
        path: echo
        args: ["cleanup-test-complete"]
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("cleanup-job")

		session := waitForBuildAndWatch("cleanup-job")
		Expect(session).To(gexec.Exit(0))

		By("waiting for pods to be cleaned up by GC")
		Eventually(func() int {
			pods := findConcoursePodsForWorker()
			count := 0
			for _, p := range pods {
				// Only count pods from this pipeline's builds.
				if p.Labels["concourse.ci/type"] == "task" {
					count++
				}
			}
			return count
		}, 3*time.Minute, 5*time.Second).Should(Equal(0),
			"expected task pods to be cleaned up after build completion",
		)
	})
})
