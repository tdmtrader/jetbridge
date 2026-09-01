package steps

import (
	"fmt"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	clientapi "github.com/concourse/concourse/go-concourse/concourse"
)

type JobClientState struct {
	API      *PipelineAPI
	Client   clientapi.Client
	Team     clientapi.Team
	Ref      atc.PipelineRef
	Job      db.Job
	BuildIDs []int
	Names    []string
	Count    int
	Found    bool
	Err      error
	PageNil  bool
	State    string
}

func JobClientDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, *JobClientState](
			"the production Go job client, real API, and PostgreSQL",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, _ brine.Params, rec *brine.Recorder, resources brine.Resources) (*JobClientState, error) {
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return nil, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				api, err := newPipelineAPI(database, rec)
				if err != nil {
					return nil, err
				}
				client := clientapi.NewClient(api.Server.URL, api.Client, false)
				return &JobClientState{
					API: api, Client: client, Team: client.Team("api-team"),
					Ref: atc.PipelineRef{Name: "target", InstanceVars: atc.InstanceVars{"branch": "master"}},
				}, nil
			},
		),
		brine.DefineMap[*JobClientState, *JobClientState](
			"the client team has a real instanced job",
			func(in *JobClientState, _ brine.Params, _ *brine.Recorder) (*JobClientState, error) {
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
		brine.DefineMap[*JobClientState, *JobClientState](
			"the real job has {int} persisted builds",
			func(in *JobClientState, p brine.Params, _ *brine.Recorder) (*JobClientState, error) {
				count, ok := p.GetInt(0)
				if !ok {
					return in, fmt.Errorf("expected build count")
				}
				for range count {
					build, err := in.Job.CreateBuild("brine-user")
					if err != nil {
						return in, err
					}
					in.BuildIDs = append(in.BuildIDs, build.ID())
				}
				return in, nil
			},
		),
		brine.DefineMap[*JobClientState, *JobClientState](
			"the Go client lists {string} jobs",
			func(in *JobClientState, p brine.Params, _ *brine.Recorder) (*JobClientState, error) {
				scope, _ := p.GetString(0)
				var jobs []atc.Job
				if scope == "pipeline" {
					jobs, in.Err = in.Team.ListJobs(in.Ref)
				} else if scope == "all" {
					jobs, in.Err = in.Client.ListAllJobs()
				} else {
					return in, fmt.Errorf("unknown job list scope %q", scope)
				}
				in.Names = nil
				for _, job := range jobs {
					in.Names = append(in.Names, job.Name)
				}
				return in, nil
			},
		),
		brine.DefineMap[*JobClientState, *JobClientState](
			"the Go client reads job {string}",
			func(in *JobClientState, p brine.Params, _ *brine.Recorder) (*JobClientState, error) {
				name, _ := p.GetString(0)
				job, found, err := in.Team.Job(in.Ref, name)
				in.Found, in.Err, in.Names = found, err, nil
				if found {
					in.Names = []string{job.Name}
				}
				return in, nil
			},
		),
		brine.DefineMap[*JobClientState, *JobClientState](
			"the Go client lists job builds with page {string}",
			func(in *JobClientState, p brine.Params, _ *brine.Recorder) (*JobClientState, error) {
				profile, _ := p.GetString(0)
				page, err := jobClientPage(profile, in.BuildIDs)
				if err != nil {
					return in, err
				}
				builds, pagination, found, err := in.Team.JobBuilds(in.Ref, "build", page)
				in.Count, in.Found, in.Err = len(builds), found, err
				in.PageNil = pagination.Previous == nil && pagination.Next == nil
				return in, nil
			},
		),
		brine.DefineMap[*JobClientState, *JobClientState](
			"the Go client lists builds for missing job",
			func(in *JobClientState, _ brine.Params, _ *brine.Recorder) (*JobClientState, error) {
				_, _, in.Found, in.Err = in.Team.JobBuilds(in.Ref, "missing", clientapi.Page{})
				return in, nil
			},
		),
		brine.DefineMap[*JobClientState, *JobClientState](
			"the Go client {string} job {string}",
			func(in *JobClientState, p brine.Params, _ *brine.Recorder) (*JobClientState, error) {
				operation, name, err := twoParams("the Go client {string} job {string}", p)
				if err != nil {
					return in, err
				}
				beforeSchedule := time.Time{}
				if in.Job != nil {
					beforeSchedule = in.Job.ScheduleRequestedTime()
				}
				switch operation {
				case "pauses":
					in.Found, in.Err = in.Team.PauseJob(in.Ref, name)
				case "unpauses":
					in.Found, in.Err = in.Team.UnpauseJob(in.Ref, name)
				case "schedules":
					in.Found, in.Err = in.Team.ScheduleJob(in.Ref, name)
				default:
					return in, fmt.Errorf("unknown job operation %q", operation)
				}
				if in.Err == nil && in.Found && in.Job != nil {
					found, reloadErr := in.Job.Reload()
					if reloadErr != nil || !found {
						return in, firstError(reloadErr, fmt.Errorf("job disappeared after %s", operation))
					}
					switch operation {
					case "pauses":
						in.State = fmt.Sprintf("paused=%t", in.Job.Paused())
					case "unpauses":
						in.State = fmt.Sprintf("paused=%t", in.Job.Paused())
					case "schedules":
						in.State = fmt.Sprintf("advanced=%t", in.Job.ScheduleRequestedTime().After(beforeSchedule))
					}
				}
				return in, nil
			},
		),
		brine.DefineCheck[*JobClientState]("the Go job client found the resource", func(in *JobClientState, _ brine.Params, _ *brine.Recorder) error {
			if !in.Found {
				return fmt.Errorf("job client did not find resource")
			}
			return nil
		}),
		brine.DefineCheck[*JobClientState]("the Go job client did not find the resource", func(in *JobClientState, _ brine.Params, _ *brine.Recorder) error {
			if in.Found {
				return fmt.Errorf("job client unexpectedly found resource")
			}
			return nil
		}),
		brine.DefineCheck[*JobClientState]("the Go job client returned no error", func(in *JobClientState, _ brine.Params, _ *brine.Recorder) error {
			return in.Err
		}),
		brine.DefineCheck[*JobClientState]("the Go job client returned jobs {string}", func(in *JobClientState, p brine.Params, _ *brine.Recorder) error {
			want, _ := p.GetString(0)
			if strings.Join(in.Names, ",") != want {
				return fmt.Errorf("expected jobs %q, got %q", want, strings.Join(in.Names, ","))
			}
			return nil
		}),
		CheckInt[*JobClientState]("the Go job client returned {int} build(s)", "job build count", func(in *JobClientState) (int, error) {
			return in.Count, nil
		}),
		brine.DefineCheck[*JobClientState]("the Go job client returned empty pagination", func(in *JobClientState, _ brine.Params, _ *brine.Recorder) error {
			if !in.PageNil {
				return fmt.Errorf("job build pagination was not empty")
			}
			return nil
		}),
		CheckString[*JobClientState]("the persisted job state is {string}", "job state", func(in *JobClientState) (string, error) {
			return in.State, nil
		}),
	}
}

func jobClientPage(profile string, ids []int) (clientapi.Page, error) {
	if profile == "all" {
		return clientapi.Page{}, nil
	}
	if len(ids) < 3 {
		return clientapi.Page{}, fmt.Errorf("page profile %q needs three builds", profile)
	}
	switch profile {
	case "from":
		return clientapi.Page{From: ids[0]}, nil
	case "from-limit":
		return clientapi.Page{From: ids[0], Limit: 1}, nil
	case "to":
		return clientapi.Page{To: ids[2]}, nil
	case "to-limit":
		return clientapi.Page{To: ids[2], Limit: 1}, nil
	case "from-to":
		return clientapi.Page{From: ids[0], To: ids[2]}, nil
	default:
		return clientapi.Page{}, fmt.Errorf("unknown job page profile %q", profile)
	}
}

func firstError(err error, fallback error) error {
	if err != nil {
		return err
	}
	return fallback
}
