package steps

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/ccserver"
	"github.com/concourse/concourse/atc/auditor"
	"github.com/concourse/concourse/atc/db"
	"github.com/tedsuo/rata"
)

var strictCCEndTime = time.Date(2018, time.November, 4, 21, 26, 38, 0, time.UTC)

type strictCCObservation struct {
	Status      int
	ContentType string
	Body        []byte
}

func CCAPIStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, strictCCObservation](
			"strict CC API profile {string} is exercised over real HTTP",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (strictCCObservation, error) {
				profile, ok := p.GetString(0)
				if !ok {
					return strictCCObservation{}, fmt.Errorf("strict CC API profile parameter is not a string")
				}
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return strictCCObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				return observeStrictCCAPI(database, profile)
			},
		),
		brine.DefineCheck[strictCCObservation](
			"the strict CC observation {string} is {string}",
			func(in strictCCObservation, p brine.Params, _ *brine.Recorder) error {
				kind, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("strict CC observation kind is not a string")
				}
				want, ok := p.GetString(1)
				if !ok {
					return fmt.Errorf("strict CC expected observation is not a string")
				}
				got, err := strictCCObservationValue(in, kind)
				if err != nil {
					return err
				}
				if got != want {
					return fmt.Errorf("expected strict CC %s observation to be %q, got %q", kind, want, got)
				}
				return nil
			},
		),
	}
}

func strictCCObservationValue(observation strictCCObservation, kind string) (string, error) {
	switch kind {
	case "status":
		return fmt.Sprintf("%d", observation.Status), nil
	case "content-type":
		return observation.ContentType, nil
	}

	var projects ccserver.ProjectsContainer
	if err := xml.Unmarshal(observation.Body, &projects); err != nil {
		return "", fmt.Errorf("decode production CC XML: %w", err)
	}
	if projects.XMLName.Local != "Projects" {
		return "", fmt.Errorf("production CC XML root is %q", projects.XMLName.Local)
	}

	switch kind {
	case "empty":
		return fmt.Sprintf("root=%s;projects=%d", projects.XMLName.Local, len(projects.Projects)), nil
	case "project":
		if len(projects.Projects) != 1 {
			return fmt.Sprintf("root=%s;projects=%d", projects.XMLName.Local, len(projects.Projects)), nil
		}
		project := projects.Projects[0]
		name := strings.ReplaceAll(project.Name, `"`, `'`)
		return fmt.Sprintf(
			"activity=%s;label=%s;build-status=%s;time=%s;name=%s;url=%s",
			project.Activity,
			project.LastBuildLabel,
			project.LastBuildStatus,
			project.LastBuildTime,
			name,
			project.WebUrl,
		), nil
	default:
		return "", fmt.Errorf("unknown strict CC observation kind %q", kind)
	}
}

func observeStrictCCAPI(database JetbridgeDB, profile string) (strictCCObservation, error) {
	logger := lager.NewLogger("brine-cc-api")
	accessFactory, err := strictResourceAccessFactory(database)
	if err != nil {
		return strictCCObservation{}, err
	}
	aud := auditor.NewAuditor(true, true, true, true, true, true, true, true, true, logger)

	teamName := "a-team"
	teamToAuthorize := teamName
	if profile == "missing-team-status" {
		teamToAuthorize = "auth-team"
	}
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: teamToAuthorize})
	if err != nil {
		return strictCCObservation{}, err
	}
	if err := grantAPIAuthRole(team, accessor.ViewerRole); err != nil {
		return strictCCObservation{}, err
	}
	authorization, err := persistAPIAuthToken(database, "strict-cc-"+profile, "brine-cc-subject", time.Now().Add(time.Hour))
	if err != nil {
		return strictCCObservation{}, err
	}

	if profile != "missing-team-status" {
		if err := prepareStrictCCProfile(database, team, profile); err != nil {
			return strictCCObservation{}, err
		}
	}

	cc := ccserver.NewServer(logger, database.TeamFactory, "https://example.com")
	wrapped := accessor.NewHandler(logger, atc.GetCC, http.HandlerFunc(cc.GetCC), accessFactory, aud, map[string]string{})
	return runStrictCCHTTP(wrapped, authorization, teamName)
}

func prepareStrictCCProfile(database JetbridgeDB, team db.Team, profile string) error {
	ref := atc.PipelineRef{Name: "something-else"}
	if profile == "instanced-project" {
		ref.InstanceVars = atc.InstanceVars{"branch": "feature/foo"}
	}

	switch profile {
	case "no-pipeline-status", "no-pipeline-empty":
		return nil
	case "no-job-status", "no-job-empty":
		_, _, err := team.SavePipeline(ref, atc.Config{}, 0, false)
		return err
	}

	pipeline, _, err := team.SavePipeline(ref, atc.Config{Jobs: atc.JobConfigs{{Name: "some-job"}}}, 0, false)
	if err != nil {
		return err
	}
	job, found, err := pipeline.Job("some-job")
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("strict CC job is missing")
	}
	if profile == "no-last-build-empty" {
		return nil
	}

	status := db.BuildStatusSucceeded
	switch profile {
	case "aborted-project":
		status = db.BuildStatusAborted
	case "errored-project":
		status = db.BuildStatusErrored
	case "failed-project":
		status = db.BuildStatusFailed
	case "succeeded-status", "succeeded-content-type", "succeeded-project", "building-project", "instanced-project":
	default:
		return fmt.Errorf("unknown strict CC API profile %q", profile)
	}
	if err := finishStrictCCBuild(database, job, status); err != nil {
		return err
	}
	if profile == "building-project" {
		next, err := job.CreateBuild("cc-api-test")
		if err != nil {
			return err
		}
		started, err := next.Start(atc.Plan{})
		if err != nil {
			return err
		}
		if !started {
			return fmt.Errorf("strict CC next build did not start")
		}
	}
	return nil
}

func finishStrictCCBuild(database JetbridgeDB, job db.Job, status db.BuildStatus) error {
	build, err := job.CreateBuild("cc-api-test")
	if err != nil {
		return err
	}
	started, err := build.Start(atc.Plan{})
	if err != nil {
		return err
	}
	if !started {
		return fmt.Errorf("strict CC build did not start")
	}
	if err := build.Finish(status); err != nil {
		return err
	}
	if _, err := database.Conn.Exec("UPDATE builds SET end_time = $1 WHERE id = $2", strictCCEndTime, build.ID()); err != nil {
		return err
	}
	found, err := build.Reload()
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("strict CC build disappeared")
	}
	return nil
}

func runStrictCCHTTP(handler http.Handler, authorization string, teamName string) (strictCCObservation, error) {
	routes := rata.Routes{}
	for _, route := range atc.Routes {
		if route.Name == atc.GetCC {
			routes = append(routes, route)
		}
	}
	if len(routes) != 1 {
		return strictCCObservation{}, fmt.Errorf("GetCC matched %d production routes", len(routes))
	}
	router, err := rata.NewRouter(routes, map[string]http.Handler{atc.GetCC: handler})
	if err != nil {
		return strictCCObservation{}, err
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return strictCCObservation{}, err
	}
	server := &http.Server{Handler: router, ReadHeaderTimeout: 5 * time.Second}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
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
	request, err := requestGenerator.CreateRequest(atc.GetCC, rata.Params{"team_name": teamName}, nil)
	if err != nil {
		_ = shutdown()
		return strictCCObservation{}, err
	}
	request.Header.Set("Authorization", authorization)
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
	if err != nil {
		_ = shutdown()
		return strictCCObservation{}, err
	}
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	shutdownErr := shutdown()
	if readErr != nil {
		return strictCCObservation{}, readErr
	}
	if closeErr != nil {
		return strictCCObservation{}, closeErr
	}
	if shutdownErr != nil {
		return strictCCObservation{}, shutdownErr
	}
	return strictCCObservation{Status: response.StatusCode, ContentType: response.Header.Get("Content-Type"), Body: body}, nil
}
