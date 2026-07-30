package workflow_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/agent/workflow/workflowtest"
)

type upgradePromotionValidator struct{}

func (upgradePromotionValidator) ValidatePromotion(workflow.Definition) error { return nil }

type upgradeNodeStore struct {
	*workflowtest.MemoryNodeStore

	mu       sync.Mutex
	bindings map[int][]workflow.ResolvedNodeBinding
}

func newUpgradeNodeStore() *upgradeNodeStore {
	return &upgradeNodeStore{
		MemoryNodeStore: workflowtest.NewMemoryNodeStore(),
		bindings:        map[int][]workflow.ResolvedNodeBinding{},
	}
}

func (store *upgradeNodeStore) Bindings(workflowDefinitionID int) ([]workflow.ResolvedNodeBinding, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return cloneUpgradeBindings(store.bindings[workflowDefinitionID]), nil
}

func (store *upgradeNodeStore) setBindings(workflowDefinitionID int, bindings []workflow.ResolvedNodeBinding) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.bindings[workflowDefinitionID] = cloneUpgradeBindings(bindings)
}

func (store *upgradeNodeStore) corruptBindingHash(workflowDefinitionID int, instanceName string) {
	store.mu.Lock()
	defer store.mu.Unlock()
	for index := range store.bindings[workflowDefinitionID] {
		if store.bindings[workflowDefinitionID][index].InstanceName == instanceName {
			store.bindings[workflowDefinitionID][index].NodeContentHash = "stale-content-hash"
		}
	}
}

func (store *upgradeNodeStore) corruptAllBindingHashes(workflowDefinitionID int) {
	store.mu.Lock()
	defer store.mu.Unlock()
	for index := range store.bindings[workflowDefinitionID] {
		store.bindings[workflowDefinitionID][index].NodeContentHash = "stale-content-hash"
	}
}

func (store *upgradeNodeStore) replaceBindingParameters(
	workflowDefinitionID int,
	instanceName string,
	parameters map[string]string,
) {
	store.mu.Lock()
	defer store.mu.Unlock()
	for index := range store.bindings[workflowDefinitionID] {
		if store.bindings[workflowDefinitionID][index].InstanceName == instanceName {
			store.bindings[workflowDefinitionID][index].Parameters = cloneUpgradeStringMap(parameters)
		}
	}
}

func cloneUpgradeBindings(source []workflow.ResolvedNodeBinding) []workflow.ResolvedNodeBinding {
	cloned := make([]workflow.ResolvedNodeBinding, len(source))
	for index := range source {
		cloned[index] = source[index]
		cloned[index].InputMapping = cloneUpgradeStringMap(source[index].InputMapping)
		cloned[index].OutputMapping = cloneUpgradeStringMap(source[index].OutputMapping)
		cloned[index].Parameters = cloneUpgradeStringMap(source[index].Parameters)
	}
	return cloned
}

func cloneUpgradeStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	cloned := make(map[string]string, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

// bindingWorkflowStore gives the semantic in-memory stores the same split
// responsibility as PostgreSQL: workflow imports own immutable definitions,
// while the node store exposes bindings keyed by the returned definition ID.
type bindingWorkflowStore struct {
	workflow.Store
	nodes *upgradeNodeStore

	mu             sync.Mutex
	importCalls    int
	importFailures map[string]error
	corruptImports map[string]bool
}

func (store *bindingWorkflowStore) ImportManifestWithOutcome(
	name string,
	manifest workflow.Manifest,
	createdBy string,
) (workflow.ImportOutcome, error) {
	store.mu.Lock()
	store.importCalls++
	failure := store.importFailures[name]
	store.mu.Unlock()
	if failure != nil {
		return workflow.ImportOutcome{}, failure
	}
	outcome, err := store.Store.ImportManifestWithOutcome(name, manifest, createdBy)
	if err != nil {
		return workflow.ImportOutcome{}, err
	}
	_, bindings, err := workflow.CompileDefinitionWithNodes(manifest, store.nodes)
	if err != nil {
		return workflow.ImportOutcome{}, err
	}
	store.nodes.setBindings(outcome.Definition.ID, bindings)
	store.mu.Lock()
	corruptBindings := store.corruptImports[name]
	store.mu.Unlock()
	if corruptBindings {
		store.nodes.corruptAllBindingHashes(outcome.Definition.ID)
	}
	return outcome, nil
}

func (store *bindingWorkflowStore) importCallCount() int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.importCalls
}

type nodeUpgradeFixture struct {
	nodes     *upgradeNodeStore
	workflows *bindingWorkflowStore
	service   workflow.NodeUpgradeService
}

func newNodeUpgradeFixture(t *testing.T) *nodeUpgradeFixture {
	t.Helper()
	nodes := newUpgradeNodeStore()
	for version := 1; version <= 5; version++ {
		if _, err := nodes.ImportManifest(
			"code-review",
			compatibleUpgradeNodeManifest("code-review", fmt.Sprintf("review version %d", version)),
			"node-author",
		); err != nil {
			t.Fatalf("import node version %d: %v", version, err)
		}
	}
	if _, err := nodes.Release("code-review", 4, workflow.ReleaseCompatible, "releaser"); err != nil {
		t.Fatalf("release predecessor: %v", err)
	}
	if _, err := nodes.Release("code-review", 5, workflow.ReleaseCompatible, "releaser"); err != nil {
		t.Fatalf("release successor: %v", err)
	}

	memory := workflowtest.NewMemoryStoreWithNodeResolver(nodes, upgradePromotionValidator{})
	workflows := &bindingWorkflowStore{
		Store:          memory,
		nodes:          nodes,
		importFailures: map[string]error{},
		corruptImports: map[string]bool{},
	}
	fixture := &nodeUpgradeFixture{
		nodes:     nodes,
		workflows: workflows,
		service:   workflow.NewNodeUpgradeService(nodes, workflows),
	}
	fixture.importLiveWorkflow(t, "small-fix", 7, workflow.WorkflowFileName, 2)
	fixture.importLiveWorkflow(t, "version-upgrade", 3, workflow.LegacyWorkflowFileName, 1)
	fixture.importLiveWorkflow(t, "dependency-audit", 2, workflow.WorkflowFileName, 1)
	return fixture
}

func (fixture *nodeUpgradeFixture) importLiveWorkflow(
	t *testing.T,
	name string,
	liveVersion int,
	sourceFile string,
	references int,
) {
	t.Helper()
	for version := 1; version <= liveVersion; version++ {
		outcome, err := fixture.workflows.ImportManifestWithOutcome(
			name,
			upgradeWorkflowManifest(name, sourceFile, version, 4, references),
			"workflow-author",
		)
		if err != nil {
			t.Fatalf("import %s@%d: %v", name, version, err)
		}
		if !outcome.Inserted || outcome.Definition.Version != version {
			t.Fatalf("import %s@%d outcome = %+v", name, version, outcome)
		}
	}
	if _, err := fixture.workflows.Promote(name, liveVersion, "promoter"); err != nil {
		t.Fatalf("promote %s@%d: %v", name, liveVersion, err)
	}
}

func compatibleUpgradeNodeManifest(name, prompt string) workflow.Manifest {
	return workflow.Manifest{workflow.NodeFileName: fmt.Sprintf(`schema_version: 1
name: %s
inputs:
  - {name: repository, type: repository/v1}
outputs:
  - {name: review, type: review/v1}
parameters:
  - {name: MODE, default: standard}
step:
  agent: review
  prompt: %s
`, name, prompt)}
}

func upgradeWorkflowManifest(name, sourceFile string, revision, nodeVersion, references int) workflow.Manifest {
	outputs := ""
	plan := ""
	for index := 1; index <= references; index++ {
		output := fmt.Sprintf("review-%d", index)
		outputs += fmt.Sprintf("  - {name: %s, type: review/v1, from: %s}\n", output, output)
		reference := fmt.Sprintf(`      - node: review-%d
        uses: code-review@%d
        input_mapping: {repository: repository}
        output_mapping: {review: %s}
        params: {MODE: strict}
`, index, nodeVersion, output)
		if index%2 == 1 {
			plan += "  - do:\n" + reference
		} else {
			plan += "  - in_parallel:\n      steps:\n" + reference
		}
	}
	plan += `  - agent: marker
    function_id: marker
    prompt: preserve arbitrary nested uses
    env:
      uses: code-review@4
`
	return workflow.Manifest{
		sourceFile: fmt.Sprintf(`schema_version: 3
name: %s
signature_version: 1
inputs:
  - {name: repository, type: repository/v1}
outputs:
%splan:
%s`, name, outputs, plan),
		"metadata/revision.txt": fmt.Sprintf("revision-%d", revision),
		"notes/uses.txt":        "code-review@4 is documentation, not a node reference",
	}
}

func ordinaryUpgradeWorkflowManifest(name string) workflow.Manifest {
	return workflow.Manifest{workflow.WorkflowFileName: fmt.Sprintf(`schema_version: 3
name: %s
signature_version: 1
inputs: []
outputs: []
plan:
  - agent: work
    function_id: work
    prompt: no reusable node
`, name)}
}

func TestNodeUpgradeCreatesSelectedImmutableRevisionsAndReplaysIdempotently(t *testing.T) {
	fixture := newNodeUpgradeFixture(t)
	beforeCalls := fixture.workflows.importCallCount()

	result, err := fixture.service.Upgrade(context.Background(), workflow.NodeUpgradeRequest{
		NodeName: "code-review",
		Version:  5,
		Workflows: []string{
			"version-upgrade",
			"small-fix",
		},
		CreatedBy: "alice",
	})
	if err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	want := []workflow.NodeUpgradeWorkflowResult{
		{Workflow: "small-fix", OldVersion: 7, NewVersion: 8, Status: workflow.NodeUpgradeCreated},
		{Workflow: "version-upgrade", OldVersion: 3, NewVersion: 4, Status: workflow.NodeUpgradeCreated},
	}
	if !reflect.DeepEqual(result.Workflows, want) {
		t.Fatalf("results = %#v, want %#v", result.Workflows, want)
	}
	if result.NodeName != "code-review" || result.Version != 5 {
		t.Fatalf("result target = %+v", result)
	}
	if got := fixture.workflows.importCallCount() - beforeCalls; got != 2 {
		t.Fatalf("imports = %d, want 2", got)
	}

	for _, test := range []struct {
		name            string
		liveVersion     int
		newVersion      int
		sourceFile      string
		referenceCount  int
		predecessorUses int
	}{
		{name: "small-fix", liveVersion: 7, newVersion: 8, sourceFile: workflow.WorkflowFileName, referenceCount: 2, predecessorUses: 3},
		{name: "version-upgrade", liveVersion: 3, newVersion: 4, sourceFile: workflow.LegacyWorkflowFileName, referenceCount: 1, predecessorUses: 2},
	} {
		live, found, err := fixture.workflows.Live(test.name)
		if err != nil || !found {
			t.Fatalf("live %s: found=%v err=%v", test.name, found, err)
		}
		if live.Version != test.liveVersion || strings.Count(live.SourceManifest[test.sourceFile], "code-review@4") != test.predecessorUses {
			t.Fatalf("live %s mutated: version=%d source=%q", test.name, live.Version, live.SourceManifest[test.sourceFile])
		}
		upgraded, found, err := fixture.workflows.Get(test.name, test.newVersion)
		if err != nil || !found {
			t.Fatalf("upgraded %s: found=%v err=%v", test.name, found, err)
		}
		if upgraded.Live || upgraded.CreatedBy != "alice" {
			t.Fatalf("upgraded definition promoted or wrong audit: %+v", upgraded)
		}
		if _, kept := upgraded.SourceManifest[test.sourceFile]; !kept {
			t.Fatalf("%s source filename changed: %#v", test.name, upgraded.SourceManifest)
		}
		if strings.Count(upgraded.SourceManifest[test.sourceFile], "code-review@5") != test.referenceCount ||
			strings.Count(upgraded.SourceManifest[test.sourceFile], "code-review@4") != 1 {
			t.Fatalf("unsafe or incomplete rewrite for %s: %q", test.name, upgraded.SourceManifest[test.sourceFile])
		}
		if upgraded.SourceManifest["notes/uses.txt"] != "code-review@4 is documentation, not a node reference" ||
			upgraded.SourceManifest["metadata/revision.txt"] != fmt.Sprintf("revision-%d", test.liveVersion) {
			t.Fatalf("non-source manifest files changed for %s: %#v", test.name, upgraded.SourceManifest)
		}
		bindings, err := fixture.nodes.Bindings(upgraded.ID)
		if err != nil || len(bindings) != test.referenceCount {
			t.Fatalf("upgraded bindings for %s = %#v err=%v", test.name, bindings, err)
		}
		for _, binding := range bindings {
			if binding.NodeVersion != 5 || binding.NodeContentHash == "" {
				t.Fatalf("binding did not target exact successor: %+v", binding)
			}
		}
	}

	unselectedLive, found, err := fixture.workflows.Live("dependency-audit")
	if err != nil || !found || unselectedLive.Version != 2 {
		t.Fatalf("unselected live = %+v found=%v err=%v", unselectedLive, found, err)
	}
	unselectedLatest, found, err := fixture.workflows.Latest("dependency-audit")
	if err != nil || !found || unselectedLatest.Version != 2 {
		t.Fatalf("unselected latest = %+v found=%v err=%v", unselectedLatest, found, err)
	}

	replayed, err := fixture.service.Upgrade(context.Background(), workflow.NodeUpgradeRequest{
		NodeName: "code-review",
		Version:  5,
		Workflows: []string{
			"small-fix",
			"version-upgrade",
		},
		CreatedBy: "mallory",
	})
	if err != nil {
		t.Fatalf("idempotent Upgrade: %v", err)
	}
	for index, item := range replayed.Workflows {
		if item.Status != workflow.NodeUpgradeUnchanged || item.NewVersion != want[index].NewVersion {
			t.Fatalf("replay result %d = %+v", index, item)
		}
	}
}

func TestNodeUpgradeRejectsDuplicateSelectionsBeforeMutation(t *testing.T) {
	fixture := newNodeUpgradeFixture(t)
	beforeCalls := fixture.workflows.importCallCount()

	_, err := fixture.service.Upgrade(context.Background(), workflow.NodeUpgradeRequest{
		NodeName: "code-review",
		Version:  5,
		Workflows: []string{
			"version-upgrade",
			"small-fix",
			"version-upgrade",
		},
		CreatedBy: "alice",
	})
	if err == nil || err.Error() != `workflow: duplicate workflow selection "version-upgrade"` {
		t.Fatalf("duplicate error = %v", err)
	}
	if fixture.workflows.importCallCount() != beforeCalls {
		t.Fatal("duplicate request imported a workflow")
	}
}

func TestNodeUpgradeReportsMissingLiveNoMatchAndImportFailureIndependently(t *testing.T) {
	fixture := newNodeUpgradeFixture(t)
	ordinary, err := fixture.workflows.ImportManifestWithOutcome(
		"ordinary",
		ordinaryUpgradeWorkflowManifest("ordinary"),
		"author",
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.workflows.Promote("ordinary", ordinary.Definition.Version, "promoter"); err != nil {
		t.Fatal(err)
	}
	fixture.workflows.importFailures["small-fix"] = errors.New("compile successor: rejected fixture")

	result, err := fixture.service.Upgrade(context.Background(), workflow.NodeUpgradeRequest{
		NodeName: "code-review",
		Version:  5,
		Workflows: []string{
			"version-upgrade",
			"ordinary",
			"missing",
			"small-fix",
		},
		CreatedBy: "alice",
	})
	if err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	if got := []string{
		result.Workflows[0].Workflow,
		result.Workflows[1].Workflow,
		result.Workflows[2].Workflow,
		result.Workflows[3].Workflow,
	}; !reflect.DeepEqual(got, []string{"missing", "ordinary", "small-fix", "version-upgrade"}) {
		t.Fatalf("result order = %v", got)
	}
	if result.Workflows[0].Status != workflow.NodeUpgradeFailed || !strings.Contains(result.Workflows[0].Error, "has no live revision") {
		t.Fatalf("missing result = %+v", result.Workflows[0])
	}
	if result.Workflows[1].Status != workflow.NodeUpgradeFailed || !strings.Contains(result.Workflows[1].Error, "does not reference predecessor") {
		t.Fatalf("no-match result = %+v", result.Workflows[1])
	}
	if result.Workflows[2].Status != workflow.NodeUpgradeFailed || !strings.Contains(result.Workflows[2].Error, "compile successor") {
		t.Fatalf("compile result = %+v", result.Workflows[2])
	}
	if result.Workflows[3].Status != workflow.NodeUpgradeCreated {
		t.Fatalf("independent success = %+v", result.Workflows[3])
	}
}

func TestNodeUpgradeRejectsStaleLiveBindingWithoutImport(t *testing.T) {
	fixture := newNodeUpgradeFixture(t)
	live, found, err := fixture.workflows.Live("small-fix")
	if err != nil || !found {
		t.Fatalf("Live: found=%v err=%v", found, err)
	}
	fixture.nodes.corruptBindingHash(live.ID, "review-1")
	beforeCalls := fixture.workflows.importCallCount()

	result, err := fixture.service.Upgrade(context.Background(), workflow.NodeUpgradeRequest{
		NodeName: "code-review",
		Version:  5,
		Workflows: []string{
			"small-fix",
		},
		CreatedBy: "alice",
	})
	if err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	if len(result.Workflows) != 1 || result.Workflows[0].Status != workflow.NodeUpgradeFailed ||
		!strings.Contains(result.Workflows[0].Error, "stale node bindings") {
		t.Fatalf("result = %+v", result)
	}
	if fixture.workflows.importCallCount() != beforeCalls {
		t.Fatal("stale binding request imported a workflow")
	}
}

func TestNodeUpgradeAllowsPreexistingSuccessorBindingAlongsideRewrittenPredecessor(t *testing.T) {
	fixture := newNodeUpgradeFixture(t)
	imported, err := fixture.workflows.ImportManifestWithOutcome(
		"mixed-consumer",
		mixedVersionUpgradeWorkflowManifest(),
		"author",
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.workflows.Promote("mixed-consumer", imported.Definition.Version, "promoter"); err != nil {
		t.Fatal(err)
	}

	result, err := fixture.service.Upgrade(context.Background(), workflow.NodeUpgradeRequest{
		NodeName:  "code-review",
		Version:   5,
		Workflows: []string{"mixed-consumer"},
		CreatedBy: "alice",
	})
	if err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	if len(result.Workflows) != 1 || result.Workflows[0] != (workflow.NodeUpgradeWorkflowResult{
		Workflow:   "mixed-consumer",
		OldVersion: 1,
		NewVersion: 2,
		Status:     workflow.NodeUpgradeCreated,
	}) {
		t.Fatalf("result = %+v", result)
	}
	upgraded, found, err := fixture.workflows.Get("mixed-consumer", 2)
	if err != nil || !found {
		t.Fatalf("upgraded definition: found=%v err=%v", found, err)
	}
	bindings, err := fixture.nodes.Bindings(upgraded.ID)
	if err != nil || len(bindings) != 2 {
		t.Fatalf("bindings = %+v err=%v", bindings, err)
	}
	for _, binding := range bindings {
		if binding.NodeVersion != 5 {
			t.Fatalf("mixed binding was not retained/upgraded: %+v", binding)
		}
	}
}

func TestNodeUpgradeDetectsStaleEmptyValuedBindingKey(t *testing.T) {
	fixture := newNodeUpgradeFixture(t)
	imported, err := fixture.workflows.ImportManifestWithOutcome(
		"empty-parameter-consumer",
		emptyParameterUpgradeWorkflowManifest(),
		"author",
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.workflows.Promote("empty-parameter-consumer", imported.Definition.Version, "promoter"); err != nil {
		t.Fatal(err)
	}
	fixture.nodes.replaceBindingParameters(imported.Definition.ID, "review", map[string]string{"OTHER": ""})
	beforeCalls := fixture.workflows.importCallCount()

	result, err := fixture.service.Upgrade(context.Background(), workflow.NodeUpgradeRequest{
		NodeName:  "code-review",
		Version:   5,
		Workflows: []string{"empty-parameter-consumer"},
		CreatedBy: "alice",
	})
	if err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	if len(result.Workflows) != 1 || result.Workflows[0].Status != workflow.NodeUpgradeFailed ||
		!strings.Contains(result.Workflows[0].Error, "stale node bindings") {
		t.Fatalf("result = %+v", result)
	}
	if fixture.workflows.importCallCount() != beforeCalls {
		t.Fatal("stale binding request imported a workflow")
	}
}

func TestNodeUpgradeFailsClosedWhenImportedBindingsDoNotMatchSuccessorContent(t *testing.T) {
	fixture := newNodeUpgradeFixture(t)
	fixture.workflows.corruptImports["version-upgrade"] = true

	result, err := fixture.service.Upgrade(context.Background(), workflow.NodeUpgradeRequest{
		NodeName:  "code-review",
		Version:   5,
		Workflows: []string{"version-upgrade"},
		CreatedBy: "alice",
	})
	if err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	if len(result.Workflows) != 1 || result.Workflows[0].Status != workflow.NodeUpgradeFailed ||
		result.Workflows[0].NewVersion != 4 ||
		!strings.Contains(result.Workflows[0].Error, "did not bind exact successor content") {
		t.Fatalf("result = %+v", result)
	}
}

func mixedVersionUpgradeWorkflowManifest() workflow.Manifest {
	return workflow.Manifest{workflow.WorkflowFileName: `schema_version: 3
name: mixed-consumer
signature_version: 1
inputs:
  - {name: repository, type: repository/v1}
outputs:
  - {name: old-review, type: review/v1, from: old-review}
  - {name: new-review, type: review/v1, from: new-review}
plan:
  - node: old-review
    uses: code-review@4
    input_mapping: {repository: repository}
    output_mapping: {review: old-review}
    params: {MODE: strict}
  - node: new-review
    uses: code-review@5
    input_mapping: {repository: repository}
    output_mapping: {review: new-review}
    params: {MODE: strict}
`}
}

func emptyParameterUpgradeWorkflowManifest() workflow.Manifest {
	return workflow.Manifest{workflow.WorkflowFileName: `schema_version: 3
name: empty-parameter-consumer
signature_version: 1
inputs:
  - {name: repository, type: repository/v1}
outputs:
  - {name: review, type: review/v1, from: review}
plan:
  - node: review
    uses: code-review@4
    input_mapping: {repository: repository}
    output_mapping: {review: review}
    params: {MODE: ""}
`}
}

func TestNodeUpgradeReportsMissingReleasePredecessorPerWorkflow(t *testing.T) {
	fixture := newNodeUpgradeFixture(t)
	successor, found, err := fixture.nodes.Get("code-review", 5)
	if err != nil || !found {
		t.Fatalf("Get successor: found=%v err=%v", found, err)
	}
	successor.Release.PredecessorVersion = 0
	nodes := &releasedOverrideNodeStore{
		NodeStore: fixture.nodes,
		target:    *successor,
	}
	service := workflow.NewNodeUpgradeService(nodes, fixture.workflows)

	result, err := service.Upgrade(context.Background(), workflow.NodeUpgradeRequest{
		NodeName: "code-review",
		Version:  5,
		Workflows: []string{
			"version-upgrade",
			"small-fix",
		},
		CreatedBy: "alice",
	})
	if err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	for _, item := range result.Workflows {
		if item.Status != workflow.NodeUpgradeFailed || !strings.Contains(item.Error, "has no released predecessor") {
			t.Fatalf("missing predecessor result = %+v", item)
		}
	}
}

type releasedOverrideNodeStore struct {
	workflow.NodeStore
	target workflow.NodeDefinition
}

func (store *releasedOverrideNodeStore) Released(name string, version int) (workflow.NodeDefinition, bool, error) {
	if name == store.target.Name && version == store.target.Version {
		return store.target, true, nil
	}
	return store.NodeStore.Released(name, version)
}

func TestNodeUpgradeBreakingReleaseReturnsDeterministicRecompositionObligationsWithoutImport(t *testing.T) {
	nodes := newUpgradeNodeStore()
	predecessor, err := nodes.ImportManifest("contract-node", breakingPredecessorManifest(), "author")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nodes.Release("contract-node", predecessor.Version, workflow.ReleaseCompatible, "releaser"); err != nil {
		t.Fatal(err)
	}
	successor, err := nodes.ImportManifest("contract-node", breakingSuccessorManifest(), "author")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nodes.Release("contract-node", successor.Version, workflow.ReleaseBreaking, "releaser"); err != nil {
		t.Fatal(err)
	}
	memory := workflowtest.NewMemoryStoreWithNodeResolver(nodes, upgradePromotionValidator{})
	workflows := &bindingWorkflowStore{
		Store:          memory,
		nodes:          nodes,
		importFailures: map[string]error{},
		corruptImports: map[string]bool{},
	}
	source := breakingConsumerManifest()
	imported, err := workflows.ImportManifestWithOutcome("breaking-consumer", source, "author")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workflows.Promote("breaking-consumer", imported.Definition.Version, "promoter"); err != nil {
		t.Fatal(err)
	}
	beforeCalls := workflows.importCallCount()

	result, err := workflow.NewNodeUpgradeService(nodes, workflows).Upgrade(
		context.Background(),
		workflow.NodeUpgradeRequest{
			NodeName:  "contract-node",
			Version:   successor.Version,
			Workflows: []string{"breaking-consumer"},
			CreatedBy: "alice",
		},
	)
	if err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	if workflows.importCallCount() != beforeCalls {
		t.Fatal("breaking release imported a workflow")
	}
	if len(result.Workflows) != 1 {
		t.Fatalf("results = %+v", result)
	}
	item := result.Workflows[0]
	if item.Status != workflow.NodeUpgradeRecompositionRequired || item.OldVersion != 1 ||
		item.NewVersion != 0 || item.Error != "" || item.Obligations == nil {
		t.Fatalf("breaking result = %+v", item)
	}
	want := &workflow.NodeUpgradeObligations{
		Inputs: workflow.NodeContractChanges{
			Added:   []string{"policy"},
			Removed: []string{"obsolete"},
			Changed: []string{"repository"},
		},
		Outputs: workflow.NodeContractChanges{
			Added:   []string{"summary"},
			Removed: []string{"legacy"},
			Changed: []string{"review"},
		},
		Parameters: workflow.NodeContractChanges{
			Added:   []string{"STRICT"},
			Removed: []string{"LEGACY"},
			Changed: []string{"MODE"},
		},
	}
	if !reflect.DeepEqual(item.Obligations, want) {
		t.Fatalf("obligations = %#v, want %#v", item.Obligations, want)
	}
	live, found, err := workflows.Live("breaking-consumer")
	if err != nil || !found || live.Version != 1 || live.SourceManifest.Hash() != source.Hash() {
		t.Fatalf("breaking request mutated live: %+v found=%v err=%v", live, found, err)
	}
}

func breakingPredecessorManifest() workflow.Manifest {
	return workflow.Manifest{workflow.NodeFileName: `schema_version: 1
name: contract-node
inputs:
  - {name: repository, type: repository/v1}
  - {name: obsolete, type: repository/v1, optional: true}
outputs:
  - {name: review, type: review/v1}
  - {name: legacy, type: review/v1, optional: true}
parameters:
  - {name: MODE, default: standard}
  - {name: LEGACY, default: retained}
step:
  agent: review
  prompt: predecessor
`}
}

func breakingSuccessorManifest() workflow.Manifest {
	return workflow.Manifest{workflow.NodeFileName: `schema_version: 1
name: contract-node
inputs:
  - {name: repository, type: repository/v2}
  - {name: policy, type: policy/v1}
outputs:
  - {name: review, type: review/v2}
  - {name: summary, type: summary/v1}
parameters:
  - {name: MODE}
  - {name: STRICT}
step:
  agent: review
  prompt: successor
`}
}

func breakingConsumerManifest() workflow.Manifest {
	return workflow.Manifest{workflow.WorkflowFileName: `schema_version: 3
name: breaking-consumer
signature_version: 1
inputs:
  - {name: repository, type: repository/v1}
  - {name: old, type: repository/v1, optional: true}
outputs:
  - {name: review, type: review/v1, from: review}
  - {name: legacy, type: review/v1, from: legacy, optional: true}
plan:
  - node: review
    uses: contract-node@1
    input_mapping: {repository: repository, obsolete: old}
    output_mapping: {review: review, legacy: legacy}
    params: {MODE: strict, LEGACY: retained}
`}
}
