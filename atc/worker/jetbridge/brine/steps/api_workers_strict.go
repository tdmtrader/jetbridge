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
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/auth"
	"github.com/concourse/concourse/atc/api/workerserver"
	"github.com/concourse/concourse/atc/auditor"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/wrappa"
	"github.com/concourse/concourse/skymarshal/skycmd"
	"github.com/tedsuo/rata"
	"golang.org/x/oauth2"
)

const (
	workerStrictAudience  = "worker-api-audience"
	workerStrictConnector = "worker-api-connector"
	workerStrictTeam      = "some-team"
	workerStrictOtherTeam = "other-team"
)

type APIWorkersStrictObservation struct {
	Profile string
	Failure string
}

type apiWorkersBoundary struct {
	database JetbridgeDB
	team     db.Team
	other    db.Team
	owner    *http.Client
	admin    *http.Client
	system   *http.Client
	regular  *http.Client
	public   *http.Client
	url      string
}

func APIWorkersStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, APIWorkersStrictObservation](
			"the production workers API executes profile {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, rec *brine.Recorder, resources brine.Resources) (APIWorkersStrictObservation, error) {
				profile, ok := p.GetString(0)
				if !ok {
					return APIWorkersStrictObservation{}, fmt.Errorf("expected workers API profile")
				}
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return APIWorkersStrictObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				boundary, err := newAPIWorkersBoundary(database, rec)
				if err != nil {
					return APIWorkersStrictObservation{}, err
				}
				return APIWorkersStrictObservation{Profile: profile, Failure: boundary.observe(profile)}, nil
			},
		),
		brine.DefineCheck[APIWorkersStrictObservation](
			"the workers API observation exactly matches profile {string}",
			func(observation APIWorkersStrictObservation, p brine.Params, _ *brine.Recorder) error {
				profile, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected workers API assertion profile")
				}
				if profile != observation.Profile {
					return fmt.Errorf("executed profile %q, asserted %q", observation.Profile, profile)
				}
				if observation.Failure != "" {
					return fmt.Errorf("workers API observation: %s", observation.Failure)
				}
				return nil
			},
		),
	}
}

func newAPIWorkersBoundary(database JetbridgeDB, rec *brine.Recorder) (*apiWorkersBoundary, error) {
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: workerStrictTeam})
	if err != nil {
		return nil, err
	}
	other, err := database.TeamFactory.CreateTeam(atc.Team{Name: workerStrictOtherTeam})
	if err != nil {
		return nil, err
	}
	if err := team.UpdateProviderAuth(atc.TeamAuth{
		accessor.OwnerRole: {"users": {workerStrictConnector + ":owner"}},
	}); err != nil {
		return nil, err
	}
	adminTeam, err := database.TeamFactory.CreateDefaultTeamIfNotExists()
	if err != nil {
		return nil, err
	}
	if err := adminTeam.UpdateProviderAuth(atc.TeamAuth{
		accessor.OwnerRole: {"users": {workerStrictConnector + ":admin"}},
	}); err != nil {
		return nil, err
	}
	owner, err := strictWorkerTokenClient(database, "owner")
	if err != nil {
		return nil, err
	}
	admin, err := strictWorkerTokenClient(database, "admin")
	if err != nil {
		return nil, err
	}
	system, err := strictWorkerTokenClient(database, "tsa")
	if err != nil {
		return nil, err
	}
	regular, err := strictWorkerTokenClient(database, "regular")
	if err != nil {
		return nil, err
	}
	public := &http.Client{Timeout: 30 * time.Second}
	url, err := startAPIWorkersServer(database, rec, owner, admin, system, regular, public)
	if err != nil {
		return nil, err
	}
	return &apiWorkersBoundary{
		database: database, team: team, other: other,
		owner: owner, admin: admin, system: system, regular: regular, public: public, url: url,
	}, nil
}

func strictWorkerTokenClient(database JetbridgeDB, user string) (*http.Client, error) {
	token := "worker-api-token-" + user
	payload, err := json.Marshal(map[string]any{
		"sub": user, "preferred_username": user, "aud": []any{workerStrictAudience},
		"exp":              time.Now().Add(time.Hour).Unix(),
		"federated_claims": map[string]any{"connector_id": workerStrictConnector, "user_id": user},
	})
	if err != nil {
		return nil, err
	}
	var claims db.Claims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, err
	}
	if err := db.NewAccessTokenFactory(database.Conn).CreateAccessToken(token, claims); err != nil {
		return nil, err
	}
	client := oauth2.NewClient(context.Background(), oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token, TokenType: "Bearer"}))
	client.Timeout = 30 * time.Second
	return client, nil
}

func startAPIWorkersServer(database JetbridgeDB, rec *brine.Recorder, clients ...*http.Client) (string, error) {
	logger := lager.NewLogger("brine-api-workers-strict")
	display, err := skycmd.NewSkyDisplayUserIdGenerator(map[string]string{})
	if err != nil {
		return "", err
	}
	accessFactory := accessor.NewAccessFactory(
		accessor.NewVerifier(db.NewAccessTokenFactory(database.Conn), []string{workerStrictAudience}),
		database.TeamFactory, "sub", []string{"tsa"}, display,
	)
	wrapper := wrappa.MultiWrappa{
		wrappa.NewAPIAuthWrappa(
			auth.NewCheckPipelineAccessHandlerFactory(database.TeamFactory),
			auth.NewCheckBuildReadAccessHandlerFactory(database.BuildFactory),
			auth.NewCheckBuildWriteAccessHandlerFactory(database.BuildFactory),
			auth.NewCheckWorkerTeamAccessHandlerFactory(database.WorkerFactory),
		),
		wrappa.NewAccessorWrappa(logger, accessFactory,
			auditor.NewAuditor(false, false, false, false, false, true, false, false, false, logger), map[string]string{}),
	}
	server := workerserver.NewServer(logger, database.TeamFactory, database.WorkerFactory)
	handlers := rata.Handlers{
		atc.ListWorkers:    http.HandlerFunc(server.ListWorkers),
		atc.RegisterWorker: http.HandlerFunc(server.RegisterWorker),
		atc.DeleteWorker:   http.HandlerFunc(server.DeleteWorker),
	}
	var routes rata.Routes
	for _, route := range atc.Routes {
		if _, ok := handlers[route.Name]; ok {
			routes = append(routes, route)
		}
	}
	router, err := rata.NewRouter(routes, wrapper.Wrap(handlers))
	if err != nil {
		return "", err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	httpServer := &http.Server{Handler: router, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = httpServer.Serve(listener) }()
	rec.RegisterDisposer(func() {
		for _, client := range clients {
			client.CloseIdleConnections()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(ctx); err != nil {
			_ = httpServer.Close()
		}
	})
	return "http://" + listener.Addr().String(), nil
}

func strictAPIWorker() atc.Worker {
	return atc.Worker{
		Name: "worker-name", ActiveContainers: 2, ActiveVolumes: 10, ActiveTasks: 42,
		ResourceTypes: []atc.WorkerResourceType{{Type: "some-resource", Image: "some-resource-image"}},
		Platform:      "haiku", Tags: []string{"not", "a", "limerick"}, Version: "1.2.3",
	}
}

func (b *apiWorkersBoundary) request(client *http.Client, method, path string, body io.Reader) (*http.Response, []byte, error) {
	request, err := http.NewRequestWithContext(context.Background(), method, b.url+path, body)
	if err != nil {
		return nil, nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, nil, err
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	return response, payload, err
}

func (b *apiWorkersBoundary) observe(profile string) string {
	var err error
	switch {
	case profile == "list-visible" || profile == "list-admin" || profile == "list-unauthenticated":
		err = b.observeList(profile)
	case profile == "register-global" || profile == "register-team" || profile == "register-missing-team" || profile == "register-not-system" || profile == "register-empty-name" || profile == "register-invalid-ttl" || profile == "register-invalid-version" || profile == "register-unauthenticated":
		err = b.observeRegister(profile)
	case profile == "delete-system" || profile == "delete-admin" || profile == "delete-team" || profile == "delete-missing":
		err = b.observeDelete(profile)
	default:
		err = fmt.Errorf("unknown workers API profile %q", profile)
	}
	if err != nil {
		return err.Error()
	}
	return ""
}

func (b *apiWorkersBoundary) observeList(profile string) error {
	global, err := b.database.WorkerFactory.SaveWorker(atc.Worker{Name: "global-worker", Platform: "linux", Version: "1.2.3", State: string(db.WorkerStateRunning), Tags: []string{"global"}}, 0)
	if err != nil {
		return err
	}
	own, err := b.team.SaveWorker(atc.Worker{Name: "some-team-worker", Platform: "linux", Version: "1.2.3", State: string(db.WorkerStateRunning), Tags: []string{"own"}}, 0)
	if err != nil {
		return err
	}
	other, err := b.other.SaveWorker(atc.Worker{Name: "other-team-worker", Platform: "linux", Version: "1.2.3", State: string(db.WorkerStateRunning), Tags: []string{"other"}}, 0)
	if err != nil {
		return err
	}
	client := b.owner
	want := []string{global.Name(), own.Name()}
	if profile == "list-admin" {
		client, want = b.admin, []string{global.Name(), own.Name(), other.Name()}
	}
	if profile == "list-unauthenticated" {
		client = b.public
	}
	response, payload, err := b.request(client, http.MethodGet, "/api/v1/workers", nil)
	if err != nil {
		return err
	}
	if profile == "list-unauthenticated" {
		if response.StatusCode != http.StatusUnauthorized {
			return fmt.Errorf("unauthenticated worker list status=%d", response.StatusCode)
		}
		return nil
	}
	var workers []atc.Worker
	if err := json.Unmarshal(payload, &workers); err != nil {
		return err
	}
	names := make([]string, len(workers))
	for i := range workers {
		names[i] = workers[i].Name
	}
	sort.Strings(names)
	sort.Strings(want)
	if profile == "list-visible" && (response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "application/json") {
		return fmt.Errorf("worker list status=%d content-type=%q", response.StatusCode, response.Header.Get("Content-Type"))
	}
	if !reflect.DeepEqual(names, want) {
		return fmt.Errorf("worker list names=%v want=%v", names, want)
	}
	return nil
}

func (b *apiWorkersBoundary) observeRegister(profile string) error {
	worker := strictAPIWorker()
	ttl := "30s"
	client := b.system
	wantStatus := http.StatusOK
	var wantBody string
	if profile == "register-team" {
		worker.Team = workerStrictTeam
	}
	if profile == "register-missing-team" {
		worker.Team, wantStatus = "missing-team", http.StatusBadRequest
	}
	if profile == "register-not-system" {
		client, wantStatus = b.regular, http.StatusForbidden
	}
	if profile == "register-empty-name" {
		worker.Name, wantStatus = "", http.StatusBadRequest
	}
	if profile == "register-invalid-ttl" {
		ttl, wantStatus, wantBody = "invalid-duration", http.StatusBadRequest, "malformed ttl"
	}
	if profile == "register-invalid-version" {
		worker.Version, wantStatus, wantBody = "invalid", http.StatusBadRequest, "invalid worker version, only numeric characters are allowed"
	}
	if profile == "register-unauthenticated" {
		client, wantStatus = b.public, http.StatusUnauthorized
	}
	var requestedAt time.Time
	if err := b.database.Conn.QueryRow("SELECT NOW()").Scan(&requestedAt); err != nil {
		return err
	}
	payload, err := json.Marshal(worker)
	if err != nil {
		return err
	}
	response, body, err := b.request(client, http.MethodPost, "/api/v1/workers?ttl="+ttl, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	var respondedAt time.Time
	if err := b.database.Conn.QueryRow("SELECT NOW()").Scan(&respondedAt); err != nil {
		return err
	}
	if response.StatusCode != wantStatus || (wantBody != "" && string(body) != wantBody) {
		return fmt.Errorf("register status=%d body=%q want=%d/%q", response.StatusCode, body, wantStatus, wantBody)
	}
	lookupName := worker.Name
	if lookupName == "" {
		lookupName = "worker-name"
	}
	registered, found, err := b.database.WorkerFactory.GetWorker(lookupName)
	if err != nil {
		return err
	}
	if wantStatus != http.StatusOK {
		if found || registered != nil {
			return fmt.Errorf("rejected worker persisted: found=%t worker=%v", found, registered)
		}
		return nil
	}
	if !found || registered == nil {
		return fmt.Errorf("registered worker not found")
	}
	if registered.Name() != worker.Name || registered.ActiveContainers() != worker.ActiveContainers || registered.ActiveVolumes() != worker.ActiveVolumes || !reflect.DeepEqual(registered.ResourceTypes(), worker.ResourceTypes) || registered.Platform() != worker.Platform || !reflect.DeepEqual(registered.Tags(), []string(worker.Tags)) || registered.Version() == nil || *registered.Version() != worker.Version || registered.ExpiresAt().Before(requestedAt.Add(30*time.Second)) || registered.ExpiresAt().After(respondedAt.Add(30*time.Second)) {
		return fmt.Errorf("persisted worker fields differ: name=%q team=%q id=%d expires=%s", registered.Name(), registered.TeamName(), registered.TeamID(), registered.ExpiresAt())
	}
	if profile == "register-global" && registered.TeamName() != "" {
		return fmt.Errorf("global registration team=%q", registered.TeamName())
	}
	if profile == "register-team" && (registered.TeamName() != workerStrictTeam || registered.TeamID() != b.team.ID()) {
		return fmt.Errorf("team registration team=%q id=%d want=%q/%d", registered.TeamName(), registered.TeamID(), workerStrictTeam, b.team.ID())
	}
	return nil
}

func (b *apiWorkersBoundary) observeDelete(profile string) error {
	client := b.system
	wantStatus := http.StatusOK
	if profile != "delete-missing" {
		worker := atc.Worker{Name: "some-worker", Version: "1.2.3"}
		var err error
		if profile == "delete-team" {
			_, err = b.team.SaveWorker(worker, 0)
			client = b.owner
		} else {
			_, err = b.database.WorkerFactory.SaveWorker(worker, 0)
			if profile == "delete-admin" {
				client = b.admin
			}
		}
		if err != nil {
			return err
		}
	} else {
		wantStatus = http.StatusInternalServerError
	}
	response, _, err := b.request(client, http.MethodDelete, "/api/v1/workers/some-worker", nil)
	if err != nil {
		return err
	}
	if response.StatusCode != wantStatus {
		return fmt.Errorf("delete status=%d want=%d", response.StatusCode, wantStatus)
	}
	if profile != "delete-missing" {
		worker, found, err := b.database.WorkerFactory.GetWorker("some-worker")
		if err != nil || found || worker != nil {
			return fmt.Errorf("deleted worker state found=%t worker=%v err=%v", found, worker, err)
		}
	}
	return nil
}
