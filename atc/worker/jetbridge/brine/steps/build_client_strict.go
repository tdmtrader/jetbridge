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
	"strconv"
	"strings"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/auth"
	"github.com/concourse/concourse/atc/api/buildserver"
	"github.com/concourse/concourse/atc/api/jobserver"
	"github.com/concourse/concourse/atc/api/pipelineserver"
	"github.com/concourse/concourse/atc/api/present"
	"github.com/concourse/concourse/atc/api/teamserver"
	"github.com/concourse/concourse/atc/auditor"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/wrappa"
	clientapi "github.com/concourse/concourse/go-concourse/concourse"
	"github.com/concourse/concourse/skymarshal/skycmd"
	"github.com/tedsuo/rata"
	"golang.org/x/oauth2"
)

const (
	buildStrictAudience  = "brine-build-client"
	buildStrictConnector = "brine-build-connector"
	buildStrictUserID    = "brine-build-user"
	buildStrictOutsider  = "brine-build-outsider"
	buildStrictTeamName  = "build-team"
)

type BuildStrictObservation struct {
	Profile string
	Failure string
}

type strictBuildBoundary struct {
	database     JetbridgeDB
	team         db.Team
	pipeline     db.Pipeline
	job          db.Job
	resource     db.Resource
	ref          atc.PipelineRef
	url          string
	httpClient   *http.Client
	outsiderHTTP *http.Client
	publicHTTP   *http.Client
	client       clientapi.Client
	clientTeam   clientapi.Team
	buildFactory db.BuildFactory
}

func BuildClientDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, BuildStrictObservation](
			"the production build boundary executes profile {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, rec *brine.Recorder, resources brine.Resources) (BuildStrictObservation, error) {
				profile, err := paramAt("the production build boundary executes profile {string}", p, 0)
				if err != nil {
					return BuildStrictObservation{}, err
				}
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return BuildStrictObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				boundary, err := newStrictBuildBoundaryForProfile(database, rec, profile)
				if err != nil {
					return BuildStrictObservation{}, err
				}
				return BuildStrictObservation{Profile: profile, Failure: boundary.observe(profile)}, nil
			},
		),
		brine.DefineCheck[BuildStrictObservation](
			"the production build observation exactly matches profile {string}",
			func(in BuildStrictObservation, p brine.Params, _ *brine.Recorder) error {
				profile, err := paramAt("the production build observation exactly matches profile {string}", p, 0)
				if err != nil {
					return err
				}
				if profile != in.Profile {
					return fmt.Errorf("build profile got %q, want %q", in.Profile, profile)
				}
				if in.Failure != "" {
					return fmt.Errorf("%s: %s", profile, in.Failure)
				}
				return nil
			},
		),
	}
}

func newStrictBuildBoundary(database JetbridgeDB, rec *brine.Recorder) (*strictBuildBoundary, error) {
	return newStrictBuildBoundaryForProfile(database, rec, "")
}

func newStrictBuildBoundaryForProfile(database JetbridgeDB, rec *brine.Recorder, profile string) (*strictBuildBoundary, error) {
	logger := lager.NewLogger("brine-build-client-strict")
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: buildStrictTeamName})
	if err != nil {
		return nil, fmt.Errorf("create build team: %w", err)
	}
	if err := team.UpdateProviderAuth(atc.TeamAuth{
		accessor.OwnerRole: {"users": {buildStrictConnector + ":" + buildStrictUserID}},
	}); err != nil {
		return nil, fmt.Errorf("grant build team owner role: %w", err)
	}

	ref := atc.PipelineRef{Name: "target"}
	if strings.HasPrefix(profile, "client-") {
		ref.InstanceVars = atc.InstanceVars{"branch": "master"}
	}
	pipeline, _, err := team.SavePipeline(ref, atc.Config{
		ResourceTypes: atc.ResourceTypes{{
			Name: "some-type", Type: "global-base-type", Source: atc.Source{"repository": "resource-type"},
		}},
		Resources: atc.ResourceConfigs{{
			Name: "some-input", Type: "some-type", Source: atc.Source{"repository": "resource"},
		}},
		Jobs: atc.JobConfigs{{
			Name:         "build",
			PlanSequence: []atc.Step{{Config: &atc.GetStep{Name: "some-input", Resource: "some-input"}}},
		}},
	}, 0, false)
	if err != nil {
		return nil, fmt.Errorf("save build pipeline: %w", err)
	}
	if profile == "api-job-public-existing" || profile == "api-list-status" {
		if err := pipeline.Expose(); err != nil {
			return nil, fmt.Errorf("expose build pipeline for outsider read: %w", err)
		}
	}
	job, found, err := pipeline.Job("build")
	if err != nil || !found {
		return nil, firstError(err, fmt.Errorf("saved build job was not found"))
	}
	resource, found, err := pipeline.Resource("some-input")
	if err != nil || !found {
		return nil, firstError(err, fmt.Errorf("saved build resource was not found"))
	}
	config, err := db.NewResourceConfigFactory(database.Conn, database.LockFactory).
		FindOrCreateResourceConfig(resource.Type(), resource.Source(), nil)
	if err != nil {
		return nil, fmt.Errorf("create build resource config: %w", err)
	}
	resourceID := resource.ID()
	scope, err := config.FindOrCreateScope(&resourceID)
	if err != nil {
		return nil, fmt.Errorf("create build resource scope: %w", err)
	}
	if err := resource.SetResourceConfigScope(scope); err != nil {
		return nil, fmt.Errorf("attach build resource scope: %w", err)
	}
	pinnedVersion := atc.Version{"ref": "pinned"}
	if err := scope.SaveVersions(db.SpanContext{}, []atc.Version{pinnedVersion}); err != nil {
		return nil, fmt.Errorf("save pinned build resource version: %w", err)
	}
	version, found, err := scope.FindVersion(pinnedVersion)
	if err != nil || !found {
		return nil, firstError(err, fmt.Errorf("saved pinned resource version was not found"))
	}
	pinned, err := resource.PinVersion(version.ID())
	if err != nil || !pinned {
		return nil, firstError(err, fmt.Errorf("resource version was not pinned"))
	}

	token := "brine-build-client-token"
	payload, err := json.Marshal(map[string]any{
		"sub": buildStrictUserID, "preferred_username": buildStrictUserID,
		"aud": []any{buildStrictAudience}, "exp": time.Now().Add(time.Hour).Unix(),
		"federated_claims": map[string]any{"connector_id": buildStrictConnector, "user_id": buildStrictUserID},
	})
	if err != nil {
		return nil, err
	}
	var claims db.Claims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, err
	}
	if err := db.NewAccessTokenFactory(database.Conn).CreateAccessToken(token, claims); err != nil {
		return nil, fmt.Errorf("persist build client access token: %w", err)
	}
	outsiderToken := "brine-build-outsider-token"
	outsiderPayload, err := json.Marshal(map[string]any{
		"sub": buildStrictOutsider, "preferred_username": buildStrictOutsider,
		"aud": []any{buildStrictAudience}, "exp": time.Now().Add(time.Hour).Unix(),
		"federated_claims": map[string]any{"connector_id": buildStrictConnector, "user_id": buildStrictOutsider},
	})
	if err != nil {
		return nil, err
	}
	var outsiderClaims db.Claims
	if err := json.Unmarshal(outsiderPayload, &outsiderClaims); err != nil {
		return nil, err
	}
	if err := db.NewAccessTokenFactory(database.Conn).CreateAccessToken(outsiderToken, outsiderClaims); err != nil {
		return nil, fmt.Errorf("persist outsider access token: %w", err)
	}
	display, err := skycmd.NewSkyDisplayUserIdGenerator(map[string]string{})
	if err != nil {
		return nil, err
	}
	accessFactory := accessor.NewAccessFactory(
		accessor.NewVerifier(db.NewAccessTokenFactory(database.Conn), []string{buildStrictAudience}),
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
	// CreateJobBuild always requests persisted checks (toDB=true), so the
	// production CheckFactory never uses its in-memory-build channel or sequence
	// generator on this route. Leave those in-memory-only dependencies absent
	// instead of wiring an unconsumed channel.
	checkFactory := db.NewCheckFactory(database.Conn, database.LockFactory, nil, nil)
	buildServer := buildserver.NewServer(logger, "https://concourse.invalid", database.TeamFactory, buildFactory, nil)
	jobServer := jobserver.NewServer(logger, "https://concourse.invalid", nil, db.NewJobFactory(database.Conn, database.LockFactory), checkFactory)
	teamServer := teamserver.NewServer(logger, database.TeamFactory, "https://concourse.invalid")
	pipelineScoped := pipelineserver.NewScopedHandlerFactory(database.TeamFactory)
	buildScoped := buildserver.NewScopedHandlerFactory(logger)
	teamScoped := api.NewTeamScopedHandlerFactory(logger, database.TeamFactory)
	handlers := rata.Handlers{
		atc.CreateBuild:    teamScoped.HandlerFor(buildServer.CreateBuild),
		atc.ListBuilds:     http.HandlerFunc(buildServer.ListBuilds),
		atc.GetBuild:       buildScoped.HandlerFor(buildServer.GetBuild),
		atc.AbortBuild:     buildScoped.HandlerFor(buildServer.AbortBuild),
		atc.ListTeamBuilds: teamScoped.HandlerFor(teamServer.ListTeamBuilds),
		atc.CreateJobBuild: pipelineScoped.HandlerFor(jobServer.CreateJobBuild),
		atc.GetJobBuild:    pipelineScoped.HandlerFor(jobServer.GetJobBuild),
	}
	var routes rata.Routes
	for _, route := range atc.Routes {
		if _, ok := handlers[route.Name]; ok {
			routes = append(routes, route)
		}
	}
	router, err := rata.NewRouter(routes, wrapper.Wrap(handlers))
	if err != nil {
		return nil, fmt.Errorf("build production build router: %w", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen for production build API: %w", err)
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
	return &strictBuildBoundary{
		database: database, team: team, pipeline: pipeline, job: job, resource: resource, ref: ref,
		url: url, httpClient: httpClient, outsiderHTTP: outsiderHTTP, publicHTTP: publicHTTP, client: client,
		clientTeam: client.Team(buildStrictTeamName), buildFactory: buildFactory,
	}, nil
}

func (b *strictBuildBoundary) observe(profile string) string {
	var err error
	switch profile {
	case "client-create-one-off":
		err = b.observeClientCreateOneOff()
	case "client-create-job":
		err = b.observeClientCreateJob()
	case "client-job-existing", "client-job-missing", "client-global-existing", "client-global-missing":
		err = b.observeClientLookup(profile)
	case "client-global-all", "client-global-from", "client-global-from-limit", "client-global-to", "client-global-to-limit", "client-global-from-to", "client-global-pagination-empty",
		"client-team-all", "client-team-from", "client-team-from-limit", "client-team-to", "client-team-to-limit", "client-team-from-to", "client-team-pagination-empty":
		err = b.observeClientList(profile)
	case "client-abort":
		err = b.observeClientAbort()
	case "api-create-status", "api-create-content-type", "api-create-state", "api-create-body":
		err = b.observeAPICreate(profile)
	case "api-list-status", "api-list-auth-status", "api-list-body":
		err = b.observeAPIList(profile)
	case "api-get-missing", "api-get-content-type", "api-get-body":
		err = b.observeAPIGet(profile)
	case "api-abort":
		err = b.observeAPIAbort()
	case "api-create-job":
		err = b.observeAPICreateJob()
	case "api-job-existing", "api-job-public-existing", "api-job-missing":
		err = b.observeAPIJobGet(profile)
	default:
		err = fmt.Errorf("unknown strict build profile %q", profile)
	}
	if err != nil {
		return err.Error()
	}
	return ""
}

func (b *strictBuildBoundary) observeClientCreateOneOff() error {
	plan := atc.Plan{ID: "client-build", Task: &atc.TaskPlan{Config: &atc.TaskConfig{Run: atc.TaskRunConfig{Path: "true"}}}}
	actual, err := b.clientTeam.CreateBuild(plan)
	if err != nil {
		return err
	}
	persisted, found, err := b.buildFactory.Build(actual.ID)
	if err != nil || !found {
		return firstError(err, fmt.Errorf("created one-off build %d was not persisted", actual.ID))
	}
	want := present.Build(persisted, nil, nil)
	if !reflect.DeepEqual(actual, want) || persisted.Status() != db.BuildStatusStarted || !reflect.DeepEqual(persisted.PrivatePlan(), plan) {
		return fmt.Errorf("created one-off mismatch: response=%#v want=%#v persisted=%d/%s/%s plan=%#v", actual, want, persisted.ID(), persisted.Name(), persisted.Status(), persisted.PrivatePlan())
	}
	return nil
}

func (b *strictBuildBoundary) observeClientCreateJob() error {
	actual, err := b.clientTeam.CreateJobBuild(b.ref, "build")
	if err != nil {
		return err
	}
	persisted, found, err := b.job.Build(actual.Name)
	if err != nil || !found {
		return firstError(err, fmt.Errorf("created job build %q was not persisted", actual.Name))
	}
	want := present.Build(persisted, nil, nil)
	if !reflect.DeepEqual(actual, want) || persisted.Status() != db.BuildStatusPending {
		return fmt.Errorf("created job build mismatch: response=%#v want=%#v persisted=%d/%s/%s", actual, want, persisted.ID(), persisted.Name(), persisted.Status())
	}
	return nil
}

func (b *strictBuildBoundary) observeClientLookup(profile string) error {
	if strings.HasSuffix(profile, "missing") {
		if strings.Contains(profile, "job") {
			_, found, err := b.clientTeam.JobBuild(b.ref, "build", "does-not-exist")
			if err != nil || found {
				return fmt.Errorf("missing job build: found=%t err=%v", found, err)
			}
			return nil
		}
		_, found, err := b.client.Build("999999999")
		if err != nil || found {
			return fmt.Errorf("missing global build: found=%t err=%v", found, err)
		}
		return nil
	}
	build, err := b.job.CreateBuild(buildStrictUserID)
	if err != nil {
		return err
	}
	if strings.Contains(profile, "job") {
		actual, found, err := b.clientTeam.JobBuild(b.ref, "build", build.Name())
		if err != nil || !found {
			return firstError(err, fmt.Errorf("persisted job build was not found"))
		}
		want := present.Build(build, b.job, nil)
		if !reflect.DeepEqual(actual, want) {
			return fmt.Errorf("job build lookup mismatch: got %#v, want %#v", actual, want)
		}
		return nil
	}
	actual, found, err := b.client.Build(strconv.Itoa(build.ID()))
	if err != nil || !found {
		return firstError(err, fmt.Errorf("persisted global build was not found"))
	}
	want := present.Build(build, b.job, nil)
	if !reflect.DeepEqual(actual, want) {
		return fmt.Errorf("global build lookup mismatch: got %#v, want %#v", actual, want)
	}
	return nil
}

func (b *strictBuildBoundary) observeClientList(profile string) error {
	count := 3
	if strings.Contains(profile, "from-limit") {
		count = 8
	}
	if strings.Contains(profile, "to-limit") {
		count = 20
	}
	if strings.Contains(profile, "from-to") {
		count = 5
	}
	upperTeam, err := b.database.TeamFactory.CreateTeam(atc.Team{Name: "build-other-upper"})
	if err != nil {
		return err
	}
	if err := upperTeam.UpdateProviderAuth(atc.TeamAuth{
		accessor.OwnerRole: {"users": {buildStrictConnector + ":" + buildStrictUserID}},
	}); err != nil {
		return err
	}
	targetIDs := make([]int, 0, count)
	allIDs := make([]int, 0, count+1)
	created := make(map[int]db.Build, count+1)
	var decoy db.Build
	decoyAfter := 1
	if strings.HasSuffix(profile, "from-limit") {
		decoyAfter = count - 4
	}
	if strings.HasSuffix(profile, "-to") && !strings.HasSuffix(profile, "from-to") {
		decoyAfter = 0
	}
	if strings.HasSuffix(profile, "to-limit") {
		decoyAfter = 13
	}
	for i := range count {
		build, err := b.team.CreateStartedBuild(atc.Plan{ID: atc.PlanID(fmt.Sprintf("list-%d", len(targetIDs)))})
		if err != nil {
			return err
		}
		targetIDs = append(targetIDs, build.ID())
		allIDs = append(allIDs, build.ID())
		created[build.ID()] = build
		if i == decoyAfter {
			decoy, err = upperTeam.CreateStartedBuild(atc.Plan{ID: atc.PlanID(fmt.Sprintf("decoy-%s", profile))})
			if err != nil {
				return err
			}
			allIDs = append(allIDs, decoy.ID())
			created[decoy.ID()] = decoy
		}
	}
	decoyID := decoy.ID()
	sort.Ints(allIDs)
	page := strictBuildPage(profile, targetIDs, decoyID)
	var actual []atc.Build
	var pagination clientapi.Pagination
	if strings.Contains(profile, "client-team-") {
		actual, pagination, err = b.clientTeam.Builds(page)
	} else {
		actual, pagination, err = b.client.Builds(page)
	}
	if err != nil {
		return err
	}
	if strings.HasSuffix(profile, "pagination-empty") && (pagination.Previous != nil || pagination.Next != nil) {
		return fmt.Errorf("pagination got previous=%#v next=%#v, want nil/nil", pagination.Previous, pagination.Next)
	}
	visibleIDs := allIDs
	if strings.Contains(profile, "client-team-") {
		visibleIDs = targetIDs
	}
	want := strictBuildPageIDs(page, visibleIDs)
	wantBuilds := make([]atc.Build, len(want))
	for i, id := range want {
		build, found := created[id]
		if !found {
			return fmt.Errorf("expected list build %d was not created", id)
		}
		wantBuilds[i] = present.Build(build, nil, nil)
	}
	if !reflect.DeepEqual(actual, wantBuilds) {
		return fmt.Errorf("build list got %#v, want %#v", actual, wantBuilds)
	}
	containsDecoy := false
	for _, build := range actual {
		containsDecoy = containsDecoy || build.ID == decoyID
	}
	if strings.Contains(profile, "client-global-") && !containsDecoy {
		return fmt.Errorf("global build list omitted visible cross-team decoy %d (page=%#v target=%v all=%v)", decoyID, page, targetIDs, allIDs)
	}
	if strings.Contains(profile, "client-team-") && containsDecoy {
		return fmt.Errorf("team build list included cross-team decoy %d", decoyID)
	}
	return nil
}

func strictBuildPage(profile string, ids []int, decoyID int) clientapi.Page {
	switch {
	case strings.HasSuffix(profile, "from-limit"):
		return clientapi.Page{From: ids[len(ids)-4], Limit: 5}
	case strings.HasSuffix(profile, "to-limit"):
		return clientapi.Page{To: decoyID, Limit: 15}
	case strings.HasSuffix(profile, "from-to"):
		return clientapi.Page{From: ids[1], To: ids[3]}
	case strings.HasSuffix(profile, "from"):
		return clientapi.Page{From: ids[1]}
	case strings.HasSuffix(profile, "to"):
		return clientapi.Page{To: ids[1]}
	default:
		return clientapi.Page{}
	}
}

func strictBuildPageIDs(page clientapi.Page, ids []int) []int {
	selected := make([]int, 0, len(ids))
	for _, id := range ids {
		if page.From > 0 && id < page.From {
			continue
		}
		if page.To > 0 && id > page.To {
			continue
		}
		selected = append(selected, id)
	}
	if page.From > 0 && page.To > 0 {
		return selected
	}
	if page.From > 0 && page.Limit > 0 && len(selected) > page.Limit {
		selected = selected[:page.Limit]
	}
	if page.To > 0 && page.Limit > 0 && len(selected) > page.Limit {
		selected = selected[len(selected)-page.Limit:]
	}
	for left, right := 0, len(selected)-1; left < right; left, right = left+1, right-1 {
		selected[left], selected[right] = selected[right], selected[left]
	}
	return selected
}

func (b *strictBuildBoundary) observeClientAbort() error {
	build, err := b.team.CreateStartedBuild(atc.Plan{ID: "abort-client"})
	if err != nil {
		return err
	}
	if err := b.client.AbortBuild(strconv.Itoa(build.ID())); err != nil {
		return err
	}
	if found, err := build.Reload(); err != nil || !found {
		return firstError(err, fmt.Errorf("aborted client build disappeared"))
	}
	if !build.IsAborted() {
		return fmt.Errorf("client abort did not persist aborted=true")
	}
	return nil
}

func (b *strictBuildBoundary) observeAPICreate(profile string) error {
	plan := atc.Plan{ID: "api-created", Task: &atc.TaskPlan{Config: &atc.TaskConfig{Run: atc.TaskRunConfig{Path: "true"}}}}
	var teamBuildsBefore int
	if profile == "api-create-state" {
		if err := b.database.Conn.QueryRow(`SELECT count(*) FROM builds WHERE team_id = $1`, b.team.ID()).Scan(&teamBuildsBefore); err != nil {
			return err
		}
	}
	body, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	response, raw, err := b.request(b.httpClient, http.MethodPost, "/api/v1/teams/"+buildStrictTeamName+"/builds", body)
	if err != nil {
		return err
	}
	if profile == "api-create-status" {
		if response.StatusCode != http.StatusCreated {
			return fmt.Errorf("create status got %d, want %d", response.StatusCode, http.StatusCreated)
		}
		return nil
	}
	if profile == "api-create-content-type" {
		if response.Header.Get("Content-Type") != "application/json" {
			return fmt.Errorf("create content type got %q, want application/json", response.Header.Get("Content-Type"))
		}
		return nil
	}
	var actual atc.Build
	if err := json.Unmarshal(raw, &actual); err != nil {
		return err
	}
	persisted, found, err := b.buildFactory.Build(actual.ID)
	if err != nil || !found {
		return firstError(err, fmt.Errorf("API-created build was not persisted"))
	}
	if profile == "api-create-state" {
		if persisted.ID() != actual.ID || persisted.TeamID() != b.team.ID() || persisted.TeamName() != b.team.Name() || persisted.Status() != db.BuildStatusStarted || !persisted.IsRunning() || persisted.StartTime().IsZero() || persisted.Schema() != "exec.v2" || !reflect.DeepEqual(persisted.PrivatePlan(), plan) || !reflect.DeepEqual(persisted.PublicPlan(), plan.Public()) {
			return fmt.Errorf("API-created persisted state mismatch for build %d", persisted.ID())
		}
		var teamBuildsAfter int
		if err := b.database.Conn.QueryRow(`SELECT count(*) FROM builds WHERE team_id = $1`, b.team.ID()).Scan(&teamBuildsAfter); err != nil {
			return err
		}
		if teamBuildsAfter != teamBuildsBefore+1 {
			return fmt.Errorf("API create changed team build count %d -> %d, want +1", teamBuildsBefore, teamBuildsAfter)
		}
	}
	if profile == "api-create-body" {
		want := present.Build(persisted, nil, nil)
		if !reflect.DeepEqual(actual, want) {
			return fmt.Errorf("API create body got %#v, want %#v", actual, want)
		}
	}
	return nil
}

func (b *strictBuildBoundary) observeAPIList(profile string) error {
	builds := make([]db.Build, 0, 3)
	for range 3 {
		build, err := b.job.CreateBuild(buildStrictUserID)
		if err != nil {
			return err
		}
		builds = append(builds, build)
	}
	httpClient := b.publicHTTP
	if profile == "api-list-auth-status" {
		httpClient = b.httpClient
	}
	response, raw, err := b.request(httpClient, http.MethodGet, "/api/v1/builds", nil)
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("list status got %d, want %d", response.StatusCode, http.StatusOK)
	}
	if profile == "api-list-body" {
		var actual []atc.Build
		if err := json.Unmarshal(raw, &actual); err != nil {
			return err
		}
		want := []atc.Build{
			present.Build(builds[2], nil, nil),
			present.Build(builds[1], nil, nil),
			present.Build(builds[0], nil, nil),
		}
		if !reflect.DeepEqual(actual, want) {
			return fmt.Errorf("API list got %#v, want %#v", actual, want)
		}
	}
	return nil
}

func (b *strictBuildBoundary) observeAPIGet(profile string) error {
	path := "/api/v1/builds/999999999"
	var persisted db.Build
	if profile != "api-get-missing" {
		var err error
		persisted, err = b.job.CreateBuild(buildStrictUserID)
		if err != nil {
			return err
		}
		if _, err := persisted.Start(atc.Plan{ID: "api-detail"}); err != nil {
			return err
		}
		if err := persisted.Finish(db.BuildStatusSucceeded); err != nil {
			return err
		}
		if found, err := persisted.Reload(); err != nil || !found {
			return firstError(err, fmt.Errorf("finished exact API build disappeared"))
		}
		path = "/api/v1/builds/" + strconv.Itoa(persisted.ID())
	}
	response, raw, err := b.request(b.httpClient, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	if profile == "api-get-missing" {
		if response.StatusCode != http.StatusNotFound {
			return fmt.Errorf("missing build status got %d, want %d", response.StatusCode, http.StatusNotFound)
		}
		return nil
	}
	if profile == "api-get-content-type" {
		if response.Header.Get("Content-Type") != "application/json" {
			return fmt.Errorf("exact build content type got %q, want application/json", response.Header.Get("Content-Type"))
		}
		return nil
	}
	if profile == "api-get-body" {
		var actual atc.Build
		if err := json.Unmarshal(raw, &actual); err != nil {
			return err
		}
		want := present.Build(persisted, b.job, nil)
		if !reflect.DeepEqual(actual, want) {
			return fmt.Errorf("exact build API body got %#v, want %#v", actual, want)
		}
	}
	return nil
}

func (b *strictBuildBoundary) observeAPIAbort() error {
	build, err := b.team.CreateStartedBuild(atc.Plan{ID: "abort-api"})
	if err != nil {
		return err
	}
	initialStatus := build.Status()
	response, _, err := b.request(b.httpClient, http.MethodPut, "/api/v1/builds/"+strconv.Itoa(build.ID())+"/abort", nil)
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("abort status got %d, want %d", response.StatusCode, http.StatusNoContent)
	}
	if found, err := build.Reload(); err != nil || !found {
		return firstError(err, fmt.Errorf("API-aborted build disappeared"))
	}
	if !build.IsAborted() {
		return fmt.Errorf("API abort did not persist aborted=true")
	}
	if build.Status() != initialStatus {
		return fmt.Errorf("API abort changed status from %s to %s", initialStatus, build.Status())
	}
	return nil
}

func (b *strictBuildBoundary) observeAPICreateJob() error {
	var before int
	if err := b.database.Conn.QueryRow(`SELECT count(*) FROM builds WHERE resource_id = $1`, b.resource.ID()).Scan(&before); err != nil {
		return err
	}
	path := fmt.Sprintf("/api/v1/teams/%s/pipelines/%s/jobs/build/builds?%s", buildStrictTeamName, b.ref.Name, b.ref.QueryParams().Encode())
	response, raw, err := b.request(b.httpClient, http.MethodPost, path, nil)
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "application/json" {
		return fmt.Errorf("manual job response status/content-type got %d/%q", response.StatusCode, response.Header.Get("Content-Type"))
	}
	var actual atc.Build
	if err := json.Unmarshal(raw, &actual); err != nil {
		return err
	}
	persisted, found, err := b.job.Build(actual.Name)
	if err != nil || !found {
		return firstError(err, fmt.Errorf("manual job build was not persisted"))
	}
	if actual.ID <= 0 || actual.ID != persisted.ID() || actual.Name == "" || actual.Name != persisted.Name() {
		return fmt.Errorf("manual job build identity got id=%d name=%q, want persisted id=%d name=%q", actual.ID, actual.Name, persisted.ID(), persisted.Name())
	}
	if actual.TeamName != buildStrictTeamName || actual.PipelineName != b.pipeline.Name() || actual.JobName != b.job.Name() {
		return fmt.Errorf("manual job build scope got team=%q pipeline=%q job=%q", actual.TeamName, actual.PipelineName, actual.JobName)
	}
	if actual.Status != atc.StatusPending || actual.StartTime != 0 || actual.EndTime != 0 {
		return fmt.Errorf("manual job build lifecycle got status=%q start=%d end=%d, want pending/zero/zero", actual.Status, actual.StartTime, actual.EndTime)
	}
	if actual.CreatedBy == nil || *actual.CreatedBy != buildStrictUserID {
		return fmt.Errorf("manual job build creator got %#v, want %q", actual.CreatedBy, buildStrictUserID)
	}
	createdBy := persisted.CreatedBy()
	if persisted.Status() != db.BuildStatusPending || !persisted.StartTime().IsZero() || !persisted.EndTime().IsZero() || createdBy == nil || *createdBy != buildStrictUserID {
		return fmt.Errorf("persisted manual job lifecycle/creator mismatch for build %d", persisted.ID())
	}
	wantBuild := present.Build(persisted, nil, nil)
	if !reflect.DeepEqual(actual, wantBuild) {
		return fmt.Errorf("manual job response got %#v, want %#v", actual, wantBuild)
	}
	var jobBuilds int
	if err := b.database.Conn.QueryRow(`SELECT count(*) FROM builds WHERE job_id = $1`, b.job.ID()).Scan(&jobBuilds); err != nil {
		return err
	}
	if jobBuilds != 1 {
		return fmt.Errorf("persisted job builds got %d, want exactly 1", jobBuilds)
	}
	var after int
	if err := b.database.Conn.QueryRow(`SELECT count(*) FROM builds WHERE resource_id = $1`, b.resource.ID()).Scan(&after); err != nil {
		return err
	}
	if after != before+1 {
		return fmt.Errorf("real persisted checks got %d -> %d, want +1", before, after)
	}
	var checkID int
	if err := b.database.Conn.QueryRow(`SELECT id FROM builds WHERE resource_id = $1 ORDER BY id DESC LIMIT 1`, b.resource.ID()).Scan(&checkID); err != nil {
		return err
	}
	check, found, err := b.buildFactory.Build(checkID)
	if err != nil || !found {
		return firstError(err, fmt.Errorf("persisted check build was not found"))
	}
	if !check.IsManuallyTriggered() {
		return fmt.Errorf("persisted check build was not manually triggered")
	}
	if check.ID() <= 0 || check.ResourceID() != b.resource.ID() || check.ResourceName() != b.resource.Name() {
		return fmt.Errorf("persisted check identity got id=%d resource=%d/%q, want positive and %d/%q", check.ID(), check.ResourceID(), check.ResourceName(), b.resource.ID(), b.resource.Name())
	}
	plan := check.PrivatePlan()
	if plan.Check == nil {
		return fmt.Errorf("persisted check has no check plan: %#v", plan)
	}
	wantFrom := atc.Version{"ref": "pinned"}
	if plan.Check.Name != b.resource.Name() || plan.Check.Type != b.resource.Type() || !reflect.DeepEqual(plan.Check.Source, b.resource.Source()) || plan.Check.Resource != b.resource.Name() || !reflect.DeepEqual(plan.Check.FromVersion, wantFrom) || !plan.Check.SkipInterval {
		return fmt.Errorf("persisted resource check plan mismatch: %#v", plan.Check)
	}
	if plan.Check.TypeImage.GetPlan == nil || plan.Check.TypeImage.GetPlan.Get == nil || plan.Check.TypeImage.CheckPlan == nil || plan.Check.TypeImage.CheckPlan.Check == nil {
		return fmt.Errorf("persisted check lacks custom resource-type plans: %#v", plan.Check.TypeImage)
	}
	imageGet := plan.Check.TypeImage.GetPlan.Get
	imageCheck := plan.Check.TypeImage.CheckPlan.Check
	if imageGet.Name != "some-type" || imageGet.Type != "global-base-type" || !reflect.DeepEqual(imageGet.Source, atc.Source{"repository": "resource-type"}) || imageCheck.Name != "some-type" || imageCheck.Type != "global-base-type" || !reflect.DeepEqual(imageCheck.Source, atc.Source{"repository": "resource-type"}) || !imageCheck.SkipInterval {
		return fmt.Errorf("persisted custom resource-type plan mismatch: get=%#v check=%#v", imageGet, imageCheck)
	}
	return nil
}

func (b *strictBuildBoundary) observeAPIJobGet(profile string) error {
	name := "does-not-exist"
	var persisted db.Build
	if profile != "api-job-missing" {
		var err error
		persisted, err = b.job.CreateBuild(buildStrictUserID)
		if err != nil {
			return err
		}
		if _, err := persisted.Start(atc.Plan{ID: "job-detail"}); err != nil {
			return err
		}
		if err := persisted.Finish(db.BuildStatusSucceeded); err != nil {
			return err
		}
		if found, err := persisted.Reload(); err != nil || !found {
			return firstError(err, fmt.Errorf("finished exact job API build disappeared"))
		}
		name = persisted.Name()
	}
	path := fmt.Sprintf("/api/v1/teams/%s/pipelines/%s/jobs/build/builds/%s?%s", buildStrictTeamName, b.ref.Name, name, b.ref.QueryParams().Encode())
	httpClient := b.httpClient
	if profile == "api-job-public-existing" {
		httpClient = b.outsiderHTTP
	}
	response, raw, err := b.request(httpClient, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	if profile == "api-job-missing" {
		if response.StatusCode != http.StatusNotFound {
			return fmt.Errorf("missing job build status got %d, want %d", response.StatusCode, http.StatusNotFound)
		}
		return nil
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("exact job build status got %d, want %d", response.StatusCode, http.StatusOK)
	}
	if profile == "api-job-existing" {
		if response.Header.Get("Content-Type") != "application/json" {
			return fmt.Errorf("exact job build content type got %q, want application/json", response.Header.Get("Content-Type"))
		}
	}
	var actual atc.Build
	if err := json.Unmarshal(raw, &actual); err != nil {
		return err
	}
	want := present.Build(persisted, b.job, nil)
	if !reflect.DeepEqual(actual, want) {
		return fmt.Errorf("exact job build API body got %#v, want %#v", actual, want)
	}
	return nil
}

func (b *strictBuildBoundary) request(client *http.Client, method, path string, body []byte) (*http.Response, []byte, error) {
	request, err := http.NewRequestWithContext(context.Background(), method, b.url+path, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, nil, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	return response, raw, err
}
