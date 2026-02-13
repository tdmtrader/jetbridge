package integration_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
)

var _ = Describe("Load Var Step", func() {
	It("loads a raw text file from task output", func() {
		pipelineFile := writePipelineFile("load-var-raw.yml", `
jobs:
- name: load-raw-job
  plan:
  - task: produce-value
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      outputs:
      - name: values
      run:
        path: sh
        args:
        - -c
        - echo -n "hello-from-load-var" > values/my-value.txt
  - load_var: my-var
    file: values/my-value.txt
  - task: use-value
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      params:
        LOADED: ((.:my-var))
      run:
        path: sh
        args:
        - -c
        - echo "loaded-value=${LOADED}"
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("load-raw-job")

		session := waitForBuildAndWatch("load-raw-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("loaded-value=hello-from-load-var"))
	})

	It("loads a JSON file and accesses nested keys", func() {
		pipelineFile := writePipelineFile("load-var-json.yml", `
jobs:
- name: load-json-job
  plan:
  - task: produce-json
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      outputs:
      - name: values
      run:
        path: sh
        args:
        - -c
        - echo '{"name":"concourse","version":"7.0"}' > values/config.json
  - load_var: cfg
    file: values/config.json
    format: json
  - task: use-json
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      params:
        APP_NAME: ((.:cfg.name))
        APP_VERSION: ((.:cfg.version))
      run:
        path: sh
        args:
        - -c
        - echo "app=${APP_NAME} ver=${APP_VERSION}"
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("load-json-job")

		session := waitForBuildAndWatch("load-json-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("app=concourse ver=7.0"))
	})

	It("loads a YAML file and accesses nested keys", func() {
		pipelineFile := writePipelineFile("load-var-yaml.yml", `
jobs:
- name: load-yaml-job
  plan:
  - task: produce-yaml
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      outputs:
      - name: values
      run:
        path: sh
        args:
        - -c
        - |
          cat > values/config.yml <<YAMLEOF
          env: production
          replicas: 3
          YAMLEOF
  - load_var: yml-cfg
    file: values/config.yml
    format: yaml
  - task: use-yaml
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      params:
        ENV: ((.:yml-cfg.env))
      run:
        path: sh
        args:
        - -c
        - echo "env=${ENV}"
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("load-yaml-job")

		session := waitForBuildAndWatch("load-yaml-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("env=production"))
	})

	It("loads a text file with trim format stripping whitespace", func() {
		pipelineFile := writePipelineFile("load-var-trim.yml", `
jobs:
- name: load-trim-job
  plan:
  - task: produce-padded
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      outputs:
      - name: values
      run:
        path: sh
        args:
        - -c
        - printf "  trimmed-value  \n" > values/padded.txt
  - load_var: trimmed
    file: values/padded.txt
    format: trim
  - task: use-trimmed
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      params:
        VAL: ((.:trimmed))
      run:
        path: sh
        args:
        - -c
        - echo "start[${VAL}]end"
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("load-trim-job")

		session := waitForBuildAndWatch("load-trim-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("start\\[trimmed-value\\]end"))
	})

	It("loads a var from a get step artifact", func() {
		pipelineFile := writePipelineFile("load-var-get.yml", `
resources:
- name: versioned-res
  type: mock
  source:
    create_files:
      version.txt: "v2.5.0"

jobs:
- name: load-from-get-job
  plan:
  - get: versioned-res
    trigger: false
  - load_var: res-version
    file: versioned-res/version.txt
    format: trim
  - task: show-version
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      params:
        VER: ((.:res-version))
      run:
        path: sh
        args:
        - -c
        - echo "resource-version=${VER}"
`)
		setAndUnpausePipeline(pipelineFile)
		newMockVersion("versioned-res", "v1")
		triggerJob("load-from-get-job")

		session := waitForBuildAndWatch("load-from-get-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("resource-version=v2.5.0"))
	})

	It("uses loaded var in subsequent task params", func() {
		pipelineFile := writePipelineFile("load-var-params.yml", `
jobs:
- name: load-params-job
  plan:
  - task: produce
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      outputs:
      - name: vals
      run:
        path: sh
        args:
        - -c
        - echo -n "param-value-42" > vals/p.txt
  - load_var: my-param
    file: vals/p.txt
  - task: consume
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      params:
        INJECTED: ((.:my-param))
      run:
        path: sh
        args:
        - -c
        - echo "injected=${INJECTED}"
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("load-params-job")

		session := waitForBuildAndWatch("load-params-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("injected=param-value-42"))
	})

	It("chains multiple load_var steps in sequence", func() {
		pipelineFile := writePipelineFile("load-var-multi.yml", `
jobs:
- name: multi-load-job
  plan:
  - task: produce-all
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      outputs:
      - name: vals
      run:
        path: sh
        args:
        - -c
        - |
          echo -n "alpha" > vals/a.txt
          echo -n "bravo" > vals/b.txt
          echo -n "charlie" > vals/c.txt
  - load_var: var-a
    file: vals/a.txt
  - load_var: var-b
    file: vals/b.txt
  - load_var: var-c
    file: vals/c.txt
  - task: use-all
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      params:
        A: ((.:var-a))
        B: ((.:var-b))
        C: ((.:var-c))
      run:
        path: sh
        args:
        - -c
        - echo "a=${A} b=${B} c=${C}"
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("multi-load-job")

		session := waitForBuildAndWatch("multi-load-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("a=alpha b=bravo c=charlie"))
	})

	It("reveals the loaded value in build output with reveal: true", func() {
		pipelineFile := writePipelineFile("load-var-reveal.yml", `
jobs:
- name: load-reveal-job
  plan:
  - task: produce
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      outputs:
      - name: vals
      run:
        path: sh
        args:
        - -c
        - echo -n "revealed-secret-value" > vals/secret.txt
  - load_var: revealed
    file: vals/secret.txt
    reveal: true
  - task: done
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      run:
        path: echo
        args: ["reveal-test-done"]
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("load-reveal-job")

		session := waitForBuildAndWatch("load-reveal-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("revealed-secret-value"))
	})

	It("fails gracefully when the file does not exist", func() {
		pipelineFile := writePipelineFile("load-var-missing.yml", `
jobs:
- name: load-missing-job
  plan:
  - task: produce-nothing
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      outputs:
      - name: vals
      run:
        path: echo
        args: ["no file produced"]
  - load_var: missing-var
    file: vals/nonexistent.txt
  - task: should-not-run
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      run:
        path: echo
        args: ["this should not appear"]
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("load-missing-job")

		session := waitForBuildAndWatch("load-missing-job")
		Expect(session.ExitCode()).ToNot(Equal(0))

		By("verifying the build failed")
		builds := flyTable("builds", "-j", inPipeline("load-missing-job"))
		Expect(builds).ToNot(BeEmpty())
		Expect(builds[0]["status"]).To(Equal("failed"))
	})
})
