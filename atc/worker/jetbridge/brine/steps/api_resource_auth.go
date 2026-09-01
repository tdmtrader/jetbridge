package steps

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/auth"
	"github.com/concourse/concourse/atc/auditor"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/skymarshal/skycmd"
)

// APIResourceAuthDefinitions exercises the authorization handlers which first
// resolve a persisted resource and then decide whether the request may reach a
// resource-scoped delegate. The fixtures are PostgreSQL rows and access_tokens,
// not factory/accessor doubles. Deliberate database errors use a genuinely
// closed connection so the production query itself fails.
func APIResourceAuthDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, AuthHTTPOutcome](
			"the real {string} resource boundary receives case {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (AuthHTTPOutcome, error) {
				boundary, testCase, err := twoParams("the real {string} resource boundary receives case {string}", p)
				if err != nil {
					return AuthHTTPOutcome{}, err
				}
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return AuthHTTPOutcome{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				return exerciseAPIResourceBoundary(database, boundary, testCase)
			},
		),
	}
}

func exerciseAPIResourceBoundary(database JetbridgeDB, boundary, testCase string) (AuthHTTPOutcome, error) {
	logger := lagertest.NewTestLogger("brine-api-resource-auth")
	display, err := skycmd.NewSkyDisplayUserIdGenerator(map[string]string{})
	if err != nil {
		return AuthHTTPOutcome{}, err
	}
	accessFactory := accessor.NewAccessFactory(
		accessor.NewVerifier(db.NewAccessTokenFactory(database.Conn), []string{brineAuthAudience}),
		database.TeamFactory, "sub", []string{"brine-system"}, display,
	)
	aud := auditor.NewAuditor(true, true, true, true, true, true, true, true, true, logger)

	var (
		action        string
		inner         http.Handler
		authorization string
		query         = url.Values{}
	)
	switch boundary {
	case "pipeline":
		action = atc.GetPipeline
		inner, authorization, query, err = preparePipelineBoundary(database, testCase)
	case "build-write":
		action = atc.AbortBuild
		inner, authorization, query, err = prepareBuildWriteBoundary(database, testCase)
	case "build-read":
		action = atc.GetBuild
		inner, authorization, query, err = prepareBuildReadBoundary(database, testCase)
	case "worker":
		action = atc.DeleteWorker
		inner, authorization, query, err = prepareWorkerBoundary(database, testCase)
	default:
		return AuthHTTPOutcome{}, fmt.Errorf("unknown resource boundary %q", boundary)
	}
	if err != nil {
		return AuthHTTPOutcome{}, err
	}

	handler := accessor.NewHandler(logger, action, inner, accessFactory, aud, map[string]string{})
	server := httptest.NewServer(handler)
	defer server.Close()
	req, err := http.NewRequest(http.MethodPost, server.URL+"?"+query.Encode(), nil)
	if err != nil {
		return AuthHTTPOutcome{}, err
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	resp, err := server.Client().Do(req)
	if err != nil {
		return AuthHTTPOutcome{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return AuthHTTPOutcome{}, err
	}
	return AuthHTTPOutcome{Status: resp.StatusCode, Body: string(body)}, nil
}

func preparePipelineBoundary(database JetbridgeDB, testCase string) (http.Handler, string, url.Values, error) {
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "some-team"})
	if err != nil {
		return nil, "", nil, err
	}
	factory := database.TeamFactory
	query := url.Values{":team_name": {team.Name()}, ":pipeline_name": {"some-pipeline"}}
	authorization := ""
	expectedPipelineID := 0

	switch testCase {
	case "team-error":
		closed, err := database.ClosedConn()
		if err != nil {
			return nil, "", nil, err
		}
		factory = db.NewTeamFactory(closed, database.LockFactory)
	case "team-missing":
		if err := team.Delete(); err != nil {
			return nil, "", nil, err
		}
	case "public":
		pipeline, err := saveAuthPipeline(team, "some-pipeline")
		if err != nil {
			return nil, "", nil, err
		}
		if err := pipeline.Expose(); err != nil {
			return nil, "", nil, err
		}
		expectedPipelineID = pipeline.ID()
	case "private-authorized":
		pipeline, err := saveAuthPipeline(team, "some-pipeline")
		if err != nil {
			return nil, "", nil, err
		}
		if err := pipeline.Hide(); err != nil {
			return nil, "", nil, err
		}
		if err := grantAPIAuthRole(team, accessor.ViewerRole); err != nil {
			return nil, "", nil, err
		}
		authorization, err = persistAPIAuthToken(database, "pipeline-viewer", "brine-subject", time.Now().Add(time.Hour))
		expectedPipelineID = pipeline.ID()
	case "private-other-team":
		pipeline, err := saveAuthPipeline(team, "some-pipeline")
		if err != nil {
			return nil, "", nil, err
		}
		if err := pipeline.Hide(); err != nil {
			return nil, "", nil, err
		}
		other, err := database.TeamFactory.CreateTeam(atc.Team{Name: "other-team"})
		if err != nil {
			return nil, "", nil, err
		}
		if err := grantAPIAuthRole(other, accessor.ViewerRole); err != nil {
			return nil, "", nil, err
		}
		authorization, err = persistAPIAuthToken(database, "other-pipeline-viewer", "brine-subject", time.Now().Add(time.Hour))
	case "private-anonymous":
		pipeline, err := saveAuthPipeline(team, "some-pipeline")
		if err != nil {
			return nil, "", nil, err
		}
		if err := pipeline.Hide(); err != nil {
			return nil, "", nil, err
		}
	case "pipeline-missing":
		if _, err := saveAuthPipeline(team, "other-pipeline"); err != nil {
			return nil, "", nil, err
		}
	default:
		return nil, "", nil, fmt.Errorf("unknown pipeline resource case %q", testCase)
	}
	if err != nil {
		return nil, "", nil, err
	}

	delegate := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pipeline, ok := r.Context().Value(auth.PipelineContextKey).(db.Pipeline)
		if !ok || pipeline.ID() != expectedPipelineID {
			http.Error(w, "bad pipeline context", http.StatusInternalServerError)
			return
		}
		_, _ = io.WriteString(w, "delegate")
	})
	return auth.NewCheckPipelineAccessHandlerFactory(factory).HandlerFor(delegate, auth.UnauthorizedRejector{}), authorization, query, nil
}

func prepareBuildReadBoundary(database JetbridgeDB, testCase string) (http.Handler, string, url.Values, error) {
	parts := strings.Split(testCase, "/")
	if len(parts) != 3 {
		return nil, "", nil, fmt.Errorf("build-read case %q must be handler/identity/shape", testCase)
	}
	handlerKind, identity, shape := parts[0], parts[1], parts[2]
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "some-team"})
	if err != nil {
		return nil, "", nil, err
	}

	var (
		build          db.Build
		factory        = database.BuildFactory
		jobPublic      bool
		pipelinePublic bool
		authorization  string
	)
	switch shape {
	case "public-job":
		jobPublic, pipelinePublic = true, true
	case "private-job":
		pipelinePublic = true
	case "public-pipeline":
		pipelinePublic = true
	case "private-pipeline":
	case "one-off":
		build, err = team.CreateOneOffBuild()
	case "missing", "lookup-error":
	default:
		return nil, "", nil, fmt.Errorf("unknown build-read shape %q", shape)
	}
	if err != nil {
		return nil, "", nil, err
	}
	if build == nil {
		_, build, err = saveAuthBuildWithVisibility(team, jobPublic, pipelinePublic)
		if err != nil {
			return nil, "", nil, err
		}
	}
	requestedID := build.ID()
	if shape == "missing" {
		requestedID += 1000000
	}
	if shape == "lookup-error" {
		closed, closeErr := database.ClosedConn()
		if closeErr != nil {
			return nil, "", nil, closeErr
		}
		factory = db.NewBuildFactory(closed, database.LockFactory, 0, time.Hour)
	}

	switch identity {
	case "same-team":
		if err := grantAPIAuthRole(team, accessor.ViewerRole); err != nil {
			return nil, "", nil, err
		}
		authorization, err = persistAPIAuthToken(database, "build-reader-"+shape, "brine-subject", time.Now().Add(time.Hour))
	case "other-team":
		other, createErr := database.TeamFactory.CreateTeam(atc.Team{Name: "other-team"})
		if createErr != nil {
			return nil, "", nil, createErr
		}
		if err := grantAPIAuthRole(other, accessor.ViewerRole); err != nil {
			return nil, "", nil, err
		}
		authorization, err = persistAPIAuthToken(database, "other-build-reader-"+shape, "brine-subject", time.Now().Add(time.Hour))
	case "anonymous":
	default:
		return nil, "", nil, fmt.Errorf("unknown build-read identity %q", identity)
	}
	if err != nil {
		return nil, "", nil, err
	}

	expectedBuildID := build.ID()
	delegate := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contextBuild, ok := r.Context().Value(auth.BuildContextKey).(db.BuildForAPI)
		if !ok || contextBuild.ID() != expectedBuildID {
			http.Error(w, "bad build context", http.StatusInternalServerError)
			return
		}
		_, _ = io.WriteString(w, "delegate")
	})
	handlerFactory := auth.NewCheckBuildReadAccessHandlerFactory(factory)
	var inner http.Handler
	switch handlerKind {
	case "any":
		inner = handlerFactory.AnyJobHandler(delegate, auth.UnauthorizedRejector{})
	case "public-job-only":
		inner = handlerFactory.CheckIfPrivateJobHandler(delegate, auth.UnauthorizedRejector{})
	default:
		return nil, "", nil, fmt.Errorf("unknown build-read handler %q", handlerKind)
	}
	query := url.Values{":build_id": {strconv.Itoa(requestedID)}}
	return inner, authorization, query, nil
}

func prepareBuildWriteBoundary(database JetbridgeDB, testCase string) (http.Handler, string, url.Values, error) {
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "some-team"})
	if err != nil {
		return nil, "", nil, err
	}
	build, err := saveAuthBuild(team)
	if err != nil {
		return nil, "", nil, err
	}
	factory := database.BuildFactory
	requestedID := build.ID()
	expectedBuildID := build.ID()
	authorization := ""

	switch testCase {
	case "same-team":
		if err := grantAPIAuthRole(team, accessor.OperatorRole); err != nil {
			return nil, "", nil, err
		}
		authorization, err = persistAPIAuthToken(database, "build-operator", "brine-subject", time.Now().Add(time.Hour))
	case "missing":
		if err := grantAPIAuthRole(team, accessor.OperatorRole); err != nil {
			return nil, "", nil, err
		}
		authorization, err = persistAPIAuthToken(database, "missing-build-operator", "brine-subject", time.Now().Add(time.Hour))
		requestedID += 1000000
	case "lookup-error":
		if err := grantAPIAuthRole(team, accessor.OperatorRole); err != nil {
			return nil, "", nil, err
		}
		authorization, err = persistAPIAuthToken(database, "error-build-operator", "brine-subject", time.Now().Add(time.Hour))
		closed, closeErr := database.ClosedConn()
		if closeErr != nil {
			return nil, "", nil, closeErr
		}
		factory = db.NewBuildFactory(closed, database.LockFactory, 0, time.Hour)
	case "other-team":
		other, createErr := database.TeamFactory.CreateTeam(atc.Team{Name: "other-team"})
		if createErr != nil {
			return nil, "", nil, createErr
		}
		if err := grantAPIAuthRole(other, accessor.OperatorRole); err != nil {
			return nil, "", nil, err
		}
		authorization, err = persistAPIAuthToken(database, "other-build-operator", "brine-subject", time.Now().Add(time.Hour))
	case "weak-role":
		if err := grantAPIAuthRole(team, accessor.ViewerRole); err != nil {
			return nil, "", nil, err
		}
		authorization, err = persistAPIAuthToken(database, "build-viewer", "brine-subject", time.Now().Add(time.Hour))
	case "anonymous":
		if err := grantAPIAuthRole(team, accessor.OperatorRole); err != nil {
			return nil, "", nil, err
		}
	default:
		return nil, "", nil, fmt.Errorf("unknown build-write resource case %q", testCase)
	}
	if err != nil {
		return nil, "", nil, err
	}

	delegate := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contextBuild, ok := r.Context().Value(auth.BuildContextKey).(db.BuildForAPI)
		if !ok || contextBuild.ID() != expectedBuildID {
			http.Error(w, "bad build context", http.StatusInternalServerError)
			return
		}
		_, _ = io.WriteString(w, "delegate")
	})
	query := url.Values{":team_name": {team.Name()}, ":build_id": {strconv.Itoa(requestedID)}}
	return auth.NewCheckBuildWriteAccessHandlerFactory(factory).HandlerFor(delegate, auth.UnauthorizedRejector{}), authorization, query, nil
}

func prepareWorkerBoundary(database JetbridgeDB, testCase string) (http.Handler, string, url.Values, error) {
	factory := database.WorkerFactory
	authorization := ""
	teamName := "some-team"

	switch testCase {
	case "anonymous":
	case "team-admin":
		team, err := saveTeamWorker(database, teamName)
		if err != nil {
			return nil, "", nil, err
		}
		if err := makeAPIAuthAdmin(database, team); err != nil {
			return nil, "", nil, err
		}
		authorization, err = persistAPIAuthToken(database, "worker-admin", "brine-subject", time.Now().Add(time.Hour))
		if err != nil {
			return nil, "", nil, err
		}
	case "team-system":
		if _, err := saveTeamWorker(database, teamName); err != nil {
			return nil, "", nil, err
		}
		var err error
		authorization, err = persistAPIAuthToken(database, "worker-system", "brine-system", time.Now().Add(time.Hour))
		if err != nil {
			return nil, "", nil, err
		}
	case "team-match":
		team, err := saveTeamWorker(database, teamName)
		if err != nil {
			return nil, "", nil, err
		}
		if err := grantAPIAuthRole(team, accessor.MemberRole); err != nil {
			return nil, "", nil, err
		}
		authorization, err = persistAPIAuthToken(database, "worker-member", "brine-subject", time.Now().Add(time.Hour))
		if err != nil {
			return nil, "", nil, err
		}
	case "team-other":
		if _, err := saveTeamWorker(database, teamName); err != nil {
			return nil, "", nil, err
		}
		other, err := database.TeamFactory.CreateTeam(atc.Team{Name: "other-team"})
		if err != nil {
			return nil, "", nil, err
		}
		if err := grantAPIAuthRole(other, accessor.MemberRole); err != nil {
			return nil, "", nil, err
		}
		authorization, err = persistAPIAuthToken(database, "other-worker-member", "brine-subject", time.Now().Add(time.Hour))
		if err != nil {
			return nil, "", nil, err
		}
	case "global-admin":
		if _, err := database.PersistNamedWorker("some-worker"); err != nil {
			return nil, "", nil, err
		}
		team, err := database.TeamFactory.CreateTeam(atc.Team{Name: teamName})
		if err != nil {
			return nil, "", nil, err
		}
		if err := makeAPIAuthAdmin(database, team); err != nil {
			return nil, "", nil, err
		}
		authorization, err = persistAPIAuthToken(database, "global-worker-admin", "brine-subject", time.Now().Add(time.Hour))
		if err != nil {
			return nil, "", nil, err
		}
	case "global-member":
		if _, err := database.PersistNamedWorker("some-worker"); err != nil {
			return nil, "", nil, err
		}
		team, err := database.TeamFactory.CreateTeam(atc.Team{Name: teamName})
		if err != nil {
			return nil, "", nil, err
		}
		if err := grantAPIAuthRole(team, accessor.MemberRole); err != nil {
			return nil, "", nil, err
		}
		authorization, err = persistAPIAuthToken(database, "global-worker-member", "brine-subject", time.Now().Add(time.Hour))
		if err != nil {
			return nil, "", nil, err
		}
	case "missing":
		team, err := database.TeamFactory.CreateTeam(atc.Team{Name: teamName})
		if err != nil {
			return nil, "", nil, err
		}
		if err := grantAPIAuthRole(team, accessor.MemberRole); err != nil {
			return nil, "", nil, err
		}
		authorization, err = persistAPIAuthToken(database, "missing-worker-member", "brine-subject", time.Now().Add(time.Hour))
		if err != nil {
			return nil, "", nil, err
		}
	case "lookup-error":
		team, err := database.TeamFactory.CreateTeam(atc.Team{Name: teamName})
		if err != nil {
			return nil, "", nil, err
		}
		if err := grantAPIAuthRole(team, accessor.MemberRole); err != nil {
			return nil, "", nil, err
		}
		authorization, err = persistAPIAuthToken(database, "error-worker-member", "brine-subject", time.Now().Add(time.Hour))
		if err != nil {
			return nil, "", nil, err
		}
		closed, err := database.ClosedConn()
		if err != nil {
			return nil, "", nil, err
		}
		factory = db.NewWorkerFactory(closed, db.NewStaticWorkerCache(lagertest.NewTestLogger("closed-worker-cache"), closed, 0))
	default:
		return nil, "", nil, fmt.Errorf("unknown worker resource case %q", testCase)
	}

	delegate := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "delegate")
	})
	query := url.Values{":team_name": {teamName}, ":worker_name": {"some-worker"}}
	return auth.NewCheckWorkerTeamAccessHandlerFactory(factory).HandlerFor(delegate, auth.UnauthorizedRejector{}), authorization, query, nil
}

func saveAuthPipeline(team db.Team, name string) (db.Pipeline, error) {
	pipeline, _, err := team.SavePipeline(
		atc.PipelineRef{Name: name},
		atc.Config{Jobs: atc.JobConfigs{{Name: "some-job"}}},
		0, false,
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

func saveAuthBuildWithVisibility(team db.Team, jobPublic, pipelinePublic bool) (db.Pipeline, db.Build, error) {
	pipeline, _, err := team.SavePipeline(
		atc.PipelineRef{Name: "some-pipeline"},
		atc.Config{Jobs: atc.JobConfigs{{Name: "some-job", Public: jobPublic}}},
		0, false,
	)
	if err != nil {
		return nil, nil, err
	}
	if pipelinePublic {
		err = pipeline.Expose()
	} else {
		err = pipeline.Hide()
	}
	if err != nil {
		return nil, nil, err
	}
	job, found, err := pipeline.Job("some-job")
	if err != nil {
		return nil, nil, err
	}
	if !found {
		return nil, nil, fmt.Errorf("saved job not found")
	}
	build, err := job.CreateBuild("brine-user")
	return pipeline, build, err
}

func saveTeamWorker(database JetbridgeDB, teamName string) (db.Team, error) {
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: teamName})
	if err != nil {
		return nil, err
	}
	_, err = team.SaveWorker(atc.Worker{
		Name: "some-worker", Platform: "linux", Version: "1.2.3", State: string(db.WorkerStateRunning),
	}, 5*time.Minute)
	return team, err
}
