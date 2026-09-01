package steps

import (
	"fmt"
	"strings"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	clientapi "github.com/concourse/concourse/go-concourse/concourse"
)

type PipelineClientState struct {
	API        *strictPipelineClientAPI
	Client     clientapi.Client
	Team       clientapi.Team
	Found      bool
	Err        error
	Names      []string
	Pipeline   atc.Pipeline
	Pipelines  []atc.Pipeline
	Build      atc.Build
	Builds     []atc.Build
	Pagination clientapi.Pagination
	BuildID    int
	PageNil    bool
}

func PipelineClientDefinitions() []brine.StepDefinition {
	definitions := []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, *PipelineClientState](
			"the production Go pipeline client, real API, and PostgreSQL",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, _ brine.Params, rec *brine.Recorder, resources brine.Resources) (*PipelineClientState, error) {
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return nil, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				api, err := newStrictPipelineClientAPI(database, rec)
				if err != nil {
					return nil, err
				}
				client := clientapi.NewClient(api.URL, api.Client, false)
				return &PipelineClientState{API: api, Client: client, Team: client.Team("api-team")}, nil
			},
		),
		brine.DefineMap[*PipelineClientState, *PipelineClientState](
			"the client team has named pipelines {string}",
			func(in *PipelineClientState, p brine.Params, _ *brine.Recorder) (*PipelineClientState, error) {
				names, ok := p.GetString(0)
				if !ok {
					return in, fmt.Errorf("expected pipeline names")
				}
				for _, name := range strings.Split(names, ",") {
					if err := in.API.save(strings.TrimSpace(name)); err != nil {
						return in, err
					}
				}
				return in, nil
			},
		),
		brine.DefineMap[*PipelineClientState, *PipelineClientState](
			"the client team has instanced pipeline {string}",
			func(in *PipelineClientState, p brine.Params, _ *brine.Recorder) (*PipelineClientState, error) {
				name, ok := p.GetString(0)
				if !ok {
					return in, fmt.Errorf("expected pipeline name")
				}
				pipeline, _, err := in.API.Team.SavePipeline(
					clientPipelineRef(name),
					atc.Config{
						Jobs:    atc.JobConfigs{{Name: "build"}},
						Groups:  atc.GroupConfigs{{Name: "all", Jobs: []string{"build"}}},
						Display: &atc.DisplayConfig{BackgroundImage: "brine-background.jpg"},
					},
					0,
					false,
				)
				if err == nil {
					in.API.Saved[name] = pipeline
				}
				return in, err
			},
		),
		brine.DefineMap[*PipelineClientState, *PipelineClientState](
			"the Go client {string} instanced pipeline {string}",
			func(in *PipelineClientState, p brine.Params, _ *brine.Recorder) (*PipelineClientState, error) {
				operation, name, err := twoParams("the Go client {string} instanced pipeline {string}", p)
				if err != nil {
					return in, err
				}
				ref := clientPipelineRef(name)
				switch operation {
				case "pauses":
					in.Found, in.Err = in.Team.PausePipeline(ref)
				case "archives":
					in.Found, in.Err = in.Team.ArchivePipeline(ref)
				case "unpauses":
					in.Found, in.Err = in.Team.UnpausePipeline(ref)
				case "exposes":
					in.Found, in.Err = in.Team.ExposePipeline(ref)
				case "hides":
					in.Found, in.Err = in.Team.HidePipeline(ref)
				case "deletes":
					in.Found, in.Err = in.Team.DeletePipeline(ref)
				default:
					return in, fmt.Errorf("unknown pipeline client operation %q", operation)
				}
				return in, nil
			},
		),
		brine.DefineMap[*PipelineClientState, *PipelineClientState](
			"the Go client reads instanced pipeline {string}",
			func(in *PipelineClientState, p brine.Params, _ *brine.Recorder) (*PipelineClientState, error) {
				name, _ := p.GetString(0)
				pipeline, found, err := in.Team.Pipeline(clientPipelineRef(name))
				in.Found, in.Err = found, err
				in.Pipeline = pipeline
				in.Names = nil
				if found {
					in.Names = []string{pipeline.Name}
				}
				return in, nil
			},
		),
		brine.DefineMap[*PipelineClientState, *PipelineClientState](
			"the Go client lists {string} pipelines",
			func(in *PipelineClientState, p brine.Params, _ *brine.Recorder) (*PipelineClientState, error) {
				scope, _ := p.GetString(0)
				var pipelines []atc.Pipeline
				if scope == "team" {
					pipelines, in.Err = in.Team.ListPipelines()
				} else if scope == "all" {
					pipelines, in.Err = in.Client.ListPipelines()
				} else {
					return in, fmt.Errorf("unknown pipeline list scope %q", scope)
				}
				in.Names = nil
				for _, pipeline := range pipelines {
					in.Names = append(in.Names, pipeline.Name)
				}
				in.Pipelines = pipelines
				return in, nil
			},
		),
		brine.DefineMap[*PipelineClientState, *PipelineClientState](
			"the Go client orders named pipelines as {string}",
			func(in *PipelineClientState, p brine.Params, _ *brine.Recorder) (*PipelineClientState, error) {
				raw, _ := p.GetString(0)
				in.Err = in.Team.OrderingPipelines(strings.Split(raw, ","))
				return in, nil
			},
		),
		brine.DefineMap[*PipelineClientState, *PipelineClientState](
			"the Go client renames pipeline {string} to {string}",
			func(in *PipelineClientState, p brine.Params, _ *brine.Recorder) (*PipelineClientState, error) {
				oldName, newName, err := twoParams("the Go client renames pipeline {string} to {string}", p)
				if err != nil {
					return in, err
				}
				in.Found, _, in.Err = in.Team.RenamePipeline(oldName, newName)
				return in, nil
			},
		),
		brine.DefineMap[*PipelineClientState, *PipelineClientState](
			"the Go client creates a build for instanced pipeline {string}",
			func(in *PipelineClientState, p brine.Params, _ *brine.Recorder) (*PipelineClientState, error) {
				name, _ := p.GetString(0)
				build, err := in.Team.CreatePipelineBuild(clientPipelineRef(name), atc.Plan{ID: "client-plan", Task: &atc.TaskPlan{Config: &atc.TaskConfig{Run: atc.TaskRunConfig{Path: "true"}}}})
				in.Err, in.BuildID, in.Build = err, build.ID, build
				return in, nil
			},
		),
		brine.DefineMap[*PipelineClientState, *PipelineClientState](
			"the Go client lists builds for instanced pipeline {string}",
			func(in *PipelineClientState, p brine.Params, _ *brine.Recorder) (*PipelineClientState, error) {
				name, _ := p.GetString(0)
				builds, pagination, found, err := in.Team.PipelineBuilds(clientPipelineRef(name), clientapi.Page{})
				in.Err, in.Found = err, found
				in.BuildID, in.Builds, in.Pagination = len(builds), builds, pagination
				in.PageNil = pagination.Previous == nil && pagination.Next == nil
				return in, nil
			},
		),
		brine.DefineCheck[*PipelineClientState]("the Go client found the resource", func(in *PipelineClientState, _ brine.Params, _ *brine.Recorder) error {
			if !in.Found {
				return fmt.Errorf("client did not find resource")
			}
			return nil
		}),
		brine.DefineCheck[*PipelineClientState]("the Go client did not find the resource", func(in *PipelineClientState, _ brine.Params, _ *brine.Recorder) error {
			if in.Found {
				return fmt.Errorf("client unexpectedly found resource")
			}
			return nil
		}),
		brine.DefineCheck[*PipelineClientState]("the Go client returned no error", func(in *PipelineClientState, _ brine.Params, _ *brine.Recorder) error { return in.Err }),
	}
	return append(definitions, pipelineClientStrictDefinitions()...)
}

func clientPipelineRef(name string) atc.PipelineRef {
	return atc.PipelineRef{Name: name, InstanceVars: atc.InstanceVars{"branch": "master"}}
}
