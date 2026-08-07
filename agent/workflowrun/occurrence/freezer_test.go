package occurrence

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc/db"
)

type definitionSourceFake struct {
	definitions map[string]*workflow.Definition
	requested   []string
	err         error
}

func (fake *definitionSourceFake) Get(name string, version int) (*workflow.Definition, bool, error) {
	fake.requested = append(fake.requested, definitionKey(name, version))
	if fake.err != nil {
		return nil, false, fake.err
	}
	definition, found := fake.definitions[definitionKey(name, version)]
	return definition, found, nil
}

func definitionKey(name string, version int) string {
	return fmt.Sprintf("%s@%d", name, version)
}

type nodeDefinitionSourceFake struct {
	definitions map[string]*workflow.NodeDefinition
	requested   []string
	err         error
}

func (fake *nodeDefinitionSourceFake) Get(name string, version int) (*workflow.NodeDefinition, bool, error) {
	fake.requested = append(fake.requested, definitionKey(name, version))
	if fake.err != nil {
		return nil, false, fake.err
	}
	definition, found := fake.definitions[definitionKey(name, version)]
	return definition, found, nil
}

type evidenceSourceFake struct {
	evidence db.AgentWorkflowRunEvidence
	runs     []db.AgentWorkflowRun
	err      error
}

func (fake *evidenceSourceFake) EvidenceForRun(
	_ context.Context,
	run db.AgentWorkflowRun,
) (db.AgentWorkflowRunEvidence, error) {
	fake.runs = append(fake.runs, run)
	if fake.err != nil {
		return db.AgentWorkflowRunEvidence{}, fake.err
	}
	return fake.evidence, nil
}

type projectionStoreFake struct {
	frozen [][]db.AgentWorkflowRunNodeOccurrence
	err    error
}

func (fake *projectionStoreFake) Freeze(
	_ context.Context,
	occurrences []db.AgentWorkflowRunNodeOccurrence,
) error {
	fake.frozen = append(fake.frozen, occurrences)
	return fake.err
}

// theRunsOwnVersion is deliberately not 1: a freezer that resolved the
// promoted version, or a hardcoded first version, would still find something
// and would still write a plausible-looking projection.
const theRunsOwnVersion = 3

// harness assembles a freezer over one real seed workflow, planned by the real
// planner, so every fixture here is one the rest of the system accepts.
type harness struct {
	freezer     *Freezer
	run         db.AgentWorkflowRun
	definitions *definitionSourceFake
	nodes       *nodeDefinitionSourceFake
	evidence    *evidenceSourceFake
	store       *projectionStoreFake
	compiled    *workflow.CompiledDefinition
}

func newHarness(t *testing.T, seed string) *harness {
	t.Helper()
	compiled := compileSeed(t, seed)
	return harnessFor(t, compiled)
}

func harnessFor(t *testing.T, compiled *workflow.CompiledDefinition) *harness {
	t.Helper()
	buildID := int64(9001)
	run := db.AgentWorkflowRun{
		ID:                   snapshot.WorkflowRunID(42),
		TeamID:               1,
		WorkflowName:         compiled.Name,
		WorkflowDefinitionID: 41,
		WorkflowVersion:      theRunsOwnVersion,
		ActualPlan:           planSeed(t, compiled),
		PlannedBuildID:       &buildID,
	}
	definitions := &definitionSourceFake{definitions: map[string]*workflow.Definition{
		definitionKey(compiled.Name, theRunsOwnVersion): {
			ID: 41, Name: compiled.Name, Version: theRunsOwnVersion, Compiled: *compiled,
		},
	}}
	nodes := &nodeDefinitionSourceFake{definitions: map[string]*workflow.NodeDefinition{}}
	evidence := &evidenceSourceFake{}
	store := &projectionStoreFake{}
	freezer, err := NewFreezer(evidence, definitions, nodes, store)
	if err != nil {
		t.Fatalf("NewFreezer: %v", err)
	}
	return &harness{
		freezer: freezer, run: run, definitions: definitions, nodes: nodes,
		evidence: evidence, store: store, compiled: compiled,
	}
}

func (h *harness) freeze(t *testing.T) []db.AgentWorkflowRunNodeOccurrence {
	t.Helper()
	if err := h.freezer.FreezeRun(context.Background(), h.run); err != nil {
		t.Fatalf("FreezeRun: %v", err)
	}
	if len(h.store.frozen) != 1 {
		t.Fatalf("freezes = %d, want exactly one", len(h.store.frozen))
	}
	return h.store.frozen[0]
}

func (h *harness) planID(t *testing.T, nodeID string) string {
	t.Helper()
	return planIDOf(t, h.run.ActualPlan, nodeID)
}

func rowFor(t *testing.T, rows []db.AgentWorkflowRunNodeOccurrence, nodeID string) db.AgentWorkflowRunNodeOccurrence {
	t.Helper()
	var found []db.AgentWorkflowRunNodeOccurrence
	for _, row := range rows {
		if row.NodeID == nodeID {
			found = append(found, row)
		}
	}
	if len(found) != 1 {
		t.Fatalf("expected exactly one row for %q, got %d", nodeID, len(found))
	}
	return found[0]
}

func nodeIDsOf(rows []db.AgentWorkflowRunNodeOccurrence) []string {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.NodeID)
	}
	sort.Strings(ids)
	return ids
}

func sortedKeys(nodes map[string]string) []string {
	keys := make([]string, 0, len(nodes))
	for key := range nodes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// A terminal run projects every execution node its own graph contains, once,
// even when it reached none of them. All-pending is the honest record that the
// run got nowhere; omitting the nodes would make a failed run look like a
// workflow that never had them.
func TestFreezerProjectsOneRowPerExecutionNodeOfEverySeed(t *testing.T) {
	for _, seed := range seedNames(t) {
		t.Run(seed, func(t *testing.T) {
			harness := newHarness(t, seed)
			rows := harness.freeze(t)

			want := sortedKeys(executionNodesOf(t, harness.compiled))
			if len(want) == 0 {
				t.Fatalf("seed %q has no execution nodes; the fixture proves nothing", seed)
			}
			if got := nodeIDsOf(rows); !slices.Equal(got, want) {
				t.Fatalf("projected %v, want exactly one row per execution node %v", got, want)
			}
			for _, row := range rows {
				if row.Status != string(StatusPending) {
					t.Errorf("node %q status = %q, want pending with no evidence", row.NodeID, row.Status)
				}
				if row.WorkflowRunID != int64(harness.run.ID) || row.TeamID != harness.run.TeamID {
					t.Errorf("node %q carries run identity %d/%d", row.NodeID, row.WorkflowRunID, row.TeamID)
				}
				if row.WorkflowVersion != theRunsOwnVersion {
					t.Errorf("node %q version = %d, want the run's own %d",
						row.NodeID, row.WorkflowVersion, theRunsOwnVersion)
				}
			}
		})
	}
}

// The graph filter is what makes the join exact: RenderFunction prepends a
// synthetic load_snapshot per input port named with the BARE port name, and
// the graph calls the same concept input:<port>. A projection that kept those
// would carry identities no graph node can ever join to.
func TestFreezerDropsSyntheticInputPortLoads(t *testing.T) {
	harness := newHarness(t, "merge-delivery-v3")
	rows := harness.freeze(t)

	executionNodes := executionNodesOf(t, harness.compiled)
	for _, row := range rows {
		kind, found := executionNodes[row.NodeID]
		if !found {
			t.Errorf("projected %q, which the run's graph does not contain", row.NodeID)
			continue
		}
		if kind != row.NodeKind {
			t.Errorf("node %q kind = %q, want the graph's %q", row.NodeID, row.NodeKind, kind)
		}
	}
}

// THE reason this projection exists. A deterministic task step records no
// attempt metric, no wait and no publication: build events are its only
// durable trace, and Concourse reclaims those. If this path is wrong the
// projection silently loses exactly the data it was built to preserve, and
// looks healthy while doing it, because every other node kind populates fine.
func TestFreezerProjectsADeterministicTaskFromBuildEventsAlone(t *testing.T) {
	harness := newHarness(t, "merge-delivery-v3")
	taskPlanID := harness.planID(t, "merge-preflight")
	harness.evidence.evidence = db.AgentWorkflowRunEvidence{
		BuildStepStatus: map[string]string{taskPlanID: db.AgentNodeBuildStepSucceeded},
	}

	rows := harness.freeze(t)

	task := rowFor(t, rows, "merge-preflight")
	if task.NodeKind != KindTask {
		t.Fatalf("node kind = %q, want task", task.NodeKind)
	}
	if task.Status != string(StatusSucceeded) {
		t.Errorf("status = %q, want succeeded from build-event evidence alone", task.Status)
	}
	if task.PlanID != taskPlanID {
		t.Errorf("plan ID = %q, want the planner's %q", task.PlanID, taskPlanID)
	}
	// The evidence is keyed by plan ID, so a reader keyed on anything else
	// would spray one status across every task.
	for _, row := range rows {
		if row.NodeID == "merge-preflight" {
			continue
		}
		if row.Status != string(StatusPending) {
			t.Errorf("node %q status = %q, want pending — only one step had evidence", row.NodeID, row.Status)
		}
	}
}

// Every deterministic task outcome has to survive, not just the happy one. A
// mapping that collapsed failed or errored onto pending would erase the runs
// a human actually goes looking for.
func TestFreezerProjectsEveryDeterministicTaskOutcome(t *testing.T) {
	for _, testCase := range []struct {
		buildStep string
		want      Status
	}{
		{db.AgentNodeBuildStepSucceeded, StatusSucceeded},
		{db.AgentNodeBuildStepFailed, StatusFailed},
		{db.AgentNodeBuildStepErrored, StatusErrored},
	} {
		t.Run(testCase.buildStep, func(t *testing.T) {
			harness := newHarness(t, "merge-delivery-v3")
			harness.evidence.evidence = db.AgentWorkflowRunEvidence{
				BuildStepStatus: map[string]string{
					harness.planID(t, "merge-prepare"): testCase.buildStep,
				},
			}

			row := rowFor(t, harness.freeze(t), "merge-prepare")
			if row.Status != string(testCase.want) {
				t.Errorf("status = %q, want %q", row.Status, testCase.want)
			}
		})
	}
}

// A build step status this code does not understand must leave the node
// pending. Coercing it to something plausible would freeze a guess as
// immutable history, which is worse than an honest gap.
func TestFreezerLeavesAnUnrecognisedBuildStepStatusPending(t *testing.T) {
	harness := newHarness(t, "merge-delivery-v3")
	harness.evidence.evidence = db.AgentWorkflowRunEvidence{
		BuildStepStatus: map[string]string{
			harness.planID(t, "merge-prepare"): "half-finished",
		},
	}

	row := rowFor(t, harness.freeze(t), "merge-prepare")
	if row.Status != string(StatusPending) {
		t.Errorf("status = %q, want pending for an unrecognised build step status", row.Status)
	}
}

// A deterministic task and an agent step land in the same projection from
// different sources. Proving them together is what shows the build-event
// reader is additive rather than a replacement for the metrics path.
//
// small-fix-v3 is the seed used here because it is the shipped workflow that
// carries agent steps and a deterministic task in one graph.
func TestFreezerProjectsTasksAndAgentStepsTogether(t *testing.T) {
	harness := newHarness(t, "small-fix-v3")
	started := time.Date(2026, 7, 31, 9, 0, 0, 0, time.UTC)
	harness.evidence.evidence = db.AgentWorkflowRunEvidence{
		AttemptMetrics: []db.AgentNodeAttemptMetric{{
			PlanID:           harness.planID(t, "review"),
			ExecutionAttempt: 2,
			Status:           "ok",
			CostUSD:          1.75,
			CreatedAt:        started,
			UpdatedAt:        started.Add(3 * time.Minute),
		}},
		BuildStepStatus: map[string]string{
			harness.planID(t, "dev-validation-repository-gates"): db.AgentNodeBuildStepFailed,
		},
	}

	rows := harness.freeze(t)

	agent := rowFor(t, rows, "review")
	if agent.Status != string(StatusSucceeded) || agent.Attempt != 2 || agent.CostUSD != 1.75 {
		t.Errorf("agent row = %+v, want succeeded attempt 2 costing 1.75", agent)
	}
	if agent.StartedAt == nil || !agent.StartedAt.Equal(started) {
		t.Errorf("agent started at %v, want %v", agent.StartedAt, started)
	}
	if agent.CompletedAt == nil || agent.DurationSeconds != 180 {
		t.Errorf("agent completion = %v/%ds, want 180s", agent.CompletedAt, agent.DurationSeconds)
	}
	task := rowFor(t, rows, "dev-validation-repository-gates")
	if task.Status != string(StatusFailed) {
		t.Errorf("task status = %q, want failed", task.Status)
	}
}

// Waits and publications reach the projection with the durable row they came
// from, so the run page can link a node straight to its evidence.
func TestFreezerCarriesWaitAndPublicationIdentityOntoTheRow(t *testing.T) {
	harness := newHarness(t, "merge-delivery-v3")
	created := time.Date(2026, 7, 31, 9, 0, 0, 0, time.UTC)
	resolved := created.Add(2 * time.Minute)
	harness.evidence.evidence = db.AgentWorkflowRunEvidence{
		Waits: []db.AgentNodeWait{{
			ID: 77, PlanID: harness.planID(t, "merge-approval"), OutputName: "merge-approval",
			Status: "resolved", TimeoutPolicy: "fail", CreatedAt: created, ResolvedAt: &resolved,
		}},
		Publications: []db.AgentNodePublication{{
			ID: 88, PlanID: harness.planID(t, "land-merge"),
			Status: "succeeded", CreatedAt: created, UpdatedAt: resolved,
		}},
	}

	rows := harness.freeze(t)

	await := rowFor(t, rows, "merge-approval")
	if await.Status != string(StatusSucceeded) || await.WaitID == nil || *await.WaitID != 77 {
		t.Errorf("await row = %+v, want succeeded carrying wait 77", await)
	}
	publish := rowFor(t, rows, "land-merge")
	if publish.Status != string(StatusSucceeded) || publish.PublicationID == nil || *publish.PublicationID != 88 {
		t.Errorf("publish row = %+v, want succeeded carrying publication 88", publish)
	}
	if publish.DurationSeconds != 120 {
		t.Errorf("publish duration = %ds, want 120", publish.DurationSeconds)
	}
}

// agent_publication_occurrences.plan_id is what makes this possible. A run
// whose publication carries no plan identity cannot be joined to its node, and
// the node must stay pending rather than adopt some other step's publication.
func TestFreezerLeavesAPublishNodeWithNoMatchingPlanPending(t *testing.T) {
	harness := newHarness(t, "merge-delivery-v3")
	harness.evidence.evidence = db.AgentWorkflowRunEvidence{
		Publications: []db.AgentNodePublication{{
			ID: 88, PlanID: "some/other/plan", Status: "succeeded",
		}},
	}

	row := rowFor(t, harness.freeze(t), "land-merge")
	if row.Status != string(StatusPending) || row.PublicationID != nil {
		t.Errorf("publish row = %+v, want pending with no publication", row)
	}
}

// A run's projection must describe the revision that actually executed. Asking
// for the promoted version instead produces a plausible-looking projection of
// the wrong workflow, and it is wrong for exactly the runs a human most wants
// to inspect: old ones, whose workflow has since moved on.
func TestFreezerLoadsTheRunsOwnWorkflowVersion(t *testing.T) {
	harness := newHarness(t, "merge-delivery-v3")
	// A later, promoted revision of the same workflow name with a different
	// node set. Nothing about it may reach this run's projection.
	promoted := compileSeed(t, "small-fix-v3")
	harness.definitions.definitions[definitionKey(harness.run.WorkflowName, theRunsOwnVersion+5)] =
		&workflow.Definition{
			ID: 99, Name: harness.run.WorkflowName, Version: theRunsOwnVersion + 5,
			Live: true, Compiled: *promoted,
		}

	rows := harness.freeze(t)

	wantRequest := definitionKey(harness.run.WorkflowName, theRunsOwnVersion)
	if len(harness.definitions.requested) != 1 || harness.definitions.requested[0] != wantRequest {
		t.Fatalf("requested %v, want exactly %q", harness.definitions.requested, wantRequest)
	}
	for _, row := range rows {
		if row.NodeID == "implement" || row.NodeID == "dev-validation-repository-gates" {
			t.Fatalf("projected %q from the promoted revision, not the run's own", row.NodeID)
		}
	}
	// and the run's own revision is fully there
	rowFor(t, rows, "merge-prepare")
}

// The evidence must be gathered for the run being frozen, not for whatever run
// the reconciler happened to see first.
func TestFreezerGathersEvidenceForTheRunItIsFreezing(t *testing.T) {
	harness := newHarness(t, "small-fix-v3")
	harness.freeze(t)

	if len(harness.evidence.runs) != 1 || harness.evidence.runs[0].ID != harness.run.ID {
		t.Fatalf("evidence gathered for %+v, want run %d", harness.evidence.runs, harness.run.ID)
	}
}

// Frozen history is immutable and the reconciler never re-attempts the freeze,
// so a projection derived from a workflow version that is gone would be
// written once and wrongly, forever. Failing loudly leaves no history, which
// is the recoverable outcome.
func TestFreezerRefusesAWorkflowVersionItCannotResolve(t *testing.T) {
	harness := newHarness(t, "small-fix-v3")
	harness.run.WorkflowVersion = theRunsOwnVersion + 1

	err := harness.freezer.FreezeRun(context.Background(), harness.run)
	if err == nil || !containsFold(err.Error(), "no version") {
		t.Fatalf("error = %v, want a missing-version failure", err)
	}
	if len(harness.store.frozen) != 0 {
		t.Fatalf("wrote %+v, want nothing frozen", harness.store.frozen)
	}
}

func TestFreezerReportsDefinitionSourceFailures(t *testing.T) {
	harness := newHarness(t, "small-fix-v3")
	harness.definitions.err = errors.New("store unavailable")

	err := harness.freezer.FreezeRun(context.Background(), harness.run)
	if err == nil || !containsFold(err.Error(), "store unavailable") {
		t.Fatalf("error = %v, want the store failure", err)
	}
	if len(harness.store.frozen) != 0 {
		t.Fatalf("wrote %+v, want nothing frozen", harness.store.frozen)
	}
}

// A definition whose graph cannot be derived yields no node set, and Derive
// refuses an empty one rather than writing an empty projection that would read
// as "this run had no nodes".
func TestFreezerRefusesADefinitionWithNoExecutionNodes(t *testing.T) {
	harness := newHarness(t, "small-fix-v3")
	harness.definitions.definitions[definitionKey(harness.run.WorkflowName, theRunsOwnVersion)] =
		&workflow.Definition{
			ID: 41, Name: harness.run.WorkflowName, Version: theRunsOwnVersion,
			Compiled: workflow.CompiledDefinition{
				SchemaVersion: 3, Name: harness.run.WorkflowName,
				Function: &workflow.FunctionConfig{SignatureVersion: 1},
			},
		}

	err := harness.freezer.FreezeRun(context.Background(), harness.run)
	if err == nil || !containsFold(err.Error(), "execution nodes") {
		t.Fatalf("error = %v, want an empty-node-set refusal", err)
	}
	if len(harness.store.frozen) != 0 {
		t.Fatalf("wrote %+v, want nothing frozen", harness.store.frozen)
	}
}

func TestFreezerReportsGraphDerivationFailures(t *testing.T) {
	harness := newHarness(t, "small-fix-v3")
	harness.definitions.definitions[definitionKey(harness.run.WorkflowName, theRunsOwnVersion)] =
		&workflow.Definition{ID: 41, Name: harness.run.WorkflowName, Version: theRunsOwnVersion}

	// Named precisely: a swallowed graph.Build error still fails the freeze,
	// but through Derive's empty-node-set guard, which reports a different
	// fault and would leave the real cause invisible in the log.
	err := harness.freezer.FreezeRun(context.Background(), harness.run)
	if err == nil || !containsFold(err.Error(), "deriving graph") {
		t.Fatalf("error = %v, want a graph derivation failure", err)
	}
	if len(harness.store.frozen) != 0 {
		t.Fatalf("wrote %+v, want nothing frozen", harness.store.frozen)
	}
}

// Partial evidence is worse than none, because the freeze happens once: a run
// frozen from a failed gather would record nodes as never-reached forever.
func TestFreezerDoesNotFreezeWhenEvidenceIsUnavailable(t *testing.T) {
	harness := newHarness(t, "small-fix-v3")
	harness.evidence.err = errors.New("evidence unavailable")

	err := harness.freezer.FreezeRun(context.Background(), harness.run)
	if err == nil || !containsFold(err.Error(), "evidence unavailable") {
		t.Fatalf("error = %v, want the evidence failure", err)
	}
	if len(harness.store.frozen) != 0 {
		t.Fatalf("wrote %+v, want nothing frozen", harness.store.frozen)
	}
}

// A run with no frozen actual plan has no node identities at all, so there is
// nothing honest to project.
func TestFreezerRefusesARunWithNoActualPlan(t *testing.T) {
	harness := newHarness(t, "small-fix-v3")
	harness.run.ActualPlan = nil

	err := harness.freezer.FreezeRun(context.Background(), harness.run)
	if err == nil || !containsFold(err.Error(), "actual plan") {
		t.Fatalf("error = %v, want a missing-plan failure", err)
	}
	if len(harness.store.frozen) != 0 {
		t.Fatalf("wrote %+v, want nothing frozen", harness.store.frozen)
	}
}

func TestFreezerReportsStoreFailures(t *testing.T) {
	harness := newHarness(t, "small-fix-v3")
	harness.store.err = errors.New("projection unavailable")

	err := harness.freezer.FreezeRun(context.Background(), harness.run)
	if err == nil || !containsFold(err.Error(), "projection unavailable") {
		t.Fatalf("error = %v, want the store failure", err)
	}
}

// Inside an ordinary WORKFLOW run a reusable node is expanded away before
// compilation, so graph.Build cannot see it and this call site must not invent
// it. The table's CHECK requires the name and version to be absent together; a
// half-filled pair would make every freeze fail at the database. (A run OF a
// reusable node is the other case, and it does carry the pair — see
// TestFreezerProjectsAReusableNodeRun.)
func TestFreezerLeavesReusableNodeProvenanceUnset(t *testing.T) {
	harness := newHarness(t, "small-fix-v3")
	rows := harness.freeze(t)

	for _, row := range rows {
		if row.ReusableNodeName != "" || row.ReusableNodeVersion != nil {
			t.Errorf("node %q invented reusable provenance %q/%v",
				row.NodeID, row.ReusableNodeName, row.ReusableNodeVersion)
		}
	}
	if len(harness.nodes.requested) != 0 {
		t.Errorf("a workflow run consulted the node store: %v", harness.nodes.requested)
	}
}

func TestNewFreezerRejectsMissingDependencies(t *testing.T) {
	evidence := &evidenceSourceFake{}
	definitions := &definitionSourceFake{}
	nodes := &nodeDefinitionSourceFake{}
	store := &projectionStoreFake{}

	for name, construct := range map[string]func() (*Freezer, error){
		"evidence":   func() (*Freezer, error) { return NewFreezer(nil, definitions, nodes, store) },
		"definition": func() (*Freezer, error) { return NewFreezer(evidence, nil, nodes, store) },
		"node":       func() (*Freezer, error) { return NewFreezer(evidence, definitions, nil, store) },
		"projection": func() (*Freezer, error) { return NewFreezer(evidence, definitions, nodes, nil) },
		"everything": func() (*Freezer, error) { return NewFreezer(nil, nil, nil, nil) },
		"none missing": func() (*Freezer, error) {
			return NewFreezer(evidence, definitions, nodes, store)
		},
	} {
		freezer, err := construct()
		if name == "none missing" {
			if err != nil || freezer == nil {
				t.Errorf("%s: NewFreezer = %v, %v, want a freezer", name, freezer, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("%s: NewFreezer accepted a missing dependency", name)
		}
	}
}

func containsFold(haystack, needle string) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) && indexFold(haystack, needle) >= 0)
}

func indexFold(haystack, needle string) int {
	for index := 0; index+len(needle) <= len(haystack); index++ {
		if equalFold(haystack[index:index+len(needle)], needle) {
			return index
		}
	}
	return -1
}

func equalFold(left, right string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		leftChar, rightChar := left[index], right[index]
		if 'A' <= leftChar && leftChar <= 'Z' {
			leftChar += 'a' - 'A'
		}
		if 'A' <= rightChar && rightChar <= 'Z' {
			rightChar += 'a' - 'A'
		}
		if leftChar != rightChar {
			return false
		}
	}
	return true
}
