package mcpserver_test

import (
	"encoding/json"
	"net/http"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/mcpserver"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
)

var _ = Describe("Tools", func() {
	var (
		server       *mcpserver.Server
		teamFactory  *dbfakes.FakeTeamFactory
		buildFactory *dbfakes.FakeBuildFactory
		fakeTeam     *dbfakes.FakeTeam
		fakePipeline *dbfakes.FakePipeline
	)

	BeforeEach(func() {
		server = mcpserver.NewServer()
		teamFactory = new(dbfakes.FakeTeamFactory)
		buildFactory = new(dbfakes.FakeBuildFactory)
		fakeTeam = new(dbfakes.FakeTeam)
		fakePipeline = new(dbfakes.FakePipeline)

		fakeTeam.NameReturns("main")
		fakePipeline.NameReturns("my-pipeline")
		fakePipeline.TeamNameReturns("main")

		teamFactory.FindTeamReturns(fakeTeam, true, nil)
		fakeTeam.PipelinesReturns([]db.Pipeline{fakePipeline}, nil)

		mcpserver.RegisterTools(server, teamFactory, buildFactory, "https://concourse.example.com", "1.0.0")
	})

	Describe("tools/list", func() {
		It("returns all 18 tools", func() {
			body := jsonRPCBody("tools/list", 1, nil)
			resp := doMCP(server, body)
			result := decodeResult(resp)
			tools := result["tools"].([]any)
			Expect(tools).To(HaveLen(18))

			names := make([]string, len(tools))
			for i, t := range tools {
				names[i] = t.(map[string]any)["name"].(string)
			}
			Expect(names).To(ContainElements(
				"list_pipelines", "get_pipeline", "set_pipeline",
				"pause_pipeline", "unpause_pipeline",
				"list_jobs", "list_builds",
				"get_build", "get_build_log", "trigger_job", "abort_build",
				"list_resources", "list_resource_versions", "check_resource",
				"get_job", "list_teams", "get_build_plan", "get_info",
			))
		})
	})

	Describe("list_pipelines", func() {
		It("returns pipelines for a team", func() {
			fakePipeline.IDReturns(1)
			fakePipeline.PausedReturns(false)
			fakePipeline.PublicReturns(true)
			fakePipeline.ArchivedReturns(false)

			result := callTool(server, "list_pipelines", map[string]any{"team": "main"})
			var pipelines []map[string]any
			Expect(json.Unmarshal([]byte(result), &pipelines)).To(Succeed())
			Expect(pipelines).To(HaveLen(1))
			Expect(pipelines[0]["name"]).To(Equal("my-pipeline"))
			Expect(pipelines[0]["team_name"]).To(Equal("main"))
		})

		It("returns error for unknown team", func() {
			teamFactory.FindTeamReturns(nil, false, nil)
			result, isError := callToolRaw(server, "list_pipelines", map[string]any{"team": "nonexistent"})
			Expect(isError).To(BeTrue())
			Expect(result).To(ContainSubstring("not found"))
		})
	})

	Describe("get_pipeline", func() {
		It("returns pipeline config", func() {
			fakePipeline.ConfigReturns(atc.Config{
				Jobs: atc.JobConfigs{{Name: "test-job"}},
			}, nil)
			fakePipeline.ConfigVersionReturns(42)

			result := callTool(server, "get_pipeline", map[string]any{
				"team":     "main",
				"pipeline": "my-pipeline",
			})
			var output map[string]any
			Expect(json.Unmarshal([]byte(result), &output)).To(Succeed())
			Expect(output["version"]).To(BeEquivalentTo(42))
		})
	})

	Describe("pause_pipeline", func() {
		It("pauses the pipeline", func() {
			result := callTool(server, "pause_pipeline", map[string]any{
				"team":     "main",
				"pipeline": "my-pipeline",
			})
			Expect(result).To(ContainSubstring("true"))
			Expect(fakePipeline.PauseCallCount()).To(Equal(1))
		})
	})

	Describe("unpause_pipeline", func() {
		It("unpauses the pipeline", func() {
			result := callTool(server, "unpause_pipeline", map[string]any{
				"team":     "main",
				"pipeline": "my-pipeline",
			})
			Expect(result).To(ContainSubstring("true"))
			Expect(fakePipeline.UnpauseCallCount()).To(Equal(1))
		})
	})

	Describe("list_jobs", func() {
		It("returns jobs for a pipeline", func() {
			fakeJob := new(dbfakes.FakeJob)
			fakeJob.NameReturns("build-it")
			fakeJob.PausedReturns(false)
			fakePipeline.JobsReturns(db.Jobs{fakeJob}, nil)

			result := callTool(server, "list_jobs", map[string]any{
				"team":     "main",
				"pipeline": "my-pipeline",
			})
			var jobs []map[string]any
			Expect(json.Unmarshal([]byte(result), &jobs)).To(Succeed())
			Expect(jobs).To(HaveLen(1))
			Expect(jobs[0]["name"]).To(Equal("build-it"))
		})
	})

	Describe("list_builds", func() {
		It("returns builds for a job", func() {
			fakeJob := new(dbfakes.FakeJob)
			fakeJob.NameReturns("build-it")
			fakePipeline.JobReturns(fakeJob, true, nil)

			fakeBuild := new(dbfakes.FakeBuildForAPI)
			fakeBuild.IDReturns(100)
			fakeBuild.NameReturns("1")
			fakeBuild.StatusReturns("succeeded")
			fakeBuild.PipelineNameReturns("my-pipeline")
			fakeBuild.JobNameReturns("build-it")
			fakeBuild.TeamNameReturns("main")
			fakeBuild.StartTimeReturns(time.Unix(1000, 0))
			fakeBuild.EndTimeReturns(time.Unix(1060, 0))

			fakeJob.BuildsReturns([]db.BuildForAPI{fakeBuild}, db.Pagination{}, nil)

			result := callTool(server, "list_builds", map[string]any{
				"team":     "main",
				"pipeline": "my-pipeline",
				"job":      "build-it",
			})
			var builds []map[string]any
			Expect(json.Unmarshal([]byte(result), &builds)).To(Succeed())
			Expect(builds).To(HaveLen(1))
			Expect(builds[0]["id"]).To(BeEquivalentTo(100))
			Expect(builds[0]["status"]).To(Equal("succeeded"))
			Expect(builds[0]["duration_seconds"]).To(BeEquivalentTo(60))
		})
	})

	Describe("get_build", func() {
		It("returns build details", func() {
			fakeBuild := new(dbfakes.FakeBuildForAPI)
			fakeBuild.IDReturns(42)
			fakeBuild.NameReturns("3")
			fakeBuild.StatusReturns("failed")
			fakeBuild.PipelineNameReturns("my-pipeline")
			fakeBuild.JobNameReturns("test")
			fakeBuild.TeamNameReturns("main")
			fakeBuild.StartTimeReturns(time.Unix(2000, 0))
			fakeBuild.EndTimeReturns(time.Unix(2120, 0))

			buildFactory.BuildForAPIReturns(fakeBuild, true, nil)

			result := callTool(server, "get_build", map[string]any{"build_id": 42})
			var build map[string]any
			Expect(json.Unmarshal([]byte(result), &build)).To(Succeed())
			Expect(build["id"]).To(BeEquivalentTo(42))
			Expect(build["status"]).To(Equal("failed"))
			Expect(build["duration_seconds"]).To(BeEquivalentTo(120))
		})

		It("returns error for missing build", func() {
			buildFactory.BuildForAPIReturns(nil, false, nil)
			_, isError := callToolRaw(server, "get_build", map[string]any{"build_id": 999})
			Expect(isError).To(BeTrue())
		})
	})

	Describe("trigger_job", func() {
		It("creates a build and returns info", func() {
			fakeJob := new(dbfakes.FakeJob)
			fakeJob.NameReturns("deploy")
			fakePipeline.JobReturns(fakeJob, true, nil)

			fakeBuild := new(dbfakes.FakeBuild)
			fakeBuild.IDReturns(200)
			fakeBuild.NameReturns("5")
			fakeJob.CreateBuildReturns(fakeBuild, nil)

			result := callTool(server, "trigger_job", map[string]any{
				"team":     "main",
				"pipeline": "my-pipeline",
				"job":      "deploy",
			})
			var output map[string]any
			Expect(json.Unmarshal([]byte(result), &output)).To(Succeed())
			Expect(output["build_id"]).To(BeEquivalentTo(200))
			Expect(output["url"]).To(ContainSubstring("concourse.example.com"))
		})
	})

	Describe("abort_build", func() {
		It("aborts the build", func() {
			fakeBuild := new(dbfakes.FakeBuild)
			buildFactory.BuildReturns(fakeBuild, true, nil)

			result := callTool(server, "abort_build", map[string]any{"build_id": 42})
			Expect(result).To(ContainSubstring("true"))
			Expect(fakeBuild.MarkAsAbortedCallCount()).To(Equal(1))
		})
	})

	Describe("list_resources", func() {
		It("returns resources for a pipeline", func() {
			fakeResource := new(dbfakes.FakeResource)
			fakeResource.NameReturns("my-repo")
			fakeResource.TypeReturns("git")
			fakePipeline.ResourcesReturns(db.Resources{fakeResource}, nil)

			result := callTool(server, "list_resources", map[string]any{
				"team":     "main",
				"pipeline": "my-pipeline",
			})
			var resources []map[string]any
			Expect(json.Unmarshal([]byte(result), &resources)).To(Succeed())
			Expect(resources).To(HaveLen(1))
			Expect(resources[0]["name"]).To(Equal("my-repo"))
			Expect(resources[0]["type"]).To(Equal("git"))
		})
	})

	Describe("check_resource", func() {
		It("triggers a resource check", func() {
			fakeResource := new(dbfakes.FakeResource)
			fakePipeline.ResourceReturns(fakeResource, true, nil)

			result := callTool(server, "check_resource", map[string]any{
				"team":     "main",
				"pipeline": "my-pipeline",
				"resource": "my-repo",
			})
			Expect(result).To(ContainSubstring("success"))
			Expect(fakeResource.NotifyScanCallCount()).To(Equal(1))
		})
	})

	Describe("list_teams", func() {
		It("returns all teams", func() {
			fakeTeam2 := new(dbfakes.FakeTeam)
			fakeTeam2.IDReturns(2)
			fakeTeam2.NameReturns("other")
			teamFactory.GetTeamsReturns([]db.Team{fakeTeam, fakeTeam2}, nil)

			result := callTool(server, "list_teams", map[string]any{})
			var teams []map[string]any
			Expect(json.Unmarshal([]byte(result), &teams)).To(Succeed())
			Expect(teams).To(HaveLen(2))
		})
	})

	Describe("get_info", func() {
		It("returns server info", func() {
			result := callTool(server, "get_info", map[string]any{})
			var info map[string]any
			Expect(json.Unmarshal([]byte(result), &info)).To(Succeed())
			Expect(info["version"]).To(Equal("1.0.0"))
			Expect(info["external_url"]).To(Equal("https://concourse.example.com"))
		})
	})

	Describe("get_build_plan", func() {
		It("returns the build plan", func() {
			plan := json.RawMessage(`{"id":"plan-1","task":{"name":"build"}}`)
			fakeBuild := new(dbfakes.FakeBuild)
			fakeBuild.PublicPlanReturns(&plan)
			buildFactory.BuildReturns(fakeBuild, true, nil)

			result := callTool(server, "get_build_plan", map[string]any{"build_id": 42})
			Expect(result).To(ContainSubstring("plan-1"))
		})
	})
})

// callTool invokes a tool and returns the text content (expects success)
func callTool(server *mcpserver.Server, name string, args map[string]any) string {
	text, isError := callToolRaw(server, name, args)
	Expect(isError).To(BeFalse(), "expected tool to succeed but got error: %s", text)
	return text
}

// callToolRaw invokes a tool and returns text + isError flag
func callToolRaw(server *mcpserver.Server, name string, args map[string]any) (string, bool) {
	body := jsonRPCBody("tools/call", 1, map[string]any{
		"name":      name,
		"arguments": args,
	})
	resp := doMCP(server, body)
	Expect(resp.StatusCode).To(Equal(http.StatusOK))

	var rpcResp jsonRPCResponse
	Expect(json.NewDecoder(resp.Body).Decode(&rpcResp)).To(Succeed())
	Expect(rpcResp.Error).To(BeNil())

	var result map[string]any
	Expect(json.Unmarshal(rpcResp.Result, &result)).To(Succeed())

	content := result["content"].([]any)
	Expect(content).To(HaveLen(1))
	text := content[0].(map[string]any)["text"].(string)

	isError, _ := result["isError"].(bool)
	return text, isError
}
