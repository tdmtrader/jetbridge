package steps

import (
	"context"
	"encoding/json"
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
	"github.com/concourse/concourse/atc/api/auth"
	"github.com/concourse/concourse/atc/api/jobserver"
	"github.com/concourse/concourse/atc/api/pipelineserver"
	"github.com/concourse/concourse/atc/auditor"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/wrappa"
	"github.com/concourse/concourse/skymarshal/skycmd"
	"github.com/tedsuo/rata"
	"golang.org/x/oauth2"
)

const (
	clearTaskCacheTeam      = "clear-cache-team"
	clearTaskCachePipeline  = "clear-cache-pipeline"
	clearTaskCacheJob       = "job-name"
	clearTaskCacheOtherJob  = "other-job"
	clearTaskCacheAudience  = "clear-cache-audience"
	clearTaskCacheConnector = "clear-cache-connector"
	clearTaskCacheUser      = "clear-cache-user"
)

type clearTaskCacheAPIObservation struct {
	Profile     string
	Status      int
	ContentType string
	Removed     int64
	TargetPath  *bool
	OtherPath   *bool
	OtherJob    *bool
}

func ClearTaskCacheAPIStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, *clearTaskCacheAPIObservation](
			"the production ClearTaskCache API executes profile {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, rec *brine.Recorder, resources brine.Resources) (*clearTaskCacheAPIObservation, error) {
				profile, ok := p.GetString(0)
				if !ok {
					return nil, fmt.Errorf("expected ClearTaskCache profile")
				}
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return nil, fmt.Errorf("jetbridge-db resource has type %T", resources.Get("jetbridge-db"))
				}
				return runClearTaskCacheAPIProfile(database, profile, rec)
			},
		),
		brine.DefineMap[*clearTaskCacheAPIObservation, *clearTaskCacheAPIObservation](
			"the ClearTaskCache API observation exactly matches profile {string}",
			func(observation *clearTaskCacheAPIObservation, p brine.Params, _ *brine.Recorder) (*clearTaskCacheAPIObservation, error) {
				profile, ok := p.GetString(0)
				if !ok {
					return observation, fmt.Errorf("expected ClearTaskCache profile")
				}
				if observation.Profile != profile {
					return observation, fmt.Errorf("executed profile %q, asserted profile %q", observation.Profile, profile)
				}
				if err := assertClearTaskCacheAPIObservation(observation); err != nil {
					return observation, err
				}
				return observation, nil
			},
		),
	}
}

func runClearTaskCacheAPIProfile(database JetbridgeDB, profile string, rec *brine.Recorder) (*clearTaskCacheAPIObservation, error) {
	logger := lager.NewLogger("brine-clear-task-cache-api")
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: clearTaskCacheTeam})
	if err != nil {
		return nil, fmt.Errorf("create ClearTaskCache team: %w", err)
	}
	if err := team.UpdateProviderAuth(atc.TeamAuth{
		accessor.OwnerRole: {"users": {clearTaskCacheConnector + ":" + clearTaskCacheUser}},
	}); err != nil {
		return nil, fmt.Errorf("authorize ClearTaskCache user: %w", err)
	}

	jobs := atc.JobConfigs{{Name: clearTaskCacheOtherJob}}
	if profile != "missing-job" {
		jobs = append(atc.JobConfigs{{Name: clearTaskCacheJob}}, jobs...)
	}
	pipeline, _, err := team.SavePipeline(
		atc.PipelineRef{Name: clearTaskCachePipeline},
		atc.Config{Jobs: jobs},
		db.ConfigVersion(0),
		false,
	)
	if err != nil {
		return nil, fmt.Errorf("save ClearTaskCache pipeline: %w", err)
	}

	var taskCaches db.TaskCacheFactory
	var job db.Job
	var otherJob db.Job
	if profile != "missing-job" {
		taskCaches = db.NewTaskCacheFactory(database.Conn)
		job, err = clearTaskCachePipelineJob(pipeline, clearTaskCacheJob)
		if err != nil {
			return nil, err
		}
		otherJob, err = clearTaskCachePipelineJob(pipeline, clearTaskCacheOtherJob)
		if err != nil {
			return nil, err
		}
		for _, cache := range []struct {
			jobID int
			path  string
		}{
			{job.ID(), "cache-path"},
			{job.ID(), "other-path"},
			{otherJob.ID(), "cache-path"},
		} {
			if _, err := taskCaches.FindOrCreate(cache.jobID, "compile", cache.path); err != nil {
				return nil, fmt.Errorf("seed task cache for job %d path %q: %w", cache.jobID, cache.path, err)
			}
		}
	}

	client, baseURL, err := startClearTaskCacheAPIServer(database, logger, rec)
	if err != nil {
		return nil, err
	}
	requestPath := fmt.Sprintf(
		"/api/v1/teams/%s/pipelines/%s/jobs/%s/tasks/compile/cache",
		clearTaskCacheTeam,
		clearTaskCachePipeline,
		clearTaskCacheJob,
	)
	switch profile {
	case "all-step-caches", "missing-job":
	case "selected-cache-path":
		requestPath += "?" + url.Values{atc.ClearTaskCacheQueryPath: {"cache-path"}}.Encode()
	case "missing-cache-path":
		requestPath += "?" + url.Values{atc.ClearTaskCacheQueryPath: {"missing"}}.Encode()
	case "missing-step":
		requestPath = fmt.Sprintf(
			"/api/v1/teams/%s/pipelines/%s/jobs/%s/tasks/missing-step/cache",
			clearTaskCacheTeam,
			clearTaskCachePipeline,
			clearTaskCacheJob,
		)
	default:
		return nil, fmt.Errorf("unknown ClearTaskCache profile %q", profile)
	}

	request, err := http.NewRequestWithContext(context.Background(), http.MethodDelete, baseURL+requestPath, nil)
	if err != nil {
		return nil, fmt.Errorf("create ClearTaskCache request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("execute ClearTaskCache request: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, fmt.Errorf("read ClearTaskCache response: %w", err)
	}

	observation := &clearTaskCacheAPIObservation{
		Profile: profile, Status: response.StatusCode, ContentType: response.Header.Get("Content-Type"),
	}
	if profile != "missing-job" {
		var payload atc.ClearTaskCacheResponse
		if err := json.Unmarshal(body, &payload); err != nil {
			return observation, fmt.Errorf("decode ClearTaskCache response %q: %w", string(body), err)
		}
		observation.Removed = payload.CachesRemoved
	}

	switch profile {
	case "all-step-caches", "selected-cache-path", "missing-step":
		targetPath, err := clearTaskCacheExists(taskCaches, job.ID(), "compile", "cache-path")
		if err != nil {
			return observation, err
		}
		otherPath, err := clearTaskCacheExists(taskCaches, job.ID(), "compile", "other-path")
		if err != nil {
			return observation, err
		}
		otherJobPath, err := clearTaskCacheExists(taskCaches, otherJob.ID(), "compile", "cache-path")
		if err != nil {
			return observation, err
		}
		observation.TargetPath = &targetPath
		observation.OtherPath = &otherPath
		observation.OtherJob = &otherJobPath
	}
	return observation, nil
}

func startClearTaskCacheAPIServer(database JetbridgeDB, logger lager.Logger, rec *brine.Recorder) (*http.Client, string, error) {
	const token = "clear-task-cache-token"
	claimsJSON, err := json.Marshal(map[string]any{
		"sub":                "clear-task-cache-subject",
		"aud":                []any{clearTaskCacheAudience},
		"exp":                time.Now().Add(time.Hour).Unix(),
		"name":               "Clear Task Cache User",
		"preferred_username": clearTaskCacheUser,
		"federated_claims": map[string]any{
			"connector_id": clearTaskCacheConnector,
			"user_id":      clearTaskCacheUser,
		},
	})
	if err != nil {
		return nil, "", err
	}
	var claims db.Claims
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		return nil, "", err
	}
	if err := db.NewAccessTokenFactory(database.Conn).CreateAccessToken(token, claims); err != nil {
		return nil, "", fmt.Errorf("persist ClearTaskCache access token: %w", err)
	}
	display, err := skycmd.NewSkyDisplayUserIdGenerator(map[string]string{})
	if err != nil {
		return nil, "", err
	}
	accessFactory := accessor.NewAccessFactory(
		accessor.NewVerifier(db.NewAccessTokenFactory(database.Conn), []string{clearTaskCacheAudience}),
		database.TeamFactory,
		"sub",
		[]string{"clear-task-cache-system"},
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
	jobServer := jobserver.NewServer(
		logger,
		"https://concourse.invalid",
		nil,
		db.NewJobFactory(database.Conn, database.LockFactory),
		nil,
	)
	handlers := rata.Handlers{
		atc.ClearTaskCache: pipelineserver.NewScopedHandlerFactory(database.TeamFactory).HandlerFor(jobServer.ClearTaskCache),
	}
	var routes rata.Routes
	for _, route := range atc.Routes {
		if _, found := handlers[route.Name]; found {
			routes = append(routes, route)
		}
	}
	router, err := rata.NewRouter(routes, wrapper.Wrap(handlers))
	if err != nil {
		return nil, "", fmt.Errorf("build ClearTaskCache router: %w", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, "", fmt.Errorf("listen for ClearTaskCache API: %w", err)
	}
	server := &http.Server{Handler: router, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = server.Serve(listener) }()
	client := oauth2.NewClient(
		context.Background(),
		oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token, TokenType: "Bearer"}),
	)
	client.Timeout = 30 * time.Second
	rec.RegisterDisposer(func() {
		client.CloseIdleConnections()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			_ = server.Close()
		}
	})
	return client, "http://" + listener.Addr().String(), nil
}

func clearTaskCachePipelineJob(pipeline db.Pipeline, name string) (db.Job, error) {
	job, found, err := pipeline.Job(name)
	if err != nil {
		return nil, fmt.Errorf("load job %q: %w", name, err)
	}
	if !found {
		return nil, fmt.Errorf("configured job %q was not persisted", name)
	}
	return job, nil
}

func clearTaskCacheExists(factory db.TaskCacheFactory, jobID int, step, path string) (bool, error) {
	_, found, err := factory.Find(jobID, step, path)
	if err != nil {
		return false, fmt.Errorf("find task cache for job %d step %q path %q: %w", jobID, step, path, err)
	}
	return found, nil
}

func assertClearTaskCacheAPIObservation(observation *clearTaskCacheAPIObservation) error {
	expect := func(condition bool, format string, args ...any) error {
		if !condition {
			return fmt.Errorf(format, args...)
		}
		return nil
	}
	if observation.Profile == "missing-job" {
		return expect(observation.Status == http.StatusNotFound, "missing job status = %d, want 404", observation.Status)
	}
	if err := expect(observation.Status == http.StatusOK, "%s status = %d, want 200", observation.Profile, observation.Status); err != nil {
		return err
	}
	switch observation.Profile {
	case "all-step-caches":
		if err := expect(observation.ContentType == "application/json", "content type = %q, want application/json", observation.ContentType); err != nil {
			return err
		}
		if err := expect(observation.Removed == 2, "removed = %d, want 2", observation.Removed); err != nil {
			return err
		}
		if err := expect(observation.TargetPath != nil && !*observation.TargetPath, "target cache still exists"); err != nil {
			return err
		}
		if err := expect(observation.OtherPath != nil && !*observation.OtherPath, "other matching cache still exists"); err != nil {
			return err
		}
		return expect(observation.OtherJob != nil && *observation.OtherJob, "another job's cache was removed")
	case "selected-cache-path":
		if err := expect(observation.Removed == 1, "removed = %d, want 1", observation.Removed); err != nil {
			return err
		}
		if err := expect(observation.TargetPath != nil && !*observation.TargetPath, "selected cache still exists"); err != nil {
			return err
		}
		if err := expect(observation.OtherPath != nil && *observation.OtherPath, "same-job decoy cache was removed"); err != nil {
			return err
		}
		return expect(observation.OtherJob != nil && *observation.OtherJob, "other-job decoy cache was removed")
	case "missing-cache-path":
		return expect(observation.Removed == 0, "removed = %d, want 0", observation.Removed)
	case "missing-step":
		if err := expect(observation.Removed == 0, "removed = %d, want 0", observation.Removed); err != nil {
			return err
		}
		if err := expect(observation.TargetPath != nil && *observation.TargetPath, "target cache was removed for missing step"); err != nil {
			return err
		}
		if err := expect(observation.OtherPath != nil && *observation.OtherPath, "other-path cache was removed for missing step"); err != nil {
			return err
		}
		return expect(observation.OtherJob != nil && *observation.OtherJob, "other-job cache was removed for missing step")
	default:
		return fmt.Errorf("unknown ClearTaskCache profile %q", observation.Profile)
	}
}
