package steps

import (
	"fmt"
	"strconv"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

type DBBuildRemainingObservation struct{ Value string }

func DBBuildRemainingStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, DBBuildRemainingObservation](
			"the remaining real DB build evaluates profile {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (DBBuildRemainingObservation, error) {
				profile, ok := p.GetString(0)
				if !ok {
					return DBBuildRemainingObservation{}, fmt.Errorf("expected remaining DB build profile")
				}
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return DBBuildRemainingObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				value, err := observeRemainingDBBuild(database, profile)
				return DBBuildRemainingObservation{Value: value}, err
			},
		),
		CheckString[DBBuildRemainingObservation](
			"the remaining DB build observation is {string}",
			"remaining DB build observation",
			func(observation DBBuildRemainingObservation) (string, error) { return observation.Value, nil },
		),
	}
}

func observeRemainingDBBuild(database JetbridgeDB, profile string) (string, error) {
	if len(profile) >= len("save-pipeline/") && profile[:len("save-pipeline/")] == "save-pipeline/" {
		return observeRemainingSavePipeline(database, profile)
	}
	if len(profile) >= len("abort/") && profile[:len("abort/")] == "abort/" {
		return observeRemainingAbort(database, profile)
	}
	if len(profile) >= len("finish-archive/") && profile[:len("finish-archive/")] == "finish-archive/" {
		return observeRemainingFinishArchive(database, profile)
	}
	return "", fmt.Errorf("unknown remaining DB build profile %q", profile)
}

func observeRemainingFinishArchive(database JetbridgeDB, profile string) (string, error) {
	team, _, job, parentBuild, err := remainingBuildFixture(database, "remaining-archive-team", "parent-pipeline")
	if err != nil {
		return "", err
	}
	config := atc.Config{Jobs: atc.JobConfigs{{Name: "some-job"}}}
	child, _, err := parentBuild.SavePipeline(atc.PipelineRef{Name: "child-pipeline"}, team.ID(), config, 0, false)
	if err != nil {
		return "", err
	}
	if err := parentBuild.Finish(db.BuildStatusSucceeded); err != nil {
		return "", err
	}
	found, err := child.Reload()
	if err != nil || !found {
		return "", firstError(err, fmt.Errorf("child pipeline was not found"))
	}
	if child.Archived() {
		return "", fmt.Errorf("child pipeline was archived while its parent build was current")
	}

	switch profile {
	case "finish-archive/direct":
		nextBuild, err := job.CreateBuild("brine-user")
		if err != nil {
			return "", err
		}
		if err := nextBuild.Finish(db.BuildStatusSucceeded); err != nil {
			return "", err
		}
		found, err := child.Reload()
		if err != nil || !found {
			return "", firstError(err, fmt.Errorf("child pipeline was not found after its parent advanced"))
		}
		return fmt.Sprintf("archived=%t", child.Archived()), nil
	case "finish-archive/descendants":
		children := []db.Pipeline{child}
		current := child
		for i := 0; i < 5; i++ {
			childJob, found, err := current.Job("some-job")
			if err != nil || !found {
				return "", firstError(err, fmt.Errorf("child job %d was not found", i))
			}
			childBuild, err := childJob.CreateBuild("brine-user")
			if err != nil {
				return "", err
			}
			current, _, err = childBuild.SavePipeline(atc.PipelineRef{Name: "descendant-pipeline-" + strconv.Itoa(i)}, team.ID(), config, 0, false)
			if err != nil {
				return "", err
			}
			if err := childBuild.Finish(db.BuildStatusSucceeded); err != nil {
				return "", err
			}
			children = append(children, current)
		}
		nextBuild, err := job.CreateBuild("brine-user")
		if err != nil {
			return "", err
		}
		if err := nextBuild.Finish(db.BuildStatusSucceeded); err != nil {
			return "", err
		}
		allArchived := true
		for i, pipeline := range children {
			found, err := pipeline.Reload()
			if err != nil || !found {
				return "", firstError(err, fmt.Errorf("descendant pipeline %d was not found", i))
			}
			allArchived = allArchived && pipeline.Archived()
		}
		return fmt.Sprintf("all-archived=%t", allArchived), nil
	default:
		return "", fmt.Errorf("unknown finish archive profile %q", profile)
	}
}

func remainingBuildFixture(database JetbridgeDB, teamName, pipelineName string) (db.Team, db.Pipeline, db.Job, db.Build, error) {
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: teamName})
	if err != nil {
		return nil, nil, nil, nil, err
	}
	pipeline, _, err := team.SavePipeline(atc.PipelineRef{Name: pipelineName}, atc.Config{Jobs: atc.JobConfigs{{Name: "some-job"}}}, 0, false)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	job, found, err := pipeline.Job("some-job")
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if !found {
		return nil, nil, nil, nil, fmt.Errorf("saved job was not found")
	}
	build, err := job.CreateBuild("brine-user")
	return team, pipeline, job, build, err
}

func observeRemainingSavePipeline(database JetbridgeDB, profile string) (string, error) {
	team, source, job, buildOne, err := remainingBuildFixture(database, "remaining-save-team", "source-pipeline")
	if err != nil {
		return "", err
	}
	config := atc.Config{Jobs: atc.JobConfigs{{Name: "some-job"}}}
	parentMatches := func(pipeline db.Pipeline, build db.Build) bool {
		return pipeline.ParentJobID() == build.JobID() && pipeline.ParentBuildID() == build.ID()
	}
	switch profile {
	case "save-pipeline/new-parent":
		pipeline, _, err := buildOne.SavePipeline(atc.PipelineRef{Name: "target-pipeline"}, team.ID(), config, 0, false)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("parent=%t", parentMatches(pipeline, buildOne)), nil
	case "save-pipeline/latest-only":
		buildTwo, err := job.CreateBuild("brine-user")
		if err != nil {
			return "", err
		}
		pipeline, _, err := buildTwo.SavePipeline(atc.PipelineRef{Name: "target-pipeline"}, team.ID(), config, 0, false)
		if err != nil {
			return "", err
		}
		_, _, olderErr := buildOne.SavePipeline(atc.PipelineRef{Name: "target-pipeline"}, team.ID(), config, pipeline.ConfigVersion(), false)
		return fmt.Sprintf("parent=%t;error=%s", parentMatches(pipeline, buildTwo), remainingPipelineError(olderErr)), nil
	case "save-pipeline/update-parent":
		pipeline, _, err := buildOne.SavePipeline(remainingPipelineRef(source), team.ID(), config, source.ConfigVersion(), false)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("parent=%t", parentMatches(pipeline, buildOne)), nil
	case "save-pipeline/unarchive", "save-pipeline/keep-paused":
		target, _, err := team.SavePipeline(atc.PipelineRef{Name: "target-pipeline"}, config, 0, false)
		if err != nil {
			return "", err
		}
		if profile == "save-pipeline/unarchive" {
			err = target.Archive()
		} else {
			err = target.Pause("brine-user")
		}
		if err != nil {
			return "", err
		}
		if profile == "save-pipeline/unarchive" {
			found, reloadErr := target.Reload()
			if reloadErr != nil || !found {
				return "", firstError(reloadErr, fmt.Errorf("archived target pipeline was not found"))
			}
		}
		target, _, err = buildOne.SavePipeline(remainingPipelineRef(target), team.ID(), config, target.ConfigVersion(), false)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("paused=%t", target.Paused()), nil
	}
	return "", fmt.Errorf("unknown SavePipeline profile %q", profile)
}

func remainingPipelineRef(pipeline db.Pipeline) atc.PipelineRef {
	return atc.PipelineRef{Name: pipeline.Name(), InstanceVars: pipeline.InstanceVars()}
}

func remainingPipelineError(err error) string {
	if err == db.ErrSetByNewerBuild {
		return "newer-build"
	}
	if err == nil {
		return "nil"
	}
	return "other"
}

func observeRemainingAbort(database JetbridgeDB, profile string) (string, error) {
	_, _, job, build, err := remainingBuildFixture(database, "remaining-abort-team", "abort-pipeline")
	if err != nil {
		return "", err
	}
	switch profile {
	case "abort/pending-schedule":
		found, err := job.Reload()
		if err != nil || !found {
			return "", firstError(err, fmt.Errorf("pending abort job was not found"))
		}
		before := job.ScheduleRequestedTime()
		if err := build.MarkAsAborted(); err != nil {
			return "", err
		}
		found, err = job.Reload()
		if err != nil || !found {
			return "", firstError(err, fmt.Errorf("abort job was not found"))
		}
		return fmt.Sprintf("advanced=%t", job.ScheduleRequestedTime().After(before)), nil
	case "abort/finished-schedule":
		if err := build.Finish(db.BuildStatusFailed); err != nil {
			return "", err
		}
		buildFound, err := build.Reload()
		if err != nil || !buildFound {
			return "", firstError(err, fmt.Errorf("finished build was not found"))
		}
		found, err := job.Reload()
		if err != nil || !found {
			return "", firstError(err, fmt.Errorf("finished job was not found"))
		}
		before := job.ScheduleRequestedTime()
		if err := build.MarkAsAborted(); err != nil {
			return "", err
		}
		found, err = job.Reload()
		if err != nil || !found {
			return "", firstError(err, fmt.Errorf("finished abort job was not found"))
		}
		return fmt.Sprintf("unchanged=%t", job.ScheduleRequestedTime().Equal(before)), nil
	}
	return "", fmt.Errorf("unknown Abort profile %q", profile)
}
