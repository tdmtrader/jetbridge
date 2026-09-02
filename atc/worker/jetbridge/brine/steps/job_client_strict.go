package steps

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/auth"
	"github.com/concourse/concourse/atc/api/jobserver"
	"github.com/concourse/concourse/atc/api/pipelineserver"
	"github.com/concourse/concourse/atc/auditor"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/util"
	"github.com/concourse/concourse/atc/wrappa"
	clientapi "github.com/concourse/concourse/go-concourse/concourse"
	"github.com/concourse/concourse/skymarshal/skycmd"
	"github.com/tedsuo/rata"
	"golang.org/x/oauth2"
)

const (
	jobStrictAudience  = "brine-job-client"
	jobStrictConnector = "brine-job-connector"
	jobStrictUserID    = "brine-job-user"
	jobStrictTeamName  = "job-team"
)

type JobStrictObservation struct {
	Profile          string
	Names            []string
	BuildIDs         []int
	CreatedBuildIDs  []int
	Found            bool
	Err              error
	Previous         *clientapi.Page
	Next             *clientapi.Page
	Status           int
	Paused           bool
	PausedBy         string
	ScheduleAdvanced bool
	Body             []byte
}

type strictJobBoundary struct {
	database     JetbridgeDB
	team         db.Team
	pipeline     db.Pipeline
	job          db.Job
	ref          atc.PipelineRef
	url          string
	httpClient   *http.Client
	outsiderHTTP *http.Client
	publicHTTP   *http.Client
	client       clientapi.Client
	clientTeam   clientapi.Team
}

func JobClientStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, JobStrictObservation](
			"the production job boundary executes profile {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, rec *brine.Recorder, resources brine.Resources) (JobStrictObservation, error) {
				profile, err := paramAt("the production job boundary executes profile {string}", p, 0)
				if err != nil {
					return JobStrictObservation{}, err
				}
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return JobStrictObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				boundary, err := newStrictJobBoundary(database, rec)
				if err != nil {
					return JobStrictObservation{}, err
				}
				return boundary.observe(profile)
			},
		),
		brine.DefineCheck[JobStrictObservation](
			"the production job observation exactly matches profile {string}",
			func(in JobStrictObservation, p brine.Params, _ *brine.Recorder) error {
				profile, err := paramAt("the production job observation exactly matches profile {string}", p, 0)
				if err != nil {
					return err
				}
				if profile != in.Profile {
					return fmt.Errorf("job observation profile: got %q, want %q", in.Profile, profile)
				}
				return validateStrictJobObservation(in)
			},
		),
	}
}

func newStrictJobBoundary(database JetbridgeDB, rec *brine.Recorder) (*strictJobBoundary, error) {
	logger := lager.NewLogger("brine-job-client-strict")
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: jobStrictTeamName})
	if err != nil {
		return nil, fmt.Errorf("create job team: %w", err)
	}
	if err := team.UpdateProviderAuth(atc.TeamAuth{
		accessor.OwnerRole: {"users": {jobStrictConnector + ":" + jobStrictUserID}},
	}); err != nil {
		return nil, fmt.Errorf("grant job team owner role: %w", err)
	}
	if _, err := database.Conn.Exec(`UPDATE teams SET admin = TRUE WHERE id = $1`, team.ID()); err != nil {
		return nil, fmt.Errorf("make job client identity administrator: %w", err)
	}
	team, found, err := database.TeamFactory.FindTeam(jobStrictTeamName)
	if err != nil || !found {
		return nil, strictJobFirstError(err, fmt.Errorf("job team disappeared after update"))
	}

	ref := atc.PipelineRef{Name: "target", InstanceVars: atc.InstanceVars{"branch": "master"}}
	pipeline, _, err := team.SavePipeline(ref, atc.Config{Jobs: atc.JobConfigs{{
		Name: "build", Public: true,
		PlanSequence: []atc.Step{{Config: &atc.TaskStep{Config: &atc.TaskConfig{
			Run: atc.TaskRunConfig{Path: "true"},
		}}}},
	}}}, 0, false)
	if err != nil {
		return nil, fmt.Errorf("save job pipeline: %w", err)
	}
	// A same-named, non-instanced pipeline makes loss of the production
	// InstanceVars query observable: requests would resolve a real but different
	// pipeline instead of merely returning the same 404.
	if _, _, err := team.SavePipeline(
		atc.PipelineRef{Name: ref.Name},
		atc.Config{Jobs: atc.JobConfigs{{Name: "missing"}}},
		0, false,
	); err != nil {
		return nil, fmt.Errorf("save discriminator job pipeline: %w", err)
	}
	job, found, err := pipeline.Job("build")
	if err != nil || !found {
		return nil, strictJobFirstError(err, fmt.Errorf("saved job was not found"))
	}

	token := "brine-job-client-token"
	payload, err := json.Marshal(map[string]any{
		"sub": jobStrictUserID, "preferred_username": jobStrictUserID,
		"aud": []any{jobStrictAudience}, "exp": time.Now().Add(time.Hour).Unix(),
		"federated_claims": map[string]any{"connector_id": jobStrictConnector, "user_id": jobStrictUserID},
	})
	if err != nil {
		return nil, err
	}
	var claims db.Claims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, err
	}
	if err := db.NewAccessTokenFactory(database.Conn).CreateAccessToken(token, claims); err != nil {
		return nil, fmt.Errorf("persist job client access token: %w", err)
	}
	outsiderToken := "brine-job-outsider-token"
	outsiderPayload, err := json.Marshal(map[string]any{
		"sub": "brine-job-outsider", "preferred_username": "brine-job-outsider",
		"aud": []any{jobStrictAudience}, "exp": time.Now().Add(time.Hour).Unix(),
		"federated_claims": map[string]any{"connector_id": jobStrictConnector, "user_id": "brine-job-outsider"},
	})
	if err != nil {
		return nil, err
	}
	var outsiderClaims db.Claims
	if err := json.Unmarshal(outsiderPayload, &outsiderClaims); err != nil {
		return nil, err
	}
	if err := db.NewAccessTokenFactory(database.Conn).CreateAccessToken(outsiderToken, outsiderClaims); err != nil {
		return nil, fmt.Errorf("persist job outsider access token: %w", err)
	}

	display, err := skycmd.NewSkyDisplayUserIdGenerator(map[string]string{})
	if err != nil {
		return nil, err
	}
	accessFactory := accessor.NewAccessFactory(
		accessor.NewVerifier(db.NewAccessTokenFactory(database.Conn), []string{jobStrictAudience}),
		database.TeamFactory, "sub", []string{"brine-system"}, display,
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
			logger, accessFactory,
			auditor.NewAuditor(false, false, false, false, false, true, false, false, false, logger),
			map[string]string{},
		),
	}

	jobFactory := db.NewJobFactory(database.Conn, database.LockFactory)
	checkFactory := db.NewCheckFactory(
		database.Conn, database.LockFactory, make(chan db.Build, 8), util.NewSequenceGenerator(1),
	)
	server := jobserver.NewServer(logger, "https://concourse.invalid", nil, jobFactory, checkFactory)
	scoped := pipelineserver.NewScopedHandlerFactory(database.TeamFactory)
	handlers := rata.Handlers{
		atc.ListAllJobs:    http.HandlerFunc(server.ListAllJobs),
		atc.ListJobs:       scoped.HandlerFor(server.ListJobs),
		atc.GetJob:         scoped.HandlerFor(server.GetJob),
		atc.ListJobBuilds:  scoped.HandlerFor(server.ListJobBuilds),
		atc.PauseJob:       scoped.HandlerFor(server.PauseJob),
		atc.UnpauseJob:     scoped.HandlerFor(server.UnpauseJob),
		atc.ScheduleJob:    scoped.HandlerFor(server.ScheduleJob),
		atc.JobBadge:       scoped.HandlerFor(server.JobBadge),
		atc.ListJobInputs:  scoped.HandlerFor(server.ListJobInputs),
		atc.GetJobBuild:    scoped.HandlerFor(server.GetJobBuild),
		atc.ClearTaskCache: scoped.HandlerFor(server.ClearTaskCache),
	}
	var routes rata.Routes
	for _, route := range atc.Routes {
		if _, ok := handlers[route.Name]; ok {
			routes = append(routes, route)
		}
	}
	router, err := rata.NewRouter(routes, wrapper.Wrap(handlers))
	if err != nil {
		return nil, fmt.Errorf("build production job router: %w", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen for production job API: %w", err)
	}
	httpServer := &http.Server{Handler: router, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = httpServer.Serve(listener) }()
	httpClient := oauth2.NewClient(context.Background(), oauth2.StaticTokenSource(&oauth2.Token{
		AccessToken: token, TokenType: "Bearer",
	}))
	httpClient.Timeout = 30 * time.Second
	outsiderHTTP := oauth2.NewClient(context.Background(), oauth2.StaticTokenSource(&oauth2.Token{
		AccessToken: outsiderToken, TokenType: "Bearer",
	}))
	outsiderHTTP.Timeout = 30 * time.Second
	publicHTTP := &http.Client{Timeout: 30 * time.Second}
	rec.RegisterDisposer(func() {
		httpClient.CloseIdleConnections()
		outsiderHTTP.CloseIdleConnections()
		publicHTTP.CloseIdleConnections()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(ctx); err != nil {
			_ = httpServer.Close()
		}
	})

	url := "http://" + listener.Addr().String()
	client := clientapi.NewClient(url, httpClient, false)
	return &strictJobBoundary{
		database: database, team: team, pipeline: pipeline, job: job, ref: ref,
		url: url, httpClient: httpClient, outsiderHTTP: outsiderHTTP, publicHTTP: publicHTTP,
		client: client, clientTeam: client.Team(jobStrictTeamName),
	}, nil
}

func (boundary *strictJobBoundary) observe(profile string) (JobStrictObservation, error) {
	observation := JobStrictObservation{Profile: profile}
	switch profile {
	case "client-list-pipeline":
		jobs, err := boundary.clientTeam.ListJobs(boundary.ref)
		observation.Names, observation.Err = strictJobNames(jobs), err
	case "client-list-all":
		jobs, err := boundary.client.ListAllJobs()
		observation.Names, observation.Err = strictJobNames(jobs), err
	case "client-get-existing":
		job, found, err := boundary.clientTeam.Job(boundary.ref, "build")
		observation.Found, observation.Err = found, err
		if found {
			observation.Names = []string{job.Name}
		}
	case "client-get-missing":
		_, observation.Found, observation.Err = boundary.clientTeam.Job(boundary.ref, "missing")
	case "client-builds-all", "client-builds-from", "client-builds-from-limit",
		"client-builds-to", "client-builds-to-limit", "client-builds-from-to",
		"client-pagination-links", "client-pagination-empty":
		if err := boundary.createBuilds(&observation, 3); err != nil {
			return observation, err
		}
		page, err := strictJobPage(profile, observation.CreatedBuildIDs)
		if err != nil {
			return observation, err
		}
		builds, pagination, found, err := boundary.clientTeam.JobBuilds(boundary.ref, "build", page)
		observation.Found, observation.Err = found, err
		observation.Previous, observation.Next = pagination.Previous, pagination.Next
		for _, build := range builds {
			observation.BuildIDs = append(observation.BuildIDs, build.ID)
		}
	case "client-builds-missing":
		_, _, observation.Found, observation.Err = boundary.clientTeam.JobBuilds(boundary.ref, "missing", clientapi.Page{})
	case "client-pause-existing", "client-unpause-existing", "client-schedule-existing",
		"client-pause-missing", "client-unpause-missing", "client-schedule-missing":
		if err := boundary.observeClientMutation(&observation); err != nil {
			return observation, err
		}
	case "api-list-admin", "api-get-missing", "api-builds-missing", "api-pause-existing",
		"api-pause-missing", "api-unpause-existing", "api-unpause-missing",
		"api-schedule-existing", "api-schedule-missing":
		if err := boundary.observeRawAPI(&observation); err != nil {
			return observation, err
		}
	case "jobs-get-unauthorized", "jobs-get-forbidden", "jobs-badge-forbidden",
		"jobs-list-unauthorized", "jobs-builds-forbidden", "jobs-inputs-unauthorized",
		"jobs-inputs-forbidden", "jobs-build-unauthorized", "jobs-build-forbidden",
		"jobs-pause-unauthorized", "jobs-unpause-unauthorized", "jobs-cache-unauthorized",
		"jobs-schedule-unauthorized":
		if err := boundary.observeAuthorization(&observation); err != nil {
			return observation, err
		}
	default:
		return observation, fmt.Errorf("unknown strict job profile %q", profile)
	}
	return observation, nil
}

func (boundary *strictJobBoundary) observeAuthorization(observation *JobStrictObservation) error {
	profile := observation.Profile
	client := boundary.publicHTTP
	wantStatus := http.StatusUnauthorized
	if strings.HasSuffix(profile, "forbidden") {
		client = boundary.outsiderHTTP
		wantStatus = http.StatusForbidden
	}
	method := http.MethodGet
	path := boundary.jobPath("build", "")
	switch {
	case strings.Contains(profile, "badge"):
		path = boundary.jobPath("build", "/badge")
	case strings.Contains(profile, "list-"):
		path = fmt.Sprintf("/api/v1/teams/%s/pipelines/%s/jobs?%s", jobStrictTeamName, boundary.ref.Name, boundary.ref.QueryParams().Encode())
	case strings.Contains(profile, "builds-"):
		path = boundary.jobPath("build", "/builds")
	case strings.Contains(profile, "inputs-"):
		path = boundary.jobPath("build", "/inputs")
	case strings.Contains(profile, "build-"):
		build, err := boundary.job.CreateBuild(jobStrictUserID)
		if err != nil {
			return err
		}
		path = boundary.jobPath("build", "/builds/"+build.Name())
	case strings.Contains(profile, "unpause-"):
		method, path = http.MethodPut, boundary.jobPath("build", "/unpause")
	case strings.Contains(profile, "pause-"):
		method, path = http.MethodPut, boundary.jobPath("build", "/pause")
	case strings.Contains(profile, "cache-"):
		method, path = http.MethodDelete, boundary.jobPath("build", "/tasks/compile/cache")
	case strings.Contains(profile, "schedule-"):
		method, path = http.MethodPut, boundary.jobPath("build", "/schedule")
	}
	request, err := http.NewRequestWithContext(context.Background(), method, boundary.url+path, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	observation.Status = response.StatusCode
	observation.Body = body
	if response.StatusCode != wantStatus {
		return fmt.Errorf("job authorization status got %d, want %d (body %q)", response.StatusCode, wantStatus, string(body))
	}
	return nil
}

func (boundary *strictJobBoundary) createBuilds(observation *JobStrictObservation, count int) error {
	for range count {
		build, err := boundary.job.CreateBuild(jobStrictUserID)
		if err != nil {
			return err
		}
		observation.CreatedBuildIDs = append(observation.CreatedBuildIDs, build.ID())
	}
	return nil
}

func (boundary *strictJobBoundary) observeClientMutation(observation *JobStrictObservation) error {
	missing := strings.HasSuffix(observation.Profile, "-missing")
	name := "build"
	if missing {
		name = "missing"
	}
	if observation.Profile == "client-unpause-existing" {
		if err := boundary.job.Pause("before-unpause"); err != nil {
			return err
		}
		found, err := boundary.job.Reload()
		if err != nil || !found {
			return strictJobFirstError(err, fmt.Errorf("job disappeared before unpause"))
		}
	}
	before := boundary.job.ScheduleRequestedTime()
	switch {
	case strings.Contains(observation.Profile, "pause-") && !strings.Contains(observation.Profile, "unpause-"):
		observation.Found, observation.Err = boundary.clientTeam.PauseJob(boundary.ref, name)
	case strings.Contains(observation.Profile, "unpause-"):
		observation.Found, observation.Err = boundary.clientTeam.UnpauseJob(boundary.ref, name)
	case strings.Contains(observation.Profile, "schedule-"):
		observation.Found, observation.Err = boundary.clientTeam.ScheduleJob(boundary.ref, name)
	}
	if observation.Err != nil || !observation.Found || missing {
		return nil
	}
	found, err := boundary.job.Reload()
	if err != nil || !found {
		return strictJobFirstError(err, fmt.Errorf("job disappeared after mutation"))
	}
	observation.Paused, observation.PausedBy = boundary.job.Paused(), boundary.job.PausedBy()
	observation.ScheduleAdvanced = boundary.job.ScheduleRequestedTime().After(before)
	return nil
}

func (boundary *strictJobBoundary) observeRawAPI(observation *JobStrictObservation) error {
	if observation.Profile == "api-list-admin" {
		other, err := boundary.database.TeamFactory.CreateTeam(atc.Team{Name: "other-job-team"})
		if err != nil {
			return err
		}
		if _, _, err := other.SavePipeline(atc.PipelineRef{Name: "other"}, atc.Config{Jobs: atc.JobConfigs{{Name: "other-build"}}}, 0, false); err != nil {
			return err
		}
	}
	if observation.Profile == "api-unpause-existing" {
		if err := boundary.job.Pause("before-unpause"); err != nil {
			return err
		}
	}
	before := boundary.job.ScheduleRequestedTime()
	method, path := http.MethodGet, "/api/v1/jobs"
	switch observation.Profile {
	case "api-get-missing":
		path = boundary.jobPath("missing", "")
	case "api-builds-missing":
		path = boundary.jobPath("missing", "/builds")
	case "api-pause-existing":
		method, path = http.MethodPut, boundary.jobPath("build", "/pause")
	case "api-pause-missing":
		method, path = http.MethodPut, boundary.jobPath("missing", "/pause")
	case "api-unpause-existing":
		method, path = http.MethodPut, boundary.jobPath("build", "/unpause")
	case "api-unpause-missing":
		method, path = http.MethodPut, boundary.jobPath("missing", "/unpause")
	case "api-schedule-existing":
		method, path = http.MethodPut, boundary.jobPath("build", "/schedule")
	case "api-schedule-missing":
		method, path = http.MethodPut, boundary.jobPath("missing", "/schedule")
	}
	request, err := http.NewRequest(method, boundary.url+path, nil)
	if err != nil {
		return err
	}
	response, err := boundary.httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	observation.Status = response.StatusCode
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	observation.Body = body
	if observation.Profile == "api-list-admin" {
		var jobs []atc.Job
		if err := json.Unmarshal(body, &jobs); err != nil {
			return err
		}
		observation.Names = strictJobNames(jobs)
	}
	if strings.HasSuffix(observation.Profile, "-existing") {
		found, err := boundary.job.Reload()
		if err != nil || !found {
			return strictJobFirstError(err, fmt.Errorf("job disappeared after raw API mutation"))
		}
		observation.Paused, observation.PausedBy = boundary.job.Paused(), boundary.job.PausedBy()
		observation.ScheduleAdvanced = boundary.job.ScheduleRequestedTime().After(before)
	}
	return nil
}

func (boundary *strictJobBoundary) jobPath(name, suffix string) string {
	return fmt.Sprintf("/api/v1/teams/%s/pipelines/%s/jobs/%s%s?%s",
		jobStrictTeamName, boundary.ref.Name, name, suffix, boundary.ref.QueryParams().Encode())
}

func strictJobPage(profile string, ids []int) (clientapi.Page, error) {
	if len(ids) != 3 {
		return clientapi.Page{}, fmt.Errorf("strict job page requires three persisted builds")
	}
	switch profile {
	case "client-builds-all", "client-pagination-empty":
		return clientapi.Page{}, nil
	case "client-builds-from":
		return clientapi.Page{From: ids[1]}, nil
	case "client-builds-from-limit":
		return clientapi.Page{From: ids[0], Limit: 1}, nil
	case "client-builds-to":
		return clientapi.Page{To: ids[1]}, nil
	case "client-builds-to-limit":
		return clientapi.Page{To: ids[1], Limit: 1}, nil
	case "client-builds-from-to":
		return clientapi.Page{From: ids[0], To: ids[1]}, nil
	case "client-pagination-links":
		return clientapi.Page{Limit: 1}, nil
	default:
		return clientapi.Page{}, fmt.Errorf("unknown strict job page profile %q", profile)
	}
}

func validateStrictJobObservation(in JobStrictObservation) error {
	if in.Err != nil {
		return fmt.Errorf("production job observation %q returned error: %w", in.Profile, in.Err)
	}
	switch in.Profile {
	case "client-list-pipeline":
		return strictJobWantNames(in, "build")
	case "client-list-all":
		return strictJobWantNames(in, "build", "missing")
	case "client-get-existing":
		if !in.Found {
			return fmt.Errorf("existing production job was not found")
		}
		return strictJobWantNames(in, "build")
	case "client-get-missing", "client-builds-missing",
		"client-pause-missing", "client-unpause-missing", "client-schedule-missing":
		if in.Found {
			return fmt.Errorf("missing production job was reported found")
		}
		return nil
	case "client-builds-all", "client-pagination-empty":
		return strictJobWantBuilds(in, []int{in.CreatedBuildIDs[2], in.CreatedBuildIDs[1], in.CreatedBuildIDs[0]}, false, false)
	case "client-builds-from":
		return strictJobWantBuilds(in, []int{in.CreatedBuildIDs[2], in.CreatedBuildIDs[1]}, false, true)
	case "client-builds-from-limit":
		return strictJobWantBuilds(in, []int{in.CreatedBuildIDs[0]}, true, false)
	case "client-builds-to":
		return strictJobWantBuilds(in, []int{in.CreatedBuildIDs[1], in.CreatedBuildIDs[0]}, true, false)
	case "client-builds-to-limit":
		return strictJobWantBuilds(in, []int{in.CreatedBuildIDs[1]}, true, true)
	case "client-builds-from-to":
		return strictJobWantBuilds(in, []int{in.CreatedBuildIDs[0], in.CreatedBuildIDs[1]}, true, true)
	case "client-pagination-links":
		return strictJobWantBuilds(in, []int{in.CreatedBuildIDs[2]}, false, true)
	case "client-pause-existing":
		return strictJobWantMutation(in, true, true, false)
	case "client-unpause-existing":
		if !in.Found || in.Paused {
			return fmt.Errorf("production client unpause: found=%t paused=%t", in.Found, in.Paused)
		}
		return nil
	case "client-schedule-existing":
		return strictJobWantMutation(in, true, false, true)
	case "api-list-admin":
		if in.Status != http.StatusOK {
			return fmt.Errorf("admin job listing status: got %d, want 200", in.Status)
		}
		return strictJobWantNames(in, "build", "missing", "other-build")
	case "api-get-missing", "api-builds-missing", "api-pause-missing", "api-unpause-missing", "api-schedule-missing":
		if in.Status != http.StatusNotFound {
			return fmt.Errorf("missing job API status: got %d, want 404", in.Status)
		}
		return strictJobWantEmptyBody(in)
	case "api-pause-existing":
		if err := strictJobWantAPIMutation(in, true, false); err != nil {
			return err
		}
		return strictJobWantEmptyBody(in)
	case "api-unpause-existing":
		if in.Status != http.StatusOK || in.Paused {
			return fmt.Errorf("production job API unpause: status=%d paused=%t", in.Status, in.Paused)
		}
		return strictJobWantEmptyBody(in)
	case "api-schedule-existing":
		if err := strictJobWantAPIMutation(in, false, true); err != nil {
			return err
		}
		return strictJobWantEmptyBody(in)
	default:
		return fmt.Errorf("no validator for strict job profile %q", in.Profile)
	}
}

func strictJobWantNames(in JobStrictObservation, expected ...string) error {
	sort.Strings(in.Names)
	sort.Strings(expected)
	if strings.Join(in.Names, ",") != strings.Join(expected, ",") {
		return fmt.Errorf("production job names: got %v, want %v", in.Names, expected)
	}
	return nil
}

func strictJobWantBuilds(in JobStrictObservation, ids []int, previous, next bool) error {
	if !in.Found || fmt.Sprint(in.BuildIDs) != fmt.Sprint(ids) {
		return fmt.Errorf("production job builds: found=%t ids=%v, want ids=%v", in.Found, in.BuildIDs, ids)
	}
	if (in.Previous != nil) != previous || (in.Next != nil) != next {
		return fmt.Errorf("production pagination: previous=%v next=%v, want nonnil=%t/%t", in.Previous, in.Next, previous, next)
	}
	return nil
}

func strictJobWantMutation(in JobStrictObservation, pausedFound, paused, scheduled bool) error {
	if in.Found != pausedFound || in.Paused != paused || in.ScheduleAdvanced != scheduled {
		return fmt.Errorf("production client mutation: found=%t paused=%t schedule-advanced=%t", in.Found, in.Paused, in.ScheduleAdvanced)
	}
	if paused && in.PausedBy != jobStrictUserID {
		return fmt.Errorf("production pause user: got %q, want %q", in.PausedBy, jobStrictUserID)
	}
	return nil
}

func strictJobWantAPIMutation(in JobStrictObservation, paused, scheduled bool) error {
	if in.Status != http.StatusOK || in.Paused != paused || in.ScheduleAdvanced != scheduled {
		return fmt.Errorf("production job API mutation: status=%d paused=%t schedule-advanced=%t", in.Status, in.Paused, in.ScheduleAdvanced)
	}
	if paused && in.PausedBy != jobStrictUserID {
		return fmt.Errorf("production API pause user: got %q, want %q", in.PausedBy, jobStrictUserID)
	}
	return nil
}

func strictJobWantEmptyBody(in JobStrictObservation) error {
	if len(in.Body) != 0 {
		return fmt.Errorf("production job API body: got %q, want empty", in.Body)
	}
	return nil
}

func strictJobNames(jobs []atc.Job) []string {
	names := make([]string, 0, len(jobs))
	for _, job := range jobs {
		names = append(names, job.Name)
	}
	return names
}

func strictJobFirstError(err error, fallback error) error {
	if err != nil {
		return err
	}
	return fallback
}

// firstError and jobClientPage remain shared by the older build/domain
// definitions; the strict job cohort itself uses the profile-specific helpers
// above.
func firstError(err error, fallback error) error {
	return strictJobFirstError(err, fallback)
}

func jobClientPage(profile string, ids []int) (clientapi.Page, error) {
	if profile == "all" {
		return clientapi.Page{}, nil
	}
	if len(ids) < 3 {
		return clientapi.Page{}, fmt.Errorf("page profile %q needs three builds", profile)
	}
	switch profile {
	case "from":
		return clientapi.Page{From: ids[0]}, nil
	case "from-limit":
		return clientapi.Page{From: ids[0], Limit: 1}, nil
	case "to":
		return clientapi.Page{To: ids[2]}, nil
	case "to-limit":
		return clientapi.Page{To: ids[2], Limit: 1}, nil
	case "from-to":
		return clientapi.Page{From: ids[0], To: ids[2]}, nil
	default:
		return clientapi.Page{}, fmt.Errorf("unknown job page profile %q", profile)
	}
}
