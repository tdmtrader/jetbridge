package steps

import (
	"fmt"
	"net/http"

	"github.com/brine-dev/brine-go/pkg/brine"
)

type JobBuildGuardObservation struct {
	Profile    string
	Status     int
	BuildCount int
}

func JobBuildGuardsStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, JobBuildGuardObservation](
			"the production job build guard executes profile {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, rec *brine.Recorder, resources brine.Resources) (JobBuildGuardObservation, error) {
				profile, err := paramAt("the production job build guard executes profile {string}", p, 0)
				if err != nil {
					return JobBuildGuardObservation{}, err
				}
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return JobBuildGuardObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				boundary, err := newStrictBuildBoundary(database, rec)
				if err != nil {
					return JobBuildGuardObservation{}, err
				}
				return observeJobBuildGuard(boundary, profile)
			},
		),
		brine.DefineCheck[JobBuildGuardObservation](
			"the job build guard observation is status {int} with {int} persisted builds",
			func(in JobBuildGuardObservation, p brine.Params, _ *brine.Recorder) error {
				status, ok := p.GetInt(0)
				if !ok {
					return fmt.Errorf("job build guard status is not an integer")
				}
				count, ok := p.GetInt(1)
				if !ok {
					return fmt.Errorf("job build guard count is not an integer")
				}
				if in.Status != status || in.BuildCount != count {
					return fmt.Errorf("job build guard %q: got status=%d builds=%d, want status=%d builds=%d", in.Profile, in.Status, in.BuildCount, status, count)
				}
				return nil
			},
		),
		brine.DefineCheck[JobBuildGuardObservation](
			"the job build guard observation is status {int}",
			func(in JobBuildGuardObservation, p brine.Params, _ *brine.Recorder) error {
				status, ok := p.GetInt(0)
				if !ok {
					return fmt.Errorf("job build guard status is not an integer")
				}
				if in.Status != status {
					return fmt.Errorf("job build guard %q: got status=%d, want status=%d", in.Profile, in.Status, status)
				}
				return nil
			},
		),
	}
}

func observeJobBuildGuard(boundary *strictBuildBoundary, profile string) (JobBuildGuardObservation, error) {
	observation := JobBuildGuardObservation{Profile: profile}
	jobName := "missing"
	if profile == "manual-disabled" {
		config, err := boundary.pipeline.Config()
		if err != nil {
			return observation, fmt.Errorf("load build pipeline config: %w", err)
		}
		config.Jobs[0].DisableManualTrigger = true
		pipeline, _, err := boundary.team.SavePipeline(
			boundary.ref,
			config,
			boundary.pipeline.ConfigVersion(),
			false,
		)
		if err != nil {
			return observation, fmt.Errorf("disable manual job triggering: %w", err)
		}
		job, found, err := pipeline.Job("build")
		if err != nil || !found {
			return observation, firstError(err, fmt.Errorf("disabled manual job was not found"))
		}
		boundary.job = job
		jobName = "build"
	} else if profile != "missing-job" {
		return observation, fmt.Errorf("unknown job build guard profile %q", profile)
	}

	path := fmt.Sprintf("/api/v1/teams/%s/pipelines/%s/jobs/%s/builds?%s",
		buildStrictTeamName, boundary.ref.Name, jobName, boundary.ref.QueryParams().Encode())
	request, err := http.NewRequest(http.MethodPost, boundary.url+path, nil)
	if err != nil {
		return observation, err
	}
	response, err := boundary.httpClient.Do(request)
	if err != nil {
		return observation, err
	}
	defer response.Body.Close()
	observation.Status = response.StatusCode

	if profile == "manual-disabled" {
		if err := boundary.database.Conn.QueryRow(
			`SELECT count(*) FROM builds WHERE job_id = $1`, boundary.job.ID(),
		).Scan(&observation.BuildCount); err != nil {
			return observation, err
		}
	}
	return observation, nil
}
