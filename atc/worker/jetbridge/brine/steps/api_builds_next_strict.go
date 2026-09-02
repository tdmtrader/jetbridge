package steps

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

type APIBuildsNextObservation struct {
	Profile string
	Failure string
}

func APIBuildsNextStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, APIBuildsNextObservation](
			"the next strict builds API executes profile {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, rec *brine.Recorder, resources brine.Resources) (APIBuildsNextObservation, error) {
				profile, err := paramAt("the next strict builds API executes profile {string}", p, 0)
				if err != nil {
					return APIBuildsNextObservation{}, err
				}
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return APIBuildsNextObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				boundary, err := newStrictBuildBoundaryForProfile(database, rec, profile)
				if err != nil {
					return APIBuildsNextObservation{}, err
				}
				err = boundary.observeNextStrict(profile)
				observation := APIBuildsNextObservation{Profile: profile}
				if err != nil {
					observation.Failure = err.Error()
				}
				return observation, nil
			},
		),
		brine.DefineCheck[APIBuildsNextObservation](
			"the next strict builds API observation exactly matches profile {string}",
			func(in APIBuildsNextObservation, p brine.Params, _ *brine.Recorder) error {
				profile, err := paramAt("the next strict builds API observation exactly matches profile {string}", p, 0)
				if err != nil {
					return err
				}
				if in.Profile != profile {
					return fmt.Errorf("next builds API profile got %q, want %q", in.Profile, profile)
				}
				if in.Failure != "" {
					return fmt.Errorf("%s: %s", profile, in.Failure)
				}
				return nil
			},
		),
	}
}

func (b *strictBuildBoundary) observeNextStrict(profile string) error {
	switch {
	case strings.Contains(profile, "list-"):
		return b.observeNextStrictList(profile)
	case strings.Contains(profile, "invalid-"):
		path := "/api/v1/builds/nope"
		if strings.HasSuffix(profile, "resources") {
			path += "/resources"
		}
		return b.expectNextStatus(b.publicHTTP, http.MethodGet, path, http.StatusBadRequest)
	case strings.Contains(profile, "missing-"):
		method := http.MethodGet
		path := "/api/v1/builds/999999999"
		switch {
		case strings.HasSuffix(profile, "resources"):
			path += "/resources"
		case strings.HasSuffix(profile, "events"):
			path += "/events"
		case strings.HasSuffix(profile, "preparation"):
			path += "/preparation"
		case strings.HasSuffix(profile, "plan"):
			path += "/plan"
		case strings.HasSuffix(profile, "abort"):
			method, path = http.MethodPut, path+"/abort"
			return b.expectNextStatus(b.httpClient, method, path, http.StatusNotFound)
		}
		return b.expectNextStatus(b.publicHTTP, method, path, http.StatusNotFound)
	case strings.Contains(profile, "get-"):
		build, err := b.nextStartedBuild("next-get")
		if err != nil {
			return err
		}
		client := b.httpClient
		if strings.Contains(profile, "public") {
			client = b.publicHTTP
		} else if strings.Contains(profile, "outsider") {
			client = b.outsiderHTTP
		}
		return b.expectNextStatus(client, http.MethodGet, "/api/v1/builds/"+strconv.Itoa(build.ID()), http.StatusOK)
	case strings.Contains(profile, "resources-"):
		build, err := b.nextStartedBuild("next-resources")
		if err != nil {
			return err
		}
		client := b.httpClient
		if strings.Contains(profile, "public") {
			client = b.publicHTTP
		}
		response, _, err := b.request(client, http.MethodGet, "/api/v1/builds/"+strconv.Itoa(build.ID())+"/resources", nil)
		if err != nil {
			return err
		}
		if strings.HasSuffix(profile, "header") {
			if response.Header.Get("Content-Type") != "application/json" {
				return fmt.Errorf("resources content type got %q", response.Header.Get("Content-Type"))
			}
			return nil
		}
		if response.StatusCode != http.StatusOK {
			return fmt.Errorf("resources status got %d, want 200", response.StatusCode)
		}
		return nil
	case strings.Contains(profile, "preparation-"):
		build, err := b.nextStartedBuild("next-preparation")
		if err != nil {
			return err
		}
		client := b.httpClient
		if strings.Contains(profile, "public") {
			client = b.publicHTTP
		}
		response, _, err := b.request(client, http.MethodGet, "/api/v1/builds/"+strconv.Itoa(build.ID())+"/preparation", nil)
		if err != nil {
			return err
		}
		if strings.HasSuffix(profile, "header") {
			if response.Header.Get("Content-Type") != "application/json" {
				return fmt.Errorf("preparation content type got %q", response.Header.Get("Content-Type"))
			}
			return nil
		}
		if response.StatusCode != http.StatusOK {
			return fmt.Errorf("preparation status got %d, want 200", response.StatusCode)
		}
		return nil
	case strings.Contains(profile, "plan-"):
		return b.observeNextStrictPlan(profile)
	default:
		return fmt.Errorf("unknown next strict builds profile %q", profile)
	}
}

func (b *strictBuildBoundary) nextStartedBuild(id atc.PlanID) (db.Build, error) {
	build, err := b.job.CreateBuild(buildStrictUserID)
	if err != nil {
		return nil, err
	}
	started, err := build.Start(atc.Plan{ID: id, Task: &atc.TaskPlan{Name: "visible-task", Config: &atc.TaskConfig{Run: atc.TaskRunConfig{Path: "private-path"}}}})
	if err != nil {
		return nil, err
	}
	if !started {
		return nil, fmt.Errorf("build %d did not start", build.ID())
	}
	found, err := build.Reload()
	if err != nil || !found {
		return nil, firstError(err, fmt.Errorf("started build %d disappeared", build.ID()))
	}
	return build, nil
}

func (b *strictBuildBoundary) expectNextStatus(client *http.Client, method, path string, want int) error {
	response, raw, err := b.request(client, method, path, nil)
	if err != nil {
		return err
	}
	if response.StatusCode != want {
		return fmt.Errorf("status got %d, want %d (body %q)", response.StatusCode, want, string(raw))
	}
	return nil
}

func (b *strictBuildBoundary) observeNextStrictList(profile string) error {
	client := b.httpClient
	if strings.Contains(profile, "public") {
		client = b.publicHTTP
	}
	if strings.HasSuffix(profile, "header") {
		response, _, err := b.request(client, http.MethodGet, "/api/v1/builds", nil)
		if err != nil {
			return err
		}
		if response.Header.Get("Content-Type") != "application/json" {
			return fmt.Errorf("list content type got %q", response.Header.Get("Content-Type"))
		}
		return nil
	}
	builds := make([]db.Build, 4)
	for i := range builds {
		build, err := b.job.CreateBuild(buildStrictUserID)
		if err != nil {
			return err
		}
		builds[i] = build
	}
	path := fmt.Sprintf("/api/v1/builds?from=%d&limit=2", builds[1].ID())
	response, raw, err := b.request(client, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	var actual []atc.Build
	if err := json.Unmarshal(raw, &actual); err != nil {
		return err
	}
	if len(actual) != 2 || actual[0].ID != builds[2].ID() || actual[1].ID != builds[1].ID() {
		return fmt.Errorf("list page IDs got %#v, want [%d %d]", actual, builds[2].ID(), builds[1].ID())
	}
	wantPrevious := fmt.Sprintf(`<https://concourse.invalid/api/v1/builds?from=%d&limit=2>; rel="previous"`, builds[3].ID())
	wantNext := fmt.Sprintf(`<https://concourse.invalid/api/v1/builds?to=%d&limit=2>; rel="next"`, builds[0].ID())
	links := response.Header.Values("Link")
	if len(links) != 2 || !containsNextString(links, wantPrevious) || !containsNextString(links, wantNext) {
		return fmt.Errorf("list links got %v, want %q and %q", links, wantPrevious, wantNext)
	}
	return nil
}

func containsNextString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func (b *strictBuildBoundary) observeNextStrictPlan(profile string) error {
	hasPlan := !strings.Contains(profile, "no-plan")
	var build db.Build
	var err error
	if hasPlan {
		build, err = b.nextStartedBuild("next-plan")
	} else {
		build, err = b.job.CreateBuild(buildStrictUserID)
	}
	if err != nil {
		return err
	}
	client := b.httpClient
	if strings.Contains(profile, "public") {
		client = b.publicHTTP
	}
	response, raw, err := b.request(client, http.MethodGet, "/api/v1/builds/"+strconv.Itoa(build.ID())+"/plan", nil)
	if err != nil {
		return err
	}
	if strings.HasSuffix(profile, "header") {
		want := "application/json"
		if !hasPlan {
			want = ""
		}
		if response.Header.Get("Content-Type") != want {
			return fmt.Errorf("plan content type got %q, want %q", response.Header.Get("Content-Type"), want)
		}
		return nil
	}
	if strings.HasSuffix(profile, "body") {
		var actual any
		var expected any
		if err := json.Unmarshal(raw, &actual); err != nil {
			return err
		}
		expectedRaw, err := json.Marshal(atc.PublicBuildPlan{Schema: build.Schema(), Plan: build.PublicPlan()})
		if err != nil {
			return err
		}
		if err := json.Unmarshal(expectedRaw, &expected); err != nil {
			return err
		}
		if !reflect.DeepEqual(actual, expected) {
			return fmt.Errorf("plan body got %#v, want %#v", actual, expected)
		}
		return nil
	}
	want := http.StatusOK
	if !hasPlan {
		want = http.StatusNotFound
	}
	if response.StatusCode != want {
		return fmt.Errorf("plan status got %d, want %d", response.StatusCode, want)
	}
	return nil
}
