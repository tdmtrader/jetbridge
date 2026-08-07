package mcpserver_test

import (
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

func callMCPToolJSON[T any](server *mcpserver.Server, name string, args map[string]any) T {
	GinkgoHelper()
	var decoded T
	Expect(json.Unmarshal([]byte(callTool(server, name, args)), &decoded)).To(Succeed())
	return decoded
}

func newFakeMCPWorkflowServer() (*mcpserver.Server, *dbfakes.FakeAgentWorkflowsFactory) {
	factory := new(dbfakes.FakeAgentWorkflowsFactory)
	return newMCPToolsServer(mcpToolDeps{WorkflowsFactory: factory}), factory
}

func newFakeMCPPipelineRunServer(
	fixture *mcpToolsDB,
) (*mcpserver.Server, db.Pipeline, *dbfakes.FakePipelineRunFactory) {
	pipeline := persistMCPPipeline(fixture, "my-pipeline", atc.Config{Template: true})
	factory := new(dbfakes.FakePipelineRunFactory)
	deps := fixture.Deps
	deps.PipelineRunFactory = factory
	return newMCPToolsServer(deps), pipeline, factory
}

var _ = Describe("Tools", func() {
	Describe("tools/list", func() {
		It("returns all 25 tools", func() {
			server := newMCPToolsServer(mcpToolDeps{})
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
		var (
			server           *mcpserver.Server
			workflowsFactory *dbfakes.FakeAgentWorkflowsFactory
		)

		BeforeEach(func() {
			server, workflowsFactory = newFakeMCPWorkflowServer()
		})

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
		var (
			server           *mcpserver.Server
			workflowsFactory *dbfakes.FakeAgentWorkflowsFactory
		)

		BeforeEach(func() {
			server, workflowsFactory = newFakeMCPWorkflowServer()
		})

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
		var (
			server            *mcpserver.Server
			costLedgerFactory *dbfakes.FakeAgentCostLedgerFactory
		)

		BeforeEach(func() {
			costLedgerFactory = new(dbfakes.FakeAgentCostLedgerFactory)
			server = newMCPToolsServer(mcpToolDeps{CostLedgerFactory: costLedgerFactory})
		})

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
		var (
			server             *mcpserver.Server
			fixture            *mcpToolsDB
			pipeline           db.Pipeline
			pipelineRunFactory *dbfakes.FakePipelineRunFactory
		)

		BeforeEach(func() {
			fixture = useMCPToolsDB()
			server, pipeline, pipelineRunFactory = newFakeMCPPipelineRunServer(fixture)
		})

		It("returns runs for a template pipeline", func() {
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
				"team":     fixture.Main.Name(),
				"pipeline": pipeline.Name(),
			})
			var runs []map[string]any
			Expect(json.Unmarshal([]byte(result), &runs)).To(Succeed())
			Expect(runs).To(HaveLen(1))
			Expect(runs[0]["number"]).To(BeEquivalentTo(3))
			Expect(runs[0]["status"]).To(Equal("succeeded"))
			Expect(runs[0]["params"].(map[string]any)["branch"]).To(Equal("main"))
			Expect(runs[0]["completed_at"]).To(BeEquivalentTo(1200))

			templateID, limit := pipelineRunFactory.ListRunsArgsForCall(0)
			Expect(templateID).To(Equal(pipeline.ID()))
			Expect(limit).To(Equal(100))
		})

		It("passes a custom limit through", func() {
			pipelineRunFactory.ListRunsReturns([]db.PipelineRun{}, nil)

			callTool(server, "list_pipeline_runs", map[string]any{
				"team":     fixture.Main.Name(),
				"pipeline": pipeline.Name(),
				"limit":    5,
			})
			_, limit := pipelineRunFactory.ListRunsArgsForCall(0)
			Expect(limit).To(Equal(5))
		})

		It("returns error for unknown pipeline", func() {
			_, isError := callToolRaw(server, "list_pipeline_runs", map[string]any{
				"team":     fixture.Main.Name(),
				"pipeline": "ghost",
			})
			Expect(isError).To(BeTrue())
		})
	})

	Describe("get_pipeline_run", func() {
		var (
			server             *mcpserver.Server
			fixture            *mcpToolsDB
			pipeline           db.Pipeline
			pipelineRunFactory *dbfakes.FakePipelineRunFactory
		)

		BeforeEach(func() {
			fixture = useMCPToolsDB()
			server, pipeline, pipelineRunFactory = newFakeMCPPipelineRunServer(fixture)
		})

		It("returns a single run by number", func() {
			run := new(dbfakes.FakePipelineRun)
			run.IDReturns(901)
			run.NumberReturns(4)
			run.StatusReturns(db.PipelineRunFailed)
			run.CreatedByReturns("bob")
			run.CreatedAtReturns(time.Unix(2000, 0))
			pipelineRunFactory.GetRunReturns(run, true, nil)

			result := callTool(server, "get_pipeline_run", map[string]any{
				"team":     fixture.Main.Name(),
				"pipeline": pipeline.Name(),
				"number":   4,
			})
			var out map[string]any
			Expect(json.Unmarshal([]byte(result), &out)).To(Succeed())
			Expect(out["number"]).To(BeEquivalentTo(4))
			Expect(out["status"]).To(Equal("failed"))

			templateID, number := pipelineRunFactory.GetRunArgsForCall(0)
			Expect(templateID).To(Equal(pipeline.ID()))
			Expect(number).To(Equal(4))
		})

		It("returns a not-found error for a missing run", func() {
			pipelineRunFactory.GetRunReturns(nil, false, nil)

			result, isError := callToolRaw(server, "get_pipeline_run", map[string]any{
				"team":     fixture.Main.Name(),
				"pipeline": pipeline.Name(),
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
