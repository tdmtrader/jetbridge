package behavioral_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
)

var _ = Describe("Caching and Volume Management", func() {

	It("persists caches across builds of the same job", func() {
		cfg := writePipelineFile("cache.yml", `
jobs:
- name: cached-job
  plan:
  - task: use-cache
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      caches:
      - path: my-cache
      run:
        path: sh
        args: ["-c", "if [ -f my-cache/marker ]; then echo 'CACHE HIT'; else echo 'CACHE MISS' && echo cached > my-cache/marker; fi"]
`)
		setAndUnpausePipeline(cfg)

		By("first build: cache miss")
		triggerJob("cached-job")
		sess := waitForBuildAndWatch("cached-job", "1")
		Expect(sess.ExitCode()).To(Equal(0))
		Expect(sess.Out).To(gbytes.Say("CACHE MISS"))

		By("second build: cache hit")
		triggerJob("cached-job")
		sess = waitForBuildAndWatch("cached-job", "2")
		Expect(sess.ExitCode()).To(Equal(0))
		Expect(sess.Out).To(gbytes.Say("CACHE HIT"))
	})

	It("scopes caches to the task within the job", func() {
		cfg := writePipelineFile("cache-scope.yml", `
jobs:
- name: scope-a
  plan:
  - task: use-cache
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      caches:
      - path: my-cache
      run:
        path: sh
        args: ["-c", "echo scope-a > my-cache/who && cat my-cache/who"]
- name: scope-b
  plan:
  - task: use-cache
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      caches:
      - path: my-cache
      run:
        path: sh
        args: ["-c", "if [ -f my-cache/who ]; then cat my-cache/who; else echo 'NO CACHE'; fi"]
`)
		setAndUnpausePipeline(cfg)

		By("running job A to populate its cache")
		triggerJob("scope-a")
		sess := waitForBuildAndWatch("scope-a")
		Expect(sess.ExitCode()).To(Equal(0))

		By("running job B which should have its own separate cache")
		triggerJob("scope-b")
		sess = waitForBuildAndWatch("scope-b")
		Expect(sess.ExitCode()).To(Equal(0))
		output := string(sess.Out.Contents())
		// scope-b should NOT see scope-a's cache
		Expect(output).To(SatisfyAny(
			ContainSubstring("NO CACHE"),
			ContainSubstring("scope-b"),
		))
	})

	It("clears task cache via fly clear-task-cache", func() {
		cfg := writePipelineFile("clear-cache.yml", `
jobs:
- name: clear-job
  plan:
  - task: cached-task
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      caches:
      - path: my-cache
      run:
        path: sh
        args: ["-c", "if [ -f my-cache/marker ]; then echo 'CACHE HIT'; else echo 'CACHE MISS' && echo x > my-cache/marker; fi"]
`)
		setAndUnpausePipeline(cfg)

		By("first build: populate cache")
		triggerJob("clear-job")
		sess := waitForBuildAndWatch("clear-job", "1")
		Expect(sess.ExitCode()).To(Equal(0))
		Expect(sess.Out).To(gbytes.Say("CACHE MISS"))

		By("clearing the task cache")
		// -n skips interactive confirmation (stdin is /dev/null in test context,
		// so without -n the command silently exits without clearing).
		fly.Run("clear-task-cache", "-n", "-j", inPipeline("clear-job"), "-s", "cached-task")

		By("second build: cache should be cleared")
		triggerJob("clear-job")
		sess = waitForBuildAndWatch("clear-job", "2")
		Expect(sess.ExitCode()).To(Equal(0))
		Expect(sess.Out).To(gbytes.Say("CACHE MISS"))
	})

	It("supports PVC-backed caches in K8s", func() {
		cfg := writePipelineFile("pvc-cache.yml", `
jobs:
- name: pvc-job
  plan:
  - task: with-cache
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      caches:
      - path: persistent-cache
      run:
        path: sh
        args: ["-c", "echo data > persistent-cache/file && ls persistent-cache/"]
`)
		setAndUnpausePipeline(cfg)
		triggerJob("pvc-job")

		sess := waitForBuildAndWatch("pvc-job")
		Expect(sess.ExitCode()).To(Equal(0))
		Expect(sess.Out).To(gbytes.Say("file"))
	})

	It("verifies K8s volume mounts for cached paths", func() {
		cfg := writePipelineFile("cache-k8s.yml", `
jobs:
- name: cache-vol-job
  plan:
  - task: cached
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      caches:
      - path: vol-cache
      run:
        path: sh
        args: ["-c", "echo vol-ok > vol-cache/test && sleep 5 && cat vol-cache/test"]
`)
		setAndUnpausePipeline(cfg)
		triggerJob("cache-vol-job")

		By("checking that a pod is created for the cached task")
		pods := waitForConcoursePodsAtLeast(1)
		Expect(pods).ToNot(BeEmpty())

		sess := waitForBuildAndWatch("cache-vol-job")
		Expect(sess.ExitCode()).To(Equal(0))
		Expect(sess.Out).To(gbytes.Say("vol-ok"))
	})
})
