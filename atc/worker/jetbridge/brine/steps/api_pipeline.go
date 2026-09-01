package steps

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sort"
	"strings"
	"time"

	"code.cloudfoundry.org/clock"
	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/auth"
	"github.com/concourse/concourse/atc/api/containerserver"
	"github.com/concourse/concourse/atc/auditor"
	"github.com/concourse/concourse/atc/creds"
	"github.com/concourse/concourse/atc/creds/noop"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/util"
	"github.com/concourse/concourse/atc/wrappa"
)

// PipelineAPI is a real HTTP router over real API handlers and PostgreSQL.
// The only adapter is authentication: it builds the production accessor for a
// fixed authenticated user. No database object is decorated and no call is
// recorded; every assertion reads an HTTP response or a freshly loaded row.
type PipelineAPI struct {
	DB     JetbridgeDB
	Team   db.Team
	Server *httptest.Server
	Client *http.Client
	CLIDir string

	Status      int
	ContentType string
	Body        []byte
	ManualPlan  atc.Plan
}

type pipelineAPIAccessFactory struct {
	teams []db.Team
}

func (f pipelineAPIAccessFactory) Create(request *http.Request, role string) (accessor.Access, error) {
	sub := "brine-user"
	if request.Header.Get("Authorization") == "Bearer brine-system" {
		sub = "brine-system"
	}
	return accessor.NewAccessor(
		accessor.Verification{
			HasToken:     true,
			IsTokenValid: true,
			RawClaims: map[string]any{
				"sub":                sub,
				"name":               "Brine User",
				"preferred_username": "brine-user",
			},
		},
		role, "sub", []string{"brine-system"}, f.teams, pipelineAPIDisplayUser{},
	), nil
}

type pipelineAPIDisplayUser struct{}

func (pipelineAPIDisplayUser) DisplayUserId(_, _, _, preferredUsername, _ string) string {
	return preferredUsername
}

func newPipelineAPI(database JetbridgeDB, rec *brine.Recorder) (*PipelineAPI, error) {
	logger := lagertest.NewTestLogger("brine-pipeline-api")
	team, err := database.TeamFactory.CreateTeam(atc.Team{
		Name: "api-team",
		Auth: atc.TeamAuth{
			accessor.OwnerRole: {},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create API team: %w", err)
	}
	if _, err := database.Conn.Exec(`UPDATE teams SET admin = TRUE WHERE id = $1`, team.ID()); err != nil {
		return nil, fmt.Errorf("make API team admin: %w", err)
	}
	team, found, err := database.TeamFactory.FindTeam("api-team")
	if err != nil {
		return nil, fmt.Errorf("reload API team: %w", err)
	}
	if !found {
		return nil, fmt.Errorf("API team disappeared after admin update")
	}

	checkBuilds := make(chan db.Build, 64)
	checkFactory := db.NewCheckFactory(
		database.Conn, database.LockFactory, checkBuilds, util.NewSequenceGenerator(1),
	)
	dbClock := db.NewClock()
	varPool := creds.NewVarSourcePool(
		logger.Session("vars"), creds.CredentialManagementConfig{},
		5*time.Minute, time.Minute, clock.NewClock(),
	)
	rec.RegisterDisposer(varPool.Close)

	cliDir, err := os.MkdirTemp("", "brine-api-cli-*")
	if err != nil {
		return nil, fmt.Errorf("create CLI directory: %w", err)
	}
	rec.RegisterDisposer(func() { _ = os.RemoveAll(cliDir) })

	sink := lager.NewReconfigurableSink(lager.NewPrettySink(io.Discard, lager.DEBUG), lager.DEBUG)
	aud := auditor.NewAuditor(false, false, false, false, false, false, false, false, false, logger)
	buildFactory := db.NewBuildFactory(database.Conn, database.LockFactory, time.Minute, time.Minute)
	apiWrapper := wrappa.MultiWrappa{
		wrappa.NewAPIAuthWrappa(
			auth.NewCheckPipelineAccessHandlerFactory(database.TeamFactory),
			auth.NewCheckBuildReadAccessHandlerFactory(buildFactory),
			auth.NewCheckBuildWriteAccessHandlerFactory(buildFactory),
			auth.NewCheckWorkerTeamAccessHandlerFactory(database.WorkerFactory),
		),
		wrappa.NewAccessorWrappa(
			logger,
			pipelineAPIAccessFactory{teams: []db.Team{team}},
			aud,
			map[string]string{},
		),
	}
	handler, err := api.NewHandler(
		logger, "https://example.invalid", "", "Brine",
		apiWrapper,
		database.TeamFactory,
		db.NewPipelineFactory(database.Conn, database.LockFactory),
		db.NewJobFactory(database.Conn, database.LockFactory),
		db.NewResourceFactory(database.Conn, database.LockFactory),
		database.WorkerFactory,
		database.TeamFactory,
		database.VolumeRepository,
		buildFactory,
		checkFactory,
		db.NewResourceConfigFactory(database.Conn, database.LockFactory),
		db.NewUserFactory(database.Conn),
		nil,
		nil,
		sink,
		false,
		cliDir,
		"brine", "brine-worker", "brine-jetbridge", "brine-concourse",
		noop.Noop{}, varPool, creds.Managers{},
		containerserver.NewInterceptTimeoutFactory(time.Hour), time.Second,
		db.NewWall(database.Conn, &dbClock), clock.NewClock(),
		db.NewSigningKeyFactory(database.Conn), database.Conn,
	)
	if err != nil {
		return nil, fmt.Errorf("build API handler: %w", err)
	}

	server := httptest.NewServer(handler)
	rec.RegisterDisposer(server.Close)
	return &PipelineAPI{DB: database, Team: team, Server: server, Client: server.Client(), CLIDir: cliDir}, nil
}

func (w *PipelineAPI) save(name string) error {
	_, _, err := w.Team.SavePipeline(
		atc.PipelineRef{Name: name},
		atc.Config{
			Jobs: atc.JobConfigs{{Name: "build"}},
			Groups: atc.GroupConfigs{{
				Name: "all", Jobs: []string{"build"},
			}},
			Display: &atc.DisplayConfig{BackgroundImage: "brine-background.jpg"},
		},
		0, false,
	)
	if err != nil {
		return fmt.Errorf("save pipeline %q: %w", name, err)
	}
	return nil
}

func (w *PipelineAPI) pipeline(name string) (db.Pipeline, error) {
	pipeline, found, err := w.Team.Pipeline(atc.PipelineRef{Name: name})
	if err != nil {
		return nil, fmt.Errorf("load pipeline %q: %w", name, err)
	}
	if !found {
		return nil, fmt.Errorf("pipeline %q does not exist", name)
	}
	return pipeline, nil
}

func (w *PipelineAPI) request(method, path string, body []byte) error {
	req, err := http.NewRequestWithContext(
		context.Background(), method, w.Server.URL+path, bytes.NewReader(body),
	)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := w.Client.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	w.Body, err = io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read %s %s response: %w", method, path, err)
	}
	w.Status = resp.StatusCode
	w.ContentType = resp.Header.Get("Content-Type")
	return nil
}

func PipelineAPIDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, *PipelineAPI](
			"the real pipeline API and PostgreSQL",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, _ brine.Params, rec *brine.Recorder, res brine.Resources) (*PipelineAPI, error) {
				database, ok := res.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return nil, fmt.Errorf("jetbridge-db resource is %T", res.Get("jetbridge-db"))
				}
				return newPipelineAPI(database, rec)
			},
		),

		brine.DefineMap[*PipelineAPI, *PipelineAPI](
			"the team has the pipelines {string}",
			func(in *PipelineAPI, p brine.Params, _ *brine.Recorder) (*PipelineAPI, error) {
				names, ok := p.GetString(0)
				if !ok {
					return in, fmt.Errorf("expected comma-separated pipeline names")
				}
				for _, name := range strings.Split(names, ",") {
					if err := in.save(strings.TrimSpace(name)); err != nil {
						return in, err
					}
				}
				return in, nil
			},
		),

		brine.DefineMap[*PipelineAPI, *PipelineAPI](
			"pipeline {string} is exposed in PostgreSQL",
			func(in *PipelineAPI, p brine.Params, _ *brine.Recorder) (*PipelineAPI, error) {
				name, err := paramAt("pipeline {string} is exposed in PostgreSQL", p, 0)
				if err != nil {
					return in, err
				}
				pipeline, err := in.pipeline(name)
				if err != nil {
					return in, err
				}
				return in, pipeline.Expose()
			},
		),

		brine.DefineMap[*PipelineAPI, *PipelineAPI](
			"pipeline {string} is paused by {string} in PostgreSQL",
			func(in *PipelineAPI, p brine.Params, _ *brine.Recorder) (*PipelineAPI, error) {
				name, pausedBy, err := twoParams("pipeline {string} is paused by {string} in PostgreSQL", p)
				if err != nil {
					return in, err
				}
				pipeline, err := in.pipeline(name)
				if err != nil {
					return in, err
				}
				return in, pipeline.Pause(pausedBy)
			},
		),

		apiRequestStep("the API lists the team's pipelines", http.MethodGet,
			func(*PipelineAPI, brine.Params) (string, []byte, error) {
				return "/api/v1/teams/api-team/pipelines", nil, nil
			}),
		apiRequestStep("the API reads pipeline {string}", http.MethodGet,
			pipelinePath("")),
		apiRequestStep("the API pauses pipeline {string}", http.MethodPut,
			pipelinePath("/pause")),
		apiRequestStep("the API unpauses pipeline {string}", http.MethodPut,
			pipelinePath("/unpause")),
		apiRequestStep("the API exposes pipeline {string}", http.MethodPut,
			pipelinePath("/expose")),
		apiRequestStep("the API hides pipeline {string}", http.MethodPut,
			pipelinePath("/hide")),
		apiRequestStep("the API archives pipeline {string}", http.MethodPut,
			pipelinePath("/archive")),
		apiRequestStep("the API deletes pipeline {string}", http.MethodDelete,
			pipelinePath("")),
		apiRequestStep("the API starts a build for pipeline {string}", http.MethodPost,
			func(in *PipelineAPI, p brine.Params) (string, []byte, error) {
				name, err := paramAt("the API starts a build for pipeline {string}", p, 0)
				if err != nil {
					return "", nil, err
				}
				inPlan := atc.Plan{
					ID: "brine-manual",
					Task: &atc.TaskPlan{Config: &atc.TaskConfig{
						Run: atc.TaskRunConfig{Path: "true"},
					}},
				}
				body, err := json.Marshal(inPlan)
				in.ManualPlan = inPlan
				return "/api/v1/teams/api-team/pipelines/" + name + "/builds", body, err
			}),

		brine.DefineMap[*PipelineAPI, *PipelineAPI](
			"the API renames pipeline {string} to {string}",
			func(in *PipelineAPI, p brine.Params, _ *brine.Recorder) (*PipelineAPI, error) {
				oldName, newName, err := twoParams("the API renames pipeline {string} to {string}", p)
				if err != nil {
					return in, err
				}
				body, _ := json.Marshal(atc.RenameRequest{NewName: newName})
				return in, in.request(http.MethodPut,
					"/api/v1/teams/api-team/pipelines/"+oldName+"/rename", body)
			},
		),

		brine.DefineMap[*PipelineAPI, *PipelineAPI](
			"the API orders the pipelines as {string}",
			func(in *PipelineAPI, p brine.Params, _ *brine.Recorder) (*PipelineAPI, error) {
				raw, ok := p.GetString(0)
				if !ok {
					return in, fmt.Errorf("expected comma-separated pipeline names")
				}
				names := strings.Split(raw, ",")
				for i := range names {
					names[i] = strings.TrimSpace(names[i])
				}
				body, _ := json.Marshal(names)
				return in, in.request(http.MethodPut,
					"/api/v1/teams/api-team/pipelines/ordering", body)
			},
		),

		CheckInt[*PipelineAPI]("the API response status is {int}", "HTTP status",
			func(in *PipelineAPI) (int, error) { return in.Status, nil }),
		CheckContains[*PipelineAPI]("the API response content type contains {string}", "Content-Type",
			func(in *PipelineAPI) (string, error) { return in.ContentType, nil }),

		brine.DefineCheck[*PipelineAPI](
			"the API returned the pipelines {string}",
			func(in *PipelineAPI, p brine.Params, _ *brine.Recorder) error {
				wantRaw, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected comma-separated pipeline names")
				}
				var pipelines []atc.Pipeline
				if err := json.Unmarshal(in.Body, &pipelines); err != nil {
					return fmt.Errorf("decode pipeline response %q: %w", in.Body, err)
				}
				got := make([]string, len(pipelines))
				for i, pipeline := range pipelines {
					got[i] = pipeline.Name
					persisted, err := in.pipeline(pipeline.Name)
					if err != nil {
						return err
					}
					if err := matchesPersistedPipeline(pipeline, persisted); err != nil {
						return err
					}
				}
				want := strings.Split(wantRaw, ",")
				for i := range want {
					want[i] = strings.TrimSpace(want[i])
				}
				sort.Strings(got)
				sort.Strings(want)
				if strings.Join(got, ",") != strings.Join(want, ",") {
					return fmt.Errorf("expected pipelines %v, got %v", want, got)
				}
				return nil
			},
		),

		brine.DefineCheck[*PipelineAPI](
			"the API returned pipeline {string}",
			func(in *PipelineAPI, p brine.Params, _ *brine.Recorder) error {
				name, err := paramAt("the API returned pipeline {string}", p, 0)
				if err != nil {
					return err
				}
				var pipeline atc.Pipeline
				if err := json.Unmarshal(in.Body, &pipeline); err != nil {
					return fmt.Errorf("decode pipeline response %q: %w", in.Body, err)
				}
				persisted, err := in.pipeline(name)
				if err != nil {
					return err
				}
				if err := matchesPersistedPipeline(pipeline, persisted); err != nil {
					return err
				}
				if !pipeline.Public || len(pipeline.Groups) == 0 || pipeline.Display == nil {
					return fmt.Errorf("pipeline response omitted public/config fields: %+v", pipeline)
				}
				return nil
			},
		),

		pipelineStateCheck("pipeline {string} is paused by the API user",
			func(p db.Pipeline) error {
				if !p.Paused() || p.PausedBy() != "brine-user" || p.PausedAt().IsZero() {
					return fmt.Errorf("expected paused by brine-user, got paused=%t by=%q", p.Paused(), p.PausedBy())
				}
				return nil
			}),
		pipelineStateCheck("pipeline {string} is unpaused",
			func(p db.Pipeline) error {
				if p.Paused() {
					return fmt.Errorf("pipeline is still paused by %q", p.PausedBy())
				}
				return nil
			}),
		pipelineStateCheck("pipeline {string} is public",
			func(p db.Pipeline) error {
				if !p.Public() {
					return fmt.Errorf("pipeline is still private")
				}
				return nil
			}),
		pipelineStateCheck("pipeline {string} is private",
			func(p db.Pipeline) error {
				if p.Public() {
					return fmt.Errorf("pipeline is still public")
				}
				return nil
			}),
		pipelineStateCheck("pipeline {string} is archived",
			func(p db.Pipeline) error {
				if !p.Archived() || !p.Paused() || p.PausedBy() != "automatic-pipeline-archiver" || p.PausedAt().IsZero() || p.LastUpdated().IsZero() {
					return fmt.Errorf("archive state is incomplete: archived=%t paused=%t by=%q", p.Archived(), p.Paused(), p.PausedBy())
				}
				if len(p.Groups()) == 0 || p.Display() == nil {
					return fmt.Errorf("archive discarded groups or display config")
				}
				return nil
			}),

		brine.DefineCheck[*PipelineAPI](
			"pipeline {string} no longer exists",
			func(in *PipelineAPI, p brine.Params, _ *brine.Recorder) error {
				name, err := paramAt("pipeline {string} no longer exists", p, 0)
				if err != nil {
					return err
				}
				_, found, err := in.Team.Pipeline(atc.PipelineRef{Name: name})
				if err != nil {
					return err
				}
				if found {
					return fmt.Errorf("pipeline %q still exists", name)
				}
				return nil
			},
		),

		brine.DefineCheck[*PipelineAPI](
			"pipeline {string} exists",
			func(in *PipelineAPI, p brine.Params, _ *brine.Recorder) error {
				name, err := paramAt("pipeline {string} exists", p, 0)
				if err != nil {
					return err
				}
				_, err = in.pipeline(name)
				return err
			},
		),

		brine.DefineCheck[*PipelineAPI](
			"the persisted pipeline order is {string}",
			func(in *PipelineAPI, p brine.Params, _ *brine.Recorder) error {
				wantRaw, err := paramAt("the persisted pipeline order is {string}", p, 0)
				if err != nil {
					return err
				}
				pipelines, err := in.Team.Pipelines()
				if err != nil {
					return err
				}
				got := make([]string, len(pipelines))
				for i, pipeline := range pipelines {
					got[i] = pipeline.Name()
				}
				want := strings.Split(wantRaw, ",")
				for i := range want {
					want[i] = strings.TrimSpace(want[i])
				}
				if strings.Join(got, ",") != strings.Join(want, ",") {
					return fmt.Errorf("expected order %v, got %v", want, got)
				}
				return nil
			},
		),

		brine.DefineCheck[*PipelineAPI](
			"pipeline {string} has one started build",
			func(in *PipelineAPI, p brine.Params, _ *brine.Recorder) error {
				name, err := paramAt("pipeline {string} has one started build", p, 0)
				if err != nil {
					return err
				}
				pipeline, err := in.pipeline(name)
				if err != nil {
					return err
				}
				builds, _, err := pipeline.Builds(db.Page{Limit: 100})
				if err != nil {
					return err
				}
				if len(builds) != 1 || builds[0].Status() != db.BuildStatusStarted {
					return fmt.Errorf("expected one started build, got %d builds", len(builds))
				}
				if !jsonValuesEqual(builds[0].PublicPlan(), in.ManualPlan.Public()) {
					return fmt.Errorf("persisted build plan differs from the submitted plan")
				}
				return nil
			},
		),

		brine.DefineCheck[*PipelineAPI](
			"the API returned a started build for pipeline {string}",
			func(in *PipelineAPI, p brine.Params, _ *brine.Recorder) error {
				name, err := paramAt("the API returned a started build for pipeline {string}", p, 0)
				if err != nil {
					return err
				}
				var build atc.Build
				if err := json.Unmarshal(in.Body, &build); err != nil {
					return fmt.Errorf("decode build response %q: %w", in.Body, err)
				}
				pipeline, err := in.pipeline(name)
				if err != nil {
					return err
				}
				builds, _, err := pipeline.Builds(db.Page{Limit: 1})
				if err != nil || len(builds) != 1 {
					return fmt.Errorf("reload persisted build: count=%d error=%v", len(builds), err)
				}
				persisted := builds[0]
				if build.ID != persisted.ID() || build.Name != persisted.Name() ||
					build.PipelineName != name || build.TeamName != persisted.TeamName() ||
					build.Status != atc.StatusStarted || build.APIURL != fmt.Sprintf("/api/v1/builds/%d", persisted.ID()) ||
					build.StartTime != persisted.StartTime().Unix() {
					return fmt.Errorf("unexpected build response: %+v", build)
				}
				return nil
			},
		),
	}
}

type apiPath func(*PipelineAPI, brine.Params) (string, []byte, error)

func apiRequestStep(pattern, method string, path apiPath) brine.StepDefinition {
	return brine.DefineMap[*PipelineAPI, *PipelineAPI](pattern,
		func(in *PipelineAPI, p brine.Params, _ *brine.Recorder) (*PipelineAPI, error) {
			requestPath, body, err := path(in, p)
			if err != nil {
				return in, err
			}
			return in, in.request(method, requestPath, body)
		})
}

func pipelinePath(suffix string) apiPath {
	return func(_ *PipelineAPI, p brine.Params) (string, []byte, error) {
		name, ok := p.GetString(0)
		if !ok {
			return "", nil, fmt.Errorf("expected a pipeline name")
		}
		return "/api/v1/teams/api-team/pipelines/" + name + suffix, nil, nil
	}
}

func pipelineStateCheck(pattern string, check func(db.Pipeline) error) brine.StepDefinition {
	return brine.DefineCheck[*PipelineAPI](pattern,
		func(in *PipelineAPI, p brine.Params, _ *brine.Recorder) error {
			name, ok := p.GetString(0)
			if !ok {
				return fmt.Errorf("expected a pipeline name")
			}
			pipeline, err := in.pipeline(name)
			if err != nil {
				return err
			}
			if ok, err := pipeline.Reload(); err != nil {
				return err
			} else if !ok {
				return fmt.Errorf("pipeline %q disappeared while reloading", name)
			}
			return check(pipeline)
		})
}

func matchesPersistedPipeline(actual atc.Pipeline, persisted db.Pipeline) error {
	pausedAt := int64(0)
	if !persisted.PausedAt().IsZero() {
		pausedAt = persisted.PausedAt().Unix()
	}
	if actual.ID != persisted.ID() || actual.Name != persisted.Name() ||
		actual.TeamName != persisted.TeamName() || actual.Paused != persisted.Paused() ||
		actual.PausedBy != persisted.PausedBy() || actual.PausedAt != pausedAt ||
		actual.Public != persisted.Public() || actual.Archived != persisted.Archived() ||
		!reflect.DeepEqual(actual.Groups, persisted.Groups()) ||
		!reflect.DeepEqual(actual.Display, persisted.Display()) {
		return fmt.Errorf("API pipeline does not match PostgreSQL: actual=%+v persisted=%s/%s", actual, persisted.TeamName(), persisted.Name())
	}
	return nil
}

func jsonValuesEqual(left, right *json.RawMessage) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	var leftValue, rightValue any
	if json.Unmarshal(*left, &leftValue) != nil || json.Unmarshal(*right, &rightValue) != nil {
		return false
	}
	return reflect.DeepEqual(leftValue, rightValue)
}
