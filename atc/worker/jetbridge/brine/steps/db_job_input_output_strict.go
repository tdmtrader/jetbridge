package steps

import (
	"fmt"
	"reflect"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

type DBJobInputOutputObservation struct {
	Profile string
	Failure string
}

func DBJobInputOutputStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, DBJobInputOutputObservation](
			"the production job input or output behavior {string} is exercised",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (DBJobInputOutputObservation, error) {
				profile, err := paramAt("the production job input or output behavior {string} is exercised", p, 0)
				if err != nil {
					return DBJobInputOutputObservation{}, err
				}
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return DBJobInputOutputObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				return DBJobInputOutputObservation{Profile: profile, Failure: observeDBJobInputOutput(database, profile)}, nil
			},
		),
		brine.DefineCheck[DBJobInputOutputObservation](
			"the job input or output behavior exactly matches {string}",
			func(in DBJobInputOutputObservation, p brine.Params, _ *brine.Recorder) error {
				profile, err := paramAt("the job input or output behavior exactly matches {string}", p, 0)
				if err != nil {
					return err
				}
				if profile != in.Profile {
					return fmt.Errorf("profile got %q, want %q", in.Profile, profile)
				}
				if in.Failure != "" {
					return fmt.Errorf("%s: %s", profile, in.Failure)
				}
				return nil
			},
		),
	}
}

func observeDBJobInputOutput(database JetbridgeDB, profile string) string {
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "job-io-team"})
	if err != nil {
		return err.Error()
	}
	fail := func(format string, args ...any) string { return fmt.Sprintf(format, args...) }

	if profile == "inputs" {
		config := atc.Config{
			Jobs: atc.JobConfigs{
				{Name: "job", PlanSequence: []atc.Step{
					{Config: &atc.PutStep{Name: "resource"}},
					{Config: &atc.GetStep{Name: "input", Resource: "resource", Passed: []string{"prior-one", "prior-two"}, Trigger: true, Version: &atc.VersionConfig{Every: true}}},
					{Config: &atc.TaskStep{Name: "task", Config: &atc.TaskConfig{RootfsURI: "image"}}},
					{Config: &atc.GetStep{Name: "resource"}},
					{Config: &atc.GetStep{Name: "other-input", Resource: "resource", Version: &atc.VersionConfig{Latest: true}}},
					{Config: &atc.GetStep{Name: "other-resource", Trigger: true, Version: &atc.VersionConfig{Pinned: atc.Version{"pinned": "version"}}}},
				}},
				{Name: "prior-one"}, {Name: "prior-two"}, {Name: "other-job", PlanSequence: []atc.Step{{Config: &atc.GetStep{Name: "other-job-input", Resource: "resource"}}}},
			},
			Resources: atc.ResourceConfigs{{Name: "resource", Type: "type"}, {Name: "other-resource", Type: "type"}},
		}
		job, _, err := saveJobForStrictTeam(team, "inputs-pipeline", config, "job")
		if err != nil {
			return err.Error()
		}
		got, err := job.Inputs()
		if err != nil {
			return err.Error()
		}
		want := []atc.JobInput{
			{Name: "input", Resource: "resource", Passed: []string{"prior-one", "prior-two"}, Trigger: true, Version: &atc.VersionConfig{Every: true}},
			{Name: "other-input", Resource: "resource", Version: &atc.VersionConfig{Latest: true}},
			{Name: "other-resource", Resource: "other-resource", Trigger: true, Version: &atc.VersionConfig{Pinned: atc.Version{"pinned": "version"}}},
			{Name: "resource", Resource: "resource"},
		}
		if !reflect.DeepEqual(got, want) {
			return fail("inputs got=%#v want=%#v", got, want)
		}
		return ""
	}

	if profile == "outputs" {
		config := atc.Config{
			Jobs: atc.JobConfigs{
				{Name: "job", PlanSequence: []atc.Step{
					{Config: &atc.PutStep{Name: "other-resource"}},
					{Config: &atc.TaskStep{Name: "task", Config: &atc.TaskConfig{RootfsURI: "image"}}},
					{Config: &atc.GetStep{Name: "resource"}},
					{Config: &atc.PutStep{Name: "output", Resource: "resource"}},
					{Config: &atc.PutStep{Name: "other-output", Resource: "resource"}},
				}},
				{Name: "other-job", PlanSequence: []atc.Step{{Config: &atc.PutStep{Name: "other-job-output", Resource: "resource"}}}},
			},
			Resources: atc.ResourceConfigs{{Name: "resource", Type: "type"}, {Name: "other-resource", Type: "type"}},
		}
		job, _, err := saveJobForStrictTeam(team, "outputs-pipeline", config, "job")
		if err != nil {
			return err.Error()
		}
		got, err := job.Outputs()
		if err != nil {
			return err.Error()
		}
		want := []atc.JobOutput{{Name: "other-output", Resource: "resource"}, {Name: "other-resource", Resource: "other-resource"}, {Name: "output", Resource: "resource"}}
		if !reflect.DeepEqual(got, want) {
			return fail("outputs got=%#v want=%#v", got, want)
		}
		return ""
	}

	config, jobName := algorithmInputConfig(profile)
	job, pipeline, err := saveJobForStrictTeam(team, "algorithm-pipeline", config, jobName)
	if err != nil {
		return err.Error()
	}
	resource, found, err := pipeline.Resource("resource")
	if err != nil || !found {
		return fail("resource lookup: found=%t err=%v", found, err)
	}
	if profile == "algorithm-get-wins-api" || profile == "algorithm-api-pin" {
		if _, err := database.Conn.Exec(`INSERT INTO resource_pins(resource_id, version, comment_text, config) VALUES($1, $2, '', false)`, resource.ID(), `{"api":"pinned"}`); err != nil {
			return err.Error()
		}
	}
	got, err := job.AlgorithmInputs()
	if err != nil {
		return err.Error()
	}
	want, err := expectedAlgorithmInputs(profile, job, pipeline)
	if err != nil {
		return err.Error()
	}
	if !reflect.DeepEqual(got, want) {
		return fail("algorithm inputs got=%#v want=%#v", got, want)
	}
	return ""
}

func algorithmInputConfig(profile string) (atc.Config, string) {
	get := atc.GetStep{Name: "input", Resource: "resource"}
	resource := atc.ResourceConfig{Name: "resource", Type: "type"}
	jobs := atc.JobConfigs{{Name: "job"}, {Name: "prior-one"}, {Name: "prior-two"}}
	switch profile {
	case "algorithm-input":
		get.Passed = []string{"prior-one", "prior-two"}
		get.Trigger = true
		get.Version = &atc.VersionConfig{Every: true}
	case "algorithm-get-pin", "algorithm-get-wins-api":
		get.Version = &atc.VersionConfig{Pinned: atc.Version{"input": "pinned"}}
	case "algorithm-config-pin":
		resource.Version = atc.Version{"config": "pinned"}
	case "algorithm-multiple":
		jobs[0].PlanSequence = []atc.Step{
			{Config: &atc.GetStep{Name: "first", Resource: "resource", Trigger: true, Version: &atc.VersionConfig{Every: true}}},
			{Config: &atc.GetStep{Name: "resource"}},
			{Config: &atc.GetStep{Name: "other", Resource: "other-resource", Trigger: true, Version: &atc.VersionConfig{Latest: true}}},
		}
		jobs = append(jobs, atc.JobConfig{Name: "foreign-job", PlanSequence: []atc.Step{{Config: &atc.GetStep{Name: "foreign", Resource: "resource"}}}})
		return atc.Config{Jobs: jobs, Resources: atc.ResourceConfigs{resource, {Name: "other-resource", Type: "type"}}}, "job"
	case "algorithm-gets-only":
		jobs[0].PlanSequence = []atc.Step{{Config: &atc.PutStep{Name: "resource"}}, {Config: &atc.TaskStep{Name: "task", Config: &atc.TaskConfig{RootfsURI: "image"}}}, {Config: &get}}
		return atc.Config{Jobs: jobs, Resources: atc.ResourceConfigs{resource}}, "job"
	}
	jobs[0].PlanSequence = []atc.Step{{Config: &get}}
	return atc.Config{Jobs: jobs, Resources: atc.ResourceConfigs{resource}}, "job"
}

func expectedAlgorithmInputs(profile string, job db.Job, pipeline db.Pipeline) (db.InputConfigs, error) {
	resource, found, err := pipeline.Resource("resource")
	if err != nil || !found {
		return nil, fmt.Errorf("resource lookup: found=%t err=%v", found, err)
	}
	base := db.InputConfig{Name: "input", JobID: job.ID(), ResourceID: resource.ID()}
	switch profile {
	case "algorithm-input":
		one, found, err := pipeline.Job("prior-one")
		if err != nil || !found {
			return nil, fmt.Errorf("prior-one lookup: found=%t err=%v", found, err)
		}
		two, found, err := pipeline.Job("prior-two")
		if err != nil || !found {
			return nil, fmt.Errorf("prior-two lookup: found=%t err=%v", found, err)
		}
		base.Passed = db.JobSet{one.ID(): true, two.ID(): true}
		base.UseEveryVersion = true
		base.Trigger = true
	case "algorithm-get-pin", "algorithm-get-wins-api":
		base.PinnedVersion = atc.Version{"input": "pinned"}
	case "algorithm-config-pin":
		base.PinnedVersion = atc.Version{"config": "pinned"}
	case "algorithm-api-pin":
		base.PinnedVersion = atc.Version{"api": "pinned"}
	case "algorithm-multiple":
		other, found, err := pipeline.Resource("other-resource")
		if err != nil || !found {
			return nil, fmt.Errorf("other resource lookup: found=%t err=%v", found, err)
		}
		return db.InputConfigs{
			{Name: "first", JobID: job.ID(), ResourceID: resource.ID(), UseEveryVersion: true, Trigger: true},
			{Name: "other", JobID: job.ID(), ResourceID: other.ID(), Trigger: true},
			{Name: "resource", JobID: job.ID(), ResourceID: resource.ID()},
		}, nil
	case "algorithm-gets-only":
		return db.InputConfigs{base}, nil
	default:
		return nil, fmt.Errorf("unknown algorithm input profile %q", profile)
	}
	return db.InputConfigs{base}, nil
}

func saveJobForStrictTeam(team db.Team, pipelineName string, config atc.Config, jobName string) (db.Job, db.Pipeline, error) {
	pipeline, _, err := team.SavePipeline(atc.PipelineRef{Name: pipelineName}, config, 1, false)
	if err != nil {
		return nil, nil, err
	}
	job, found, err := pipeline.Job(jobName)
	if err != nil {
		return nil, nil, err
	}
	if !found {
		return nil, nil, fmt.Errorf("job %q missing", jobName)
	}
	return job, pipeline, nil
}
