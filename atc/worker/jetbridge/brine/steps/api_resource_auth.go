package steps

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/auditor"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/skymarshal/skycmd"
	"github.com/tedsuo/rata"
)

type strictResourceAuthOutcome struct {
	Value string
}

type strictResourceAuthResponse struct {
	Status int
	Body   string
}

func APIResourceAuthDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, strictResourceAuthOutcome](
			"strict resource authorization profile {string} is exercised over real HTTP",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (strictResourceAuthOutcome, error) {
				profile, ok := p.GetString(0)
				if !ok {
					return strictResourceAuthOutcome{}, fmt.Errorf("strict resource authorization profile parameter is not a string")
				}
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return strictResourceAuthOutcome{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				value, err := observeStrictResourceAuthorization(database, profile)
				return strictResourceAuthOutcome{Value: value}, err
			},
		),
		CheckString[strictResourceAuthOutcome](
			"the strict resource authorization result is {string}",
			"strict resource authorization result",
			func(in strictResourceAuthOutcome) (string, error) { return in.Value, nil },
		),
	}
}

func observeStrictResourceAuthorization(database JetbridgeDB, profile string) (string, error) {
	logger := lager.NewLogger("brine-api-resource-auth")
	accessFactory, err := strictResourceAccessFactory(database)
	if err != nil {
		return "", err
	}
	aud := auditor.NewAuditor(true, true, true, true, true, true, true, true, true, logger)

	switch {
	case len(profile) > len("pipeline-") && profile[:len("pipeline-")] == "pipeline-":
		return observeStrictPipelineAuthorization(database, logger, accessFactory, aud, profile[len("pipeline-"):])
	case len(profile) > len("build-") && profile[:len("build-")] == "build-":
		return observeStrictBuildAuthorization(database, logger, accessFactory, aud, profile[len("build-"):])
	case len(profile) > len("worker-") && profile[:len("worker-")] == "worker-":
		return observeStrictWorkerAuthorization(database, logger, accessFactory, aud, profile[len("worker-"):])
	default:
		return "", fmt.Errorf("unknown strict resource authorization profile %q", profile)
	}
}

func strictResourceAccessFactory(database JetbridgeDB) (accessor.AccessFactory, error) {
	display, err := skycmd.NewSkyDisplayUserIdGenerator(map[string]string{})
	if err != nil {
		return nil, err
	}
	return accessor.NewAccessFactory(
		accessor.NewVerifier(db.NewAccessTokenFactory(database.Conn), []string{brineAuthAudience}),
		database.TeamFactory,
		"sub",
		[]string{"brine-system"},
		display,
	), nil
}

func runStrictResourceAuthorizationHTTP(
	logger lager.Logger,
	accessFactory accessor.AccessFactory,
	aud auditor.Auditor,
	action string,
	inner http.Handler,
	authorization string,
	params rata.Params,
	extraQuery url.Values,
) (strictResourceAuthResponse, error) {
	wrapped := accessor.NewHandler(logger, action, inner, accessFactory, aud, map[string]string{})
	routes := rata.Routes{}
	for _, route := range atc.Routes {
		if route.Name == action {
			routes = append(routes, route)
		}
	}
	if len(routes) != 1 {
		return strictResourceAuthResponse{}, fmt.Errorf("action %q matched %d production routes", action, len(routes))
	}
	router, err := rata.NewRouter(routes, map[string]http.Handler{action: wrapped})
	if err != nil {
		return strictResourceAuthResponse{}, err
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return strictResourceAuthResponse{}, err
	}
	server := &http.Server{Handler: router, ReadHeaderTimeout: 5 * time.Second}
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- server.Serve(listener)
	}()
	shutdown := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shutdownErr := server.Shutdown(ctx)
		serveErr := <-serveDone
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			return serveErr
		}
		return shutdownErr
	}

	requestGenerator := rata.NewRequestGenerator("http://"+listener.Addr().String(), atc.Routes)
	request, err := requestGenerator.CreateRequest(action, params, nil)
	if err != nil {
		_ = shutdown()
		return strictResourceAuthResponse{}, err
	}
	if len(extraQuery) != 0 {
		query := request.URL.Query()
		for key, values := range extraQuery {
			for _, value := range values {
				query.Add(key, value)
			}
		}
		request.URL.RawQuery = query.Encode()
	}
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
	if err != nil {
		_ = shutdown()
		return strictResourceAuthResponse{}, err
	}
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	shutdownErr := shutdown()
	if readErr != nil {
		return strictResourceAuthResponse{}, readErr
	}
	if closeErr != nil {
		return strictResourceAuthResponse{}, closeErr
	}
	if shutdownErr != nil {
		return strictResourceAuthResponse{}, shutdownErr
	}
	return strictResourceAuthResponse{Status: response.StatusCode, Body: string(body)}, nil
}

func strictResourceWorkerFactory(database JetbridgeDB, logger lager.Logger) (db.WorkerFactory, error) {
	cache, err := db.NewWorkerCache(logger.Session("production-worker-cache"), database.Conn, 0)
	if err != nil {
		return nil, err
	}
	return db.NewWorkerFactory(database.Conn, cache), nil
}

func strictResourceATCWorker(name string) atc.Worker {
	return atc.Worker{
		Name: name, Platform: "linux", Version: "1.2.3", State: string(db.WorkerStateRunning),
	}
}

func strictResourceWorkerPresent(database JetbridgeDB, name string) (bool, error) {
	var present bool
	if err := database.Conn.QueryRow("SELECT EXISTS (SELECT 1 FROM workers WHERE name = $1)", name).Scan(&present); err != nil {
		return false, err
	}
	return present, nil
}

func saveAuthPipeline(team db.Team, name string) (db.Pipeline, error) {
	pipeline, _, err := team.SavePipeline(
		atc.PipelineRef{Name: name},
		atc.Config{Jobs: atc.JobConfigs{{Name: "some-job"}}},
		0,
		false,
	)
	return pipeline, err
}

func saveAuthBuild(team db.Team) (db.Build, error) {
	pipeline, err := saveAuthPipeline(team, "some-pipeline")
	if err != nil {
		return nil, err
	}
	job, found, err := pipeline.Job("some-job")
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("saved job not found")
	}
	return job.CreateBuild("brine-user")
}
