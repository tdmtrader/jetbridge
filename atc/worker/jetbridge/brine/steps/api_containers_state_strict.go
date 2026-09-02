package steps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"reflect"
	"time"

	"code.cloudfoundry.org/clock"
	"code.cloudfoundry.org/lager/v3"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/auth"
	"github.com/concourse/concourse/atc/api/containerserver"
	"github.com/concourse/concourse/atc/auditor"
	"github.com/concourse/concourse/atc/db"
	atcworker "github.com/concourse/concourse/atc/worker"
	"github.com/concourse/concourse/atc/wrappa"
	"github.com/tedsuo/rata"
)

type strictContainersAPIObservation struct {
	Profile string
	Failure string
}

type strictContainersAPIResponse struct {
	Status      int
	ContentType string
	Body        []byte
}

type strictContainersAPIFixture struct {
	Container db.Container
	Created   db.CreatedContainer
	Build     db.Build
}

func ContainersAPIStateStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, strictContainersAPIObservation](
			"the production container query API behavior {string} is exercised",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (strictContainersAPIObservation, error) {
				profile, err := paramAt("the production container query API behavior {string} is exercised", p, 0)
				if err != nil {
					return strictContainersAPIObservation{}, err
				}
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return strictContainersAPIObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				return strictContainersAPIObservation{Profile: profile, Failure: observeStrictContainersAPI(database, profile)}, nil
			},
		),
		brine.DefineCheck[strictContainersAPIObservation](
			"the container query API behavior exactly matches {string}",
			func(in strictContainersAPIObservation, p brine.Params, _ *brine.Recorder) error {
				profile, err := paramAt("the container query API behavior exactly matches {string}", p, 0)
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

func observeStrictContainersAPI(database JetbridgeDB, profile string) string {
	fail := func(format string, args ...any) string { return fmt.Sprintf(format, args...) }
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "strict-containers-api"})
	if err != nil {
		return err.Error()
	}
	if err := grantAPIAuthRole(team, accessor.ViewerRole); err != nil {
		return err.Error()
	}
	authorization, err := persistAPIAuthToken(database, "strict-containers-"+profile, brineAuthSubject, time.Now().Add(time.Hour))
	if err != nil {
		return err.Error()
	}

	metadata := db.ContainerMetadata{
		Type: db.ContainerTypeTask, StepName: "some-step", Attempt: "1.5",
		PipelineID: 1111, JobID: 2222, WorkingDirectory: "/tmp/build/strict", User: "snoopy",
	}
	action := atc.ListContainers
	params := rata.Params{"team_name": team.Name()}
	var fixtures []strictContainersAPIFixture

	switch profile {
	case "list-status", "list-content-type", "list-body":
		first, err := strictCreateAPIContainer(database, team, "strict-worker-one", "strict-plan-one", metadata, false)
		if err != nil {
			return err.Error()
		}
		other := metadata
		other.StepName = "other-step"
		other.Attempt = "2.1"
		other.PipelineID++
		other.JobID++
		other.WorkingDirectory += "/other"
		other.User = "woodstock"
		second, err := strictCreateAPIContainer(database, team, "strict-worker-two", "strict-plan-two", other, true)
		if err != nil {
			return err.Error()
		}
		fixtures = []strictContainersAPIFixture{first, second}
	case "list-empty-status", "list-empty-body":
	case "get-missing":
		fixture, err := strictCreateAPIContainer(database, team, "strict-worker-missing", "strict-plan-missing", metadata, true)
		if err != nil {
			return err.Error()
		}
		destroying, err := fixture.Created.Destroying()
		if err != nil {
			return err.Error()
		}
		destroyed, err := destroying.Destroy()
		if err != nil || !destroyed {
			return fail("destroy setup changed=%t err=%v", destroyed, err)
		}
		action = atc.GetContainer
		params["id"] = fixture.Container.Handle()
	case "get-status", "get-content-type", "get-body":
		fixture, err := strictCreateAPIContainer(database, team, "strict-worker-get", "strict-plan-get", metadata, true)
		if err != nil {
			return err.Error()
		}
		fixtures = []strictContainersAPIFixture{fixture}
		action = atc.GetContainer
		params["id"] = fixture.Container.Handle()
	case "get-outside-team":
		outside, err := database.TeamFactory.CreateTeam(atc.Team{Name: "strict-containers-outside"})
		if err != nil {
			return err.Error()
		}
		fixture, err := strictCreateAPIContainer(database, outside, "strict-worker-outside", "strict-plan-outside", metadata, true)
		if err != nil {
			return err.Error()
		}
		action = atc.GetContainer
		params["id"] = fixture.Container.Handle()
	default:
		return fmt.Sprintf("unknown profile %q", profile)
	}

	response, err := runStrictContainersAPIRequest(database, action, params, authorization)
	if err != nil {
		return err.Error()
	}
	switch profile {
	case "list-status", "list-empty-status", "get-status":
		if response.Status != http.StatusOK {
			return fail("status=%d", response.Status)
		}
	case "list-content-type", "get-content-type":
		if response.ContentType != "application/json" {
			return fail("content-type=%q", response.ContentType)
		}
	case "list-body":
		var got []atc.Container
		if err := json.Unmarshal(response.Body, &got); err != nil {
			return err.Error()
		}
		want := []atc.Container{
			strictExpectedAPIContainer(fixtures[0], metadata, "strict-worker-one", atc.ContainerStateCreating),
			strictExpectedAPIContainer(fixtures[1], db.ContainerMetadata{
				Type: db.ContainerTypeTask, StepName: "other-step", Attempt: "2.1",
				PipelineID: 1112, JobID: 2223, WorkingDirectory: "/tmp/build/strict/other", User: "woodstock",
			}, "strict-worker-two", atc.ContainerStateCreated),
		}
		if !strictContainersEqual(got, want) {
			return fail("containers=%#v want=%#v", got, want)
		}
	case "list-empty-body":
		var got []atc.Container
		if err := json.Unmarshal(response.Body, &got); err != nil || got == nil || len(got) != 0 {
			return fail("empty containers=%#v err=%v", got, err)
		}
	case "get-missing", "get-outside-team":
		if response.Status != http.StatusNotFound {
			return fail("status=%d", response.Status)
		}
	case "get-body":
		var got atc.Container
		if err := json.Unmarshal(response.Body, &got); err != nil {
			return err.Error()
		}
		want := strictExpectedAPIContainer(fixtures[0], metadata, "strict-worker-get", atc.ContainerStateCreated)
		if !reflect.DeepEqual(got, want) {
			return fail("container=%#v want=%#v", got, want)
		}
	}
	return ""
}

func strictCreateAPIContainer(database JetbridgeDB, team db.Team, workerName string, planID atc.PlanID, metadata db.ContainerMetadata, markCreated bool) (strictContainersAPIFixture, error) {
	worker, err := database.WorkerFactory.SaveWorker(atc.Worker{
		Name: workerName, Platform: "linux", Version: "1.2.3", State: string(db.WorkerStateRunning),
	}, 0)
	if err != nil {
		return strictContainersAPIFixture{}, err
	}
	build, err := team.CreateOneOffBuild()
	if err != nil {
		return strictContainersAPIFixture{}, err
	}
	metadata.BuildID = build.ID()
	creating, err := worker.CreateContainer(db.NewBuildStepContainerOwner(build.ID(), planID, team.ID()), metadata)
	if err != nil {
		return strictContainersAPIFixture{}, err
	}
	fixture := strictContainersAPIFixture{Container: creating, Build: build}
	if markCreated {
		fixture.Created, err = creating.Created()
		if err != nil {
			return strictContainersAPIFixture{}, err
		}
		fixture.Container = fixture.Created
	}
	return fixture, nil
}

func strictExpectedAPIContainer(fixture strictContainersAPIFixture, metadata db.ContainerMetadata, workerName string, state string) atc.Container {
	return atc.Container{
		ID: fixture.Container.Handle(), WorkerName: workerName, State: state, Type: string(metadata.Type),
		StepName: metadata.StepName, Attempt: metadata.Attempt, PipelineID: metadata.PipelineID,
		JobID: metadata.JobID, BuildID: fixture.Build.ID(), WorkingDirectory: metadata.WorkingDirectory, User: metadata.User,
	}
}

func strictContainersEqual(got, want []atc.Container) bool {
	if len(got) != len(want) {
		return false
	}
	byID := make(map[string]atc.Container, len(got))
	for _, container := range got {
		byID[container.ID] = container
	}
	for _, expected := range want {
		if actual, found := byID[expected.ID]; !found || !reflect.DeepEqual(actual, expected) {
			return false
		}
	}
	return true
}

func runStrictContainersAPIRequest(database JetbridgeDB, action string, params rata.Params, authorization string) (strictContainersAPIResponse, error) {
	logger := lager.NewLogger("brine-containers-api")
	accessFactory, err := strictResourceAccessFactory(database)
	if err != nil {
		return strictContainersAPIResponse{}, err
	}
	server := containerserver.NewServer(
		logger,
		atcworker.Pool{},
		containerserver.NewInterceptTimeoutFactory(time.Minute),
		time.Second,
		clock.NewClock(),
	)
	teamScoped := api.NewTeamScopedHandlerFactory(logger, database.TeamFactory)
	handlers := rata.Handlers{
		atc.ListContainers: teamScoped.HandlerFor(server.ListContainers),
		atc.GetContainer:   teamScoped.HandlerFor(server.GetContainer),
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
		return strictContainersAPIResponse{}, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return strictContainersAPIResponse{}, err
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

	generator := rata.NewRequestGenerator("http://"+listener.Addr().String(), atc.Routes)
	request, err := generator.CreateRequest(action, params, nil)
	if err != nil {
		_ = shutdown()
		return strictContainersAPIResponse{}, err
	}
	request.Header.Set("Authorization", authorization)
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
	if err != nil {
		_ = shutdown()
		return strictContainersAPIResponse{}, err
	}
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	shutdownErr := shutdown()
	if readErr != nil {
		return strictContainersAPIResponse{}, readErr
	}
	if closeErr != nil {
		return strictContainersAPIResponse{}, closeErr
	}
	if shutdownErr != nil {
		return strictContainersAPIResponse{}, shutdownErr
	}
	return strictContainersAPIResponse{Status: response.StatusCode, ContentType: response.Header.Get("Content-Type"), Body: body}, nil
}
