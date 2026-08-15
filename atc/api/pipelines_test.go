package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"
	. "github.com/concourse/concourse/atc/testhelpers"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type pipelineDebugDecoyIDs struct {
	resourceIDs []int
	versionIDs  []int
	buildIDs    []int
	jobIDs      []int
	scopeIDs    []int
}

type pipelineDebugVersionsFixture struct {
	Database *realDB
	Team     db.Team
	Expected atc.DebugVersionsDB
	Decoy    pipelineDebugDecoyIDs
}

func pipelineDebugScopeID(id int) *int {
	if id == 0 {
		return nil
	}
	return &id
}

func persistPipelineDebugVersionsFixture() pipelineDebugVersionsFixture {
	GinkgoHelper()

	database := useRealDB()
	targetTeam, err := database.Deps.teamFactory.CreateTeam(atc.Team{Name: "a-team"})
	Expect(err).NotTo(HaveOccurred())

	targetConfig := atc.Config{
		Resources: atc.ResourceConfigs{
			{Name: "input-resource", Type: dbtest.BaseResourceType, Source: atc.Source{"fixture": "input"}},
			{Name: "output-resource", Type: dbtest.BaseResourceType, Source: atc.Source{"fixture": "output"}},
			{Name: "idle-resource", Type: dbtest.BaseResourceType, Source: atc.Source{"fixture": "idle"}},
		},
		Jobs: atc.JobConfigs{
			{
				Name: "graph-job",
				PlanSequence: []atc.Step{{Config: &atc.GetStep{
					Name:     "source-input",
					Resource: "input-resource",
				}}},
			},
			{Name: "idle-job"},
		},
	}
	targetPipeline := database.SavePipeline(targetTeam, "a-pipeline", targetConfig)
	builder := dbtest.NewBuilder(database.Conn, database.LockFactory)
	targetScenario := &dbtest.Scenario{Team: targetTeam, Pipeline: targetPipeline}

	inputVersion := atc.Version{"ref": "input-v1"}
	targetScenario.Run(builder.WithResourceVersions("input-resource", inputVersion))
	inputResource := targetScenario.Resource("input-resource")
	inputResourceVersion, found, err := inputResource.FindVersion(inputVersion)
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())

	graphJob := targetScenario.Job("graph-job")
	idleJob := targetScenario.Job("idle-job")

	explicitBuild, err := graphJob.CreateBuild("pipeline-api-debug-fixture")
	Expect(err).NotTo(HaveOccurred())
	outputResource := targetScenario.Resource("output-resource")
	outputVersion := atc.Version{"ref": "output-v1"}
	Expect(explicitBuild.SaveOutput(
		outputResource.Type(),
		nil,
		outputResource.Source(),
		outputVersion,
		nil,
		"published-output",
		outputResource.Name(),
	)).To(Succeed())
	Expect(explicitBuild.Finish(db.BuildStatusSucceeded)).To(Succeed())

	outputResource = targetScenario.Resource("output-resource")
	outputResourceVersion, found, err := outputResource.FindVersion(outputVersion)
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	idleResource := targetScenario.Resource("idle-resource")

	var inputBuild db.Build
	targetScenario.Run(builder.WithJobBuild(
		&inputBuild,
		"graph-job",
		dbtest.JobInputs{{
			Name:            "source-input",
			Version:         inputVersion,
			FirstOccurrence: true,
		}},
		nil,
	))
	Expect(inputBuild.Finish(db.BuildStatusSucceeded)).To(Succeed())
	rerunBuild, err := graphJob.RerunBuild(inputBuild, "pipeline-api-debug-rerun")
	Expect(err).NotTo(HaveOccurred())

	expected := atc.DebugVersionsDB{
		Jobs: []atc.DebugJob{
			{Name: graphJob.Name(), ID: graphJob.ID()},
			{Name: idleJob.Name(), ID: idleJob.ID()},
		},
		Resources: []atc.DebugResource{
			{Name: inputResource.Name(), ID: inputResource.ID(), ScopeID: pipelineDebugScopeID(inputResource.ResourceConfigScopeID())},
			{Name: outputResource.Name(), ID: outputResource.ID(), ScopeID: pipelineDebugScopeID(outputResource.ResourceConfigScopeID())},
			{Name: idleResource.Name(), ID: idleResource.ID(), ScopeID: nil},
		},
		ResourceVersions: []atc.DebugResourceVersion{
			{
				VersionID:  inputResourceVersion.ID(),
				ResourceID: inputResource.ID(),
				CheckOrder: inputResourceVersion.CheckOrder(),
				ScopeID:    inputResource.ResourceConfigScopeID(),
			},
			{
				VersionID:  outputResourceVersion.ID(),
				ResourceID: outputResource.ID(),
				CheckOrder: outputResourceVersion.CheckOrder(),
				ScopeID:    outputResource.ResourceConfigScopeID(),
			},
		},
		BuildOutputs: []atc.DebugBuildOutput{
			{
				DebugResourceVersion: atc.DebugResourceVersion{
					VersionID:  outputResourceVersion.ID(),
					ResourceID: outputResource.ID(),
					CheckOrder: outputResourceVersion.CheckOrder(),
					ScopeID:    outputResource.ResourceConfigScopeID(),
				},
				BuildID: explicitBuild.ID(),
				JobID:   graphJob.ID(),
			},
			{
				DebugResourceVersion: atc.DebugResourceVersion{
					VersionID:  inputResourceVersion.ID(),
					ResourceID: inputResource.ID(),
					CheckOrder: inputResourceVersion.CheckOrder(),
					ScopeID:    inputResource.ResourceConfigScopeID(),
				},
				BuildID: inputBuild.ID(),
				JobID:   graphJob.ID(),
			},
		},
		BuildInputs: []atc.DebugBuildInput{{
			DebugResourceVersion: atc.DebugResourceVersion{
				VersionID:  inputResourceVersion.ID(),
				ResourceID: inputResource.ID(),
				CheckOrder: inputResourceVersion.CheckOrder(),
				ScopeID:    inputResource.ResourceConfigScopeID(),
			},
			BuildID:   inputBuild.ID(),
			JobID:     graphJob.ID(),
			InputName: "source-input",
		}},
		BuildReruns: []atc.DebugBuildRerun{{
			BuildID: rerunBuild.ID(),
			JobID:   graphJob.ID(),
			RerunOf: inputBuild.ID(),
		}},
	}

	decoyTeam, err := database.Deps.teamFactory.CreateTeam(atc.Team{Name: "decoy-team"})
	Expect(err).NotTo(HaveOccurred())
	decoyPipeline := database.SavePipeline(decoyTeam, "a-pipeline", atc.Config{
		Resources: atc.ResourceConfigs{{
			Name: "decoy-resource", Type: dbtest.BaseResourceType, Source: atc.Source{"fixture": "decoy"},
		}},
		Jobs: atc.JobConfigs{{Name: "decoy-job"}},
	})
	decoyScenario := &dbtest.Scenario{Team: decoyTeam, Pipeline: decoyPipeline}
	decoyVersion := atc.Version{"ref": "decoy-checked"}
	decoyScenario.Run(builder.WithResourceVersions("decoy-resource", decoyVersion))
	decoyResource := decoyScenario.Resource("decoy-resource")
	decoyCheckedVersion, found, err := decoyResource.FindVersion(decoyVersion)
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	decoyJob := decoyScenario.Job("decoy-job")
	decoyBuild, err := decoyJob.CreateBuild("pipeline-api-debug-decoy")
	Expect(err).NotTo(HaveOccurred())
	decoyOutputVersion := atc.Version{"ref": "decoy-output"}
	Expect(decoyBuild.SaveOutput(
		decoyResource.Type(),
		nil,
		decoyResource.Source(),
		decoyOutputVersion,
		nil,
		"decoy-output",
		decoyResource.Name(),
	)).To(Succeed())
	Expect(decoyBuild.Finish(db.BuildStatusSucceeded)).To(Succeed())
	decoyResource = decoyScenario.Resource("decoy-resource")
	decoyOutputResourceVersion, found, err := decoyResource.FindVersion(decoyOutputVersion)
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())

	return pipelineDebugVersionsFixture{
		Database: database,
		Team:     targetTeam,
		Expected: expected,
		Decoy: pipelineDebugDecoyIDs{
			resourceIDs: []int{decoyResource.ID()},
			versionIDs:  []int{decoyCheckedVersion.ID(), decoyOutputResourceVersion.ID()},
			buildIDs:    []int{decoyBuild.ID()},
			jobIDs:      []int{decoyJob.ID()},
			scopeIDs:    []int{decoyResource.ResourceConfigScopeID()},
		},
	}
}

func copyPipelineDebugVersionsDB(source atc.DebugVersionsDB) atc.DebugVersionsDB {
	cloned := source
	cloned.Jobs = append([]atc.DebugJob(nil), source.Jobs...)
	cloned.Resources = append([]atc.DebugResource(nil), source.Resources...)
	cloned.ResourceVersions = append([]atc.DebugResourceVersion(nil), source.ResourceVersions...)
	cloned.BuildOutputs = append([]atc.DebugBuildOutput(nil), source.BuildOutputs...)
	cloned.BuildInputs = append([]atc.DebugBuildInput(nil), source.BuildInputs...)
	cloned.BuildReruns = append([]atc.DebugBuildRerun(nil), source.BuildReruns...)
	return cloned
}

func pipelineDebugScopeSortValue(scopeID *int) (bool, int) {
	if scopeID == nil {
		return false, 0
	}
	return true, *scopeID
}

func normalizePipelineDebugVersionsDB(source atc.DebugVersionsDB) atc.DebugVersionsDB {
	normalized := copyPipelineDebugVersionsDB(source)

	sort.Slice(normalized.Jobs, func(i, j int) bool {
		left, right := normalized.Jobs[i], normalized.Jobs[j]
		if left.Name != right.Name {
			return left.Name < right.Name
		}
		return left.ID < right.ID
	})
	sort.Slice(normalized.Resources, func(i, j int) bool {
		left, right := normalized.Resources[i], normalized.Resources[j]
		if left.Name != right.Name {
			return left.Name < right.Name
		}
		if left.ID != right.ID {
			return left.ID < right.ID
		}
		leftValid, leftScope := pipelineDebugScopeSortValue(left.ScopeID)
		rightValid, rightScope := pipelineDebugScopeSortValue(right.ScopeID)
		if leftValid != rightValid {
			return !leftValid
		}
		return leftScope < rightScope
	})
	sort.Slice(normalized.ResourceVersions, func(i, j int) bool {
		left, right := normalized.ResourceVersions[i], normalized.ResourceVersions[j]
		if left.ResourceID != right.ResourceID {
			return left.ResourceID < right.ResourceID
		}
		if left.ScopeID != right.ScopeID {
			return left.ScopeID < right.ScopeID
		}
		if left.CheckOrder != right.CheckOrder {
			return left.CheckOrder < right.CheckOrder
		}
		return left.VersionID < right.VersionID
	})
	sort.Slice(normalized.BuildOutputs, func(i, j int) bool {
		left, right := normalized.BuildOutputs[i], normalized.BuildOutputs[j]
		if left.ResourceID != right.ResourceID {
			return left.ResourceID < right.ResourceID
		}
		if left.ScopeID != right.ScopeID {
			return left.ScopeID < right.ScopeID
		}
		if left.CheckOrder != right.CheckOrder {
			return left.CheckOrder < right.CheckOrder
		}
		if left.VersionID != right.VersionID {
			return left.VersionID < right.VersionID
		}
		if left.JobID != right.JobID {
			return left.JobID < right.JobID
		}
		return left.BuildID < right.BuildID
	})
	sort.Slice(normalized.BuildInputs, func(i, j int) bool {
		left, right := normalized.BuildInputs[i], normalized.BuildInputs[j]
		if left.ResourceID != right.ResourceID {
			return left.ResourceID < right.ResourceID
		}
		if left.ScopeID != right.ScopeID {
			return left.ScopeID < right.ScopeID
		}
		if left.CheckOrder != right.CheckOrder {
			return left.CheckOrder < right.CheckOrder
		}
		if left.VersionID != right.VersionID {
			return left.VersionID < right.VersionID
		}
		if left.InputName != right.InputName {
			return left.InputName < right.InputName
		}
		if left.JobID != right.JobID {
			return left.JobID < right.JobID
		}
		return left.BuildID < right.BuildID
	})
	sort.Slice(normalized.BuildReruns, func(i, j int) bool {
		left, right := normalized.BuildReruns[i], normalized.BuildReruns[j]
		if left.JobID != right.JobID {
			return left.JobID < right.JobID
		}
		if left.RerunOf != right.RerunOf {
			return left.RerunOf < right.RerunOf
		}
		return left.BuildID < right.BuildID
	})

	return normalized
}

func expectPipelineDebugCardinalities(actual atc.DebugVersionsDB, expected atc.DebugVersionsDB) {
	GinkgoHelper()
	Expect(actual.Jobs).To(HaveLen(len(expected.Jobs)))
	Expect(actual.Resources).To(HaveLen(len(expected.Resources)))
	Expect(actual.ResourceVersions).To(HaveLen(len(expected.ResourceVersions)))
	Expect(actual.BuildOutputs).To(HaveLen(len(expected.BuildOutputs)))
	Expect(actual.BuildInputs).To(HaveLen(len(expected.BuildInputs)))
	Expect(actual.BuildReruns).To(HaveLen(len(expected.BuildReruns)))
}

func expectPipelineDebugExcludesDecoy(actual atc.DebugVersionsDB, decoy pipelineDebugDecoyIDs) {
	GinkgoHelper()

	for _, resource := range actual.Resources {
		Expect(decoy.resourceIDs).NotTo(ContainElement(resource.ID))
		if resource.ScopeID != nil {
			Expect(decoy.scopeIDs).NotTo(ContainElement(*resource.ScopeID))
		}
	}
	for _, version := range actual.ResourceVersions {
		Expect(decoy.resourceIDs).NotTo(ContainElement(version.ResourceID))
		Expect(decoy.versionIDs).NotTo(ContainElement(version.VersionID))
		Expect(decoy.scopeIDs).NotTo(ContainElement(version.ScopeID))
	}
	for _, output := range actual.BuildOutputs {
		Expect(decoy.resourceIDs).NotTo(ContainElement(output.ResourceID))
		Expect(decoy.versionIDs).NotTo(ContainElement(output.VersionID))
		Expect(decoy.buildIDs).NotTo(ContainElement(output.BuildID))
		Expect(decoy.jobIDs).NotTo(ContainElement(output.JobID))
		Expect(decoy.scopeIDs).NotTo(ContainElement(output.ScopeID))
	}
	for _, input := range actual.BuildInputs {
		Expect(decoy.resourceIDs).NotTo(ContainElement(input.ResourceID))
		Expect(decoy.versionIDs).NotTo(ContainElement(input.VersionID))
		Expect(decoy.buildIDs).NotTo(ContainElement(input.BuildID))
		Expect(decoy.jobIDs).NotTo(ContainElement(input.JobID))
		Expect(decoy.scopeIDs).NotTo(ContainElement(input.ScopeID))
	}
	for _, rerun := range actual.BuildReruns {
		Expect(decoy.buildIDs).NotTo(ContainElement(rerun.BuildID))
		Expect(decoy.buildIDs).NotTo(ContainElement(rerun.RerunOf))
		Expect(decoy.jobIDs).NotTo(ContainElement(rerun.JobID))
	}
	for _, job := range actual.Jobs {
		Expect(decoy.jobIDs).NotTo(ContainElement(job.ID))
	}
}

type pipelineListingFixture struct {
	pipelines map[string]db.Pipeline
}

func expectPersistedPipelineShape(pipeline db.Pipeline, expected atc.Config) {
	GinkgoHelper()

	Expect(pipeline.Groups()).To(Equal(expected.Groups))
	Expect(pipeline.Display()).To(Equal(expected.Display))
	jobs, err := pipeline.Jobs()
	Expect(err).NotTo(HaveOccurred())
	Expect(jobs).To(HaveLen(len(expected.Jobs)))
	for _, expectedJob := range expected.Jobs {
		job, found, err := pipeline.Job(expectedJob.Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue(), "job %q was absent", expectedJob.Name)
		Expect(job.Name()).To(Equal(expectedJob.Name))
	}
	resources, err := pipeline.Resources()
	Expect(err).NotTo(HaveOccurred())
	Expect(resources).To(HaveLen(len(expected.Resources)))
	for _, expectedResource := range expected.Resources {
		resource, found, err := pipeline.Resource(expectedResource.Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue(), "resource %q was absent", expectedResource.Name)
		Expect(resource.Name()).To(Equal(expectedResource.Name))
	}
	Expect(pipeline.LastUpdated()).NotTo(BeZero())
}

func persistPipelineListingFixture(realdb *realDB) pipelineListingFixture {
	GinkgoHelper()

	anotherTeam, err := realdb.Deps.teamFactory.CreateTeam(atc.Team{Name: "another"})
	Expect(err).NotTo(HaveOccurred())

	mainPublicConfig := atc.Config{
		Groups: atc.GroupConfigs{{
			Name:      "group2",
			Jobs:      []string{"job3", "job4"},
			Resources: []string{"resource3", "resource4"},
		}},
		Jobs: atc.JobConfigs{{Name: "job3"}, {Name: "job4"}},
		Resources: atc.ResourceConfigs{
			{Name: "resource3", Type: "mock", Source: atc.Source{"key": "three"}},
			{Name: "resource4", Type: "mock", Source: atc.Source{"key": "four"}},
		},
		Display: &atc.DisplayConfig{BackgroundImage: "background.jpg"},
	}
	mainPrivateConfig := atc.Config{
		Groups: atc.GroupConfigs{{
			Name:      "group1",
			Jobs:      []string{"job1", "job2"},
			Resources: []string{"resource1", "resource2"},
		}},
		Jobs: atc.JobConfigs{{Name: "job1"}, {Name: "job2"}},
		Resources: atc.ResourceConfigs{
			{Name: "resource1", Type: "mock", Source: atc.Source{"key": "one"}},
			{Name: "resource2", Type: "mock", Source: atc.Source{"key": "two"}},
		},
	}
	anotherPublicConfig := atc.Config{Jobs: atc.JobConfigs{{Name: "public-job"}}}
	anotherPrivateConfig := atc.Config{Jobs: atc.JobConfigs{{Name: "private-job"}}}

	pipelines := map[string]db.Pipeline{
		"public-main":   realdb.SavePipeline(realdb.Main, "public-pipeline", mainPublicConfig),
		"private-main":  realdb.SavePipeline(realdb.Main, "private-pipeline", mainPrivateConfig),
		"public-other":  realdb.SavePipeline(anotherTeam, "another-pipeline", anotherPublicConfig),
		"private-other": realdb.SavePipeline(anotherTeam, "another-private-pipeline", anotherPrivateConfig),
	}
	configs := map[string]atc.Config{
		"public-main":   mainPublicConfig,
		"private-main":  mainPrivateConfig,
		"public-other":  anotherPublicConfig,
		"private-other": anotherPrivateConfig,
	}
	Expect(pipelines["public-main"].Expose()).To(Succeed())
	Expect(pipelines["public-other"].Expose()).To(Succeed())
	Expect(pipelines["public-main"].Pause("api-test")).To(Succeed())
	Expect(pipelines["public-other"].Pause("api-test")).To(Succeed())

	archiveRequestedAt := time.Now()
	Expect(pipelines["private-main"].Archive()).To(Succeed())

	for name, pipeline := range pipelines {
		found, err := pipeline.Reload()
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		expectPersistedPipelineShape(pipeline, configs[name])
	}

	Expect(pipelines["public-main"].Public()).To(BeTrue())
	Expect(pipelines["public-main"].Paused()).To(BeTrue())
	Expect(pipelines["public-main"].PausedBy()).To(Equal("api-test"))
	Expect(pipelines["public-main"].Archived()).To(BeFalse())
	Expect(pipelines["public-main"].Groups()).To(Equal(mainPublicConfig.Groups))
	Expect(pipelines["public-main"].Display()).To(Equal(mainPublicConfig.Display))
	Expect(pipelines["private-main"].Public()).To(BeFalse())
	Expect(pipelines["private-main"].Archived()).To(BeTrue())
	Expect(pipelines["private-main"].Paused()).To(BeTrue())
	Expect(pipelines["private-main"].PausedAt()).To(BeTemporally(">=", archiveRequestedAt))
	Expect(pipelines["private-main"].PausedBy()).To(Equal("automatic-pipeline-archiver"))
	Expect(pipelines["private-main"].Groups()).To(Equal(mainPrivateConfig.Groups))
	Expect(pipelines["private-main"].Display()).To(Equal(mainPrivateConfig.Display))
	Expect(pipelines["public-other"].Public()).To(BeTrue())
	Expect(pipelines["public-other"].Paused()).To(BeTrue())
	Expect(pipelines["public-other"].PausedBy()).To(Equal("api-test"))
	Expect(pipelines["public-other"].Archived()).To(BeFalse())
	Expect(pipelines["private-other"].Public()).To(BeFalse())
	Expect(pipelines["private-other"].Paused()).To(BeFalse())
	Expect(pipelines["private-other"].Archived()).To(BeFalse())

	return pipelineListingFixture{pipelines: pipelines}
}

func expectPresentedPipeline(actual atc.Pipeline, expected db.Pipeline) {
	GinkgoHelper()

	Expect(actual.ID).To(Equal(expected.ID()))
	Expect(actual.Name).To(Equal(expected.Name()))
	Expect(actual.InstanceVars).To(Equal(expected.InstanceVars()))
	Expect(actual.TeamName).To(Equal(expected.TeamName()))
	Expect(actual.Paused).To(Equal(expected.Paused()))
	Expect(actual.PausedBy).To(Equal(expected.PausedBy()))
	if expected.PausedAt().IsZero() {
		Expect(actual.PausedAt).To(BeZero())
	} else {
		Expect(actual.PausedAt).To(Equal(expected.PausedAt().Unix()))
	}
	Expect(actual.Public).To(Equal(expected.Public()))
	Expect(actual.Archived).To(Equal(expected.Archived()))
	Expect(actual.Groups).To(Equal(expected.Groups()))
	Expect(actual.Display).To(Equal(expected.Display()))
	Expect(actual.LastUpdated).To(Equal(expected.LastUpdated().Unix()))
}

func expectPipelineResponse(response *http.Response, expected ...db.Pipeline) {
	GinkgoHelper()

	body, err := io.ReadAll(response.Body)
	Expect(err).NotTo(HaveOccurred())
	var actual []atc.Pipeline
	Expect(json.Unmarshal(body, &actual)).To(Succeed())
	Expect(actual).To(HaveLen(len(expected)))

	actualByID := map[int]atc.Pipeline{}
	for _, pipeline := range actual {
		actualByID[pipeline.ID] = pipeline
	}
	Expect(actualByID).To(HaveLen(len(expected)))
	for _, pipeline := range expected {
		presented, found := actualByID[pipeline.ID()]
		Expect(found).To(BeTrue(), "pipeline ID %d was absent", pipeline.ID())
		expectPresentedPipeline(presented, pipeline)
	}
}

func normalizedInstanceVars(pipelines []db.Pipeline, pipelineName string) []atc.InstanceVars {
	GinkgoHelper()

	var normalized []atc.InstanceVars
	for _, pipeline := range pipelines {
		if pipeline.Name() != pipelineName {
			continue
		}
		instanceVars := pipeline.InstanceVars()
		if instanceVars == nil {
			instanceVars = atc.InstanceVars{}
		}
		normalized = append(normalized, instanceVars)
	}
	return normalized
}

var _ = Describe("Pipelines API", func() {
	Describe("GET /api/v1/pipelines", func() {
		var (
			response  *http.Response
			listingDB *realDB
			pipelines map[string]db.Pipeline
		)

		BeforeEach(func() {
			listingDB = useRealDB()
			fixture := persistPipelineListingFixture(listingDB)
			pipelines = fixture.pipelines
			server = listingDB.Serve()
		})

		JustBeforeEach(func() {
			req, err := http.NewRequest("GET", server.URL+"/api/v1/pipelines", nil)
			Expect(err).NotTo(HaveOccurred())

			req.Header.Set("Content-Type", "application/json")

			response, err = client.Do(req)
			Expect(err).NotTo(HaveOccurred())
		})

		It("returns 200 OK", func() {
			Expect(response.StatusCode).To(Equal(http.StatusOK))
		})

		It("returns application/json", func() {
			expectedHeaderEntries := map[string]string{
				"Content-Type": "application/json",
			}
			Expect(response).Should(IncludeHeaderEntries(expectedHeaderEntries))
		})

		It("returns public pipeline objects from both teams", func() {
			expectPipelineResponse(response, pipelines["public-main"], pipelines["public-other"])
		})

		Context("when team is set in user context", func() {
			BeforeEach(func() {
				unrelatedTeam, err := listingDB.Deps.teamFactory.CreateTeam(atc.Team{Name: "unrelated"})
				Expect(err).NotTo(HaveOccurred())
				grantProfile(unrelatedTeam, memberProfile, accessor.ViewerRole)
				useProfile(memberProfile)
			})

			It("does not grant visibility to an unrelated team", func() {
				expectPipelineResponse(response, pipelines["public-main"], pipelines["public-other"])
			})
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				useProfile(anonymousProfile)
			})

			It("returns only public pipelines", func() {
				expectPipelineResponse(response, pipelines["public-main"], pipelines["public-other"])
			})
		})

		Context("when authenticated", func() {
			BeforeEach(func() {
				grantProfile(listingDB.Main, memberProfile, accessor.ViewerRole)
				useProfile(memberProfile)
			})

			It("returns all pipelines of the team + all public pipelines", func() {
				expectPipelineResponse(response,
					pipelines["public-main"],
					pipelines["private-main"],
					pipelines["public-other"],
				)
			})

			Context("user has the Admin privilege", func() {
				BeforeEach(func() {
					useProfile(adminProfile)
				})

				It("user can see all private and public pipelines from all teams", func() {
					expectPipelineResponse(response,
						pipelines["public-main"],
						pipelines["private-main"],
						pipelines["public-other"],
						pipelines["private-other"],
					)
				})
			})

		})
	})

	Describe("GET /api/v1/teams/:team_name/pipelines", func() {
		var (
			response  *http.Response
			listingDB *realDB
			pipelines map[string]db.Pipeline
		)

		BeforeEach(func() {
			listingDB = useRealDB()
			fixture := persistPipelineListingFixture(listingDB)
			pipelines = fixture.pipelines
		})

		JustBeforeEach(func() {
			server = listingDB.Serve()
			req, err := http.NewRequest("GET", server.URL+"/api/v1/teams/main/pipelines", nil)
			Expect(err).NotTo(HaveOccurred())

			req.Header.Set("Content-Type", "application/json")

			response, err = client.Do(req)
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when authenticated as requested team", func() {
			BeforeEach(func() {
				grantProfile(listingDB.Main, memberProfile, accessor.ViewerRole)
				useProfile(memberProfile)
			})

			It("returns 200 OK", func() {
				Expect(response.StatusCode).To(Equal(http.StatusOK))
			})

			It("returns application/json", func() {
				expectedHeaderEntries := map[string]string{
					"Content-Type": "application/json",
				}
				Expect(response).Should(IncludeHeaderEntries(expectedHeaderEntries))
			})

			It("returns the persisted team pipeline objects", func() {
				expectPipelineResponse(response, pipelines["public-main"], pipelines["private-main"])
				Expect(pipelines["private-main"].PausedAt()).NotTo(BeZero())
				Expect(pipelines["private-main"].PausedBy()).To(Equal("automatic-pipeline-archiver"))
			})

			It("returns all team's pipelines", func() {
				body, err := io.ReadAll(response.Body)
				Expect(err).NotTo(HaveOccurred())
				var actual []map[string]any
				Expect(json.Unmarshal(body, &actual)).To(Succeed())

				Expect(actual).To(ConsistOf(
					HaveKeyWithValue("id", BeNumerically("==", pipelines["public-main"].ID())),
					HaveKeyWithValue("id", BeNumerically("==", pipelines["private-main"].ID())),
				))
			})

		})

		Context("when authenticated as another team", func() {
			BeforeEach(func() {
				anotherTeam, found, err := listingDB.Deps.teamFactory.FindTeam("another")
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				grantProfile(anotherTeam, memberProfile, accessor.ViewerRole)
				useProfile(memberProfile)
			})

			It("returns only team's public pipelines", func() {
				expectPipelineResponse(response, pipelines["public-main"])
			})
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				useProfile(anonymousProfile)
			})

			It("returns public pipelines and 404 for a missing team", func() {
				expectPipelineResponse(response, pipelines["public-main"])

				missing, err := client.Get(server.URL + "/api/v1/teams/missing-team/pipelines")
				Expect(err).NotTo(HaveOccurred())
				DeferCleanup(missing.Body.Close)
				Expect(missing.StatusCode).To(Equal(http.StatusNotFound))
			})
		})
	})

	Describe("GET /api/v1/teams/:team_name/pipelines/:pipeline_name", func() {
		var (
			response       *http.Response
			detailDB       *realDB
			detailPipeline db.Pipeline
			requestTeam    = "main"
		)

		BeforeEach(func() {
			detailDB = useRealDB()
			detailPipeline = detailDB.SavePipeline(detailDB.Main, "some-specific-pipeline", atc.Config{
				Groups: atc.GroupConfigs{
					{Name: "group1", Jobs: []string{"job1", "job2"}, Resources: []string{"resource1", "resource2"}},
					{Name: "group2", Jobs: []string{"job3", "job4"}, Resources: []string{"resource3", "resource4"}},
				},
				Display: &atc.DisplayConfig{BackgroundImage: "background.jpg"},
			})
			Expect(detailPipeline.Expose()).To(Succeed())
			server = detailDB.Serve()
			requestTeam = "main"
		})

		JustBeforeEach(func() {
			req, err := http.NewRequest("GET", server.URL+"/api/v1/teams/"+requestTeam+"/pipelines/some-specific-pipeline", nil)
			Expect(err).NotTo(HaveOccurred())

			req.Header.Set("Content-Type", "application/json")

			response, err = client.Do(req)
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				useProfile(anonymousProfile)
				Expect(detailPipeline.Hide()).To(Succeed())
			})

			It("returns 401", func() {
				Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
			})
		})

		Context("when authenticated as requested team", func() {
			BeforeEach(func() {
				grantProfile(detailDB.Main, memberProfile, accessor.ViewerRole)
				useProfile(memberProfile)
			})

			It("returns 200 ok", func() {
				Expect(response.StatusCode).To(Equal(http.StatusOK))
			})

			It("returns application/json", func() {
				expectedHeaderEntries := map[string]string{
					"Content-Type": "application/json",
				}
				Expect(response).Should(IncludeHeaderEntries(expectedHeaderEntries))
			})

			It("returns a pipeline JSON", func() {
				body, err := io.ReadAll(response.Body)
				Expect(err).NotTo(HaveOccurred())

				var pipeline atc.Pipeline
				Expect(json.Unmarshal(body, &pipeline)).To(Succeed())
				Expect(pipeline.ID).To(Equal(detailPipeline.ID()))
				Expect(pipeline.Name).To(Equal(detailPipeline.Name()))
				Expect(pipeline.TeamName).To(Equal("main"))
				Expect(pipeline.Public).To(BeTrue())
				Expect(pipeline.Groups).To(Equal(detailPipeline.Groups()))
				Expect(pipeline.Display).To(Equal(detailPipeline.Display()))
			})
		})

		Context("when authenticated as another team", func() {
			BeforeEach(func() {
				useProfile(memberProfile)
			})

			Context("and the pipeline is private", func() {
				BeforeEach(func() {
					Expect(detailPipeline.Hide()).To(Succeed())
				})

				It("returns 403", func() {
					Expect(response.StatusCode).To(Equal(http.StatusForbidden))
				})
			})

			Context("and the pipeline is public", func() {
				BeforeEach(func() {
					Expect(detailPipeline.Expose()).To(Succeed())
				})

				It("returns 200 OK", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
				})
			})
		})

		Context("when not authenticated at all", func() {
			BeforeEach(func() {
				useProfile(anonymousProfile)
			})

			Context("and the pipeline is private", func() {
				BeforeEach(func() {
					Expect(detailPipeline.Hide()).To(Succeed())
				})

				It("returns 401", func() {
					Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
				})
			})

			Context("and the pipeline is public", func() {
				BeforeEach(func() {
					Expect(detailPipeline.Expose()).To(Succeed())
				})

				It("returns 200 OK", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
				})
			})
		})
	})

	Describe("GET /api/v1/teams/:team_name/pipelines/:pipeline_name/badge", func() {
		var response *http.Response
		var (
			badgeDB         *realDB
			badgePipeline   db.Pipeline
			teamName        = "some-team"
			selectedProfile requestProfile
			grantRole       string
		)

		persistBadgePipeline := func(config atc.Config, statuses map[string]db.BuildStatus) {
			badgeDB = useRealDB()
			badgePipeline = badgeDB.SavePipeline(badgeDB.Main, "some-pipeline", config)
			server = badgeDB.Serve()
			teamName = "main"
			for jobName, status := range statuses {
				job, found, err := badgePipeline.Job(jobName)
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				build, err := job.CreateBuild("api-badge-test")
				Expect(err).NotTo(HaveOccurred())
				started, err := build.Start(atc.Plan{})
				Expect(err).NotTo(HaveOccurred())
				Expect(started).To(BeTrue())
				Expect(build.Finish(status)).To(Succeed())
			}
		}

		BeforeEach(func() {
			teamName = "some-team"
			selectedProfile = anonymousProfile
			grantRole = ""
		})

		JustBeforeEach(func() {
			var err error
			if grantRole != "" {
				grantProfile(badgeDB.Main, selectedProfile, grantRole)
			}
			useProfile(selectedProfile)

			response, err = client.Get(server.URL + "/api/v1/teams/" + teamName + "/pipelines/some-pipeline/badge")
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when not authorized", func() {
			Context("and the pipeline is private", func() {
				BeforeEach(func() {
					persistBadgePipeline(atc.Config{Jobs: atc.JobConfigs{{Name: "private-job"}}}, nil)
				})

				Context("when user is authenticated", func() {
					BeforeEach(func() {
						selectedProfile = memberProfile
					})
					It("returns 403", func() {
						Expect(response.StatusCode).To(Equal(http.StatusForbidden))
					})
				})

				Context("when user is not authenticated", func() {
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
					persistBadgePipeline(atc.Config{Jobs: atc.JobConfigs{{Name: "public-job"}}}, nil)
					Expect(badgePipeline.Expose()).To(Succeed())
				})

				It("returns 200 OK", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
				})
			})
		})

		Context("when authorized", func() {
			BeforeEach(func() {
				selectedProfile = memberProfile
				grantRole = accessor.ViewerRole
			})

			Context("when the pipeline has no finished builds", func() {
				BeforeEach(func() {
					persistBadgePipeline(atc.Config{Jobs: atc.JobConfigs{{Name: "no-build"}}}, nil)
				})

				It("returns 200 OK", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
				})

				It("returns Content-Type as image/svg+xml and disables caching", func() {
					expectedHeaderEntries := map[string]string{
						"Content-Type":  "image/svg+xml",
						"Cache-Control": "no-cache, no-store, must-revalidate",
						"Expires":       "0",
					}
					Expect(response).Should(IncludeHeaderEntries(expectedHeaderEntries))
				})

				It("returns an unknown badge", func() {
					body, err := io.ReadAll(response.Body)
					Expect(err).NotTo(HaveOccurred())

					Expect(string(body)).To(Equal(`<?xml version="1.0" encoding="UTF-8"?>
<svg xmlns="http://www.w3.org/2000/svg" width="98" height="20">
   <linearGradient id="b" x2="0" y2="100%">
      <stop offset="0" stop-color="#bbb" stop-opacity=".1" />
      <stop offset="1" stop-opacity=".1" />
   </linearGradient>
   <mask id="a">
      <rect width="98" height="20" rx="3" fill="#fff" />
   </mask>
   <g mask="url(#a)">
      <path fill="#555" d="M0 0h37v20H0z" />
      <path fill="#9f9f9f" d="M37 0h61v20H37z" />
      <path fill="url(#b)" d="M0 0h98v20H0z" />
   </g>
   <g fill="#fff" text-anchor="middle" font-family="DejaVu Sans,Verdana,Geneva,sans-serif" font-size="11">
      <text x="18.5" y="15" fill="#010101" fill-opacity=".3">build</text>
      <text x="18.5" y="14">build</text>
      <text x="66.5" y="15" fill="#010101" fill-opacity=".3">unknown</text>
      <text x="66.5" y="14">unknown</text>
   </g>
</svg>`))
				})
			})

			Context("when the pipeline has a successful build", func() {
				BeforeEach(func() {
					persistBadgePipeline(atc.Config{Jobs: atc.JobConfigs{{Name: "succeeded"}}}, map[string]db.BuildStatus{"succeeded": db.BuildStatusSucceeded})
				})

				It("returns a successful badge", func() {
					body, err := io.ReadAll(response.Body)
					Expect(err).NotTo(HaveOccurred())

					Expect(string(body)).To(Equal(`<?xml version="1.0" encoding="UTF-8"?>
<svg xmlns="http://www.w3.org/2000/svg" width="88" height="20">
   <linearGradient id="b" x2="0" y2="100%">
      <stop offset="0" stop-color="#bbb" stop-opacity=".1" />
      <stop offset="1" stop-opacity=".1" />
   </linearGradient>
   <mask id="a">
      <rect width="88" height="20" rx="3" fill="#fff" />
   </mask>
   <g mask="url(#a)">
      <path fill="#555" d="M0 0h37v20H0z" />
      <path fill="#44cc11" d="M37 0h51v20H37z" />
      <path fill="url(#b)" d="M0 0h88v20H0z" />
   </g>
   <g fill="#fff" text-anchor="middle" font-family="DejaVu Sans,Verdana,Geneva,sans-serif" font-size="11">
      <text x="18.5" y="15" fill="#010101" fill-opacity=".3">build</text>
      <text x="18.5" y="14">build</text>
      <text x="61.5" y="15" fill="#010101" fill-opacity=".3">passing</text>
      <text x="61.5" y="14">passing</text>
   </g>
</svg>`))
				})
			})

			Context("when the pipeline has an aborted build", func() {
				BeforeEach(func() {
					persistBadgePipeline(atc.Config{Jobs: atc.JobConfigs{{Name: "succeeded"}, {Name: "aborted"}}}, map[string]db.BuildStatus{"succeeded": db.BuildStatusSucceeded, "aborted": db.BuildStatusAborted})
				})

				It("returns an aborted badge", func() {
					body, err := io.ReadAll(response.Body)
					Expect(err).NotTo(HaveOccurred())

					Expect(string(body)).To(Equal(`<?xml version="1.0" encoding="UTF-8"?>
<svg xmlns="http://www.w3.org/2000/svg" width="90" height="20">
   <linearGradient id="b" x2="0" y2="100%">
      <stop offset="0" stop-color="#bbb" stop-opacity=".1" />
      <stop offset="1" stop-opacity=".1" />
   </linearGradient>
   <mask id="a">
      <rect width="90" height="20" rx="3" fill="#fff" />
   </mask>
   <g mask="url(#a)">
      <path fill="#555" d="M0 0h37v20H0z" />
      <path fill="#8f4b2d" d="M37 0h53v20H37z" />
      <path fill="url(#b)" d="M0 0h90v20H0z" />
   </g>
   <g fill="#fff" text-anchor="middle" font-family="DejaVu Sans,Verdana,Geneva,sans-serif" font-size="11">
      <text x="18.5" y="15" fill="#010101" fill-opacity=".3">build</text>
      <text x="18.5" y="14">build</text>
      <text x="62.5" y="15" fill="#010101" fill-opacity=".3">aborted</text>
      <text x="62.5" y="14">aborted</text>
   </g>
</svg>`))
				})
			})

			Context("when the pipeline has an errored build", func() {
				BeforeEach(func() {
					persistBadgePipeline(atc.Config{Jobs: atc.JobConfigs{{Name: "succeeded"}, {Name: "aborted"}, {Name: "errored"}}}, map[string]db.BuildStatus{"succeeded": db.BuildStatusSucceeded, "aborted": db.BuildStatusAborted, "errored": db.BuildStatusErrored})
				})

				It("returns an errored badge", func() {
					body, err := io.ReadAll(response.Body)
					Expect(err).NotTo(HaveOccurred())

					Expect(string(body)).To(Equal(`<?xml version="1.0" encoding="UTF-8"?>
<svg xmlns="http://www.w3.org/2000/svg" width="88" height="20">
   <linearGradient id="b" x2="0" y2="100%">
      <stop offset="0" stop-color="#bbb" stop-opacity=".1" />
      <stop offset="1" stop-opacity=".1" />
   </linearGradient>
   <mask id="a">
      <rect width="88" height="20" rx="3" fill="#fff" />
   </mask>
   <g mask="url(#a)">
      <path fill="#555" d="M0 0h37v20H0z" />
      <path fill="#fe7d37" d="M37 0h51v20H37z" />
      <path fill="url(#b)" d="M0 0h88v20H0z" />
   </g>
   <g fill="#fff" text-anchor="middle" font-family="DejaVu Sans,Verdana,Geneva,sans-serif" font-size="11">
      <text x="18.5" y="15" fill="#010101" fill-opacity=".3">build</text>
      <text x="18.5" y="14">build</text>
      <text x="61.5" y="15" fill="#010101" fill-opacity=".3">errored</text>
      <text x="61.5" y="14">errored</text>
   </g>
</svg>`))
				})
			})

			Context("when the pipeline has a failed build", func() {
				BeforeEach(func() {
					persistBadgePipeline(atc.Config{Jobs: atc.JobConfigs{{Name: "succeeded"}, {Name: "aborted"}, {Name: "errored"}, {Name: "failed"}}}, map[string]db.BuildStatus{"succeeded": db.BuildStatusSucceeded, "aborted": db.BuildStatusAborted, "errored": db.BuildStatusErrored, "failed": db.BuildStatusFailed})
				})

				It("returns a failed badge", func() {
					body, err := io.ReadAll(response.Body)
					Expect(err).NotTo(HaveOccurred())

					Expect(string(body)).To(Equal(`<?xml version="1.0" encoding="UTF-8"?>
<svg xmlns="http://www.w3.org/2000/svg" width="80" height="20">
   <linearGradient id="b" x2="0" y2="100%">
      <stop offset="0" stop-color="#bbb" stop-opacity=".1" />
      <stop offset="1" stop-opacity=".1" />
   </linearGradient>
   <mask id="a">
      <rect width="80" height="20" rx="3" fill="#fff" />
   </mask>
   <g mask="url(#a)">
      <path fill="#555" d="M0 0h37v20H0z" />
      <path fill="#e05d44" d="M37 0h43v20H37z" />
      <path fill="url(#b)" d="M0 0h80v20H0z" />
   </g>
   <g fill="#fff" text-anchor="middle" font-family="DejaVu Sans,Verdana,Geneva,sans-serif" font-size="11">
      <text x="18.5" y="15" fill="#010101" fill-opacity=".3">build</text>
      <text x="18.5" y="14">build</text>
      <text x="57.5" y="15" fill="#010101" fill-opacity=".3">failing</text>
      <text x="57.5" y="14">failing</text>
   </g>
</svg>`))
				})
			})
		})
	})

	Describe("DELETE /api/v1/teams/:team_name/pipelines/:pipeline_name", func() {
		var (
			response    *http.Response
			deleteDB    *realDB
			requestTeam string
		)

		BeforeEach(func() {
			requestTeam = "a-team"
		})

		JustBeforeEach(func() {
			pipelineName := "a-pipeline-name"
			req, err := http.NewRequest("DELETE", server.URL+"/api/v1/teams/"+requestTeam+"/pipelines/"+pipelineName, nil)
			Expect(err).NotTo(HaveOccurred())

			req.Header.Set("Content-Type", "application/json")

			response, err = client.Do(req)
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when authenticated", func() {
			BeforeEach(func() {
				useProfile(memberProfile)
			})

			Context("when requester belongs to the team", func() {
				Context("when deleting succeeds", func() {
					BeforeEach(func() {
						deleteDB = useRealDB()
						deleteDB.SavePipeline(deleteDB.Main, "a-pipeline-name", atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}})
						grantProfile(deleteDB.Main, memberProfile, accessor.MemberRole)
						server = deleteDB.Serve()
						requestTeam = "main"
					})

					It("returns 204 and removes the named pipeline from PostgreSQL", func() {
						Expect(response.StatusCode).To(Equal(http.StatusNoContent))
						pipeline, found, err := deleteDB.Main.Pipeline(atc.PipelineRef{Name: "a-pipeline-name"})
						Expect(err).NotTo(HaveOccurred())
						Expect(found).To(BeFalse())
						Expect(pipeline).To(BeNil())
					})
				})

			})

			Context("when requester does not belong to the team", func() {
				It("returns 403", func() {
					Expect(response.StatusCode).To(Equal(http.StatusForbidden))
				})
			})
		})

		Context("when the user is not logged in", func() {
			BeforeEach(func() {
				useProfile(anonymousProfile)
			})

			It("returns 401 Unauthorized", func() {
				Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
			})
		})
	})

	Describe("PUT /api/v1/teams/:team_name/pipelines/:pipeline_name/pause", func() {
		var (
			response          *http.Response
			realdb            *realDB
			persistedPipeline db.Pipeline
			requestTeam       = "a-team"
		)

		BeforeEach(func() {
			requestTeam = "a-team"
		})

		JustBeforeEach(func() {
			var err error

			request, err := http.NewRequest("PUT", server.URL+"/api/v1/teams/"+requestTeam+"/pipelines/a-pipeline/pause", nil)
			Expect(err).NotTo(HaveOccurred())

			response, err = client.Do(request)
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when authenticated", func() {
			BeforeEach(func() {
				useProfile(memberProfile)
			})

			Context("when requester belongs to the team", func() {
				var apiUserProfile requestProfile

				BeforeEach(func() {
					apiUserProfile = persistRequestProfile(
						"api-user-token", "api-user-subject", "api-user-id",
						"API User", "api-user",
					)
					useProfile(apiUserProfile)
				})

				Context("when pausing the pipeline succeeds", func() {
					BeforeEach(func() {
						realdb = useRealDB()
						persistedPipeline = realdb.SavePipeline(realdb.Main, "a-pipeline", atc.Config{
							Jobs: atc.JobConfigs{{Name: "job"}},
						})
						grantProfile(realdb.Main, apiUserProfile, accessor.OperatorRole)
						server = realdb.Serve()
						requestTeam = "main"
					})

					It("returns 200", func() {
						Expect(response.StatusCode).To(Equal(http.StatusOK))
					})

					It("persists pipeline pause through PostgreSQL", func() {
						found, err := persistedPipeline.Reload()
						Expect(err).NotTo(HaveOccurred())
						Expect(found).To(BeTrue())
						Expect(persistedPipeline.Paused()).To(BeTrue())
						Expect(persistedPipeline.PausedBy()).To(Equal("api-user"))
						Expect(persistedPipeline.PausedAt()).NotTo(BeZero())
					})
				})

			})

			Context("when requester does not belong to the team", func() {
				It("returns 403", func() {
					Expect(response.StatusCode).To(Equal(http.StatusForbidden))
				})
			})
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				useProfile(anonymousProfile)
			})

			It("returns 401", func() {
				Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
			})
		})
	})

	Describe("PUT /api/v1/teams/:team_name/pipelines/:pipeline_name/archive", func() {
		var (
			response         *http.Response
			archiveDB        *realDB
			archivedPipeline db.Pipeline
			archiveConfig    atc.Config
			requestedAt      time.Time
			requestTeam      = "a-team"
		)

		BeforeEach(func() {
			requestTeam = "a-team"
			useProfile(memberProfile)
		})

		JustBeforeEach(func() {
			request, _ := http.NewRequest("PUT", server.URL+"/api/v1/teams/"+requestTeam+"/pipelines/a-pipeline/archive", nil)
			var err error
			response, err = client.Do(request)
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when archiving succeeds", func() {
			BeforeEach(func() {
				archiveDB = useRealDB()
				archiveConfig = atc.Config{
					Groups: atc.GroupConfigs{{Name: "release", Jobs: []string{"ship"}, Resources: []string{"artifact"}}},
					Jobs:   atc.JobConfigs{{Name: "ship"}},
					Resources: atc.ResourceConfigs{{
						Name: "artifact", Type: "mock", Source: atc.Source{"uri": "archive://artifact"},
					}},
					Display: &atc.DisplayConfig{BackgroundImage: "archive.jpg"},
				}
				archivedPipeline = archiveDB.SavePipeline(archiveDB.Main, "a-pipeline", archiveConfig)
				grantProfile(archiveDB.Main, memberProfile, accessor.MemberRole)
				server = archiveDB.Serve()
				requestTeam = "main"
				requestedAt = time.Now()
			})

			It("returns 200 and archives the pipeline in PostgreSQL", func() {
				Expect(response.StatusCode).To(Equal(http.StatusOK))
				found, err := archivedPipeline.Reload()
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(archivedPipeline.Archived()).To(BeTrue())
				Expect(archivedPipeline.Paused()).To(BeTrue())
				Expect(archivedPipeline.PausedBy()).To(Equal("automatic-pipeline-archiver"))
				Expect(archivedPipeline.PausedAt()).To(BeTemporally(">=", requestedAt))
				Expect(archivedPipeline.LastUpdated()).To(BeTemporally(">=", requestedAt))
				Expect(archivedPipeline.Groups()).To(Equal(archiveConfig.Groups))
				Expect(archivedPipeline.Display()).To(Equal(archiveConfig.Display))
				expectPersistedPipelineShape(archivedPipeline, archiveConfig)
			})
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				useProfile(anonymousProfile)
			})

			It("returns 401", func() {
				Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
			})
		})
	})

	Describe("PUT /api/v1/teams/:team_name/pipelines/:pipeline_name/unpause", func() {
		var (
			response         *http.Response
			unpauseDB        *realDB
			unpausedPipeline db.Pipeline
			requestTeam      = "a-team"
		)

		BeforeEach(func() {
			requestTeam = "a-team"
		})

		JustBeforeEach(func() {
			var err error

			request, err := http.NewRequest("PUT", server.URL+"/api/v1/teams/"+requestTeam+"/pipelines/a-pipeline/unpause", nil)
			Expect(err).NotTo(HaveOccurred())

			response, err = client.Do(request)
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when authenticated", func() {
			BeforeEach(func() {
				useProfile(memberProfile)
			})
			Context("when requester belongs to the team", func() {
				Context("when unpausing the pipeline succeeds", func() {
					BeforeEach(func() {
						unpauseDB = useRealDB()
						unpausedPipeline = unpauseDB.SavePipeline(unpauseDB.Main, "a-pipeline", atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}})
						Expect(unpausedPipeline.Pause("fixture")).To(Succeed())
						grantProfile(unpauseDB.Main, memberProfile, accessor.OperatorRole)
						server = unpauseDB.Serve()
						requestTeam = "main"
					})

					It("returns 200", func() {
						Expect(response.StatusCode).To(Equal(http.StatusOK))
					})

					It("persists the unpaused state", func() {
						found, err := unpausedPipeline.Reload()
						Expect(err).NotTo(HaveOccurred())
						Expect(found).To(BeTrue())
						Expect(unpausedPipeline.Paused()).To(BeFalse())
						Expect(unpausedPipeline.PausedBy()).To(BeEmpty())
						Expect(unpausedPipeline.PausedAt()).To(BeZero())
					})
				})

			})

			Context("when requester does not belong to the team", func() {
				It("returns 403", func() {
					Expect(response.StatusCode).To(Equal(http.StatusForbidden))
				})
			})
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				useProfile(anonymousProfile)
			})

			It("returns 401 Unauthorized", func() {
				Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
			})
		})
	})

	Describe("PUT /api/v1/teams/:team_name/pipelines/:pipeline_name/expose", func() {
		var (
			response        *http.Response
			exposeDB        *realDB
			exposedPipeline db.Pipeline
			requestTeam     = "a-team"
		)

		BeforeEach(func() {
			requestTeam = "a-team"
		})

		JustBeforeEach(func() {
			var err error

			request, err := http.NewRequest("PUT", server.URL+"/api/v1/teams/"+requestTeam+"/pipelines/a-pipeline/expose", nil)
			Expect(err).NotTo(HaveOccurred())

			response, err = client.Do(request)
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when authenticated", func() {
			BeforeEach(func() {
				useProfile(memberProfile)
			})

			Context("when requester belongs to the team", func() {
				Context("when exposing the pipeline succeeds", func() {
					BeforeEach(func() {
						exposeDB = useRealDB()
						exposedPipeline = exposeDB.SavePipeline(exposeDB.Main, "a-pipeline", atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}})
						grantProfile(exposeDB.Main, memberProfile, accessor.MemberRole)
						server = exposeDB.Serve()
						requestTeam = "main"
					})

					It("returns 200", func() {
						Expect(response.StatusCode).To(Equal(http.StatusOK))
					})

					It("persists public visibility", func() {
						found, err := exposedPipeline.Reload()
						Expect(err).NotTo(HaveOccurred())
						Expect(found).To(BeTrue())
						Expect(exposedPipeline.Public()).To(BeTrue())
					})
				})

			})

			Context("when requester does not belong to the team", func() {
				It("returns 403", func() {
					Expect(response.StatusCode).To(Equal(http.StatusForbidden))
				})
			})
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				useProfile(anonymousProfile)
			})

			It("returns 401 Unauthorized", func() {
				Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
			})
		})
	})

	Describe("PUT /api/v1/teams/:team_name/pipelines/:pipeline_name/hide", func() {
		var (
			response       *http.Response
			hideDB         *realDB
			hiddenPipeline db.Pipeline
			requestTeam    = "a-team"
		)

		BeforeEach(func() {
			requestTeam = "a-team"
		})

		JustBeforeEach(func() {
			var err error

			request, err := http.NewRequest("PUT", server.URL+"/api/v1/teams/"+requestTeam+"/pipelines/a-pipeline/hide", nil)
			Expect(err).NotTo(HaveOccurred())

			response, err = client.Do(request)
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when authenticated", func() {
			BeforeEach(func() {
				useProfile(memberProfile)
			})
			Context("when requester belongs to the team", func() {
				Context("when hiding the pipeline succeeds", func() {
					BeforeEach(func() {
						hideDB = useRealDB()
						hiddenPipeline = hideDB.SavePipeline(hideDB.Main, "a-pipeline", atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}})
						Expect(hiddenPipeline.Expose()).To(Succeed())
						grantProfile(hideDB.Main, memberProfile, accessor.MemberRole)
						server = hideDB.Serve()
						requestTeam = "main"
					})

					It("returns 200", func() {
						Expect(response.StatusCode).To(Equal(http.StatusOK))
					})

					It("persists private visibility", func() {
						found, err := hiddenPipeline.Reload()
						Expect(err).NotTo(HaveOccurred())
						Expect(found).To(BeTrue())
						Expect(hiddenPipeline.Public()).To(BeFalse())
					})
				})

			})

			Context("when requester does not belong to the team", func() {
				It("returns 403 Forbidden", func() {
					Expect(response.StatusCode).To(Equal(http.StatusForbidden))
				})
			})
		})

		Context("when not authorized", func() {
			BeforeEach(func() {
				useProfile(anonymousProfile)
			})

			It("returns 401 Unauthorized", func() {
				Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
			})
		})
	})

	Describe("PUT /api/v1/teams/:team_name/pipelines/ordering", func() {
		var (
			response      *http.Response
			pipelineNames []string
			orderingDB    *realDB
			orderingTeam  db.Team
			initialNames  []string
			requestTeam   = "a-team"
		)

		BeforeEach(func() {
			requestTeam = "a-team"
			pipelineNames = []string{
				"a-pipeline",
				"another-pipeline",
				"yet-another-pipeline",
				"one-final-pipeline",
				"just-kidding",
			}
		})

		JustBeforeEach(func() {
			requestPayload, err := json.Marshal(pipelineNames)
			Expect(err).NotTo(HaveOccurred())

			request, err := http.NewRequest("PUT", server.URL+"/api/v1/teams/"+requestTeam+"/pipelines/ordering", bytes.NewBuffer(requestPayload))
			Expect(err).NotTo(HaveOccurred())

			response, err = client.Do(request)
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when authenticated", func() {
			BeforeEach(func() {
				useProfile(memberProfile)
			})

			Context("when requester belongs to the team", func() {
				Context("when ordering the pipelines succeeds", func() {
					BeforeEach(func() {
						orderingDB = useRealDB()
						var err error
						orderingTeam, err = orderingDB.Deps.teamFactory.CreateTeam(atc.Team{Name: "a-team"})
						Expect(err).NotTo(HaveOccurred())
						grantProfile(orderingTeam, memberProfile, accessor.MemberRole)
						for _, name := range []string{
							"just-kidding",
							"a-pipeline",
							"one-final-pipeline",
							"yet-another-pipeline",
							"another-pipeline",
						} {
							orderingDB.SavePipeline(orderingTeam, name, atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}})
						}
						initialPipelines, err := orderingTeam.Pipelines()
						Expect(err).NotTo(HaveOccurred())
						initialNames = make([]string, len(initialPipelines))
						for i, pipeline := range initialPipelines {
							initialNames[i] = pipeline.Name()
						}
						server = orderingDB.Serve()
					})

					It("persists the requested order from a deliberately different initial order", func() {
						Expect(initialNames).To(Equal([]string{
							"just-kidding",
							"a-pipeline",
							"one-final-pipeline",
							"yet-another-pipeline",
							"another-pipeline",
						}))
						pipelines, err := orderingTeam.Pipelines()
						Expect(err).NotTo(HaveOccurred())
						actualNames := make([]string, len(pipelines))
						for i, pipeline := range pipelines {
							actualNames[i] = pipeline.Name()
						}
						Expect(actualNames).To(Equal(pipelineNames))
						Expect(actualNames).NotTo(Equal(initialNames))
					})

					It("returns 200 and rejects malformed ordering JSON", func() {
						Expect(response.StatusCode).To(Equal(http.StatusOK))

						request, err := http.NewRequest(
							http.MethodPut,
							server.URL+"/api/v1/teams/a-team/pipelines/ordering",
							bytes.NewBufferString("{"),
						)
						Expect(err).NotTo(HaveOccurred())
						malformed, err := client.Do(request)
						Expect(err).NotTo(HaveOccurred())
						DeferCleanup(malformed.Body.Close)
						Expect(malformed.StatusCode).To(Equal(http.StatusBadRequest))
					})
				})

				Context("when a pipeline does not exist", func() {
					BeforeEach(func() {
						orderingDB = useRealDB()
						var err error
						orderingTeam, err = orderingDB.Deps.teamFactory.CreateTeam(atc.Team{Name: "a-team"})
						Expect(err).NotTo(HaveOccurred())
						grantProfile(orderingTeam, memberProfile, accessor.MemberRole)
						for _, name := range pipelineNames[1:] {
							orderingDB.SavePipeline(orderingTeam, name, atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}})
						}
						server = orderingDB.Serve()
					})

					It("returns 400", func() {
						Expect(response.StatusCode).To(Equal(http.StatusBadRequest))
						Expect(io.ReadAll(response.Body)).To(ContainSubstring("pipeline 'a-pipeline' not found"))
						pipelines, err := orderingTeam.Pipelines()
						Expect(err).NotTo(HaveOccurred())
						actualNames := make([]string, len(pipelines))
						for i, pipeline := range pipelines {
							actualNames[i] = pipeline.Name()
						}
						Expect(actualNames).To(Equal(pipelineNames[1:]))
					})
				})
			})

			Context("when requester does not belong to the team", func() {
				It("returns 403", func() {
					Expect(response.StatusCode).To(Equal(http.StatusForbidden))
				})
			})
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				useProfile(anonymousProfile)
			})

			It("returns 401 Unauthorized", func() {
				Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
			})
		})
	})

	Describe("PUT /api/v1/teams/:team_name/pipelines/:pipeline_name/ordering", func() {
		var (
			response            *http.Response
			instanceVars        []atc.InstanceVars
			withinDB            *realDB
			withinTeam          db.Team
			initialInstanceVars []atc.InstanceVars
			requestTeam         = "a-team"
		)

		BeforeEach(func() {
			requestTeam = "a-team"
			instanceVars = []atc.InstanceVars{
				{"branch": "test"},
				{},
				{"branch": "test-2"},
			}
		})

		JustBeforeEach(func() {
			requestPayload, err := json.Marshal(instanceVars)
			Expect(err).NotTo(HaveOccurred())

			request, err := http.NewRequest("PUT", server.URL+"/api/v1/teams/"+requestTeam+"/pipelines/a-pipeline/ordering", bytes.NewBuffer(requestPayload))
			Expect(err).NotTo(HaveOccurred())

			response, err = client.Do(request)
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when authenticated", func() {
			BeforeEach(func() {
				useProfile(memberProfile)
			})

			Context("when requester belongs to the team", func() {
				Context("when ordering the pipelines succeeds", func() {
					BeforeEach(func() {
						withinDB = useRealDB()
						var err error
						withinTeam, err = withinDB.Deps.teamFactory.CreateTeam(atc.Team{Name: "a-team"})
						Expect(err).NotTo(HaveOccurred())
						grantProfile(withinTeam, memberProfile, accessor.MemberRole)
						for _, vars := range []atc.InstanceVars{{"branch": "test-2"}, nil, {"branch": "test"}} {
							_, _, err := withinTeam.SavePipeline(atc.PipelineRef{Name: "a-pipeline", InstanceVars: vars}, atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}}, db.ConfigVersion(0), false)
							Expect(err).NotTo(HaveOccurred())
						}
						initialPipelines, err := withinTeam.Pipelines()
						Expect(err).NotTo(HaveOccurred())
						initialInstanceVars = normalizedInstanceVars(initialPipelines, "a-pipeline")
						Expect(initialInstanceVars).To(Equal([]atc.InstanceVars{
							{"branch": "test-2"},
							{},
							{"branch": "test"},
						}))
						server = withinDB.Serve()
					})

					It("persists the requested instance order from a deliberately different initial order", func() {
						pipelines, err := withinTeam.Pipelines()
						Expect(err).NotTo(HaveOccurred())
						actualInstanceVars := normalizedInstanceVars(pipelines, "a-pipeline")
						Expect(actualInstanceVars).To(Equal(instanceVars))
						Expect(actualInstanceVars).NotTo(Equal(initialInstanceVars))
					})

					It("returns 200 and rejects malformed instance ordering JSON", func() {
						Expect(response.StatusCode).To(Equal(http.StatusOK))

						request, err := http.NewRequest(
							http.MethodPut,
							server.URL+"/api/v1/teams/a-team/pipelines/a-pipeline/ordering",
							bytes.NewBufferString("{"),
						)
						Expect(err).NotTo(HaveOccurred())
						malformed, err := client.Do(request)
						Expect(err).NotTo(HaveOccurred())
						DeferCleanup(malformed.Body.Close)
						Expect(malformed.StatusCode).To(Equal(http.StatusBadRequest))
					})
				})

				Context("when a pipeline does not exist", func() {
					BeforeEach(func() {
						withinDB = useRealDB()
						var err error
						withinTeam, err = withinDB.Deps.teamFactory.CreateTeam(atc.Team{Name: "a-team"})
						Expect(err).NotTo(HaveOccurred())
						grantProfile(withinTeam, memberProfile, accessor.MemberRole)
						for _, vars := range []atc.InstanceVars{{"branch": "test-2"}, nil} {
							_, _, err := withinTeam.SavePipeline(
								atc.PipelineRef{Name: "a-pipeline", InstanceVars: vars},
								atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}},
								db.ConfigVersion(0),
								false,
							)
							Expect(err).NotTo(HaveOccurred())
						}
						server = withinDB.Serve()
					})

					It("returns 400", func() {
						Expect(response.StatusCode).To(Equal(http.StatusBadRequest))
						Expect(io.ReadAll(response.Body)).To(ContainSubstring("pipeline 'a-pipeline/branch:test' not found"))
						pipelines, err := withinTeam.Pipelines()
						Expect(err).NotTo(HaveOccurred())
						Expect(normalizedInstanceVars(pipelines, "a-pipeline")).To(Equal([]atc.InstanceVars{
							{"branch": "test-2"},
							{},
						}))
					})
				})
			})

			Context("when requester does not belong to the team", func() {
				It("returns 403", func() {
					Expect(response.StatusCode).To(Equal(http.StatusForbidden))
				})
			})
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				useProfile(anonymousProfile)
			})

			It("returns 401 Unauthorized", func() {
				Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
			})
		})
	})

	Describe("GET /api/v1/teams/:team_name/pipelines/:pipeline_name/versions-db", func() {
		var response *http.Response

		JustBeforeEach(func() {
			var err error

			request, err := http.NewRequest("GET", server.URL+"/api/v1/teams/a-team/pipelines/a-pipeline/versions-db", nil)
			Expect(err).NotTo(HaveOccurred())

			response, err = client.Do(request)
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when authenticated", func() {
			BeforeEach(func() {
				useProfile(memberProfile)
			})

			Context("when getting the debug versions db works", func() {
				var fixture pipelineDebugVersionsFixture

				BeforeEach(func() {
					fixture = persistPipelineDebugVersionsFixture()
					grantProfile(fixture.Team, memberProfile, accessor.ViewerRole)
					server = fixture.Database.Serve()
				})

				It("returns 200", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
				})

				It("returns application/json", func() {
					expectedHeaderEntries := map[string]string{
						"Content-Type": "application/json",
					}
					Expect(response).Should(IncludeHeaderEntries(expectedHeaderEntries))
				})

				It("returns a json representation of all the versions in the pipeline", func() {
					body, err := io.ReadAll(response.Body)
					Expect(err).NotTo(HaveOccurred())

					var actual atc.DebugVersionsDB
					Expect(json.Unmarshal(body, &actual)).To(Succeed())
					expectPipelineDebugCardinalities(actual, fixture.Expected)
					expectPipelineDebugExcludesDecoy(actual, fixture.Decoy)

					normalizedActual := normalizePipelineDebugVersionsDB(actual)

					wrongVersionID := copyPipelineDebugVersionsDB(fixture.Expected)
					wrongVersionID.ResourceVersions[0].VersionID = fixture.Decoy.versionIDs[0]
					Expect(normalizedActual).NotTo(Equal(normalizePipelineDebugVersionsDB(wrongVersionID)))

					missingRerun := copyPipelineDebugVersionsDB(fixture.Expected)
					missingRerun.BuildReruns = nil
					Expect(normalizedActual).NotTo(Equal(normalizePipelineDebugVersionsDB(missingRerun)))

					Expect(normalizedActual).To(Equal(normalizePipelineDebugVersionsDB(fixture.Expected)))
				})
			})

		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				useProfile(anonymousProfile)
			})

			It("returns 401 Unauthorized", func() {
				Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
			})
		})
	})

	Describe("PUT /api/v1/teams/:team_name/pipelines/:pipeline_name/rename", func() {
		var (
			response    *http.Response
			requestBody string
			renameDB    *realDB
			renameTeam  db.Team
			requestTeam = "a-team"
		)

		BeforeEach(func() {
			requestTeam = "a-team"
			requestBody = `{"name":"some-new-name"}`
		})

		JustBeforeEach(func() {
			var err error

			request, err := http.NewRequest("PUT", server.URL+"/api/v1/teams/"+requestTeam+"/pipelines/a-pipeline/rename", bytes.NewBufferString(requestBody))
			Expect(err).NotTo(HaveOccurred())

			response, err = client.Do(request)
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when authenticated", func() {
			BeforeEach(func() {
				useProfile(memberProfile)
			})
			Context("when authorized", func() {
				Context("when renaming succeeds", func() {
					BeforeEach(func() {
						renameDB = useRealDB()
						var err error
						renameTeam, err = renameDB.Deps.teamFactory.CreateTeam(atc.Team{Name: "a-team"})
						Expect(err).NotTo(HaveOccurred())
						grantProfile(renameTeam, memberProfile, accessor.MemberRole)
						renameDB.SavePipeline(renameTeam, "a-pipeline", atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}})
						server = renameDB.Serve()
					})

					It("returns 200 and renames the pipeline in PostgreSQL", func() {
						Expect(response.StatusCode).To(Equal(http.StatusOK))
						_, found, err := renameTeam.Pipeline(atc.PipelineRef{Name: "a-pipeline"})
						Expect(err).NotTo(HaveOccurred())
						Expect(found).To(BeFalse())
						renamed, found, err := renameTeam.Pipeline(atc.PipelineRef{Name: "some-new-name"})
						Expect(err).NotTo(HaveOccurred())
						Expect(found).To(BeTrue())
						Expect(renamed.Name()).To(Equal("some-new-name"))
					})
				})

				Context("when the pipeline does not exist", func() {
					BeforeEach(func() {
						renameDB = useRealDB()
						var err error
						renameTeam, err = renameDB.Deps.teamFactory.CreateTeam(atc.Team{Name: "a-team"})
						Expect(err).NotTo(HaveOccurred())
						grantProfile(renameTeam, memberProfile, accessor.MemberRole)
						renameDB.SavePipeline(renameTeam, "decoy-pipeline", atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}})
						server = renameDB.Serve()
					})

					It("returns a 404 without renaming another pipeline", func() {
						Expect(response.StatusCode).To(Equal(http.StatusNotFound))
						decoy, found, err := renameTeam.Pipeline(atc.PipelineRef{Name: "decoy-pipeline"})
						Expect(err).NotTo(HaveOccurred())
						Expect(found).To(BeTrue())
						Expect(decoy.Name()).To(Equal("decoy-pipeline"))
					})
				})

				Context("when the new name is an invalid identifier", func() {
					BeforeEach(func() {
						renameDB = useRealDB()
						var err error
						renameTeam, err = renameDB.Deps.teamFactory.CreateTeam(atc.Team{Name: "a-team"})
						Expect(err).NotTo(HaveOccurred())
						grantProfile(renameTeam, memberProfile, accessor.MemberRole)
						renameDB.SavePipeline(renameTeam, "a-pipeline", atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}})
						server = renameDB.Serve()
					})

					Context("and is a string", func() {
						BeforeEach(func() {
							requestBody = `{"name":"_some-new-name"}`
						})

						It("returns a warning in the response body", func() {
							Expect(io.ReadAll(response.Body)).To(MatchJSON(`
							{
								"warnings": [
									{
										"type": "invalid_identifier",
										"message": "pipeline: '_some-new-name' is not a valid identifier: must start with a lowercase letter or a number"
									}
								]
							}`))
							renamed, found, err := renameTeam.Pipeline(atc.PipelineRef{Name: "_some-new-name"})
							Expect(err).NotTo(HaveOccurred())
							Expect(found).To(BeTrue())
							Expect(renamed.Name()).To(Equal("_some-new-name"))
						})
					})
					Context("and is an empty string", func() {
						BeforeEach(func() {
							requestBody = `{"name":""}`
						})

						It("returns 400 Bad Request and an error in the response body", func() {
							Expect(response.StatusCode).To(Equal(http.StatusBadRequest))
							Expect(io.ReadAll(response.Body)).To(MatchJSON(`
							{
								"errors": [
										"pipeline: identifier cannot be an empty string"
								]
							}`))
						})
					})
				})
			})

			Context("when requester does not belong to the team", func() {
				It("returns 403 Forbidden", func() {
					Expect(response.StatusCode).To(Equal(http.StatusForbidden))
				})
			})
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				useProfile(anonymousProfile)
			})

			It("returns 401 Unauthorized", func() {
				Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
			})
		})
	})

	Describe("GET /api/v1/teams/:team_name/pipelines/:pipeline_name/builds", func() {
		var (
			response        *http.Response
			queryParams     string
			requestTeam     = "some-team"
			requestPipe     = "some-pipeline"
			listDB          *realDB
			listPipeline    db.Pipeline
			listBuild1      db.Build
			listBuild2      db.Build
			listBuild2Start time.Time
			grantListAccess bool
		)

		persistPipelineWithBuilds := func(pipelineRef atc.PipelineRef, count int) (db.Pipeline, []db.Build) {
			GinkgoHelper()

			listDB = useRealDB()
			pipeline, _, err := listDB.Main.SavePipeline(
				pipelineRef,
				atc.Config{Jobs: atc.JobConfigs{{Name: "some-job"}}},
				db.ConfigVersion(0),
				false,
			)
			Expect(err).NotTo(HaveOccurred())
			if grantListAccess {
				grantProfile(listDB.Main, memberProfile, accessor.ViewerRole)
			}
			job, found, err := pipeline.Job("some-job")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			builds := make([]db.Build, 0, count)
			for range count {
				build, err := job.CreateBuild("api-test")
				Expect(err).NotTo(HaveOccurred())
				builds = append(builds, build)
			}
			server = listDB.Serve()
			requestTeam = "main"
			requestPipe = pipelineRef.Name
			return pipeline, builds
		}

		decodeBuilds := func() []atc.Build {
			GinkgoHelper()
			body, err := io.ReadAll(response.Body)
			Expect(err).NotTo(HaveOccurred())
			var builds []atc.Build
			Expect(json.Unmarshal(body, &builds)).To(Succeed())
			return builds
		}

		BeforeEach(func() {
			requestTeam = "some-team"
			requestPipe = "some-pipeline"
			queryParams = ""
			grantListAccess = false
		})

		JustBeforeEach(func() {
			var err error

			response, err = client.Get(server.URL + "/api/v1/teams/" + requestTeam + "/pipelines/" + requestPipe + "/builds" + queryParams)
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				useProfile(anonymousProfile)
			})

			Context("and the pipeline is private", func() {
				BeforeEach(func() {
					persistPipelineWithBuilds(atc.PipelineRef{Name: "some-pipeline"}, 0)
				})

				It("returns 401", func() {
					Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
				})
			})

			Context("and the pipeline is public", func() {
				BeforeEach(func() {
					pipeline, _ := persistPipelineWithBuilds(atc.PipelineRef{Name: "some-pipeline"}, 0)
					Expect(pipeline.Expose()).To(Succeed())
				})

				It("returns 200 OK", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
				})
			})
		})

		Context("when authorized", func() {
			BeforeEach(func() {
				grantListAccess = true
				useProfile(memberProfile)
			})

			Context("when no params are passed", func() {
				var persistedBuilds []db.Build

				BeforeEach(func() {
					_, persistedBuilds = persistPipelineWithBuilds(atc.PipelineRef{Name: "some-pipeline"}, 101)
				})

				It("applies the default limit without implicit range boundaries", func() {
					actual := decodeBuilds()
					Expect(actual).To(HaveLen(atc.PaginationAPIDefaultLimit))
					expectedIDs := make([]int, 0, atc.PaginationAPIDefaultLimit)
					for i := len(persistedBuilds) - 1; i >= 1; i-- {
						expectedIDs = append(expectedIDs, persistedBuilds[i].ID())
					}
					actualIDs := make([]int, len(actual))
					for i, build := range actual {
						actualIDs[i] = build.ID
					}
					Expect(actualIDs).To(Equal(expectedIDs))
					Expect(actualIDs).NotTo(ContainElement(persistedBuilds[0].ID()))
				})
			})

			Context("when all the params are passed", func() {
				var persistedBuilds []db.Build

				BeforeEach(func() {
					_, persistedBuilds = persistPipelineWithBuilds(atc.PipelineRef{Name: "some-pipeline"}, 7)
					queryParams = fmt.Sprintf(
						"?from=%d&to=%d&limit=3",
						persistedBuilds[1].ID(),
						persistedBuilds[5].ID(),
					)
				})

				It("applies each of from, to, and limit to persisted builds", func() {
					By("using limit to truncate a wider inclusive range")
					actual := decodeBuilds()
					Expect(actual).To(HaveLen(3))
					Expect([]int{actual[0].ID, actual[1].ID, actual[2].ID}).To(Equal([]int{
						persistedBuilds[1].ID(),
						persistedBuilds[2].ID(),
						persistedBuilds[3].ID(),
					}))

					By("using from and to to bound a range narrower than limit")
					boundedResponse, err := client.Get(fmt.Sprintf(
						"%s/api/v1/teams/main/pipelines/some-pipeline/builds?from=%d&to=%d&limit=6",
						server.URL,
						persistedBuilds[1].ID(),
						persistedBuilds[3].ID(),
					))
					Expect(err).NotTo(HaveOccurred())
					Expect(boundedResponse.StatusCode).To(Equal(http.StatusOK))
					DeferCleanup(boundedResponse.Body.Close)
					body, err := io.ReadAll(boundedResponse.Body)
					Expect(err).NotTo(HaveOccurred())
					var bounded []atc.Build
					Expect(json.Unmarshal(body, &bounded)).To(Succeed())
					Expect(bounded).To(HaveLen(3))
					Expect([]int{bounded[0].ID, bounded[1].ID, bounded[2].ID}).To(Equal([]int{
						persistedBuilds[1].ID(),
						persistedBuilds[2].ID(),
						persistedBuilds[3].ID(),
					}))
				})
			})

			Context("when getting the builds succeeds", func() {
				BeforeEach(func() {
					var builds []db.Build
					listPipeline, builds = persistPipelineWithBuilds(atc.PipelineRef{Name: "some-pipeline"}, 2)
					listBuild1 = builds[0]
					listBuild2 = builds[1]
					started, err := listBuild1.Start(atc.Plan{})
					Expect(err).NotTo(HaveOccurred())
					Expect(started).To(BeTrue())
					Expect(listBuild1.Finish(db.BuildStatusSucceeded)).To(Succeed())
					started, err = listBuild2.Start(atc.Plan{})
					Expect(err).NotTo(HaveOccurred())
					Expect(started).To(BeTrue())
					listBuild2Start = time.Date(2020, time.November, 2, 0, 0, 0, 0, time.UTC)
					for index, build := range []db.Build{listBuild1, listBuild2} {
						result, err := listDB.Conn.Exec(
							"UPDATE builds SET start_time = $1 WHERE id = $2",
							listBuild2Start.Add(time.Duration(index-1)*time.Hour),
							build.ID(),
						)
						Expect(err).NotTo(HaveOccurred())
						rows, err := result.RowsAffected()
						Expect(err).NotTo(HaveOccurred())
						Expect(rows).To(Equal(int64(1)))
					}
					queryParams = "?limit=2"
				})

				It("returns 200 OK", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
				})

				It("returns Content-Type 'application/json'", func() {
					expectedHeaderEntries := map[string]string{
						"Content-Type": "application/json",
					}
					Expect(response).Should(IncludeHeaderEntries(expectedHeaderEntries))
				})

				It("returns the builds", func() {
					body, err := io.ReadAll(response.Body)
					Expect(err).NotTo(HaveOccurred())
					var builds []atc.Build
					Expect(json.Unmarshal(body, &builds)).To(Succeed())
					Expect(builds).To(HaveLen(2))
					byID := map[int]atc.Build{}
					for _, build := range builds {
						byID[build.ID] = build
					}
					for _, build := range []db.Build{listBuild1, listBuild2} {
						persisted, found, err := listDB.Deps.buildFactory.Build(build.ID())
						Expect(err).NotTo(HaveOccurred())
						Expect(found).To(BeTrue())
						actual, ok := byID[build.ID()]
						Expect(ok).To(BeTrue())
						Expect(actual.Name).To(Equal(persisted.Name()))
						Expect(actual.Status).To(Equal(atc.BuildStatus(persisted.Status())))
						Expect(actual.TeamName).To(Equal(persisted.TeamName()))
						Expect(actual.PipelineName).To(Equal("some-pipeline"))
						Expect(actual.JobName).To(Equal("some-job"))
						Expect(actual.StartTime).To(Equal(persisted.StartTime().Unix()))
						if persisted.EndTime().IsZero() {
							Expect(actual.EndTime).To(BeZero())
						} else {
							Expect(actual.EndTime).To(Equal(persisted.EndTime().Unix()))
						}
					}

					timestamped, err := client.Get(fmt.Sprintf(
						"%s/api/v1/teams/main/pipelines/some-pipeline/builds?timestamps=true&from=%d&to=%d&limit=2",
						server.URL,
						listBuild2Start.Unix(),
						listBuild2Start.Unix(),
					))
					Expect(err).NotTo(HaveOccurred())
					DeferCleanup(timestamped.Body.Close)
					Expect(timestamped.StatusCode).To(Equal(http.StatusOK))
					var timestampedBuilds []atc.Build
					Expect(json.NewDecoder(timestamped.Body).Decode(&timestampedBuilds)).To(Succeed())
					Expect(timestampedBuilds).To(HaveLen(1))
					Expect(timestampedBuilds[0].ID).To(Equal(listBuild2.ID()))
					Expect(timestamped.Header.Values("Link")).To(BeEmpty())
				})

				Context("when next/previous pages are available", func() {
					var (
						olderBuild  db.Build
						middleBuild db.Build
						newerBuild  db.Build
					)

					BeforeEach(func() {
						olderBuild = listBuild1
						middleBuild = listBuild2
						job, found, err := listPipeline.Job("some-job")
						Expect(err).NotTo(HaveOccurred())
						Expect(found).To(BeTrue())
						newerBuild, err = job.CreateBuild("api-test")
						Expect(err).NotTo(HaveOccurred())
						queryParams = fmt.Sprintf("?from=%d&to=%d&limit=1", middleBuild.ID(), middleBuild.ID())
					})

					It("returns Link headers per rfc5988", func() {
						Expect(response.Header["Link"]).To(ConsistOf([]string{
							fmt.Sprintf(`<%s/api/v1/teams/main/pipelines/some-pipeline/builds?to=%d&limit=1>; rel="next"`, externalURL, olderBuild.ID()),
							fmt.Sprintf(`<%s/api/v1/teams/main/pipelines/some-pipeline/builds?from=%d&limit=1>; rel="previous"`, externalURL, newerBuild.ID()),
						}))
					})

					Context("and pipeline is instanced", func() {
						BeforeEach(func() {
							instancedPipeline, _, err := listDB.Main.SavePipeline(
								atc.PipelineRef{Name: "some-pipeline", InstanceVars: atc.InstanceVars{"branch": "master"}},
								atc.Config{Jobs: atc.JobConfigs{{Name: "some-job"}}},
								db.ConfigVersion(0),
								false,
							)
							Expect(err).NotTo(HaveOccurred())
							job, found, err := instancedPipeline.Job("some-job")
							Expect(err).NotTo(HaveOccurred())
							Expect(found).To(BeTrue())
							olderBuild, err = job.CreateBuild("api-test")
							Expect(err).NotTo(HaveOccurred())
							middleBuild, err = job.CreateBuild("api-test")
							Expect(err).NotTo(HaveOccurred())
							newerBuild, err = job.CreateBuild("api-test")
							Expect(err).NotTo(HaveOccurred())
							queryParams = fmt.Sprintf(
								"?from=%d&to=%d&limit=1&vars.branch=%%22master%%22",
								middleBuild.ID(),
								middleBuild.ID(),
							)
						})

						It("returns Link headers per rfc5988", func() {
							link := fmt.Sprintf(`<%s/api/v1/teams/main/pipelines/some-pipeline/builds?`, externalURL)
							Expect(response.Header["Link"]).To(ConsistOf([]string{
								fmt.Sprintf(`%sto=%d&limit=1&vars.branch=%%22master%%22>; rel="next"`, link, olderBuild.ID()),
								fmt.Sprintf(`%sfrom=%d&limit=1&vars.branch=%%22master%%22>; rel="previous"`, link, newerBuild.ID()),
							}))
						})
					})
				})
			})

		})
	})

	Describe("POST /api/v1/teams/:team_name/pipelines/:pipeline_name/builds", func() {
		var (
			plan         atc.Plan
			response     *http.Response
			postDB       *realDB
			postPipeline db.Pipeline
			postTeam     = "a-team"
		)

		BeforeEach(func() {
			postTeam = "a-team"
			plan = atc.Plan{
				ID: atc.PlanID("api-manual"),
				Task: &atc.TaskPlan{
					Config: &atc.TaskConfig{
						Run: atc.TaskRunConfig{
							Path: "ls",
						},
					},
				},
			}
		})

		JustBeforeEach(func() {
			reqPayload, err := json.Marshal(plan)
			Expect(err).NotTo(HaveOccurred())

			req, err := http.NewRequest("POST", server.URL+"/api/v1/teams/"+postTeam+"/pipelines/a-pipeline/builds", bytes.NewBuffer(reqPayload))
			Expect(err).NotTo(HaveOccurred())

			req.Header.Set("Content-Type", "application/json")

			response, err = client.Do(req)
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				useProfile(anonymousProfile)
				postDB = useRealDB()
				postPipeline = postDB.SavePipeline(postDB.Main, "a-pipeline", atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}})
				server = postDB.Serve()
				postTeam = "main"
			})

			It("returns 401 without creating a build", func() {
				Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
				builds, _, err := postPipeline.Builds(db.Page{Limit: 10})
				Expect(err).NotTo(HaveOccurred())
				Expect(builds).To(BeEmpty())
			})
		})

		Context("when authenticated", func() {
			BeforeEach(func() {
				useProfile(memberProfile)
			})

			Context("when not authorized", func() {
				BeforeEach(func() {
					postDB = useRealDB()
					postPipeline = postDB.SavePipeline(postDB.Main, "a-pipeline", atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}})
					server = postDB.Serve()
					postTeam = "main"
				})

				It("returns 403 without creating a build", func() {
					Expect(response.StatusCode).To(Equal(http.StatusForbidden))
					builds, _, err := postPipeline.Builds(db.Page{Limit: 10})
					Expect(err).NotTo(HaveOccurred())
					Expect(builds).To(BeEmpty())
				})
			})

			Context("when authorized", func() {
				Context("when creating a started build succeeds", func() {
					BeforeEach(func() {
						postDB = useRealDB()
						postPipeline = postDB.SavePipeline(postDB.Main, "a-pipeline", atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}})
						grantProfile(postDB.Main, memberProfile, accessor.MemberRole)
						server = postDB.Serve()
						postTeam = "main"
					})

					It("returns 201 Created", func() {
						Expect(response.StatusCode).To(Equal(http.StatusCreated))
					})

					It("returns Content-Type 'application/json'", func() {
						expectedHeaderEntries := map[string]string{
							"Content-Type": "application/json",
						}
						Expect(response).Should(IncludeHeaderEntries(expectedHeaderEntries))
					})

					It("creates a started build and rejects malformed plan JSON", func() {
						builds, _, err := postPipeline.Builds(db.Page{Limit: 1})
						Expect(err).NotTo(HaveOccurred())
						Expect(builds).To(HaveLen(1))
						build, found, err := postDB.Deps.buildFactory.Build(builds[0].ID())
						Expect(err).NotTo(HaveOccurred())
						Expect(found).To(BeTrue())
						found, err = build.Reload()
						Expect(err).NotTo(HaveOccurred())
						Expect(found).To(BeTrue())
						Expect(build.Status()).To(Equal(db.BuildStatusStarted))
						Expect([]byte(*build.PublicPlan())).To(MatchJSON([]byte(*plan.Public())))

						malformedRequest, err := http.NewRequest(
							http.MethodPost,
							server.URL+"/api/v1/teams/main/pipelines/a-pipeline/builds",
							bytes.NewBufferString("{"),
						)
						Expect(err).NotTo(HaveOccurred())
						malformed, err := client.Do(malformedRequest)
						Expect(err).NotTo(HaveOccurred())
						DeferCleanup(malformed.Body.Close)
						Expect(malformed.StatusCode).To(Equal(http.StatusBadRequest))

						builds, _, err = postPipeline.Builds(db.Page{Limit: 2})
						Expect(err).NotTo(HaveOccurred())
						Expect(builds).To(HaveLen(1))
					})

					It("returns the created build", func() {
						body, err := io.ReadAll(response.Body)
						Expect(err).NotTo(HaveOccurred())

						var actual atc.Build
						Expect(json.Unmarshal(body, &actual)).To(Succeed())
						builds, _, err := postPipeline.Builds(db.Page{Limit: 1})
						Expect(err).NotTo(HaveOccurred())
						Expect(builds).To(HaveLen(1))
						build, found, err := postDB.Deps.buildFactory.Build(builds[0].ID())
						Expect(err).NotTo(HaveOccurred())
						Expect(found).To(BeTrue())
						found, err = build.Reload()
						Expect(err).NotTo(HaveOccurred())
						Expect(found).To(BeTrue())
						Expect(actual.ID).To(Equal(build.ID()))
						Expect(actual.Name).To(Equal(build.Name()))
						Expect(actual.TeamName).To(Equal(build.TeamName()))
						Expect(actual.PipelineName).To(Equal("a-pipeline"))
						Expect(actual.Status).To(Equal(atc.BuildStatus(db.BuildStatusStarted)))
						Expect(actual.APIURL).To(Equal(fmt.Sprintf("/api/v1/builds/%d", build.ID())))
						Expect(actual.StartTime).To(Equal(build.StartTime().Unix()))
						Expect([]byte(*build.PublicPlan())).To(MatchJSON([]byte(*plan.Public())))
					})
				})
			})
		})
	})
})
