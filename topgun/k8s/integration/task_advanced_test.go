package integration_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
)

var _ = Describe("Task Advanced", func() {
	It("runs a privileged task", func() {
		pipelineFile := writePipelineFile("privileged-task.yml", `
jobs:
- name: priv-job
  plan:
  - task: privileged-task
    privileged: true
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      run:
        path: sh
        args:
        - -c
        - |
          echo "uid=$(id -u)"
          echo "privileged-task-done"
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("priv-job")

		session := waitForBuildAndWatch("priv-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("privileged-task-done"))
	})

	It("handles optional inputs that are missing", func() {
		pipelineFile := writePipelineFile("optional-input.yml", `
jobs:
- name: optional-input-job
  plan:
  - task: uses-optional
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      inputs:
      - name: required-output
        optional: true
      run:
        path: sh
        args:
        - -c
        - |
          if [ -d required-output ]; then
            echo "optional-input-present"
          else
            echo "optional-input-missing"
          fi
          echo "optional-input-done"
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("optional-input-job")

		session := waitForBuildAndWatch("optional-input-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("optional-input-done"))
	})

	It("loads task config from a file in an artifact", func() {
		pipelineFile := writePipelineFile("task-file.yml", `
resources:
- name: task-source
  type: mock
  source:
    create_files:
      tasks/my-task.yml: |
        platform: linux
        rootfs_uri: docker:///busybox
        run:
          path: echo
          args: ["task-from-file-done"]

jobs:
- name: task-file-job
  plan:
  - get: task-source
    trigger: false
  - task: from-file
    file: task-source/tasks/my-task.yml
`)
		setAndUnpausePipeline(pipelineFile)
		newMockVersion("task-source", "v1")
		triggerJob("task-file-job")

		session := waitForBuildAndWatch("task-file-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("task-from-file-done"))
	})

	It("overrides task params from the pipeline step", func() {
		pipelineFile := writePipelineFile("task-params-override.yml", `
resources:
- name: task-res
  type: mock
  source:
    create_files:
      tasks/parameterized.yml: |
        platform: linux
        rootfs_uri: docker:///busybox
        params:
          GREETING: default-greeting
          TARGET: default-target
        run:
          path: sh
          args: ["-c", "echo ${GREETING} to ${TARGET}"]

jobs:
- name: params-override-job
  plan:
  - get: task-res
    trigger: false
  - task: parameterized
    file: task-res/tasks/parameterized.yml
    params:
      GREETING: overridden-hello
      TARGET: overridden-world
`)
		setAndUnpausePipeline(pipelineFile)
		newMockVersion("task-res", "v1")
		triggerJob("params-override-job")

		session := waitForBuildAndWatch("params-override-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("overridden-hello to overridden-world"))
	})

	It("runs a task with custom working directory via run.user", func() {
		pipelineFile := writePipelineFile("task-user.yml", `
jobs:
- name: user-job
  plan:
  - task: user-task
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      run:
        path: sh
        args:
        - -c
        - echo "running-as=$(whoami)"
        user: root
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("user-job")

		session := waitForBuildAndWatch("user-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("running-as=root"))
	})

	It("uses custom output paths", func() {
		pipelineFile := writePipelineFile("output-path.yml", `
jobs:
- name: output-path-job
  plan:
  - task: producer
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      outputs:
      - name: my-output
        path: custom/output/dir
      run:
        path: sh
        args:
        - -c
        - echo "custom-path-data" > custom/output/dir/data.txt
  - task: consumer
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      inputs:
      - name: my-output
      run:
        path: sh
        args:
        - -c
        - echo "output-path-content=$(cat my-output/data.txt)"
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("output-path-job")

		session := waitForBuildAndWatch("output-path-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("output-path-content=custom-path-data"))
	})

	It("uses custom input paths", func() {
		pipelineFile := writePipelineFile("input-path.yml", `
jobs:
- name: input-path-job
  plan:
  - task: producer
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      outputs:
      - name: source-data
      run:
        path: sh
        args:
        - -c
        - echo "input-path-content" > source-data/file.txt
  - task: consumer
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      inputs:
      - name: source-data
        path: custom/input/path
      run:
        path: sh
        args:
        - -c
        - echo "read-from-path=$(cat custom/input/path/file.txt)"
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("input-path-job")

		session := waitForBuildAndWatch("input-path-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("read-from-path=input-path-content"))
	})

	It("preserves multiple caches across builds", func() {
		pipelineFile := writePipelineFile("multi-cache.yml", `
jobs:
- name: multi-cache-job
  plan:
  - task: cached-task
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      caches:
      - path: cache-a
      - path: cache-b
      run:
        path: sh
        args:
        - -c
        - |
          if [ -f cache-a/marker ] && [ -f cache-b/marker ]; then
            echo "multi-cache-hit: a=$(cat cache-a/marker) b=$(cat cache-b/marker)"
          else
            echo "multi-cache-miss"
          fi
          echo "build-a" > cache-a/marker
          echo "build-b" > cache-b/marker
          echo "multi-cache-done"
`)
		setAndUnpausePipeline(pipelineFile)

		By("first build: cache miss")
		triggerJob("multi-cache-job")
		session := waitForBuildAndWatch("multi-cache-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("multi-cache-done"))

		By("second build: may hit cache")
		triggerJob("multi-cache-job")
		session = waitForBuildAndWatch("multi-cache-job", "2")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("multi-cache-done"))
	})

	It("handles task with both input and output of the same name", func() {
		pipelineFile := writePipelineFile("input-output-same.yml", `
jobs:
- name: same-name-job
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
        - echo "original" > shared/data.txt
  - task: modifier
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      inputs:
      - name: shared
      outputs:
      - name: shared
      run:
        path: sh
        args:
        - -c
        - |
          original=$(cat shared/data.txt)
          echo "modified-${original}" > shared/data.txt
  - task: verifier
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      inputs:
      - name: shared
      run:
        path: sh
        args:
        - -c
        - echo "same-name-result=$(cat shared/data.txt)"
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("same-name-job")

		session := waitForBuildAndWatch("same-name-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("same-name-result=modified-original"))
	})

	It("applies container_limits with both cpu and memory", func() {
		pipelineFile := writePipelineFile("container-limits.yml", `
jobs:
- name: limits-job
  plan:
  - task: limited-task
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      container_limits:
        cpu: 512
        memory: 268435456
      run:
        path: echo
        args: ["container-limits-done"]
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("limits-job")

		session := waitForBuildAndWatch("limits-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("container-limits-done"))
	})

	It("handles empty output artifact gracefully", func() {
		pipelineFile := writePipelineFile("empty-output.yml", `
jobs:
- name: empty-output-job
  plan:
  - task: produce-empty
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      outputs:
      - name: empty-out
      run:
        path: echo
        args: ["producing-nothing-in-output"]
  - task: consume-empty
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      inputs:
      - name: empty-out
      run:
        path: sh
        args:
        - -c
        - |
          count=$(ls -1 empty-out/ | wc -l)
          echo "empty-output-files=${count}"
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("empty-output-job")

		session := waitForBuildAndWatch("empty-output-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("empty-output-files="))
	})
})
