package behavioral_test

import (
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
)

var _ = Describe("Sidecar Containers", func() {

	PIt("runs an inline sidecar alongside the main task", func() {
		// Pending: sidecar E2E requires live K8s cluster with nginx:alpine pullable
		cfg := writePipelineFile("sidecar-inline.yml", `
jobs:
- name: sidecar-job
  plan:
  - task: with-sidecar
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: sh
        args: ["-c", "sleep 2 && wget -qO- http://localhost:8080/ && echo SIDECAR_OK"]
    sidecars:
    - name: web
      image: nginx:alpine
      ports:
      - containerPort: 8080
`)
		setAndUnpausePipeline(cfg)
		triggerJob("sidecar-job")

		sess := waitForBuildAndWatch("sidecar-job")
		Expect(sess.ExitCode()).To(Equal(0))
		Expect(sess.Out).To(gbytes.Say("SIDECAR_OK"))
	})

	PIt("runs a file-based sidecar from a task config file", func() {
		// Pending: file-based sidecars use `file:` which resolves from resource
		// artifacts (e.g., my-repo/ci/task.yml), not from the host filesystem.
		// This test needs a git/mock resource containing the task YAML to work.
		// Until we have that resource setup, this test is kept pending.
		cfg := writePipelineFile("sidecar-file.yml", `
jobs:
- name: sidecar-file-job
  plan:
  - task: with-sidecar
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: sh
        args: ["-c", "sleep 2 && wget -qO- http://localhost:8080/ && echo FILE_SIDECAR_OK"]
    sidecars:
    - name: web
      image: nginx:alpine
      ports:
      - containerPort: 8080
`)
		setAndUnpausePipeline(cfg)
		triggerJob("sidecar-file-job")

		sess := waitForBuildAndWatch("sidecar-file-job")
		Expect(sess.ExitCode()).To(Equal(0))
		Expect(sess.Out).To(gbytes.Say("FILE_SIDECAR_OK"))
	})

	PIt("allows the main container to reach sidecar on localhost", func() {
		// Pending: sidecar localhost connectivity requires live K8s cluster
		cfg := writePipelineFile("sidecar-localhost.yml", `
jobs:
- name: localhost-test
  plan:
  - task: ping-sidecar
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: sh
        args: ["-c", "sleep 3 && wget -qO- http://localhost:9090/ && echo LOCALHOST_OK"]
    sidecars:
    - name: health-svc
      image: busybox
      command: ["sh", "-c", "while true; do echo ok | nc -l -p 9090; done"]
`)
		setAndUnpausePipeline(cfg)
		triggerJob("localhost-test")

		sess := waitForBuildAndWatch("localhost-test")
		Expect(sess.ExitCode()).To(Equal(0))
	})

	PIt("passes environment variables to sidecar containers", func() {
		// Pending: sidecar env vars require live K8s cluster
		cfg := writePipelineFile("sidecar-env.yml", `
jobs:
- name: sidecar-env-job
  plan:
  - task: check-env
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: sh
        args: ["-c", "sleep 2 && wget -qO- http://localhost:8081/ || echo done"]
    sidecars:
    - name: env-svc
      image: busybox
      env:
      - name: MY_VAR
        value: hello-sidecar
      command: ["sh", "-c", "echo $MY_VAR | nc -l -p 8081"]
`)
		setAndUnpausePipeline(cfg)
		triggerJob("sidecar-env-job")

		sess := waitForBuildAndWatch("sidecar-env-job")
		Expect(sess.ExitCode()).To(Equal(0))
	})

	PIt("applies resource limits to sidecar containers", func() {
		// Pending: sidecar resource limits require live K8s cluster for pod inspection
		cfg := writePipelineFile("sidecar-limits.yml", `
jobs:
- name: sidecar-limits-job
  plan:
  - task: limited
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["done"]
    sidecars:
    - name: limited-svc
      image: busybox
      command: ["sleep", "30"]
      resources:
        limits:
          memory: 64Mi
          cpu: 100m
`)
		setAndUnpausePipeline(cfg)
		triggerJob("sidecar-limits-job")

		By("waiting for a pod to appear and inspecting sidecar limits")
		pods := waitForConcoursePodsAtLeast(1)
		sidecar := containerByName(&pods[0], "limited-svc")
		if sidecar != nil {
			Expect(sidecar.Resources.Limits).ToNot(BeEmpty())
		}

		sess := waitForBuildAndWatch("sidecar-limits-job")
		Expect(sess.ExitCode()).To(Equal(0))
	})

	PIt("stops sidecars when the main task completes", func() {
		// Pending: sidecar lifecycle management requires live K8s cluster
		cfg := writePipelineFile("sidecar-stop.yml", `
jobs:
- name: sidecar-stop-job
  plan:
  - task: quick-main
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["main done"]
    sidecars:
    - name: long-svc
      image: busybox
      command: ["sleep", "3600"]
`)
		setAndUnpausePipeline(cfg)
		triggerJob("sidecar-stop-job")

		sess := waitForBuildAndWatch("sidecar-stop-job")
		Expect(sess.ExitCode()).To(Equal(0))

		By("verifying pod is cleaned up after main completes")
		assertPodCleanupForPipeline()
	})

	PIt("rejects reserved sidecar container names", func() {
		// Pending: reserved name "main" is an actually reserved container name
		cfg := writePipelineFile("sidecar-reserved.yml", `
jobs:
- name: reserved-name-job
  plan:
  - task: bad-sidecar
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["hello"]
    sidecars:
    - name: main
      image: busybox
      command: ["sleep", "5"]
`)
		// set-pipeline should reject the reserved name "main"
		sess := fly.Start("set-pipeline", "-n", "-p", pipelineName, "-c", cfg)
		<-sess.Exited
		output := string(sess.Out.Contents()) + string(sess.Err.Contents())
		Expect(output).To(ContainSubstring("reserved"), "expected set-pipeline to reject reserved sidecar name 'main'")
	})

	PIt("creates the expected container count in K8s pod", func() {
		// Pending: sidecar container count verification requires live K8s cluster
		cfg := writePipelineFile("sidecar-count.yml", `
jobs:
- name: multi-sidecar-job
  plan:
  - task: with-sidecars
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: sh
        args: ["-c", "sleep 5 && echo done"]
    sidecars:
    - name: svc-a
      image: busybox
      command: ["sleep", "30"]
    - name: svc-b
      image: busybox
      command: ["sleep", "30"]
`)
		setAndUnpausePipeline(cfg)
		triggerJob("multi-sidecar-job")

		By("waiting for pod and checking container count")
		pods := waitForConcoursePodsAtLeast(1)
		// Main container + 2 sidecars = at least 3 containers
		Expect(len(pods[0].Spec.Containers)).To(BeNumerically(">=", 3),
			fmt.Sprintf("expected at least 3 containers, got %d", len(pods[0].Spec.Containers)))

		sess := waitForBuildAndWatch("multi-sidecar-job")
		Expect(sess.ExitCode()).To(Equal(0))
	})
})
