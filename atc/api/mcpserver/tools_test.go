package mcpserver_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/agent/budget"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/mcpserver"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
)

var _ = Describe("MCP tools PostgreSQL fixture", func() {
	It("reads committed state through separately constructed production factories", func() {
		fixture := useMCPToolsDB()
		loaded, found, err := db.NewTeamFactory(fixture.Conn, fixture.LockFactory).
			FindTeam(fixture.Main.Name())
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(loaded.ID()).To(Equal(fixture.Main.ID()))
		Expect(loaded.Name()).To(Equal(fixture.Main.Name()))
	})
})

var _ = Describe("Tools", func() {
	var (
		server             *mcpserver.Server
		teamFactory        *dbfakes.FakeTeamFactory
		buildFactory       *dbfakes.FakeBuildFactory
		workflowsFactory   *dbfakes.FakeAgentWorkflowsFactory
		costLedgerFactory  *dbfakes.FakeAgentCostLedgerFactory
		pipelineRunFactory *dbfakes.FakePipelineRunFactory
		fakeTeam           *dbfakes.FakeTeam
		fakePipeline       *dbfakes.FakePipeline
	)

	BeforeEach(func() {
		server = mcpserver.NewServer()
		teamFactory = new(dbfakes.FakeTeamFactory)
		buildFactory = new(dbfakes.FakeBuildFactory)
		workflowsFactory = new(dbfakes.FakeAgentWorkflowsFactory)
		costLedgerFactory = new(dbfakes.FakeAgentCostLedgerFactory)
		pipelineRunFactory = new(dbfakes.FakePipelineRunFactory)
		fakeTeam = new(dbfakes.FakeTeam)
		fakePipeline = new(dbfakes.FakePipeline)

		fakeTeam.NameReturns("main")
		fakePipeline.NameReturns("my-pipeline")
		fakePipeline.TeamNameReturns("main")

		teamFactory.FindTeamReturns(fakeTeam, true, nil)
		fakeTeam.PipelinesReturns([]db.Pipeline{fakePipeline}, nil)

		mcpserver.RegisterTools(server, teamFactory, buildFactory, workflowsFactory, costLedgerFactory, pipelineRunFactory, "https://concourse.example.com", "1.0.0")
	})

	Describe("tools/list", func() {
		It("returns all 25 tools", func() {
			body := jsonRPCBody("tools/list", 1, nil)
			resp := doMCP(server, body)
			result := decodeResult(resp)
			tools := result["tools"].([]any)
			Expect(tools).To(HaveLen(25))

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
				"list_deprecated_scopes", "copy_resource_versions",
				"list_agent_workflows", "get_agent_workflow", "agent_cost_rollup",
				"list_pipeline_runs", "get_pipeline_run",
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

	Describe("list_deprecated_scopes", func() {
		It("returns deprecated scopes for a resource", func() {
			fakeResource := new(dbfakes.FakeResource)
			fakeResource.NameReturns("my-resource")
			fakeResource.DeprecatedScopesReturns([]db.DeprecatedScope{
				{ID: 42, DeprecatedAt: time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC), ConfigID: 17},
				{ID: 38, DeprecatedAt: time.Date(2026, 4, 8, 9, 30, 0, 0, time.UTC), ConfigID: 15},
			}, nil)
			fakePipeline.ResourceReturns(fakeResource, true, nil)

			result := callTool(server, "list_deprecated_scopes", map[string]any{
				"team":     "main",
				"pipeline": "my-pipeline",
				"resource": "my-resource",
			})
			var scopes []map[string]any
			Expect(json.Unmarshal([]byte(result), &scopes)).To(Succeed())
			Expect(scopes).To(HaveLen(2))
			Expect(scopes[0]["id"]).To(BeEquivalentTo(42))
			Expect(scopes[1]["id"]).To(BeEquivalentTo(38))
		})

		It("returns empty list when no deprecated scopes", func() {
			fakeResource := new(dbfakes.FakeResource)
			fakeResource.DeprecatedScopesReturns([]db.DeprecatedScope{}, nil)
			fakePipeline.ResourceReturns(fakeResource, true, nil)

			result := callTool(server, "list_deprecated_scopes", map[string]any{
				"team":     "main",
				"pipeline": "my-pipeline",
				"resource": "my-resource",
			})
			var scopes []map[string]any
			Expect(json.Unmarshal([]byte(result), &scopes)).To(Succeed())
			Expect(scopes).To(BeEmpty())
		})
	})

	Describe("copy_resource_versions", func() {
		It("copies versions from a deprecated scope", func() {
			fakeResource := new(dbfakes.FakeResource)
			fakeResource.NameReturns("my-resource")
			fakeResource.DeprecatedScopesReturns([]db.DeprecatedScope{
				{ID: 42, DeprecatedAt: time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC), ConfigID: 17},
			}, nil)
			fakeResource.CopyVersionsFromScopeReturns(150, nil)
			fakePipeline.ResourceReturns(fakeResource, true, nil)

			result := callTool(server, "copy_resource_versions", map[string]any{
				"team":          "main",
				"pipeline":      "my-pipeline",
				"resource":      "my-resource",
				"from_scope_id": 42,
			})
			var output map[string]any
			Expect(json.Unmarshal([]byte(result), &output)).To(Succeed())
			Expect(output["success"]).To(BeTrue())
			Expect(output["versions_copied"]).To(BeEquivalentTo(150))
		})

		It("returns error when scope does not belong to resource", func() {
			fakeResource := new(dbfakes.FakeResource)
			fakeResource.NameReturns("my-resource")
			fakeResource.DeprecatedScopesReturns([]db.DeprecatedScope{
				{ID: 42, DeprecatedAt: time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC), ConfigID: 17},
			}, nil)
			fakePipeline.ResourceReturns(fakeResource, true, nil)

			_, isError := callToolRaw(server, "copy_resource_versions", map[string]any{
				"team":          "main",
				"pipeline":      "my-pipeline",
				"resource":      "my-resource",
				"from_scope_id": 99,
			})
			Expect(isError).To(BeTrue())
		})
	})

	Describe("list_agent_workflows", func() {
		It("returns workflow summaries with resolved live versions", func() {
			workflowsFactory.ListReturns([]workflow.Definition{
				{Name: "standard-dev", Description: "Standard dev flow", Version: 5, SchemaVersion: 3, SignatureVersion: 2, ContentHash: "abc123", Live: true, CreatedAt: 1700},
				{Name: "test-first", Description: "TDD flow", Version: 3, SchemaVersion: 2, SignatureVersion: 0, ContentHash: "def456", Live: false, CreatedAt: 1800},
			}, nil)
			workflowsFactory.LiveVersionsReturns(map[string]int{
				"standard-dev": 5,
				"test-first":   2,
			}, nil)

			result := callTool(server, "list_agent_workflows", map[string]any{})
			var summaries []map[string]any
			Expect(json.Unmarshal([]byte(result), &summaries)).To(Succeed())
			Expect(summaries).To(HaveLen(2))

			Expect(summaries[0]["name"]).To(Equal("standard-dev"))
			Expect(summaries[0]["latest_version"]).To(BeEquivalentTo(5))
			Expect(summaries[0]["schema_version"]).To(BeEquivalentTo(3))
			Expect(summaries[0]["signature_version"]).To(BeEquivalentTo(2))
			Expect(summaries[0]["live_version"]).To(BeEquivalentTo(5))
			Expect(summaries[0]["content_hash"]).To(Equal("abc123"))

			// second workflow is not live itself; live version resolved via Live()
			Expect(summaries[1]["name"]).To(Equal("test-first"))
			Expect(summaries[1]["latest_version"]).To(BeEquivalentTo(3))
			Expect(summaries[1]["live_version"]).To(BeEquivalentTo(2))
			// live versions resolve via ONE LiveVersions lookup, never a
			// per-name Live() fetch of the full definition
			Expect(workflowsFactory.LiveVersionsCallCount()).To(Equal(1))
			Expect(workflowsFactory.LiveCallCount()).To(BeZero())
		})

		It("returns error when listing fails", func() {
			workflowsFactory.ListReturns(nil, errors.New("boom"))
			_, isError := callToolRaw(server, "list_agent_workflows", map[string]any{})
			Expect(isError).To(BeTrue())
		})
	})

	Describe("get_agent_workflow", func() {
		It("returns a specific version when requested", func() {
			workflowsFactory.GetReturns(&workflow.Definition{
				Name: "standard-dev", Version: 4, SchemaVersion: 3, SignatureVersion: 6,
				ContentHash: "hash4", RawYAML: "name: standard-dev\n",
			}, true, nil)

			result := callTool(server, "get_agent_workflow", map[string]any{
				"workflow": "standard-dev",
				"version":  4,
			})
			var def map[string]any
			Expect(json.Unmarshal([]byte(result), &def)).To(Succeed())
			Expect(def["name"]).To(Equal("standard-dev"))
			Expect(def["version"]).To(BeEquivalentTo(4))
			Expect(def["schema_version"]).To(BeEquivalentTo(3))
			Expect(def["signature_version"]).To(BeEquivalentTo(6))
			Expect(def["raw_yaml"]).To(Equal("name: standard-dev\n"))

			name, version := workflowsFactory.GetArgsForCall(0)
			Expect(name).To(Equal("standard-dev"))
			Expect(version).To(Equal(4))
		})

		It("defaults to the live version when no version is given", func() {
			workflowsFactory.LiveReturns(&workflow.Definition{
				Name: "standard-dev", Version: 7, Live: true,
			}, true, nil)

			result := callTool(server, "get_agent_workflow", map[string]any{
				"workflow": "standard-dev",
			})
			var def map[string]any
			Expect(json.Unmarshal([]byte(result), &def)).To(Succeed())
			Expect(def["version"]).To(BeEquivalentTo(7))
			Expect(workflowsFactory.GetCallCount()).To(Equal(0))
		})

		It("falls back to the latest version when none is live", func() {
			workflowsFactory.LiveReturns(nil, false, nil)
			workflowsFactory.LatestReturns(&workflow.Definition{
				Name: "standard-dev", Version: 2,
			}, true, nil)

			result := callTool(server, "get_agent_workflow", map[string]any{
				"workflow": "standard-dev",
			})
			var def map[string]any
			Expect(json.Unmarshal([]byte(result), &def)).To(Succeed())
			Expect(def["version"]).To(BeEquivalentTo(2))

			// one lookup, not a full Versions scan plus a point Get
			Expect(workflowsFactory.LatestArgsForCall(0)).To(Equal("standard-dev"))
			Expect(workflowsFactory.VersionsCallCount()).To(BeZero())
			Expect(workflowsFactory.GetCallCount()).To(BeZero())
		})

		It("returns a not-found error for an unknown workflow", func() {
			workflowsFactory.LiveReturns(nil, false, nil)
			workflowsFactory.LatestReturns(nil, false, nil)

			result, isError := callToolRaw(server, "get_agent_workflow", map[string]any{
				"workflow": "nope",
			})
			Expect(isError).To(BeTrue())
			Expect(result).To(ContainSubstring("not found"))
		})

		It("returns a not-found error for an unknown version", func() {
			workflowsFactory.GetReturns(nil, false, nil)

			result, isError := callToolRaw(server, "get_agent_workflow", map[string]any{
				"workflow": "standard-dev",
				"version":  99,
			})
			Expect(isError).To(BeTrue())
			Expect(result).To(ContainSubstring("not found"))
		})
	})

	Describe("agent_cost_rollup", func() {
		It("rolls up ledger rows and totals them", func() {
			costLedgerFactory.RollupReturns([]budget.RollupRow{
				{Key: "2026-07-10", Entries: 3, InputTokens: 100, OutputTokens: 20, Turns: 6, CostUSD: 1.50},
				{Key: "2026-07-11", Entries: 2, InputTokens: 50, OutputTokens: 10, Turns: 4, CostUSD: 0.50},
			}, nil)

			result := callTool(server, "agent_cost_rollup", map[string]any{"group_by": "day"})
			var output map[string]any
			Expect(json.Unmarshal([]byte(result), &output)).To(Succeed())
			Expect(output["group_by"]).To(Equal("day"))

			rows := output["rows"].([]any)
			Expect(rows).To(HaveLen(2))

			summary := output["summary"].(map[string]any)
			Expect(summary["rows"]).To(BeEquivalentTo(2))
			Expect(summary["entries"]).To(BeEquivalentTo(5))
			Expect(summary["input_tokens"]).To(BeEquivalentTo(150))
			Expect(summary["turns"]).To(BeEquivalentTo(10))
			Expect(summary["cost_usd"]).To(BeEquivalentTo(2.0))

			groupBy, _, _ := costLedgerFactory.RollupArgsForCall(0)
			Expect(groupBy).To(Equal("day"))
		})

		It("defaults group_by to day", func() {
			costLedgerFactory.RollupReturns([]budget.RollupRow{}, nil)

			result := callTool(server, "agent_cost_rollup", map[string]any{})
			var output map[string]any
			Expect(json.Unmarshal([]byte(result), &output)).To(Succeed())
			Expect(output["group_by"]).To(Equal("day"))

			groupBy, _, _ := costLedgerFactory.RollupArgsForCall(0)
			Expect(groupBy).To(Equal(budget.GroupByDay))
		})

		It("rejects an invalid group_by", func() {
			_, isError := callToolRaw(server, "agent_cost_rollup", map[string]any{"group_by": "bogus"})
			Expect(isError).To(BeTrue())
			Expect(costLedgerFactory.RollupCallCount()).To(Equal(0))
		})

		It("passes an explicit until through to the ledger", func() {
			costLedgerFactory.RollupReturns([]budget.RollupRow{}, nil)

			callTool(server, "agent_cost_rollup", map[string]any{
				"group_by": "workflow",
				"since":    "2026-07-01",
				"until":    "2026-07-08",
			})
			groupBy, since, until := costLedgerFactory.RollupArgsForCall(0)
			Expect(groupBy).To(Equal("workflow"))
			Expect(since).To(Equal(time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)))
			Expect(until).To(Equal(time.Date(2026, 7, 8, 0, 0, 0, 0, time.UTC)))
		})
	})

	Describe("list_pipeline_runs", func() {
		It("returns runs for a template pipeline", func() {
			fakePipeline.IDReturns(77)

			run := new(dbfakes.FakePipelineRun)
			run.IDReturns(900)
			run.NumberReturns(3)
			run.StatusReturns(db.PipelineRunSucceeded)
			run.ParamsReturns(map[string]any{"branch": "main"})
			run.CreatedByReturns("alice")
			run.CreatedAtReturns(time.Unix(1000, 0))
			run.CompletedAtReturns(time.Unix(1200, 0), true)
			pipelineRunFactory.ListRunsReturns([]db.PipelineRun{run}, nil)

			result := callTool(server, "list_pipeline_runs", map[string]any{
				"team":     "main",
				"pipeline": "my-pipeline",
			})
			var runs []map[string]any
			Expect(json.Unmarshal([]byte(result), &runs)).To(Succeed())
			Expect(runs).To(HaveLen(1))
			Expect(runs[0]["number"]).To(BeEquivalentTo(3))
			Expect(runs[0]["status"]).To(Equal("succeeded"))
			Expect(runs[0]["params"].(map[string]any)["branch"]).To(Equal("main"))
			Expect(runs[0]["completed_at"]).To(BeEquivalentTo(1200))

			templateID, limit := pipelineRunFactory.ListRunsArgsForCall(0)
			Expect(templateID).To(Equal(77))
			Expect(limit).To(Equal(100))
		})

		It("passes a custom limit through", func() {
			fakePipeline.IDReturns(77)
			pipelineRunFactory.ListRunsReturns([]db.PipelineRun{}, nil)

			callTool(server, "list_pipeline_runs", map[string]any{
				"team":     "main",
				"pipeline": "my-pipeline",
				"limit":    5,
			})
			_, limit := pipelineRunFactory.ListRunsArgsForCall(0)
			Expect(limit).To(Equal(5))
		})

		It("returns error for unknown pipeline", func() {
			fakeTeam.PipelinesReturns([]db.Pipeline{}, nil)
			_, isError := callToolRaw(server, "list_pipeline_runs", map[string]any{
				"team":     "main",
				"pipeline": "ghost",
			})
			Expect(isError).To(BeTrue())
		})
	})

	Describe("get_pipeline_run", func() {
		It("returns a single run by number", func() {
			fakePipeline.IDReturns(77)

			run := new(dbfakes.FakePipelineRun)
			run.IDReturns(901)
			run.NumberReturns(4)
			run.StatusReturns(db.PipelineRunFailed)
			run.CreatedByReturns("bob")
			run.CreatedAtReturns(time.Unix(2000, 0))
			pipelineRunFactory.GetRunReturns(run, true, nil)

			result := callTool(server, "get_pipeline_run", map[string]any{
				"team":     "main",
				"pipeline": "my-pipeline",
				"number":   4,
			})
			var out map[string]any
			Expect(json.Unmarshal([]byte(result), &out)).To(Succeed())
			Expect(out["number"]).To(BeEquivalentTo(4))
			Expect(out["status"]).To(Equal("failed"))

			templateID, number := pipelineRunFactory.GetRunArgsForCall(0)
			Expect(templateID).To(Equal(77))
			Expect(number).To(Equal(4))
		})

		It("returns a not-found error for a missing run", func() {
			fakePipeline.IDReturns(77)
			pipelineRunFactory.GetRunReturns(nil, false, nil)

			result, isError := callToolRaw(server, "get_pipeline_run", map[string]any{
				"team":     "main",
				"pipeline": "my-pipeline",
				"number":   999,
			})
			Expect(isError).To(BeTrue())
			Expect(result).To(ContainSubstring("not found"))
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
