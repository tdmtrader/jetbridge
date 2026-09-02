package steps

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	gocache "github.com/patrickmn/go-cache"
)

type DBVersionsFinalObservation struct {
	Profile string
	Failure string
}

func DBVersionsFinalStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, DBVersionsFinalObservation](
			"the production versions database profile {string} is exercised",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (DBVersionsFinalObservation, error) {
				profile, err := paramAt("the production versions database profile {string} is exercised", p, 0)
				if err != nil {
					return DBVersionsFinalObservation{}, err
				}
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return DBVersionsFinalObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				return DBVersionsFinalObservation{Profile: profile, Failure: observeDBVersionsFinal(database, profile)}, nil
			},
		),
		brine.DefineCheck[DBVersionsFinalObservation](
			"the versions database behavior exactly matches {string}",
			func(in DBVersionsFinalObservation, p brine.Params, _ *brine.Recorder) error {
				profile, err := paramAt("the versions database behavior exactly matches {string}", p, 0)
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

func dbVersionsFinalJob(database JetbridgeDB, profile string) (db.Job, error) {
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "versions-final-" + profile})
	if err != nil {
		return nil, err
	}
	pipeline, _, err := team.SavePipeline(atc.PipelineRef{Name: "pipeline"}, atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}}, 0, false)
	if err != nil {
		return nil, err
	}
	job, found, err := pipeline.Job("job")
	if err != nil || !found {
		return nil, fmt.Errorf("load job: found=%t: %w", found, err)
	}
	return job, nil
}

func dbVersionsFinalBuild(job db.Job, status db.BuildStatus) (db.Build, error) {
	build, err := job.CreateBuild("strict versions database")
	if err != nil {
		return nil, err
	}
	if err := build.Finish(status); err != nil {
		return nil, err
	}
	return build, nil
}

func dbVersionsFinalRerun(job db.Job, original db.Build) (db.Build, error) {
	build, err := job.RerunBuild(original, "strict versions database")
	if err != nil {
		return nil, err
	}
	if err := build.Finish(db.BuildStatusSucceeded); err != nil {
		return nil, err
	}
	return build, nil
}

func dbVersionsFinalDrain(builds *db.PaginatedBuilds) ([]int, int, error) {
	var ids []int
	for {
		id, ok, err := builds.Next(context.Background())
		if err != nil {
			return nil, 0, err
		}
		if !ok {
			return ids, id, nil
		}
		ids = append(ids, id)
	}
}

func dbVersionsFinalIDs(builds ...db.Build) []int {
	ids := make([]int, len(builds))
	for i, build := range builds {
		ids[i] = build.ID()
	}
	return ids
}

func dbVersionsFinalReverse(builds []db.Build) []db.Build {
	reversed := make([]db.Build, len(builds))
	for i := range builds {
		reversed[i] = builds[len(builds)-1-i]
	}
	return reversed
}

func dbVersionsFinalFinished(job db.Job, count int) ([]db.Build, error) {
	builds := make([]db.Build, 0, count)
	for range count {
		build, err := dbVersionsFinalBuild(job, db.BuildStatusSucceeded)
		if err != nil {
			return nil, err
		}
		builds = append(builds, build)
	}
	return builds, nil
}

func observeDBVersionsFinal(database JetbridgeDB, profile string) string {
	if profile == "find-partial-version" {
		return observeDBVersionsFinalFind(database)
	}
	job, err := dbVersionsFinalJob(database, profile)
	if err != nil {
		return err.Error()
	}
	versions := db.NewVersionsDB(database.Conn, 5, gocache.New(-1, -1))
	if len(profile) >= len("successful-") && profile[:len("successful-")] == "successful-" {
		return observeDBVersionsFinalSuccessful(versions, job, profile)
	}
	return observeDBVersionsFinalUnused(versions, job, profile)
}

func observeDBVersionsFinalSuccessful(versions db.VersionsDB, job db.Job, profile string) string {
	var expected []db.Build
	switch profile {
	case "successful-one":
		build, err := dbVersionsFinalBuild(job, db.BuildStatusSucceeded)
		if err != nil {
			return err.Error()
		}
		expected = []db.Build{build}
	case "successful-page-limit":
		builds, err := dbVersionsFinalFinished(job, 5)
		if err != nil {
			return err.Error()
		}
		expected = dbVersionsFinalReverse(builds)
	case "successful-reruns-filler", "successful-reruns-failed-origin":
		build1, err := dbVersionsFinalBuild(job, db.BuildStatusSucceeded)
		if err != nil {
			return err.Error()
		}
		build2, err := dbVersionsFinalBuild(job, db.BuildStatusFailed)
		if err != nil {
			return err.Error()
		}
		build3, err := dbVersionsFinalBuild(job, db.BuildStatusSucceeded)
		if err != nil {
			return err.Error()
		}
		build4, err := dbVersionsFinalRerun(job, build2)
		if err != nil {
			return err.Error()
		}
		build5, err := dbVersionsFinalRerun(job, build2)
		if err != nil {
			return err.Error()
		}
		build6, err := dbVersionsFinalBuild(job, db.BuildStatusSucceeded)
		if err != nil {
			return err.Error()
		}
		if profile == "successful-reruns-filler" {
			filler, err := dbVersionsFinalFinished(job, 5)
			if err != nil {
				return err.Error()
			}
			expected = append(dbVersionsFinalReverse(filler), build6, build3, build5, build4, build1)
		} else {
			expected = []db.Build{build6, build3, build5, build4, build1}
		}
	case "successful-failed-boundary", "successful-succeeded-boundary", "successful-multiple-reruns-boundary":
		status := db.BuildStatusFailed
		if profile == "successful-succeeded-boundary" {
			status = db.BuildStatusSucceeded
		}
		original, err := dbVersionsFinalBuild(job, status)
		if err != nil {
			return err.Error()
		}
		filler, err := dbVersionsFinalFinished(job, 4)
		if err != nil {
			return err.Error()
		}
		rerun1, err := dbVersionsFinalRerun(job, original)
		if err != nil {
			return err.Error()
		}
		expected = append(dbVersionsFinalReverse(filler), rerun1)
		if profile == "successful-succeeded-boundary" {
			expected = append(expected, original)
		}
		if profile == "successful-multiple-reruns-boundary" {
			rerun2, err := dbVersionsFinalRerun(job, original)
			if err != nil {
				return err.Error()
			}
			rerun3, err := dbVersionsFinalRerun(job, original)
			if err != nil {
				return err.Error()
			}
			expected = append(dbVersionsFinalReverse(filler), rerun3, rerun2, rerun1)
		}
	default:
		return fmt.Sprintf("unknown successful profile %q", profile)
	}
	paginated := versions.SuccessfulBuilds(context.Background(), job.ID())
	got, terminalID, err := dbVersionsFinalDrain(&paginated)
	if err != nil {
		return err.Error()
	}
	want := dbVersionsFinalIDs(expected...)
	if !reflect.DeepEqual(got, want) || terminalID != 0 {
		return fmt.Sprintf("builds=%v terminal=%d want=%v terminal=0", got, terminalID, want)
	}
	return ""
}

func observeDBVersionsFinalUnused(versions db.VersionsDB, job db.Job, profile string) string {
	var cursor db.BuildCursor
	var expected []db.Build
	switch profile {
	case "unused-one":
		build, err := dbVersionsFinalBuild(job, db.BuildStatusSucceeded)
		if err != nil {
			return err.Error()
		}
		cursor = db.BuildCursor{ID: build.ID()}
		expected = []db.Build{build}
	case "unused-older-newer", "unused-reruns-excluded":
		older, err := dbVersionsFinalFinished(job, 5)
		if err != nil {
			return err.Error()
		}
		cursorBuild, err := dbVersionsFinalBuild(job, db.BuildStatusSucceeded)
		if err != nil {
			return err.Error()
		}
		newer, err := dbVersionsFinalFinished(job, 5)
		if err != nil {
			return err.Error()
		}
		if profile == "unused-reruns-excluded" {
			for range 5 {
				if _, err := dbVersionsFinalRerun(job, cursorBuild); err != nil {
					return err.Error()
				}
			}
		}
		cursor = db.BuildCursor{ID: cursorBuild.ID()}
		expected = append(expected, newer...)
		expected = append(expected, cursorBuild)
		expected = append(expected, dbVersionsFinalReverse(older)...)
	case "unused-rerun-cursor":
		build1, err := dbVersionsFinalBuild(job, db.BuildStatusSucceeded)
		if err != nil {
			return err.Error()
		}
		build2, err := dbVersionsFinalBuild(job, db.BuildStatusFailed)
		if err != nil {
			return err.Error()
		}
		build3, err := dbVersionsFinalBuild(job, db.BuildStatusSucceeded)
		if err != nil {
			return err.Error()
		}
		build4, err := dbVersionsFinalRerun(job, build2)
		if err != nil {
			return err.Error()
		}
		build5, err := dbVersionsFinalRerun(job, build2)
		if err != nil {
			return err.Error()
		}
		build6, err := dbVersionsFinalRerun(job, build2)
		if err != nil {
			return err.Error()
		}
		build7, err := dbVersionsFinalBuild(job, db.BuildStatusSucceeded)
		if err != nil {
			return err.Error()
		}
		cursor = db.BuildCursor{ID: build5.ID(), RerunOf: sql.NullInt64{Int64: int64(build5.RerunOf()), Valid: true}}
		expected = []db.Build{build6, build3, build7, build5, build4, build1}
	default:
		return fmt.Sprintf("unknown unused profile %q", profile)
	}
	paginated, err := versions.UnusedBuilds(context.Background(), job.ID(), cursor)
	if err != nil {
		return err.Error()
	}
	got, terminalID, err := dbVersionsFinalDrain(&paginated)
	if err != nil {
		return err.Error()
	}
	want := dbVersionsFinalIDs(expected...)
	if !reflect.DeepEqual(got, want) || terminalID != 0 {
		return fmt.Sprintf("builds=%v terminal=%d want=%v terminal=0", got, terminalID, want)
	}
	return ""
}

func observeDBVersionsFinalFind(database JetbridgeDB) string {
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "versions-final-find"})
	if err != nil {
		return err.Error()
	}
	pipeline, _, err := team.SavePipeline(atc.PipelineRef{Name: "pipeline"}, atc.Config{Resources: atc.ResourceConfigs{{Name: "resource", Type: "registry-image", Source: atc.Source{"repository": "example/resource"}}}}, 0, false)
	if err != nil {
		return err.Error()
	}
	resource, found, err := pipeline.Resource("resource")
	if err != nil || !found {
		return fmt.Sprintf("load resource: found=%t err=%v", found, err)
	}
	version := atc.Version{"tag": "v1", "commit": "v2"}
	config, err := db.NewResourceConfigFactory(database.Conn, database.LockFactory).FindOrCreateResourceConfig(resource.Type(), resource.Source(), nil)
	if err != nil {
		return err.Error()
	}
	id := resource.ID()
	scope, err := config.FindOrCreateScope(&id)
	if err != nil {
		return err.Error()
	}
	if err := scope.SaveVersions(db.SpanContext{}, []atc.Version{version}); err != nil {
		return err.Error()
	}
	if err := resource.SetResourceConfigScope(scope); err != nil {
		return err.Error()
	}
	got, found, err := db.NewVersionsDB(database.Conn, 5, gocache.New(-1, -1)).FindVersionOfResource(context.Background(), resource.ID(), atc.Version{"tag": "v1"})
	if err != nil {
		return err.Error()
	}
	encoded, err := json.Marshal(version)
	if err != nil {
		return err.Error()
	}
	digest := sha256.Sum256(encoded)
	want := hex.EncodeToString(digest[:])
	if !found || string(got) != want {
		return fmt.Sprintf("found=%t version=%q want=%q", found, got, want)
	}
	return ""
}
