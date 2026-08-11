package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type resourceAPIFixture struct {
	database *realDB
	builder  dbtest.Builder
	team     db.Team
	pipeline db.Pipeline
	scenario *dbtest.Scenario
}

type resourceAPITeamFactory struct {
	db.TeamFactory
	teamName string
	team     db.Team
}

func (factory resourceAPITeamFactory) FindTeam(name string) (db.Team, bool, error) {
	if name == factory.teamName {
		return factory.team, true, nil
	}
	return factory.TeamFactory.FindTeam(name)
}

type resourceAPITeam struct {
	db.Team
	pipeline db.Pipeline
}

func (team resourceAPITeam) Pipeline(ref atc.PipelineRef) (db.Pipeline, bool, error) {
	if ref.Name == team.pipeline.Name() && reflect.DeepEqual(ref.InstanceVars, team.pipeline.InstanceVars()) {
		return team.pipeline, true, nil
	}
	return team.Team.Pipeline(ref)
}

// resourceAPIPipeline is a narrow post-lookup seam. Ordinary calls delegate to
// the production pipeline; individual fields are used only to deliver a
// method-specific SQL failure or a preloaded production object whose secondary
// connection has already been closed.
type resourceAPIPipeline struct {
	db.Pipeline
	resource                 db.Resource
	resourceType             db.ResourceType
	resourceTypesPipeline    db.Pipeline
	resourceOverrideName     string
	resourceTypeOverrideName string
}

func (pipeline resourceAPIPipeline) Resource(name string) (db.Resource, bool, error) {
	if pipeline.resource != nil && name == pipeline.resourceOverrideName {
		return pipeline.resource, true, nil
	}
	return pipeline.Pipeline.Resource(name)
}

func (pipeline resourceAPIPipeline) Resources() (db.Resources, error) {
	return pipeline.Pipeline.Resources()
}

func (pipeline resourceAPIPipeline) ResourceType(name string) (db.ResourceType, bool, error) {
	if pipeline.resourceType != nil && name == pipeline.resourceTypeOverrideName {
		return pipeline.resourceType, true, nil
	}
	return pipeline.Pipeline.ResourceType(name)
}

func (pipeline resourceAPIPipeline) ResourceTypes() (db.ResourceTypes, error) {
	if pipeline.resourceTypesPipeline != nil {
		return pipeline.resourceTypesPipeline.ResourceTypes()
	}
	return pipeline.Pipeline.ResourceTypes()
}

type resourceAPICheckCall struct {
	checkable               db.Checkable
	resourceTypes           db.ResourceTypes
	from                    atc.Version
	manuallyTriggered       bool
	skipIntervalRecursively bool
	toDB                    bool
}

// resourceAPICheckFactory normally delegates to the real CheckFactory. Its two
// non-delegating boundaries are deliberately limited to the handler's
// impossible defensive "not created" result and an injected factory error.
type resourceAPICheckFactory struct {
	db.CheckFactory

	mu         sync.Mutex
	calls      []resourceAPICheckCall
	notCreated bool
	err        error
}

func (factory *resourceAPICheckFactory) TryCreateCheck(
	ctx context.Context,
	checkable db.Checkable,
	resourceTypes db.ResourceTypes,
	from atc.Version,
	manuallyTriggered bool,
	skipIntervalRecursively bool,
	toDB bool,
) (db.Build, bool, error) {
	factory.mu.Lock()
	factory.calls = append(factory.calls, resourceAPICheckCall{
		checkable: checkable, resourceTypes: resourceTypes, from: from,
		manuallyTriggered:       manuallyTriggered,
		skipIntervalRecursively: skipIntervalRecursively,
		toDB:                    toDB,
	})
	notCreated, configuredErr := factory.notCreated, factory.err
	factory.mu.Unlock()

	if configuredErr != nil {
		// PostgreSQL cannot be asked to produce an arbitrary CheckFactory error.
		return nil, false, configuredErr
	}
	if notCreated {
		// Manual checks bypass duplicate suppression, so this defensive handler
		// result cannot arise from the production factory.
		return nil, false, nil
	}
	return factory.CheckFactory.TryCreateCheck(
		ctx, checkable, resourceTypes, from,
		manuallyTriggered, skipIntervalRecursively, toDB,
	)
}

func (factory *resourceAPICheckFactory) Calls() []resourceAPICheckCall {
	factory.mu.Lock()
	defer factory.mu.Unlock()
	return append([]resourceAPICheckCall(nil), factory.calls...)
}

func defaultResourceAPIConfig() atc.Config {
	return atc.Config{
		Resources: atc.ResourceConfigs{{
			Name:         "resource-name",
			Type:         dbtest.BaseResourceType,
			Source:       atc.Source{"repository": "primary"},
			WebhookToken: "webhook-token",
			CheckTimeout: "4m",
			Tags:         atc.Tags{"resource-worker"},
			Icon:         "git",
		}},
		ResourceTypes: atc.ResourceTypes{{
			Name:       "resource-type-name",
			Type:       dbtest.BaseResourceType,
			Source:     atc.Source{"repository": "resource-type"},
			Defaults:   atc.Source{"branch": "main"},
			Privileged: true,
			Tags:       atc.Tags{"resource-type-worker"},
			Params:     atc.Params{"resource-type-param": "persisted"},
		}},
		Prototypes: atc.Prototypes{{
			Name:   "prototype-name",
			Type:   dbtest.BaseResourceType,
			Source: atc.Source{"repository": "prototype"},
			Tags:   atc.Tags{"prototype-worker"},
		}},
	}
}

func newResourceAPIFixture(config atc.Config) *resourceAPIFixture {
	GinkgoHelper()

	database := useRealDB()
	team, err := database.Deps.teamFactory.CreateTeam(atc.Team{Name: "a-team"})
	Expect(err).NotTo(HaveOccurred())
	pipeline := database.SavePipeline(team, "a-pipeline", config)
	builder := dbtest.NewBuilder(database.Conn, database.LockFactory)

	return &resourceAPIFixture{
		database: database,
		builder:  builder,
		team:     team,
		pipeline: pipeline,
		scenario: &dbtest.Scenario{Team: team, Pipeline: pipeline},
	}
}

func (fixture *resourceAPIFixture) updatePipeline(config atc.Config) {
	GinkgoHelper()

	pipeline, _, err := fixture.team.SavePipeline(
		atc.PipelineRef{Name: fixture.pipeline.Name()},
		config,
		fixture.pipeline.ConfigVersion(),
		false,
	)
	Expect(err).NotTo(HaveOccurred())
	fixture.pipeline = pipeline
	fixture.scenario.Pipeline = pipeline
}

func (fixture *resourceAPIFixture) overridePipeline(pipeline db.Pipeline) {
	fixture.database.Deps.teamFactory = resourceAPITeamFactory{
		TeamFactory: fixture.database.Deps.teamFactory,
		teamName:    fixture.team.Name(),
		team: resourceAPITeam{
			Team: fixture.team, pipeline: pipeline,
		},
	}
}

func (fixture *resourceAPIFixture) doomedPipeline() db.Pipeline {
	GinkgoHelper()

	conn := postgresRunner.OpenConn()
	teamFactory := db.NewTeamFactory(conn, fixture.database.LockFactory)
	team, found, err := teamFactory.FindTeam(fixture.team.Name())
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	pipeline, found, err := team.Pipeline(atc.PipelineRef{
		Name: fixture.pipeline.Name(), InstanceVars: fixture.pipeline.InstanceVars(),
	})
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	Expect(conn.Close()).To(Succeed())
	return pipeline
}

func (fixture *resourceAPIFixture) doomedResource(name string) db.Resource {
	GinkgoHelper()

	conn := postgresRunner.OpenConn()
	teamFactory := db.NewTeamFactory(conn, fixture.database.LockFactory)
	team, found, err := teamFactory.FindTeam(fixture.team.Name())
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	pipeline, found, err := team.Pipeline(atc.PipelineRef{
		Name: fixture.pipeline.Name(), InstanceVars: fixture.pipeline.InstanceVars(),
	})
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	resource, found, err := pipeline.Resource(name)
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	Expect(conn.Close()).To(Succeed())
	return resource
}

func (fixture *resourceAPIFixture) doomedResourceType(name string) db.ResourceType {
	GinkgoHelper()

	conn := postgresRunner.OpenConn()
	teamFactory := db.NewTeamFactory(conn, fixture.database.LockFactory)
	team, found, err := teamFactory.FindTeam(fixture.team.Name())
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	pipeline, found, err := team.Pipeline(atc.PipelineRef{
		Name: fixture.pipeline.Name(), InstanceVars: fixture.pipeline.InstanceVars(),
	})
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	resourceType, found, err := pipeline.ResourceType(name)
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	Expect(conn.Close()).To(Succeed())
	return resourceType
}

func (fixture *resourceAPIFixture) doomedResourceFactory() db.ResourceFactory {
	GinkgoHelper()

	conn := postgresRunner.OpenConn()
	factory := db.NewResourceFactory(conn, fixture.database.LockFactory)
	Expect(conn.Close()).To(Succeed())
	return factory
}

func requestResourceAPI(fixture *resourceAPIFixture, method, path string, body io.Reader) *http.Response {
	GinkgoHelper()

	server := fixture.database.Serve()
	request, err := http.NewRequest(method, server.URL+path, body)
	Expect(err).NotTo(HaveOccurred())
	response, err := client.Do(request)
	if response != nil {
		DeferCleanup(func() {
			Expect(response.Body.Close()).To(Succeed())
		})
	}
	Expect(err).NotTo(HaveOccurred())
	return response
}

func resourceAPIJSONBody(value any) io.Reader {
	GinkgoHelper()

	payload, err := json.Marshal(value)
	Expect(err).NotTo(HaveOccurred())
	return bytes.NewReader(payload)
}

func decodeResourceAPIResponse[T any](response *http.Response) T {
	GinkgoHelper()

	var value T
	Expect(json.NewDecoder(response.Body).Decode(&value)).To(Succeed())
	return value
}

func resourceByName(resources []atc.Resource, name string) atc.Resource {
	GinkgoHelper()

	for _, resource := range resources {
		if resource.Name == name {
			return resource
		}
	}
	Fail("resource " + name + " was absent")
	return atc.Resource{}
}

func persistResourceListingGraph(fixture *resourceAPIFixture) map[string]db.Resource {
	GinkgoHelper()

	fixture.scenario.Run(fixture.builder.WithResourceVersions(
		"resource-name", atc.Version{"ref": "v1"},
	))

	result := map[string]db.Resource{
		"authorized": fixture.scenario.Resource("resource-name"),
	}
	tests := []struct {
		key, teamName, pipelineName, resourceName string
		public                                    bool
	}{
		{key: "public", teamName: "public-team", pipelineName: "public-pipeline", resourceName: "public-resource", public: true},
		{key: "private", teamName: "private-team", pipelineName: "private-pipeline", resourceName: "private-resource"},
	}
	for _, test := range tests {
		team, err := fixture.database.Deps.teamFactory.CreateTeam(atc.Team{Name: test.teamName})
		Expect(err).NotTo(HaveOccurred())
		pipeline := fixture.database.SavePipeline(team, test.pipelineName, atc.Config{
			Resources: atc.ResourceConfigs{{
				Name: test.resourceName, Type: dbtest.BaseResourceType,
				Source: atc.Source{"repository": test.key},
			}},
		})
		if test.public {
			Expect(pipeline.Expose()).To(Succeed())
		}
		resource, found, err := pipeline.Resource(test.resourceName)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		result[test.key] = resource
	}
	return result
}

func persistSharedResources(fixture *resourceAPIFixture) atc.ResourcesAndTypes {
	GinkgoHelper()

	previous := atc.EnableGlobalResources
	atc.EnableGlobalResources = true
	DeferCleanup(func() {
		atc.EnableGlobalResources = previous
	})

	sharedConfig := atc.Config{
		Resources: atc.ResourceConfigs{{
			Name: "shared-resource", Type: dbtest.BaseResourceType,
			Source: atc.Source{"repository": "shared"},
		}},
		ResourceTypes: atc.ResourceTypes{{
			Name: "shared-type", Type: dbtest.BaseResourceType,
			Source: atc.Source{"repository": "shared"},
		}},
	}
	fixture.updatePipeline(sharedConfig)
	fixture.scenario.Run(
		fixture.builder.WithResourceVersions("shared-resource", atc.Version{"ref": "base"}),
		fixture.builder.WithResourceTypeVersions("shared-type", atc.Version{"ref": "base"}),
	)

	expected := atc.ResourcesAndTypes{
		Resources: atc.ResourceIdentifiers{{
			Name: "shared-resource", PipelineName: fixture.pipeline.Name(), TeamName: fixture.team.Name(),
		}},
		ResourceTypes: atc.ResourceIdentifiers{{
			Name: "shared-type", PipelineName: fixture.pipeline.Name(), TeamName: fixture.team.Name(),
		}},
	}
	for _, other := range []struct {
		teamName, pipelineName string
		public                 bool
	}{
		{teamName: "public-team", pipelineName: "public-pipeline", public: true},
		{teamName: "private-team", pipelineName: "private-pipeline"},
	} {
		team, err := fixture.database.Deps.teamFactory.CreateTeam(atc.Team{Name: other.teamName})
		Expect(err).NotTo(HaveOccurred())
		pipeline := fixture.database.SavePipeline(team, other.pipelineName, sharedConfig)
		if other.public {
			Expect(pipeline.Expose()).To(Succeed())
		}
		scenario := &dbtest.Scenario{Team: team, Pipeline: pipeline}
		scenario.Run(
			fixture.builder.WithResourceVersions("shared-resource", atc.Version{"ref": other.teamName}),
			fixture.builder.WithResourceTypeVersions("shared-type", atc.Version{"ref": other.teamName}),
		)
		expected.Resources = append(expected.Resources, atc.ResourceIdentifier{
			Name: "shared-resource", PipelineName: pipeline.Name(), TeamName: team.Name(),
		})
		expected.ResourceTypes = append(expected.ResourceTypes, atc.ResourceIdentifier{
			Name: "shared-type", PipelineName: pipeline.Name(), TeamName: team.Name(),
		})
	}
	return expected
}

func persistResourceCacheAssociation(
	fixture *resourceAPIFixture,
	worker db.Worker,
	resource db.Resource,
	version atc.Version,
	handle string,
) db.ResourceCache {
	GinkgoHelper()

	build, err := fixture.team.CreateOneOffBuild()
	Expect(err).NotTo(HaveOccurred())
	cache, err := db.NewResourceCacheFactory(fixture.database.Conn, fixture.database.LockFactory).
		FindOrCreateResourceCache(
			db.ForBuild(build.ID()), resource.Type(), version, resource.Source(), nil, nil,
		)
	Expect(err).NotTo(HaveOccurred())
	creating, err := fixture.database.Deps.volumeRepository.CreateVolumeWithHandle(
		handle, fixture.team.ID(), worker.Name(), db.VolumeTypeContainer,
	)
	Expect(err).NotTo(HaveOccurred())
	created, err := creating.Created()
	Expect(err).NotTo(HaveOccurred())
	association, err := created.InitializeResourceCache(cache)
	Expect(err).NotTo(HaveOccurred())
	Expect(association).NotTo(BeNil())
	return cache
}

func expectResourceCacheAssociationCount(fixture *resourceAPIFixture, cache db.ResourceCache, count int) {
	GinkgoHelper()

	var actual int
	Expect(fixture.database.Conn.QueryRow(
		`SELECT count(*) FROM worker_resource_caches WHERE resource_cache_id = $1`, cache.ID(),
	).Scan(&actual)).To(Succeed())
	Expect(actual).To(Equal(count))
}

var _ = Describe("Resources API", func() {
	var fixture *resourceAPIFixture

	BeforeEach(func() {
		oldCheckInterval := atc.DefaultCheckInterval
		oldWebhookInterval := atc.DefaultWebhookInterval
		oldResourceTypeInterval := atc.DefaultResourceTypeInterval
		atc.DefaultCheckInterval = time.Minute
		atc.DefaultWebhookInterval = 2 * time.Minute
		atc.DefaultResourceTypeInterval = 3 * time.Minute
		DeferCleanup(func() {
			atc.DefaultCheckInterval = oldCheckInterval
			atc.DefaultWebhookInterval = oldWebhookInterval
			atc.DefaultResourceTypeInterval = oldResourceTypeInterval
		})

		fixture = newResourceAPIFixture(defaultResourceAPIConfig())
	})

	Describe("GET /api/v1/resources", func() {
		It("returns the persisted public and authorized-private resources with their real build state", func() {
			resources := persistResourceListingGraph(fixture)
			fakeAccess.IsAuthenticatedReturns(true)
			fakeAccess.TeamNamesReturns([]string{fixture.team.Name()})

			response := requestResourceAPI(fixture, http.MethodGet, "/api/v1/resources", nil)
			Expect(response.StatusCode).To(Equal(http.StatusOK))
			Expect(response.Header.Get("Content-Type")).To(Equal("application/json"))
			presented := decodeResourceAPIResponse[[]atc.Resource](response)
			Expect(presented).To(HaveLen(2))
			Expect([]string{presented[0].Name, presented[1].Name}).To(ConsistOf(
				resources["authorized"].Name(), resources["public"].Name(),
			))
			Expect([]string{presented[0].Name, presented[1].Name}).NotTo(ContainElement(
				resources["private"].Name(),
			))

			authorized := resourceByName(presented, resources["authorized"].Name())
			Expect(authorized.PipelineID).To(Equal(fixture.pipeline.ID()))
			Expect(authorized.PipelineName).To(Equal(fixture.pipeline.Name()))
			Expect(authorized.TeamName).To(Equal(fixture.team.Name()))
			Expect(authorized.Type).To(Equal(dbtest.BaseResourceType))
			Expect(authorized.LastChecked).To(BeNumerically(">", 0))
			Expect(authorized.Build).NotTo(BeNil())
			Expect(authorized.Build.Status).To(Equal(atc.StatusSucceeded))
			Expect(authorized.Build.TeamName).To(Equal(fixture.team.Name()))
		})

		It("returns only the exposed resource to an anonymous user", func() {
			resources := persistResourceListingGraph(fixture)
			fakeAccess.IsAuthenticatedReturns(false)
			fakeAccess.TeamNamesReturns(nil)

			response := requestResourceAPI(fixture, http.MethodGet, "/api/v1/resources", nil)
			Expect(response.StatusCode).To(Equal(http.StatusOK))
			presented := decodeResourceAPIResponse[[]atc.Resource](response)
			Expect(presented).To(HaveLen(1))
			Expect(presented[0].Name).To(Equal(resources["public"].Name()))
		})

		It("returns every persisted resource to an administrator", func() {
			resources := persistResourceListingGraph(fixture)
			fakeAccess.IsAdminReturns(true)

			response := requestResourceAPI(fixture, http.MethodGet, "/api/v1/resources", nil)
			presented := decodeResourceAPIResponse[[]atc.Resource](response)
			Expect(presented).To(HaveLen(3))
			Expect([]string{presented[0].Name, presented[1].Name, presented[2].Name}).To(ConsistOf(
				resources["authorized"].Name(), resources["public"].Name(), resources["private"].Name(),
			))
		})

		It("returns an empty JSON array when no resource rows exist", func() {
			fixture.updatePipeline(atc.Config{})
			fakeAccess.TeamNamesReturns([]string{fixture.team.Name()})
			response := requestResourceAPI(fixture, http.MethodGet, "/api/v1/resources", nil)
			Expect(response.StatusCode).To(Equal(http.StatusOK))
			Expect(decodeResourceAPIResponse[[]atc.Resource](response)).To(BeEmpty())
		})

		It("returns 500 when the production resource factory connection is closed", func() {
			fixture.database.Deps.resourceFactory = fixture.doomedResourceFactory()
			response := requestResourceAPI(fixture, http.MethodGet, "/api/v1/resources", nil)
			Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
		})
	})

	Describe("GET /api/v1/teams/:team_name/pipelines/:pipeline_name/resources", func() {
		const path = "/api/v1/teams/a-team/pipelines/a-pipeline/resources"

		It("returns resources read from the authorized private pipeline", func() {
			fixture.scenario.Run(fixture.builder.WithResourceVersions(
				"resource-name", atc.Version{"ref": "persisted"},
			))
			fakeAccess.IsAuthenticatedReturns(true)
			fakeAccess.IsAuthorizedReturns(true)

			response := requestResourceAPI(fixture, http.MethodGet, path, nil)
			Expect(response.StatusCode).To(Equal(http.StatusOK))
			Expect(response.Header.Get("Content-Type")).To(Equal("application/json"))
			resources := decodeResourceAPIResponse[[]atc.Resource](response)
			Expect(resources).To(HaveLen(1))
			Expect(resources[0].Name).To(Equal("resource-name"))
			Expect(resources[0].PipelineID).To(Equal(fixture.pipeline.ID()))
			Expect(resources[0].TeamName).To(Equal(fixture.team.Name()))
			Expect(resources[0].LastChecked).To(BeNumerically(">", 0))
		})

		It("permits anonymous reads only after the persisted pipeline is exposed", func() {
			fakeAccess.IsAuthenticatedReturns(false)
			fakeAccess.IsAuthorizedReturns(false)
			privateResponse := requestResourceAPI(fixture, http.MethodGet, path, nil)
			Expect(privateResponse.StatusCode).To(Equal(http.StatusUnauthorized))

			Expect(fixture.pipeline.Expose()).To(Succeed())
			publicResponse := requestResourceAPI(fixture, http.MethodGet, path, nil)
			Expect(publicResponse.StatusCode).To(Equal(http.StatusOK))
			Expect(decodeResourceAPIResponse[[]atc.Resource](publicResponse)).To(HaveLen(1))
		})

		It("returns an empty array for a pipeline persisted without resources", func() {
			fixture.updatePipeline(atc.Config{})
			fakeAccess.IsAuthenticatedReturns(true)
			fakeAccess.IsAuthorizedReturns(true)
			response := requestResourceAPI(fixture, http.MethodGet, path, nil)
			Expect(response.StatusCode).To(Equal(http.StatusOK))
			Expect(decodeResourceAPIResponse[[]atc.Resource](response)).To(BeEmpty())
		})

		It("returns 500 when the production pipeline resource query fails", func() {
			fakeAccess.IsAuthenticatedReturns(true)
			fakeAccess.IsAuthorizedReturns(true)
			fixture.overridePipeline(fixture.doomedPipeline())
			response := requestResourceAPI(fixture, http.MethodGet, path, nil)
			Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
		})
	})

	Describe("GET /api/v1/teams/:team_name/pipelines/:pipeline_name/resource-types", func() {
		const path = "/api/v1/teams/a-team/pipelines/a-pipeline/resource-types"

		It("returns the resource types stored in the authorized pipeline", func() {
			config := defaultResourceAPIConfig()
			config.ResourceTypes[0].CheckEvery = &atc.CheckEvery{Interval: 10 * time.Millisecond}
			fixture.updatePipeline(config)
			fakeAccess.IsAuthenticatedReturns(true)
			fakeAccess.IsAuthorizedReturns(true)
			response := requestResourceAPI(fixture, http.MethodGet, path, nil)
			Expect(response.StatusCode).To(Equal(http.StatusOK))
			Expect(response.Header.Get("Content-Type")).To(Equal("application/json"))
			resourceTypes := decodeResourceAPIResponse[atc.ResourceTypes](response)
			Expect(resourceTypes).To(ConsistOf(atc.ResourceType{
				Name:       "resource-type-name",
				Type:       dbtest.BaseResourceType,
				Source:     atc.Source{"repository": "resource-type"},
				Defaults:   atc.Source{"branch": "main"},
				Privileged: true,
				CheckEvery: &atc.CheckEvery{Interval: 10 * time.Millisecond},
				Tags:       atc.Tags{"resource-type-worker"},
				Params:     atc.Params{"resource-type-param": "persisted"},
			}))
		})

		It("permits anonymous reads only for an exposed pipeline", func() {
			fakeAccess.IsAuthenticatedReturns(false)
			fakeAccess.IsAuthorizedReturns(false)
			Expect(requestResourceAPI(fixture, http.MethodGet, path, nil).StatusCode).To(Equal(http.StatusUnauthorized))
			Expect(fixture.pipeline.Expose()).To(Succeed())
			response := requestResourceAPI(fixture, http.MethodGet, path, nil)
			Expect(response.StatusCode).To(Equal(http.StatusOK))
			Expect(decodeResourceAPIResponse[atc.ResourceTypes](response)).To(HaveLen(1))
		})

		It("returns an empty array when the real pipeline has no resource types", func() {
			fixture.updatePipeline(atc.Config{})
			fakeAccess.IsAuthenticatedReturns(true)
			fakeAccess.IsAuthorizedReturns(true)
			response := requestResourceAPI(fixture, http.MethodGet, path, nil)
			Expect(response.StatusCode).To(Equal(http.StatusOK))
			Expect(decodeResourceAPIResponse[atc.ResourceTypes](response)).To(BeEmpty())
		})

		It("returns 500 when the production resource-type query fails", func() {
			fakeAccess.IsAuthenticatedReturns(true)
			fakeAccess.IsAuthorizedReturns(true)
			fixture.overridePipeline(fixture.doomedPipeline())
			response := requestResourceAPI(fixture, http.MethodGet, path, nil)
			Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
		})
	})

	Describe("GET /api/v1/teams/:team_name/pipelines/:pipeline_name/resources/:resource_name", func() {
		const path = "/api/v1/teams/a-team/pipelines/a-pipeline/resources/resource-name"

		BeforeEach(func() {
			fakeAccess.IsAuthenticatedReturns(true)
			fakeAccess.IsAuthorizedReturns(true)
		})

		It("returns the persisted resource, last check, and completed check build", func() {
			fixture.scenario.Run(fixture.builder.WithResourceVersions(
				"resource-name", atc.Version{"ref": "persisted"},
			))
			response := requestResourceAPI(fixture, http.MethodGet, path, nil)
			Expect(response.StatusCode).To(Equal(http.StatusOK))
			Expect(response.Header.Get("Content-Type")).To(Equal("application/json"))
			resource := decodeResourceAPIResponse[atc.Resource](response)
			Expect(resource.Name).To(Equal("resource-name"))
			Expect(resource.PipelineID).To(Equal(fixture.pipeline.ID()))
			Expect(resource.TeamName).To(Equal(fixture.team.Name()))
			Expect(resource.Type).To(Equal(dbtest.BaseResourceType))
			Expect(resource.Icon).To(Equal("git"))
			Expect(resource.LastChecked).To(BeNumerically(">", 0))
			Expect(resource.Build).NotTo(BeNil())
			Expect(resource.Build.Status).To(Equal(atc.StatusSucceeded))
		})

		It("reports a version pinned through persisted pipeline config", func() {
			config := defaultResourceAPIConfig()
			config.Resources[0].Version = atc.Version{"ref": "configured"}
			fixture.updatePipeline(config)
			response := requestResourceAPI(fixture, http.MethodGet, path, nil)
			resource := decodeResourceAPIResponse[atc.Resource](response)
			Expect(resource.PinnedVersion).To(Equal(atc.Version{"ref": "configured"}))
			Expect(resource.PinnedInConfig).To(BeTrue())
		})

		It("reports a real API pin and pin comment", func() {
			version := atc.Version{"ref": "api-pinned"}
			fixture.scenario.Run(fixture.builder.WithResourceVersions("resource-name", version))
			resource := fixture.scenario.Resource("resource-name")
			pinned, err := resource.PinVersion(fixture.scenario.ResourceVersion("resource-name", version).ID())
			Expect(err).NotTo(HaveOccurred())
			Expect(pinned).To(BeTrue())
			Expect(resource.SetPinComment("release candidate")).To(Succeed())

			response := requestResourceAPI(fixture, http.MethodGet, path, nil)
			presented := decodeResourceAPIResponse[atc.Resource](response)
			Expect(presented.PinnedVersion).To(Equal(version))
			Expect(presented.PinnedInConfig).To(BeFalse())
			Expect(presented.PinComment).To(Equal("release candidate"))
		})

		It("returns 404 for a resource absent from the persisted pipeline", func() {
			response := requestResourceAPI(
				fixture, http.MethodGet,
				"/api/v1/teams/a-team/pipelines/a-pipeline/resources/missing", nil,
			)
			Expect(response.StatusCode).To(Equal(http.StatusNotFound))
		})

		It("returns 500 when the production resource lookup connection is closed", func() {
			fixture.overridePipeline(fixture.doomedPipeline())
			response := requestResourceAPI(fixture, http.MethodGet, path, nil)
			Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
		})

		It("enforces persisted pipeline visibility", func() {
			fakeAccess.IsAuthenticatedReturns(false)
			fakeAccess.IsAuthorizedReturns(false)
			Expect(requestResourceAPI(fixture, http.MethodGet, path, nil).StatusCode).To(Equal(http.StatusUnauthorized))

			fakeAccess.IsAuthenticatedReturns(true)
			Expect(requestResourceAPI(fixture, http.MethodGet, path, nil).StatusCode).To(Equal(http.StatusForbidden))

			fakeAccess.IsAuthenticatedReturns(false)
			Expect(fixture.pipeline.Expose()).To(Succeed())
			Expect(requestResourceAPI(fixture, http.MethodGet, path, nil).StatusCode).To(Equal(http.StatusOK))
		})
	})

	Describe("PUT resource pin mutations", func() {
		const (
			unpinPath   = "/api/v1/teams/a-team/pipelines/a-pipeline/resources/resource-name/unpin"
			commentPath = "/api/v1/teams/a-team/pipelines/a-pipeline/resources/resource-name/pin_comment"
		)

		BeforeEach(func() {
			fakeAccess.IsAuthenticatedReturns(true)
			fakeAccess.IsAuthorizedReturns(true)
		})

		It("unpins the persisted API pin", func() {
			version := atc.Version{"ref": "pinned"}
			fixture.scenario.Run(fixture.builder.WithResourceVersions("resource-name", version))
			resource := fixture.scenario.Resource("resource-name")
			pinned, err := resource.PinVersion(fixture.scenario.ResourceVersion("resource-name", version).ID())
			Expect(err).NotTo(HaveOccurred())
			Expect(pinned).To(BeTrue())

			response := requestResourceAPI(fixture, http.MethodPut, unpinPath, nil)
			Expect(response.StatusCode).To(Equal(http.StatusOK))
			found, err := resource.Reload()
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(resource.APIPinnedVersion()).To(BeNil())
		})

		It("returns 500 when no persisted pin row can be removed", func() {
			response := requestResourceAPI(fixture, http.MethodPut, unpinPath, nil)
			Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
		})

		It("returns 500 when the production unpin resource lookup fails", func() {
			fixture.overridePipeline(fixture.doomedPipeline())
			response := requestResourceAPI(fixture, http.MethodPut, unpinPath, nil)
			Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
		})

		It("sets a comment on an existing persisted pin", func() {
			version := atc.Version{"ref": "commented"}
			fixture.scenario.Run(fixture.builder.WithResourceVersions("resource-name", version))
			resource := fixture.scenario.Resource("resource-name")
			pinned, err := resource.PinVersion(fixture.scenario.ResourceVersion("resource-name", version).ID())
			Expect(err).NotTo(HaveOccurred())
			Expect(pinned).To(BeTrue())

			response := requestResourceAPI(
				fixture, http.MethodPut, commentPath,
				resourceAPIJSONBody(atc.SetPinCommentRequestBody{PinComment: "promote after soak"}),
			)
			Expect(response.StatusCode).To(Equal(http.StatusOK))
			found, err := resource.Reload()
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(resource.APIPinnedVersion()).To(Equal(version))
			Expect(resource.PinComment()).To(Equal("promote after soak"))
		})

		It("returns 400 for malformed pin-comment JSON", func() {
			response := requestResourceAPI(fixture, http.MethodPut, commentPath, strings.NewReader("{"))
			Expect(response.StatusCode).To(Equal(http.StatusBadRequest))
		})

		It("returns 500 when a preloaded real resource loses its connection", func() {
			doomed := fixture.doomedResource("resource-name")
			fixture.overridePipeline(resourceAPIPipeline{
				Pipeline: fixture.pipeline, resource: doomed,
				resourceOverrideName: "resource-name",
			})
			response := requestResourceAPI(
				fixture, http.MethodPut, commentPath,
				resourceAPIJSONBody(atc.SetPinCommentRequestBody{PinComment: "cannot persist"}),
			)
			Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
		})

		It("returns 500 when the production pin-comment resource lookup fails", func() {
			fixture.overridePipeline(fixture.doomedPipeline())
			response := requestResourceAPI(
				fixture, http.MethodPut, commentPath,
				resourceAPIJSONBody(atc.SetPinCommentRequestBody{PinComment: "cannot look up"}),
			)
			Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
		})

		It("preserves pin-comment missing and authorization statuses", func() {
			body := func() io.Reader {
				return resourceAPIJSONBody(atc.SetPinCommentRequestBody{PinComment: "irrelevant"})
			}
			missing := "/api/v1/teams/a-team/pipelines/a-pipeline/resources/missing/pin_comment"
			Expect(requestResourceAPI(fixture, http.MethodPut, missing, body()).StatusCode).To(Equal(http.StatusNotFound))

			fakeAccess.IsAuthorizedReturns(false)
			Expect(requestResourceAPI(fixture, http.MethodPut, commentPath, body()).StatusCode).To(Equal(http.StatusForbidden))
			fakeAccess.IsAuthenticatedReturns(false)
			Expect(requestResourceAPI(fixture, http.MethodPut, commentPath, body()).StatusCode).To(Equal(http.StatusUnauthorized))
		})

		It("returns natural lookup and authorization statuses", func() {
			missing := "/api/v1/teams/a-team/pipelines/a-pipeline/resources/missing/unpin"
			Expect(requestResourceAPI(fixture, http.MethodPut, missing, nil).StatusCode).To(Equal(http.StatusNotFound))

			fakeAccess.IsAuthorizedReturns(false)
			Expect(requestResourceAPI(fixture, http.MethodPut, unpinPath, nil).StatusCode).To(Equal(http.StatusForbidden))
			fakeAccess.IsAuthenticatedReturns(false)
			Expect(requestResourceAPI(fixture, http.MethodPut, unpinPath, nil).StatusCode).To(Equal(http.StatusUnauthorized))
		})
	})

	Describe("DELETE /api/v1/teams/:team_name/pipelines/:pipeline_name/resources/:resource_name/cache", func() {
		const path = "/api/v1/teams/a-team/pipelines/a-pipeline/resources/resource-name/cache"

		BeforeEach(func() {
			fakeAccess.IsAuthenticatedReturns(true)
			fakeAccess.IsAuthorizedReturns(true)
		})

		It("removes only the worker-cache association matching the requested version", func() {
			fixture.scenario.Run(fixture.builder.WithResourceVersions(
				"resource-name", atc.Version{"ref": "scope"},
			))
			resource := fixture.scenario.Resource("resource-name")
			worker := fixture.scenario.Workers[0]
			matchingVersion := atc.Version{"ref": "matching"}
			matching := persistResourceCacheAssociation(fixture, worker, resource, matchingVersion, "matching-cache-volume")
			decoy := persistResourceCacheAssociation(
				fixture, worker, resource, atc.Version{"ref": "decoy"}, "decoy-cache-volume",
			)
			expectResourceCacheAssociationCount(fixture, matching, 1)
			expectResourceCacheAssociationCount(fixture, decoy, 1)

			response := requestResourceAPI(
				fixture, http.MethodDelete, path,
				resourceAPIJSONBody(atc.VersionDeleteBody{Version: matchingVersion}),
			)
			Expect(response.StatusCode).To(Equal(http.StatusOK))
			Expect(response.Header.Get("Content-Type")).To(Equal("application/json"))
			Expect(decodeResourceAPIResponse[atc.ClearResourceCacheResponse](response).CachesRemoved).To(Equal(int64(1)))
			expectResourceCacheAssociationCount(fixture, matching, 0)
			expectResourceCacheAssociationCount(fixture, decoy, 1)
		})

		It("removes every persisted worker-cache association when no version is supplied", func() {
			fixture.scenario.Run(fixture.builder.WithResourceVersions(
				"resource-name", atc.Version{"ref": "scope"},
			))
			resource := fixture.scenario.Resource("resource-name")
			worker := fixture.scenario.Workers[0]
			first := persistResourceCacheAssociation(
				fixture, worker, resource, atc.Version{"ref": "first"}, "all-cache-volume-first",
			)
			second := persistResourceCacheAssociation(
				fixture, worker, resource, atc.Version{"ref": "second"}, "all-cache-volume-second",
			)
			expectResourceCacheAssociationCount(fixture, first, 1)
			expectResourceCacheAssociationCount(fixture, second, 1)

			response := requestResourceAPI(fixture, http.MethodDelete, path, nil)
			Expect(response.StatusCode).To(Equal(http.StatusOK))
			Expect(response.Header.Get("Content-Type")).To(Equal("application/json"))
			Expect(decodeResourceAPIResponse[atc.ClearResourceCacheResponse](response).CachesRemoved).To(Equal(int64(2)))
			expectResourceCacheAssociationCount(fixture, first, 0)
			expectResourceCacheAssociationCount(fixture, second, 0)
		})

		It("returns zero when no persisted worker cache matches", func() {
			response := requestResourceAPI(fixture, http.MethodDelete, path, nil)
			Expect(response.StatusCode).To(Equal(http.StatusOK))
			Expect(decodeResourceAPIResponse[atc.ClearResourceCacheResponse](response).CachesRemoved).To(BeZero())
		})

		It("returns 500 when a preloaded resource loses its connection", func() {
			doomed := fixture.doomedResource("resource-name")
			fixture.overridePipeline(resourceAPIPipeline{
				Pipeline: fixture.pipeline, resource: doomed,
				resourceOverrideName: "resource-name",
			})
			response := requestResourceAPI(fixture, http.MethodDelete, path, nil)
			Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
		})

		It("returns 500 when the production cache resource lookup fails", func() {
			fixture.overridePipeline(fixture.doomedPipeline())
			response := requestResourceAPI(fixture, http.MethodDelete, path, nil)
			Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
		})

		It("preserves cache authorization statuses", func() {
			fakeAccess.IsAuthorizedReturns(false)
			Expect(requestResourceAPI(fixture, http.MethodDelete, path, nil).StatusCode).To(Equal(http.StatusForbidden))
			fakeAccess.IsAuthenticatedReturns(false)
			Expect(requestResourceAPI(fixture, http.MethodDelete, path, nil).StatusCode).To(Equal(http.StatusUnauthorized))
		})

		It("returns 400 for malformed JSON and 404 for an absent resource", func() {
			Expect(requestResourceAPI(fixture, http.MethodDelete, path, strings.NewReader("{")).StatusCode).To(Equal(http.StatusBadRequest))
			missing := "/api/v1/teams/a-team/pipelines/a-pipeline/resources/missing/cache"
			Expect(requestResourceAPI(fixture, http.MethodDelete, missing, nil).StatusCode).To(Equal(http.StatusNotFound))
		})
	})

	Describe("manual resource, resource-type, and prototype checks", func() {
		BeforeEach(func() {
			fakeAccess.IsAuthenticatedReturns(true)
			fakeAccess.IsAuthorizedReturns(true)
		})

		DescribeTable("persists a started manual check with the forwarded request and private plan",
			func(kind, path string) {
				factory := &resourceAPICheckFactory{CheckFactory: fixture.database.Deps.checkFactory}
				fixture.database.Deps.checkFactory = factory
				from := atc.Version{"ref": kind + "-from"}
				response := requestResourceAPI(
					fixture, http.MethodPost, path,
					resourceAPIJSONBody(atc.CheckRequestBody{From: from, Shallow: true}),
				)
				Expect(response.StatusCode).To(Equal(http.StatusCreated))
				presented := decodeResourceAPIResponse[atc.Build](response)
				Expect(presented.ID).To(BeNumerically(">", 0))
				Expect(presented.Status).To(Equal(atc.StatusStarted))
				Expect(presented.TeamName).To(Equal(fixture.team.Name()))

				calls := factory.Calls()
				Expect(calls).To(HaveLen(1))
				call := calls[0]
				Expect(call.from).To(Equal(from))
				Expect(call.manuallyTriggered).To(BeTrue())
				Expect(call.skipIntervalRecursively).To(BeFalse())
				Expect(call.toDB).To(BeTrue())
				Expect(call.checkable.Name()).To(Equal(kind + "-name"))

				build, found, err := fixture.database.Deps.buildFactory.Build(presented.ID)
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(build.Status()).To(Equal(db.BuildStatusStarted))
				Expect(build.IsCompleted()).To(BeFalse())
				Expect(build.IsManuallyTriggered()).To(BeTrue())
				Expect(build.StartTime()).NotTo(BeZero())
				privatePlan := build.PrivatePlan()
				Expect(privatePlan.ID).NotTo(BeEmpty())
				expectedCheck := atc.CheckPlan{
					Type:         dbtest.BaseResourceType,
					TypeImage:    atc.TypeImage{BaseType: dbtest.BaseResourceType},
					FromVersion:  from,
					SkipInterval: true,
				}
				switch kind {
				case "resource":
					Expect(build.ResourceID()).To(Equal(call.checkable.(db.Resource).ID()))
					expectedCheck.Name = "resource-name"
					expectedCheck.Source = atc.Source{"repository": "primary"}
					expectedCheck.Resource = "resource-name"
					expectedCheck.Interval = atc.CheckEvery{Interval: 2 * time.Minute}
					expectedCheck.Tags = atc.Tags{"resource-worker"}
					expectedCheck.Timeout = "4m"
				case "resource-type":
					Expect(build.ResourceTypeID()).To(Equal(call.checkable.(db.ResourceType).ID()))
					expectedCheck.Name = "resource-type-name"
					expectedCheck.Source = atc.Source{"repository": "resource-type"}
					expectedCheck.ResourceType = "resource-type-name"
					expectedCheck.Interval = atc.CheckEvery{Interval: 3 * time.Minute}
					expectedCheck.Tags = atc.Tags{"resource-type-worker"}
				case "prototype":
					Expect(build.ResourceID()).To(BeZero())
					Expect(build.ResourceTypeID()).To(BeZero())
					expectedCheck.Name = "prototype-name"
					expectedCheck.Source = atc.Source{"repository": "prototype"}
					expectedCheck.Prototype = "prototype-name"
					expectedCheck.Interval = atc.CheckEvery{Interval: time.Minute}
					expectedCheck.Tags = atc.Tags{"prototype-worker"}
				}
				Expect(privatePlan.Check).To(Equal(&expectedCheck))
			},
			Entry("resource", "resource", "/api/v1/teams/a-team/pipelines/a-pipeline/resources/resource-name/check"),
			Entry("resource type", "resource-type", "/api/v1/teams/a-team/pipelines/a-pipeline/resource-types/resource-type-name/check"),
			Entry("prototype", "prototype", "/api/v1/teams/a-team/pipelines/a-pipeline/prototypes/prototype-name/check"),
		)

		DescribeTable("returns 500 for the explicit check-factory error boundary",
			func(path string) {
				factory := &resourceAPICheckFactory{
					CheckFactory: fixture.database.Deps.checkFactory,
					err:          errors.New("configured check failure"),
				}
				fixture.database.Deps.checkFactory = factory
				response := requestResourceAPI(
					fixture, http.MethodPost, path, resourceAPIJSONBody(atc.CheckRequestBody{}),
				)
				Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
				body, err := io.ReadAll(response.Body)
				Expect(err).NotTo(HaveOccurred())
				Expect(string(body)).To(ContainSubstring("configured check failure"))
				Expect(factory.Calls()).To(HaveLen(1))
			},
			Entry("resource", "/api/v1/teams/a-team/pipelines/a-pipeline/resources/resource-name/check"),
			Entry("resource type", "/api/v1/teams/a-team/pipelines/a-pipeline/resource-types/resource-type-name/check"),
			Entry("prototype", "/api/v1/teams/a-team/pipelines/a-pipeline/prototypes/prototype-name/check"),
		)

		DescribeTable("returns 500 for the impossible manual-check not-created result",
			func(path string) {
				factory := &resourceAPICheckFactory{
					CheckFactory: fixture.database.Deps.checkFactory,
					notCreated:   true,
				}
				fixture.database.Deps.checkFactory = factory
				response := requestResourceAPI(
					fixture, http.MethodPost, path, resourceAPIJSONBody(atc.CheckRequestBody{}),
				)
				Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
				Expect(factory.Calls()).To(HaveLen(1))
				var builds int
				Expect(fixture.database.Conn.QueryRow(`SELECT count(*) FROM builds`).Scan(&builds)).To(Succeed())
				Expect(builds).To(BeZero())
			},
			Entry("resource", "/api/v1/teams/a-team/pipelines/a-pipeline/resources/resource-name/check"),
			Entry("resource type", "/api/v1/teams/a-team/pipelines/a-pipeline/resource-types/resource-type-name/check"),
			Entry("prototype", "/api/v1/teams/a-team/pipelines/a-pipeline/prototypes/prototype-name/check"),
		)

		DescribeTable("forwards recursive checking when shallow is false",
			func(path string) {
				factory := &resourceAPICheckFactory{CheckFactory: fixture.database.Deps.checkFactory}
				fixture.database.Deps.checkFactory = factory
				response := requestResourceAPI(
					fixture, http.MethodPost, path, resourceAPIJSONBody(atc.CheckRequestBody{}),
				)
				Expect(response.StatusCode).To(Equal(http.StatusCreated))
				Expect(factory.Calls()).To(HaveLen(1))
				Expect(factory.Calls()[0].skipIntervalRecursively).To(BeTrue())
			},
			Entry("resource", "/api/v1/teams/a-team/pipelines/a-pipeline/resources/resource-name/check"),
			Entry("resource type", "/api/v1/teams/a-team/pipelines/a-pipeline/resource-types/resource-type-name/check"),
			Entry("prototype", "/api/v1/teams/a-team/pipelines/a-pipeline/prototypes/prototype-name/check"),
		)

		DescribeTable("preserves each check handler's missing and authorization statuses",
			func(path, missingPath string) {
				Expect(requestResourceAPI(
					fixture, http.MethodPost, missingPath, resourceAPIJSONBody(atc.CheckRequestBody{}),
				).StatusCode).To(Equal(http.StatusNotFound))
				fakeAccess.IsAuthorizedReturns(false)
				Expect(requestResourceAPI(
					fixture, http.MethodPost, path, resourceAPIJSONBody(atc.CheckRequestBody{}),
				).StatusCode).To(Equal(http.StatusForbidden))
				fakeAccess.IsAuthenticatedReturns(false)
				Expect(requestResourceAPI(
					fixture, http.MethodPost, path, resourceAPIJSONBody(atc.CheckRequestBody{}),
				).StatusCode).To(Equal(http.StatusUnauthorized))
			},
			Entry(
				"resource",
				"/api/v1/teams/a-team/pipelines/a-pipeline/resources/resource-name/check",
				"/api/v1/teams/a-team/pipelines/a-pipeline/resources/missing/check",
			),
			Entry(
				"resource type",
				"/api/v1/teams/a-team/pipelines/a-pipeline/resource-types/resource-type-name/check",
				"/api/v1/teams/a-team/pipelines/a-pipeline/resource-types/missing/check",
			),
			Entry(
				"prototype",
				"/api/v1/teams/a-team/pipelines/a-pipeline/prototypes/prototype-name/check",
				"/api/v1/teams/a-team/pipelines/a-pipeline/prototypes/missing/check",
			),
		)

		It("returns 400 for malformed resource-check JSON", func() {
			path := "/api/v1/teams/a-team/pipelines/a-pipeline/resources/resource-name/check"
			Expect(requestResourceAPI(fixture, http.MethodPost, path, strings.NewReader("{")).StatusCode).To(Equal(http.StatusBadRequest))
		})

		DescribeTable("returns 500 when the selected production lookup connection is closed",
			func(path string) {
				fixture.overridePipeline(fixture.doomedPipeline())
				response := requestResourceAPI(
					fixture, http.MethodPost, path, resourceAPIJSONBody(atc.CheckRequestBody{}),
				)
				Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
			},
			Entry("resource", "/api/v1/teams/a-team/pipelines/a-pipeline/resources/resource-name/check"),
			Entry("resource type", "/api/v1/teams/a-team/pipelines/a-pipeline/resource-types/resource-type-name/check"),
			Entry("prototype", "/api/v1/teams/a-team/pipelines/a-pipeline/prototypes/prototype-name/check"),
		)

		DescribeTable("returns 500 when resource-type expansion fails after a real checkable lookup",
			func(path string) {
				fixture.overridePipeline(resourceAPIPipeline{
					Pipeline: fixture.pipeline, resourceTypesPipeline: fixture.doomedPipeline(),
				})
				response := requestResourceAPI(
					fixture, http.MethodPost, path, resourceAPIJSONBody(atc.CheckRequestBody{}),
				)
				Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
			},
			Entry("resource", "/api/v1/teams/a-team/pipelines/a-pipeline/resources/resource-name/check"),
			Entry("resource type", "/api/v1/teams/a-team/pipelines/a-pipeline/resource-types/resource-type-name/check"),
			Entry("prototype", "/api/v1/teams/a-team/pipelines/a-pipeline/prototypes/prototype-name/check"),
		)
	})

	Describe("POST resource check webhook", func() {
		const basePath = "/api/v1/teams/a-team/pipelines/a-pipeline/resources/resource-name/check/webhook"

		It("uses the literal token from persisted resource config and creates a real check", func() {
			factory := &resourceAPICheckFactory{CheckFactory: fixture.database.Deps.checkFactory}
			fixture.database.Deps.checkFactory = factory
			response := requestResourceAPI(
				fixture, http.MethodPost, basePath+"?webhook_token=webhook-token", nil,
			)
			Expect(response.StatusCode).To(Equal(http.StatusCreated))
			presented := decodeResourceAPIResponse[atc.Build](response)
			build, found, err := fixture.database.Deps.buildFactory.Build(presented.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(build.IsManuallyTriggered()).To(BeTrue())
			Expect(build.Status()).To(Equal(db.BuildStatusStarted))
			Expect(build.PrivatePlan().Check.Resource).To(Equal("resource-name"))
			calls := factory.Calls()
			Expect(calls).To(HaveLen(1))
			Expect(calls[0].from).To(BeNil())
			Expect(calls[0].manuallyTriggered).To(BeTrue())
			Expect(calls[0].skipIntervalRecursively).To(BeFalse())
			Expect(calls[0].toDB).To(BeTrue())
		})

		It("rejects a missing or incorrect webhook token", func() {
			Expect(requestResourceAPI(fixture, http.MethodPost, basePath, nil).StatusCode).To(Equal(http.StatusBadRequest))
			Expect(requestResourceAPI(
				fixture, http.MethodPost, basePath+"?webhook_token=wrong", nil,
			).StatusCode).To(Equal(http.StatusUnauthorized))
		})

		It("returns 500 when the production webhook resource lookup fails", func() {
			fixture.overridePipeline(fixture.doomedPipeline())
			response := requestResourceAPI(
				fixture, http.MethodPost, basePath+"?webhook_token=webhook-token", nil,
			)
			Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
		})

		It("returns 404 when the persisted webhook resource is missing", func() {
			path := "/api/v1/teams/a-team/pipelines/a-pipeline/resources/missing/check/webhook?webhook_token=webhook-token"
			response := requestResourceAPI(fixture, http.MethodPost, path, nil)
			Expect(response.StatusCode).To(Equal(http.StatusNotFound))
		})

		It("returns 500 when webhook resource-type expansion loses its production connection", func() {
			fixture.overridePipeline(resourceAPIPipeline{
				Pipeline: fixture.pipeline, resourceTypesPipeline: fixture.doomedPipeline(),
			})
			response := requestResourceAPI(
				fixture, http.MethodPost, basePath+"?webhook_token=webhook-token", nil,
			)
			Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
		})

		It("uses the same two narrow CheckFactory failure boundaries", func() {
			factory := &resourceAPICheckFactory{
				CheckFactory: fixture.database.Deps.checkFactory,
				err:          errors.New("webhook check failure"),
			}
			fixture.database.Deps.checkFactory = factory
			response := requestResourceAPI(
				fixture, http.MethodPost, basePath+"?webhook_token=webhook-token", nil,
			)
			Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))

			factory.err = nil
			factory.notCreated = true
			response = requestResourceAPI(
				fixture, http.MethodPost, basePath+"?webhook_token=webhook-token", nil,
			)
			Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
		})
	})

	Describe("shared resources and resource types", func() {
		BeforeEach(func() {
			fakeAccess.IsAuthenticatedReturns(true)
			fakeAccess.IsAdminReturns(true)
			fakeAccess.TeamNamesReturns([]string{fixture.team.Name()})
		})

		It("returns the public, authorized-private, and unauthorized-private resources sharing a real scope", func() {
			expected := persistSharedResources(fixture)
			response := requestResourceAPI(
				fixture, http.MethodGet,
				"/api/v1/teams/a-team/pipelines/a-pipeline/resources/shared-resource/shared", nil,
			)
			Expect(response.StatusCode).To(Equal(http.StatusOK))
			Expect(response.Header.Get("Content-Type")).To(Equal("application/json"))
			actual := decodeResourceAPIResponse[atc.ResourcesAndTypes](response)
			Expect(actual.Resources).To(ConsistOf(expected.Resources))
			Expect(actual.ResourceTypes).To(ConsistOf(expected.ResourceTypes))
		})

		It("returns the resources and types sharing a real resource-type scope", func() {
			expected := persistSharedResources(fixture)
			response := requestResourceAPI(
				fixture, http.MethodGet,
				"/api/v1/teams/a-team/pipelines/a-pipeline/resource-types/shared-type/shared", nil,
			)
			Expect(response.StatusCode).To(Equal(http.StatusOK))
			Expect(response.Header.Get("Content-Type")).To(Equal("application/json"))
			actual := decodeResourceAPIResponse[atc.ResourcesAndTypes](response)
			Expect(actual.Resources).To(ConsistOf(expected.Resources))
			Expect(actual.ResourceTypes).To(ConsistOf(expected.ResourceTypes))
		})

		It("returns 500 naturally when a resource or type has no scope", func() {
			resourceResponse := requestResourceAPI(
				fixture, http.MethodGet,
				"/api/v1/teams/a-team/pipelines/a-pipeline/resources/resource-name/shared", nil,
			)
			Expect(resourceResponse.StatusCode).To(Equal(http.StatusInternalServerError))
			resourceBody, err := io.ReadAll(resourceResponse.Body)
			Expect(err).NotTo(HaveOccurred())
			Expect(resourceBody).NotTo(BeEmpty())
			typeResponse := requestResourceAPI(
				fixture, http.MethodGet,
				"/api/v1/teams/a-team/pipelines/a-pipeline/resource-types/resource-type-name/shared", nil,
			)
			Expect(typeResponse.StatusCode).To(Equal(http.StatusInternalServerError))
			typeBody, err := io.ReadAll(typeResponse.Body)
			Expect(err).NotTo(HaveOccurred())
			Expect(typeBody).NotTo(BeEmpty())
		})

		It("returns 500 when preloaded shared objects lose their secondary connections", func() {
			doomedResource := fixture.doomedResource("resource-name")
			fixture.overridePipeline(resourceAPIPipeline{
				Pipeline: fixture.pipeline, resource: doomedResource,
				resourceOverrideName: "resource-name",
			})
			resourceResponse := requestResourceAPI(
				fixture, http.MethodGet,
				"/api/v1/teams/a-team/pipelines/a-pipeline/resources/resource-name/shared", nil,
			)
			Expect(resourceResponse.StatusCode).To(Equal(http.StatusInternalServerError))
			resourceBody, err := io.ReadAll(resourceResponse.Body)
			Expect(err).NotTo(HaveOccurred())
			Expect(resourceBody).NotTo(BeEmpty())

			fixture.database.Deps.teamFactory = db.NewTeamFactory(fixture.database.Conn, fixture.database.LockFactory)
			doomedType := fixture.doomedResourceType("resource-type-name")
			fixture.overridePipeline(resourceAPIPipeline{
				Pipeline: fixture.pipeline, resourceType: doomedType,
				resourceTypeOverrideName: "resource-type-name",
			})
			typeResponse := requestResourceAPI(
				fixture, http.MethodGet,
				"/api/v1/teams/a-team/pipelines/a-pipeline/resource-types/resource-type-name/shared", nil,
			)
			Expect(typeResponse.StatusCode).To(Equal(http.StatusInternalServerError))
			typeBody, err := io.ReadAll(typeResponse.Body)
			Expect(err).NotTo(HaveOccurred())
			Expect(typeBody).NotTo(BeEmpty())
		})

		DescribeTable("returns 500 when the selected production shared lookup connection is closed",
			func(path string) {
				fixture.overridePipeline(fixture.doomedPipeline())
				response := requestResourceAPI(fixture, http.MethodGet, path, nil)
				Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
			},
			Entry("resource", "/api/v1/teams/a-team/pipelines/a-pipeline/resources/resource-name/shared"),
			Entry("resource type", "/api/v1/teams/a-team/pipelines/a-pipeline/resource-types/resource-type-name/shared"),
		)

		DescribeTable("requires authentication and administrator access",
			func(path string) {
				fakeAccess.IsAdminReturns(false)
				Expect(requestResourceAPI(fixture, http.MethodGet, path, nil).StatusCode).To(Equal(http.StatusForbidden))
				fakeAccess.IsAuthenticatedReturns(false)
				Expect(requestResourceAPI(fixture, http.MethodGet, path, nil).StatusCode).To(Equal(http.StatusUnauthorized))
			},
			Entry("resource", "/api/v1/teams/a-team/pipelines/a-pipeline/resources/resource-name/shared"),
			Entry("resource type", "/api/v1/teams/a-team/pipelines/a-pipeline/resource-types/resource-type-name/shared"),
		)

		It("returns natural 404s for missing persisted resources and types", func() {
			resourcePath := "/api/v1/teams/a-team/pipelines/a-pipeline/resources/missing/shared"
			typePath := "/api/v1/teams/a-team/pipelines/a-pipeline/resource-types/missing/shared"
			Expect(requestResourceAPI(fixture, http.MethodGet, resourcePath, nil).StatusCode).To(Equal(http.StatusNotFound))
			Expect(requestResourceAPI(fixture, http.MethodGet, typePath, nil).StatusCode).To(Equal(http.StatusNotFound))
		})
	})
})
