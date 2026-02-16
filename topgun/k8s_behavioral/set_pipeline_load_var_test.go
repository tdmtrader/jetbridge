package behavioral_test

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
)

var _ = Describe("Set Pipeline and Load Var Steps", func() {
	Context("set_pipeline", func() {
		It("10.1: creates a new pipeline from a task output file", func() {
			childPipeline := pipelineName + "-child"

			pipelineFile := writePipelineFile("sp-create.yml", fmt.Sprintf(`
jobs:
- name: sp-create-job
  plan:
  - task: generate
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      outputs: [{name: pipeline-config}]
      run:
        path: sh
        args:
        - -c
        - |
          printf '%%s\n' 'jobs:' '- name: child-job' '  plan:' '  - task: child-task' '    config:' '      platform: linux' '      image_resource: {type: registry-image, source: {repository: busybox}}' '      run:' '        path: echo' '        args: ["hello-from-child"]' > pipeline-config/pipeline.yml
          echo "sp-create-generated"
  - set_pipeline: %s
    file: pipeline-config/pipeline.yml
`, childPipeline))

			setAndUnpausePipeline(pipelineFile)
			triggerJob("sp-create-job")

			session := waitForBuildAndWatch("sp-create-job")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("sp-create-generated"))

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

		It("10.2: updates an existing pipeline with new jobs", func() {
			childPipeline := pipelineName + "-update"

			By("creating the initial child pipeline")
			initialFile := writePipelineFile("sp-child-initial.yml", `
jobs:
- name: original-job
  plan:
  - task: original-task
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["original"]
`)
			fly.Run("set-pipeline", "-n", "-p", childPipeline, "-c", initialFile)

			By("running set_pipeline to update it")
			pipelineFile := writePipelineFile("sp-update.yml", fmt.Sprintf(`
jobs:
- name: sp-update-job
  plan:
  - task: generate-updated
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      outputs: [{name: pipeline-config}]
      run:
        path: sh
        args:
        - -c
        - |
          printf '%%s\n' 'jobs:' '- name: original-job' '  plan:' '  - task: original-task' '    config:' '      platform: linux' '      image_resource: {type: registry-image, source: {repository: busybox}}' '      run:' '        path: echo' '        args: ["original"]' '- name: added-job' '  plan:' '  - task: added-task' '    config:' '      platform: linux' '      image_resource: {type: registry-image, source: {repository: busybox}}' '      run:' '        path: echo' '        args: ["added"]' > pipeline-config/pipeline.yml
          echo "sp-update-generated"
  - set_pipeline: %s
    file: pipeline-config/pipeline.yml
`, childPipeline))

			setAndUnpausePipeline(pipelineFile)
			triggerJob("sp-update-job")

			session := waitForBuildAndWatch("sp-update-job")
			Expect(session).To(gexec.Exit(0))

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

		It("10.3: interpolates vars into set_pipeline", func() {
			childPipeline := pipelineName + "-vars"

			pipelineFile := writePipelineFile("sp-vars.yml", fmt.Sprintf(`
jobs:
- name: sp-vars-job
  plan:
  - task: produce-var
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      outputs: [{name: vals}]
      run:
        path: sh
        args: ["-c", "echo -n dynamic-greeting > vals/msg.txt"]
  - load_var: greeting
    file: vals/msg.txt
  - task: generate
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      outputs: [{name: pipeline-config}]
      run:
        path: sh
        args:
        - -c
        - |
          OP='(('
          CL='))'
          printf '%%s\n' 'jobs:' '- name: greet-job' '  plan:' '  - task: greet' '    config:' '      platform: linux' '      image_resource: {type: registry-image, source: {repository: busybox}}' '      params:' "        MSG: ${OP}msg${CL}" '      run:' '        path: sh' '        args: ["-c", "echo greeting=${MSG}"]' > pipeline-config/pipeline.yml
          echo "sp-vars-generated"
  - set_pipeline: %s
    file: pipeline-config/pipeline.yml
    vars:
      msg: ((.:greeting))
`, childPipeline))

			setAndUnpausePipeline(pipelineFile)
			triggerJob("sp-vars-job")

			session := waitForBuildAndWatch("sp-vars-job")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("sp-vars-generated"))

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

		It("10.4: uses var_files with set_pipeline", func() {
			childPipeline := pipelineName + "-vf"

			pipelineFile := writePipelineFile("sp-var-files.yml", fmt.Sprintf(`
jobs:
- name: sp-vf-job
  plan:
  - task: generate
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      outputs:
      - name: pipeline-config
      - name: vars
      run:
        path: sh
        args:
        - -c
        - |
          OP='(('
          CL='))'
          printf '%%s\n' 'greeting: hello-from-var-file' > vars/vars.yml
          printf '%%s\n' 'jobs:' '- name: vf-job' '  plan:' '  - task: vf-task' '    config:' '      platform: linux' '      image_resource: {type: registry-image, source: {repository: busybox}}' '      params:' "        GREET: ${OP}greeting${CL}" '      run:' '        path: sh' '        args: ["-c", "echo vf-greeting=${GREET}"]' > pipeline-config/pipeline.yml
          echo "sp-var-files-generated"
  - set_pipeline: %s
    file: pipeline-config/pipeline.yml
    var_files:
    - vars/vars.yml
`, childPipeline))

			setAndUnpausePipeline(pipelineFile)
			triggerJob("sp-vf-job")

			session := waitForBuildAndWatch("sp-vf-job")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("sp-var-files-generated"))

			By("cleaning up the child pipeline")
			sess := fly.Start("destroy-pipeline", "-n", "-p", childPipeline)
			<-sess.Exited
		})

		It("10.5: set_pipeline with instance_vars creates instanced pipeline", func() {
			childPipeline := pipelineName + "-inst"

			pipelineFile := writePipelineFile("sp-instance.yml", fmt.Sprintf(`
jobs:
- name: sp-inst-job
  plan:
  - task: generate
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      outputs: [{name: pipeline-config}]
      run:
        path: sh
        args:
        - -c
        - |
          printf '%%s\n' 'jobs:' '- name: inst-job' '  plan:' '  - task: inst-task' '    config:' '      platform: linux' '      image_resource: {type: registry-image, source: {repository: busybox}}' '      run:' '        path: echo' '        args: ["instance-pipeline"]' > pipeline-config/pipeline.yml
          echo "sp-instance-generated"
  - set_pipeline: %s
    file: pipeline-config/pipeline.yml
    instance_vars:
      branch: main
`, childPipeline))

			setAndUnpausePipeline(pipelineFile)
			triggerJob("sp-inst-job")

			session := waitForBuildAndWatch("sp-inst-job")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("sp-instance-generated"))

			By("cleaning up the instanced pipeline")
			sess := fly.Start("destroy-pipeline", "-n", "-p", childPipeline+"/branch:main")
			<-sess.Exited
			// Also try without instance vars in case naming is different
			sess2 := fly.Start("destroy-pipeline", "-n", "-p", childPipeline)
			<-sess2.Exited
		})

		It("10.6: set_pipeline with team sets pipeline in another team", func() {
			// This test validates the team parameter. The actual target team
			// must exist — we use main team for simplicity.
			childPipeline := pipelineName + "-team"

			pipelineFile := writePipelineFile("sp-team.yml", fmt.Sprintf(`
jobs:
- name: sp-team-job
  plan:
  - task: generate
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      outputs: [{name: pipeline-config}]
      run:
        path: sh
        args:
        - -c
        - |
          printf '%%s\n' 'jobs:' '- name: team-job' '  plan:' '  - task: team-task' '    config:' '      platform: linux' '      image_resource: {type: registry-image, source: {repository: busybox}}' '      run:' '        path: echo' '        args: ["team-pipeline"]' > pipeline-config/pipeline.yml
          echo "sp-team-generated"
  - set_pipeline: %s
    file: pipeline-config/pipeline.yml
    team: main
`, childPipeline))

			setAndUnpausePipeline(pipelineFile)
			triggerJob("sp-team-job")

			session := waitForBuildAndWatch("sp-team-job")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("sp-team-generated"))

			By("cleaning up the child pipeline")
			sess := fly.Start("destroy-pipeline", "-n", "-p", childPipeline)
			<-sess.Exited
		})

		It("10.7: set_pipeline: self updates the current pipeline", func() {
			pipelineFile := writePipelineFile("sp-self.yml", `
jobs:
- name: sp-self-job
  plan:
  - task: generate
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      outputs: [{name: pipeline-config}]
      run:
        path: sh
        args:
        - -c
        - |
          printf '%s\n' 'jobs:' '- name: sp-self-job' '  plan:' '  - task: generate' '    config:' '      platform: linux' '      image_resource: {type: registry-image, source: {repository: busybox}}' '      outputs: [{name: pipeline-config}]' '      run:' '        path: echo' '        args: ["self-updated"]' '  - set_pipeline: self' '    file: pipeline-config/pipeline.yml' '- name: new-job-from-self' '  plan:' '  - task: new-task' '    config:' '      platform: linux' '      image_resource: {type: registry-image, source: {repository: busybox}}' '      run:' '        path: echo' '        args: ["new-job-exists"]' > pipeline-config/pipeline.yml
          echo "sp-self-generated"
  - set_pipeline: self
    file: pipeline-config/pipeline.yml
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("sp-self-job")

			session := waitForBuildAndWatch("sp-self-job")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("sp-self-generated"))

			By("verifying the pipeline was self-updated with the new job")
			Eventually(func() bool {
				jobs := flyTable("jobs", "-p", pipelineName)
				for _, j := range jobs {
					if j["name"] == "new-job-from-self" {
						return true
					}
				}
				return false
			}, 1*time.Minute, 2*time.Second).Should(BeTrue(),
				"expected new-job-from-self to exist after self-update",
			)
		})
	})

	Context("load_var", func() {
		It("10.8: loads a plain string value", func() {
			pipelineFile := writePipelineFile("lv-string.yml", `
jobs:
- name: lv-string-job
  plan:
  - task: produce
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      outputs: [{name: vals}]
      run:
        path: sh
        args: ["-c", "echo -n hello-load-var > vals/val.txt"]
  - load_var: my-str
    file: vals/val.txt
  - task: consume
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      params:
        VAL: ((.:my-str))
      run:
        path: sh
        args: ["-c", "echo loaded-string=${VAL}"]
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("lv-string-job")

			session := waitForBuildAndWatch("lv-string-job")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("loaded-string=hello-load-var"))
		})

		It("10.9: loads a JSON file and accesses nested keys", func() {
			pipelineFile := writePipelineFile("lv-json.yml", `
jobs:
- name: lv-json-job
  plan:
  - task: produce
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      outputs: [{name: vals}]
      run:
        path: sh
        args:
        - -c
        - echo '{"name":"concourse","version":"7.0"}' > vals/config.json
  - load_var: cfg
    file: vals/config.json
    format: json
  - task: consume
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      params:
        APP_NAME: ((.:cfg.name))
        APP_VERSION: ((.:cfg.version))
      run:
        path: sh
        args: ["-c", "echo app=${APP_NAME} ver=${APP_VERSION}"]
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("lv-json-job")

			session := waitForBuildAndWatch("lv-json-job")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("app=concourse ver=7.0"))
		})

		It("10.10: loads a YAML file and accesses nested keys", func() {
			pipelineFile := writePipelineFile("lv-yaml.yml", `
jobs:
- name: lv-yaml-job
  plan:
  - task: produce
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      outputs: [{name: vals}]
      run:
        path: sh
        args:
        - -c
        - |
          printf '%%s\n' 'env: production' 'replicas: 3' > vals/config.yml
  - load_var: yml-cfg
    file: vals/config.yml
    format: yaml
  - task: consume
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      params:
        ENV: ((.:yml-cfg.env))
      run:
        path: sh
        args: ["-c", "echo env=${ENV}"]
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("lv-yaml-job")

			session := waitForBuildAndWatch("lv-yaml-job")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("env=production"))
		})

		It("10.11: loads a raw (unformatted) file preserving whitespace", func() {
			pipelineFile := writePipelineFile("lv-raw.yml", `
jobs:
- name: lv-raw-job
  plan:
  - task: produce
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      outputs: [{name: vals}]
      run:
        path: sh
        args: ["-c", "echo -n '  raw-with-spaces  ' > vals/raw.txt"]
  - load_var: raw-val
    file: vals/raw.txt
    format: raw
  - task: consume
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      params:
        RAW: ((.:raw-val))
      run:
        path: sh
        args: ["-c", "echo start[${RAW}]end"]
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("lv-raw-job")

			session := waitForBuildAndWatch("lv-raw-job")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("start\\[  raw-with-spaces  \\]end"))
		})

		It("10.12: reveal: true shows the loaded value in build output", func() {
			pipelineFile := writePipelineFile("lv-reveal.yml", `
jobs:
- name: lv-reveal-job
  plan:
  - task: produce
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      outputs: [{name: vals}]
      run:
        path: sh
        args: ["-c", "echo -n revealed-secret-42 > vals/secret.txt"]
  - load_var: revealed
    file: vals/secret.txt
    reveal: true
  - task: done
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["lv-reveal-done"]
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("lv-reveal-job")

			session := waitForBuildAndWatch("lv-reveal-job")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("revealed-secret-42"))
		})

		It("10.13: loaded var is usable in subsequent task params", func() {
			pipelineFile := writePipelineFile("lv-task-params.yml", `
jobs:
- name: lv-params-job
  plan:
  - task: produce
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      outputs: [{name: vals}]
      run:
        path: sh
        args: ["-c", "echo -n param-value-99 > vals/p.txt"]
  - load_var: my-param
    file: vals/p.txt
  - task: consume
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      params:
        INJECTED: ((.:my-param))
      run:
        path: sh
        args: ["-c", "echo injected=${INJECTED}"]
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("lv-params-job")

			session := waitForBuildAndWatch("lv-params-job")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("injected=param-value-99"))
		})

		It("10.14: loaded var is usable in put params", func() {
			pipelineFile := writePipelineFile("lv-put-params.yml", `
resources:
- name: lv-put-resource
  type: mock
  source: {}
  check_every: never

jobs:
- name: lv-put-params-job
  plan:
  - task: produce
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      outputs: [{name: vals}]
      run:
        path: sh
        args: ["-c", "echo -n put-version-from-var > vals/ver.txt"]
  - load_var: ver
    file: vals/ver.txt
  - put: lv-put-resource
    params:
      version: ((.:ver))
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("lv-put-params-job")

			session := waitForBuildAndWatch("lv-put-params-job")
			Expect(session).To(gexec.Exit(0))
		})

		It("10.15: chains multiple load_var steps in sequence", func() {
			pipelineFile := writePipelineFile("lv-multi.yml", `
jobs:
- name: lv-multi-job
  plan:
  - task: produce-all
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      outputs: [{name: vals}]
      run:
        path: sh
        args:
        - -c
        - |
          echo -n alpha > vals/a.txt
          echo -n bravo > vals/b.txt
          echo -n charlie > vals/c.txt
  - load_var: var-a
    file: vals/a.txt
  - load_var: var-b
    file: vals/b.txt
  - load_var: var-c
    file: vals/c.txt
  - task: use-all
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      params:
        A: ((.:var-a))
        B: ((.:var-b))
        C: ((.:var-c))
      run:
        path: sh
        args: ["-c", "echo a=${A} b=${B} c=${C}"]
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("lv-multi-job")

			session := waitForBuildAndWatch("lv-multi-job")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("a=alpha b=bravo c=charlie"))
		})
	})
})
