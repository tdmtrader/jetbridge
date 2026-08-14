package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"
	. "github.com/concourse/concourse/atc/testhelpers"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type versionsAPIFixture struct {
	database *realDB
	builder  dbtest.Builder
	team     db.Team
	pipeline db.Pipeline
	ref      atc.PipelineRef
	scenario *dbtest.Scenario
	server   *httptest.Server
}

type versionsAPIMutationVersions struct {
	target   atc.Version
	targetID int
	decoy    atc.Version
	decoyID  int
}

func newVersionsAPIFixture(ref atc.PipelineRef, config atc.Config) *versionsAPIFixture {
	GinkgoHelper()

	database := useRealDB()
	team, err := database.Deps.teamFactory.CreateTeam(atc.Team{Name: "a-team"})
	Expect(err).NotTo(HaveOccurred())
	pipeline, _, err := team.SavePipeline(ref, config, db.ConfigVersion(0), false)
	Expect(err).NotTo(HaveOccurred())
	builder := dbtest.NewBuilder(database.Conn, database.LockFactory)

	return &versionsAPIFixture{
		database: database,
		builder:  builder,
		team:     team,
		pipeline: pipeline,
		ref: atc.PipelineRef{
			Name:         pipeline.Name(),
			InstanceVars: pipeline.InstanceVars(),
		},
		scenario: &dbtest.Scenario{Team: team, Pipeline: pipeline},
	}
}

func (fixture *versionsAPIFixture) updatePipeline(config atc.Config) {
	GinkgoHelper()

	pipeline, _, err := fixture.team.SavePipeline(
		fixture.ref, config, fixture.pipeline.ConfigVersion(), false,
	)
	Expect(err).NotTo(HaveOccurred())
	fixture.pipeline = pipeline
	fixture.scenario.Pipeline = pipeline
}

func (fixture *versionsAPIFixture) resource(name string) db.Resource {
	GinkgoHelper()

	resource, found, err := fixture.pipeline.Resource(name)
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue(), "resource %q not found", name)
	return resource
}

func (fixture *versionsAPIFixture) resourceType(name string) db.ResourceType {
	GinkgoHelper()

	resourceType, found, err := fixture.pipeline.ResourceType(name)
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue(), "resource type %q not found", name)
	return resourceType
}

func (fixture *versionsAPIFixture) requestVersions(resourceName string, query url.Values) *http.Response {
	GinkgoHelper()

	if fixture.server == nil {
		fixture.server = fixture.database.Serve()
	}
	merged := url.Values{}
	for key, values := range fixture.ref.QueryParams() {
		merged[key] = append(merged[key], values...)
	}
	for key, values := range query {
		merged[key] = append(merged[key], values...)
	}
	path := fmt.Sprintf(
		"/api/v1/teams/%s/pipelines/%s/resources/%s/versions",
		fixture.team.Name(), fixture.pipeline.Name(), resourceName,
	)
	if encoded := merged.Encode(); encoded != "" {
		path += "?" + encoded
	}
	request, err := http.NewRequest(http.MethodGet, fixture.server.URL+path, nil)
	Expect(err).NotTo(HaveOccurred())
	response, err := client.Do(request)
	if response != nil {
		DeferCleanup(func() { Expect(response.Body.Close()).To(Succeed()) })
	}
	Expect(err).NotTo(HaveOccurred())
	return response
}

func (fixture *versionsAPIFixture) requestVersionMutation(
	resourceName string,
	versionID int,
	action string,
) *http.Response {
	GinkgoHelper()

	if fixture.server == nil {
		fixture.server = fixture.database.Serve()
	}
	path := fmt.Sprintf(
		"/api/v1/teams/%s/pipelines/%s/resources/%s/versions/%d/%s",
		fixture.team.Name(), fixture.pipeline.Name(), resourceName, versionID, action,
	)
	if encoded := fixture.ref.QueryParams().Encode(); encoded != "" {
		path += "?" + encoded
	}
	request, err := http.NewRequest(http.MethodPut, fixture.server.URL+path, nil)
	Expect(err).NotTo(HaveOccurred())
	response, err := client.Do(request)
	if response != nil {
		DeferCleanup(func() { Expect(response.Body.Close()).To(Succeed()) })
	}
	Expect(err).NotTo(HaveOccurred())
	return response
}

func (fixture *versionsAPIFixture) requestVersionBuilds(
	resourceName string,
	versionID string,
	relationship string,
) *http.Response {
	GinkgoHelper()

	if fixture.server == nil {
		fixture.server = fixture.database.Serve()
	}
	path := fmt.Sprintf(
		"/api/v1/teams/%s/pipelines/%s/resources/%s/versions/%s/%s",
		fixture.team.Name(), fixture.pipeline.Name(), resourceName, versionID, relationship,
	)
	if encoded := fixture.ref.QueryParams().Encode(); encoded != "" {
		path += "?" + encoded
	}
	request, err := http.NewRequest(http.MethodGet, fixture.server.URL+path, nil)
	Expect(err).NotTo(HaveOccurred())
	response, err := client.Do(request)
	if response != nil {
		DeferCleanup(func() { Expect(response.Body.Close()).To(Succeed()) })
	}
	Expect(err).NotTo(HaveOccurred())
	return response
}

func (fixture *versionsAPIFixture) requestClearVersions(collection string, name string) *http.Response {
	GinkgoHelper()

	if fixture.server == nil {
		fixture.server = fixture.database.Serve()
	}
	path := fmt.Sprintf(
		"/api/v1/teams/%s/pipelines/%s/%s/%s/versions",
		fixture.team.Name(), fixture.pipeline.Name(), collection, name,
	)
	if encoded := fixture.ref.QueryParams().Encode(); encoded != "" {
		path += "?" + encoded
	}
	request, err := http.NewRequest(http.MethodDelete, fixture.server.URL+path, nil)
	Expect(err).NotTo(HaveOccurred())
	response, err := client.Do(request)
	if response != nil {
		DeferCleanup(func() { Expect(response.Body.Close()).To(Succeed()) })
	}
	Expect(err).NotTo(HaveOccurred())
	return response
}

func newVersionsAPIMutationFixture() *versionsAPIFixture {
	GinkgoHelper()

	return newVersionsAPIFixture(atc.PipelineRef{Name: "a-pipeline"}, atc.Config{
		Resources: atc.ResourceConfigs{{
			Name: "resource-name", Type: dbtest.BaseResourceType,
			Source: atc.Source{"repository": "versions-mutations"},
		}},
		Jobs: atc.JobConfigs{{
			Name: "admit",
			PlanSequence: []atc.Step{{Config: &atc.GetStep{
				Name: "repository-source", Resource: "resource-name",
			}}},
		}},
	})
}

func seedVersionsAPIMutations(fixture *versionsAPIFixture) versionsAPIMutationVersions {
	GinkgoHelper()

	versions := versionsAPIMutationVersions{
		target: atc.Version{"ref": "target"},
		decoy:  atc.Version{"ref": "decoy"},
	}
	fixture.scenario.Run(fixture.builder.WithResourceVersions(
		"resource-name", versions.target, versions.decoy,
	))
	versions.targetID = fixture.scenario.ResourceVersion(
		"resource-name", versions.target,
	).ID()
	versions.decoyID = fixture.scenario.ResourceVersion(
		"resource-name", versions.decoy,
	).ID()
	return versions
}

func reloadVersionsAPIResource(fixture *versionsAPIFixture) db.Resource {
	GinkgoHelper()

	resource := fixture.resource("resource-name")
	found, err := resource.Reload()
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	return resource
}

func versionsAPIVersionByID(
	fixture *versionsAPIFixture,
	versionID int,
) atc.ResourceVersion {
	GinkgoHelper()

	resource := reloadVersionsAPIResource(fixture)
	versions, _, found, err := resource.Versions(db.Page{Limit: 100}, nil)
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	for _, version := range versions {
		if version.ID == versionID {
			return version
		}
	}
	Fail(fmt.Sprintf("resource version %d not found", versionID))
	return atc.ResourceVersion{}
}

type versionsAPIResourceClearState struct {
	fixture       *versionsAPIFixture
	resourceName  string
	decoyName     string
	versions      []atc.Version
	decoyVersions []atc.Version
}

func newVersionsAPIResourceClearState() *versionsAPIResourceClearState {
	GinkgoHelper()

	state := &versionsAPIResourceClearState{
		resourceName: "resource-name",
		decoyName:    "decoy-resource",
		versions: []atc.Version{
			{"ref": "target-v0"},
			{"ref": "target-v1"},
			{"ref": "target-v2"},
		},
		decoyVersions: []atc.Version{{"ref": "decoy-v0"}},
	}
	state.fixture = newVersionsAPIFixture(atc.PipelineRef{Name: "a-pipeline"}, atc.Config{
		Resources: atc.ResourceConfigs{
			{
				Name: state.resourceName, Type: dbtest.BaseResourceType,
				Source: atc.Source{"repository": "clear-target"},
			},
			{
				Name: state.decoyName, Type: dbtest.BaseResourceType,
				Source: atc.Source{"repository": "clear-decoy"},
			},
		},
		Jobs: atc.JobConfigs{{
			Name: "admit",
			PlanSequence: []atc.Step{{Config: &atc.GetStep{
				Name: "repository-source", Resource: state.resourceName,
			}}},
		}},
	})
	state.fixture.scenario.Run(
		state.fixture.builder.WithResourceVersions(state.resourceName, state.versions...),
		state.fixture.builder.WithResourceVersions(state.decoyName, state.decoyVersions...),
	)
	return state
}

type versionsAPIResourceTypeClearState struct {
	fixture       *versionsAPIFixture
	resourceType  string
	decoyType     string
	versions      []atc.Version
	decoyVersions []atc.Version
}

func newVersionsAPIResourceTypeClearState() *versionsAPIResourceTypeClearState {
	GinkgoHelper()

	state := &versionsAPIResourceTypeClearState{
		resourceType: "some-resource-type",
		decoyType:    "decoy-resource-type",
		versions: []atc.Version{
			{"ref": "target-v0"},
			{"ref": "target-v1"},
			{"ref": "target-v2"},
		},
		decoyVersions: []atc.Version{{"ref": "decoy-v0"}},
	}
	state.fixture = newVersionsAPIFixture(atc.PipelineRef{Name: "a-pipeline"}, atc.Config{
		ResourceTypes: atc.ResourceTypes{
			{
				Name: state.resourceType, Type: dbtest.BaseResourceType,
				Source: atc.Source{"repository": "clear-type-target"},
			},
			{
				Name: state.decoyType, Type: dbtest.BaseResourceType,
				Source: atc.Source{"repository": "clear-type-decoy"},
			},
		},
	})
	state.fixture.scenario.Run(
		state.fixture.builder.WithResourceTypeVersions(state.resourceType, state.versions...),
		state.fixture.builder.WithResourceTypeVersions(state.decoyType, state.decoyVersions...),
	)
	target := state.fixture.resourceType(state.resourceType)
	decoy := state.fixture.resourceType(state.decoyType)
	Expect(target.ResourceConfigID()).NotTo(Equal(decoy.ResourceConfigID()))
	Expect(target.ResourceConfigScopeID()).NotTo(Equal(decoy.ResourceConfigScopeID()))
	return state
}

func versionsAPIResourceVersions(fixture *versionsAPIFixture, name string) []atc.ResourceVersion {
	GinkgoHelper()

	resource := fixture.resource(name)
	found, err := resource.Reload()
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	versions, _, found, err := resource.Versions(db.Page{Limit: 100}, nil)
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	return versions
}

func versionsAPIVersionValues(versions []atc.ResourceVersion) []atc.Version {
	values := make([]atc.Version, len(versions))
	for index, version := range versions {
		values[index] = version.Version
	}
	return values
}

func versionsAPIResourceTypeVersionExists(
	fixture *versionsAPIFixture,
	name string,
	version atc.Version,
) bool {
	GinkgoHelper()

	resourceType := fixture.resourceType(name)
	found, err := resourceType.Reload()
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	resourceConfig, found, err := fixture.database.Deps.resourceConfigFactory.FindResourceConfigByID(
		resourceType.ResourceConfigID(),
	)
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	scope, err := resourceConfig.FindOrCreateScope(nil)
	Expect(err).NotTo(HaveOccurred())
	_, found, err = scope.FindVersion(version)
	Expect(err).NotTo(HaveOccurred())
	return found
}

func decodeVersionsAPIClearResponse(response *http.Response) atc.ClearVersionsResponse {
	GinkgoHelper()

	var decoded atc.ClearVersionsResponse
	Expect(json.NewDecoder(response.Body).Decode(&decoded)).To(Succeed())
	return decoded
}

func decodeVersionsAPIResponse(response *http.Response) []atc.ResourceVersion {
	GinkgoHelper()

	var versions []atc.ResourceVersion
	Expect(json.NewDecoder(response.Body).Decode(&versions)).To(Succeed())
	return versions
}

func seedVersionsAPIListing(fixture *versionsAPIFixture) []atc.ResourceVersion {
	GinkgoHelper()

	older := atc.Version{"some": "version", "ref": "blah"}
	newer := atc.Version{"some": "version", "ref": "foo"}
	metadata := atc.Metadata{{Name: "some", Value: "metadata"}}
	fixture.scenario.Run(
		fixture.builder.WithResourceVersions("some-resource", older, newer),
		fixture.builder.WithVersionMetadata(
			"some-resource", older, db.NewResourceConfigMetadataFields(metadata),
		),
		fixture.builder.WithVersionMetadata(
			"some-resource", newer, db.NewResourceConfigMetadataFields(metadata),
		),
	)
	olderRow := fixture.scenario.ResourceVersion("some-resource", older)
	newerRow := fixture.scenario.ResourceVersion("some-resource", newer)
	resource := fixture.resource("some-resource")
	Expect(resource.DisableVersion(olderRow.ID())).To(Succeed())
	found, err := resource.Reload()
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())

	return []atc.ResourceVersion{
		{ID: newerRow.ID(), Enabled: true, Version: newer, Metadata: metadata},
		{ID: olderRow.ID(), Enabled: false, Version: older, Metadata: metadata},
	}
}

func versionsAPIWithoutMetadata(versions []atc.ResourceVersion) []atc.ResourceVersion {
	without := append([]atc.ResourceVersion(nil), versions...)
	for i := range without {
		without[i].Metadata = nil
	}
	return without
}

type versionsAPIBuildRelationships struct {
	fixture         *versionsAPIFixture
	targetVersionID int
	inputBuilds     []db.Build
	outputBuilds    []db.Build
	decoyBuild      db.Build
	otherBuild      db.Build
}

func newVersionsAPIBuildRelationships() *versionsAPIBuildRelationships {
	GinkgoHelper()

	fixture := newVersionsAPIFixture(atc.PipelineRef{Name: "a-pipeline"}, atc.Config{
		Resources: atc.ResourceConfigs{
			{
				Name: "some-resource", Type: dbtest.BaseResourceType,
				Source: atc.Source{"repository": "version-relationships"},
			},
			{
				Name: "other-resource", Type: dbtest.BaseResourceType,
				Source: atc.Source{"repository": "other-version-relationships"},
			},
		},
		Jobs: atc.JobConfigs{
			{
				Name: "input-job",
				PlanSequence: []atc.Step{{Config: &atc.GetStep{
					Name: "some-input", Resource: "some-resource",
				}}},
			},
			{
				Name: "output-job",
				PlanSequence: []atc.Step{{Config: &atc.PutStep{
					Name: "some-output", Resource: "some-resource",
				}}},
			},
			{
				Name: "decoy-job",
				PlanSequence: []atc.Step{
					{Config: &atc.GetStep{Name: "decoy-input", Resource: "some-resource"}},
					{Config: &atc.PutStep{Name: "decoy-output", Resource: "some-resource"}},
				},
			},
			{
				Name: "other-job",
				PlanSequence: []atc.Step{
					{Config: &atc.GetStep{Name: "other-input", Resource: "other-resource"}},
					{Config: &atc.PutStep{Name: "other-output", Resource: "other-resource"}},
				},
			},
		},
	})

	target := atc.Version{"ref": "target"}
	decoy := atc.Version{"ref": "decoy"}
	// WithJobBuild adopts saved input mappings, but it does not create their
	// resource_config_versions. Persist every mapped version first.
	fixture.scenario.Run(
		fixture.builder.WithResourceVersions("some-resource", target, decoy),
		fixture.builder.WithResourceVersions("other-resource", target),
	)

	relationships := &versionsAPIBuildRelationships{
		fixture: fixture,
		targetVersionID: fixture.scenario.ResourceVersion(
			"some-resource", target,
		).ID(),
	}
	var inputOne, inputTwo, outputOne, outputTwo db.Build
	fixture.scenario.Run(
		fixture.builder.WithJobBuild(
			&inputOne, "input-job",
			dbtest.JobInputs{{Name: "some-input", Version: target}}, nil,
		),
		fixture.builder.WithJobBuild(
			&inputTwo, "input-job",
			dbtest.JobInputs{{Name: "some-input", Version: target}}, nil,
		),
		fixture.builder.WithJobBuild(
			&outputOne, "output-job", nil,
			dbtest.JobOutputs{"some-output": target},
		),
		fixture.builder.WithJobBuild(
			&outputTwo, "output-job", nil,
			dbtest.JobOutputs{"some-output": target},
		),
		fixture.builder.WithJobBuild(
			&relationships.decoyBuild, "decoy-job",
			dbtest.JobInputs{{Name: "decoy-input", Version: decoy}},
			dbtest.JobOutputs{"decoy-output": decoy},
		),
		fixture.builder.WithJobBuild(
			&relationships.otherBuild, "other-job",
			dbtest.JobInputs{{Name: "other-input", Version: target}},
			dbtest.JobOutputs{"other-output": target},
		),
	)

	startVersionsAPIBuild(inputOne)
	finishVersionsAPIBuild(inputOne, db.BuildStatusSucceeded)
	startVersionsAPIBuild(inputTwo)
	startVersionsAPIBuild(outputOne)
	finishVersionsAPIBuild(outputOne, db.BuildStatusFailed)
	startVersionsAPIBuild(outputTwo)
	relationships.inputBuilds = []db.Build{inputOne, inputTwo}
	relationships.outputBuilds = []db.Build{outputOne, outputTwo}
	return relationships
}

func startVersionsAPIBuild(build db.Build) {
	GinkgoHelper()

	started, err := build.Start(atc.Plan{})
	Expect(err).NotTo(HaveOccurred())
	Expect(started).To(BeTrue())
	reloadVersionsAPIBuild(build)
}

func finishVersionsAPIBuild(build db.Build, status db.BuildStatus) {
	GinkgoHelper()

	Expect(build.Finish(status)).To(Succeed())
	reloadVersionsAPIBuild(build)
}

func reloadVersionsAPIBuild(build db.Build) {
	GinkgoHelper()

	found, err := build.Reload()
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
}

func decodeVersionsAPIBuilds(response *http.Response) []atc.Build {
	GinkgoHelper()

	var builds []atc.Build
	Expect(json.NewDecoder(response.Body).Decode(&builds)).To(Succeed())
	return builds
}

func versionsAPIBuildIDs(builds []atc.Build) []int {
	ids := make([]int, 0, len(builds))
	for _, build := range builds {
		ids = append(ids, build.ID)
	}
	return ids
}

func versionsAPIDBBuildIDs(builds []db.Build) []int {
	ids := make([]int, 0, len(builds))
	for _, build := range builds {
		ids = append(ids, build.ID())
	}
	return ids
}

func expectVersionsAPIBuilds(actual []atc.Build, expected []db.Build) {
	GinkgoHelper()

	Expect(versionsAPIBuildIDs(actual)).To(ConsistOf(versionsAPIDBBuildIDs(expected)))
	byID := map[int]atc.Build{}
	for _, build := range actual {
		byID[build.ID] = build
	}
	for _, build := range expected {
		expectedBuild := atc.Build{
			ID:                   build.ID(),
			TeamName:             build.TeamName(),
			Name:                 build.Name(),
			Status:               atc.BuildStatus(build.Status()),
			APIURL:               fmt.Sprintf("/api/v1/builds/%d", build.ID()),
			JobName:              build.JobName(),
			PipelineID:           build.PipelineID(),
			PipelineName:         build.PipelineName(),
			PipelineInstanceVars: build.PipelineInstanceVars(),
			CreatedBy:            build.CreatedBy(),
		}
		if !build.StartTime().IsZero() {
			expectedBuild.StartTime = build.StartTime().Unix()
		}
		if !build.EndTime().IsZero() {
			expectedBuild.EndTime = build.EndTime().Unix()
		}
		Expect(byID).To(HaveKeyWithValue(build.ID(), expectedBuild))
	}
}

func versionsAPIBuildRelationshipSpecs(relationship string) func() {
	return func() {
		var (
			response          *http.Response
			relationships     *versionsAPIBuildRelationships
			resourceName      string
			stringVersionID   string
			exposePipeline    bool
			selectedProfile   requestProfile
			grantRole         string
			prepare           func(*versionsAPIBuildRelationships)
			expectedBuildsFor func(*versionsAPIBuildRelationships) []db.Build
		)

		BeforeEach(func() {
			resourceName = "some-resource"
			stringVersionID = ""
			exposePipeline = false
			selectedProfile = memberProfile
			grantRole = accessor.ViewerRole
			prepare = nil
			if relationship == "input_to" {
				expectedBuildsFor = func(r *versionsAPIBuildRelationships) []db.Build {
					return r.inputBuilds
				}
			} else {
				expectedBuildsFor = func(r *versionsAPIBuildRelationships) []db.Build {
					return r.outputBuilds
				}
			}
		})

		JustBeforeEach(func() {
			relationships = newVersionsAPIBuildRelationships()
			if exposePipeline {
				Expect(relationships.fixture.pipeline.Expose()).To(Succeed())
			}
			if prepare != nil {
				prepare(relationships)
			}
			if stringVersionID == "" {
				stringVersionID = strconv.Itoa(relationships.targetVersionID)
			}
			if grantRole != "" {
				grantProfile(relationships.fixture.team, selectedProfile, grantRole)
			}
			useProfile(selectedProfile)
			response = relationships.fixture.requestVersionBuilds(
				resourceName, stringVersionID, relationship,
			)
		})

		Context("when not authorized", func() {
			BeforeEach(func() {
				grantRole = ""
			})

			Context("and the pipeline is private", func() {
				Context("when authenticated", func() {
					It("returns 403", func() {
						Expect(response.StatusCode).To(Equal(http.StatusForbidden))
					})
				})

				Context("when not authenticated", func() {
					BeforeEach(func() {
						selectedProfile = anonymousProfile
					})

					It("returns 401", func() {
						Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
					})
				})
			})

			Context("and the pipeline is public", func() {
				BeforeEach(func() {
					exposePipeline = true
					selectedProfile = anonymousProfile
				})

				It("returns 200 OK", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
				})
			})
		})

		Context("when authorized", func() {
			Context("when not finding the resource", func() {
				BeforeEach(func() {
					resourceName = "missing-resource"
				})

				It("returns 404", func() {
					Expect(response.StatusCode).To(Equal(http.StatusNotFound))
				})
			})

			It("selects relationships for the requested resource", func() {
				actual := decodeVersionsAPIBuilds(response)
				Expect(versionsAPIBuildIDs(actual)).ToNot(ContainElement(relationships.otherBuild.ID()))
				Expect(versionsAPIBuildIDs(actual)).To(ConsistOf(
					versionsAPIDBBuildIDs(expectedBuildsFor(relationships)),
				))
			})

			It("selects relationships for the requested persisted version ID", func() {
				actual := decodeVersionsAPIBuilds(response)
				Expect(versionsAPIBuildIDs(actual)).ToNot(ContainElement(relationships.decoyBuild.ID()))
				Expect(versionsAPIBuildIDs(actual)).To(ConsistOf(
					versionsAPIDBBuildIDs(expectedBuildsFor(relationships)),
				))
			})

			It("returns 200 OK", func() {
				Expect(response.StatusCode).To(Equal(http.StatusOK))
			})

			It("returns content type application/json", func() {
				Expect(response).To(IncludeHeaderEntries(map[string]string{
					"Content-Type": "application/json",
				}))
			})

			It("returns the persisted builds", func() {
				expectVersionsAPIBuilds(
					decodeVersionsAPIBuilds(response),
					expectedBuildsFor(relationships),
				)
			})

			Context("when the version ID does not identify a persisted version", func() {
				BeforeEach(func() {
					stringVersionID = "hello"
				})

				It("returns an empty 200 list for numeric and nonnumeric IDs", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
					Expect(decodeVersionsAPIBuilds(response)).To(BeEmpty())

					numericResponse := relationships.fixture.requestVersionBuilds(
						resourceName,
						strconv.Itoa(relationships.targetVersionID+1_000_000),
						relationship,
					)
					Expect(numericResponse.StatusCode).To(Equal(http.StatusOK))
					Expect(decodeVersionsAPIBuilds(numericResponse)).To(BeEmpty())
				})
			})

		})
	}
}

var _ = Describe("Versions API", func() {
	Describe("GET /api/v1/teams/:team_name/pipelines/:pipeline_name/resources/:resource_name/versions", func() {
		var (
			response         *http.Response
			fixture          *versionsAPIFixture
			queryParams      url.Values
			pipelineRef      atc.PipelineRef
			resourceName     string
			resourcePublic   bool
			pipelinePublic   bool
			selectedProfile  requestProfile
			grantRole        string
			prepare          []func(*versionsAPIFixture)
			expectedVersions []atc.ResourceVersion
		)

		BeforeEach(func() {
			queryParams = url.Values{}
			pipelineRef = atc.PipelineRef{Name: "a-pipeline"}
			resourceName = "some-resource"
			resourcePublic = false
			pipelinePublic = false
			selectedProfile = anonymousProfile
			grantRole = ""
			prepare = nil
			expectedVersions = nil
		})

		JustBeforeEach(func() {
			fixture = newVersionsAPIFixture(pipelineRef, atc.Config{
				Resources: atc.ResourceConfigs{{
					Name:   "some-resource",
					Type:   dbtest.BaseResourceType,
					Source: atc.Source{"repository": "versions-api"},
					Public: resourcePublic,
				}},
			})
			if pipelinePublic {
				Expect(fixture.pipeline.Expose()).To(Succeed())
			}
			for _, setup := range prepare {
				setup(fixture)
			}
			if grantRole != "" {
				grantProfile(fixture.team, selectedProfile, grantRole)
			}
			useProfile(selectedProfile)
			response = fixture.requestVersions(resourceName, queryParams)
		})

		Context("when not authorized", func() {
			Context("and the pipeline is private", func() {
				Context("user is not authenticated", func() {
					It("returns 401", func() {
						Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
					})
				})

				Context("user is authenticated", func() {
					BeforeEach(func() {
						selectedProfile = memberProfile
					})

					It("returns 403", func() {
						Expect(response.StatusCode).To(Equal(http.StatusForbidden))
					})
				})
			})

			Context("and the pipeline is public", func() {
				BeforeEach(func() {
					pipelinePublic = true
					prepare = append(prepare, func(fixture *versionsAPIFixture) {
						expectedVersions = seedVersionsAPIListing(fixture)
					})
				})

				It("returns 200 OK", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
				})

				It("returns content type application/json", func() {
					expectedHeaderEntries := map[string]string{
						"Content-Type": "application/json",
					}
					Expect(response).Should(IncludeHeaderEntries(expectedHeaderEntries))
				})

				Context("when resource is public", func() {
					BeforeEach(func() {
						resourcePublic = true
					})

					It("returns the json", func() {
						Expect(decodeVersionsAPIResponse(response)).To(Equal(expectedVersions))
					})
				})

				Context("when resource is not public", func() {
					Context("when the user is not authenticated", func() {
						It("returns the json without version metadata", func() {
							Expect(decodeVersionsAPIResponse(response)).To(Equal(
								versionsAPIWithoutMetadata(expectedVersions),
							))
						})
					})

					Context("when the user is authenticated", func() {
						BeforeEach(func() {
							selectedProfile = memberProfile
						})

						It("returns the json without version metadata", func() {
							Expect(decodeVersionsAPIResponse(response)).To(Equal(
								versionsAPIWithoutMetadata(expectedVersions),
							))
						})
					})
				})
			})
		})

		Context("when authorized", func() {
			BeforeEach(func() {
				selectedProfile = memberProfile
				grantRole = accessor.ViewerRole
			})

			It("finds the resource", func() {
				Expect(response.StatusCode).To(Equal(http.StatusOK))
				Expect(decodeVersionsAPIResponse(response)).To(BeEmpty())
			})

			Context("when finding the resource succeeds", func() {
				Context("when no params are passed", func() {
					BeforeEach(func() {
						prepare = append(prepare, func(fixture *versionsAPIFixture) {
							versions := make([]atc.Version, 101)
							for i := range versions {
								versions[i] = atc.Version{"ref": fmt.Sprintf("%03d", i)}
							}
							fixture.scenario.Run(
								fixture.builder.WithResourceVersions("some-resource", versions...),
							)
						})
					})

					It("does not set defaults for since and until", func() {
						actual := decodeVersionsAPIResponse(response)
						Expect(actual).To(HaveLen(atc.PaginationAPIDefaultLimit))

						expectedRefs := make([]string, 0, atc.PaginationAPIDefaultLimit)
						for i := 1; i <= atc.PaginationAPIDefaultLimit; i++ {
							expectedRefs = append(expectedRefs, fmt.Sprintf("%03d", i))
						}
						actualRefs := make([]string, 0, len(actual))
						for _, version := range actual {
							actualRefs = append(actualRefs, version.Version["ref"])
						}
						Expect(actualRefs).To(ConsistOf(expectedRefs))
					})
				})

				Context("when all the params are passed", func() {
					var (
						match1 atc.Version
						match2 atc.Version
						match3 atc.Version
						match4 atc.Version
						fromID int
						toID   int
					)

					BeforeEach(func() {
						match1 = atc.Version{"ref": "foo", "some-ref": "blah", "marker": "one"}
						match2 = atc.Version{"ref": "foo", "some-ref": "blah", "marker": "two"}
						match3 = atc.Version{"ref": "foo", "some-ref": "blah", "marker": "three"}
						match4 = atc.Version{"ref": "foo", "some-ref": "blah", "marker": "four"}
						prepare = append(prepare, func(fixture *versionsAPIFixture) {
							fixture.scenario.Run(fixture.builder.WithResourceVersions(
								"some-resource",
								match1,
								atc.Version{"ref": "foo", "some-ref": "wrong", "marker": "decoy-one"},
								match2,
								match3,
								atc.Version{"ref": "wrong", "some-ref": "blah", "marker": "decoy-two"},
								match4,
							))
							fromID = fixture.scenario.ResourceVersion("some-resource", match1).ID()
							toID = fixture.scenario.ResourceVersion("some-resource", match4).ID()
							queryParams.Set("from", strconv.Itoa(fromID))
							queryParams.Set("to", strconv.Itoa(toID))
							queryParams.Set("limit", "2")
							queryParams.Add("filter", "ref:foo")
							queryParams.Add("filter", "some-ref:blah")
						})
					})

					It("passes them through", func() {
						Expect(decodeVersionsAPIResponse(response)).To(Equal([]atc.ResourceVersion{
							{
								ID:      fixture.scenario.ResourceVersion("some-resource", match2).ID(),
								Enabled: true,
								Version: match2,
							},
							{
								ID:      fixture.scenario.ResourceVersion("some-resource", match1).ID(),
								Enabled: true,
								Version: match1,
							},
						}))

						toOnly := url.Values{
							"to":     []string{strconv.Itoa(toID)},
							"limit":  []string{"2"},
							"filter": []string{"ref:foo", "some-ref:blah"},
						}
						toResponse := fixture.requestVersions(resourceName, toOnly)
						Expect(decodeVersionsAPIResponse(toResponse)).To(Equal([]atc.ResourceVersion{
							{
								ID:      fixture.scenario.ResourceVersion("some-resource", match4).ID(),
								Enabled: true,
								Version: match4,
							},
							{
								ID:      fixture.scenario.ResourceVersion("some-resource", match3).ID(),
								Enabled: true,
								Version: match3,
							},
						}))
					})
				})

				Context("when params includes version filter has special char", func() {
					Context("space char", func() {
						var matching atc.Version

						BeforeEach(func() {
							matching = atc.Version{"some ref": "some value", "marker": "match"}
							queryParams.Set("filter", "some ref:some value")
							prepare = append(prepare, func(fixture *versionsAPIFixture) {
								fixture.scenario.Run(fixture.builder.WithResourceVersions(
									"some-resource",
									matching,
									atc.Version{"some ref": "other", "marker": "decoy"},
								))
							})
						})

						It("passes them through", func() {
							Expect(decodeVersionsAPIResponse(response)).To(Equal([]atc.ResourceVersion{{
								ID:      fixture.scenario.ResourceVersion("some-resource", matching).ID(),
								Enabled: true,
								Version: matching,
							}}))
						})
					})

					Context("% char", func() {
						var matching atc.Version

						BeforeEach(func() {
							matching = atc.Version{"ref": "some%value", "marker": "match"}
							queryParams.Set("filter", "ref:some%value")
							prepare = append(prepare, func(fixture *versionsAPIFixture) {
								fixture.scenario.Run(fixture.builder.WithResourceVersions(
									"some-resource",
									matching,
									atc.Version{"ref": "some-value", "marker": "decoy"},
								))
							})
						})

						It("passes them through", func() {
							Expect(decodeVersionsAPIResponse(response)).To(Equal([]atc.ResourceVersion{{
								ID:      fixture.scenario.ResourceVersion("some-resource", matching).ID(),
								Enabled: true,
								Version: matching,
							}}))
						})
					})

					Context(": char", func() {
						var matching atc.Version

						BeforeEach(func() {
							matching = atc.Version{"key": "with:colon:abcdef", "marker": "match"}
							queryParams.Set("filter", "key:with:colon:abcdef")
							prepare = append(prepare, func(fixture *versionsAPIFixture) {
								fixture.scenario.Run(fixture.builder.WithResourceVersions(
									"some-resource",
									matching,
									atc.Version{"key:with:colon": "abcdef", "marker": "decoy"},
								))
							})
						})

						It("passes them through by splitting on first colon", func() {
							Expect(decodeVersionsAPIResponse(response)).To(Equal([]atc.ResourceVersion{{
								ID:      fixture.scenario.ResourceVersion("some-resource", matching).ID(),
								Enabled: true,
								Version: matching,
							}}))
						})
					})

					Context("if there is no : ", func() {
						var versions []atc.Version

						BeforeEach(func() {
							versions = []atc.Version{{"ref": "one"}, {"ref": "two"}}
							queryParams.Set("filter", "abcdef")
							prepare = append(prepare, func(fixture *versionsAPIFixture) {
								fixture.scenario.Run(
									fixture.builder.WithResourceVersions("some-resource", versions...),
								)
							})
						})

						It("set no filter when fetching versions", func() {
							actual := decodeVersionsAPIResponse(response)
							actualVersions := make([]atc.Version, 0, len(actual))
							for _, version := range actual {
								actualVersions = append(actualVersions, version.Version)
							}
							Expect(actualVersions).To(ConsistOf(versions))
						})
					})
				})

				Context("when getting the versions succeeds", func() {
					BeforeEach(func() {
						queryParams.Set("since", "5")
						queryParams.Set("limit", "2")
						prepare = append(prepare, func(fixture *versionsAPIFixture) {
							expectedVersions = seedVersionsAPIListing(fixture)
						})
					})

					It("returns 200 OK", func() {
						Expect(response.StatusCode).To(Equal(http.StatusOK))
					})

					It("returns content type application/json", func() {
						expectedHeaderEntries := map[string]string{
							"Content-Type": "application/json",
						}
						Expect(response).Should(IncludeHeaderEntries(expectedHeaderEntries))
					})

					It("returns the json", func() {
						Expect(decodeVersionsAPIResponse(response)).To(Equal(expectedVersions))
					})

					Context("when next/previous pages are available", func() {
						var (
							olderCursorID int
							newerCursorID int
						)

						BeforeEach(func() {
							pipelineRef.Name = "some-pipeline"
							prepare = append(prepare, func(fixture *versionsAPIFixture) {
								middle := atc.Version{"ref": "middle"}
								pageNewer := atc.Version{"ref": "page-newer"}
								newest := atc.Version{"ref": "newest"}
								fixture.scenario.Run(fixture.builder.WithResourceVersions(
									"some-resource", middle, pageNewer, newest,
								))
								queryParams.Del("since")
								queryParams.Set(
									"from",
									strconv.Itoa(fixture.scenario.ResourceVersion("some-resource", middle).ID()),
								)
								olderCursorID = expectedVersions[0].ID
								newerCursorID = fixture.scenario.ResourceVersion("some-resource", newest).ID()
							})
						})

						It("returns Link headers per rfc5988", func() {
							link := fmt.Sprintf(
								`<%s/api/v1/teams/a-team/pipelines/%s/resources/some-resource/versions?`,
								externalURL, fixture.ref.Name,
							)
							Expect(response.Header["Link"]).To(ConsistOf([]string{
								fmt.Sprintf(`%sfrom=%d&limit=2>; rel="previous"`, link, newerCursorID),
								fmt.Sprintf(`%sto=%d&limit=2>; rel="next"`, link, olderCursorID),
							}))
						})

						Context("and resource is on an instanced pipeline", func() {
							BeforeEach(func() {
								pipelineRef.InstanceVars = atc.InstanceVars{"branch": "master"}
							})

							It("returns Link headers per rfc5988", func() {
								link := fmt.Sprintf(
									`<%s/api/v1/teams/a-team/pipelines/%s/resources/some-resource/versions?`,
									externalURL, fixture.ref.Name,
								)
								vars := fixture.ref.QueryParams().Encode()
								Expect(response.Header["Link"]).To(ConsistOf([]string{
									fmt.Sprintf(`%sto=%d&limit=2&%s>; rel="next"`, link, olderCursorID, vars),
									fmt.Sprintf(`%sfrom=%d&limit=2&%s>; rel="previous"`, link, newerCursorID, vars),
								}))
							})
						})
					})
				})

			})

			Context("when the resource is not found", func() {
				BeforeEach(func() {
					resourceName = "missing-resource"
				})

				It("returns 404 not found", func() {
					Expect(response.StatusCode).To(Equal(http.StatusNotFound))
				})
			})
		})
	})

	Describe("PUT /api/v1/teams/:team_name/pipelines/:pipeline_name/resources/:resource_name/versions/:resource_version_id/enable", func() {
		var (
			response        *http.Response
			fixture         *versionsAPIFixture
			resourceName    string
			versions        versionsAPIMutationVersions
			selectedProfile requestProfile
			grantRole       string
			prepare         []func(*versionsAPIFixture)
		)

		BeforeEach(func() {
			resourceName = "resource-name"
			selectedProfile = anonymousProfile
			grantRole = ""
			prepare = nil
		})

		JustBeforeEach(func() {
			fixture = newVersionsAPIMutationFixture()
			versions = seedVersionsAPIMutations(fixture)
			resource := fixture.resource("resource-name")
			Expect(resource.DisableVersion(versions.targetID)).To(Succeed())
			Expect(resource.DisableVersion(versions.decoyID)).To(Succeed())
			for _, setup := range prepare {
				setup(fixture)
			}
			if grantRole != "" {
				grantProfile(fixture.team, selectedProfile, grantRole)
			}
			useProfile(selectedProfile)
			response = fixture.requestVersionMutation(
				resourceName, versions.targetID, "enable",
			)
		})

		Context("when authenticated", func() {
			BeforeEach(func() {
				selectedProfile = memberProfile
			})

			Context("when authorized", func() {
				BeforeEach(func() {
					grantRole = accessor.OperatorRole
				})

				It("finds the configured resource", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
				})

				Context("when finding the resource succeeds", func() {
					It("enables the exact persisted resource version from the URL", func() {
						Expect(response.StatusCode).To(Equal(http.StatusOK))
						Expect(versionsAPIVersionByID(fixture, versions.targetID).Enabled).To(BeTrue())
						Expect(versionsAPIVersionByID(fixture, versions.decoyID).Enabled).To(BeFalse())
					})

					Context("when enabling the resource succeeds", func() {
						It("returns 200", func() {
							Expect(response.StatusCode).To(Equal(http.StatusOK))
						})
					})

				})

				Context("when the resource is not found", func() {
					BeforeEach(func() {
						resourceName = "missing-resource"
					})

					It("returns not found", func() {
						Expect(response.StatusCode).To(Equal(http.StatusNotFound))
					})
				})
			})

			Context("when not authorized", func() {
				It("returns Forbidden", func() {
					Expect(response.StatusCode).To(Equal(http.StatusForbidden))
				})
			})
		})

		Context("when not authenticated", func() {
			It("returns Unauthorized", func() {
				Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
			})
		})
	})

	Describe("PUT /api/v1/teams/:team_name/pipelines/:pipeline_name/resources/:resource_name/versions/:resource_version_id/disable", func() {
		var (
			response        *http.Response
			fixture         *versionsAPIFixture
			resourceName    string
			versions        versionsAPIMutationVersions
			selectedProfile requestProfile
			grantRole       string
			prepare         []func(*versionsAPIFixture)
		)

		BeforeEach(func() {
			resourceName = "resource-name"
			selectedProfile = anonymousProfile
			grantRole = ""
			prepare = nil
		})

		JustBeforeEach(func() {
			fixture = newVersionsAPIMutationFixture()
			versions = seedVersionsAPIMutations(fixture)
			for _, setup := range prepare {
				setup(fixture)
			}
			if grantRole != "" {
				grantProfile(fixture.team, selectedProfile, grantRole)
			}
			useProfile(selectedProfile)
			response = fixture.requestVersionMutation(
				resourceName, versions.targetID, "disable",
			)
		})

		Context("when authenticated ", func() {
			BeforeEach(func() {
				selectedProfile = memberProfile
			})

			Context("when authorized", func() {
				BeforeEach(func() {
					grantRole = accessor.OperatorRole
				})

				It("finds the configured resource", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
				})

				Context("when finding the resource succeeds", func() {
					It("disables the exact persisted resource version from the URL", func() {
						Expect(response.StatusCode).To(Equal(http.StatusOK))
						Expect(versionsAPIVersionByID(fixture, versions.targetID).Enabled).To(BeFalse())
						Expect(versionsAPIVersionByID(fixture, versions.decoyID).Enabled).To(BeTrue())
					})

					Context("when disabling the resource version succeeds", func() {
						It("returns 200", func() {
							Expect(response.StatusCode).To(Equal(http.StatusOK))
						})
					})

				})

				Context("when the resource is not found", func() {
					BeforeEach(func() {
						resourceName = "missing-resource"
					})

					It("returns not found", func() {
						Expect(response.StatusCode).To(Equal(http.StatusNotFound))
					})
				})
			})
			Context("when not authorized", func() {
				It("returns Forbidden", func() {
					Expect(response.StatusCode).To(Equal(http.StatusForbidden))
				})
			})
		})
		Context("when not authenticated", func() {
			It("returns Unauthorized", func() {
				Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
			})
		})
	})

	Describe("PUT /api/v1/teams/:team_name/pipelines/:pipeline_name/resources/:resource_name/versions/:resource_version_id/pin", func() {
		var (
			response        *http.Response
			fixture         *versionsAPIFixture
			resourceName    string
			versions        versionsAPIMutationVersions
			selectedProfile requestProfile
			grantRole       string
			prepare         []func(*versionsAPIFixture)
		)

		BeforeEach(func() {
			resourceName = "resource-name"
			selectedProfile = anonymousProfile
			grantRole = ""
			prepare = nil
		})

		JustBeforeEach(func() {
			fixture = newVersionsAPIMutationFixture()
			versions = seedVersionsAPIMutations(fixture)
			pinned, err := fixture.resource("resource-name").PinVersion(versions.decoyID)
			Expect(err).NotTo(HaveOccurred())
			Expect(pinned).To(BeTrue())
			for _, setup := range prepare {
				setup(fixture)
			}
			if grantRole != "" {
				grantProfile(fixture.team, selectedProfile, grantRole)
			}
			useProfile(selectedProfile)
			response = fixture.requestVersionMutation(
				resourceName, versions.targetID, "pin",
			)
		})

		Context("when authenticated", func() {
			BeforeEach(func() {
				selectedProfile = memberProfile
			})

			Context("when authorized", func() {
				BeforeEach(func() {
					grantRole = accessor.OperatorRole
				})

				It("finds the configured resource", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
				})

				Context("when finding the resource succeeds", func() {
					It("pins the exact persisted resource version from the URL", func() {
						Expect(response.StatusCode).To(Equal(http.StatusOK))
						resource := reloadVersionsAPIResource(fixture)
						Expect(resource.CurrentPinnedVersion()).To(Equal(versions.target))
					})

					Context("when pinning the resource succeeds", func() {
						It("returns 200", func() {
							Expect(response.StatusCode).To(Equal(http.StatusOK))

							retry := fixture.requestVersionMutation(
								resourceName, versions.targetID, "pin",
							)
							Expect(retry.StatusCode).To(Equal(http.StatusOK))
							Expect(reloadVersionsAPIResource(fixture).CurrentPinnedVersion()).To(Equal(versions.target))

							missing := fixture.requestVersionMutation(resourceName, -1, "pin")
							Expect(missing.StatusCode).To(Equal(http.StatusInternalServerError))
							Expect(reloadVersionsAPIResource(fixture).CurrentPinnedVersion()).To(Equal(versions.target))
						})
					})

				})

				Context("when the resource is not found", func() {
					BeforeEach(func() {
						resourceName = "missing-resource"
					})

					It("returns not found", func() {
						Expect(response.StatusCode).To(Equal(http.StatusNotFound))
					})
				})
			})

			Context("when not authorized", func() {
				It("returns Forbidden", func() {
					Expect(response.StatusCode).To(Equal(http.StatusForbidden))
				})
			})
		})

		Context("when not authenticated", func() {
			It("returns Unauthorized", func() {
				Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
			})
		})
	})

	Describe("GET /api/v1/teams/:team_name/pipelines/:pipeline_name/resources/:resource_name/versions/:resource_version_id/input_to", versionsAPIBuildRelationshipSpecs("input_to"))

	Describe("GET /api/v1/teams/:team_name/pipelines/:pipeline_name/resources/:resource_name/versions/:resource_version_id/output_of", versionsAPIBuildRelationshipSpecs("output_of"))
	Describe("DELETE /api/v1/teams/:team_name/pipelines/:pipeline_name/resources/:resource_name/versions", func() {
		var (
			response        *http.Response
			state           *versionsAPIResourceClearState
			resourceName    string
			selectedProfile requestProfile
			prepare         []func(*versionsAPIResourceClearState)
		)

		BeforeEach(func() {
			resourceName = "resource-name"
			selectedProfile = memberProfile
			prepare = nil
		})

		JustBeforeEach(func() {
			state = newVersionsAPIResourceClearState()
			for _, setup := range prepare {
				setup(state)
			}
			useProfile(selectedProfile)
			response = state.fixture.requestClearVersions("resources", resourceName)
		})

		Context("when authenticated", func() {
			Context("when the user is not admin", func() {
				It("returns Forbidden", func() {
					Expect(response.StatusCode).To(Equal(http.StatusForbidden))
				})
			})

			Context("when the user is an admin", func() {
				BeforeEach(func() {
					selectedProfile = adminProfile
				})

				It("returns 200", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
				})

				It("returns Content-Type 'application/json'", func() {
					Expect(response).To(IncludeHeaderEntries(map[string]string{
						"Content-Type": "application/json",
					}))
				})

				It("returns the persisted deletion count", func() {
					Expect(decodeVersionsAPIClearResponse(response).VersionsRemoved).To(Equal(
						int64(len(state.versions)),
					))
				})

				It("clears the persisted resource versions", func() {
					Expect(versionsAPIResourceVersions(state.fixture, state.resourceName)).To(BeEmpty())
				})

				It("preserves versions in a different resource scope", func() {
					Expect(versionsAPIVersionValues(
						versionsAPIResourceVersions(state.fixture, state.decoyName),
					)).To(ConsistOf(state.decoyVersions))
				})

				Context("when the resource is not found", func() {
					BeforeEach(func() {
						resourceName = "missing-resource"
					})

					It("returns 404", func() {
						Expect(response.StatusCode).To(Equal(http.StatusNotFound))
					})
				})

			})
		})
	})

	Describe("DELETE /api/v1/teams/:team_name/pipelines/:pipeline_name/resource-types/:resource_type_name/versions", func() {
		var (
			response         *http.Response
			state            *versionsAPIResourceTypeClearState
			resourceTypeName string
			selectedProfile  requestProfile
			prepare          []func(*versionsAPIResourceTypeClearState)
		)

		BeforeEach(func() {
			resourceTypeName = "some-resource-type"
			selectedProfile = memberProfile
			prepare = nil
		})

		JustBeforeEach(func() {
			state = newVersionsAPIResourceTypeClearState()
			for _, setup := range prepare {
				setup(state)
			}
			useProfile(selectedProfile)
			response = state.fixture.requestClearVersions("resource-types", resourceTypeName)
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				selectedProfile = anonymousProfile
			})

			It("returns Unauthorized", func() {
				Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
			})
		})

		Context("when authenticated", func() {
			Context("when the user is not admin", func() {
				It("returns Forbidden", func() {
					Expect(response.StatusCode).To(Equal(http.StatusForbidden))
				})
			})

			Context("when the user is an admin", func() {
				BeforeEach(func() {
					selectedProfile = adminProfile
				})

				It("returns 200", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
				})

				It("returns Content-Type 'application/json'", func() {
					Expect(response).To(IncludeHeaderEntries(map[string]string{
						"Content-Type": "application/json",
					}))
				})

				It("returns the persisted deletion count", func() {
					Expect(decodeVersionsAPIClearResponse(response).VersionsRemoved).To(Equal(
						int64(len(state.versions)),
					))
				})

				It("clears the persisted resource type versions", func() {
					for _, version := range state.versions {
						Expect(versionsAPIResourceTypeVersionExists(
							state.fixture, state.resourceType, version,
						)).To(BeFalse(), "target version %v survived", version)
					}
				})

				It("preserves versions in a distinct resource type scope", func() {
					for _, version := range state.decoyVersions {
						Expect(versionsAPIResourceTypeVersionExists(
							state.fixture, state.decoyType, version,
						)).To(BeTrue(), "decoy version %v was removed", version)
					}
				})

				Context("when the resource type is not found", func() {
					BeforeEach(func() {
						resourceTypeName = "missing-resource-type"
					})

					It("returns 404", func() {
						Expect(response.StatusCode).To(Equal(http.StatusNotFound))
					})
				})

			})
		})
	})
})
