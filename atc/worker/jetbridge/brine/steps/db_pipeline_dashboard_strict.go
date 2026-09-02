package steps

import (
	"fmt"
	"slices"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

type DBPipelineDashboardObservation struct {
	Failure string
}

func DBPipelineDashboardStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, DBPipelineDashboardObservation](
			"the production pipeline dashboard behavior is exercised",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, _ brine.Params, _ *brine.Recorder, resources brine.Resources) (DBPipelineDashboardObservation, error) {
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return DBPipelineDashboardObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				return DBPipelineDashboardObservation{Failure: observeDBPipelineDashboard(database)}, nil
			},
		),
		brine.DefineCheck[DBPipelineDashboardObservation](
			"the pipeline dashboard behavior exactly matches the persisted state",
			func(in DBPipelineDashboardObservation, _ brine.Params, _ *brine.Recorder) error {
				if in.Failure != "" {
					return fmt.Errorf("pipeline dashboard result: %s", in.Failure)
				}
				return nil
			},
		),
	}
}

func observeDBPipelineDashboard(database JetbridgeDB) string {
	fail := func(format string, args ...any) string { return fmt.Sprintf(format, args...) }

	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "strict-dashboard-team"})
	if err != nil {
		return fail("create team: %v", err)
	}
	config := atc.Config{
		Jobs: atc.JobConfigs{
			{
				Name:         "job-name",
				Public:       true,
				Serial:       true,
				SerialGroups: []string{"serial-group"},
				PlanSequence: []atc.Step{
					{Config: &atc.PutStep{Name: "some-resource", Params: atc.Params{"some-param": "some-value"}}},
					{Config: &atc.GetStep{
						Name:     "some-input",
						Resource: "some-resource",
						Params:   atc.Params{"some-param": "some-value"},
						Passed:   []string{"job-1", "job-2"},
						Trigger:  true,
					}},
				},
			},
			{Name: "some-other-job", Serial: true},
			{Name: "a-job"},
			{Name: "shared-job"},
			{Name: "random-job"},
			{Name: "job-1"},
			{Name: "job-2"},
			{Name: "other-serial-group-job", SerialGroups: []string{"serial-group", "really-different-group"}},
			{Name: "different-serial-group-job", SerialGroups: []string{"different-serial-group"}},
		},
		Resources: atc.ResourceConfigs{
			{Name: "some-resource", Type: "some-type", Source: atc.Source{"some": "source"}},
		},
	}
	pipeline, _, err := team.SavePipeline(atc.PipelineRef{Name: "strict-dashboard-pipeline"}, config, 0, false)
	if err != nil {
		return fail("save pipeline: %v", err)
	}
	job, found, err := pipeline.Job("job-name")
	if err != nil || !found {
		return fail("load job found=%t err=%v", found, err)
	}
	if err := job.UpdateFirstLoggedBuildID(57); err != nil {
		return fail("update first logged build id: %v", err)
	}

	wantNames := []string{
		"job-name", "some-other-job", "a-job", "shared-job", "random-job",
		"job-1", "job-2", "other-serial-group-job", "different-serial-group-job",
	}
	dashboard, err := pipeline.Dashboard()
	if err != nil {
		return fail("initial dashboard: %v", err)
	}
	if got := dashboardNames(dashboard); !slices.Equal(got, wantNames) {
		return fail("configured job order got %v, want %v", got, wantNames)
	}

	first, err := job.CreateBuild("strict dashboard")
	if err != nil {
		return fail("create first build: %v", err)
	}
	dashboard, err = pipeline.Dashboard()
	if err != nil {
		return fail("pending dashboard: %v", err)
	}
	primary, failure := dashboardJob(dashboard, "job-name")
	if failure != "" {
		return failure
	}
	if primary.NextBuild == nil || primary.NextBuild.ID != first.ID() {
		return fail("pending next build got %+v, want ID %d", primary.NextBuild, first.ID())
	}

	started, err := first.Start(atc.Plan{ID: "some-id"})
	if err != nil || !started {
		return fail("start first build started=%t err=%v", started, err)
	}
	if found, err := first.Reload(); err != nil || !found {
		return fail("reload first build found=%t err=%v", found, err)
	}
	dashboard, err = pipeline.Dashboard()
	if err != nil {
		return fail("started dashboard: %v", err)
	}
	primary, failure = dashboardJob(dashboard, "job-name")
	if failure != "" {
		return failure
	}
	if primary.NextBuild == nil || primary.NextBuild.ID != first.ID() || primary.NextBuild.Status != atc.StatusStarted {
		return fail("started next build got %+v, want ID %d status %s", primary.NextBuild, first.ID(), atc.StatusStarted)
	}

	second, err := job.CreateBuild("strict dashboard")
	if err != nil {
		return fail("create second build: %v", err)
	}
	dashboard, err = pipeline.Dashboard()
	if err != nil {
		return fail("newer pending dashboard: %v", err)
	}
	primary, failure = dashboardJob(dashboard, "job-name")
	if failure != "" {
		return failure
	}
	if primary.NextBuild == nil || primary.NextBuild.ID != first.ID() {
		return fail("running build displaced by pending build: got %+v, want ID %d", primary.NextBuild, first.ID())
	}

	if err := first.Finish(db.BuildStatusSucceeded); err != nil {
		return fail("finish first build: %v", err)
	}
	if err := second.Finish(db.BuildStatusSucceeded); err != nil {
		return fail("finish second build: %v", err)
	}
	if found, err := second.Reload(); err != nil || !found {
		return fail("reload second build found=%t err=%v", found, err)
	}
	dashboard, err = pipeline.Dashboard()
	if err != nil {
		return fail("finished dashboard: %v", err)
	}
	primary, failure = dashboardJob(dashboard, "job-name")
	if failure != "" {
		return failure
	}
	if primary.NextBuild != nil {
		return fail("finished dashboard next build got %+v, want nil", primary.NextBuild)
	}
	if primary.FinishedBuild == nil || primary.FinishedBuild.ID != second.ID() {
		return fail("finished build got %+v, want ID %d", primary.FinishedBuild, second.ID())
	}
	wantOutput := atc.JobOutputSummary{Name: "some-resource", Resource: "some-resource"}
	if len(primary.Outputs) != 1 || primary.Outputs[0] != wantOutput {
		return fail("outputs got %+v, want %+v", primary.Outputs, wantOutput)
	}
	wantInput := atc.JobInputSummary{
		Name: "some-input", Resource: "some-resource", Passed: []string{"job-1", "job-2"}, Trigger: true,
	}
	if len(primary.Inputs) != 1 || primary.Inputs[0].Name != wantInput.Name ||
		primary.Inputs[0].Resource != wantInput.Resource ||
		!slices.Equal(primary.Inputs[0].Passed, wantInput.Passed) ||
		primary.Inputs[0].Trigger != wantInput.Trigger {
		return fail("inputs got %+v, want %+v", primary.Inputs, wantInput)
	}
	return ""
}

func dashboardNames(dashboard []atc.JobSummary) []string {
	names := make([]string, len(dashboard))
	for i, summary := range dashboard {
		names[i] = summary.Name
	}
	return names
}

func dashboardJob(dashboard []atc.JobSummary, name string) (atc.JobSummary, string) {
	for _, summary := range dashboard {
		if summary.Name == name {
			return summary, ""
		}
	}
	return atc.JobSummary{}, fmt.Sprintf("dashboard job %q not found in %v", name, dashboardNames(dashboard))
}
