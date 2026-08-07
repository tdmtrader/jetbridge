package mcpserver_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/agent/budget"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/mcpserver"
	"github.com/concourse/concourse/atc/db"
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

func persistMCPPipeline(fixture *mcpToolsDB, name string, config atc.Config) db.Pipeline {
	GinkgoHelper()
	pipeline, _, err := fixture.Main.SavePipeline(
		atc.PipelineRef{Name: name}, config, db.ConfigVersion(0), false,
	)
	Expect(err).NotTo(HaveOccurred())
	return pipeline
}

func reloadMCPPipeline(fixture *mcpToolsDB, pipeline db.Pipeline) db.Pipeline {
	GinkgoHelper()
	loaded, found, err := fixture.Main.Pipeline(atc.PipelineRef{
		Name: pipeline.Name(), InstanceVars: pipeline.InstanceVars(),
	})
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	return loaded
}

func loadMCPJob(pipeline db.Pipeline, name string) db.Job {
	GinkgoHelper()
	job, found, err := pipeline.Job(name)
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue(), "job %q not found", name)
	return job
}

func loadMCPResource(pipeline db.Pipeline, name string) db.Resource {
	GinkgoHelper()
	resource, found, err := pipeline.Resource(name)
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue(), "resource %q not found", name)
	return resource
}

func persistMCPBuild(
	fixture *mcpToolsDB,
	job db.Job,
	plan atc.Plan,
	status db.BuildStatus,
	start time.Time,
	end time.Time,
) db.Build {
	GinkgoHelper()
	build, err := job.CreateBuild("mcp-fixture")
	Expect(err).NotTo(HaveOccurred())
	started, err := build.Start(plan)
	Expect(err).NotTo(HaveOccurred())
	Expect(started).To(BeTrue())
	if status != "" {
		Expect(build.Finish(status)).To(Succeed())
	}
	if !start.IsZero() || !end.IsZero() {
		_, err := fixture.Conn.Exec(
			`UPDATE builds SET start_time = $2, end_time = $3 WHERE id = $1`,
			build.ID(), start, end,
		)
		Expect(err).NotTo(HaveOccurred())
	}
	loaded, found, err := fixture.Deps.BuildFactory.Build(build.ID())
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	return loaded
}

func attachMCPResourceScope(
	fixture *mcpToolsDB,
	resource db.Resource,
	source atc.Source,
	versions ...atc.Version,
) db.ResourceConfigScope {
	GinkgoHelper()
	config, err := fixture.ResourceConfigFactory.FindOrCreateResourceConfig(
		resource.Type(), source, nil,
	)
	Expect(err).NotTo(HaveOccurred())
	resourceID := resource.ID()
	scope, err := config.FindOrCreateScope(&resourceID)
	Expect(err).NotTo(HaveOccurred())
	if len(versions) > 0 {
		Expect(scope.SaveVersions(db.SpanContext{}, versions)).To(Succeed())
	}
	Expect(resource.SetResourceConfigScope(scope)).To(Succeed())
	found, err := resource.Reload()
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	return scope
}

func mcpWorkflowYAML(name string, description string, prompt string) []byte {
	return []byte(fmt.Sprintf(`schema_version: 3
name: %q
description: %q
signature_version: 1
inputs: []
outputs: []
plan:
  - agent: work
    function_id: work
    prompt: %q
`, name, description, prompt))
}

func importMCPWorkflow(
	factory db.AgentWorkflowsFactory,
	name string,
	description string,
	prompt string,
) *workflow.Definition {
	GinkgoHelper()
	definition, err := factory.Import(
		name, mcpWorkflowYAML(name, description, prompt), "mcp-fixture",
	)
	Expect(err).NotTo(HaveOccurred())
	return definition
}

func promoteMCPWorkflow(
	factory db.AgentWorkflowsFactory,
	definition *workflow.Definition,
) *workflow.Definition {
	GinkgoHelper()
	_, err := factory.Promote(definition.Name, definition.Version, "mcp-fixture")
	Expect(err).NotTo(HaveOccurred())
	promoted, found, err := factory.Get(definition.Name, definition.Version)
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	return promoted
}

func insertMCPCost(factory db.AgentCostLedgerFactory, entry budget.LedgerEntry) {
	GinkgoHelper()
	entry.Source = budget.SourceCIAgent
	Expect(factory.Insert(entry)).To(Succeed())
}

type observedAgentWorkflowsFactory struct {
	db.AgentWorkflowsFactory
	listErr error

	listCalls         int
	liveVersionsCalls int
	liveNames         []string
	latestNames       []string
	getArgs           []struct {
		name    string
		version int
	}
	versionsCalls int
}

func (factory *observedAgentWorkflowsFactory) List() ([]workflow.Definition, error) {
	factory.listCalls++
	if factory.listErr != nil {
		return nil, factory.listErr
	}
	return factory.AgentWorkflowsFactory.List()
}

func (factory *observedAgentWorkflowsFactory) LiveVersions() (map[string]int, error) {
	factory.liveVersionsCalls++
	return factory.AgentWorkflowsFactory.LiveVersions()
}

func (factory *observedAgentWorkflowsFactory) Live(name string) (*workflow.Definition, bool, error) {
	factory.liveNames = append(factory.liveNames, name)
	return factory.AgentWorkflowsFactory.Live(name)
}

func (factory *observedAgentWorkflowsFactory) Latest(name string) (*workflow.Definition, bool, error) {
	factory.latestNames = append(factory.latestNames, name)
	return factory.AgentWorkflowsFactory.Latest(name)
}

func (factory *observedAgentWorkflowsFactory) Get(
	name string,
	version int,
) (*workflow.Definition, bool, error) {
	factory.getArgs = append(factory.getArgs, struct {
		name    string
		version int
	}{name: name, version: version})
	return factory.AgentWorkflowsFactory.Get(name, version)
}

func (factory *observedAgentWorkflowsFactory) Versions(
	ctx context.Context,
	name string,
	request workflow.VersionPageRequest,
) (workflow.VersionPage, error) {
	factory.versionsCalls++
	return factory.AgentWorkflowsFactory.Versions(ctx, name, request)
}

func callMCPToolJSON[T any](server *mcpserver.Server, name string, args map[string]any) T {
	GinkgoHelper()
	var decoded T
	Expect(json.Unmarshal([]byte(callTool(server, name, args)), &decoded)).To(Succeed())
	return decoded
}

func persistMCPRunTemplate(fixture *mcpToolsDB, name string) db.Pipeline {
	GinkgoHelper()
	return persistMCPPipeline(fixture, name, atc.Config{
		Template: true,
		Params: []atc.ParamSchema{{
			Name: "branch", Type: "string", Default: "main",
		}},
	})
}

func createMCPPipelineRun(
	fixture *mcpToolsDB,
	template db.Pipeline,
	params map[string]any,
	createdBy string,
) db.PipelineRun {
	GinkgoHelper()
	run, err := fixture.Deps.PipelineRunFactory.CreateRun(template.ID(), params, createdBy)
	Expect(err).NotTo(HaveOccurred())
	return run
}

func finishMCPPipelineRun(
	fixture *mcpToolsDB,
	template db.Pipeline,
	run db.PipelineRun,
	status db.PipelineRunStatus,
) db.PipelineRun {
	GinkgoHelper()
	Expect(run.Finish(status)).To(Succeed())
	loaded, found, err := fixture.Deps.PipelineRunFactory.GetRun(template.ID(), run.Number())
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	return loaded
}

func expectMCPPipelineRun(actual atc.PipelineRun, expected db.PipelineRun) {
	GinkgoHelper()
	Expect(actual.ID).To(Equal(expected.ID()))
	Expect(actual.Number).To(Equal(expected.Number()))
	Expect(actual.Status).To(Equal(string(expected.Status())))
	Expect(actual.Params).To(Equal(expected.Params()))
	Expect(actual.CreatedBy).To(Equal(expected.CreatedBy()))
	Expect(actual.CreatedAt).To(Equal(expected.CreatedAt().Unix()))
	completedAt, completed := expected.CompletedAt()
	if completed {
		Expect(actual.CompletedAt).To(Equal(completedAt.Unix()))
	} else {
		Expect(actual.CompletedAt).To(BeZero())
	}
	Expect(actual.Archived).To(Equal(expected.Archived()))
}

var _ = Describe("Tools", func() {
	Describe("tools/list", func() {
		It("returns all 25 tools", func() {
			server := newMCPToolsServer(mcpToolDeps{})
			body := jsonRPCBody("tools/list", 1, nil)
			resp := doMCP(server, body)
			defer func() { Expect(resp.Body.Close()).To(Succeed()) }()
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
			fixture := useMCPToolsDB()
			persisted := persistMCPPipeline(fixture, "my-pipeline", atc.Config{})
			server := newMCPToolsServer(fixture.Deps)

			result := callTool(server, "list_pipelines", map[string]any{"team": fixture.Main.Name()})
			var pipelines []map[string]any
			Expect(json.Unmarshal([]byte(result), &pipelines)).To(Succeed())
			Expect(pipelines).To(HaveLen(1))
			Expect(pipelines[0]["id"]).To(BeEquivalentTo(persisted.ID()))
			Expect(pipelines[0]["name"]).To(Equal(persisted.Name()))
			Expect(pipelines[0]["team_name"]).To(Equal(persisted.TeamName()))
			Expect(pipelines[0]["paused"]).To(Equal(persisted.Paused()))
			Expect(pipelines[0]["public"]).To(Equal(persisted.Public()))
			Expect(pipelines[0]["archived"]).To(Equal(persisted.Archived()))
		})

		It("returns error for unknown team", func() {
			fixture := useMCPToolsDB()
			server := newMCPToolsServer(fixture.Deps)
			result, isError := callToolRaw(server, "list_pipelines", map[string]any{"team": "nonexistent"})
			Expect(isError).To(BeTrue())
			Expect(result).To(ContainSubstring("not found"))
		})
	})

	Describe("get_pipeline", func() {
		It("returns pipeline config", func() {
			fixture := useMCPToolsDB()
			config := atc.Config{
				Jobs: atc.JobConfigs{{Name: "test-job"}},
			}
			pipeline := persistMCPPipeline(fixture, "my-pipeline", config)
			server := newMCPToolsServer(fixture.Deps)

			result := callTool(server, "get_pipeline", map[string]any{
				"team":     fixture.Main.Name(),
				"pipeline": pipeline.Name(),
			})
			var output map[string]any
			Expect(json.Unmarshal([]byte(result), &output)).To(Succeed())
			Expect(output["version"]).To(BeEquivalentTo(pipeline.ConfigVersion()))
			encodedConfig, err := json.Marshal(output["config"])
			Expect(err).NotTo(HaveOccurred())
			var returned atc.Config
			Expect(json.Unmarshal(encodedConfig, &returned)).To(Succeed())
			Expect(returned.Jobs).To(Equal(config.Jobs))
		})
	})

	Describe("pause_pipeline", func() {
		It("pauses the pipeline", func() {
			fixture := useMCPToolsDB()
			pipeline := persistMCPPipeline(fixture, "my-pipeline", atc.Config{})
			server := newMCPToolsServer(fixture.Deps)
			result := callTool(server, "pause_pipeline", map[string]any{
				"team":     fixture.Main.Name(),
				"pipeline": pipeline.Name(),
			})
			Expect(result).To(ContainSubstring("true"))
			Expect(reloadMCPPipeline(fixture, pipeline).Paused()).To(BeTrue())
		})
	})

	Describe("unpause_pipeline", func() {
		It("unpauses the pipeline", func() {
			fixture := useMCPToolsDB()
			pipeline := persistMCPPipeline(fixture, "my-pipeline", atc.Config{})
			Expect(pipeline.Pause("fixture")).To(Succeed())
			pipeline = reloadMCPPipeline(fixture, pipeline)
			Expect(pipeline.Paused()).To(BeTrue())
			server := newMCPToolsServer(fixture.Deps)
			result := callTool(server, "unpause_pipeline", map[string]any{
				"team":     fixture.Main.Name(),
				"pipeline": pipeline.Name(),
			})
			Expect(result).To(ContainSubstring("true"))
			Expect(reloadMCPPipeline(fixture, pipeline).Paused()).To(BeFalse())
		})
	})

	Describe("list_jobs", func() {
		It("returns jobs for a pipeline", func() {
			fixture := useMCPToolsDB()
			pipeline := persistMCPPipeline(fixture, "my-pipeline", atc.Config{
				Jobs: atc.JobConfigs{{Name: "build-it"}},
			})
			job := loadMCPJob(pipeline, "build-it")
			server := newMCPToolsServer(fixture.Deps)

			result := callTool(server, "list_jobs", map[string]any{
				"team":     fixture.Main.Name(),
				"pipeline": pipeline.Name(),
			})
			var jobs []map[string]any
			Expect(json.Unmarshal([]byte(result), &jobs)).To(Succeed())
			Expect(jobs).To(HaveLen(1))
			Expect(jobs[0]["name"]).To(Equal(job.Name()))
			Expect(jobs[0]["pipeline_name"]).To(Equal(job.PipelineName()))
			Expect(jobs[0]["team_name"]).To(Equal(job.TeamName()))
			Expect(jobs[0]["paused"]).To(BeNil())
		})
	})

	Describe("list_builds", func() {
		It("returns builds for a job", func() {
			fixture := useMCPToolsDB()
			pipeline := persistMCPPipeline(fixture, "my-pipeline", atc.Config{
				Jobs: atc.JobConfigs{{Name: "build-it"}},
			})
			job := loadMCPJob(pipeline, "build-it")
			start := time.Unix(1000, 0).UTC()
			end := time.Unix(1060, 0).UTC()
			persistedBuild := persistMCPBuild(
				fixture, job, atc.Plan{}, db.BuildStatusSucceeded, start, end,
			)
			server := newMCPToolsServer(fixture.Deps)

			result := callTool(server, "list_builds", map[string]any{
				"team":     fixture.Main.Name(),
				"pipeline": pipeline.Name(),
				"job":      job.Name(),
			})
			var builds []map[string]any
			Expect(json.Unmarshal([]byte(result), &builds)).To(Succeed())
			Expect(builds).To(HaveLen(1))
			Expect(builds[0]["id"]).To(BeEquivalentTo(persistedBuild.ID()))
			Expect(builds[0]["name"]).To(Equal(persistedBuild.Name()))
			Expect(builds[0]["status"]).To(Equal(string(persistedBuild.Status())))
			Expect(builds[0]["pipeline_name"]).To(Equal(persistedBuild.PipelineName()))
			Expect(builds[0]["job_name"]).To(Equal(persistedBuild.JobName()))
			Expect(builds[0]["team_name"]).To(Equal(persistedBuild.TeamName()))
			Expect(builds[0]["duration_seconds"]).To(BeEquivalentTo(60))
		})
	})

	Describe("get_build", func() {
		It("returns build details", func() {
			fixture := useMCPToolsDB()
			pipeline := persistMCPPipeline(fixture, "my-pipeline", atc.Config{
				Jobs: atc.JobConfigs{{Name: "test"}},
			})
			persistedBuild := persistMCPBuild(
				fixture,
				loadMCPJob(pipeline, "test"),
				atc.Plan{},
				db.BuildStatusFailed,
				time.Unix(2000, 0).UTC(),
				time.Unix(2120, 0).UTC(),
			)
			server := newMCPToolsServer(fixture.Deps)

			result := callTool(server, "get_build", map[string]any{"build_id": persistedBuild.ID()})
			var decoded map[string]any
			Expect(json.Unmarshal([]byte(result), &decoded)).To(Succeed())
			Expect(decoded["id"]).To(BeEquivalentTo(persistedBuild.ID()))
			Expect(decoded["status"]).To(Equal(string(persistedBuild.Status())))
			Expect(decoded["duration_seconds"]).To(BeEquivalentTo(120))
		})

		It("returns error for missing build", func() {
			fixture := useMCPToolsDB()
			pipeline := persistMCPPipeline(fixture, "my-pipeline", atc.Config{
				Jobs: atc.JobConfigs{{Name: "test"}},
			})
			existing := persistMCPBuild(
				fixture, loadMCPJob(pipeline, "test"), atc.Plan{}, "", time.Time{}, time.Time{},
			)
			missingID := existing.ID() + 1_000_000
			_, found, err := fixture.Deps.BuildFactory.BuildForAPI(missingID)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse())
			server := newMCPToolsServer(fixture.Deps)
			_, isError := callToolRaw(server, "get_build", map[string]any{"build_id": missingID})
			Expect(isError).To(BeTrue())
		})
	})

	Describe("trigger_job", func() {
		It("creates a build and returns info", func() {
			fixture := useMCPToolsDB()
			pipeline := persistMCPPipeline(fixture, "my-pipeline", atc.Config{
				Jobs: atc.JobConfigs{{Name: "deploy"}},
			})
			job := loadMCPJob(pipeline, "deploy")
			server := newMCPToolsServer(fixture.Deps)

			result := callTool(server, "trigger_job", map[string]any{
				"team":     fixture.Main.Name(),
				"pipeline": pipeline.Name(),
				"job":      job.Name(),
			})
			var output map[string]any
			Expect(json.Unmarshal([]byte(result), &output)).To(Succeed())
			buildName, ok := output["build_name"].(string)
			Expect(ok).To(BeTrue())
			build, found, err := job.Build(buildName)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(output["build_id"]).To(BeEquivalentTo(build.ID()))
			Expect(output["build_name"]).To(Equal(build.Name()))
			Expect(output["url"]).To(Equal(fmt.Sprintf(
				"https://concourse.example.com/teams/%s/pipelines/%s/jobs/%s/builds/%s",
				fixture.Main.Name(), pipeline.Name(), job.Name(), build.Name(),
			)))
			Expect(build.CreatedBy()).NotTo(BeNil())
			Expect(*build.CreatedBy()).To(Equal("mcp"))
		})
	})

	Describe("abort_build", func() {
		It("aborts the build", func() {
			fixture := useMCPToolsDB()
			pipeline := persistMCPPipeline(fixture, "my-pipeline", atc.Config{
				Jobs: atc.JobConfigs{{Name: "abort-me"}},
			})
			build := persistMCPBuild(
				fixture, loadMCPJob(pipeline, "abort-me"), atc.Plan{}, "", time.Time{}, time.Time{},
			)
			server := newMCPToolsServer(fixture.Deps)

			result := callTool(server, "abort_build", map[string]any{"build_id": build.ID()})
			Expect(result).To(ContainSubstring("true"))
			found, err := build.Reload()
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(build.IsAborted()).To(BeTrue())
		})
	})

	Describe("list_resources", func() {
		It("returns resources for a pipeline", func() {
			fixture := useMCPToolsDB()
			pipeline := persistMCPPipeline(fixture, "my-pipeline", atc.Config{
				Resources: atc.ResourceConfigs{{
					Name: "my-repo", Type: "git", Source: atc.Source{"uri": "example"},
				}},
			})
			resource := loadMCPResource(pipeline, "my-repo")
			server := newMCPToolsServer(fixture.Deps)

			result := callTool(server, "list_resources", map[string]any{
				"team":     fixture.Main.Name(),
				"pipeline": pipeline.Name(),
			})
			var resources []map[string]any
			Expect(json.Unmarshal([]byte(result), &resources)).To(Succeed())
			Expect(resources).To(HaveLen(1))
			Expect(resources[0]["name"]).To(Equal(resource.Name()))
			Expect(resources[0]["type"]).To(Equal(resource.Type()))
			Expect(resources[0]["pipeline_name"]).To(Equal(resource.PipelineName()))
			Expect(resources[0]["team_name"]).To(Equal(resource.TeamName()))
		})
	})

	Describe("check_resource", func() {
		It("triggers a resource check", func() {
			fixture := useMCPToolsDB()
			pipeline := persistMCPPipeline(fixture, "my-pipeline", atc.Config{
				Resources: atc.ResourceConfigs{{Name: "my-repo", Type: "git"}},
			})
			resource := loadMCPResource(pipeline, "my-repo")
			channel := fmt.Sprintf("resource_scan_%d", resource.ID())
			signal, err := fixture.Conn.Bus().ListenSignal(channel)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() {
				Expect(fixture.Conn.Bus().UnlistenSignal(channel, signal)).To(Succeed())
			})
			server := newMCPToolsServer(fixture.Deps)

			result := callTool(server, "check_resource", map[string]any{
				"team":     fixture.Main.Name(),
				"pipeline": pipeline.Name(),
				"resource": resource.Name(),
			})
			Expect(result).To(ContainSubstring("success"))
			Eventually(signal.C()).Should(Receive())
		})
	})

	Describe("list_teams", func() {
		It("returns all teams", func() {
			fixture := useMCPToolsDB()
			other, err := fixture.Deps.TeamFactory.CreateTeam(atc.Team{Name: "other"})
			Expect(err).NotTo(HaveOccurred())
			server := newMCPToolsServer(fixture.Deps)

			result := callTool(server, "list_teams", map[string]any{})
			var teams []map[string]any
			Expect(json.Unmarshal([]byte(result), &teams)).To(Succeed())
			Expect(teams).To(HaveLen(2))
			byName := map[string]int{}
			for _, team := range teams {
				byName[team["name"].(string)] = int(team["id"].(float64))
			}
			Expect(byName).To(Equal(map[string]int{
				fixture.Main.Name(): fixture.Main.ID(),
				other.Name():        other.ID(),
			}))
		})
	})

	Describe("get_info", func() {
		It("returns server info", func() {
			server := newMCPToolsServer(mcpToolDeps{})
			result := callTool(server, "get_info", map[string]any{})
			var info map[string]any
			Expect(json.Unmarshal([]byte(result), &info)).To(Succeed())
			Expect(info["version"]).To(Equal("1.0.0"))
			Expect(info["external_url"]).To(Equal("https://concourse.example.com"))
		})
	})

	Describe("get_build_plan", func() {
		It("returns the build plan", func() {
			fixture := useMCPToolsDB()
			pipeline := persistMCPPipeline(fixture, "my-pipeline", atc.Config{
				Jobs: atc.JobConfigs{{Name: "build-it"}},
			})
			build := persistMCPBuild(
				fixture,
				loadMCPJob(pipeline, "build-it"),
				atc.Plan{ID: "plan-1", Task: &atc.TaskPlan{Name: "build"}},
				"",
				time.Time{},
				time.Time{},
			)
			Expect(build.PublicPlan()).NotTo(BeNil())
			server := newMCPToolsServer(fixture.Deps)

			result := callTool(server, "get_build_plan", map[string]any{"build_id": build.ID()})
			Expect([]byte(result)).To(MatchJSON(*build.PublicPlan()))
		})
	})

	Describe("list_deprecated_scopes", func() {
		It("returns deprecated scopes for a resource", func() {
			previous := atc.EnableGlobalResources
			atc.EnableGlobalResources = false
			DeferCleanup(func() { atc.EnableGlobalResources = previous })
			fixture := useMCPToolsDB()
			pipeline := persistMCPPipeline(fixture, "my-pipeline", atc.Config{
				Resources: atc.ResourceConfigs{{
					Name: "my-resource", Type: "git", Source: atc.Source{"revision": "initial"},
				}},
			})
			resource := loadMCPResource(pipeline, "my-resource")
			initial := attachMCPResourceScope(fixture, resource, resource.Source())
			second := attachMCPResourceScope(
				fixture, resource, atc.Source{"revision": "second"},
			)
			attachMCPResourceScope(fixture, resource, atc.Source{"revision": "third"})
			deprecated, err := resource.DeprecatedScopes()
			Expect(err).NotTo(HaveOccurred())
			Expect(deprecated).To(HaveLen(2))
			server := newMCPToolsServer(fixture.Deps)

			result := callTool(server, "list_deprecated_scopes", map[string]any{
				"team":     fixture.Main.Name(),
				"pipeline": pipeline.Name(),
				"resource": resource.Name(),
			})
			var scopes []map[string]any
			Expect(json.Unmarshal([]byte(result), &scopes)).To(Succeed())
			Expect(scopes).To(HaveLen(2))
			actualIDs := []int{
				int(scopes[0]["id"].(float64)),
				int(scopes[1]["id"].(float64)),
			}
			Expect(actualIDs).To(ConsistOf(initial.ID(), second.ID()))
			persistedIDs := []int{deprecated[0].ID, deprecated[1].ID}
			Expect(actualIDs).To(ConsistOf(persistedIDs))
		})

		It("returns empty list when no deprecated scopes", func() {
			previous := atc.EnableGlobalResources
			atc.EnableGlobalResources = false
			DeferCleanup(func() { atc.EnableGlobalResources = previous })
			fixture := useMCPToolsDB()
			pipeline := persistMCPPipeline(fixture, "my-pipeline", atc.Config{
				Resources: atc.ResourceConfigs{{Name: "my-resource", Type: "git"}},
			})
			resource := loadMCPResource(pipeline, "my-resource")
			attachMCPResourceScope(fixture, resource, resource.Source())
			server := newMCPToolsServer(fixture.Deps)

			result := callTool(server, "list_deprecated_scopes", map[string]any{
				"team":     fixture.Main.Name(),
				"pipeline": pipeline.Name(),
				"resource": resource.Name(),
			})
			var scopes []map[string]any
			Expect(json.Unmarshal([]byte(result), &scopes)).To(Succeed())
			Expect(scopes).To(BeEmpty())
		})
	})

	Describe("copy_resource_versions", func() {
		It("copies versions from a deprecated scope", func() {
			previous := atc.EnableGlobalResources
			atc.EnableGlobalResources = false
			DeferCleanup(func() { atc.EnableGlobalResources = previous })
			fixture := useMCPToolsDB()
			pipeline := persistMCPPipeline(fixture, "my-pipeline", atc.Config{
				Resources: atc.ResourceConfigs{{
					Name: "my-resource", Type: "git", Source: atc.Source{"revision": "initial"},
				}},
			})
			resource := loadMCPResource(pipeline, "my-resource")
			persistedVersions := []atc.Version{
				{"ref": "v1"}, {"ref": "v2"}, {"ref": "v3"},
			}
			deprecatedScope := attachMCPResourceScope(
				fixture, resource, resource.Source(), persistedVersions...,
			)
			attachMCPResourceScope(fixture, resource, atc.Source{"revision": "current"})
			deprecated, err := resource.DeprecatedScopes()
			Expect(err).NotTo(HaveOccurred())
			Expect(deprecated).To(ContainElement(WithTransform(
				func(scope db.DeprecatedScope) int { return scope.ID }, Equal(deprecatedScope.ID()),
			)))
			server := newMCPToolsServer(fixture.Deps)

			result := callTool(server, "copy_resource_versions", map[string]any{
				"team":          fixture.Main.Name(),
				"pipeline":      pipeline.Name(),
				"resource":      resource.Name(),
				"from_scope_id": deprecatedScope.ID(),
			})
			var output map[string]any
			Expect(json.Unmarshal([]byte(result), &output)).To(Succeed())
			Expect(output["success"]).To(BeTrue())
			Expect(output["versions_copied"]).To(BeEquivalentTo(len(persistedVersions)))
			Expect(output["from_scope_id"]).To(BeEquivalentTo(deprecatedScope.ID()))
			resource = loadMCPResource(reloadMCPPipeline(fixture, pipeline), resource.Name())
			versions, _, _, err := resource.Versions(db.Page{Limit: 100}, nil)
			Expect(err).NotTo(HaveOccurred())
			actualVersions := make([]atc.Version, 0, len(versions))
			for _, version := range versions {
				actualVersions = append(actualVersions, version.Version)
			}
			Expect(actualVersions).To(ConsistOf(persistedVersions))
		})

		It("returns error when scope does not belong to resource", func() {
			previous := atc.EnableGlobalResources
			atc.EnableGlobalResources = false
			DeferCleanup(func() { atc.EnableGlobalResources = previous })
			fixture := useMCPToolsDB()
			pipeline := persistMCPPipeline(fixture, "my-pipeline", atc.Config{
				Resources: atc.ResourceConfigs{{Name: "my-resource", Type: "git"}},
			})
			resource := loadMCPResource(pipeline, "my-resource")
			attachMCPResourceScope(fixture, resource, resource.Source(), atc.Version{"ref": "old"})
			attachMCPResourceScope(fixture, resource, atc.Source{"revision": "current"})
			deprecated, err := resource.DeprecatedScopes()
			Expect(err).NotTo(HaveOccurred())
			Expect(deprecated).NotTo(BeEmpty())
			missingScopeID := deprecated[0].ID + 1_000_000
			for _, scope := range deprecated {
				Expect(scope.ID).NotTo(Equal(missingScopeID))
			}
			before, _, _, err := resource.Versions(db.Page{Limit: 100}, nil)
			Expect(err).NotTo(HaveOccurred())
			server := newMCPToolsServer(fixture.Deps)

			_, isError := callToolRaw(server, "copy_resource_versions", map[string]any{
				"team":          fixture.Main.Name(),
				"pipeline":      pipeline.Name(),
				"resource":      resource.Name(),
				"from_scope_id": missingScopeID,
			})
			Expect(isError).To(BeTrue())
			resource = loadMCPResource(reloadMCPPipeline(fixture, pipeline), resource.Name())
			after, _, _, err := resource.Versions(db.Page{Limit: 100}, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(after).To(HaveLen(len(before)))
		})
	})

	Describe("list_agent_workflows", func() {
		It("returns workflow summaries with resolved live versions", func() {
			fixture := useMCPToolsDB()
			standard := promoteMCPWorkflow(
				fixture.Deps.WorkflowsFactory,
				importMCPWorkflow(fixture.Deps.WorkflowsFactory, "standard-dev", "Standard dev flow", "first"),
			)
			testLive := promoteMCPWorkflow(
				fixture.Deps.WorkflowsFactory,
				importMCPWorkflow(fixture.Deps.WorkflowsFactory, "test-first", "TDD flow", "live"),
			)
			testLatest := importMCPWorkflow(
				fixture.Deps.WorkflowsFactory, "test-first", "TDD flow", "latest",
			)
			observer := &observedAgentWorkflowsFactory{
				AgentWorkflowsFactory: fixture.Deps.WorkflowsFactory,
			}
			deps := fixture.Deps
			deps.WorkflowsFactory = observer
			server := newMCPToolsServer(deps)

			result := callTool(server, "list_agent_workflows", map[string]any{})
			var summaries []map[string]any
			Expect(json.Unmarshal([]byte(result), &summaries)).To(Succeed())
			Expect(summaries).To(HaveLen(2))
			byName := map[string]map[string]any{}
			for _, summary := range summaries {
				byName[summary["name"].(string)] = summary
			}
			Expect(byName[standard.Name]["latest_version"]).To(BeEquivalentTo(standard.Version))
			Expect(byName[standard.Name]["live_version"]).To(BeEquivalentTo(standard.Version))
			Expect(byName[standard.Name]["schema_version"]).To(BeEquivalentTo(standard.SchemaVersion))
			Expect(byName[standard.Name]["signature_version"]).To(BeEquivalentTo(standard.SignatureVersion))
			Expect(byName[standard.Name]["content_hash"]).To(Equal(standard.ContentHash))
			Expect(byName[testLatest.Name]["latest_version"]).To(BeEquivalentTo(testLatest.Version))
			Expect(byName[testLatest.Name]["live_version"]).To(BeEquivalentTo(testLive.Version))
			Expect(byName[testLatest.Name]["schema_version"]).To(BeEquivalentTo(testLatest.SchemaVersion))
			Expect(byName[testLatest.Name]["signature_version"]).To(BeEquivalentTo(testLatest.SignatureVersion))
			Expect(byName[testLatest.Name]["content_hash"]).To(Equal(testLatest.ContentHash))
			Expect(observer.listCalls).To(Equal(1))
			Expect(observer.liveVersionsCalls).To(Equal(1))
			Expect(observer.liveNames).To(BeEmpty())
		})

		It("returns error when listing fails", func() {
			fixture := useMCPToolsDB()
			observer := &observedAgentWorkflowsFactory{
				AgentWorkflowsFactory: fixture.Deps.WorkflowsFactory,
				listErr:               errors.New("boom"),
			}
			deps := fixture.Deps
			deps.WorkflowsFactory = observer
			server := newMCPToolsServer(deps)
			_, isError := callToolRaw(server, "list_agent_workflows", map[string]any{})
			Expect(isError).To(BeTrue())
			Expect(observer.listCalls).To(Equal(1))
		})
	})

	Describe("get_agent_workflow", func() {
		It("returns a specific version when requested", func() {
			fixture := useMCPToolsDB()
			definition := importMCPWorkflow(
				fixture.Deps.WorkflowsFactory, "standard-dev", "Standard dev flow", "specific",
			)
			observer := &observedAgentWorkflowsFactory{
				AgentWorkflowsFactory: fixture.Deps.WorkflowsFactory,
			}
			deps := fixture.Deps
			deps.WorkflowsFactory = observer
			server := newMCPToolsServer(deps)

			result := callTool(server, "get_agent_workflow", map[string]any{
				"workflow": definition.Name,
				"version":  definition.Version,
			})
			var def map[string]any
			Expect(json.Unmarshal([]byte(result), &def)).To(Succeed())
			Expect(def["name"]).To(Equal(definition.Name))
			Expect(def["version"]).To(BeEquivalentTo(definition.Version))
			Expect(def["schema_version"]).To(BeEquivalentTo(definition.SchemaVersion))
			Expect(def["signature_version"]).To(BeEquivalentTo(definition.SignatureVersion))
			Expect(def["content_hash"]).To(Equal(definition.ContentHash))
			Expect(def["raw_yaml"]).To(Equal(definition.RawYAML))
			Expect(observer.getArgs).To(HaveLen(1))
			Expect(observer.getArgs[0].name).To(Equal(definition.Name))
			Expect(observer.getArgs[0].version).To(Equal(definition.Version))
		})

		It("defaults to the live version when no version is given", func() {
			fixture := useMCPToolsDB()
			definition := promoteMCPWorkflow(
				fixture.Deps.WorkflowsFactory,
				importMCPWorkflow(fixture.Deps.WorkflowsFactory, "standard-dev", "Standard dev flow", "live"),
			)
			observer := &observedAgentWorkflowsFactory{
				AgentWorkflowsFactory: fixture.Deps.WorkflowsFactory,
			}
			deps := fixture.Deps
			deps.WorkflowsFactory = observer
			server := newMCPToolsServer(deps)

			result := callTool(server, "get_agent_workflow", map[string]any{
				"workflow": definition.Name,
			})
			var def map[string]any
			Expect(json.Unmarshal([]byte(result), &def)).To(Succeed())
			Expect(def["version"]).To(BeEquivalentTo(definition.Version))
			Expect(def["content_hash"]).To(Equal(definition.ContentHash))
			Expect(observer.liveNames).To(Equal([]string{definition.Name}))
			Expect(observer.getArgs).To(BeEmpty())
		})

		It("falls back to the latest version when none is live", func() {
			fixture := useMCPToolsDB()
			importMCPWorkflow(
				fixture.Deps.WorkflowsFactory, "standard-dev", "Standard dev flow", "first",
			)
			latest := importMCPWorkflow(
				fixture.Deps.WorkflowsFactory, "standard-dev", "Standard dev flow", "latest",
			)
			observer := &observedAgentWorkflowsFactory{
				AgentWorkflowsFactory: fixture.Deps.WorkflowsFactory,
			}
			deps := fixture.Deps
			deps.WorkflowsFactory = observer
			server := newMCPToolsServer(deps)

			result := callTool(server, "get_agent_workflow", map[string]any{
				"workflow": latest.Name,
			})
			var def map[string]any
			Expect(json.Unmarshal([]byte(result), &def)).To(Succeed())
			Expect(def["version"]).To(BeEquivalentTo(latest.Version))
			Expect(def["content_hash"]).To(Equal(latest.ContentHash))

			// one lookup, not a full Versions scan plus a point Get
			Expect(observer.liveNames).To(Equal([]string{latest.Name}))
			Expect(observer.latestNames).To(Equal([]string{latest.Name}))
			Expect(observer.versionsCalls).To(BeZero())
			Expect(observer.getArgs).To(BeEmpty())
		})

		It("returns a not-found error for an unknown workflow", func() {
			fixture := useMCPToolsDB()
			observer := &observedAgentWorkflowsFactory{
				AgentWorkflowsFactory: fixture.Deps.WorkflowsFactory,
			}
			deps := fixture.Deps
			deps.WorkflowsFactory = observer
			server := newMCPToolsServer(deps)

			result, isError := callToolRaw(server, "get_agent_workflow", map[string]any{
				"workflow": "nope",
			})
			Expect(isError).To(BeTrue())
			Expect(result).To(ContainSubstring("not found"))
			Expect(observer.liveNames).To(Equal([]string{"nope"}))
			Expect(observer.latestNames).To(Equal([]string{"nope"}))
		})

		It("returns a not-found error for an unknown version", func() {
			fixture := useMCPToolsDB()
			definition := importMCPWorkflow(
				fixture.Deps.WorkflowsFactory, "standard-dev", "Standard dev flow", "only",
			)
			missingVersion := definition.Version + 1_000_000
			_, found, err := fixture.Deps.WorkflowsFactory.Get(definition.Name, missingVersion)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse())
			observer := &observedAgentWorkflowsFactory{
				AgentWorkflowsFactory: fixture.Deps.WorkflowsFactory,
			}
			deps := fixture.Deps
			deps.WorkflowsFactory = observer
			server := newMCPToolsServer(deps)

			result, isError := callToolRaw(server, "get_agent_workflow", map[string]any{
				"workflow": definition.Name,
				"version":  missingVersion,
			})
			Expect(isError).To(BeTrue())
			Expect(result).To(ContainSubstring("not found"))
			Expect(observer.getArgs).To(HaveLen(1))
			Expect(observer.getArgs[0].name).To(Equal(definition.Name))
			Expect(observer.getArgs[0].version).To(Equal(missingVersion))
		})
	})

	Describe("agent_cost_rollup", func() {
		It("rolls up ledger rows and totals them", func() {
			fixture := useMCPToolsDB()
			insertMCPCost(fixture.Deps.CostLedgerFactory, budget.LedgerEntry{
				OccurredAt: time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC), UserName: "alice",
				Model: "model-a", StepName: "first", InputTokens: 10, OutputTokens: 3,
				Turns: 1, CostUSD: 0.25,
			})
			insertMCPCost(fixture.Deps.CostLedgerFactory, budget.LedgerEntry{
				OccurredAt: time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC), UserName: "bob",
				Model: "model-b", StepName: "second", InputTokens: 7, OutputTokens: 4,
				Turns: 2, CostUSD: 0.50,
			})
			server := newMCPToolsServer(fixture.Deps)

			result := callTool(server, "agent_cost_rollup", map[string]any{
				"group_by": "day", "since": "2026-07-10", "until": "2026-07-12",
			})
			var output map[string]any
			Expect(json.Unmarshal([]byte(result), &output)).To(Succeed())
			Expect(output["group_by"]).To(Equal("day"))

			rows := output["rows"].([]any)
			Expect(rows).To(HaveLen(2))
			byKey := map[string]map[string]any{}
			for _, row := range rows {
				decoded := row.(map[string]any)
				byKey[decoded["key"].(string)] = decoded
			}
			Expect(byKey["2026-07-10"]["input_tokens"]).To(BeEquivalentTo(10))
			Expect(byKey["2026-07-10"]["output_tokens"]).To(BeEquivalentTo(3))
			Expect(byKey["2026-07-11"]["input_tokens"]).To(BeEquivalentTo(7))
			Expect(byKey["2026-07-11"]["output_tokens"]).To(BeEquivalentTo(4))

			summary := output["summary"].(map[string]any)
			Expect(summary["rows"]).To(BeEquivalentTo(2))
			Expect(summary["entries"]).To(BeEquivalentTo(2))
			Expect(summary["input_tokens"]).To(BeEquivalentTo(17))
			Expect(summary["output_tokens"]).To(BeEquivalentTo(7))
			Expect(summary["turns"]).To(BeEquivalentTo(3))
			Expect(summary["cost_usd"]).To(BeEquivalentTo(0.75))
		})

		It("defaults group_by to day", func() {
			fixture := useMCPToolsDB()
			for _, occurredAt := range []time.Time{
				time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC),
				time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC),
			} {
				insertMCPCost(fixture.Deps.CostLedgerFactory, budget.LedgerEntry{
					OccurredAt:  occurredAt,
					UserName:    "same-user",
					Model:       "same-model",
					StepName:    "same-step",
					InputTokens: 1,
					Turns:       1,
					CostUSD:     0.1,
				})
			}
			server := newMCPToolsServer(fixture.Deps)
			result := callTool(server, "agent_cost_rollup", map[string]any{
				"since": "2026-07-03", "until": "2026-07-05",
			})
			var output map[string]any
			Expect(json.Unmarshal([]byte(result), &output)).To(Succeed())
			Expect(output["group_by"]).To(Equal("day"))
			rows := output["rows"].([]any)
			Expect(rows).To(HaveLen(2))
			keys := make([]string, 0, len(rows))
			for _, row := range rows {
				keys = append(keys, row.(map[string]any)["key"].(string))
			}
			Expect(keys).To(ConsistOf("2026-07-03", "2026-07-04"))
		})

		It("rejects an invalid group_by", func() {
			server := newMCPToolsServer(mcpToolDeps{})
			_, isError := callToolRaw(server, "agent_cost_rollup", map[string]any{"group_by": "bogus"})
			Expect(isError).To(BeTrue())
		})

		It("applies an explicit half-open until boundary", func() {
			fixture := useMCPToolsDB()
			for _, entry := range []budget.LedgerEntry{
				{OccurredAt: time.Date(2026, 6, 30, 23, 59, 0, 0, time.UTC), StepName: "before", InputTokens: 100, CostUSD: 10},
				{OccurredAt: time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC), StepName: "inside", InputTokens: 11, OutputTokens: 5, Turns: 2, CostUSD: 0.5},
				{OccurredAt: time.Date(2026, 7, 8, 0, 0, 0, 0, time.UTC), StepName: "boundary", InputTokens: 200, CostUSD: 20},
				{OccurredAt: time.Date(2026, 7, 9, 0, 0, 0, 0, time.UTC), StepName: "after", InputTokens: 300, CostUSD: 30},
			} {
				insertMCPCost(fixture.Deps.CostLedgerFactory, entry)
			}
			server := newMCPToolsServer(fixture.Deps)
			result := callTool(server, "agent_cost_rollup", map[string]any{
				"group_by": "day",
				"since":    "2026-07-01",
				"until":    "2026-07-08",
			})
			var output map[string]any
			Expect(json.Unmarshal([]byte(result), &output)).To(Succeed())
			rows := output["rows"].([]any)
			Expect(rows).To(HaveLen(1))
			row := rows[0].(map[string]any)
			Expect(row["key"]).To(Equal("2026-07-02"))
			Expect(row["entries"]).To(BeEquivalentTo(1))
			Expect(row["input_tokens"]).To(BeEquivalentTo(11))
			Expect(row["output_tokens"]).To(BeEquivalentTo(5))
			Expect(row["turns"]).To(BeEquivalentTo(2))
			Expect(row["cost_usd"]).To(BeEquivalentTo(0.5))
		})
	})

	Describe("list_pipeline_runs", func() {
		It("returns runs for a template pipeline", func() {
			fixture := useMCPToolsDB()
			template := persistMCPRunTemplate(fixture, "my-pipeline")
			created := make([]db.PipelineRun, 0, 101)
			for range 101 {
				created = append(created, createMCPPipelineRun(
					fixture, template, map[string]any{"branch": "main"}, "alice",
				))
			}
			newest := finishMCPPipelineRun(
				fixture, template, created[len(created)-1], db.PipelineRunSucceeded,
			)
			created[len(created)-1] = newest
			server := newMCPToolsServer(fixture.Deps)
			result := callTool(server, "list_pipeline_runs", map[string]any{
				"team":     fixture.Main.Name(),
				"pipeline": template.Name(),
			})
			var runs []atc.PipelineRun
			Expect(json.Unmarshal([]byte(result), &runs)).To(Succeed())
			Expect(runs).To(HaveLen(100))
			expectedNumbers := make([]int, 0, 100)
			for i := len(created) - 1; i >= 1; i-- {
				expectedNumbers = append(expectedNumbers, created[i].Number())
			}
			actualNumbers := make([]int, 0, len(runs))
			for _, run := range runs {
				actualNumbers = append(actualNumbers, run.Number)
			}
			Expect(actualNumbers).To(Equal(expectedNumbers))
			Expect(actualNumbers).NotTo(ContainElement(created[0].Number()))
			expectMCPPipelineRun(runs[0], newest)
		})

		It("returns the newest runs for a custom limit", func() {
			fixture := useMCPToolsDB()
			template := persistMCPRunTemplate(fixture, "my-pipeline")
			created := make([]db.PipelineRun, 0, 6)
			for range 6 {
				created = append(created, createMCPPipelineRun(
					fixture, template, nil, "custom-limit",
				))
			}
			server := newMCPToolsServer(fixture.Deps)
			result := callTool(server, "list_pipeline_runs", map[string]any{
				"team":     fixture.Main.Name(),
				"pipeline": template.Name(),
				"limit":    5,
			})
			var runs []atc.PipelineRun
			Expect(json.Unmarshal([]byte(result), &runs)).To(Succeed())
			Expect(runs).To(HaveLen(5))
			expectedNumbers := make([]int, 0, 5)
			for i := len(created) - 1; i >= 1; i-- {
				expectedNumbers = append(expectedNumbers, created[i].Number())
			}
			actualNumbers := make([]int, 0, len(runs))
			for _, run := range runs {
				actualNumbers = append(actualNumbers, run.Number)
			}
			Expect(actualNumbers).To(Equal(expectedNumbers))
			Expect(actualNumbers).NotTo(ContainElement(created[0].Number()))
		})

		It("returns error for unknown pipeline", func() {
			fixture := useMCPToolsDB()
			persistMCPRunTemplate(fixture, "my-pipeline")
			server := newMCPToolsServer(fixture.Deps)
			_, isError := callToolRaw(server, "list_pipeline_runs", map[string]any{
				"team":     fixture.Main.Name(),
				"pipeline": "ghost",
			})
			Expect(isError).To(BeTrue())
		})
	})

	Describe("get_pipeline_run", func() {
		It("returns a single run by number", func() {
			fixture := useMCPToolsDB()
			template := persistMCPRunTemplate(fixture, "my-pipeline")
			run := createMCPPipelineRun(
				fixture, template, map[string]any{"branch": "feature"}, "bob",
			)
			run = finishMCPPipelineRun(fixture, template, run, db.PipelineRunFailed)
			server := newMCPToolsServer(fixture.Deps)
			result := callTool(server, "get_pipeline_run", map[string]any{
				"team":     fixture.Main.Name(),
				"pipeline": template.Name(),
				"number":   run.Number(),
			})
			var out atc.PipelineRun
			Expect(json.Unmarshal([]byte(result), &out)).To(Succeed())
			expectMCPPipelineRun(out, run)
		})

		It("returns a not-found error for a missing run", func() {
			fixture := useMCPToolsDB()
			template := persistMCPRunTemplate(fixture, "my-pipeline")
			existing := createMCPPipelineRun(fixture, template, nil, "existing")
			missingNumber := existing.Number() + 1_000_000
			_, found, err := fixture.Deps.PipelineRunFactory.GetRun(
				template.ID(), missingNumber,
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse())
			server := newMCPToolsServer(fixture.Deps)
			result, isError := callToolRaw(server, "get_pipeline_run", map[string]any{
				"team":     fixture.Main.Name(),
				"pipeline": template.Name(),
				"number":   missingNumber,
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
	defer func() { Expect(resp.Body.Close()).To(Succeed()) }()
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
