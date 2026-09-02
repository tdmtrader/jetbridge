package steps

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/event"
)

type DBTeamRemainingObservation struct {
	Profile string
	Failure string
}

func DBTeamRemainingStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, DBTeamRemainingObservation](
			"the remaining production DB team behavior {string} is exercised",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (DBTeamRemainingObservation, error) {
				profile, err := paramAt("the remaining production DB team behavior {string} is exercised", p, 0)
				if err != nil {
					return DBTeamRemainingObservation{}, err
				}
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return DBTeamRemainingObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				return DBTeamRemainingObservation{Profile: profile, Failure: observeDBTeamRemaining(database, profile)}, nil
			},
		),
		brine.DefineCheck[DBTeamRemainingObservation](
			"the remaining DB team behavior exactly matches {string}",
			func(in DBTeamRemainingObservation, p brine.Params, _ *brine.Recorder) error {
				profile, err := paramAt("the remaining DB team behavior exactly matches {string}", p, 0)
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

func observeDBTeamRemaining(database JetbridgeDB, profile string) string {
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "remaining-team"})
	if err != nil {
		return err.Error()
	}
	other, err := database.TeamFactory.CreateTeam(atc.Team{Name: "remaining-other"})
	if err != nil {
		return err.Error()
	}

	fail := func(format string, args ...any) string { return fmt.Sprintf(format, args...) }
	worker := func() atc.Worker {
		return atc.Worker{
			Name: "remaining-worker", Platform: "linux", Tags: atc.Tags{"one", "two"},
			ActiveContainers: 140, StartTime: 55,
			ResourceTypes: []atc.WorkerResourceType{{Type: "some-type", Image: "some-image", Version: "some-version"}},
		}
	}
	config := atc.Config{
		Resources: atc.ResourceConfigs{{Name: "input", Type: "some-type", Source: atc.Source{"key": "value"}}},
		Jobs:      atc.JobConfigs{{Name: "job", PlanSequence: []atc.Step{{Config: &atc.GetStep{Name: "input", Resource: "input"}}}}},
	}

	switch profile {
	case "save-worker-overwrites":
		if _, err := team.SaveWorker(worker(), 5*time.Minute); err != nil {
			return err.Error()
		}
		changed := worker()
		changed.Platform = "changed-platform"
		changed.ActiveContainers = 12
		saved, err := team.SaveWorker(changed, 5*time.Minute)
		if err != nil {
			return err.Error()
		}
		if saved.Platform() != changed.Platform || saved.ActiveContainers() != 12 || saved.State() != db.WorkerStateRunning {
			return fail("worker was not overwritten: platform=%q active=%d state=%q", saved.Platform(), saved.ActiveContainers(), saved.State())
		}
	case "save-worker-rejects-other-team":
		if _, err := other.SaveWorker(worker(), 5*time.Minute); err != nil {
			return err.Error()
		}
		if _, err := team.SaveWorker(worker(), 5*time.Minute); err == nil {
			return "same worker name was accepted for a different team"
		}
	case "workers-empty":
		workers, err := team.Workers()
		if err != nil {
			return err.Error()
		}
		if len(workers) != 0 {
			return fail("got %d workers, want none", len(workers))
		}
	case "auth-persists":
		auth := atc.TeamAuth{accessor.OwnerRole: {"users": {"local:username"}}}
		if err := team.UpdateProviderAuth(auth); err != nil {
			return err.Error()
		}
		if !reflect.DeepEqual(team.Auth(), auth) {
			return fail("auth got %#v, want %#v", team.Auth(), auth)
		}
	case "auth-clears-legacy":
		if _, err := database.Conn.Exec("UPDATE teams SET legacy_auth = $1 WHERE id = $2", `{"basicauth":{"username":"u","password":"p"}}`, team.ID()); err != nil {
			return err.Error()
		}
		if err := team.UpdateProviderAuth(atc.TeamAuth{accessor.OwnerRole: {"users": {"local:username"}}}); err != nil {
			return err.Error()
		}
		var legacy sql.NullString
		if err := database.Conn.QueryRow("SELECT legacy_auth FROM teams WHERE id = $1", team.ID()).Scan(&legacy); err != nil {
			return err.Error()
		}
		if legacy.Valid {
			return fail("legacy auth remained %q", legacy.String)
		}
	case "auth-overrides":
		if err := team.UpdateProviderAuth(atc.TeamAuth{accessor.OwnerRole: {"users": {"local:old"}}, accessor.ViewerRole: {"users": {"local:viewer"}}}); err != nil {
			return err.Error()
		}
		want := atc.TeamAuth{accessor.OwnerRole: {"users": {"local:new"}}}
		if err := team.UpdateProviderAuth(want); err != nil {
			return err.Error()
		}
		if !reflect.DeepEqual(team.Auth(), want) {
			return fail("auth got %#v, want %#v", team.Auth(), want)
		}
	case "one-off-build":
		build, err := team.CreateOneOffBuild()
		if err != nil {
			return err.Error()
		}
		if build.ID() == 0 || build.Name() != "1" || build.TeamName() != team.Name() || build.JobName() != "" || build.PipelineName() != "" || build.Status() != db.BuildStatusPending {
			return fail("unexpected one-off build: id=%d name=%q team=%q job=%q pipeline=%q status=%q", build.ID(), build.Name(), build.TeamName(), build.JobName(), build.PipelineName(), build.Status())
		}
	case "started-build-fields", "started-build-public-plan", "started-build-event":
		plan := atc.Plan{ID: "56", Get: &atc.GetPlan{Name: "some-name", Resource: "some-resource", Type: "some-type", Source: atc.Source{"some": "source"}, Version: &atc.Version{"some": "version"}}}
		build, err := team.CreateStartedBuild(plan)
		if err != nil {
			return err.Error()
		}
		switch profile {
		case "started-build-fields":
			if build.ID() == 0 || build.Name() != "1" || build.TeamName() != team.Name() || build.JobName() != "" || build.PipelineName() != "" || build.Status() != db.BuildStatusStarted {
				return fail("unexpected started build: id=%d name=%q team=%q job=%q pipeline=%q status=%q", build.ID(), build.Name(), build.TeamName(), build.JobName(), build.PipelineName(), build.Status())
			}
		case "started-build-public-plan":
			found, err := build.Reload()
			if err != nil || !found {
				return fail("reload: found=%t err=%v", found, err)
			}
			if !reflect.DeepEqual(build.PublicPlan(), plan.Public()) {
				return fail("public plan got %#v, want %#v", build.PublicPlan(), plan.Public())
			}
		case "started-build-event":
			events, err := build.Events(0)
			if err != nil {
				return err.Error()
			}
			defer events.Close()
			envelope, err := events.Next()
			if err != nil {
				return err.Error()
			}
			var status event.Status
			if envelope.Data == nil || json.Unmarshal(*envelope.Data, &status) != nil || status.Status != atc.StatusStarted || status.Time != build.StartTime().Unix() {
				return fail("unexpected start event: event=%q status=%q time=%d", envelope.Event, status.Status, status.Time)
			}
		}
	case "save-pipeline-created", "save-pipeline-team", "save-pipeline-paused", "save-pipeline-unpaused", "save-pipeline-unarchived", "save-pipeline-update-created", "save-pipeline-update-paused", "save-pipeline-update-unpaused", "save-pipeline-update-unarchives", "pipeline-lookup", "save-pipeline-same-name-across-teams":
		initiallyPaused := profile == "save-pipeline-paused" || profile == "save-pipeline-unarchived" || profile == "save-pipeline-update-paused"
		pipeline, created, err := team.SavePipeline(atc.PipelineRef{Name: "pipeline"}, config, 0, initiallyPaused)
		if err != nil {
			return err.Error()
		}
		switch profile {
		case "save-pipeline-created":
			if !created {
				return "new pipeline reported created=false"
			}
		case "save-pipeline-team":
			if pipeline.TeamID() != team.ID() {
				return fail("team id got %d, want %d", pipeline.TeamID(), team.ID())
			}
		case "save-pipeline-paused":
			if !pipeline.Paused() {
				return "initially paused pipeline is unpaused"
			}
		case "save-pipeline-unpaused":
			if pipeline.Paused() {
				return "initially unpaused pipeline is paused"
			}
		case "save-pipeline-unarchived":
			if pipeline.Archived() {
				return "new pipeline is archived"
			}
		case "save-pipeline-update-created":
			_, created, err = team.SavePipeline(atc.PipelineRef{Name: "pipeline"}, config, pipeline.ConfigVersion(), false)
			if err != nil {
				return err.Error()
			}
			if created {
				return "updated pipeline reported created=true"
			}
		case "save-pipeline-update-paused":
			updated, _, err := team.SavePipeline(atc.PipelineRef{Name: "pipeline"}, config, pipeline.ConfigVersion(), false)
			if err != nil {
				return err.Error()
			}
			if !updated.Paused() {
				return "paused pipeline lost paused state on update"
			}
		case "save-pipeline-update-unpaused":
			updated, _, err := team.SavePipeline(atc.PipelineRef{Name: "pipeline"}, config, pipeline.ConfigVersion(), true)
			if err != nil {
				return err.Error()
			}
			if updated.Paused() {
				return "unpaused pipeline became paused on update"
			}
		case "save-pipeline-update-unarchives":
			if err := pipeline.Archive(); err != nil {
				return err.Error()
			}
			updated, _, err := team.SavePipeline(atc.PipelineRef{Name: "pipeline"}, config, 0, true)
			if err != nil {
				return err.Error()
			}
			if updated.Archived() {
				return "resaved pipeline remained archived"
			}
		case "pipeline-lookup":
			foundPipeline, found, err := team.Pipeline(atc.PipelineRef{Name: "pipeline"})
			if err != nil {
				return err.Error()
			}
			if !found || foundPipeline.ID() != pipeline.ID() || foundPipeline.Name() != "pipeline" {
				return fail("lookup found=%t id=%d", found, foundPipeline.ID())
			}
		case "save-pipeline-same-name-across-teams":
			ref := atc.PipelineRef{Name: "shared"}
			mine, _, err := team.SavePipeline(ref, config, 0, true)
			if err != nil || !mine.Paused() {
				return fail("first team's pipeline: paused=%t err=%v", mine != nil && mine.Paused(), err)
			}
			theirs, _, err := other.SavePipeline(ref, atc.Config{Jobs: atc.JobConfigs{{Name: "other-job"}}}, 0, true)
			if err != nil || !theirs.Paused() {
				return fail("second team's pipeline: paused=%t err=%v", theirs != nil && theirs.Paused(), err)
			}
			if _, _, err := team.SavePipeline(ref, config, theirs.ConfigVersion(), false); err == nil {
				return "first team cross-updated the second team's config version"
			}
			if _, _, err := other.SavePipeline(ref, config, mine.ConfigVersion(), false); err == nil {
				return "second team cross-updated the first team's config version"
			}
		}
	default:
		return fail("unknown remaining DB team profile %q", profile)
	}
	return ""
}
