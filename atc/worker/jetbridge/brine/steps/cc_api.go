package steps

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

type CCAPIObservation struct {
	Status      int
	ContentType string
	Body        string
}

func CCAPIDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, CCAPIObservation](
			"the real CC API handles profile {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, rec *brine.Recorder, resources brine.Resources) (CCAPIObservation, error) {
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return CCAPIObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				profile, _ := p.GetString(0)
				return observeCCAPI(database, profile, rec)
			},
		),
		CheckInt[CCAPIObservation]("the CC API returned status {int}", "CC API status", func(in CCAPIObservation) (int, error) { return in.Status, nil }),
		CheckString[CCAPIObservation]("the CC API content type is {string}", "CC API content type", func(in CCAPIObservation) (string, error) { return in.ContentType, nil }),
		brine.DefineCheck[CCAPIObservation]("the CC XML contains {string}", func(in CCAPIObservation, p brine.Params, _ *brine.Recorder) error {
			want, _ := p.GetString(0)
			if !strings.Contains(in.Body, want) {
				return fmt.Errorf("CC XML %q does not contain %q", in.Body, want)
			}
			return nil
		}),
		brine.DefineCheck[CCAPIObservation]("the CC XML activity is {string}", func(in CCAPIObservation, p brine.Params, _ *brine.Recorder) error {
			want, _ := p.GetString(0)
			if !strings.Contains(in.Body, `activity="`+want+`"`) {
				return fmt.Errorf("CC XML %q does not have activity %q", in.Body, want)
			}
			return nil
		}),
		brine.DefineCheck[CCAPIObservation]("the CC XML build status is {string}", func(in CCAPIObservation, p brine.Params, _ *brine.Recorder) error {
			want, _ := p.GetString(0)
			if !strings.Contains(in.Body, `lastBuildStatus="`+want+`"`) {
				return fmt.Errorf("CC XML %q does not have build status %q", in.Body, want)
			}
			return nil
		}),
		brine.DefineCheck[CCAPIObservation]("the CC XML is empty", func(in CCAPIObservation, _ brine.Params, _ *brine.Recorder) error {
			if strings.Contains(in.Body, "<Project ") {
				return fmt.Errorf("CC XML unexpectedly contained a project: %s", in.Body)
			}
			return nil
		}),
	}
}

func observeCCAPI(database JetbridgeDB, profile string, rec *brine.Recorder) (CCAPIObservation, error) {
	api, err := newPipelineAPI(database, rec)
	if err != nil {
		return CCAPIObservation{}, err
	}
	teamName := "api-team"
	if profile == "missing-team" {
		teamName = "missing"
	} else if profile != "no-pipeline" {
		ref := atc.PipelineRef{Name: "pipeline"}
		if profile == "instanced" {
			ref.InstanceVars = atc.InstanceVars{"branch": "feature/foo"}
		}
		config := atc.Config{}
		if profile != "no-job" {
			config.Jobs = atc.JobConfigs{{Name: "job"}}
		}
		pipeline, _, err := api.Team.SavePipeline(ref, config, 0, false)
		if err != nil {
			return CCAPIObservation{}, err
		}
		if profile != "no-job" && profile != "no-last-build" {
			job, found, err := pipeline.Job("job")
			if err != nil || !found {
				return CCAPIObservation{}, firstError(err, fmt.Errorf("CC job missing"))
			}
			status := db.BuildStatusSucceeded
			switch profile {
			case "aborted":
				status = db.BuildStatusAborted
			case "errored":
				status = db.BuildStatusErrored
			case "failed":
				status = db.BuildStatusFailed
			}
			build, err := job.CreateBuild("brine-user")
			if err != nil {
				return CCAPIObservation{}, err
			}
			if _, err := build.Start(atc.Plan{}); err != nil {
				return CCAPIObservation{}, err
			}
			if err := build.Finish(status); err != nil {
				return CCAPIObservation{}, err
			}
			if profile == "building" {
				next, err := job.CreateBuild("brine-user")
				if err != nil {
					return CCAPIObservation{}, err
				}
				if _, err := next.Start(atc.Plan{}); err != nil {
					return CCAPIObservation{}, err
				}
			}
		}
	}
	if err := api.request(http.MethodGet, "/api/v1/teams/"+teamName+"/cc.xml", nil); err != nil {
		return CCAPIObservation{}, err
	}
	return CCAPIObservation{Status: api.Status, ContentType: api.ContentType, Body: string(api.Body)}, nil
}
