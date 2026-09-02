package steps

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/jobserver"
	"github.com/concourse/concourse/atc/db"
)

type apiPipelinesNextObservation struct {
	Profile string
	Failure string
}

func APIPipelinesNextStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, apiPipelinesNextObservation](
			"the remaining production pipelines API behavior {string} is exercised",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, rec *brine.Recorder, resources brine.Resources) (apiPipelinesNextObservation, error) {
				profile, err := paramAt("the remaining production pipelines API behavior {string} is exercised", p, 0)
				if err != nil {
					return apiPipelinesNextObservation{}, err
				}
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return apiPipelinesNextObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				return apiPipelinesNextObservation{Profile: profile, Failure: observeAPIPipelinesNext(database, rec, profile)}, nil
			},
		),
		brine.DefineCheck[apiPipelinesNextObservation](
			"the remaining pipelines API behavior exactly matches {string}",
			func(in apiPipelinesNextObservation, p brine.Params, _ *brine.Recorder) error {
				profile, err := paramAt("the remaining pipelines API behavior exactly matches {string}", p, 0)
				if err != nil {
					return err
				}
				if in.Profile != profile {
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

func observeAPIPipelinesNext(database JetbridgeDB, rec *brine.Recorder, profile string) string {
	fail := func(format string, args ...any) string { return fmt.Sprintf(format, args...) }
	boundary, err := newStrictPipelineClientAPI(database, rec)
	if err != nil {
		return err.Error()
	}
	if err := boundary.save("target"); err != nil {
		return err.Error()
	}
	pipeline := boundary.Saved["target"]

	method, path := http.MethodGet, "/api/v1/teams/api-team/pipelines/target"
	identity := "owner"
	var body []byte
	expectedStatus := 0
	expectedContentType := ""
	verifyBadge := ""
	verifyNoBuild := false
	verifyRename := ""
	var verifyBounded []int

	unauthenticated := map[string]struct {
		method string
		path   string
		body   string
	}{
		"get-private-unauth-primary":   {http.MethodGet, "/api/v1/teams/api-team/pipelines/target", ""},
		"get-private-unauth-secondary": {http.MethodGet, "/api/v1/teams/api-team/pipelines/target", ""},
		"badge-private-unauth":         {http.MethodGet, "/api/v1/teams/api-team/pipelines/target/badge", ""},
		"delete-unauth":                {http.MethodDelete, "/api/v1/teams/api-team/pipelines/target", ""},
		"pause-unauth":                 {http.MethodPut, "/api/v1/teams/api-team/pipelines/target/pause", ""},
		"archive-unauth":               {http.MethodPut, "/api/v1/teams/api-team/pipelines/target/archive", ""},
		"unpause-unauth":               {http.MethodPut, "/api/v1/teams/api-team/pipelines/target/unpause", ""},
		"expose-unauth":                {http.MethodPut, "/api/v1/teams/api-team/pipelines/target/expose", ""},
		"hide-unauth":                  {http.MethodPut, "/api/v1/teams/api-team/pipelines/target/hide", ""},
		"order-global-unauth":          {http.MethodPut, "/api/v1/teams/api-team/pipelines/ordering", `["target"]`},
		"order-instance-unauth":        {http.MethodPut, "/api/v1/teams/api-team/pipelines/target/ordering", `[{}]`},
		"versions-unauth":              {http.MethodGet, "/api/v1/teams/api-team/pipelines/target/versions-db", ""},
		"rename-unauth":                {http.MethodPut, "/api/v1/teams/api-team/pipelines/target/rename", `{"name":"renamed"}`},
		"list-builds-unauth":           {http.MethodGet, "/api/v1/teams/api-team/pipelines/target/builds", ""},
		"create-build-unauth":          {http.MethodPost, "/api/v1/teams/api-team/pipelines/target/builds", apiPipelinesNextPlanJSON},
	}
	forbidden := map[string]struct {
		method string
		path   string
		body   string
	}{
		"get-private-outsider":    {http.MethodGet, "/api/v1/teams/api-team/pipelines/target", ""},
		"badge-private-outsider":  {http.MethodGet, "/api/v1/teams/api-team/pipelines/target/badge", ""},
		"delete-outsider":         {http.MethodDelete, "/api/v1/teams/api-team/pipelines/target", ""},
		"pause-outsider":          {http.MethodPut, "/api/v1/teams/api-team/pipelines/target/pause", ""},
		"unpause-outsider":        {http.MethodPut, "/api/v1/teams/api-team/pipelines/target/unpause", ""},
		"expose-outsider":         {http.MethodPut, "/api/v1/teams/api-team/pipelines/target/expose", ""},
		"hide-outsider":           {http.MethodPut, "/api/v1/teams/api-team/pipelines/target/hide", ""},
		"order-global-outsider":   {http.MethodPut, "/api/v1/teams/api-team/pipelines/ordering", `["target"]`},
		"order-instance-outsider": {http.MethodPut, "/api/v1/teams/api-team/pipelines/target/ordering", `[{}]`},
		"rename-outsider":         {http.MethodPut, "/api/v1/teams/api-team/pipelines/target/rename", `{"name":"renamed"}`},
		"create-build-outsider":   {http.MethodPost, "/api/v1/teams/api-team/pipelines/target/builds", apiPipelinesNextPlanJSON},
	}

	if request, ok := unauthenticated[profile]; ok {
		method, path, body, identity, expectedStatus = request.method, request.path, []byte(request.body), "anonymous", http.StatusUnauthorized
		verifyNoBuild = strings.HasPrefix(profile, "create-build-")
	} else if request, ok := forbidden[profile]; ok {
		method, path, body, identity, expectedStatus = request.method, request.path, []byte(request.body), "outsider", http.StatusForbidden
		verifyNoBuild = strings.HasPrefix(profile, "create-build-")
	} else {
		switch profile {
		case "global-list-json":
			path, expectedContentType = "/api/v1/pipelines", "application/json"
		case "team-list-json":
			path, expectedContentType = "/api/v1/teams/api-team/pipelines", "application/json"
		case "get-json":
			expectedContentType = "application/json"
		case "versions-json":
			path, expectedContentType = "/api/v1/teams/api-team/pipelines/target/versions-db", "application/json"
		case "list-builds-json":
			path, expectedContentType = "/api/v1/teams/api-team/pipelines/target/builds", "application/json"
		case "create-build-json":
			method, path, body, expectedContentType = http.MethodPost, "/api/v1/teams/api-team/pipelines/target/builds", []byte(apiPipelinesNextPlanJSON), "application/json"
		case "badge-public-ok":
			if err := pipeline.Expose(); err != nil {
				return err.Error()
			}
			identity, path, expectedStatus = "anonymous", "/api/v1/teams/api-team/pipelines/target/badge", http.StatusOK
		case "badge-owner-ok":
			path, expectedStatus = "/api/v1/teams/api-team/pipelines/target/badge", http.StatusOK
		case "pause-owner-ok":
			method, path, expectedStatus = http.MethodPut, "/api/v1/teams/api-team/pipelines/target/pause", http.StatusOK
		case "unpause-owner-ok":
			if err := pipeline.Pause("fixture"); err != nil {
				return err.Error()
			}
			method, path, expectedStatus = http.MethodPut, "/api/v1/teams/api-team/pipelines/target/unpause", http.StatusOK
		case "expose-owner-ok":
			method, path, expectedStatus = http.MethodPut, "/api/v1/teams/api-team/pipelines/target/expose", http.StatusOK
		case "hide-owner-ok":
			if err := pipeline.Expose(); err != nil {
				return err.Error()
			}
			method, path, expectedStatus = http.MethodPut, "/api/v1/teams/api-team/pipelines/target/hide", http.StatusOK
		case "order-global-owner-ok":
			method, path, body, expectedStatus = http.MethodPut, "/api/v1/teams/api-team/pipelines/ordering", []byte(`["target"]`), http.StatusOK
		case "order-instance-owner-ok":
			method, path, body, expectedStatus = http.MethodPut, "/api/v1/teams/api-team/pipelines/target/ordering", []byte(`[{}]`), http.StatusOK
		case "list-builds-owner-ok":
			path, expectedStatus = "/api/v1/teams/api-team/pipelines/target/builds", http.StatusOK
		case "list-builds-public-ok":
			if err := pipeline.Expose(); err != nil {
				return err.Error()
			}
			identity, path, expectedStatus = "anonymous", "/api/v1/teams/api-team/pipelines/target/builds", http.StatusOK
		case "list-builds-bounded-ok":
			job, found, err := pipeline.Job("build")
			if err != nil || !found {
				return fail("load bounded-build job found=%t err=%v", found, err)
			}
			builds := make([]db.Build, 0, 7)
			for range 7 {
				build, err := job.CreateBuild("brine-bounded")
				if err != nil {
					return err.Error()
				}
				builds = append(builds, build)
			}
			verifyBounded = []int{builds[1].ID(), builds[2].ID(), builds[3].ID()}
			path = fmt.Sprintf("/api/v1/teams/api-team/pipelines/target/builds?from=%d&to=%d&limit=3", builds[1].ID(), builds[5].ID())
			expectedStatus = http.StatusOK
		case "create-build-owner-created":
			method, path, body, expectedStatus = http.MethodPost, "/api/v1/teams/api-team/pipelines/target/builds", []byte(apiPipelinesNextPlanJSON), http.StatusCreated
		case "order-instance-missing-bad-request":
			method, path, body, expectedStatus = http.MethodPut, "/api/v1/teams/api-team/pipelines/missing/ordering", []byte(`[{}]`), http.StatusBadRequest
		case "rename-missing-not-found":
			method, path, body, expectedStatus = http.MethodPut, "/api/v1/teams/api-team/pipelines/missing/rename", []byte(`{"name":"renamed"}`), http.StatusNotFound
		case "rename-empty-bad-request":
			method, path, body, expectedStatus = http.MethodPut, "/api/v1/teams/api-team/pipelines/target/rename", []byte(`{"name":""}`), http.StatusBadRequest
		case "rename-success-state":
			method, path, body, expectedStatus, verifyRename = http.MethodPut, "/api/v1/teams/api-team/pipelines/target/rename", []byte(`{"name":"renamed"}`), http.StatusOK, "renamed"
		case "rename-invalid-warning":
			method, path, body, verifyRename = http.MethodPut, "/api/v1/teams/api-team/pipelines/target/rename", []byte(`{"name":"_renamed"}`), "_renamed"
		case "badge-owner-headers":
			path = "/api/v1/teams/api-team/pipelines/target/badge"
		case "badge-unknown-body":
			path, verifyBadge = "/api/v1/teams/api-team/pipelines/target/badge", "unknown"
		case "badge-success-body", "badge-aborted-body", "badge-errored-body", "badge-failed-body":
			path = "/api/v1/teams/api-team/pipelines/target/badge"
			verifyBadge = strings.TrimSuffix(strings.TrimPrefix(profile, "badge-"), "-body")
			status := map[string]db.BuildStatus{"success": db.BuildStatusSucceeded, "aborted": db.BuildStatusAborted, "errored": db.BuildStatusErrored, "failed": db.BuildStatusFailed}[verifyBadge]
			job, found, err := pipeline.Job("build")
			if err != nil || !found {
				return fail("load badge job found=%t err=%v", found, err)
			}
			build, err := job.CreateBuild("brine-badge")
			if err != nil {
				return err.Error()
			}
			started, err := build.Start(atc.Plan{})
			if err != nil || !started {
				return fail("start badge build started=%t err=%v", started, err)
			}
			if err := build.Finish(status); err != nil {
				return err.Error()
			}
		default:
			return fail("unknown profile %q", profile)
		}
	}

	beforeBuilds, _, err := pipeline.Builds(db.Page{Limit: 100})
	if err != nil {
		return err.Error()
	}
	request, err := http.NewRequest(method, boundary.URL+path, bytes.NewReader(body))
	if err != nil {
		return err.Error()
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	client := boundary.Client
	if identity == "anonymous" {
		client = &http.Client{Timeout: 30 * time.Second}
	} else if identity == "outsider" {
		authorization, err := persistPipelineNextToken(database, "pipelines-next-outsider", "outsider-connector", "outsider-user")
		if err != nil {
			return err.Error()
		}
		request.Header.Set("Authorization", authorization)
		client = &http.Client{Timeout: 30 * time.Second}
	}
	response, err := client.Do(request)
	if err != nil {
		return err.Error()
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return err.Error()
	}

	if expectedStatus != 0 && response.StatusCode != expectedStatus {
		return fail("status=%d want=%d body=%q", response.StatusCode, expectedStatus, responseBody)
	}
	if expectedContentType != "" && response.Header.Get("Content-Type") != expectedContentType {
		return fail("content-type=%q want=%q", response.Header.Get("Content-Type"), expectedContentType)
	}
	if profile == "badge-owner-headers" {
		want := map[string]string{"Content-Type": "image/svg+xml", "Cache-Control": "no-cache, no-store, must-revalidate", "Expires": "0"}
		for key, value := range want {
			if response.Header.Get(key) != value {
				return fail("%s=%q want=%q", key, response.Header.Get(key), value)
			}
		}
	}
	if verifyBadge != "" {
		badge := map[string]jobserver.Badge{
			"unknown": {Width: 98, FillColor: "#9f9f9f", Status: "unknown", Title: "build"},
			"success": {Width: 88, FillColor: "#44cc11", Status: "passing", Title: "build"},
			"aborted": {Width: 90, FillColor: "#8f4b2d", Status: "aborted", Title: "build"},
			"errored": {Width: 88, FillColor: "#fe7d37", Status: "errored", Title: "build"},
			"failed":  {Width: 80, FillColor: "#e05d44", Status: "failing", Title: "build"},
		}[verifyBadge]
		if string(responseBody) != badge.String() {
			return fail("badge body mismatch for %s", verifyBadge)
		}
	}
	if verifyNoBuild {
		afterBuilds, _, err := pipeline.Builds(db.Page{Limit: 100})
		if err != nil {
			return err.Error()
		}
		if len(beforeBuilds) != 0 || len(afterBuilds) != 0 {
			return fail("unauthorized build count before=%d after=%d", len(beforeBuilds), len(afterBuilds))
		}
	}
	if verifyRename != "" {
		_, oldFound, err := boundary.Team.Pipeline(atc.PipelineRef{Name: "target"})
		if err != nil {
			return err.Error()
		}
		renamed, found, err := boundary.Team.Pipeline(atc.PipelineRef{Name: verifyRename})
		if err != nil || oldFound || !found || renamed.Name() != verifyRename {
			return fail("rename old=%t new=%t name=%q err=%v", oldFound, found, renamed.Name(), err)
		}
		var got map[string]any
		if err := json.Unmarshal(responseBody, &got); err != nil {
			return err.Error()
		}
		if verifyRename == "_renamed" {
			want := map[string]any{"warnings": []any{map[string]any{"type": "invalid_identifier", "message": "pipeline: '_renamed' is not a valid identifier: must start with a lowercase letter or a number"}}}
			if !reflect.DeepEqual(got, want) {
				return fail("warning body=%v want=%v", got, want)
			}
		}
	}
	if profile == "rename-empty-bad-request" {
		var got map[string]any
		if err := json.Unmarshal(responseBody, &got); err != nil {
			return err.Error()
		}
		want := map[string]any{"errors": []any{"pipeline: identifier cannot be an empty string"}}
		if !reflect.DeepEqual(got, want) {
			return fail("empty-name body=%v want=%v", got, want)
		}
	}
	if verifyBounded != nil {
		var builds []atc.Build
		if err := json.Unmarshal(responseBody, &builds); err != nil {
			return err.Error()
		}
		got := make([]int, len(builds))
		for index, build := range builds {
			got[index] = build.ID
		}
		if !reflect.DeepEqual(got, verifyBounded) {
			return fail("bounded build ids=%v want=%v", got, verifyBounded)
		}
	}
	return ""
}

const apiPipelinesNextPlanJSON = `{"id":"api-manual","task":{"config":{"run":{"path":"ls"}}}}`

func persistPipelineNextToken(database JetbridgeDB, token, connector, user string) (string, error) {
	payload, err := json.Marshal(map[string]any{
		"sub": token, "aud": []any{pipelineClientAudience}, "exp": time.Now().Add(time.Hour).Unix(),
		"federated_claims": map[string]any{"connector_id": connector, "user_id": user},
	})
	if err != nil {
		return "", err
	}
	var claims db.Claims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", err
	}
	if err := db.NewAccessTokenFactory(database.Conn).CreateAccessToken(token, claims); err != nil {
		return "", err
	}
	return "bearer " + token, nil
}
