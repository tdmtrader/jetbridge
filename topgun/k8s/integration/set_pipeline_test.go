package integration_test

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
)

var _ = Describe("Set Pipeline Step", func() {
	It("creates a new pipeline from task output", func() {
		// The set_pipeline step will create a pipeline named after the
		// current pipeline plus a suffix to avoid collision.
		childPipeline := pipelineName + "-child"

		pipelineFile := writePipelineFile("set-pipeline-create.yml", fmt.Sprintf(`
jobs:
- name: set-pipeline-job
  plan:
  - task: generate-pipeline
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      outputs:
      - name: pipeline-config
      run:
        path: sh
        args:
        - -c
        - |
          cat > pipeline-config/pipeline.yml <<'PIPEEOF'
          jobs:
          - name: child-job
            plan:
            - task: child-task
              config:
                platform: linux
                rootfs_uri: docker:///busybox
                run:
                  path: echo
                  args: ["hello-from-child"]
          PIPEEOF
          echo "pipeline-generated"
  - set_pipeline: %s
    file: pipeline-config/pipeline.yml
`, childPipeline))

		setAndUnpausePipeline(pipelineFile)
		triggerJob("set-pipeline-job")

		session := waitForBuildAndWatch("set-pipeline-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("pipeline-generated"))

		By("verifying the child pipeline was created")
		Eventually(func() bool {
			pipelines := flyTable("pipelines")
			for _, p := range pipelines {
				if p["name"] == childPipeline {
					return true
				}
			}
			return false
		}, 1*time.Minute, 2*time.Second).Should(BeTrue(),
			fmt.Sprintf("expected pipeline %q to exist", childPipeline),
		)

		By("cleaning up the child pipeline")
		sess := fly.Start("destroy-pipeline", "-n", "-p", childPipeline)
		<-sess.Exited
	})

	It("updates an existing pipeline with a new job", func() {
		childPipeline := pipelineName + "-update"

		By("creating the initial child pipeline")
		initialFile := writePipelineFile("child-initial.yml", `
jobs:
- name: original-job
  plan:
  - task: original-task
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      run:
        path: echo
        args: ["original"]
`)
		fly.Run("set-pipeline", "-n", "-p", childPipeline, "-c", initialFile)

		By("running set_pipeline to update it")
		pipelineFile := writePipelineFile("set-pipeline-update.yml", fmt.Sprintf(`
jobs:
- name: update-pipeline-job
  plan:
  - task: generate-updated
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      outputs:
      - name: pipeline-config
      run:
        path: sh
        args:
        - -c
        - |
          cat > pipeline-config/pipeline.yml <<'PIPEEOF'
          jobs:
          - name: original-job
            plan:
            - task: original-task
              config:
                platform: linux
                rootfs_uri: docker:///busybox
                run:
                  path: echo
                  args: ["original"]
          - name: added-job
            plan:
            - task: added-task
              config:
                platform: linux
                rootfs_uri: docker:///busybox
                run:
                  path: echo
                  args: ["added"]
          PIPEEOF
          echo "updated-pipeline-generated"
  - set_pipeline: %s
    file: pipeline-config/pipeline.yml
`, childPipeline))

		setAndUnpausePipeline(pipelineFile)
		triggerJob("update-pipeline-job")

		session := waitForBuildAndWatch("update-pipeline-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("updated-pipeline-generated"))

		By("verifying the new job exists in the updated pipeline")
		Eventually(func() bool {
			jobs := flyTable("jobs", "-p", childPipeline)
			for _, j := range jobs {
				if j["name"] == "added-job" {
					return true
				}
			}
			return false
		}, 1*time.Minute, 2*time.Second).Should(BeTrue(),
			"expected added-job to exist in updated pipeline",
		)

		By("cleaning up the child pipeline")
		sess := fly.Start("destroy-pipeline", "-n", "-p", childPipeline)
		<-sess.Exited
	})

	It("interpolates variables from load_var into set_pipeline", func() {
		childPipeline := pipelineName + "-vars"

		pipelineFile := writePipelineFile("set-pipeline-vars.yml", fmt.Sprintf(`
jobs:
- name: set-with-vars-job
  plan:
  - task: produce-var
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      outputs:
      - name: vals
      run:
        path: sh
        args:
        - -c
        - echo -n "dynamic-greeting" > vals/msg.txt
  - load_var: greeting
    file: vals/msg.txt
  - task: generate-pipeline
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      outputs:
      - name: pipeline-config
      run:
        path: sh
        args:
        - -c
        - |
          cat > pipeline-config/pipeline.yml <<'PIPEEOF'
          jobs:
          - name: greet-job
            plan:
            - task: greet
              config:
                platform: linux
                rootfs_uri: docker:///busybox
                params:
                  MSG: ((msg))
                run:
                  path: sh
                  args: ["-c", "echo greeting=${MSG}"]
          PIPEEOF
          echo "var-pipeline-generated"
  - set_pipeline: %s
    file: pipeline-config/pipeline.yml
    vars:
      msg: ((.:greeting))
`, childPipeline))

		setAndUnpausePipeline(pipelineFile)
		triggerJob("set-with-vars-job")

		session := waitForBuildAndWatch("set-with-vars-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("var-pipeline-generated"))

		By("verifying the child pipeline was created")
		Eventually(func() bool {
			pipelines := flyTable("pipelines")
			for _, p := range pipelines {
				if p["name"] == childPipeline {
					return true
				}
			}
			return false
		}, 1*time.Minute, 2*time.Second).Should(BeTrue())

		By("cleaning up the child pipeline")
		sess := fly.Start("destroy-pipeline", "-n", "-p", childPipeline)
		<-sess.Exited
	})
})
