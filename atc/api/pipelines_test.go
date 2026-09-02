package api_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type pipelineAPIFaults struct {
	pipelinesErr                 error
	orderPipelinesErr            error
	orderPipelinesWithinGroupErr error
	renamePipelineOverride       bool
	renamePipelineFound          bool
	renamePipelineErr            error
	destroyErr                   error
	pauseErr                     error
	archiveErr                   error
	unpauseErr                   error
	exposeErr                    error
	hideErr                      error
	loadDebugVersionsDBErr       error
	buildsErr                    error
	createStartedBuildErr        error
}

type pipelineAPIWithinGroupCall struct {
	groupName    string
	instanceVars []atc.InstanceVars
}

type pipelineAPIDecoratorState struct {
	mu sync.Mutex

	faults pipelineAPIFaults

	findTeamArgs         []string
	orderPipelinesArgs   [][]string
	orderWithinGroupArgs []pipelineAPIWithinGroupCall
	buildPages           []db.Page
}

func newPipelineAPIDecoratorState(faults pipelineAPIFaults) *pipelineAPIDecoratorState {
	return &pipelineAPIDecoratorState{faults: faults}
}

func (state *pipelineAPIDecoratorState) snapshotFaults() pipelineAPIFaults {
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.faults
}

func (state *pipelineAPIDecoratorState) recordFindTeam(name string) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.findTeamArgs = append(state.findTeamArgs, name)
}

func (state *pipelineAPIDecoratorState) findTeamArguments() []string {
	state.mu.Lock()
	defer state.mu.Unlock()
	return append([]string(nil), state.findTeamArgs...)
}

func (state *pipelineAPIDecoratorState) recordPipelineOrder(names []string) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.orderPipelinesArgs = append(state.orderPipelinesArgs, append([]string(nil), names...))
}

func clonePipelineAPIInstanceVars(instanceVars []atc.InstanceVars) []atc.InstanceVars {
	cloned := make([]atc.InstanceVars, len(instanceVars))
	for i, vars := range instanceVars {
		if vars == nil {
			continue
		}
		cloned[i] = make(atc.InstanceVars, len(vars))
		for key, value := range vars {
			cloned[i][key] = clonePipelineAPIInstanceVarValue(value)
		}
	}
	return cloned
}

func clonePipelineAPIInstanceVarValue(value any) any {
	switch typed := value.(type) {
	case atc.InstanceVars:
		cloned := make(atc.InstanceVars, len(typed))
		for key, child := range typed {
			cloned[key] = clonePipelineAPIInstanceVarValue(child)
		}
		return cloned
	case map[string]any:
		cloned := make(map[string]any, len(typed))
		for key, child := range typed {
			cloned[key] = clonePipelineAPIInstanceVarValue(child)
		}
		return cloned
	case []any:
		cloned := make([]any, len(typed))
		for i, child := range typed {
			cloned[i] = clonePipelineAPIInstanceVarValue(child)
		}
		return cloned
	default:
		return typed
	}
}

func (state *pipelineAPIDecoratorState) recordWithinGroupOrder(groupName string, instanceVars []atc.InstanceVars) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.orderWithinGroupArgs = append(state.orderWithinGroupArgs, pipelineAPIWithinGroupCall{
		groupName:    groupName,
		instanceVars: clonePipelineAPIInstanceVars(instanceVars),
	})
}

func clonePipelineAPIPage(page db.Page) db.Page {
	cloned := page
	if page.From != nil {
		from := *page.From
		cloned.From = &from
	}
	if page.To != nil {
		to := *page.To
		cloned.To = &to
	}
	return cloned
}

func (state *pipelineAPIDecoratorState) recordBuildPage(page db.Page) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.buildPages = append(state.buildPages, clonePipelineAPIPage(page))
}

type pipelineAPITeamFactory struct {
	db.TeamFactory

	state            *pipelineAPIDecoratorState
	targetTeamID     int
	targetPipelineID int
}

func (factory *pipelineAPITeamFactory) FindTeam(name string) (db.Team, bool, error) {
	team, found, err := factory.TeamFactory.FindTeam(name)
	factory.state.recordFindTeam(name)
	if err != nil || !found || team.ID() != factory.targetTeamID {
		return team, found, err
	}
	return &pipelineAPITeam{
		Team:             team,
		state:            factory.state,
		targetPipelineID: factory.targetPipelineID,
	}, true, nil
}

type pipelineAPITeam struct {
	db.Team

	state            *pipelineAPIDecoratorState
	targetPipelineID int
}

func (team *pipelineAPITeam) Pipelines() ([]db.Pipeline, error) {
	if err := team.state.snapshotFaults().pipelinesErr; err != nil {
		return nil, err
	}
	pipelines, err := team.Team.Pipelines()
	if err != nil {
		return nil, err
	}
	for i, pipeline := range pipelines {
		pipelines[i] = team.decoratePipeline(pipeline)
	}
	return pipelines, nil
}

func (team *pipelineAPITeam) Pipeline(ref atc.PipelineRef) (db.Pipeline, bool, error) {
	pipeline, found, err := team.Team.Pipeline(ref)
	if err != nil || !found {
		return pipeline, found, err
	}
	return team.decoratePipeline(pipeline), true, nil
}

func (team *pipelineAPITeam) decoratePipeline(pipeline db.Pipeline) db.Pipeline {
	if pipeline == nil || team.targetPipelineID == 0 || pipeline.ID() != team.targetPipelineID {
		return pipeline
	}
	return &pipelineAPIPipeline{Pipeline: pipeline, state: team.state}
}

func (team *pipelineAPITeam) OrderPipelines(names []string) error {
	team.state.recordPipelineOrder(names)
	if err := team.state.snapshotFaults().orderPipelinesErr; err != nil {
		return err
	}
	return team.Team.OrderPipelines(names)
}

func (team *pipelineAPITeam) OrderPipelinesWithinGroup(groupName string, instanceVars []atc.InstanceVars) error {
	team.state.recordWithinGroupOrder(groupName, instanceVars)
	if err := team.state.snapshotFaults().orderPipelinesWithinGroupErr; err != nil {
		return err
	}
	return team.Team.OrderPipelinesWithinGroup(groupName, instanceVars)
}

func (team *pipelineAPITeam) RenamePipeline(oldName string, newName string) (bool, error) {
	faults := team.state.snapshotFaults()
	if faults.renamePipelineOverride {
		return faults.renamePipelineFound, faults.renamePipelineErr
	}
	return team.Team.RenamePipeline(oldName, newName)
}

type pipelineAPIPipeline struct {
	db.Pipeline
	state *pipelineAPIDecoratorState
}

func (pipeline *pipelineAPIPipeline) Destroy() error {
	if err := pipeline.state.snapshotFaults().destroyErr; err != nil {
		return err
	}
	return pipeline.Pipeline.Destroy()
}

func (pipeline *pipelineAPIPipeline) Pause(pausedBy string) error {
	if err := pipeline.state.snapshotFaults().pauseErr; err != nil {
		return err
	}
	return pipeline.Pipeline.Pause(pausedBy)
}

func (pipeline *pipelineAPIPipeline) Archive() error {
	if err := pipeline.state.snapshotFaults().archiveErr; err != nil {
		return err
	}
	return pipeline.Pipeline.Archive()
}

func (pipeline *pipelineAPIPipeline) Unpause() error {
	if err := pipeline.state.snapshotFaults().unpauseErr; err != nil {
		return err
	}
	return pipeline.Pipeline.Unpause()
}

func (pipeline *pipelineAPIPipeline) Expose() error {
	if err := pipeline.state.snapshotFaults().exposeErr; err != nil {
		return err
	}
	return pipeline.Pipeline.Expose()
}

func (pipeline *pipelineAPIPipeline) Hide() error {
	if err := pipeline.state.snapshotFaults().hideErr; err != nil {
		return err
	}
	return pipeline.Pipeline.Hide()
}

func (pipeline *pipelineAPIPipeline) LoadDebugVersionsDB() (*atc.DebugVersionsDB, error) {
	if err := pipeline.state.snapshotFaults().loadDebugVersionsDBErr; err != nil {
		return nil, err
	}
	return pipeline.Pipeline.LoadDebugVersionsDB()
}

func (pipeline *pipelineAPIPipeline) Builds(page db.Page) ([]db.BuildForAPI, db.Pagination, error) {
	pipeline.state.recordBuildPage(page)
	if err := pipeline.state.snapshotFaults().buildsErr; err != nil {
		return nil, db.Pagination{}, err
	}
	return pipeline.Pipeline.Builds(page)
}

func (pipeline *pipelineAPIPipeline) CreateStartedBuild(plan atc.Plan) (db.Build, error) {
	if err := pipeline.state.snapshotFaults().createStartedBuildErr; err != nil {
		return nil, err
	}
	return pipeline.Pipeline.CreateStartedBuild(plan)
}

type pipelineAPIFaultFixture struct {
	Database *realDB
	State    *pipelineAPIDecoratorState
	Team     db.Team
	Pipeline db.Pipeline
}

func persistPipelineAPIFaultFixture(teamName string, pipelineName string, faults pipelineAPIFaults) pipelineAPIFaultFixture {
	GinkgoHelper()

	database := useRealDB()
	return decoratePipelineAPIFaultFixture(database, teamName, pipelineName, faults)
}

func decoratePipelineAPIFaultFixture(database *realDB, teamName string, pipelineName string, faults pipelineAPIFaults) pipelineAPIFaultFixture {
	GinkgoHelper()

	team := database.Main
	if teamName != database.Main.Name() {
		var err error
		team, err = database.Deps.teamFactory.CreateTeam(atc.Team{Name: teamName})
		Expect(err).NotTo(HaveOccurred())
	}

	var pipeline db.Pipeline
	if pipelineName != "" {
		pipeline = database.SavePipeline(team, pipelineName, atc.Config{
			Jobs: atc.JobConfigs{{Name: "fixture-job"}},
		})
	}

	state := newPipelineAPIDecoratorState(faults)
	targetPipelineID := 0
	if pipeline != nil {
		targetPipelineID = pipeline.ID()
	}
	database.Deps.teamFactory = &pipelineAPITeamFactory{
		TeamFactory:      database.Deps.teamFactory,
		state:            state,
		targetTeamID:     team.ID(),
		targetPipelineID: targetPipelineID,
	}

	return pipelineAPIFaultFixture{
		Database: database,
		State:    state,
		Team:     team,
		Pipeline: pipeline,
	}
}

type pipelineDebugDecoyIDs struct {
	resourceIDs []int
	versionIDs  []int
	buildIDs    []int
	jobIDs      []int
	scopeIDs    []int
}

type pipelineDebugVersionsFixture struct {
	Database *realDB
	State    *pipelineAPIDecoratorState
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

	state := newPipelineAPIDecoratorState(pipelineAPIFaults{})
	database.Deps.teamFactory = &pipelineAPITeamFactory{
		TeamFactory:      database.Deps.teamFactory,
		state:            state,
		targetTeamID:     targetTeam.ID(),
		targetPipelineID: targetPipeline.ID(),
	}

	return pipelineDebugVersionsFixture{
		Database: database,
		State:    state,
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

		Context("when team is set in user context", func() {
			BeforeEach(func() {
				fakeAccess.TeamNamesReturns([]string{"some-team"})
			})

			It("does not grant visibility to an unrelated team", func() {
				expectPipelineResponse(response, pipelines["public-main"], pipelines["public-other"])
			})
		})

		Context("when not authenticated", func() {
			It("returns only public pipelines", func() {
				expectPipelineResponse(response, pipelines["public-main"], pipelines["public-other"])
			})
		})

		Context("when authenticated", func() {
			BeforeEach(func() {
				fakeAccess.TeamNamesReturns([]string{"main"})
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
					fakeAccess.IsAdminReturns(true)
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

			Context("when the call to get active pipelines fails", func() {
				BeforeEach(func() {
					doomed := postgresRunner.OpenConn()
					Expect(doomed.Close()).To(Succeed())
					deps := listingDB.Deps
					deps.pipelineFactory = db.NewPipelineFactory(doomed, listingDB.LockFactory)
					server = newAPIServer(deps)
					DeferCleanup(server.Close)
				})

				It("returns 500 internal server error", func() {
					Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
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
				fakeAccess.IsAuthorizedReturns(true)
			})

			It("returns 200 OK", func() {
				Expect(response.StatusCode).To(Equal(http.StatusOK))
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

			Context("when the call to get active pipelines fails", func() {
				BeforeEach(func() {
					fixture := decoratePipelineAPIFaultFixture(listingDB, "main", "", pipelineAPIFaults{
						pipelinesErr: errors.New("disaster"),
					})
					server = fixture.Database.Serve()
				})

				It("returns 500 internal server error", func() {
					Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
				})
			})
		})

		Context("when authenticated as another team", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(true)
				fakeAccess.IsAuthorizedReturns(false)
			})

			It("returns only team's public pipelines", func() {
				expectPipelineResponse(response, pipelines["public-main"])
			})
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(false)
			})

			It("returns only team's public pipelines", func() {
				expectPipelineResponse(response, pipelines["public-main"])
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
				fakeAccess.IsAuthenticatedReturns(false)
				Expect(detailPipeline.Hide()).To(Succeed())
			})

		})

		Context("when authenticated as requested team", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(true)
				fakeAccess.IsAuthorizedReturns(true)
			})

			It("returns 200 ok", func() {
				Expect(response.StatusCode).To(Equal(http.StatusOK))
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
				fakeAccess.IsAuthenticatedReturns(true)
				fakeAccess.IsAuthorizedReturns(false)

			})

			Context("and the pipeline is private", func() {
				BeforeEach(func() {
					Expect(detailPipeline.Hide()).To(Succeed())
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
				fakeAccess.IsAuthenticatedReturns(false)
			})

			Context("and the pipeline is private", func() {
				BeforeEach(func() {
					Expect(detailPipeline.Hide()).To(Succeed())
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
				fakeAccess.IsAuthenticatedReturns(true)
			})

			Context("when requester belongs to the team", func() {
				BeforeEach(func() {
					fakeAccess.IsAuthorizedReturns(true)
				})

				Context("when deleting succeeds", func() {
					BeforeEach(func() {
						deleteDB = useRealDB()
						deleteDB.SavePipeline(deleteDB.Main, "a-pipeline-name", atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}})
						server = deleteDB.Serve()
						requestTeam = "main"
					})

				})

				Context("when an error occurs destroying the pipeline", func() {
					BeforeEach(func() {
						fixture := persistPipelineAPIFaultFixture("a-team", "a-pipeline-name", pipelineAPIFaults{
							destroyErr: errors.New("disaster!"),
						})
						server = fixture.Database.Serve()
					})

					It("returns a 500 Internal Server Error", func() {
						Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
					})
				})
			})

			Context("when requester does not belong to the team", func() {
				BeforeEach(func() {
					fakeAccess.IsAuthorizedReturns(false)
				})

			})
		})

		Context("when the user is not logged in", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(false)
			})

		})
	})

	Describe("PUT /api/v1/teams/:team_name/pipelines/:pipeline_name/pause", func() {
		var (
			response    *http.Response
			realdb      *realDB
			requestTeam = "a-team"
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
				fakeAccess.IsAuthenticatedReturns(true)
			})

			Context("when requester belongs to the team", func() {
				BeforeEach(func() {
					fakeAccess.IsAuthorizedReturns(true)
				})

				Context("when pausing the pipeline succeeds", func() {
					BeforeEach(func() {
						realdb = useRealDB()
						realdb.SavePipeline(realdb.Main, "a-pipeline", atc.Config{
							Jobs: atc.JobConfigs{{Name: "job"}},
						})
						server = realdb.Serve()
						requestTeam = "main"
						fakeAccess.UserInfoReturns(atc.UserInfo{DisplayUserId: "api-user"})
					})

				})

				Context("when pausing the pipeline fails", func() {
					BeforeEach(func() {
						fixture := persistPipelineAPIFaultFixture("a-team", "a-pipeline", pipelineAPIFaults{
							pauseErr: errors.New("welp"),
						})
						server = fixture.Database.Serve()
					})

					It("returns 500", func() {
						Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
					})
				})
			})

			Context("when requester does not belong to the team", func() {
				BeforeEach(func() {
					fakeAccess.IsAuthorizedReturns(false)
				})

			})
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(false)
			})

		})
	})

	Describe("PUT /api/v1/teams/:team_name/pipelines/:pipeline_name/archive", func() {
		var (
			response      *http.Response
			archiveDB     *realDB
			archiveConfig atc.Config
			requestTeam   = "a-team"
		)

		BeforeEach(func() {
			requestTeam = "a-team"
			fakeAccess.IsAuthenticatedReturns(true)
			fakeAccess.IsAuthorizedReturns(true)
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
				archiveDB.SavePipeline(archiveDB.Main, "a-pipeline", archiveConfig)
				server = archiveDB.Serve()
				requestTeam = "main"
			})

		})

		Context("when archiving the pipeline fails due to the DB", func() {
			BeforeEach(func() {
				fixture := persistPipelineAPIFaultFixture("a-team", "a-pipeline", pipelineAPIFaults{
					archiveErr: errors.New("pq: a db error"),
				})
				server = fixture.Database.Serve()
			})

			It("gives a server error", func() {
				Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
			})
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(false)
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
				fakeAccess.IsAuthenticatedReturns(true)
			})
			Context("when requester belongs to the team", func() {
				BeforeEach(func() {
					fakeAccess.IsAuthorizedReturns(true)
				})

				Context("when unpausing the pipeline succeeds", func() {
					BeforeEach(func() {
						unpauseDB = useRealDB()
						unpausedPipeline = unpauseDB.SavePipeline(unpauseDB.Main, "a-pipeline", atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}})
						Expect(unpausedPipeline.Pause("fixture")).To(Succeed())
						server = unpauseDB.Serve()
						requestTeam = "main"
					})

				})

				Context("when unpausing the pipeline fails for an unknown reason", func() {
					BeforeEach(func() {
						fixture := persistPipelineAPIFaultFixture("a-team", "a-pipeline", pipelineAPIFaults{
							unpauseErr: errors.New("welp"),
						})
						server = fixture.Database.Serve()
					})

					It("returns 500", func() {
						Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
					})
				})
			})

			Context("when requester does not belong to the team", func() {
				BeforeEach(func() {
					fakeAccess.IsAuthorizedReturns(false)
				})

			})
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(false)
			})

		})
	})

	Describe("PUT /api/v1/teams/:team_name/pipelines/:pipeline_name/expose", func() {
		var (
			response    *http.Response
			exposeDB    *realDB
			requestTeam = "a-team"
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
				fakeAccess.IsAuthenticatedReturns(true)
			})

			Context("when requester belongs to the team", func() {
				BeforeEach(func() {
					fakeAccess.IsAuthorizedReturns(true)
				})

				Context("when exposing the pipeline succeeds", func() {
					BeforeEach(func() {
						exposeDB = useRealDB()
						exposeDB.SavePipeline(exposeDB.Main, "a-pipeline", atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}})
						server = exposeDB.Serve()
						requestTeam = "main"
					})

				})

				Context("when exposing the pipeline fails", func() {
					BeforeEach(func() {
						fixture := persistPipelineAPIFaultFixture("a-team", "a-pipeline", pipelineAPIFaults{
							exposeErr: errors.New("welp"),
						})
						server = fixture.Database.Serve()
					})

					It("returns 500", func() {
						Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
					})
				})
			})

			Context("when requester does not belong to the team", func() {
				BeforeEach(func() {
					fakeAccess.IsAuthorizedReturns(false)
				})

			})
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(false)
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
				fakeAccess.IsAuthenticatedReturns(true)
			})
			Context("when requester belongs to the team", func() {
				BeforeEach(func() {
					fakeAccess.IsAuthorizedReturns(true)
				})

				Context("when hiding the pipeline succeeds", func() {
					BeforeEach(func() {
						hideDB = useRealDB()
						hiddenPipeline = hideDB.SavePipeline(hideDB.Main, "a-pipeline", atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}})
						Expect(hiddenPipeline.Expose()).To(Succeed())
						server = hideDB.Serve()
						requestTeam = "main"
					})

				})

				Context("when hiding the pipeline fails", func() {
					BeforeEach(func() {
						fixture := persistPipelineAPIFaultFixture("a-team", "a-pipeline", pipelineAPIFaults{
							hideErr: errors.New("welp"),
						})
						server = fixture.Database.Serve()
					})

					It("returns 500", func() {
						Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
					})
				})
			})

			Context("when requester does not belong to the team", func() {
				BeforeEach(func() {
					fakeAccess.IsAuthorizedReturns(false)
				})

			})
		})

		Context("when not authorized", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(false)
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
				fakeAccess.IsAuthenticatedReturns(true)
			})

			Context("when requester belongs to the team", func() {
				BeforeEach(func() {
					fakeAccess.IsAuthorizedReturns(true)
				})

				Context("when ordering the pipelines succeeds", func() {
					BeforeEach(func() {
						orderingDB = useRealDB()
						var err error
						orderingTeam, err = orderingDB.Deps.teamFactory.CreateTeam(atc.Team{Name: "a-team"})
						Expect(err).NotTo(HaveOccurred())
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

				})

				Context("when a pipeline does not exist", func() {
					BeforeEach(func() {
						fixture := persistPipelineAPIFaultFixture("a-team", "", pipelineAPIFaults{
							orderPipelinesErr: db.ErrPipelineNotFound{Name: "a-pipeline"},
						})
						for _, name := range pipelineNames {
							fixture.Database.SavePipeline(fixture.Team, name, atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}})
						}
						server = fixture.Database.Serve()
					})

				})

				Context("when ordering the pipelines fails", func() {
					BeforeEach(func() {
						fixture := persistPipelineAPIFaultFixture("a-team", "", pipelineAPIFaults{
							orderPipelinesErr: errors.New("welp"),
						})
						for _, name := range pipelineNames {
							fixture.Database.SavePipeline(fixture.Team, name, atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}})
						}
						server = fixture.Database.Serve()
					})

					It("returns 500", func() {
						Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
					})
				})
			})

			Context("when requester does not belong to the team", func() {
				BeforeEach(func() {
					fakeAccess.IsAuthorizedReturns(false)
				})

			})
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(false)
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
				fakeAccess.IsAuthenticatedReturns(true)
			})

			Context("when requester belongs to the team", func() {
				BeforeEach(func() {
					fakeAccess.IsAuthorizedReturns(true)
				})

				Context("when ordering the pipelines succeeds", func() {
					BeforeEach(func() {
						withinDB = useRealDB()
						var err error
						withinTeam, err = withinDB.Deps.teamFactory.CreateTeam(atc.Team{Name: "a-team"})
						Expect(err).NotTo(HaveOccurred())
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

				})

				Context("when a pipeline does not exist", func() {
					BeforeEach(func() {
						fixture := persistPipelineAPIFaultFixture("a-team", "", pipelineAPIFaults{
							orderPipelinesWithinGroupErr: db.ErrPipelineNotFound{Name: "a-pipeline"},
						})
						for _, vars := range []atc.InstanceVars{{"branch": "test-2"}, nil, {"branch": "test"}} {
							_, _, err := fixture.Team.SavePipeline(atc.PipelineRef{Name: "a-pipeline", InstanceVars: vars}, atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}}, db.ConfigVersion(0), false)
							Expect(err).NotTo(HaveOccurred())
						}
						server = fixture.Database.Serve()
					})

				})

				Context("when ordering the pipelines fails", func() {
					BeforeEach(func() {
						fixture := persistPipelineAPIFaultFixture("a-team", "", pipelineAPIFaults{
							orderPipelinesWithinGroupErr: errors.New("welp"),
						})
						for _, vars := range []atc.InstanceVars{{"branch": "test-2"}, nil, {"branch": "test"}} {
							_, _, err := fixture.Team.SavePipeline(atc.PipelineRef{Name: "a-pipeline", InstanceVars: vars}, atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}}, db.ConfigVersion(0), false)
							Expect(err).NotTo(HaveOccurred())
						}
						server = fixture.Database.Serve()
					})

					It("returns 500", func() {
						Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
					})
				})
			})

			Context("when requester does not belong to the team", func() {
				BeforeEach(func() {
					fakeAccess.IsAuthorizedReturns(false)
				})

			})
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(false)
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
				fakeAccess.IsAuthenticatedReturns(true)
				fakeAccess.IsAuthorizedReturns(true)
			})

			Context("when getting the debug versions db works", func() {
				var fixture pipelineDebugVersionsFixture

				BeforeEach(func() {
					fixture = persistPipelineDebugVersionsFixture()
					server = fixture.Database.Serve()
				})

				It("constructs teamDB with provided team name", func() {
					Expect(fixture.State.findTeamArguments()).To(Equal([]string{"a-team"}))
				})

				It("returns 200", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
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

			Context("when getting the debug versions db fails", func() {
				var fixture pipelineAPIFaultFixture

				BeforeEach(func() {
					fixture = persistPipelineAPIFaultFixture("a-team", "a-pipeline", pipelineAPIFaults{
						loadDebugVersionsDBErr: errors.New("nope"),
					})
					server = fixture.Database.Serve()
				})

				It("constructs teamDB with provided team name", func() {
					Expect(fixture.State.findTeamArguments()).To(Equal([]string{"a-team"}))
				})

				It("returns 500", func() {
					Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
				})

				It("does not return application/json", func() {
					Expect(response.Header.Get("Content-Type")).To(BeEmpty())
				})
			})
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(false)
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
				fakeAccess.IsAuthenticatedReturns(true)
			})
			Context("when authorized", func() {
				BeforeEach(func() {
					fakeAccess.IsAuthorizedReturns(true)
				})

				Context("when renaming succeeds", func() {
					BeforeEach(func() {
						renameDB = useRealDB()
						var err error
						renameTeam, err = renameDB.Deps.teamFactory.CreateTeam(atc.Team{Name: "a-team"})
						Expect(err).NotTo(HaveOccurred())
						renameDB.SavePipeline(renameTeam, "a-pipeline", atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}})
						server = renameDB.Serve()
					})

				})

				Context("when the pipeline does not exist", func() {
					BeforeEach(func() {
						fixture := persistPipelineAPIFaultFixture("a-team", "a-pipeline", pipelineAPIFaults{
							renamePipelineOverride: true,
							renamePipelineFound:    false,
						})
						server = fixture.Database.Serve()
					})

				})

				Context("when renaming the pipeline errors", func() {
					BeforeEach(func() {
						fixture := persistPipelineAPIFaultFixture("a-team", "a-pipeline", pipelineAPIFaults{
							renamePipelineOverride: true,
							renamePipelineFound:    false,
							renamePipelineErr:      errors.New("whoops"),
						})
						server = fixture.Database.Serve()
					})

					It("returns a 500 internal server error", func() {
						Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
					})
				})

				Context("when the new name is an invalid identifier", func() {
					BeforeEach(func() {
						renameDB = useRealDB()
						var err error
						renameTeam, err = renameDB.Deps.teamFactory.CreateTeam(atc.Team{Name: "a-team"})
						Expect(err).NotTo(HaveOccurred())
						renameDB.SavePipeline(renameTeam, "a-pipeline", atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}})
						server = renameDB.Serve()
					})

					Context("and is a string", func() {
						BeforeEach(func() {
							requestBody = `{"name":"_some-new-name"}`
						})

					})
					Context("and is an empty string", func() {
						BeforeEach(func() {
							requestBody = `{"name":""}`
						})

					})
				})
			})

			Context("when requester does not belong to the team", func() {
				BeforeEach(func() {
					fakeAccess.IsAuthorizedReturns(false)
				})

			})
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(false)
			})

		})
	})

	Describe("GET /api/v1/teams/:team_name/pipelines/:pipeline_name/builds", func() {
		var (
			response     *http.Response
			queryParams  string
			requestTeam  = "some-team"
			requestPipe  = "some-pipeline"
			listDB       *realDB
			listPipeline db.Pipeline
			listBuild1   db.Build
			listBuild2   db.Build
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
		})

		JustBeforeEach(func() {
			var err error

			response, err = client.Get(server.URL + "/api/v1/teams/" + requestTeam + "/pipelines/" + requestPipe + "/builds" + queryParams)
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(false)
			})

			Context("and the pipeline is private", func() {
				BeforeEach(func() {
					persistPipelineWithBuilds(atc.PipelineRef{Name: "some-pipeline"}, 0)
				})

			})

			Context("and the pipeline is public", func() {
				BeforeEach(func() {
					pipeline, _ := persistPipelineWithBuilds(atc.PipelineRef{Name: "some-pipeline"}, 0)
					Expect(pipeline.Expose()).To(Succeed())
				})

			})
		})

		Context("when authorized", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(true)
				fakeAccess.IsAuthorizedReturns(true)
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
					queryParams = "?limit=2"
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

			Context("when getting the build fails", func() {
				BeforeEach(func() {
					fixture := persistPipelineAPIFaultFixture("some-team", "some-pipeline", pipelineAPIFaults{
						buildsErr: errors.New("oh no!"),
					})
					server = fixture.Database.Serve()
				})

				It("returns 404 Not Found", func() {
					Expect(response.StatusCode).To(Equal(http.StatusNotFound))
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
				fakeAccess.IsAuthenticatedReturns(false)
				postDB = useRealDB()
				postPipeline = postDB.SavePipeline(postDB.Main, "a-pipeline", atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}})
				server = postDB.Serve()
				postTeam = "main"
			})

		})

		Context("when authenticated", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(true)
			})

			Context("when not authorized", func() {
				BeforeEach(func() {
					fakeAccess.IsAuthorizedReturns(false)
					postDB = useRealDB()
					postPipeline = postDB.SavePipeline(postDB.Main, "a-pipeline", atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}})
					server = postDB.Serve()
					postTeam = "main"
				})

			})

			Context("when authorized", func() {
				BeforeEach(func() {
					fakeAccess.IsAuthorizedReturns(true)
				})

				Context("when creating a started build fails", func() {
					BeforeEach(func() {
						fixture := persistPipelineAPIFaultFixture("a-team", "a-pipeline", pipelineAPIFaults{
							createStartedBuildErr: errors.New("oh no!"),
						})
						server = fixture.Database.Serve()
					})

					It("returns 500 Internal Server Error", func() {
						Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
					})
				})

				Context("when creating a started build succeeds", func() {
					BeforeEach(func() {
						postDB = useRealDB()
						postPipeline = postDB.SavePipeline(postDB.Main, "a-pipeline", atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}})
						server = postDB.Serve()
						postTeam = "main"
					})

					It("creates a started build", func() {
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
					})

				})
			})
		})
	})
})
