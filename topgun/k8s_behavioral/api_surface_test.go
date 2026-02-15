package behavioral_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("API Surface Validation", func() {

	It("returns server info from /api/v1/info", func() {
		status, body := apiGet("/api/v1/info")
		Expect(status).To(Equal(http.StatusOK))

		var info map[string]interface{}
		Expect(json.Unmarshal(body, &info)).To(Succeed())
		Expect(info).To(HaveKey("version"))
		Expect(info).To(HaveKey("worker_version"))
	})

	It("streams build events via SSE", func() {
		cfg := writePipelineFile("api-events.yml", `
jobs:
- name: api-events-job
  plan:
  - task: hello
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["api-event-test"]
`)
		setAndUnpausePipeline(cfg)
		triggerJob("api-events-job")

		sess := waitForBuildAndWatch("api-events-job")
		Expect(sess.ExitCode()).To(Equal(0))

		By("fetching build events from the API")
		rows := flyTable("builds", "-j", inPipeline("api-events-job"))
		Expect(rows).ToNot(BeEmpty())
		buildID := rows[0]["id"]

		events := getBuildEvents(buildID)
		Expect(events).ToNot(BeEmpty())
		// Should include standard event types
		allEvents := strings.Join(events, ",")
		Expect(allEvents).To(SatisfyAny(
			ContainSubstring("initialize"),
			ContainSubstring("start"),
			ContainSubstring("log"),
			ContainSubstring("finish"),
		))
	})

	It("returns pipeline badge SVGs", func() {
		cfg := writePipelineFile("badge.yml", `
jobs:
- name: badge-job
  plan:
  - task: pass
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["ok"]
`)
		setAndUnpausePipeline(cfg)

		By("exposing the pipeline for public badge access")
		fly.Run("expose-pipeline", "-p", pipelineName)

		triggerJob("badge-job")
		sess := waitForBuildAndWatch("badge-job")
		Expect(sess.ExitCode()).To(Equal(0))

		By("fetching the job badge")
		path := fmt.Sprintf("/api/v1/teams/main/pipelines/%s/jobs/badge-job/badge", pipelineName)
		status, body := apiGet(path)
		Expect(status).To(Equal(http.StatusOK))
		Expect(string(body)).To(ContainSubstring("<svg"))
	})

	It("returns CCTray XML for pipeline monitoring", func() {
		cfg := writePipelineFile("cctray.yml", `
jobs:
- name: cctray-job
  plan:
  - task: pass
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["cctray"]
`)
		setAndUnpausePipeline(cfg)
		triggerJob("cctray-job")
		sess := waitForBuildAndWatch("cctray-job")
		Expect(sess.ExitCode()).To(Equal(0))

		By("fetching CCTray XML")
		status, body := apiGet("/api/v1/teams/main/cc.xml")
		Expect(status).To(Equal(http.StatusOK))
		Expect(string(body)).To(ContainSubstring("<Project"))
	})

	It("supports pagination on builds endpoint", func() {
		cfg := writePipelineFile("paginate.yml", `
jobs:
- name: paginate-job
  plan:
  - task: pass
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["ok"]
`)
		setAndUnpausePipeline(cfg)

		By("creating multiple builds")
		for i := 0; i < 3; i++ {
			triggerJob("paginate-job")
			sess := waitForBuildAndWatch("paginate-job", fmt.Sprintf("%d", i+1))
			Expect(sess.ExitCode()).To(Equal(0))
		}

		By("fetching builds with limit parameter")
		path := fmt.Sprintf("/api/v1/teams/main/pipelines/%s/jobs/paginate-job/builds?limit=2", pipelineName)
		status, body := apiGet(path)
		Expect(status).To(Equal(http.StatusOK))

		var builds []map[string]interface{}
		Expect(json.Unmarshal(body, &builds)).To(Succeed())
		Expect(len(builds)).To(Equal(2))
	})

	It("filters builds by pipeline and job", func() {
		cfg := writePipelineFile("filter.yml", `
jobs:
- name: filter-job
  plan:
  - task: pass
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["filtered"]
`)
		setAndUnpausePipeline(cfg)
		triggerJob("filter-job")
		sess := waitForBuildAndWatch("filter-job")
		Expect(sess.ExitCode()).To(Equal(0))

		By("fetching builds filtered by job")
		path := fmt.Sprintf("/api/v1/teams/main/pipelines/%s/jobs/filter-job/builds", pipelineName)
		status, body := apiGet(path)
		Expect(status).To(Equal(http.StatusOK))

		var builds []map[string]interface{}
		Expect(json.Unmarshal(body, &builds)).To(Succeed())
		Expect(builds).ToNot(BeEmpty())

		for _, b := range builds {
			Expect(b["pipeline_name"]).To(Equal(pipelineName))
			Expect(b["job_name"]).To(Equal("filter-job"))
		}
	})

	It("returns proper 404 for non-existent resources", func() {
		status, _ := apiGet("/api/v1/teams/main/pipelines/does-not-exist-xyz/jobs")
		Expect(status).To(Equal(http.StatusNotFound))
	})
})
