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
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/auth"
	"github.com/concourse/concourse/atc/api/pipelineserver"
	"github.com/concourse/concourse/atc/api/resourceserver"
	"github.com/concourse/concourse/atc/auditor"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"
	"github.com/concourse/concourse/atc/wrappa"
	"github.com/concourse/concourse/skymarshal/skycmd"
	"github.com/tedsuo/rata"
	"golang.org/x/oauth2"
)

const (
	resourceStrictTeam      = "resource-team"
	resourceStrictPipeline  = "resource-pipeline"
	resourceStrictAudience  = "resource-audience"
	resourceStrictConnector = "resource-connector"
	resourceStrictUser      = "resource-user"
)

type APIResourcesStrictObservation struct {
	Profile string
	Failure string
}

type apiResourcesBoundary struct {
	database JetbridgeDB
	team     db.Team
	pipeline db.Pipeline
	resource db.Resource
	scenario *dbtest.Scenario
	owner    *http.Client
	admin    *http.Client
	public   *http.Client
	url      string
}

func APIResourcesStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, APIResourcesStrictObservation](
			"the production resources API executes profile {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, rec *brine.Recorder, resources brine.Resources) (APIResourcesStrictObservation, error) {
				profile, ok := p.GetString(0)
				if !ok {
					return APIResourcesStrictObservation{}, fmt.Errorf("expected resources API profile")
				}
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return APIResourcesStrictObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				boundary, err := newAPIResourcesBoundary(database, rec, profile)
				if err != nil {
					return APIResourcesStrictObservation{}, err
				}
				return APIResourcesStrictObservation{Profile: profile, Failure: boundary.observe(profile)}, nil
			},
		),
		brine.DefineCheck[APIResourcesStrictObservation](
			"the resources API observation exactly matches profile {string}",
			func(observation APIResourcesStrictObservation, p brine.Params, _ *brine.Recorder) error {
				profile, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected resources API assertion profile")
				}
				if profile != observation.Profile {
					return fmt.Errorf("executed profile %q, asserted %q", observation.Profile, profile)
				}
				if observation.Failure != "" {
					return fmt.Errorf("resources API observation: %s", observation.Failure)
				}
				return nil
			},
		),
	}
}

func strictResourceConfig() atc.Config {
	return atc.Config{
		Resources: atc.ResourceConfigs{{
			Name: "resource-name", Type: dbtest.BaseResourceType,
			Source: atc.Source{"repository": "primary"}, Icon: "git",
		}},
		ResourceTypes: atc.ResourceTypes{{
			Name: "resource-type-name", Type: dbtest.BaseResourceType,
			Source: atc.Source{"repository": "resource-type"}, Defaults: atc.Source{"branch": "main"},
			Privileged: true, Tags: atc.Tags{"resource-type-worker"}, Params: atc.Params{"resource-type-param": "persisted"},
		}},
	}
}

func newAPIResourcesBoundary(database JetbridgeDB, rec *brine.Recorder, profile string) (*apiResourcesBoundary, error) {
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: resourceStrictTeam})
	if err != nil {
		return nil, err
	}
	if err := team.UpdateProviderAuth(atc.TeamAuth{
		accessor.OwnerRole: {"users": {resourceStrictConnector + ":" + resourceStrictUser}},
	}); err != nil {
		return nil, err
	}
	adminTeam, err := database.TeamFactory.CreateDefaultTeamIfNotExists()
	if err != nil {
		return nil, err
	}
	if err := adminTeam.UpdateProviderAuth(atc.TeamAuth{
		accessor.OwnerRole: {"users": {resourceStrictConnector + ":resource-admin"}},
	}); err != nil {
		return nil, err
	}
	config := strictResourceConfig()
	switch profile {
	case "resource-types":
		config.ResourceTypes[0].CheckEvery = &atc.CheckEvery{Interval: 10 * time.Millisecond}
	case "get-config-pin":
		config.Resources[0].Version = atc.Version{"ref": "configured"}
	}
	pipeline, _, err := team.SavePipeline(atc.PipelineRef{Name: resourceStrictPipeline}, config, 0, false)
	if err != nil {
		return nil, err
	}
	scenario := &dbtest.Scenario{Team: team, Pipeline: pipeline}
	var resource db.Resource
	if len(config.Resources) > 0 {
		resource, _, err = pipeline.Resource("resource-name")
		if err != nil {
			return nil, err
		}
	}
	boundary := &apiResourcesBoundary{database: database, team: team, pipeline: pipeline, resource: resource, scenario: scenario}
	if strings.HasPrefix(profile, "all-") {
		if err := boundary.seedAllResources(profile); err != nil {
			return nil, err
		}
	}
	if profile == "all-auth" || profile == "pipeline-resources" || profile == "get-details" {
		scenario.Run(database.Builder.WithResourceVersions("resource-name", atc.Version{"ref": "persisted"}))
		resource = scenario.Resource("resource-name")
		boundary.resource = resource
	}
	owner, err := strictResourceTokenClient(database, resourceStrictUser, nil)
	if err != nil {
		return nil, err
	}
	admin, err := strictResourceTokenClient(database, "resource-admin", []string{"resource-system"})
	if err != nil {
		return nil, err
	}
	public := &http.Client{Timeout: 30 * time.Second}
	url, err := startAPIResourcesServer(database, rec, owner, admin, public)
	if err != nil {
		return nil, err
	}
	boundary.owner, boundary.admin, boundary.public, boundary.url = owner, admin, public, url
	return boundary, nil
}

func strictResourceTokenClient(database JetbridgeDB, user string, groups []string) (*http.Client, error) {
	token := "resource-token-" + user
	payload, err := json.Marshal(map[string]any{
		"sub": user, "preferred_username": user, "aud": []any{resourceStrictAudience},
		"exp": time.Now().Add(time.Hour).Unix(), "groups": groups,
		"federated_claims": map[string]any{"connector_id": resourceStrictConnector, "user_id": user},
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

func startAPIResourcesServer(database JetbridgeDB, rec *brine.Recorder, clients ...*http.Client) (string, error) {
	logger := lager.NewLogger("brine-api-resources-strict")
	display, err := skycmd.NewSkyDisplayUserIdGenerator(map[string]string{})
	if err != nil {
		return "", err
	}
	accessFactory := accessor.NewAccessFactory(
		accessor.NewVerifier(db.NewAccessTokenFactory(database.Conn), []string{resourceStrictAudience}),
		database.TeamFactory, "sub", []string{"resource-system"}, display,
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
	server := resourceserver.NewServer(
		logger, nil, nil, db.NewCheckFactory(database.Conn, database.LockFactory, nil, nil),
		db.NewResourceFactory(database.Conn, database.LockFactory),
		db.NewResourceConfigFactory(database.Conn, database.LockFactory),
	)
	pipelines := pipelineserver.NewScopedHandlerFactory(database.TeamFactory)
	handlers := rata.Handlers{
		atc.ListAllResources:          http.HandlerFunc(server.ListAllResources),
		atc.ListResources:             pipelines.HandlerFor(server.ListResources),
		atc.ListResourceTypes:         pipelines.HandlerFor(server.ListResourceTypes),
		atc.GetResource:               pipelines.HandlerFor(server.GetResource),
		atc.UnpinResource:             pipelines.HandlerFor(server.UnpinResource),
		atc.SetPinCommentOnResource:   pipelines.HandlerFor(server.SetPinCommentOnResource),
		atc.ClearResourceCache:        pipelines.HandlerFor(server.ClearResourceCache),
		atc.ListSharedForResource:     pipelines.HandlerFor(server.ListSharedForResource),
		atc.ListSharedForResourceType: pipelines.HandlerFor(server.ListSharedForResourceType),
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

func (b *apiResourcesBoundary) seedAllResources(profile string) error {
	for _, spec := range []struct {
		team, pipeline, resource string
		public                   bool
	}{{"public-team", "public-pipeline", "public-resource", true}, {"private-team", "private-pipeline", "private-resource", false}} {
		team, err := b.database.TeamFactory.CreateTeam(atc.Team{Name: spec.team})
		if err != nil {
			return err
		}
		pipeline, _, err := team.SavePipeline(atc.PipelineRef{Name: spec.pipeline}, atc.Config{Resources: atc.ResourceConfigs{{
			Name: spec.resource, Type: dbtest.BaseResourceType, Source: atc.Source{"repository": spec.resource},
		}}}, 0, false)
		if err != nil {
			return err
		}
		if spec.public {
			if err := pipeline.Expose(); err != nil {
				return err
			}
		}
	}
	return nil
}

func (b *apiResourcesBoundary) observe(profile string) string {
	var err error
	switch profile {
	case "all-auth", "all-anon", "all-admin":
		err = b.observeAll(profile)
	case "pipeline-resources":
		err = b.observePipelineResources(profile)
	case "resource-types":
		err = b.observeResourceTypes(profile)
	case "get-details", "get-config-pin", "get-api-pin", "get-missing":
		err = b.observeGet(profile)
	case "unpin-success", "unpin-empty":
		err = b.observeUnpin(profile)
	case "comment-success", "comment-malformed":
		err = b.observeComment(profile)
	case "cache-version", "cache-all", "cache-malformed-missing":
		err = b.observeCache(profile)
	default:
		err = fmt.Errorf("unknown resources API profile %q", profile)
	}
	if err != nil {
		return err.Error()
	}
	return ""
}

func (b *apiResourcesBoundary) request(client *http.Client, method, path string, body io.Reader) (*http.Response, []byte, error) {
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

func strictNames(resources []atc.Resource) []string {
	names := make([]string, len(resources))
	for i, resource := range resources {
		names[i] = resource.Name
	}
	sort.Strings(names)
	return names
}

func (b *apiResourcesBoundary) observeAll(profile string) error {
	client := b.owner
	want := []string{"public-resource", "resource-name"}
	if profile == "all-anon" {
		client, want = b.public, []string{"public-resource"}
	}
	if profile == "all-admin" {
		client, want = b.admin, []string{"private-resource", "public-resource", "resource-name"}
	}
	response, payload, err := b.request(client, http.MethodGet, "/api/v1/resources", nil)
	if err != nil {
		return err
	}
	var actual []atc.Resource
	if err := json.Unmarshal(payload, &actual); err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "application/json" || !reflect.DeepEqual(strictNames(actual), want) {
		return fmt.Errorf("global resources status=%d content-type=%q names=%v want=%v", response.StatusCode, response.Header.Get("Content-Type"), strictNames(actual), want)
	}
	if profile == "all-auth" {
		var found *atc.Resource
		for i := range actual {
			if actual[i].Name == "resource-name" {
				found = &actual[i]
			}
		}
		if found == nil || found.PipelineID != b.pipeline.ID() || found.TeamName != b.team.Name() || found.LastChecked <= 0 || found.Build == nil || found.Build.Status != atc.StatusSucceeded {
			return fmt.Errorf("authorized persisted resource was incomplete: %#v", found)
		}
	}
	return nil
}

func (b *apiResourcesBoundary) observePipelineResources(profile string) error {
	response, payload, err := b.request(b.owner, http.MethodGet, "/api/v1/teams/resource-team/pipelines/resource-pipeline/resources", nil)
	if err != nil {
		return err
	}
	var resources []atc.Resource
	if err := json.Unmarshal(payload, &resources); err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "application/json" || len(resources) != 1 {
		return fmt.Errorf("pipeline resources status=%d content-type=%q count=%d want=1", response.StatusCode, response.Header.Get("Content-Type"), len(resources))
	}
	if resources[0].Name != "resource-name" || resources[0].PipelineID != b.pipeline.ID() || resources[0].TeamName != b.team.Name() || resources[0].LastChecked <= 0 {
		return fmt.Errorf("pipeline resource mismatch: %#v", resources[0])
	}
	return nil
}

func (b *apiResourcesBoundary) observeResourceTypes(profile string) error {
	response, payload, err := b.request(b.owner, http.MethodGet, "/api/v1/teams/resource-team/pipelines/resource-pipeline/resource-types", nil)
	if err != nil {
		return err
	}
	var actual atc.ResourceTypes
	if err := json.Unmarshal(payload, &actual); err != nil {
		return err
	}
	want := strictResourceConfig().ResourceTypes
	want[0].CheckEvery = &atc.CheckEvery{Interval: 10 * time.Millisecond}
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "application/json" || !reflect.DeepEqual(actual, want) {
		return fmt.Errorf("resource types status=%d content-type=%q got=%#v want=%#v", response.StatusCode, response.Header.Get("Content-Type"), actual, want)
	}
	return nil
}

func (b *apiResourcesBoundary) observeGet(profile string) error {
	path := "/api/v1/teams/resource-team/pipelines/resource-pipeline/resources/resource-name"
	if profile == "get-missing" {
		path = "/api/v1/teams/resource-team/pipelines/resource-pipeline/resources/missing"
	}
	if profile == "get-api-pin" {
		version := atc.Version{"ref": "api-pinned"}
		b.scenario.Run(b.database.Builder.WithResourceVersions("resource-name", version))
		b.resource = b.scenario.Resource("resource-name")
		pinned, err := b.resource.PinVersion(b.scenario.ResourceVersion("resource-name", version).ID())
		if err != nil || !pinned {
			return fmt.Errorf("pin resource: pinned=%t err=%v", pinned, err)
		}
		if err := b.resource.SetPinComment("release candidate"); err != nil {
			return err
		}
	}
	response, payload, err := b.request(b.owner, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	if profile == "get-missing" {
		if response.StatusCode != http.StatusNotFound {
			return fmt.Errorf("missing resource status=%d", response.StatusCode)
		}
		return nil
	}
	var resource atc.Resource
	if err := json.Unmarshal(payload, &resource); err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "application/json" {
		return fmt.Errorf("get resource status=%d content-type=%q", response.StatusCode, response.Header.Get("Content-Type"))
	}
	switch profile {
	case "get-details":
		if resource.Name != "resource-name" || resource.PipelineID != b.pipeline.ID() || resource.TeamName != b.team.Name() || resource.Type != dbtest.BaseResourceType || resource.Icon != "git" || resource.LastChecked <= 0 || resource.Build == nil || resource.Build.Status != atc.StatusSucceeded {
			return fmt.Errorf("resource details mismatch: %#v", resource)
		}
	case "get-config-pin":
		if !reflect.DeepEqual(resource.PinnedVersion, atc.Version{"ref": "configured"}) || !resource.PinnedInConfig {
			return fmt.Errorf("configured pin mismatch: %#v", resource)
		}
	case "get-api-pin":
		if !reflect.DeepEqual(resource.PinnedVersion, atc.Version{"ref": "api-pinned"}) || resource.PinnedInConfig || resource.PinComment != "release candidate" {
			return fmt.Errorf("API pin mismatch: %#v", resource)
		}
	}
	return nil
}

func (b *apiResourcesBoundary) pin(version atc.Version) error {
	b.scenario.Run(b.database.Builder.WithResourceVersions("resource-name", version))
	b.resource = b.scenario.Resource("resource-name")
	pinned, err := b.resource.PinVersion(b.scenario.ResourceVersion("resource-name", version).ID())
	if err != nil || !pinned {
		return fmt.Errorf("pin resource: pinned=%t err=%v", pinned, err)
	}
	return nil
}

func (b *apiResourcesBoundary) observeUnpin(profile string) error {
	if profile == "unpin-success" {
		if err := b.pin(atc.Version{"ref": "pinned"}); err != nil {
			return err
		}
	}
	response, _, err := b.request(b.owner, http.MethodPut, "/api/v1/teams/resource-team/pipelines/resource-pipeline/resources/resource-name/unpin", nil)
	if err != nil {
		return err
	}
	want := http.StatusOK
	if profile == "unpin-empty" {
		want = http.StatusInternalServerError
	}
	if response.StatusCode != want {
		return fmt.Errorf("unpin status=%d want=%d", response.StatusCode, want)
	}
	if profile == "unpin-success" {
		found, err := b.resource.Reload()
		if err != nil || !found || b.resource.APIPinnedVersion() != nil {
			return fmt.Errorf("unpin state found=%t pin=%v err=%v", found, b.resource.APIPinnedVersion(), err)
		}
	}
	return nil
}

func (b *apiResourcesBoundary) observeComment(profile string) error {
	var body io.Reader = strings.NewReader("{")
	if profile == "comment-success" {
		if err := b.pin(atc.Version{"ref": "commented"}); err != nil {
			return err
		}
		payload, _ := json.Marshal(atc.SetPinCommentRequestBody{PinComment: "promote after soak"})
		body = bytes.NewReader(payload)
	}
	response, _, err := b.request(b.owner, http.MethodPut, "/api/v1/teams/resource-team/pipelines/resource-pipeline/resources/resource-name/pin_comment", body)
	if err != nil {
		return err
	}
	want := http.StatusBadRequest
	if profile == "comment-success" {
		want = http.StatusOK
	}
	if response.StatusCode != want {
		return fmt.Errorf("pin comment status=%d want=%d", response.StatusCode, want)
	}
	if profile == "comment-success" {
		found, err := b.resource.Reload()
		if err != nil || !found || !reflect.DeepEqual(b.resource.APIPinnedVersion(), atc.Version{"ref": "commented"}) || b.resource.PinComment() != "promote after soak" {
			return fmt.Errorf("pin comment state found=%t pin=%v comment=%q err=%v", found, b.resource.APIPinnedVersion(), b.resource.PinComment(), err)
		}
	}
	return nil
}

func (b *apiResourcesBoundary) cacheAssociation(version atc.Version, handle string) (db.ResourceCache, error) {
	worker := b.scenario.Workers[0]
	build, err := b.team.CreateOneOffBuild()
	if err != nil {
		return nil, err
	}
	cache, err := db.NewResourceCacheFactory(b.database.Conn, b.database.LockFactory).FindOrCreateResourceCache(
		db.ForBuild(build.ID()), b.resource.Type(), version, b.resource.Source(), nil, nil,
	)
	if err != nil {
		return nil, err
	}
	creating, err := b.database.VolumeRepository.CreateVolumeWithHandle(handle, b.team.ID(), worker.Name(), db.VolumeTypeContainer)
	if err != nil {
		return nil, err
	}
	created, err := creating.Created()
	if err != nil {
		return nil, err
	}
	_, err = created.InitializeResourceCache(cache)
	return cache, err
}

func (b *apiResourcesBoundary) cacheCount(cache db.ResourceCache) (int, error) {
	var count int
	err := b.database.Conn.QueryRow(`SELECT count(*) FROM worker_resource_caches WHERE resource_cache_id = $1`, cache.ID()).Scan(&count)
	return count, err
}

func (b *apiResourcesBoundary) observeCache(profile string) error {
	path := "/api/v1/teams/resource-team/pipelines/resource-pipeline/resources/resource-name/cache"
	if profile == "cache-malformed-missing" {
		response, _, err := b.request(b.owner, http.MethodDelete, path, strings.NewReader("{"))
		if err != nil || response.StatusCode != http.StatusBadRequest {
			return fmt.Errorf("malformed cache status=%v err=%v", response.StatusCode, err)
		}
		response, _, err = b.request(b.owner, http.MethodDelete, "/api/v1/teams/resource-team/pipelines/resource-pipeline/resources/missing/cache", nil)
		if err != nil || response.StatusCode != http.StatusNotFound {
			return fmt.Errorf("missing cache status=%v err=%v", response.StatusCode, err)
		}
		return nil
	}
	b.scenario.Run(b.database.Builder.WithResourceVersions("resource-name", atc.Version{"ref": "scope"}))
	b.resource = b.scenario.Resource("resource-name")
	firstVersion := atc.Version{"ref": "matching"}
	first, err := b.cacheAssociation(firstVersion, "cache-first")
	if err != nil {
		return err
	}
	second, err := b.cacheAssociation(atc.Version{"ref": "decoy"}, "cache-second")
	if err != nil {
		return err
	}
	var body io.Reader
	if profile == "cache-version" {
		payload, _ := json.Marshal(atc.VersionDeleteBody{Version: firstVersion})
		body = bytes.NewReader(payload)
	}
	response, payload, err := b.request(b.owner, http.MethodDelete, path, body)
	if err != nil {
		return err
	}
	var result atc.ClearResourceCacheResponse
	if err := json.Unmarshal(payload, &result); err != nil {
		return err
	}
	firstCount, err := b.cacheCount(first)
	if err != nil {
		return err
	}
	secondCount, err := b.cacheCount(second)
	if err != nil {
		return err
	}
	wantRemoved, wantFirst, wantSecond := int64(1), 0, 1
	if profile == "cache-all" {
		wantRemoved, wantSecond = 2, 0
	}
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "application/json" || result.CachesRemoved != wantRemoved || firstCount != wantFirst || secondCount != wantSecond {
		return fmt.Errorf("cache status=%d type=%q removed=%d counts=%d/%d want=%d/%d/%d", response.StatusCode, response.Header.Get("Content-Type"), result.CachesRemoved, firstCount, secondCount, wantRemoved, wantFirst, wantSecond)
	}
	return nil
}
