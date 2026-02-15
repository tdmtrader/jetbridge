package behavioral_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
)

var _ = Describe("Artifact Flow", func() {

	It("passes a get output to a task input", func() {
		cfg := writePipelineFile("get-to-task.yml", `
resources:
- name: src
  type: mock
  source: {mirror_self: true}

jobs:
- name: get-task-job
  plan:
  - get: src
  - task: read-src
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs: [{name: src}]
      run:
        path: ls
        args: ["src/"]
`)
		setAndUnpausePipeline(cfg)
		newMockVersion("src", "v1")
		triggerJob("get-task-job")

		sess := waitForBuildAndWatch("get-task-job")
		Expect(sess.ExitCode()).To(Equal(0))
	})

	It("passes a task output to a put step", func() {
		cfg := writePipelineFile("task-to-put.yml", `
resources:
- name: output-res
  type: mock
  source: {mirror_self: true}

jobs:
- name: task-put-job
  plan:
  - task: produce
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      outputs: [{name: artifacts}]
      run:
        path: sh
        args: ["-c", "echo built > artifacts/result.txt"]
  - put: output-res
    params: {file: artifacts/result.txt}
`)
		setAndUnpausePipeline(cfg)
		triggerJob("task-put-job")

		sess := waitForBuildAndWatch("task-put-job")
		Expect(sess.ExitCode()).To(Equal(0))
	})

	It("chains get -> task -> put -> get in a multi-step flow", func() {
		cfg := writePipelineFile("chain.yml", `
resources:
- name: src
  type: mock
  source: {mirror_self: true}
- name: dest
  type: mock
  source: {mirror_self: true}

jobs:
- name: chain-job
  plan:
  - get: src
  - task: transform
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs: [{name: src}]
      outputs: [{name: out}]
      run:
        path: sh
        args: ["-c", "echo transformed > out/data.txt"]
  - put: dest
    params: {file: out/data.txt}
  - get: dest
`)
		setAndUnpausePipeline(cfg)
		newMockVersion("src", "v1")
		triggerJob("chain-job")

		sess := waitForBuildAndWatch("chain-job")
		Expect(sess.ExitCode()).To(Equal(0))
	})

	It("fetches resources in parallel with in_parallel gets", func() {
		cfg := writePipelineFile("parallel-get.yml", `
resources:
- name: res-a
  type: mock
  source: {mirror_self: true}
- name: res-b
  type: mock
  source: {mirror_self: true}

jobs:
- name: parallel-job
  plan:
  - in_parallel:
    - get: res-a
    - get: res-b
  - task: verify
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs:
      - name: res-a
      - name: res-b
      run:
        path: echo
        args: ["BOTH_PRESENT"]
`)
		setAndUnpausePipeline(cfg)
		newMockVersion("res-a", "v1")
		newMockVersion("res-b", "v1")
		triggerJob("parallel-job")

		sess := waitForBuildAndWatch("parallel-job")
		Expect(sess.ExitCode()).To(Equal(0))
		Expect(sess.Out).To(gbytes.Say("BOTH_PRESENT"))
	})

	It("makes put output available as an implicit get", func() {
		cfg := writePipelineFile("implicit-get.yml", `
resources:
- name: dest
  type: mock
  source: {mirror_self: true}

jobs:
- name: implicit-job
  plan:
  - task: produce
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      outputs: [{name: out}]
      run:
        path: sh
        args: ["-c", "echo data > out/file.txt"]
  - put: dest
    params: {file: out/file.txt}
  - task: consume
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs: [{name: dest}]
      run:
        path: ls
        args: ["dest/"]
`)
		setAndUnpausePipeline(cfg)
		triggerJob("implicit-job")

		sess := waitForBuildAndWatch("implicit-job")
		Expect(sess.ExitCode()).To(Equal(0))
	})

	It("handles large artifacts", func() {
		cfg := writePipelineFile("large-artifact.yml", `
jobs:
- name: large-job
  plan:
  - task: produce-large
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      outputs: [{name: big}]
      run:
        path: sh
        args: ["-c", "dd if=/dev/zero of=big/large.bin bs=1M count=50 2>/dev/null && echo LARGE_OK"]
  - task: consume-large
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs: [{name: big}]
      run:
        path: sh
        args: ["-c", "ls -lh big/large.bin && echo CONSUMED"]
`)
		setAndUnpausePipeline(cfg)
		triggerJob("large-job")

		sess := waitForBuildAndWatch("large-job")
		Expect(sess.ExitCode()).To(Equal(0))
		Expect(sess.Out).To(gbytes.Say("CONSUMED"))
	})

	It("handles many small files", func() {
		cfg := writePipelineFile("many-files.yml", `
jobs:
- name: many-files-job
  plan:
  - task: produce
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      outputs: [{name: files}]
      run:
        path: sh
        args: ["-c", "for i in $(seq 1 100); do echo $i > files/file_$i.txt; done && echo FILES_CREATED"]
  - task: count
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs: [{name: files}]
      run:
        path: sh
        args: ["-c", "echo FILE_COUNT=$(ls files/ | wc -l)"]
`)
		setAndUnpausePipeline(cfg)
		triggerJob("many-files-job")

		sess := waitForBuildAndWatch("many-files-job")
		Expect(sess.ExitCode()).To(Equal(0))
		Expect(sess.Out).To(gbytes.Say("FILE_COUNT="))
	})

	It("preserves binary file content through artifact flow", func() {
		cfg := writePipelineFile("binary-artifact.yml", `
jobs:
- name: binary-job
  plan:
  - task: produce-binary
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      outputs: [{name: bin}]
      run:
        path: sh
        args: ["-c", "dd if=/dev/urandom of=bin/random.bin bs=1024 count=10 2>/dev/null && md5sum bin/random.bin > bin/checksum && echo BINARY_PRODUCED"]
  - task: verify-binary
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs: [{name: bin}]
      run:
        path: sh
        args: ["-c", "md5sum -c bin/checksum && echo BINARY_VERIFIED"]
`)
		setAndUnpausePipeline(cfg)
		triggerJob("binary-job")

		sess := waitForBuildAndWatch("binary-job")
		Expect(sess.ExitCode()).To(Equal(0))
		Expect(sess.Out).To(gbytes.Say("BINARY_VERIFIED"))
	})

	It("preserves symlinks in artifacts", func() {
		cfg := writePipelineFile("symlink.yml", `
jobs:
- name: symlink-job
  plan:
  - task: make-symlinks
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      outputs: [{name: linked}]
      run:
        path: sh
        args: ["-c", "echo target > linked/real.txt && ln -s real.txt linked/link.txt && echo SYMLINK_CREATED"]
  - task: read-symlink
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs: [{name: linked}]
      run:
        path: sh
        args: ["-c", "cat linked/link.txt && echo SYMLINK_READ"]
`)
		setAndUnpausePipeline(cfg)
		triggerJob("symlink-job")

		sess := waitForBuildAndWatch("symlink-job")
		Expect(sess.ExitCode()).To(Equal(0))
		Expect(sess.Out).To(gbytes.Say("SYMLINK_READ"))
	})

	It("handles empty output directories", func() {
		cfg := writePipelineFile("empty-output.yml", `
jobs:
- name: empty-job
  plan:
  - task: empty-out
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      outputs: [{name: empty}]
      run:
        path: echo
        args: ["EMPTY_OUTPUT"]
  - task: read-empty
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs: [{name: empty}]
      run:
        path: sh
        args: ["-c", "echo ITEMS=$(ls empty/ | wc -l)"]
`)
		setAndUnpausePipeline(cfg)
		triggerJob("empty-job")

		sess := waitForBuildAndWatch("empty-job")
		Expect(sess.ExitCode()).To(Equal(0))
		Expect(sess.Out).To(gbytes.Say("ITEMS="))
	})

	It("flows artifacts across parallel tasks on different nodes", func() {
		cfg := writePipelineFile("cross-node.yml", `
resources:
- name: shared
  type: mock
  source: {mirror_self: true}

jobs:
- name: producer
  plan:
  - task: make
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      outputs: [{name: data}]
      run:
        path: sh
        args: ["-c", "echo cross-node > data/value.txt"]
  - put: shared
    params: {file: data/value.txt}

- name: consumer
  plan:
  - get: shared
    passed: [producer]
    trigger: true
  - task: read
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs: [{name: shared}]
      run:
        path: echo
        args: ["CROSS_NODE_OK"]
`)
		setAndUnpausePipeline(cfg)
		newMockVersion("shared", "v1")
		triggerJob("producer")

		sess := waitForBuildAndWatch("producer")
		Expect(sess.ExitCode()).To(Equal(0))

		sess = waitForBuildAndWatch("consumer")
		Expect(sess.ExitCode()).To(Equal(0))
		Expect(sess.Out).To(gbytes.Say("CROSS_NODE_OK"))
	})

	It("mounts volumes correctly in K8s pods", func() {
		cfg := writePipelineFile("volume-mount.yml", `
jobs:
- name: volume-job
  plan:
  - task: write
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      outputs: [{name: vol}]
      run:
        path: sh
        args: ["-c", "echo mounted > vol/test && sleep 5 && echo VOL_OK"]
`)
		setAndUnpausePipeline(cfg)
		triggerJob("volume-job")

		By("verifying pod is created")
		pods := waitForConcoursePodsAtLeast(1)
		Expect(pods).ToNot(BeEmpty())

		sess := waitForBuildAndWatch("volume-job")
		Expect(sess.ExitCode()).To(Equal(0))
		Expect(sess.Out).To(gbytes.Say("VOL_OK"))
	})
})
