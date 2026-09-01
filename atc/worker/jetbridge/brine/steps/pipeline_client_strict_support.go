package steps

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/auth"
	"github.com/concourse/concourse/atc/api/pipelineserver"
	"github.com/concourse/concourse/atc/api/present"
	"github.com/concourse/concourse/atc/auditor"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/wrappa"
	clientapi "github.com/concourse/concourse/go-concourse/concourse"
	"github.com/concourse/concourse/skymarshal/skycmd"
	"github.com/tedsuo/rata"
	"golang.org/x/oauth2"
)

const (
	pipelineClientAudience  = "brine-pipeline-client"
	pipelineClientConnector = "brine-pipeline-connector"
	pipelineClientUserID    = "brine-pipeline-user"
	pipelineClientSubject   = "brine-pipeline-subject"
	pipelineClientTeamName  = "api-team"
)

type strictPipelineClientAPI struct {
	DB       JetbridgeDB
	Team     db.Team
	URL      string
	Client   *http.Client
	Saved    map[string]db.Pipeline
	Builds   []db.Build
	Response *http.Response
	Body     []byte
}

func newStrictPipelineClientAPI(database JetbridgeDB, rec *brine.Recorder) (*strictPipelineClientAPI, error) {
	logger := lager.NewLogger("brine-pipeline-client")
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: pipelineClientTeamName})
	if err != nil {
		return nil, fmt.Errorf("create pipeline client team: %w", err)
	}
	if err := team.UpdateProviderAuth(atc.TeamAuth{
		accessor.OwnerRole: {"users": {pipelineClientConnector + ":" + pipelineClientUserID}},
	}); err != nil {
		return nil, fmt.Errorf("grant pipeline client owner role: %w", err)
	}

	token := "brine-pipeline-client-token"
	payload, err := json.Marshal(map[string]any{
		"sub":                pipelineClientSubject,
		"aud":                []any{pipelineClientAudience},
		"exp":                time.Now().Add(time.Hour).Unix(),
		"name":               "Brine Pipeline User",
		"preferred_username": pipelineClientUserID,
		"federated_claims": map[string]any{
			"connector_id": pipelineClientConnector,
			"user_id":      pipelineClientUserID,
		},
	})
	if err != nil {
		return nil, err
	}
	var claims db.Claims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, err
	}
	if err := db.NewAccessTokenFactory(database.Conn).CreateAccessToken(token, claims); err != nil {
		return nil, fmt.Errorf("persist pipeline client token: %w", err)
	}

	display, err := skycmd.NewSkyDisplayUserIdGenerator(map[string]string{})
	if err != nil {
		return nil, err
	}
	accessFactory := accessor.NewAccessFactory(
		accessor.NewVerifier(db.NewAccessTokenFactory(database.Conn), []string{pipelineClientAudience}),
		database.TeamFactory,
		"sub",
		[]string{"brine-system"},
		display,
	)
	buildFactory := db.NewBuildFactory(database.Conn, database.LockFactory, time.Minute, time.Minute)
	wrapper := wrappa.MultiWrappa{
		wrappa.NewAPIAuthWrappa(
			auth.NewCheckPipelineAccessHandlerFactory(database.TeamFactory),
			auth.NewCheckBuildReadAccessHandlerFactory(buildFactory),
			auth.NewCheckBuildWriteAccessHandlerFactory(buildFactory),
			auth.NewCheckWorkerTeamAccessHandlerFactory(database.WorkerFactory),
		),
		wrappa.NewAccessorWrappa(
			logger,
			accessFactory,
			auditor.NewAuditor(false, false, false, false, false, true, false, false, false, logger),
			map[string]string{},
		),
	}

	pipelineServer := pipelineserver.NewServer(
		logger,
		database.TeamFactory,
		db.NewPipelineFactory(database.Conn, database.LockFactory),
		"https://concourse.invalid",
	)
	pipelineHandlerFactory := pipelineserver.NewScopedHandlerFactory(database.TeamFactory)
	teamHandlerFactory := api.NewTeamScopedHandlerFactory(logger, database.TeamFactory)
	handlers := rata.Handlers{
		atc.ListAllPipelines:    http.HandlerFunc(pipelineServer.ListAllPipelines),
		atc.ListPipelines:       http.HandlerFunc(pipelineServer.ListPipelines),
		atc.GetPipeline:         pipelineHandlerFactory.HandlerFor(pipelineServer.GetPipeline),
		atc.DeletePipeline:      pipelineHandlerFactory.HandlerFor(pipelineServer.DeletePipeline),
		atc.OrderPipelines:      teamHandlerFactory.HandlerFor(pipelineServer.OrderPipelines),
		atc.PausePipeline:       pipelineHandlerFactory.HandlerFor(pipelineServer.PausePipeline),
		atc.ArchivePipeline:     pipelineHandlerFactory.HandlerFor(pipelineServer.ArchivePipeline),
		atc.UnpausePipeline:     pipelineHandlerFactory.HandlerFor(pipelineServer.UnpausePipeline),
		atc.ExposePipeline:      pipelineHandlerFactory.HandlerFor(pipelineServer.ExposePipeline),
		atc.HidePipeline:        pipelineHandlerFactory.HandlerFor(pipelineServer.HidePipeline),
		atc.RenamePipeline:      teamHandlerFactory.HandlerFor(pipelineServer.RenamePipeline),
		atc.ListPipelineBuilds:  pipelineHandlerFactory.HandlerFor(pipelineServer.ListPipelineBuilds),
		atc.CreatePipelineBuild: pipelineHandlerFactory.HandlerFor(pipelineServer.CreateBuild),
	}
	var routes rata.Routes
	for _, route := range atc.Routes {
		if _, ok := handlers[route.Name]; ok {
			routes = append(routes, route)
		}
	}
	router, err := rata.NewRouter(routes, wrapper.Wrap(handlers))
	if err != nil {
		return nil, fmt.Errorf("build production pipeline client router: %w", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen for production pipeline client API: %w", err)
	}
	server := &http.Server{Handler: router, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = server.Serve(listener) }()
	httpClient := oauth2.NewClient(
		context.Background(),
		oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token, TokenType: "Bearer"}),
	)
	httpClient.Timeout = 30 * time.Second
	rec.RegisterDisposer(func() {
		httpClient.CloseIdleConnections()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			_ = server.Close()
		}
	})

	return &strictPipelineClientAPI{
		DB: database, Team: team, URL: "http://" + listener.Addr().String(), Client: httpClient,
		Saved: map[string]db.Pipeline{},
	}, nil
}

func (api *strictPipelineClientAPI) save(name string) error {
	pipeline, _, err := api.Team.SavePipeline(
		atc.PipelineRef{Name: name},
		atc.Config{
			Jobs:    atc.JobConfigs{{Name: "build"}},
			Groups:  atc.GroupConfigs{{Name: "all", Jobs: []string{"build"}}},
			Display: &atc.DisplayConfig{BackgroundImage: "brine-background.jpg"},
		},
		db.ConfigVersion(0),
		false,
	)
	if err == nil {
		api.Saved[name] = pipeline
	}
	return err
}

func pipelineClientStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[*PipelineClientState, *PipelineClientState](
			"the client instanced pipeline {string} starts {string}",
			func(in *PipelineClientState, p brine.Params, _ *brine.Recorder) (*PipelineClientState, error) {
				name, state, err := twoParams("the client instanced pipeline {string} starts {string}", p)
				if err != nil {
					return in, err
				}
				pipeline := in.API.Saved[name]
				if pipeline == nil {
					return in, fmt.Errorf("instanced pipeline %q was not persisted", name)
				}
				switch state {
				case "plain":
				case "paused":
					if err := pipeline.Pause(pipelineClientUserID); err != nil {
						return in, err
					}
				case "public":
					if err := pipeline.Expose(); err != nil {
						return in, err
					}
				default:
					return in, fmt.Errorf("unknown initial pipeline state %q", state)
				}
				return in, nil
			},
		),
		brine.DefineMap[*PipelineClientState, *PipelineClientState](
			"the client instanced pipeline {string} has two persisted builds",
			func(in *PipelineClientState, p brine.Params, _ *brine.Recorder) (*PipelineClientState, error) {
				name, _ := p.GetString(0)
				pipeline := in.API.Saved[name]
				if pipeline == nil {
					return in, fmt.Errorf("instanced pipeline %q was not persisted", name)
				}
				for i := 0; i < 2; i++ {
					build, err := pipeline.CreateStartedBuild(atc.Plan{ID: atc.PlanID(fmt.Sprintf("persisted-%d", i))})
					if err != nil {
						return in, err
					}
					in.API.Builds = append(in.API.Builds, build)
				}
				return in, nil
			},
		),
		brine.DefineMap[*PipelineClientState, *PipelineClientState](
			"public pipeline {string} exists on client team {string}",
			func(in *PipelineClientState, p brine.Params, _ *brine.Recorder) (*PipelineClientState, error) {
				name, teamName, err := twoParams("public pipeline {string} exists on client team {string}", p)
				if err != nil {
					return in, err
				}
				team, found, err := in.API.DB.TeamFactory.FindTeam(teamName)
				if err != nil {
					return in, err
				}
				if !found {
					team, err = in.API.DB.TeamFactory.CreateTeam(atc.Team{Name: teamName})
					if err != nil {
						return in, err
					}
				}
				pipeline, _, err := team.SavePipeline(atc.PipelineRef{Name: name}, strictPipelineConfig(), 0, false)
				if err != nil {
					return in, err
				}
				if err := pipeline.Expose(); err != nil {
					return in, err
				}
				in.API.Saved[teamName+"/"+name] = pipeline
				return in, nil
			},
		),
		brine.DefineMap[*PipelineClientState, *PipelineClientState](
			"the raw production pipeline API performs {string}",
			func(in *PipelineClientState, p brine.Params, _ *brine.Recorder) (*PipelineClientState, error) {
				profile, _ := p.GetString(0)
				method, path, body, err := strictPipelineAPIRequest(profile)
				if err != nil {
					return in, err
				}
				req, err := http.NewRequest(method, in.API.URL+path, bytes.NewReader(body))
				if err != nil {
					return in, err
				}
				if body != nil {
					req.Header.Set("Content-Type", "application/json")
				}
				resp, err := in.API.Client.Do(req)
				if err != nil {
					return in, err
				}
				defer resp.Body.Close()
				in.API.Body, err = io.ReadAll(resp.Body)
				if err != nil {
					return in, err
				}
				in.API.Response = resp
				return in, nil
			},
		),
		brine.DefineCheck[*PipelineClientState](
			"the client decoded the exact persisted pipeline {string}",
			func(in *PipelineClientState, p brine.Params, _ *brine.Recorder) error {
				name, _ := p.GetString(0)
				pipeline := in.API.Saved[name]
				if pipeline == nil {
					return fmt.Errorf("persisted pipeline %q was not tracked", name)
				}
				if _, err := pipeline.Reload(); err != nil {
					return err
				}
				want := present.Pipeline(pipeline)
				if !reflect.DeepEqual(in.Pipeline, want) {
					return fmt.Errorf("decoded pipeline mismatch: got %#v, want %#v", in.Pipeline, want)
				}
				return nil
			},
		),
		brine.DefineCheck[*PipelineClientState](
			"the client decoded exact persisted pipelines {string}",
			func(in *PipelineClientState, p brine.Params, _ *brine.Recorder) error {
				raw, _ := p.GetString(0)
				names := strings.Split(raw, ",")
				if len(in.Pipelines) != len(names) {
					return fmt.Errorf("decoded %d pipelines, want %d", len(in.Pipelines), len(names))
				}
				got := append([]atc.Pipeline(nil), in.Pipelines...)
				sort.Slice(got, func(i, j int) bool { return got[i].Name < got[j].Name })
				sort.Strings(names)
				for i, name := range names {
					pipeline := in.API.Saved[name]
					if pipeline == nil {
						return fmt.Errorf("persisted pipeline %q was not tracked", name)
					}
					if _, err := pipeline.Reload(); err != nil {
						return err
					}
					want := present.Pipeline(pipeline)
					if !reflect.DeepEqual(got[i], want) {
						return fmt.Errorf("decoded pipeline %q mismatch: got %#v, want %#v", name, got[i], want)
					}
				}
				return nil
			},
		),
		brine.DefineCheck[*PipelineClientState](
			"pipeline {string} persisted state is {string}",
			func(in *PipelineClientState, p brine.Params, _ *brine.Recorder) error {
				name, state, err := twoParams("pipeline {string} persisted state is {string}", p)
				if err != nil {
					return err
				}
				pipeline := in.API.Saved[name]
				if pipeline == nil {
					return fmt.Errorf("pipeline %q was not tracked", name)
				}
				found, err := pipeline.Reload()
				if err != nil {
					return err
				}
				switch state {
				case "paused":
					if !found || !pipeline.Paused() || pipeline.Archived() {
						return fmt.Errorf("pipeline %q is not exclusively paused: found=%t paused=%t archived=%t", name, found, pipeline.Paused(), pipeline.Archived())
					}
				case "unpaused":
					if !found || pipeline.Paused() {
						return fmt.Errorf("pipeline %q is not unpaused", name)
					}
				case "public":
					if !found || !pipeline.Public() {
						return fmt.Errorf("pipeline %q is not public", name)
					}
				case "private":
					if !found || pipeline.Public() {
						return fmt.Errorf("pipeline %q is not private", name)
					}
				case "archived":
					if !found || !pipeline.Archived() {
						return fmt.Errorf("pipeline %q is not archived", name)
					}
				case "deleted":
					if found {
						return fmt.Errorf("pipeline %q still exists", name)
					}
				default:
					return fmt.Errorf("unknown expected pipeline state %q", state)
				}
				return nil
			},
		),
		brine.DefineCheck[*PipelineClientState](
			"the persisted named pipeline order is {string}",
			func(in *PipelineClientState, p brine.Params, _ *brine.Recorder) error {
				want, _ := p.GetString(0)
				pipelines, err := in.API.Team.Pipelines()
				if err != nil {
					return err
				}
				names := make([]string, len(pipelines))
				for i, pipeline := range pipelines {
					names[i] = pipeline.Name()
				}
				if strings.Join(names, ",") != want {
					return fmt.Errorf("persisted order is %q, want %q", strings.Join(names, ","), want)
				}
				return nil
			},
		),
		brine.DefineCheck[*PipelineClientState](
			"pipeline {string} was renamed to {string} in PostgreSQL",
			func(in *PipelineClientState, p brine.Params, _ *brine.Recorder) error {
				oldName, newName, err := twoParams("pipeline {string} was renamed to {string} in PostgreSQL", p)
				if err != nil {
					return err
				}
				_, oldFound, err := in.API.Team.Pipeline(atc.PipelineRef{Name: oldName})
				if err != nil {
					return err
				}
				_, newFound, err := in.API.Team.Pipeline(atc.PipelineRef{Name: newName})
				if err != nil {
					return err
				}
				if oldFound || !newFound {
					return fmt.Errorf("rename state old=%t new=%t", oldFound, newFound)
				}
				return nil
			},
		),
		brine.DefineCheck[*PipelineClientState](
			"the client returned the exact persisted created build",
			func(in *PipelineClientState, _ brine.Params, _ *brine.Recorder) error {
				if in.Build.ID <= 0 {
					return fmt.Errorf("client returned invalid build %#v", in.Build)
				}
				build, found, err := db.NewBuildFactory(in.API.DB.Conn, in.API.DB.LockFactory, time.Minute, time.Minute).Build(in.Build.ID)
				if err != nil {
					return err
				}
				if !found {
					return fmt.Errorf("created build %d was not persisted", in.Build.ID)
				}
				want := present.Build(build, nil, nil)
				if !reflect.DeepEqual(in.Build, want) {
					return fmt.Errorf("created build mismatch: got %#v, want %#v", in.Build, want)
				}
				return nil
			},
		),
		brine.DefineCheck[*PipelineClientState](
			"the client returned the exact two persisted builds",
			func(in *PipelineClientState, _ brine.Params, _ *brine.Recorder) error {
				if len(in.Builds) != 2 {
					return fmt.Errorf("client returned %d builds, want 2", len(in.Builds))
				}
				want, err := exactPipelineBuilds(in.API.Saved["target"])
				if err != nil {
					return err
				}
				if !reflect.DeepEqual(in.Builds, want) {
					return fmt.Errorf("pipeline builds mismatch: got %#v, want %#v", in.Builds, want)
				}
				return nil
			},
		),
		brine.DefineCheck[*PipelineClientState](
			"the client returned nil pipeline-build pagination",
			func(in *PipelineClientState, _ brine.Params, _ *brine.Recorder) error {
				if in.Pagination.Previous != nil || in.Pagination.Next != nil {
					return fmt.Errorf("pagination was %#v, want nil pages", in.Pagination)
				}
				return nil
			},
		),
		CheckInt[*PipelineClientState]("the raw pipeline API status is {int}", "raw pipeline API status", func(in *PipelineClientState) (int, error) {
			if in.API.Response == nil {
				return 0, fmt.Errorf("raw pipeline API was not called")
			}
			return in.API.Response.StatusCode, nil
		}),
		brine.DefineCheck[*PipelineClientState](
			"the raw pipeline API returned both exact public pipelines",
			func(in *PipelineClientState, _ brine.Params, _ *brine.Recorder) error {
				var got []atc.Pipeline
				if err := json.Unmarshal(in.API.Body, &got); err != nil {
					return err
				}
				main := present.Pipeline(in.API.Saved["api-team/public-main"])
				main.Public = true
				other := present.Pipeline(in.API.Saved["other-team/public-other"])
				other.Public = true
				want := []atc.Pipeline{main, other}
				sort.Slice(got, func(i, j int) bool { return got[i].Name < got[j].Name })
				sort.Slice(want, func(i, j int) bool { return want[i].Name < want[j].Name })
				if !reflect.DeepEqual(got, want) {
					return fmt.Errorf("global pipeline body mismatch: got %#v, want %#v", got, want)
				}
				return nil
			},
		),
		brine.DefineCheck[*PipelineClientState](
			"the raw pipeline API returned the exact missing-order error",
			func(in *PipelineClientState, _ brine.Params, _ *brine.Recorder) error {
				if string(in.API.Body) != "pipeline 'missing' not found\n" {
					return fmt.Errorf("missing-order body is %q", string(in.API.Body))
				}
				return nil
			},
		),
		brine.DefineCheck[*PipelineClientState](
			"the raw pipeline API returned the exact persisted builds",
			func(in *PipelineClientState, _ brine.Params, _ *brine.Recorder) error {
				var got []atc.Build
				if err := json.Unmarshal(in.API.Body, &got); err != nil {
					return err
				}
				want, err := exactPipelineBuilds(in.API.Saved["target"])
				if err != nil {
					return err
				}
				if !reflect.DeepEqual(got, want) {
					return fmt.Errorf("raw build body mismatch: got %#v, want %#v", got, want)
				}
				return nil
			},
		),
		brine.DefineCheck[*PipelineClientState](
			"the raw pipeline API returned the exact created build",
			func(in *PipelineClientState, _ brine.Params, _ *brine.Recorder) error {
				var got atc.Build
				if err := json.Unmarshal(in.API.Body, &got); err != nil {
					return err
				}
				build, found, err := db.NewBuildFactory(in.API.DB.Conn, in.API.DB.LockFactory, time.Minute, time.Minute).Build(got.ID)
				if err != nil {
					return err
				}
				if !found {
					return fmt.Errorf("raw API build %d was not persisted", got.ID)
				}
				want := present.Build(build, nil, nil)
				if !reflect.DeepEqual(got, want) {
					return fmt.Errorf("raw created build mismatch: got %#v, want %#v", got, want)
				}
				return nil
			},
		),
	}
}

func strictPipelineConfig() atc.Config {
	return atc.Config{
		Jobs:    atc.JobConfigs{{Name: "build"}},
		Groups:  atc.GroupConfigs{{Name: "all", Jobs: []string{"build"}}},
		Display: &atc.DisplayConfig{BackgroundImage: "brine-background.jpg"},
	}
}

func strictPipelineAPIRequest(profile string) (string, string, []byte, error) {
	query := clientPipelineRef("target").QueryParams().Encode()
	switch profile {
	case "lists all pipelines":
		return http.MethodGet, "/api/v1/pipelines", nil, nil
	case "orders a missing pipeline":
		body, err := json.Marshal([]string{"alpha", "missing"})
		return http.MethodPut, "/api/v1/teams/" + pipelineClientTeamName + "/pipelines/ordering", body, err
	case "lists pipeline builds":
		return http.MethodGet, "/api/v1/teams/" + pipelineClientTeamName + "/pipelines/target/builds?" + query, nil, nil
	case "creates a pipeline build":
		body, err := json.Marshal(atc.Plan{ID: "raw-client-plan", Task: &atc.TaskPlan{Config: &atc.TaskConfig{Run: atc.TaskRunConfig{Path: "true"}}}})
		return http.MethodPost, "/api/v1/teams/" + pipelineClientTeamName + "/pipelines/target/builds?" + query, body, err
	default:
		return "", "", nil, fmt.Errorf("unknown raw pipeline API profile %q", profile)
	}
}

func exactPipelineBuilds(pipeline db.Pipeline) ([]atc.Build, error) {
	if pipeline == nil {
		return nil, fmt.Errorf("target pipeline was not persisted")
	}
	builds, _, err := pipeline.Builds(db.Page{Limit: atc.PaginationAPIDefaultLimit})
	if err != nil {
		return nil, err
	}
	want := make([]atc.Build, len(builds))
	for i, build := range builds {
		want[i] = present.Build(build, nil, nil)
	}
	return want, nil
}

var _ clientapi.Pagination
