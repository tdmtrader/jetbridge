package steps

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

type APIJobsNextObservation struct {
	Profile     string
	Status      int
	ContentType string
	Cache       string
	Expires     string
	Body        []byte
	Names       []string
	BuildID     int
}

func APIJobsNextStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, APIJobsNextObservation](
			"the remaining production jobs API behavior {string} is exercised",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, rec *brine.Recorder, resources brine.Resources) (APIJobsNextObservation, error) {
				profile, err := paramAt("the remaining production jobs API behavior {string} is exercised", p, 0)
				if err != nil {
					return APIJobsNextObservation{}, err
				}
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return APIJobsNextObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				boundary, err := newStrictJobBoundary(database, rec)
				if err != nil {
					return APIJobsNextObservation{}, err
				}
				return observeAPIJobsNext(boundary, profile)
			},
		),
		brine.DefineCheck[APIJobsNextObservation](
			"the remaining production jobs API behavior exactly matches {string}",
			func(in APIJobsNextObservation, p brine.Params, _ *brine.Recorder) error {
				profile, err := paramAt("the remaining production jobs API behavior exactly matches {string}", p, 0)
				if err != nil {
					return err
				}
				if in.Profile != profile {
					return fmt.Errorf("profile got %q, want %q", in.Profile, profile)
				}
				return validateAPIJobsNext(in)
			},
		),
	}
}

func observeAPIJobsNext(boundary *strictJobBoundary, profile string) (APIJobsNextObservation, error) {
	out := APIJobsNextObservation{Profile: profile}
	client := boundary.httpClient
	path := boundary.jobPath("build", "")

	switch profile {
	case "list-public-only", "list-member-private", "list-empty":
		if _, err := boundary.database.Conn.Exec("UPDATE teams SET admin = FALSE WHERE id = $1", boundary.team.ID()); err != nil {
			return out, err
		}
		discriminator, found, err := boundary.team.Pipeline(atc.PipelineRef{Name: boundary.ref.Name})
		if err != nil {
			return out, err
		}
		if found {
			if _, _, err := boundary.team.SavePipeline(
				atc.PipelineRef{Name: boundary.ref.Name},
				atc.Config{},
				discriminator.ConfigVersion(),
				false,
			); err != nil {
				return out, err
			}
		}
		if profile == "list-empty" {
			if err := boundary.pipeline.Archive(); err != nil {
				return out, err
			}
		} else {
			publicTeam, err := boundary.database.TeamFactory.CreateTeam(atc.Team{Name: "jobs-next-public"})
			if err != nil {
				return out, err
			}
			publicPipeline, _, err := publicTeam.SavePipeline(atc.PipelineRef{Name: "public"}, atc.Config{Jobs: atc.JobConfigs{{Name: "public-job"}}}, 0, false)
			if err != nil {
				return out, err
			}
			if err := publicPipeline.Expose(); err != nil {
				return out, err
			}
			privateTeam, err := boundary.database.TeamFactory.CreateTeam(atc.Team{Name: "jobs-next-private"})
			if err != nil {
				return out, err
			}
			if _, _, err := privateTeam.SavePipeline(atc.PipelineRef{Name: "private"}, atc.Config{Jobs: atc.JobConfigs{{Name: "private-job"}}}, 0, false); err != nil {
				return out, err
			}
		}
		path = "/api/v1/jobs"
		if profile != "list-member-private" {
			client = boundary.publicHTTP
		}

	case "get-public-anonymous", "get-public-outsider", "badge-public-outsider", "dashboard-public-anonymous", "builds-public-outsider":
		if err := boundary.pipeline.Expose(); err != nil {
			return out, err
		}
		switch profile {
		case "get-public-anonymous":
			client = boundary.publicHTTP
		case "get-public-outsider":
			client = boundary.outsiderHTTP
		case "badge-public-outsider":
			client, path = boundary.outsiderHTTP, boundary.jobPath("build", "/badge")
		case "dashboard-public-anonymous":
			client, path = boundary.publicHTTP, fmt.Sprintf("/api/v1/teams/%s/pipelines/%s/jobs?%s", jobStrictTeamName, boundary.ref.Name, boundary.ref.QueryParams().Encode())
		case "builds-public-outsider":
			build, err := boundary.job.CreateBuild("jobs-next")
			if err != nil {
				return out, err
			}
			out.BuildID = build.ID()
			client, path = boundary.outsiderHTTP, boundary.jobPath("build", "/builds")
		}

	case "dashboard-empty":
		empty, _, err := boundary.team.SavePipeline(atc.PipelineRef{Name: "empty"}, atc.Config{}, 0, false)
		if err != nil {
			return out, err
		}
		path = fmt.Sprintf("/api/v1/teams/%s/pipelines/%s/jobs", jobStrictTeamName, empty.Name())

	default:
		path = boundary.jobPath("build", "/badge")
		status, finished := badgeStatusForProfile(profile)
		if finished {
			build, err := boundary.job.CreateBuild("jobs-next-badge")
			if err != nil {
				return out, err
			}
			if _, err := build.Start(atc.Plan{}); err != nil {
				return out, err
			}
			if err := build.Finish(status); err != nil {
				return out, err
			}
		}
		switch profile {
		case "badge-default-empty":
			path += "&title="
		case "badge-scale-short":
			path += "&title=test"
		case "badge-scale-medium":
			path += "&title=integration"
		case "badge-scale-long":
			path += "&title=very-long-deployment-name"
		case "badge-production-title":
			path += "&title=production-deployment"
		case "badge-status-width":
			path += "&title=custom"
		}
	}

	request, err := http.NewRequest(http.MethodGet, boundary.url+path, nil)
	if err != nil {
		return out, err
	}
	response, err := client.Do(request)
	if err != nil {
		return out, err
	}
	defer response.Body.Close()
	out.Status = response.StatusCode
	out.ContentType = response.Header.Get("Content-Type")
	out.Cache = response.Header.Get("Cache-Control")
	out.Expires = response.Header.Get("Expires")
	out.Body, err = io.ReadAll(response.Body)
	if err != nil {
		return out, err
	}
	if strings.HasPrefix(profile, "list-") || strings.HasPrefix(profile, "dashboard-") {
		var summaries []atc.JobSummary
		if err := json.Unmarshal(out.Body, &summaries); err != nil {
			return out, err
		}
		for _, summary := range summaries {
			out.Names = append(out.Names, summary.Name)
		}
		sort.Strings(out.Names)
	}
	return out, nil
}

func badgeStatusForProfile(profile string) (db.BuildStatus, bool) {
	switch profile {
	case "badge-errored":
		return db.BuildStatusErrored, true
	case "badge-failed":
		return db.BuildStatusFailed, true
	case "badge-aborted":
		return db.BuildStatusAborted, true
	case "badge-succeeded", "badge-default-omitted", "badge-default-empty", "badge-scale-short", "badge-scale-medium", "badge-scale-long", "badge-production-title", "badge-status-width":
		return db.BuildStatusSucceeded, true
	default:
		return "", false
	}
}

func validateAPIJobsNext(in APIJobsNextObservation) error {
	if in.Status != http.StatusOK {
		return fmt.Errorf("status got %d, want 200; body=%q", in.Status, string(in.Body))
	}
	body := string(in.Body)
	switch in.Profile {
	case "list-public-only":
		return wantAPIJobsNextNames(in.Names, []string{"public-job"})
	case "list-member-private":
		return wantAPIJobsNextNames(in.Names, []string{"build", "public-job"})
	case "list-empty", "dashboard-empty":
		return wantAPIJobsNextNames(in.Names, nil)
	case "get-public-anonymous", "get-public-outsider", "badge-public-outsider", "dashboard-public-anonymous":
		// The corresponding source leaves assert only successful public access.
		return nil
	case "builds-public-outsider":
		var builds []atc.Build
		if err := json.Unmarshal(in.Body, &builds); err != nil {
			return err
		}
		if len(builds) != 1 || builds[0].ID != in.BuildID {
			return fmt.Errorf("builds got=%#v want id=%d", builds, in.BuildID)
		}
	case "badge-buildless":
		if in.ContentType != "image/svg+xml" || in.Cache != "no-cache, no-store, must-revalidate" || in.Expires != "0" || !strings.Contains(body, "unknown") || !strings.Contains(body, "#9f9f9f") {
			return fmt.Errorf("buildless badge headers/body mismatch: %q", body)
		}
	case "badge-errored":
		return badgeContains(body, "errored", "#fe7d37")
	case "badge-failed":
		return badgeContains(body, "failing", "#e05d44")
	case "badge-succeeded":
		return badgeContains(body, "passing", "#44cc11")
	case "badge-aborted":
		return badgeContains(body, "aborted", "#8f4b2d")
	case "badge-default-omitted", "badge-default-empty":
		return badgeContains(body, "build", `width="88"`, `d="M0 0h37v20H0z"`)
	case "badge-scale-short":
		return badgeContains(body, "test", `width="87"`)
	case "badge-scale-medium":
		return badgeContains(body, "integration", `width="123"`)
	case "badge-scale-long":
		return badgeContains(body, "very-long-deployment-name", `width="201"`)
	case "badge-production-title":
		if !strings.Contains(body, "production-deployment") || !strings.Contains(body, "passing") || !strings.Contains(body, `width="`) {
			return fmt.Errorf("production badge body=%q", body)
		}
	case "badge-status-width":
		if !strings.Contains(body, "h51v20H") {
			return fmt.Errorf("status width body=%q", body)
		}
	default:
		return fmt.Errorf("unknown jobs API profile %q", in.Profile)
	}
	return nil
}

func wantAPIJobsNextNames(got, want []string) error {
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		return fmt.Errorf("names got=%v want=%v", got, want)
	}
	return nil
}

func badgeContains(body string, values ...string) error {
	for _, value := range values {
		if !strings.Contains(body, value) {
			return fmt.Errorf("badge body missing %q: %q", value, body)
		}
	}
	return nil
}
