package workflowrun

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/creds/credsfakes"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/postgresrunner"
	"github.com/concourse/concourse/atc/util"
	"github.com/stretchr/testify/require"
)

var workflowRunPostgres postgresrunner.StandardTestRunner

func TestMain(m *testing.M) {
	os.Exit(workflowRunPostgres.Main(m))
}

type workflowRunDB struct {
	Conn         db.DbConn
	Team         db.Team
	Teams        db.TeamFactory
	Builds       db.BuildFactory
	Runs         db.AgentWorkflowRunsFactory
	Templates    db.WorkflowRunTemplateFactory
	PipelineRuns db.PipelineRunFactory
}

func useRealWorkflowRunDB(t *testing.T) workflowRunDB {
	t.Helper()

	db.CleanupBaseResourceTypesCache()
	conn := workflowRunPostgres.OpenConn(t)
	locks := lock.NewTestLockFactory(&workflowRunLockDB{held: map[string]bool{}})
	teams := db.NewTeamFactory(conn, locks)
	team, err := teams.CreateTeam(atc.Team{Name: "workflow-run-main"})
	require.NoError(t, err)

	checks := db.NewCheckFactory(
		conn,
		locks,
		new(credsfakes.FakeSecrets),
		new(credsfakes.FakeVarSourcePool),
		make(chan db.Build, 16),
		util.NewSequenceGenerator(1),
	)

	return workflowRunDB{
		Conn:         conn,
		Team:         team,
		Teams:        teams,
		Builds:       db.NewBuildFactory(conn, locks, 0, time.Hour),
		Runs:         db.NewAgentWorkflowRunsFactory(conn),
		Templates:    db.NewWorkflowRunTemplateFactory(conn, locks),
		PipelineRuns: db.NewPipelineRunFactory(lagertest.NewTestLogger("workflowrun-postgres"), conn, locks, checks),
	}
}

// workflowRunLockDB gives the production test lock factory advisory-lock
// semantics without opening connections outside StandardTestRunner's clone.
type workflowRunLockDB struct {
	mu   sync.Mutex
	held map[string]bool
}

func (database *workflowRunLockDB) Acquire(id lock.LockID) (bool, error) {
	database.mu.Lock()
	defer database.mu.Unlock()

	key := fmt.Sprint([]int(id))
	if database.held[key] {
		return false, nil
	}
	database.held[key] = true
	return true, nil
}

func (database *workflowRunLockDB) Release(id lock.LockID) (bool, error) {
	database.mu.Lock()
	defer database.mu.Unlock()

	key := fmt.Sprint([]int(id))
	if !database.held[key] {
		return false, nil
	}
	delete(database.held, key)
	return true, nil
}

func createDurableRun(t *testing.T, fixture workflowRunDB) (db.AgentWorkflowRun, workflow.RenderedFunction) {
	t.Helper()

	definitionName := fmt.Sprintf("canceler-workflow-%d", time.Now().UnixNano())
	definitionSource := fmt.Sprintf(`schema_version: 3
name: %s
signature_version: 1
inputs: []
outputs: []
plan:
  - agent: run
    function_id: run
    prompt: test
`, definitionName)
	definitionHash := workflow.Manifest{
		workflow.LegacyWorkflowFileName: definitionSource,
	}.Hash()
	var definitionID int
	require.NoError(t, fixture.Conn.QueryRow(`
		INSERT INTO agent_workflow_definitions
			(definition_kind, name, version, content_hash, definition, created_by,
			 schema_version, signature_version, live)
		VALUES ('workflow', $1, 1, $2, $3, 'alice', 3, 1, true)
		RETURNING id
	`, definitionName, definitionHash, definitionSource).Scan(&definitionID))

	rendered, err := workflow.RenderFunction(workflow.FunctionTarget{
		Kind:                 workflow.TargetWorkflow,
		WorkflowDefinitionID: definitionID,
		WorkflowName:         definitionName,
		WorkflowVersion:      1,
		SignatureVersion:     1,
		Function: workflow.FunctionConfig{
			SignatureVersion: 1,
			Plan: []atc.Step{{Config: &atc.TaskStep{
				Name:       "run",
				FunctionID: "run",
				Config: &atc.TaskConfig{
					Platform: "linux",
					ImageResource: &atc.ImageResource{
						Type:    "registry-image",
						Source:  atc.Source{"repository": "example/task"},
						Version: atc.Version{"digest": "sha256:immutable"},
					},
					Run: atc.TaskRunConfig{Path: "/bin/true"},
				},
			}}},
		},
	})
	require.NoError(t, err)
	canonical, err := rendered.Config.CanonicalJSON()
	require.NoError(t, err)
	targetHash, err := workflow.TargetConfigHash(rendered.Config)
	require.NoError(t, err)
	durable, created, err := fixture.Runs.CreateWithInputs(context.Background(), db.AgentWorkflowRunCreateRequest{
		DefinitionKind:          workflow.DefinitionKindWorkflow,
		TeamID:                  fixture.Team.ID(),
		TeamName:                fixture.Team.Name(),
		WorkflowDefinitionID:    definitionID,
		WorkflowName:            definitionName,
		WorkflowVersion:         1,
		SchemaVersion:           3,
		SignatureVersion:        1,
		DefinitionContentHash:   definitionHash,
		IdempotencyKey:          fmt.Sprintf("cancel-%d", time.Now().UnixNano()),
		ParameterizedConfig:     json.RawMessage(canonical),
		ParameterizedConfigHash: targetHash,
		OriginKind:              "manual",
		CreatedBy:               "alice",
		Status:                  db.AgentWorkflowRunStatusAdmitting,
		Inputs:                  map[string]snapshot.SnapshotRef{},
	})
	require.NoError(t, err)
	require.True(t, created)
	return durable, rendered
}

func createLinkedExecution(t *testing.T, fixture workflowRunDB) (db.AgentWorkflowRun, db.BuildForAPI) {
	t.Helper()

	ctx := context.Background()
	durable, rendered := createDurableRun(t, fixture)
	template, created, err := fixture.Templates.SaveWorkflowRunTemplate(
		ctx,
		fixture.Team.ID(),
		atc.PipelineRef{Name: fmt.Sprintf("cancel-template-%d", durable.ID)},
		rendered.Config,
	)
	require.NoError(t, err)
	require.True(t, created)
	targetHash, err := workflow.TargetConfigHash(rendered.Config)
	require.NoError(t, err)
	templateRef := db.WorkflowRunTemplateRef{
		PipelineID:    template.ID(),
		TeamID:        fixture.Team.ID(),
		Name:          template.Name(),
		ConfigVersion: int(template.ConfigVersion()),
		FullHash:      targetHash,
	}
	envelope, err := rendered.ExecutionEnvelope(map[string]any{
		"workflow_run_id": durable.ID.String(),
	})
	require.NoError(t, err)
	execution, created, err := fixture.PipelineRuns.CreateRunForWorkflowRun(
		ctx,
		durable.ID,
		templateRef,
		envelope,
		"alice",
		func(context.Context, int) error { return nil },
	)
	require.NoError(t, err)
	require.True(t, created)

	stored, found, err := fixture.Runs.Get(ctx, fixture.Team.ID(), durable.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.NotNil(t, stored.PipelineRunID)
	require.Equal(t, execution.PipelineRun.ID(), *stored.PipelineRunID)
	require.NotNil(t, stored.TemplatePipelineID)
	require.Equal(t, templateRef.PipelineID, *stored.TemplatePipelineID)
	instanceID, hasInstance := execution.PipelineRun.InstancePipelineID()
	require.True(t, hasInstance)
	require.NotNil(t, stored.InstancePipelineID)
	require.Equal(t, instanceID, *stored.InstancePipelineID)
	require.Len(t, execution.EntryBuildIDs, 1)
	require.NotNil(t, stored.PlannedBuildID)
	require.Equal(t, int64(execution.EntryBuildIDs[0]), *stored.PlannedBuildID)
	require.JSONEq(t, string(execution.InstanceCanonicalJSON), string(stored.ConcreteConfig))
	require.NotNil(t, stored.ConcreteConfigHash)
	require.Equal(t, execution.InstanceConfigHash, *stored.ConcreteConfigHash)

	changed, err := fixture.Runs.Transition(
		ctx,
		durable.ID,
		db.AgentWorkflowRunStatusAdmitting,
		db.AgentWorkflowRunStatusRunning,
		"",
	)
	require.NoError(t, err)
	require.True(t, changed)
	stored, found, err = fixture.Runs.Get(ctx, fixture.Team.ID(), durable.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, db.AgentWorkflowRunStatusRunning, stored.Status)
	require.NotNil(t, stored.PlannedBuildID)
	selected, found, err := fixture.Builds.BuildForAPI(int(*stored.PlannedBuildID))
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, int(*stored.PlannedBuildID), selected.ID())
	require.Equal(t, fixture.Team.ID(), selected.TeamID())
	return stored, selected
}
