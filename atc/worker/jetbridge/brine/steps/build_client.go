package steps

import (
	"fmt"
	"strconv"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	clientapi "github.com/concourse/concourse/go-concourse/concourse"
)

type BuildClientState struct {
	API       *PipelineAPI
	Client    clientapi.Client
	Team      clientapi.Team
	Ref       atc.PipelineRef
	Job       db.Job
	Build     db.Build
	BuildIDs  []int
	BuildName string
	Found     bool
	Err       error
	Count     int
	PageNil   bool
	State     string
}

func BuildClientDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, *BuildClientState](
			"the production Go build client, real API, and PostgreSQL",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, _ brine.Params, rec *brine.Recorder, resources brine.Resources) (*BuildClientState, error) {
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return nil, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				api, err := newPipelineAPI(database, rec)
				if err != nil {
					return nil, err
				}
				client := clientapi.NewClient(api.Server.URL, api.Client, false)
				return &BuildClientState{
					API: api, Client: client, Team: client.Team("api-team"),
					Ref: atc.PipelineRef{Name: "target", InstanceVars: atc.InstanceVars{"branch": "master"}},
				}, nil
			},
		),
		brine.DefineMap[*BuildClientState, *BuildClientState](
			"the build client team has a real instanced job",
			func(in *BuildClientState, _ brine.Params, _ *brine.Recorder) (*BuildClientState, error) {
				_, _, err := in.API.Team.SavePipeline(in.Ref, atc.Config{Jobs: atc.JobConfigs{{
					Name: "build", Public: true,
					PlanSequence: []atc.Step{{Config: &atc.TaskStep{Config: &atc.TaskConfig{
						Run: atc.TaskRunConfig{Path: "true"},
					}}}},
				}}}, 0, false)
				if err != nil {
					return in, err
				}
				pipeline, found, err := in.API.Team.Pipeline(in.Ref)
				if err != nil || !found {
					return in, firstError(err, fmt.Errorf("saved pipeline was not found"))
				}
				in.Job, found, err = pipeline.Job("build")
				if err != nil || !found {
					return in, firstError(err, fmt.Errorf("saved job was not found"))
				}
				return in, nil
			},
		),
		brine.DefineMap[*BuildClientState, *BuildClientState](
			"the build client has a persisted {string} build",
			func(in *BuildClientState, p brine.Params, _ *brine.Recorder) (*BuildClientState, error) {
				kind, _ := p.GetString(0)
				var build db.Build
				var err error
				if kind == "one-off" {
					build, err = in.API.Team.CreateStartedBuild(atc.Plan{
						ID: "persisted-client-build",
						Task: &atc.TaskPlan{Config: &atc.TaskConfig{
							Run: atc.TaskRunConfig{Path: "true"},
						}},
					})
				} else if kind == "job" {
					if in.Job == nil {
						return in, fmt.Errorf("real job is not configured")
					}
					build, err = in.Job.CreateBuild("brine-user")
				} else {
					return in, fmt.Errorf("unknown build kind %q", kind)
				}
				if err != nil {
					return in, err
				}
				in.Build, in.BuildName = build, build.Name()
				in.BuildIDs = append(in.BuildIDs, build.ID())
				return in, nil
			},
		),
		brine.DefineMap[*BuildClientState, *BuildClientState](
			"the build client has {int} persisted one-off builds",
			func(in *BuildClientState, p brine.Params, _ *brine.Recorder) (*BuildClientState, error) {
				count, ok := p.GetInt(0)
				if !ok {
					return in, fmt.Errorf("expected build count")
				}
				for range count {
					build, err := in.API.Team.CreateOneOffBuild()
					if err != nil {
						return in, err
					}
					in.BuildIDs = append(in.BuildIDs, build.ID())
				}
				return in, nil
			},
		),
		brine.DefineMap[*BuildClientState, *BuildClientState](
			"the Go client creates a one-off build",
			func(in *BuildClientState, _ brine.Params, _ *brine.Recorder) (*BuildClientState, error) {
				build, err := in.Team.CreateBuild(atc.Plan{ID: "client-build", Task: &atc.TaskPlan{Config: &atc.TaskConfig{
					Run: atc.TaskRunConfig{Path: "true"},
				}}})
				in.Err, in.BuildName, in.State = err, build.Name, fmt.Sprintf("id-positive=%t;status=%s", build.ID > 0, build.Status)
				return in, nil
			},
		),
		brine.DefineMap[*BuildClientState, *BuildClientState](
			"the Go client creates a job build",
			func(in *BuildClientState, _ brine.Params, _ *brine.Recorder) (*BuildClientState, error) {
				build, err := in.Team.CreateJobBuild(in.Ref, "build")
				in.Err, in.BuildName, in.State = err, build.Name, fmt.Sprintf("id-positive=%t;status=%s", build.ID > 0, build.Status)
				return in, nil
			},
		),
		brine.DefineMap[*BuildClientState, *BuildClientState](
			"the Go client reads the {string} build",
			func(in *BuildClientState, p brine.Params, _ *brine.Recorder) (*BuildClientState, error) {
				kind, _ := p.GetString(0)
				if kind == "global" {
					build, found, err := in.Client.Build(strconv.Itoa(in.Build.ID()))
					in.Found, in.Err, in.State = found, err, fmt.Sprintf("id=%d;name=%s", build.ID, build.Name)
				} else if kind == "job" {
					build, found, err := in.Team.JobBuild(in.Ref, "build", in.BuildName)
					in.Found, in.Err, in.State = found, err, fmt.Sprintf("id=%d;name=%s", build.ID, build.Name)
				} else {
					return in, fmt.Errorf("unknown build lookup kind %q", kind)
				}
				return in, nil
			},
		),
		brine.DefineMap[*BuildClientState, *BuildClientState](
			"the Go client reads a missing {string} build",
			func(in *BuildClientState, p brine.Params, _ *brine.Recorder) (*BuildClientState, error) {
				kind, _ := p.GetString(0)
				if kind == "global" {
					_, in.Found, in.Err = in.Client.Build("999999999")
				} else if kind == "job" {
					_, in.Found, in.Err = in.Team.JobBuild(in.Ref, "build", "missing")
				} else {
					return in, fmt.Errorf("unknown missing lookup kind %q", kind)
				}
				return in, nil
			},
		),
		brine.DefineMap[*BuildClientState, *BuildClientState](
			"the Go client lists {string} builds with page {string}",
			func(in *BuildClientState, p brine.Params, _ *brine.Recorder) (*BuildClientState, error) {
				scope, profile, err := twoParams("the Go client lists {string} builds with page {string}", p)
				if err != nil {
					return in, err
				}
				page, err := jobClientPage(profile, in.BuildIDs)
				if err != nil {
					return in, err
				}
				var builds []atc.Build
				var pagination clientapi.Pagination
				if scope == "global" {
					builds, pagination, in.Err = in.Client.Builds(page)
				} else if scope == "team" {
					builds, pagination, in.Err = in.Team.Builds(page)
				} else {
					return in, fmt.Errorf("unknown build list scope %q", scope)
				}
				in.Count = len(builds)
				in.PageNil = pagination.Previous == nil && pagination.Next == nil
				return in, nil
			},
		),
		brine.DefineMap[*BuildClientState, *BuildClientState](
			"the Go client aborts the persisted build",
			func(in *BuildClientState, _ brine.Params, _ *brine.Recorder) (*BuildClientState, error) {
				in.Err = in.Client.AbortBuild(strconv.Itoa(in.Build.ID()))
				if in.Err != nil {
					return in, nil
				}
				found, err := in.Build.Reload()
				if err != nil || !found {
					return in, firstError(err, fmt.Errorf("aborted build disappeared"))
				}
				in.State = fmt.Sprintf("aborted=%t", in.Build.IsAborted())
				return in, nil
			},
		),
		brine.DefineCheck[*BuildClientState]("the Go build client found the resource", func(in *BuildClientState, _ brine.Params, _ *brine.Recorder) error {
			if !in.Found {
				return fmt.Errorf("build client did not find resource")
			}
			return nil
		}),
		brine.DefineCheck[*BuildClientState]("the Go build client did not find the resource", func(in *BuildClientState, _ brine.Params, _ *brine.Recorder) error {
			if in.Found {
				return fmt.Errorf("build client unexpectedly found resource")
			}
			return nil
		}),
		brine.DefineCheck[*BuildClientState]("the Go build client returned no error", func(in *BuildClientState, _ brine.Params, _ *brine.Recorder) error {
			return in.Err
		}),
		CheckInt[*BuildClientState]("the Go build client returned {int} build(s)", "build list count", func(in *BuildClientState) (int, error) {
			return in.Count, nil
		}),
		brine.DefineCheck[*BuildClientState]("the Go build client returned empty pagination", func(in *BuildClientState, _ brine.Params, _ *brine.Recorder) error {
			if !in.PageNil {
				return fmt.Errorf("build pagination was not empty")
			}
			return nil
		}),
		CheckString[*BuildClientState]("the Go build client state is {string}", "build client state", func(in *BuildClientState) (string, error) {
			return in.State, nil
		}),
	}
}
