package steps

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"reflect"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/auth"
	"github.com/concourse/concourse/atc/api/pipelineserver"
	"github.com/concourse/concourse/atc/auditor"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/wrappa"
	"github.com/tedsuo/rata"
)

type strictPipelineStateObservation struct {
	Profile string
	Failure string
}

func PipelineAPIStateStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, strictPipelineStateObservation](
			"the production pipeline state API behavior {string} is exercised",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (strictPipelineStateObservation, error) {
				profile, err := paramAt("the production pipeline state API behavior {string} is exercised", p, 0)
				if err != nil {
					return strictPipelineStateObservation{}, err
				}
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return strictPipelineStateObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				return strictPipelineStateObservation{Profile: profile, Failure: observeStrictPipelineStateAPI(database, profile)}, nil
			},
		),
		brine.DefineCheck[strictPipelineStateObservation](
			"the pipeline state API behavior exactly matches {string}",
			func(in strictPipelineStateObservation, p brine.Params, _ *brine.Recorder) error {
				profile, err := paramAt("the pipeline state API behavior exactly matches {string}", p, 0)
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

func observeStrictPipelineStateAPI(database JetbridgeDB, profile string) string {
	fail := func(format string, args ...any) string { return fmt.Sprintf(format, args...) }
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "strict-pipeline-state"})
	if err != nil {
		return err.Error()
	}
	if err := grantAPIAuthRole(team, accessor.OwnerRole); err != nil {
		return err.Error()
	}
	authorization, err := persistAPIAuthToken(database, "strict-pipeline-state-"+profile, brineAuthSubject, time.Now().Add(time.Hour))
	if err != nil {
		return err.Error()
	}

	config := atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}}
	ref := atc.PipelineRef{Name: "a-pipeline"}
	var pipeline db.Pipeline
	var action string
	var body any
	requestedAt := time.Now()

	switch profile {
	case "delete", "pause", "unpause", "expose", "hide":
		pipeline, _, err = team.SavePipeline(ref, config, db.ConfigVersion(0), false)
		if err != nil {
			return err.Error()
		}
		switch profile {
		case "delete":
			action = atc.DeletePipeline
		case "pause":
			action = atc.PausePipeline
		case "unpause":
			if err := pipeline.Pause("fixture"); err != nil {
				return err.Error()
			}
			action = atc.UnpausePipeline
		case "expose":
			action = atc.ExposePipeline
		case "hide":
			if err := pipeline.Expose(); err != nil {
				return err.Error()
			}
			action = atc.HidePipeline
		}
	case "archive":
		config = atc.Config{
			Groups: atc.GroupConfigs{{Name: "release", Jobs: []string{"ship"}, Resources: []string{"artifact"}}},
			Jobs:   atc.JobConfigs{{Name: "ship"}},
			Resources: atc.ResourceConfigs{{
				Name: "artifact", Type: "registry-image", Source: atc.Source{"repository": "example.invalid/archive"},
			}},
			Display: &atc.DisplayConfig{BackgroundImage: "archive.jpg"},
		}
		pipeline, _, err = team.SavePipeline(ref, config, db.ConfigVersion(0), false)
		if err != nil {
			return err.Error()
		}
		requestedAt = time.Now()
		action = atc.ArchivePipeline
	case "order-global":
		initial := []string{"just-kidding", "a-pipeline", "one-final-pipeline", "yet-another-pipeline", "another-pipeline"}
		for _, name := range initial {
			if _, _, err := team.SavePipeline(atc.PipelineRef{Name: name}, config, 0, false); err != nil {
				return err.Error()
			}
		}
		if got, err := strictPipelineNames(team); err != nil || !reflect.DeepEqual(got, initial) {
			return fail("initial global order=%v err=%v", got, err)
		}
		action = atc.OrderPipelines
		body = []string{"a-pipeline", "another-pipeline", "yet-another-pipeline", "one-final-pipeline", "just-kidding"}
	case "order-instance":
		initial := []atc.InstanceVars{{"branch": "test-2"}, nil, {"branch": "test"}}
		for _, vars := range initial {
			if _, _, err := team.SavePipeline(atc.PipelineRef{Name: ref.Name, InstanceVars: vars}, config, 0, false); err != nil {
				return err.Error()
			}
		}
		got, err := strictPipelineInstanceVars(team, ref.Name)
		wantInitial := []atc.InstanceVars{{"branch": "test-2"}, {}, {"branch": "test"}}
		if err != nil || !reflect.DeepEqual(got, wantInitial) {
			return fail("initial instance order=%v err=%v", got, err)
		}
		action = atc.OrderPipelinesWithinGroup
		body = []atc.InstanceVars{{"branch": "test"}, {}, {"branch": "test-2"}}
	default:
		return fmt.Sprintf("unknown profile %q", profile)
	}

	params := rata.Params{"team_name": team.Name()}
	if action != atc.OrderPipelines {
		params["pipeline_name"] = ref.Name
	}
	status, err := runStrictPipelineStateRequest(database, action, params, body, authorization)
	if err != nil {
		return err.Error()
	}
	if status != http.StatusOK && !(profile == "delete" && status == http.StatusNoContent) {
		return fail("status=%d", status)
	}

	switch profile {
	case "delete":
		got, found, err := team.Pipeline(ref)
		if err != nil || found || got != nil {
			return fail("deleted pipeline found=%t value=%v err=%v", found, got, err)
		}
	case "pause":
		found, err := pipeline.Reload()
		if err != nil || !found || !pipeline.Paused() || pipeline.PausedBy() != brineAuthUserID || pipeline.PausedAt().IsZero() {
			return fail("paused found=%t paused=%t by=%q at=%v err=%v", found, pipeline.Paused(), pipeline.PausedBy(), pipeline.PausedAt(), err)
		}
	case "archive":
		found, err := pipeline.Reload()
		if err != nil || !found || !pipeline.Archived() || !pipeline.Paused() || pipeline.PausedBy() != "automatic-pipeline-archiver" || pipeline.PausedAt().Before(requestedAt) || pipeline.LastUpdated().Before(requestedAt) || !reflect.DeepEqual(pipeline.Groups(), config.Groups) || !reflect.DeepEqual(pipeline.Display(), config.Display) {
			return fail("archive state found=%t archived=%t paused=%t by=%q at=%v updated=%v groups=%v display=%v err=%v", found, pipeline.Archived(), pipeline.Paused(), pipeline.PausedBy(), pipeline.PausedAt(), pipeline.LastUpdated(), pipeline.Groups(), pipeline.Display(), err)
		}
		jobs, err := pipeline.Jobs()
		if err != nil || len(jobs) != 1 {
			return fail("archive jobs=%d err=%v", len(jobs), err)
		}
		job, jobFound, err := pipeline.Job("ship")
		if err != nil || !jobFound || job.Name() != "ship" {
			return fail("archive job found=%t value=%v err=%v", jobFound, job, err)
		}
		resources, err := pipeline.Resources()
		if err != nil || len(resources) != 1 {
			return fail("archive resources=%d err=%v", len(resources), err)
		}
		resource, resourceFound, err := pipeline.Resource("artifact")
		if err != nil || !resourceFound || resource.Name() != "artifact" {
			return fail("archive resource found=%t value=%v err=%v", resourceFound, resource, err)
		}
	case "unpause":
		found, err := pipeline.Reload()
		if err != nil || !found || pipeline.Paused() || pipeline.PausedBy() != "" || !pipeline.PausedAt().IsZero() {
			return fail("unpaused found=%t paused=%t by=%q at=%v err=%v", found, pipeline.Paused(), pipeline.PausedBy(), pipeline.PausedAt(), err)
		}
	case "expose":
		found, err := pipeline.Reload()
		if err != nil || !found || !pipeline.Public() {
			return fail("exposed found=%t public=%t err=%v", found, pipeline.Public(), err)
		}
	case "hide":
		found, err := pipeline.Reload()
		if err != nil || !found || pipeline.Public() {
			return fail("hidden found=%t public=%t err=%v", found, pipeline.Public(), err)
		}
	case "order-global":
		want := body.([]string)
		got, err := strictPipelineNames(team)
		if err != nil || !reflect.DeepEqual(got, want) {
			return fail("global order=%v want=%v err=%v", got, want, err)
		}
	case "order-instance":
		want := body.([]atc.InstanceVars)
		got, err := strictPipelineInstanceVars(team, ref.Name)
		if err != nil || !reflect.DeepEqual(got, want) {
			return fail("instance order=%v want=%v err=%v", got, want, err)
		}
	}
	return ""
}

func strictPipelineNames(team db.Team) ([]string, error) {
	pipelines, err := team.Pipelines()
	if err != nil {
		return nil, err
	}
	names := make([]string, len(pipelines))
	for i, pipeline := range pipelines {
		names[i] = pipeline.Name()
	}
	return names, nil
}

func strictPipelineInstanceVars(team db.Team, name string) ([]atc.InstanceVars, error) {
	pipelines, err := team.Pipelines()
	if err != nil {
		return nil, err
	}
	vars := []atc.InstanceVars{}
	for _, pipeline := range pipelines {
		if pipeline.Name() == name {
			instanceVars := pipeline.InstanceVars()
			if instanceVars == nil {
				instanceVars = atc.InstanceVars{}
			}
			vars = append(vars, instanceVars)
		}
	}
	return vars, nil
}

func runStrictPipelineStateRequest(database JetbridgeDB, action string, params rata.Params, payload any, authorization string) (int, error) {
	logger := lager.NewLogger("brine-pipeline-state")
	accessFactory, err := strictResourceAccessFactory(database)
	if err != nil {
		return 0, err
	}
	server := pipelineserver.NewServer(logger, database.TeamFactory, db.NewPipelineFactory(database.Conn, database.LockFactory), "https://concourse.invalid")
	pipelineScoped := pipelineserver.NewScopedHandlerFactory(database.TeamFactory)
	teamScoped := api.NewTeamScopedHandlerFactory(logger, database.TeamFactory)
	handlers := rata.Handlers{
		atc.DeletePipeline:            pipelineScoped.HandlerFor(server.DeletePipeline),
		atc.PausePipeline:             pipelineScoped.HandlerFor(server.PausePipeline),
		atc.ArchivePipeline:           pipelineScoped.HandlerFor(server.ArchivePipeline),
		atc.UnpausePipeline:           pipelineScoped.HandlerFor(server.UnpausePipeline),
		atc.ExposePipeline:            pipelineScoped.HandlerFor(server.ExposePipeline),
		atc.HidePipeline:              pipelineScoped.HandlerFor(server.HidePipeline),
		atc.OrderPipelines:            teamScoped.HandlerFor(server.OrderPipelines),
		atc.OrderPipelinesWithinGroup: teamScoped.HandlerFor(server.OrderPipelinesWithinGroup),
	}
	wrapper := wrappa.MultiWrappa{
		wrappa.NewAPIAuthWrappa(auth.NewCheckPipelineAccessHandlerFactory(database.TeamFactory), nil, nil, nil),
		wrappa.NewAccessorWrappa(logger, accessFactory, auditor.NewAuditor(false, false, false, false, false, false, false, false, false, logger), map[string]string{}),
	}
	wrapped := wrapper.Wrap(handlers)
	var routes rata.Routes
	for _, route := range atc.Routes {
		if _, found := wrapped[route.Name]; found {
			routes = append(routes, route)
		}
	}
	router, err := rata.NewRouter(routes, wrapped)
	if err != nil {
		return 0, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	httpServer := &http.Server{Handler: router, ReadHeaderTimeout: 5 * time.Second}
	serveDone := make(chan error, 1)
	go func() { serveDone <- httpServer.Serve(listener) }()
	shutdown := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shutdownErr := httpServer.Shutdown(ctx)
		serveErr := <-serveDone
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			return serveErr
		}
		return shutdownErr
	}

	var requestBody io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			_ = shutdown()
			return 0, err
		}
		requestBody = bytes.NewReader(encoded)
	}
	generator := rata.NewRequestGenerator("http://"+listener.Addr().String(), atc.Routes)
	request, err := generator.CreateRequest(action, params, requestBody)
	if err != nil {
		_ = shutdown()
		return 0, err
	}
	request.Header.Set("Authorization", authorization)
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
	if err != nil {
		_ = shutdown()
		return 0, err
	}
	_, readErr := io.Copy(io.Discard, response.Body)
	closeErr := response.Body.Close()
	shutdownErr := shutdown()
	if readErr != nil {
		return 0, readErr
	}
	if closeErr != nil {
		return 0, closeErr
	}
	if shutdownErr != nil {
		return 0, shutdownErr
	}
	return response.StatusCode, nil
}
