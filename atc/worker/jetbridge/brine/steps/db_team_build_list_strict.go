package steps

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

type DBTeamBuildListObservation struct {
	Profile string
	Failure string
}

func DBTeamBuildListStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, DBTeamBuildListObservation](
			"the production team build list behavior {string} is exercised",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (DBTeamBuildListObservation, error) {
				profile, err := paramAt("the production team build list behavior {string} is exercised", p, 0)
				if err != nil {
					return DBTeamBuildListObservation{}, err
				}
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return DBTeamBuildListObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				return DBTeamBuildListObservation{Profile: profile, Failure: observeDBTeamBuildList(database, profile)}, nil
			},
		),
		brine.DefineCheck[DBTeamBuildListObservation](
			"the team build list behavior exactly matches {string}",
			func(in DBTeamBuildListObservation, p brine.Params, _ *brine.Recorder) error {
				profile, err := paramAt("the team build list behavior exactly matches {string}", p, 0)
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

func observeDBTeamBuildList(database JetbridgeDB, profile string) string {
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "build-list-team"})
	if err != nil {
		return err.Error()
	}

	fail := func(format string, args ...any) string { return fmt.Sprintf(format, args...) }

	if profile == "private-public-public-other" {
		requesting, err := database.TeamFactory.CreateTeam(atc.Team{Name: "requesting-team"})
		if err != nil {
			return err.Error()
		}
		for range 3 {
			if _, err := requesting.CreateOneOffBuild(); err != nil {
				return err.Error()
			}
		}
		pipeline, _, err := team.SavePipeline(atc.PipelineRef{Name: "visible-pipeline"}, atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}}, 1, false)
		if err != nil {
			return err.Error()
		}
		job, found, err := pipeline.Job("job")
		if err != nil || !found {
			return fail("job lookup: found=%t err=%v", found, err)
		}
		publicIDs := []int{}
		for range 2 {
			build, err := job.CreateBuild("strict migration")
			if err != nil {
				return err.Error()
			}
			publicIDs = append(publicIDs, build.ID())
		}
		if err := pipeline.Expose(); err != nil {
			return err.Error()
		}
		builds, _, err := requesting.PrivateAndPublicBuilds(db.Page{Limit: 10})
		if err != nil {
			return err.Error()
		}
		if len(builds) != 5 || !containsAllBuildIDs(builds, publicIDs) {
			return fail("visible builds got ids=%s, want count=5 including=%v", buildIDs(builds), publicIDs)
		}
		return ""
	}

	if strings.HasPrefix(profile, "time-") {
		builds, err := createTimedBuilds(database, team)
		if err != nil {
			return err.Error()
		}
		page := db.Page{Limit: 50}
		var want []int
		switch profile {
		case "time-limit":
			page.Limit = 2
			want = []int{builds[3].ID(), builds[2].ID()}
		case "time-to-inclusive":
			page.To = db.NewIntPtr(int(builds[2].StartTime().Unix()))
			want = buildIDSlice(builds[0], builds[1], builds[2])
		case "time-from-inclusive":
			page.From = db.NewIntPtr(int(builds[1].StartTime().Unix()))
			want = buildIDSlice(builds[1], builds[2], builds[3])
		case "time-range-inclusive":
			page.From = db.NewIntPtr(int(builds[1].StartTime().Unix()))
			page.To = db.NewIntPtr(int(builds[2].StartTime().Unix()))
			want = buildIDSlice(builds[1], builds[2])
		default:
			return fail("unknown time profile %q", profile)
		}
		got, _, err := team.BuildsWithTime(page)
		if err != nil {
			return err.Error()
		}
		if !sameBuildIDs(got, want) {
			return fail("timed builds got=%s want=%v", buildIDs(got), want)
		}
		return ""
	}

	if strings.HasPrefix(profile, "builds-") {
		current, err := createOrdinaryBuilds(team)
		if err != nil {
			return err.Error()
		}
		page := db.Page{Limit: 50}
		want := buildIDSlice(current...)
		switch profile {
		case "builds-current-team":
		case "builds-from-inclusive":
			page.From = db.NewIntPtr(current[2].ID())
			want = buildIDSlice(current[2], current[3])
		case "builds-to-inclusive":
			page.To = db.NewIntPtr(current[2].ID())
			want = buildIDSlice(current[0], current[1], current[2])
		case "builds-range-inclusive":
			page.From = db.NewIntPtr(current[1].ID())
			page.To = db.NewIntPtr(current[3].ID())
			want = buildIDSlice(current[1], current[2], current[3])
		case "builds-private-other-team", "builds-public-other-team":
			other, err := database.TeamFactory.CreateTeam(atc.Team{Name: "query-team"})
			if err != nil {
				return err.Error()
			}
			others := []db.Build{}
			for range 3 {
				build, err := other.CreateOneOffBuild()
				if err != nil {
					return err.Error()
				}
				others = append(others, build)
			}
			if profile == "builds-public-other-team" {
				pipeline, _, err := team.SavePipeline(atc.PipelineRef{Name: "public-pipeline"}, atc.Config{}, 1, false)
				if err != nil {
					return err.Error()
				}
				if err := pipeline.Expose(); err != nil {
					return err.Error()
				}
			}
			got, _, err := other.Builds(db.Page{Limit: 10})
			if err != nil {
				return err.Error()
			}
			want = buildIDSlice(others...)
			if !sameBuildIDs(got, want) {
				return fail("team builds got=%s want=%v", buildIDs(got), want)
			}
			return ""
		default:
			return fail("unknown builds profile %q", profile)
		}
		got, _, err := team.Builds(page)
		if err != nil {
			return err.Error()
		}
		if !sameBuildIDs(got, want) {
			return fail("team builds got=%s want=%v", buildIDs(got), want)
		}
		return ""
	}

	return fail("unknown team build list profile %q", profile)
}

func createTimedBuilds(database JetbridgeDB, team db.Team) ([]db.Build, error) {
	builds, err := createJobBuilds(team, 4)
	if err != nil {
		return nil, err
	}
	for i := range builds {
		start := time.Date(2020, 11, i+1, 0, 0, 0, 0, time.UTC)
		if _, err := database.Conn.Exec("UPDATE builds SET start_time = to_timestamp($1) WHERE id = $2", start.Unix(), builds[i].ID()); err != nil {
			return nil, err
		}
		found, err := builds[i].Reload()
		if err != nil || !found {
			return nil, fmt.Errorf("reload timed build %d: found=%t err=%v", builds[i].ID(), found, err)
		}
	}
	return builds, nil
}

func createOrdinaryBuilds(team db.Team) ([]db.Build, error) {
	oneOff, err := team.CreateOneOffBuild()
	if err != nil {
		return nil, err
	}
	jobBuilds, err := createJobBuilds(team, 3)
	if err != nil {
		return nil, err
	}
	return append([]db.Build{oneOff}, jobBuilds...), nil
}

func createJobBuilds(team db.Team, count int) ([]db.Build, error) {
	pipeline, _, err := team.SavePipeline(atc.PipelineRef{Name: "build-list-pipeline"}, atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}}, 1, false)
	if err != nil {
		return nil, err
	}
	job, found, err := pipeline.Job("job")
	if err != nil || !found {
		return nil, fmt.Errorf("job lookup: found=%t err=%v", found, err)
	}
	builds := make([]db.Build, 0, count)
	for range count {
		build, err := job.CreateBuild("strict migration")
		if err != nil {
			return nil, err
		}
		builds = append(builds, build)
	}
	return builds, nil
}

func buildIDSlice(builds ...db.Build) []int {
	ids := make([]int, len(builds))
	for i := range builds {
		ids[i] = builds[i].ID()
	}
	return ids
}

func sameBuildIDs(builds []db.BuildForAPI, want []int) bool {
	if len(builds) != len(want) {
		return false
	}
	got := make([]int, len(builds))
	for i := range builds {
		got[i] = builds[i].ID()
	}
	sort.Ints(got)
	sort.Ints(want)
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func containsAllBuildIDs(builds []db.BuildForAPI, want []int) bool {
	seen := map[int]bool{}
	for _, build := range builds {
		seen[build.ID()] = true
	}
	for _, id := range want {
		if !seen[id] {
			return false
		}
	}
	return true
}

func buildIDs(builds []db.BuildForAPI) string {
	ids := make([]string, len(builds))
	for i := range builds {
		ids[i] = fmt.Sprint(builds[i].ID())
	}
	return strings.Join(ids, ",")
}
