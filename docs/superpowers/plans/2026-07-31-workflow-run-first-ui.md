# Workflow-run-first UI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the current definition-management workflow page with a workflow-run-first UI — a promoted-revision DAG coupled to a chronological run list, an exact per-run DAG, and an optional cross-workflow ticket journal — backed by a durable node-occurrence read model that survives Concourse build GC.

**Architecture:** One graph model, one occurrence model, three views. `agent/workflow/graph` derives a redacted semantic DAG from any compiled v3 definition by mirroring the producer-to-consumer walk that `agent/workflow/typecheck.go` already performs. `agent/workflowrun/occurrence` derives per-node occurrence state from authoritative rows and is called twice: live (for current attention) and at run finalization (to freeze durable history before build GC). Elm renders both the aggregate and the exact graph through one `AgentGraph.View` parameterised by a node-state lookup.

**Tech Stack:** Go 1.x with plain `testing` (the `agent/` tree is not Ginkgo), PostgreSQL migrations as embedded `.up.sql`/`.down.sql` files, Elm 0.19 with `elm-explorations/test` run via `yarn test`, Ginkgo for `atc/db` specs.

**Design source:** `docs/superpowers/specs/2026-07-31-workflow-run-first-ui-design.md`

**Base:** `origin/jetbridge` at `7008b0fec0`. Head migration is `1773106156`; this plan adds `1773106157` and `1773106158`.

---

## Phase dependency

```
A ──▶ B ──▶ C ──▶ D ──▶ E
F (independent — may run in parallel or first)
```

Phase F touches only `agent_workflow_runs` ticket columns, `agent_tickets`, and admission paths. It shares no files with A–E.

---

## File Structure

### Phase A — identity and semantic graph (pure Go, no schema)

| File | Responsibility |
|---|---|
| Create `agent/workflow/graph/graph.go` | `Graph`, `Node`, `Edge`, `NodeKind`, `Decoration` types |
| Create `agent/workflow/graph/build.go` | `Build(*workflow.FunctionConfig) (Graph, error)` |
| Create `agent/workflow/graph/build_test.go` | unit tests for each node kind and wrapper |
| Create `agent/workflow/graph/seeds_test.go` | golden-graph tests over all nine seeds |
| Create `agent/workflow/graph/testdata/*.json` | golden graphs |
| Modify `agent/workflow/typecheck.go` | register await/publish/resource-source/port names in the identity namespace |

### Phase B — node occurrences

| File | Responsibility |
|---|---|
| Create `agent/workflowrun/occurrence/occurrence.go` | `NodeOccurrence`, `Status`, `Sources` types |
| Create `agent/workflowrun/occurrence/derive.go` | `Derive(Sources) []NodeOccurrence` |
| Create `agent/workflowrun/occurrence/attention.go` | retry-chain effective-state resolution |
| Create `agent/workflowrun/occurrence/*_test.go` | table tests per node kind |
| Create `atc/db/migration/migrations/1773106157_create_workflow_run_node_occurrences.{up,down}.sql` | projection table |
| Create `atc/db/agent_workflow_run_node_occurrences_factory.go` | freeze and read |
| Create `atc/db/agent_workflow_run_node_occurrences_factory_test.go` | Ginkgo DB specs |
| Modify `agent/workflowrun/reconciler.go` | call freeze at finalization |

### Phase C — API

| File | Responsibility |
|---|---|
| Create `agent/api/workflowoverview/handler.go` | overview endpoint |
| Create `agent/api/workflowoverview/types.go` | response types |
| Create `agent/api/workflowoverview/handler_test.go` | window and scope semantics |
| Modify `atc/routes.go` | register `GetAgentWorkflowOverview`, `GetAgentWorkflowRunGraph`, `ListAgentTicketRuns` |
| Modify `atc/api/handler.go` | wire the overview handler |
| Modify `atc/db/agent_workflow_run.go` | extend `AgentWorkflowRunListFilter` |
| Modify `atc/db/agent_workflow_runs_factory.go` | implement new filters |
| Modify `agent/api/workflowruns/handler.go` | parse new query params |

### Phase D — overview UI

| File | Responsibility |
|---|---|
| Create `web/elm/src/AgentGraph/Model.elm` | graph data types |
| Create `web/elm/src/AgentGraph/Decoder.elm` | JSON decoding |
| Create `web/elm/src/AgentGraph/Layout.elm` | pure `Graph -> LaidOut` |
| Create `web/elm/src/AgentGraph/View.elm` | SVG parameterised by `NodeState` |
| Create `web/elm/src/AgentWorkflow/Filters.elm` | filter state to and from URL |
| Create `web/elm/src/AgentWorkflow/RunList.elm` | coupled run list |
| Create `web/elm/src/AgentWorkflow/Panels.elm` | Start and Versions overlays |
| Create `web/elm/tests/AgentGraphLayoutTests.elm` | fuzz tests |
| Create `web/elm/tests/AgentGraphViewTests.elm` | state-language tests |
| Modify `web/elm/src/AgentWorkflow/AgentWorkflow.elm` | recompose page |
| Modify `web/elm/src/Api/Endpoints.elm`, `Message/Effects.elm`, `Message/Callback.elm`, `Message/Message.elm`, `Routes.elm` | new endpoint, effect, callback, messages, query state |

### Phase E — exact run DAG

| File | Responsibility |
|---|---|
| Create `agent/api/workflowruns/graph.go` | run graph endpoint |
| Create `web/elm/src/AgentWorkflowRun/NodeDetail.elm` | selected-node durable detail |
| Modify `web/elm/src/AgentWorkflowRun/AgentWorkflowRun.elm` | recompose around the DAG |

### Phase F — ticket association and journal

| File | Responsibility |
|---|---|
| Create `atc/db/migration/migrations/1773106158_associate_runs_with_tickets.{up,down}.sql` | ticket columns, backfill, drop old |
| Create `web/elm/src/AgentTicket/Journal.elm` | chronological journal |
| Modify `agent/workflowrun/types.go`, `binder.go` | carry association through admission |
| Modify `agent/api/tickets/handler.go` | journal endpoint |

---

## Phase A — identity and semantic graph

No schema, no user-visible change. Unblocks every other phase.

### Task A1: Register await, publish, and load names in the identity namespace

Today `checkLeaf` registers `function_id` for agent and task nodes into `checker.functionIDs`, but `await_snapshot`, `publish_snapshot`, and `load_snapshot` names are never registered. An agent's `function_id` can therefore silently collide with an await name, which would make two graph nodes share one identity.

**Files:**
- Modify: `agent/workflow/typecheck.go`
- Test: `agent/workflow/typecheck_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/workflow/typecheck_test.go`:

```go
func TestTypeCheckRejectsFunctionIDCollidingWithAwaitName(t *testing.T) {
	agent := typedAgent("ask", "approval", []string{"repo"}, map[string]atc.SnapshotInputConfig{
		"repo": {Type: repositoryV1},
	}, []string{"question"}, map[string]atc.SnapshotOutputConfig{
		"question": {Type: questionV1},
	})

	function := &FunctionConfig{
		SignatureVersion: 1,
		Inputs:           []snapshot.Port{{Name: "repo", Type: repositoryV1}},
		Outputs: []FunctionOutput{
			{Port: snapshot.Port{Name: "answer", Type: humanAnswerV1}, From: "approval"},
		},
		Plan: []atc.Step{
			agent,
			{Config: &atc.AwaitSnapshotStep{
				Name:      "approval",
				Question:  "question",
				Type:      humanAnswerV1,
				OnTimeout: atc.AwaitSnapshotOnTimeoutFail,
			}},
		},
	}

	err := TypeCheckFunction(function)
	if err == nil {
		t.Fatal("expected a duplicate-identity error, got nil")
	}
	if !strings.Contains(err.Error(), "duplicate") || !strings.Contains(err.Error(), "approval") {
		t.Fatalf("expected a duplicate identity error naming %q, got: %v", "approval", err)
	}
}
```

- [ ] **Step 2: Run the test and confirm it fails**

```bash
go test ./agent/workflow/ -run TestTypeCheckRejectsFunctionIDCollidingWithAwaitName -v
```

Expected: FAIL — `expected a duplicate-identity error, got nil`. The await name is currently unregistered, so the collision passes type-checking.

- [ ] **Step 3: Add a shared registration helper**

In `agent/workflow/typecheck.go`, add above `checkLeaf`:

```go
// registerNodeIdentity records one workflow-local node identity. Every
// semantic node kind shares a single namespace: agent and task nodes use their
// authored function_id, while await_snapshot, publish_snapshot, and
// load_snapshot use their binding name, which downstream steps already
// reference by value. Two nodes may never share an identity, because the
// durable node-occurrence projection is keyed by it.
func (checker *snapshotFlowChecker) registerNodeIdentity(nodeID, identity string) error {
	if strings.TrimSpace(nodeID) == "" {
		return fmt.Errorf("workflow: %s: node identity must be nonblank", identity)
	}
	if previous, found := checker.functionIDs[nodeID]; found {
		return fmt.Errorf("workflow: %s: duplicate node identity %q; first declared at %s", identity, nodeID, previous)
	}
	checker.functionIDs[nodeID] = identity
	return nil
}
```

- [ ] **Step 4: Route `checkLeaf` through the helper**

In `checkLeaf`, replace the existing blank check and duplicate check:

```go
	identity := fmt.Sprintf("%s.%s(%q)", path, kind, displayName)
	if strings.TrimSpace(functionID) == "" {
		return snapshotFlow{}, fmt.Errorf("workflow: %s: typed node requires a nonblank authored function_id", identity)
	}
	if err := checker.registerNodeIdentity(functionID, identity); err != nil {
		return snapshotFlow{}, err
	}
```

- [ ] **Step 5: Register await, publish, and load names**

In `checkAwaitSnapshot`, immediately after the existing `identity := fmt.Sprintf(...)` line:

```go
	if err := checker.registerNodeIdentity(step.Name, identity); err != nil {
		return snapshotFlow{}, err
	}
```

Add the identical two-line block after the `identity :=` line in `checkPublishSnapshot` and in `checkLoadSnapshot`.

- [ ] **Step 6: Run the new test and the whole package**

```bash
go test ./agent/workflow/ -run TestTypeCheckRejectsFunctionIDCollidingWithAwaitName -v
```

Expected: PASS.

```bash
go test ./agent/workflow/ -count=1
```

Expected: PASS. If an existing test now fails with a duplicate-identity error, that test's fixture has a genuine collision — rename the colliding await or publish binding in the fixture rather than weakening the check.

- [ ] **Step 7: Commit**

```bash
git add agent/workflow/typecheck.go agent/workflow/typecheck_test.go
git commit -m "feat(workflow): register every semantic node in one identity namespace"
```

### Task A2: Graph types

**Files:**
- Create: `agent/workflow/graph/graph.go`
- Test: `agent/workflow/graph/graph_test.go`

- [ ] **Step 1: Write the failing test**

Create `agent/workflow/graph/graph_test.go`:

```go
package graph

import "testing"

func TestNodeKindValidation(t *testing.T) {
	valid := []NodeKind{KindInput, KindResourceSource, KindLoad, KindAgent, KindTask, KindAwait, KindPublish, KindOutput}
	for _, kind := range valid {
		if err := kind.Validate(); err != nil {
			t.Fatalf("expected %q to be valid, got: %v", kind, err)
		}
	}
	if err := NodeKind("prompt").Validate(); err == nil {
		t.Fatal("expected an unknown node kind to be rejected")
	}
}

func TestGraphLookupByID(t *testing.T) {
	g := Graph{Nodes: []Node{{ID: "implement", Kind: KindAgent, DisplayName: "implement"}}}
	node, found := g.Node("implement")
	if !found || node.Kind != KindAgent {
		t.Fatalf("expected to find the agent node, got found=%v node=%+v", found, node)
	}
	if _, found := g.Node("absent"); found {
		t.Fatal("expected absent node lookup to report not found")
	}
}
```

- [ ] **Step 2: Run the test and confirm it fails**

```bash
go test ./agent/workflow/graph/ -count=1
```

Expected: FAIL — the package does not exist.

- [ ] **Step 3: Write the types**

Create `agent/workflow/graph/graph.go`:

```go
// Package graph derives the redacted semantic DAG of a compiled v3 workflow
// function. It is the single graph model shared by the workflow overview, the
// individual run page, and any later consumer.
//
// Redaction is structural: prompts, task configs, and broker profiles have no
// fields in these types, so a call site cannot forget to redact them.
package graph

import "fmt"

type NodeKind string

const (
	KindInput          NodeKind = "input"
	KindResourceSource NodeKind = "resource_source"
	KindLoad           NodeKind = "load"
	KindAgent          NodeKind = "agent"
	KindTask           NodeKind = "task"
	KindAwait          NodeKind = "await"
	KindPublish        NodeKind = "publish"
	KindOutput         NodeKind = "output"
)

func (kind NodeKind) Validate() error {
	switch kind {
	case KindInput, KindResourceSource, KindLoad, KindAgent, KindTask, KindAwait, KindPublish, KindOutput:
		return nil
	default:
		return fmt.Errorf("graph: unknown node kind %q", kind)
	}
}

// Decoration is a control-machinery wrapper that affects a node without
// becoming a node of its own.
type Decoration string

const (
	DecorationRetry     Decoration = "retry"
	DecorationTimeout   Decoration = "timeout"
	DecorationTry       Decoration = "try"
	DecorationEnsure    Decoration = "ensure"
	DecorationOnFailure Decoration = "on_failure"
	DecorationOnError   Decoration = "on_error"
	DecorationOnAbort   Decoration = "on_abort"
	DecorationOnSuccess Decoration = "on_success"
)

// Node is one semantic workflow element. ID is the stable workflow-local
// identity: the authored function_id for agent and task nodes, and the
// contract-bearing binding or port name for every other kind.
type Node struct {
	ID          string       `json:"id"`
	Kind        NodeKind     `json:"kind"`
	DisplayName string       `json:"display_name"`
	TypeRef     string       `json:"type_ref,omitempty"`
	Optional    bool         `json:"optional,omitempty"`
	Decorations []Decoration `json:"decorations,omitempty"`

	// Set only when the node is a reusable-node binding.
	ReusableNodeName    string `json:"reusable_node_name,omitempty"`
	ReusableNodeVersion int    `json:"reusable_node_version,omitempty"`
}

// Edge runs from a producing node to a consuming node, labelled with the
// snapshot binding that connects them.
type Edge struct {
	From     string `json:"from"`
	To       string `json:"to"`
	PortName string `json:"port_name"`
	TypeRef  string `json:"type_ref,omitempty"`
}

type Graph struct {
	Nodes []Node `json:"nodes"`
	Edges []Edge `json:"edges"`
}

func (g Graph) Node(id string) (Node, bool) {
	for _, node := range g.Nodes {
		if node.ID == id {
			return node, true
		}
	}
	return Node{}, false
}
```

- [ ] **Step 4: Run the test and confirm it passes**

```bash
go test ./agent/workflow/graph/ -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/workflow/graph/
git commit -m "feat(graph): semantic workflow graph types"
```

### Task A3: Build the graph for agent and task nodes

**Files:**
- Create: `agent/workflow/graph/build.go`
- Test: `agent/workflow/graph/build_test.go`

- [ ] **Step 1: Write the failing test**

Create `agent/workflow/graph/build_test.go`:

```go
package graph

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc"
)

func agentStep(name, functionID string, inputs, outputs []string, inTypes map[string]atc.SnapshotInputConfig, outTypes map[string]atc.SnapshotOutputConfig) atc.Step {
	return atc.Step{Config: &atc.AgentStep{
		Name:            name,
		FunctionID:      functionID,
		Inputs:          inputs,
		Outputs:         outputs,
		SnapshotInputs:  inTypes,
		SnapshotOutputs: outTypes,
	}}
}

func TestBuildLinksProducerToConsumer(t *testing.T) {
	function := &workflow.FunctionConfig{
		SignatureVersion: 1,
		Inputs:           []snapshot.Port{{Name: "repository", Type: "repository/v1"}},
		Outputs: []workflow.FunctionOutput{
			{Port: snapshot.Port{Name: "change", Type: "repository-change/v1"}, From: "candidate"},
		},
		Plan: []atc.Step{
			agentStep("implement", "implement",
				[]string{"repository"}, []string{"draft"},
				map[string]atc.SnapshotInputConfig{"repository": {Type: "repository/v1"}},
				map[string]atc.SnapshotOutputConfig{"draft": {Type: "repository-change/v1"}}),
			agentStep("review", "review",
				[]string{"draft"}, []string{"candidate"},
				map[string]atc.SnapshotInputConfig{"draft": {Type: "repository-change/v1"}},
				map[string]atc.SnapshotOutputConfig{"candidate": {Type: "repository-change/v1"}}),
		},
	}

	g, err := Build(function)
	if err != nil {
		t.Fatalf("Build returned an error: %v", err)
	}

	if _, found := g.Node("implement"); !found {
		t.Fatal("expected an implement node")
	}
	if _, found := g.Node("repository"); !found {
		t.Fatal("expected a repository input node")
	}
	if _, found := g.Node("change"); !found {
		t.Fatal("expected a change output node")
	}

	want := []Edge{
		{From: "repository", To: "implement", PortName: "repository", TypeRef: "repository/v1"},
		{From: "implement", To: "review", PortName: "draft", TypeRef: "repository-change/v1"},
		{From: "review", To: "change", PortName: "candidate", TypeRef: "repository-change/v1"},
	}
	for _, edge := range want {
		if !hasEdge(g, edge) {
			t.Fatalf("expected edge %+v in %+v", edge, g.Edges)
		}
	}
}

func TestBuildRedactsPrompts(t *testing.T) {
	function := &workflow.FunctionConfig{
		SignatureVersion: 1,
		Plan: []atc.Step{
			func() atc.Step {
				step := agentStep("implement", "implement", nil, []string{"draft"}, nil,
					map[string]atc.SnapshotOutputConfig{"draft": {Type: "repository-change/v1"}})
				step.Config.(*atc.AgentStep).Prompt = "SECRET PROMPT"
				return step
			}(),
		},
	}

	g, err := Build(function)
	if err != nil {
		t.Fatalf("Build returned an error: %v", err)
	}
	node, found := g.Node("implement")
	if !found || node.DisplayName != "implement" {
		t.Fatalf("expected the agent node, got found=%v node=%+v", found, node)
	}

	// Redaction is structural: Node has no prompt field, so the secret cannot
	// survive serialization. Assert against the wire form, which is what a
	// browser would actually receive.
	encoded, err := json.Marshal(g)
	if err != nil {
		t.Fatalf("marshalling graph: %v", err)
	}
	if strings.Contains(string(encoded), "SECRET PROMPT") {
		t.Fatalf("the graph must never carry prompt text: %s", encoded)
	}
}

func hasEdge(g Graph, want Edge) bool {
	for _, edge := range g.Edges {
		if edge == want {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Run the test and confirm it fails**

```bash
go test ./agent/workflow/graph/ -run TestBuild -count=1
```

Expected: FAIL — `undefined: Build`.

- [ ] **Step 3: Write the builder**

Create `agent/workflow/graph/build.go`:

```go
package graph

import (
	"fmt"
	"sort"

	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc"
)

// Build derives the semantic DAG of a compiled function. It mirrors the step
// dispatch in agent/workflow/typecheck.go so that wrapped and conditional
// steps stay consistent with the type checker, but records nodes and edges
// instead of validating flow.
//
// Build assumes the function already type-checked. Step kinds the type checker
// rejects are treated as programmer error.
func Build(function *workflow.FunctionConfig) (Graph, error) {
	if function == nil {
		return Graph{}, fmt.Errorf("graph: function config is required")
	}

	builder := &builder{
		producers: map[string]string{},
		types:     map[string]string{},
		seen:      map[string]bool{},
	}

	for _, port := range function.Inputs {
		builder.addNode(Node{
			ID:          port.Name,
			Kind:        KindInput,
			DisplayName: port.Name,
			TypeRef:     string(port.Type),
			Optional:    port.Optional,
		})
		builder.producers[port.Name] = port.Name
		builder.types[port.Name] = string(port.Type)
	}

	for _, source := range function.ResourceSources {
		builder.addNode(Node{
			ID:          source.Name,
			Kind:        KindResourceSource,
			DisplayName: source.Name,
			TypeRef:     string(source.Type),
		})
		builder.producers[source.Name] = source.Name
		builder.types[source.Name] = string(source.Type)
	}

	if err := builder.walkSequence(function.Plan, nil); err != nil {
		return Graph{}, err
	}

	for _, output := range function.Outputs {
		builder.addNode(Node{
			ID:          output.Port.Name,
			Kind:        KindOutput,
			DisplayName: output.Port.Name,
			TypeRef:     string(output.Port.Type),
			Optional:    output.Port.Optional,
		})
		builder.link(output.From, output.Port.Name)
	}

	sort.SliceStable(builder.graph.Edges, func(i, j int) bool {
		left, right := builder.graph.Edges[i], builder.graph.Edges[j]
		if left.From != right.From {
			return left.From < right.From
		}
		if left.To != right.To {
			return left.To < right.To
		}
		return left.PortName < right.PortName
	})

	return builder.graph, nil
}

type builder struct {
	graph Graph
	// producers maps a snapshot binding name to the node ID that most recently
	// produced it.
	producers map[string]string
	types     map[string]string
	seen      map[string]bool
}

func (b *builder) addNode(node Node) {
	if b.seen[node.ID] {
		return
	}
	b.seen[node.ID] = true
	b.graph.Nodes = append(b.graph.Nodes, node)
}

// link records an edge from whichever node produced bindingName into nodeID.
// An unknown binding produces no edge: the type checker has already proven
// every consumed binding has a producer, so this can only be a workflow port
// consumed directly.
func (b *builder) link(bindingName, nodeID string) {
	producer, found := b.producers[bindingName]
	if !found || producer == nodeID {
		return
	}
	b.graph.Edges = append(b.graph.Edges, Edge{
		From:     producer,
		To:       nodeID,
		PortName: bindingName,
		TypeRef:  b.types[bindingName],
	})
}

func (b *builder) produce(bindingName, nodeID, typeRef string) {
	b.producers[bindingName] = nodeID
	if typeRef != "" {
		b.types[bindingName] = typeRef
	}
}

func (b *builder) walkSequence(steps []atc.Step, decorations []Decoration) error {
	for index := range steps {
		if err := b.walkStep(steps[index], decorations); err != nil {
			return err
		}
	}
	return nil
}

func (b *builder) walkStep(step atc.Step, decorations []Decoration) error {
	return b.walkStepConfig(step.Config, decorations)
}

// walkStepConfig is the StepConfig-typed entry point. atc's wrapper steps are
// not uniform: TimeoutStep.Step, RetryStep.Step, and every hook's .Step are
// StepConfig, while TryStep.Step, DoStep.Steps, and each hook's .Hook are Step.
// agent/workflow/typecheck.go splits checkStep/checkWrapped for exactly this
// reason; mirroring the split keeps both traversals the same shape.
func (b *builder) walkStepConfig(stepConfig atc.StepConfig, decorations []Decoration) error {
	if stepConfig == nil {
		return fmt.Errorf("graph: step config is required")
	}

	switch config := stepConfig.(type) {
	case *atc.AgentStep:
		b.addLeaf(config.FunctionID, KindAgent, config.Name, decorations,
			config.Inputs, config.Outputs, snapshotOutputTypes(config.SnapshotOutputs))
		return nil
	case *atc.TaskStep:
		inputs, outputs := taskArtifactNames(config)
		b.addLeaf(config.FunctionID, KindTask, config.Name, decorations,
			inputs, outputs, snapshotOutputTypes(config.SnapshotOutputs))
		return nil
	default:
		return fmt.Errorf("graph: unsupported step config %T", config)
	}
}

func (b *builder) addLeaf(nodeID string, kind NodeKind, displayName string, decorations []Decoration, inputs, outputs []string, outputTypes map[string]string) {
	b.addNode(Node{
		ID:          nodeID,
		Kind:        kind,
		DisplayName: displayName,
		Decorations: append([]Decoration(nil), decorations...),
	})
	for _, name := range inputs {
		b.link(name, nodeID)
	}
	for _, name := range outputs {
		b.produce(name, nodeID, outputTypes[name])
	}
}

func snapshotOutputTypes(outputs map[string]atc.SnapshotOutputConfig) map[string]string {
	result := make(map[string]string, len(outputs))
	for name, config := range outputs {
		result[name] = string(config.Type)
	}
	return result
}

func taskArtifactNames(step *atc.TaskStep) ([]string, []string) {
	inputs := make([]string, 0, len(step.SnapshotInputs))
	for name := range step.SnapshotInputs {
		inputs = append(inputs, name)
	}
	sort.Strings(inputs)

	outputs := make([]string, 0, len(step.SnapshotOutputs))
	for name := range step.SnapshotOutputs {
		outputs = append(outputs, name)
	}
	sort.Strings(outputs)

	return inputs, outputs
}
```

- [ ] **Step 4: Run the tests and confirm they pass**

```bash
go test ./agent/workflow/graph/ -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/workflow/graph/
git commit -m "feat(graph): derive producer-to-consumer edges for agent and task nodes"
```

### Task A4: Build await, publish, and load nodes

**Files:**
- Modify: `agent/workflow/graph/build.go`
- Test: `agent/workflow/graph/build_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/workflow/graph/build_test.go`:

```go
func TestBuildAwaitPublishAndLoadNodes(t *testing.T) {
	function := &workflow.FunctionConfig{
		SignatureVersion: 1,
		Inputs:           []snapshot.Port{{Name: "repository", Type: "repository/v1"}},
		Plan: []atc.Step{
			agentStep("prepare", "prepare",
				[]string{"repository"}, []string{"approval-question"},
				map[string]atc.SnapshotInputConfig{"repository": {Type: "repository/v1"}},
				map[string]atc.SnapshotOutputConfig{"approval-question": {Type: "question/v1"}}),
			{Config: &atc.AwaitSnapshotStep{
				Name:      "approval",
				Question:  "approval-question",
				Type:      "human-answer/v1",
				OnTimeout: atc.AwaitSnapshotOnTimeoutFail,
			}},
			{Config: &atc.PublishSnapshotStep{
				Name:      "ship",
				Input:     "repository",
				InputType: "repository/v1",
				Approval:  "approval",
			}},
		},
	}

	g, err := Build(function)
	if err != nil {
		t.Fatalf("Build returned an error: %v", err)
	}

	await, found := g.Node("approval")
	if !found || await.Kind != KindAwait {
		t.Fatalf("expected an await node, got found=%v node=%+v", found, await)
	}
	if await.TypeRef != "human-answer/v1" {
		t.Fatalf("expected the await node to carry its answer type, got %q", await.TypeRef)
	}

	publish, found := g.Node("ship")
	if !found || publish.Kind != KindPublish {
		t.Fatalf("expected a publish node, got found=%v node=%+v", found, publish)
	}

	want := []Edge{
		{From: "prepare", To: "approval", PortName: "approval-question", TypeRef: "question/v1"},
		{From: "repository", To: "ship", PortName: "repository", TypeRef: "repository/v1"},
		{From: "approval", To: "ship", PortName: "approval", TypeRef: "human-answer/v1"},
	}
	for _, edge := range want {
		if !hasEdge(g, edge) {
			t.Fatalf("expected edge %+v in %+v", edge, g.Edges)
		}
	}
}
```

- [ ] **Step 2: Run the test and confirm it fails**

```bash
go test ./agent/workflow/graph/ -run TestBuildAwaitPublishAndLoadNodes -count=1
```

Expected: FAIL — `graph: unsupported step config *atc.AwaitSnapshotStep`.

- [ ] **Step 3: Handle the three kinds in `walkStep`**

In `agent/workflow/graph/build.go`, insert these cases into the `switch` in `walkStep`, before `default`:

```go
	case *atc.AwaitSnapshotStep:
		b.addNode(Node{
			ID:          config.Name,
			Kind:        KindAwait,
			DisplayName: config.Name,
			TypeRef:     string(config.Type),
			Decorations: append([]Decoration(nil), decorations...),
		})
		if config.Question != "" {
			b.link(config.Question, config.Name)
		}
		b.produce(config.Name, config.Name, string(config.Type))
		return nil
	case *atc.PublishSnapshotStep:
		b.addNode(Node{
			ID:          config.Name,
			Kind:        KindPublish,
			DisplayName: config.Name,
			TypeRef:     string(config.InputType),
			Decorations: append([]Decoration(nil), decorations...),
		})
		b.link(config.Input, config.Name)
		if config.Approval != "" {
			b.link(config.Approval, config.Name)
		}
		if config.Validation != "" {
			b.link(config.Validation, config.Name)
		}
		return nil
	case *atc.LoadSnapshotStep:
		b.addNode(Node{
			ID:          config.Name,
			Kind:        KindLoad,
			DisplayName: config.Name,
			TypeRef:     string(config.Type),
			Decorations: append([]Decoration(nil), decorations...),
		})
		b.produce(config.Name, config.Name, string(config.Type))
		return nil
```

- [ ] **Step 4: Run the tests and confirm they pass**

```bash
go test ./agent/workflow/graph/ -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/workflow/graph/
git commit -m "feat(graph): add await, publish, and load nodes"
```

### Task A5: Wrappers become decorations, not nodes

**Files:**
- Modify: `agent/workflow/graph/build.go`
- Test: `agent/workflow/graph/build_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/workflow/graph/build_test.go`:

```go
func TestBuildTreatsWrappersAsDecorations(t *testing.T) {
	inner := agentStep("implement", "implement",
		[]string{"repository"}, []string{"draft"},
		map[string]atc.SnapshotInputConfig{"repository": {Type: "repository/v1"}},
		map[string]atc.SnapshotOutputConfig{"draft": {Type: "repository-change/v1"}})

	function := &workflow.FunctionConfig{
		SignatureVersion: 1,
		Inputs:           []snapshot.Port{{Name: "repository", Type: "repository/v1"}},
		Plan: []atc.Step{
			// TimeoutStep.Step and RetryStep.Step are StepConfig, not Step, so
			// the inner agent is passed as inner.Config.
			{Config: &atc.TimeoutStep{
				Step:     &atc.RetryStep{Step: inner.Config, Attempts: 3},
				Duration: "1h",
			}},
		},
	}

	g, err := Build(function)
	if err != nil {
		t.Fatalf("Build returned an error: %v", err)
	}

	if len(g.Nodes) != 2 {
		t.Fatalf("expected exactly the input and agent nodes, got %+v", g.Nodes)
	}

	node, found := g.Node("implement")
	if !found {
		t.Fatal("expected the wrapped agent to remain a node")
	}
	if !hasDecoration(node, DecorationTimeout) || !hasDecoration(node, DecorationRetry) {
		t.Fatalf("expected timeout and retry decorations, got %+v", node.Decorations)
	}
}

func TestBuildWalksDoAndInParallel(t *testing.T) {
	left := agentStep("left", "left", []string{"repository"}, []string{"a"},
		map[string]atc.SnapshotInputConfig{"repository": {Type: "repository/v1"}},
		map[string]atc.SnapshotOutputConfig{"a": {Type: "opaque/v1"}})
	right := agentStep("right", "right", []string{"repository"}, []string{"b"},
		map[string]atc.SnapshotInputConfig{"repository": {Type: "repository/v1"}},
		map[string]atc.SnapshotOutputConfig{"b": {Type: "opaque/v1"}})

	function := &workflow.FunctionConfig{
		SignatureVersion: 1,
		Inputs:           []snapshot.Port{{Name: "repository", Type: "repository/v1"}},
		Plan: []atc.Step{
			{Config: &atc.DoStep{Steps: []atc.Step{
				{Config: &atc.InParallelStep{Config: atc.InParallelConfig{Steps: []atc.Step{left, right}}}},
			}}},
		},
	}

	g, err := Build(function)
	if err != nil {
		t.Fatalf("Build returned an error: %v", err)
	}
	for _, id := range []string{"left", "right"} {
		if _, found := g.Node(id); !found {
			t.Fatalf("expected node %q in %+v", id, g.Nodes)
		}
	}
}

func hasDecoration(node Node, want Decoration) bool {
	for _, decoration := range node.Decorations {
		if decoration == want {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Run the tests and confirm they fail**

```bash
go test ./agent/workflow/graph/ -run 'TestBuildTreatsWrappers|TestBuildWalksDo' -count=1
```

Expected: FAIL — `graph: unsupported step config *atc.TimeoutStep`.

- [ ] **Step 3: Add the wrapper cases**

Insert into the `walkStep` switch in `agent/workflow/graph/build.go`, before `default`:

```go
	case *atc.DoStep:
		return b.walkSequence(config.Steps, decorations)
	case *atc.InParallelStep:
		return b.walkSequence(config.Config.Steps, decorations)
	case *atc.TryStep:
		// TryStep.Step is atc.Step, unlike Timeout/Retry below.
		return b.walkStep(config.Step, append(decorations, DecorationTry))
	case *atc.RetryStep:
		return b.walkStepConfig(config.Step, append(decorations, DecorationRetry))
	case *atc.TimeoutStep:
		return b.walkStepConfig(config.Step, append(decorations, DecorationTimeout))
	case *atc.OnSuccessStep:
		if err := b.walkStepConfig(config.Step, decorations); err != nil {
			return err
		}
		return b.walkStep(config.Hook, append(decorations, DecorationOnSuccess))
	case *atc.OnFailureStep:
		if err := b.walkStepConfig(config.Step, decorations); err != nil {
			return err
		}
		return b.walkStep(config.Hook, append(decorations, DecorationOnFailure))
	case *atc.OnErrorStep:
		if err := b.walkStepConfig(config.Step, decorations); err != nil {
			return err
		}
		return b.walkStep(config.Hook, append(decorations, DecorationOnError))
	case *atc.OnAbortStep:
		if err := b.walkStepConfig(config.Step, decorations); err != nil {
			return err
		}
		return b.walkStep(config.Hook, append(decorations, DecorationOnAbort))
	case *atc.EnsureStep:
		if err := b.walkStepConfig(config.Step, decorations); err != nil {
			return err
		}
		return b.walkStep(config.Hook, append(decorations, DecorationEnsure))
```

Each wrapper's `.Step` is `StepConfig` except `TryStep`'s, and each hook's `.Hook` is `Step`. Confirm against `atc/steps.go` rather than assuming uniformity — the compiler will catch a mistake, but knowing why they differ saves a debugging round.

`append(decorations, X)` may share backing storage between sibling branches, which would leak a decoration from one branch into another. `addLeaf` and every node case already copy with `append([]Decoration(nil), decorations...)`, so the stored slice is always private. Keep that copy.

- [ ] **Step 4: Run the tests and confirm they pass**

```bash
go test ./agent/workflow/graph/ -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/workflow/graph/
git commit -m "feat(graph): attach control wrappers as node decorations"
```

### Task A6: Golden graphs for all nine seed workflows

This is the regression net for the whole phase: it proves `Build` handles every shape the platform actually ships.

**Files:**
- Create: `agent/workflow/graph/seeds_test.go`
- Create: `agent/workflow/graph/testdata/<seed-name>.json` (nine files, generated in step 3)

- [ ] **Step 1: Write the failing test**

Create `agent/workflow/graph/seeds_test.go`:

```go
package graph_test

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/agent/workflow/graph"
)

var updateGolden = flag.Bool("update-golden", false, "rewrite testdata golden graphs")

// Every released seed workflow must produce a stable graph. Regenerate with:
//   go test ./agent/workflow/graph/ -run TestSeedGraphs -update-golden
func TestSeedGraphs(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join("..", "seeds"))
	if err != nil {
		t.Fatalf("reading seeds: %v", err)
	}

	found := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join("..", "seeds", entry.Name(), "workflow.yaml")
		if _, err := os.Stat(path); err != nil {
			continue // reusable node seeds carry node.yaml instead
		}
		found++

		t.Run(entry.Name(), func(t *testing.T) {
			definition, err := workflow.ParseDefinitionDir(filepath.Join("..", "seeds", entry.Name()))
			if err != nil {
				t.Fatalf("parsing seed: %v", err)
			}

			built, err := graph.Build(definition.Function)
			if err != nil {
				t.Fatalf("Build returned an error: %v", err)
			}

			actual, err := json.MarshalIndent(built, "", "  ")
			if err != nil {
				t.Fatalf("marshalling graph: %v", err)
			}

			golden := filepath.Join("testdata", entry.Name()+".json")
			if *updateGolden {
				if err := os.MkdirAll("testdata", 0o755); err != nil {
					t.Fatalf("creating testdata: %v", err)
				}
				if err := os.WriteFile(golden, append(actual, '\n'), 0o644); err != nil {
					t.Fatalf("writing golden: %v", err)
				}
				return
			}

			expected, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("reading golden (regenerate with -update-golden): %v", err)
			}
			if string(expected) != string(actual)+"\n" {
				t.Fatalf("graph for %s changed.\nwant:\n%s\ngot:\n%s", entry.Name(), expected, actual)
			}
		})
	}

	if found == 0 {
		t.Fatal("expected to find seed workflows")
	}
}
```

- [ ] **Step 2: Run the test and confirm it fails**

```bash
go test ./agent/workflow/graph/ -run TestSeedGraphs -count=1
```

Expected: FAIL — either `undefined: workflow.ParseDefinitionDir` or missing golden files.

If the parse helper has a different name, find the correct exported entry point:

```bash
grep -n "^func Parse\|^func Load\|^func Compile" agent/workflow/dirload.go agent/workflow/parse.go
```

Use whichever function loads a seed directory into a `*Definition`, and adjust the call. Do not add a new parser.

- [ ] **Step 3: Generate the golden graphs and inspect them**

```bash
go test ./agent/workflow/graph/ -run TestSeedGraphs -update-golden -count=1
```

Then read each generated file and confirm it is a sensible graph before trusting it:

```bash
ls agent/workflow/graph/testdata/ && cat agent/workflow/graph/testdata/small-fix-v3.json
```

Expected for `small-fix-v3`: input nodes `repository` and `work-item`; agent nodes `implement`, `review`, `prepare-question`; a task node for the `validate` dev-validation step; an await node `approval`; output nodes `change` and `report`; and edges following the seed's `inputs:`/`outputs:` wiring. A golden file with zero edges, or with a node missing, means `Build` has a gap — fix `Build`, do not accept the golden.

- [ ] **Step 4: Re-run without the flag and confirm it passes**

```bash
go test ./agent/workflow/graph/ -count=1
```

Expected: PASS, nine subtests.

- [ ] **Step 5: Commit**

```bash
git add agent/workflow/graph/
git commit -m "test(graph): golden graphs for every seed workflow"
```

- [ ] **Step 6: Run the full workflow package once at the phase checkpoint**

```bash
go test ./agent/workflow/... -count=1
```

Expected: PASS. This is the Phase A exit gate.

---

## Phase B — durable node occurrences

The overview needs a durable answer to "which semantic nodes did this run reach, and what was their effective state?" that survives build and template GC. This phase builds one derivation and calls it from two places: live (for current attention) and at run finalization (to freeze history).

**Key existing machinery to reuse, not duplicate:**
- `atc/workflowprovenance/provenance.go` already walks the frozen `actual_plan` and yields `ExpectedProducer{PlanID, StepKind, StepName, LocalOutputPort}`. Phase B adds a sibling walker that yields node identity, because the runtime plan carries `AgentPlan.FunctionID`, `TaskPlan.FunctionID`, `AwaitSnapshotPlan.Name`, and `PublishSnapshotPlan.Name` — the same IDs Phase A registered.
- Evidence tables keyed for exact joins: `agent_run_attempt_metrics` on `(build_id, plan_id, execution_attempt)`, `agent_workflow_waits` on `(build_id_evidence, plan_id, attempt, output_name)`, `agent_run_transcripts` on `(build_id, plan_id)`.
- `agent_publication_occurrences` is keyed `(publication_id, workflow_run_id, build_id)` with **no `plan_id`**, so publish nodes cannot be joined exactly today. Task B4 adds a nullable `plan_id` to close that.

### Task B1: Plan node walker

**Files:**
- Create: `agent/workflowrun/occurrence/plannodes.go`
- Test: `agent/workflowrun/occurrence/plannodes_test.go`

- [ ] **Step 1: Write the failing test**

Create `agent/workflowrun/occurrence/plannodes_test.go`:

```go
package occurrence

import (
	"encoding/json"
	"testing"

	"github.com/concourse/concourse/atc"
)

func TestPlanNodesWalksEverySemanticKind(t *testing.T) {
	plan := atc.Plan{
		ID: "1",
		Do: &atc.DoPlan{
			{ID: "1/1", Agent: &atc.AgentPlan{Name: "implement", FunctionID: "implement"}},
			{ID: "1/2", Retry: &atc.RetryPlan{
				{ID: "1/2/1", Task: &atc.TaskPlan{Name: "validate", FunctionID: "validate"}},
			}},
			{ID: "1/3", AwaitSnapshot: &atc.AwaitSnapshotPlan{Name: "approval"}},
			{ID: "1/4", PublishSnapshot: &atc.PublishSnapshotPlan{Name: "ship"}},
		},
	}

	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("marshalling plan: %v", err)
	}

	nodes, err := PlanNodes(raw)
	if err != nil {
		t.Fatalf("PlanNodes returned an error: %v", err)
	}

	want := []PlanNode{
		{PlanID: "1/1", NodeID: "implement", Kind: "agent", DisplayName: "implement"},
		{PlanID: "1/2/1", NodeID: "validate", Kind: "task", DisplayName: "validate"},
		{PlanID: "1/3", NodeID: "approval", Kind: "await", DisplayName: "approval"},
		{PlanID: "1/4", NodeID: "ship", Kind: "publish", DisplayName: "ship"},
	}
	if len(nodes) != len(want) {
		t.Fatalf("expected %d nodes, got %+v", len(want), nodes)
	}
	for index := range want {
		if nodes[index] != want[index] {
			t.Fatalf("node %d: expected %+v, got %+v", index, want[index], nodes[index])
		}
	}
}

func TestPlanNodesRejectsMalformedPlan(t *testing.T) {
	if _, err := PlanNodes([]byte("not json")); err == nil {
		t.Fatal("expected a decode error for a malformed plan")
	}
}
```

Confirm the sub-plan field names before running. If `atc.Plan` names them differently, correct the test to match:

```bash
grep -n "Do \|Retry \|InParallel \|Try \|Timeout \|OnSuccess \|Ensure \|Agent \|Task \|AwaitSnapshot \|PublishSnapshot " atc/plan.go | head -30
```

- [ ] **Step 2: Run the test and confirm it fails**

```bash
go test ./agent/workflowrun/occurrence/ -count=1
```

Expected: FAIL — the package does not exist.

- [ ] **Step 3: Write the walker**

Create `agent/workflowrun/occurrence/plannodes.go`:

```go
// Package occurrence derives per-node occurrence state for one workflow run
// from authoritative execution records. It is called twice with the same
// semantics: live, so the overview's current-attention view is never stale,
// and at run finalization, to freeze durable history before Concourse
// reclaims the underlying build.
package occurrence

import (
	"encoding/json"
	"fmt"

	"github.com/concourse/concourse/atc"
)

// PlanNode is one semantic node located in a run's frozen actual plan. NodeID
// is the stable workflow-local identity registered by the type checker.
type PlanNode struct {
	PlanID      string
	NodeID      string
	Kind        string
	DisplayName string
}

// PlanNodes decodes a frozen actual plan and returns its semantic nodes in
// plan order. Control wrappers are traversed but never emitted, matching the
// rule that wrappers decorate nodes rather than becoming them.
func PlanNodes(actualPlan []byte) ([]PlanNode, error) {
	var plan atc.Plan
	if err := json.Unmarshal(actualPlan, &plan); err != nil {
		return nil, fmt.Errorf("occurrence: decoding actual plan: %w", err)
	}
	var nodes []PlanNode
	walkPlan(plan, &nodes)
	return nodes, nil
}

func walkPlan(plan atc.Plan, nodes *[]PlanNode) {
	switch {
	case plan.Agent != nil:
		*nodes = append(*nodes, PlanNode{
			PlanID:      string(plan.ID),
			NodeID:      plan.Agent.FunctionID,
			Kind:        "agent",
			DisplayName: plan.Agent.Name,
		})
	case plan.Task != nil:
		*nodes = append(*nodes, PlanNode{
			PlanID:      string(plan.ID),
			NodeID:      plan.Task.FunctionID,
			Kind:        "task",
			DisplayName: plan.Task.Name,
		})
	case plan.AwaitSnapshot != nil:
		*nodes = append(*nodes, PlanNode{
			PlanID:      string(plan.ID),
			NodeID:      plan.AwaitSnapshot.Name,
			Kind:        "await",
			DisplayName: plan.AwaitSnapshot.Name,
		})
	case plan.PublishSnapshot != nil:
		*nodes = append(*nodes, PlanNode{
			PlanID:      string(plan.ID),
			NodeID:      plan.PublishSnapshot.Name,
			Kind:        "publish",
			DisplayName: plan.PublishSnapshot.Name,
		})
	case plan.LoadSnapshot != nil:
		*nodes = append(*nodes, PlanNode{
			PlanID:      string(plan.ID),
			NodeID:      plan.LoadSnapshot.Name,
			Kind:        "load",
			DisplayName: plan.LoadSnapshot.Name,
		})
	case plan.Do != nil:
		for _, child := range *plan.Do {
			walkPlan(child, nodes)
		}
	case plan.InParallel != nil:
		for _, child := range plan.InParallel.Steps {
			walkPlan(child, nodes)
		}
	case plan.Retry != nil:
		for _, child := range *plan.Retry {
			walkPlan(child, nodes)
		}
	case plan.Try != nil:
		walkPlan(plan.Try.Step, nodes)
	case plan.Timeout != nil:
		walkPlan(plan.Timeout.Step, nodes)
	case plan.OnSuccess != nil:
		walkPlan(plan.OnSuccess.Step, nodes)
		walkPlan(plan.OnSuccess.Next, nodes)
	case plan.OnFailure != nil:
		walkPlan(plan.OnFailure.Step, nodes)
		walkPlan(plan.OnFailure.Next, nodes)
	case plan.OnError != nil:
		walkPlan(plan.OnError.Step, nodes)
		walkPlan(plan.OnError.Next, nodes)
	case plan.OnAbort != nil:
		walkPlan(plan.OnAbort.Step, nodes)
		walkPlan(plan.OnAbort.Next, nodes)
	case plan.Ensure != nil:
		walkPlan(plan.Ensure.Step, nodes)
		walkPlan(plan.Ensure.Next, nodes)
	}
}
```

Field names on the hook plans (`Step`/`Next` versus `Step`/`Hook`) must match `atc/plan.go`. Correct them from the grep in step 1 rather than guessing.

- [ ] **Step 4: Run the tests and confirm they pass**

```bash
go test ./agent/workflowrun/occurrence/ -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/workflowrun/occurrence/
git commit -m "feat(occurrence): walk a frozen actual plan into semantic node identities"
```

### Task B2: Occurrence types and derivation for agent nodes

**Files:**
- Create: `agent/workflowrun/occurrence/occurrence.go`
- Create: `agent/workflowrun/occurrence/derive.go`
- Test: `agent/workflowrun/occurrence/derive_test.go`

- [ ] **Step 1: Write the failing test**

Create `agent/workflowrun/occurrence/derive_test.go`:

```go
package occurrence

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

func planWithAgent(t *testing.T) []byte {
	t.Helper()
	raw, err := json.Marshal(atc.Plan{
		ID: "1",
		Do: &atc.DoPlan{
			{ID: "1/1", Agent: &atc.AgentPlan{Name: "implement", FunctionID: "implement"}},
		},
	})
	if err != nil {
		t.Fatalf("marshalling plan: %v", err)
	}
	return raw
}

func TestDeriveAgentNodeFromAttemptMetrics(t *testing.T) {
	started := time.Date(2026, 7, 31, 10, 0, 0, 0, time.UTC)
	completed := started.Add(4 * time.Minute)

	sources := Sources{
		Run: db.AgentWorkflowRun{
			ID:            42,
			TeamID:        1,
			WorkflowName:  "small-fix",
			WorkflowVersion: 3,
			ActualPlan:    planWithAgent(t),
			PlannedBuildID: int64Ptr(900),
		},
		AttemptMetrics: []AttemptMetric{{
			PlanID:           "1/1",
			ExecutionAttempt: 1,
			Status:           "ok",
			CostUSD:          1.25,
			CreatedAt:        started,
			UpdatedAt:        completed,
		}},
	}

	occurrences, err := Derive(sources)
	if err != nil {
		t.Fatalf("Derive returned an error: %v", err)
	}
	if len(occurrences) != 1 {
		t.Fatalf("expected one occurrence, got %+v", occurrences)
	}

	got := occurrences[0]
	if got.NodeID != "implement" || got.NodeKind != "agent" {
		t.Fatalf("unexpected node identity: %+v", got)
	}
	if got.Status != StatusSucceeded {
		t.Fatalf("expected succeeded, got %q", got.Status)
	}
	if got.Attempt != 1 || got.CostUSD != 1.25 {
		t.Fatalf("unexpected attempt or cost: %+v", got)
	}
}

func TestDeriveMapsAgentFailureAndError(t *testing.T) {
	for status, want := range map[string]Status{
		"failed":     StatusFailed,
		"error":      StatusErrored,
		"incomplete": StatusSucceeded,
	} {
		sources := Sources{
			Run: db.AgentWorkflowRun{
				ID: 42, TeamID: 1, WorkflowName: "small-fix", WorkflowVersion: 3,
				ActualPlan: planWithAgent(t), PlannedBuildID: int64Ptr(900),
			},
			AttemptMetrics: []AttemptMetric{{PlanID: "1/1", ExecutionAttempt: 1, Status: status}},
		}
		occurrences, err := Derive(sources)
		if err != nil {
			t.Fatalf("Derive returned an error: %v", err)
		}
		if occurrences[0].Status != want {
			t.Fatalf("metric status %q: expected %q, got %q", status, want, occurrences[0].Status)
		}
	}
}

func TestDeriveEmitsPendingForUnreachedNode(t *testing.T) {
	sources := Sources{
		Run: db.AgentWorkflowRun{
			ID: 42, TeamID: 1, WorkflowName: "small-fix", WorkflowVersion: 3,
			ActualPlan: planWithAgent(t), PlannedBuildID: int64Ptr(900),
		},
	}
	occurrences, err := Derive(sources)
	if err != nil {
		t.Fatalf("Derive returned an error: %v", err)
	}
	if len(occurrences) != 1 || occurrences[0].Status != StatusPending {
		t.Fatalf("expected one pending occurrence, got %+v", occurrences)
	}
}

func int64Ptr(value int64) *int64 { return &value }
```

`incomplete` maps to succeeded because migration `1773106126` documents it as a missing *recording*, not a failed step, and `DeriveOutcome` already fuses it to amber on a succeeded build. Rendering it red on the canvas would be a false alarm.

- [ ] **Step 2: Run the test and confirm it fails**

```bash
go test ./agent/workflowrun/occurrence/ -run TestDerive -count=1
```

Expected: FAIL — `undefined: Sources`.

- [ ] **Step 3: Write the types**

Create `agent/workflowrun/occurrence/occurrence.go`:

```go
package occurrence

import (
	"fmt"
	"time"

	"github.com/concourse/concourse/atc/db"
)

type Status string

const (
	StatusPending   Status = "pending"
	StatusRunning   Status = "running"
	StatusWaiting   Status = "waiting"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusErrored   Status = "errored"
	StatusAborted   Status = "aborted"
	StatusSkipped   Status = "skipped"
)

func (status Status) Validate() error {
	switch status {
	case StatusPending, StatusRunning, StatusWaiting, StatusSucceeded,
		StatusFailed, StatusErrored, StatusAborted, StatusSkipped:
		return nil
	default:
		return fmt.Errorf("occurrence: invalid status %q", status)
	}
}

// Terminal reports whether the status can no longer change for this attempt.
func (status Status) Terminal() bool {
	switch status {
	case StatusSucceeded, StatusFailed, StatusErrored, StatusAborted, StatusSkipped:
		return true
	default:
		return false
	}
}

// NodeOccurrence is one attempt of one semantic node within one workflow run.
type NodeOccurrence struct {
	WorkflowRunID        int64
	TeamID               int
	WorkflowName         string
	WorkflowDefinitionID int
	WorkflowVersion      int
	NodeID               string
	NodeKind             string
	Attempt              int
	PlanID               string
	Status               Status
	ReusableNodeName     string
	ReusableNodeVersion  int
	WaitID               *int64
	PublicationID        *int64
	StartedAt            *time.Time
	CompletedAt          *time.Time
	DurationSeconds      int
	CostUSD              float64
}

// AttemptMetric is the narrow projection of agent_run_attempt_metrics the
// derivation needs. It is a local type so the derivation does not depend on
// the full DB row shape.
type AttemptMetric struct {
	PlanID           string
	ExecutionAttempt int
	Status           string
	CostUSD          float64
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// Wait is the narrow projection of agent_workflow_waits.
type Wait struct {
	ID         int64
	PlanID     string
	OutputName string
	Status     string
	CreatedAt  time.Time
	ResolvedAt *time.Time
}

// Publication is the narrow projection of agent_publication_occurrences.
type Publication struct {
	ID        int64
	PlanID    string
	Status    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Sources is every authoritative record the derivation reads. Callers supply
// whatever they have; absent evidence yields a pending occurrence rather than
// an error, because a run may legitimately not have reached a node.
type Sources struct {
	Run            db.AgentWorkflowRun
	AttemptMetrics []AttemptMetric
	Waits          []Wait
	Publications   []Publication
	// BuildStepStatus maps a plan ID to a terminal build step status for nodes
	// that have no other durable evidence, notably deterministic tasks. It is
	// read from build events while they still exist.
	BuildStepStatus map[string]Status
}
```

- [ ] **Step 4: Write the derivation**

Create `agent/workflowrun/occurrence/derive.go`:

```go
package occurrence

import (
	"fmt"
	"time"
)

// Derive maps authoritative execution records onto one occurrence per
// semantic node attempt. It never originates a fact: every status traces to a
// durable record, and a node with no evidence is pending.
func Derive(sources Sources) ([]NodeOccurrence, error) {
	if len(sources.Run.ActualPlan) == 0 {
		return nil, fmt.Errorf("occurrence: run %d has no frozen actual plan", sources.Run.ID)
	}

	nodes, err := PlanNodes(sources.Run.ActualPlan)
	if err != nil {
		return nil, err
	}

	metricsByPlan := map[string][]AttemptMetric{}
	for _, metric := range sources.AttemptMetrics {
		metricsByPlan[metric.PlanID] = append(metricsByPlan[metric.PlanID], metric)
	}

	var result []NodeOccurrence
	for _, node := range nodes {
		base := NodeOccurrence{
			WorkflowRunID:        int64(sources.Run.ID),
			TeamID:               sources.Run.TeamID,
			WorkflowName:         sources.Run.WorkflowName,
			WorkflowDefinitionID: sources.Run.WorkflowDefinitionID,
			WorkflowVersion:      sources.Run.WorkflowVersion,
			NodeID:               node.NodeID,
			NodeKind:             node.Kind,
			PlanID:               node.PlanID,
			Attempt:              1,
			Status:               StatusPending,
		}

		metrics := metricsByPlan[node.PlanID]
		if len(metrics) == 0 {
			if status, found := sources.BuildStepStatus[node.PlanID]; found {
				base.Status = status
			}
			result = append(result, base)
			continue
		}

		for _, metric := range metrics {
			occurrence := base
			occurrence.Attempt = metric.ExecutionAttempt
			occurrence.Status = agentMetricStatus(metric.Status)
			occurrence.CostUSD = metric.CostUSD
			occurrence.StartedAt = timePtr(metric.CreatedAt)
			if occurrence.Status.Terminal() {
				occurrence.CompletedAt = timePtr(metric.UpdatedAt)
				occurrence.DurationSeconds = int(metric.UpdatedAt.Sub(metric.CreatedAt).Seconds())
			}
			result = append(result, occurrence)
		}
	}

	return result, nil
}

// agentMetricStatus maps agent_run_metrics.status onto occurrence status.
// 'incomplete' means a missing flight recording on an otherwise successful
// step (migration 1773106126), so it must not render as a failure.
func agentMetricStatus(status string) Status {
	switch status {
	case "ok", "incomplete":
		return StatusSucceeded
	case "failed":
		return StatusFailed
	case "error":
		return StatusErrored
	case "parked":
		return StatusRunning
	default:
		return StatusPending
	}
}

func timePtr(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}
```

- [ ] **Step 5: Run the tests and confirm they pass**

```bash
go test ./agent/workflowrun/occurrence/ -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add agent/workflowrun/occurrence/
git commit -m "feat(occurrence): derive agent node occurrences from attempt metrics"
```

### Task B3: Derive await and publish occurrences

**Files:**
- Modify: `agent/workflowrun/occurrence/derive.go`
- Test: `agent/workflowrun/occurrence/derive_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/workflowrun/occurrence/derive_test.go`:

```go
func planWithAwaitAndPublish(t *testing.T) []byte {
	t.Helper()
	raw, err := json.Marshal(atc.Plan{
		ID: "1",
		Do: &atc.DoPlan{
			{ID: "1/1", AwaitSnapshot: &atc.AwaitSnapshotPlan{Name: "approval"}},
			{ID: "1/2", PublishSnapshot: &atc.PublishSnapshotPlan{Name: "ship"}},
		},
	})
	if err != nil {
		t.Fatalf("marshalling plan: %v", err)
	}
	return raw
}

func TestDeriveAwaitAndPublishOccurrences(t *testing.T) {
	created := time.Date(2026, 7, 31, 10, 0, 0, 0, time.UTC)

	sources := Sources{
		Run: db.AgentWorkflowRun{
			ID: 42, TeamID: 1, WorkflowName: "small-fix", WorkflowVersion: 3,
			ActualPlan: planWithAwaitAndPublish(t), PlannedBuildID: int64Ptr(900),
		},
		Waits: []Wait{{
			ID: 7, PlanID: "1/1", OutputName: "approval", Status: "waiting", CreatedAt: created,
		}},
		Publications: []Publication{{
			ID: 11, PlanID: "1/2", Status: "succeeded", CreatedAt: created, UpdatedAt: created.Add(time.Minute),
		}},
	}

	occurrences, err := Derive(sources)
	if err != nil {
		t.Fatalf("Derive returned an error: %v", err)
	}

	byNode := map[string]NodeOccurrence{}
	for _, occurrence := range occurrences {
		byNode[occurrence.NodeID] = occurrence
	}

	approval := byNode["approval"]
	if approval.Status != StatusWaiting {
		t.Fatalf("expected the unresolved wait to be waiting, got %q", approval.Status)
	}
	if approval.WaitID == nil || *approval.WaitID != 7 {
		t.Fatalf("expected the wait detail pointer, got %+v", approval.WaitID)
	}

	ship := byNode["ship"]
	if ship.Status != StatusSucceeded {
		t.Fatalf("expected the publication to be succeeded, got %q", ship.Status)
	}
	if ship.PublicationID == nil || *ship.PublicationID != 11 {
		t.Fatalf("expected the publication detail pointer, got %+v", ship.PublicationID)
	}
}

func TestDeriveMapsWaitResolutionStatuses(t *testing.T) {
	resolved := time.Date(2026, 7, 31, 11, 0, 0, 0, time.UTC)
	for waitStatus, want := range map[string]Status{
		"waiting":   StatusWaiting,
		"resolved":  StatusSucceeded,
		"timed_out": StatusFailed,
		"cancelled": StatusAborted,
	} {
		sources := Sources{
			Run: db.AgentWorkflowRun{
				ID: 42, TeamID: 1, WorkflowName: "small-fix", WorkflowVersion: 3,
				ActualPlan: planWithAwaitAndPublish(t), PlannedBuildID: int64Ptr(900),
			},
			Waits: []Wait{{ID: 7, PlanID: "1/1", OutputName: "approval", Status: waitStatus, ResolvedAt: &resolved}},
		}
		occurrences, err := Derive(sources)
		if err != nil {
			t.Fatalf("Derive returned an error: %v", err)
		}
		var got Status
		for _, occurrence := range occurrences {
			if occurrence.NodeID == "approval" {
				got = occurrence.Status
			}
		}
		if got != want {
			t.Fatalf("wait status %q: expected %q, got %q", waitStatus, want, got)
		}
	}
}
```

- [ ] **Step 2: Run the tests and confirm they fail**

```bash
go test ./agent/workflowrun/occurrence/ -run 'TestDeriveAwait|TestDeriveMapsWait' -count=1
```

Expected: FAIL — both nodes come back pending.

- [ ] **Step 3: Index the new evidence in `Derive`**

In `agent/workflowrun/occurrence/derive.go`, after `metricsByPlan` is built:

```go
	waitsByPlan := map[string]Wait{}
	for _, wait := range sources.Waits {
		waitsByPlan[wait.PlanID] = wait
	}

	publicationsByPlan := map[string]Publication{}
	for _, publication := range sources.Publications {
		publicationsByPlan[publication.PlanID] = publication
	}
```

- [ ] **Step 4: Branch on node kind before the metrics path**

Replace the `metrics := metricsByPlan[node.PlanID]` block's opening with a kind switch:

```go
		switch node.Kind {
		case "await":
			if wait, found := waitsByPlan[node.PlanID]; found {
				base.Status = waitStatus(wait.Status)
				base.WaitID = int64Ref(wait.ID)
				base.StartedAt = timePtr(wait.CreatedAt)
				if base.Status.Terminal() && wait.ResolvedAt != nil {
					base.CompletedAt = wait.ResolvedAt
					base.DurationSeconds = int(wait.ResolvedAt.Sub(wait.CreatedAt).Seconds())
				}
			}
			result = append(result, base)
			continue
		case "publish":
			if publication, found := publicationsByPlan[node.PlanID]; found {
				base.Status = publicationStatus(publication.Status)
				base.PublicationID = int64Ref(publication.ID)
				base.StartedAt = timePtr(publication.CreatedAt)
				if base.Status.Terminal() {
					base.CompletedAt = timePtr(publication.UpdatedAt)
					base.DurationSeconds = int(publication.UpdatedAt.Sub(publication.CreatedAt).Seconds())
				}
			}
			result = append(result, base)
			continue
		}

		metrics := metricsByPlan[node.PlanID]
```

- [ ] **Step 5: Add the status mappings**

Append to `agent/workflowrun/occurrence/derive.go`:

```go
// waitStatus maps agent_workflow_waits.status. A timed-out wait is a failure
// because its workflow could not obtain the human answer it required; a
// cancelled wait is an abort.
func waitStatus(status string) Status {
	switch status {
	case "waiting":
		return StatusWaiting
	case "resolved":
		return StatusSucceeded
	case "timed_out":
		return StatusFailed
	case "cancelled":
		return StatusAborted
	default:
		return StatusPending
	}
}

// publicationStatus maps agent_publication_occurrences.status. Both
// 'stale_base' and 'rebase_required' are unresolved outbound effects that need
// human attention, so they surface as failures rather than as pending.
func publicationStatus(status string) Status {
	switch status {
	case "pending":
		return StatusRunning
	case "succeeded":
		return StatusSucceeded
	case "failed", "stale_base", "rebase_required":
		return StatusFailed
	default:
		return StatusPending
	}
}

func int64Ref(value int64) *int64 { return &value }
```

- [ ] **Step 6: Run the tests and confirm they pass**

```bash
go test ./agent/workflowrun/occurrence/ -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add agent/workflowrun/occurrence/
git commit -m "feat(occurrence): derive await and publish node occurrences"
```

### Task B4: Retry-chain attention resolution

Resolves the branching-retry case: for each pair of retry closure and node ID, the effective occurrence is the last terminal one, unioned with any currently-active ones.

**Files:**
- Create: `agent/workflowrun/occurrence/attention.go`
- Test: `agent/workflowrun/occurrence/attention_test.go`

- [ ] **Step 1: Write the failing test**

Create `agent/workflowrun/occurrence/attention_test.go`:

```go
package occurrence

import (
	"testing"
	"time"
)

func at(minute int) time.Time {
	return time.Date(2026, 7, 31, 10, minute, 0, 0, time.UTC)
}

func TestResolveEffectiveLaterSuccessClearsEarlierFailure(t *testing.T) {
	entries := []ChainEntry{
		{RunID: 1, RunCreatedAt: at(0), Occurrence: NodeOccurrence{NodeID: "implement", Status: StatusFailed}},
		{RunID: 2, RunCreatedAt: at(5), Occurrence: NodeOccurrence{NodeID: "implement", Status: StatusSucceeded}},
	}

	effective := ResolveEffective(entries)
	if len(effective) != 1 {
		t.Fatalf("expected one effective occurrence, got %+v", effective)
	}
	if effective[0].Status != StatusSucceeded {
		t.Fatalf("expected the later success to win, got %q", effective[0].Status)
	}
	if effective[0].NeedsAttention {
		t.Fatal("a resolved node must not need attention")
	}
}

func TestResolveEffectiveKeepsActiveAlongsideTerminal(t *testing.T) {
	entries := []ChainEntry{
		{RunID: 1, RunCreatedAt: at(0), Occurrence: NodeOccurrence{NodeID: "implement", Status: StatusFailed}},
		{RunID: 2, RunCreatedAt: at(5), Occurrence: NodeOccurrence{NodeID: "implement", Status: StatusRunning}},
	}

	effective := ResolveEffective(entries)
	if len(effective) != 2 {
		t.Fatalf("expected the active occurrence to be retained beside the terminal one, got %+v", effective)
	}

	var sawRunning bool
	for _, resolved := range effective {
		if resolved.Status == StatusRunning {
			sawRunning = true
			if !resolved.NeedsAttention {
				t.Fatal("a running node is attention-worthy")
			}
		}
	}
	if !sawRunning {
		t.Fatal("expected a running effective occurrence")
	}
}

func TestResolveEffectiveBranchingRetriesTakeTheLatest(t *testing.T) {
	entries := []ChainEntry{
		{RunID: 1, RunCreatedAt: at(0), Occurrence: NodeOccurrence{NodeID: "implement", Status: StatusFailed}},
		{RunID: 2, RunCreatedAt: at(5), Occurrence: NodeOccurrence{NodeID: "implement", Status: StatusSucceeded}},
		{RunID: 3, RunCreatedAt: at(3), Occurrence: NodeOccurrence{NodeID: "implement", Status: StatusFailed}},
	}

	effective := ResolveEffective(entries)
	if len(effective) != 1 || effective[0].Status != StatusSucceeded || effective[0].RunID != 2 {
		t.Fatalf("expected the latest-created terminal occurrence to win, got %+v", effective)
	}
}

func TestResolveEffectiveUnresolvedFailureNeedsAttention(t *testing.T) {
	entries := []ChainEntry{
		{RunID: 1, RunCreatedAt: at(0), Occurrence: NodeOccurrence{NodeID: "implement", Status: StatusFailed}},
	}
	effective := ResolveEffective(entries)
	if len(effective) != 1 || !effective[0].NeedsAttention {
		t.Fatalf("expected an unresolved failure to need attention, got %+v", effective)
	}
}

func TestResolveEffectiveWaitingNeedsAttention(t *testing.T) {
	entries := []ChainEntry{
		{RunID: 1, RunCreatedAt: at(0), Occurrence: NodeOccurrence{NodeID: "approval", Status: StatusWaiting}},
	}
	effective := ResolveEffective(entries)
	if len(effective) != 1 || !effective[0].NeedsAttention {
		t.Fatalf("expected a waiting node to need attention, got %+v", effective)
	}
}
```

- [ ] **Step 2: Run the tests and confirm they fail**

```bash
go test ./agent/workflowrun/occurrence/ -run TestResolveEffective -count=1
```

Expected: FAIL — `undefined: ChainEntry`.

- [ ] **Step 3: Write the resolver**

Create `agent/workflowrun/occurrence/attention.go`:

```go
package occurrence

import (
	"sort"
	"time"
)

// ChainEntry is one occurrence located within a retry closure. RunCreatedAt is
// the creating run's timestamp, which supplies the deterministic ordering the
// resolution depends on.
type ChainEntry struct {
	RunID        int64
	RunCreatedAt time.Time
	Occurrence   NodeOccurrence
}

// Effective is the resolved state of one node across a whole retry closure.
type Effective struct {
	NodeID         string
	RunID          int64
	Status         Status
	NeedsAttention bool
	Occurrence     NodeOccurrence
}

// ResolveEffective collapses a retry closure onto the state the overview
// should show. For each node the effective set is the last terminal occurrence
// unioned with every currently-active occurrence, so a later success clears an
// earlier failure from attention while branching retries resolve
// deterministically without inventing causal edges.
//
// Nothing is discarded: the superseded occurrences remain in run history and
// in evaluation statistics. This function answers "what needs action now", not
// "what happened".
func ResolveEffective(entries []ChainEntry) []Effective {
	ordered := append([]ChainEntry(nil), entries...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if !ordered[i].RunCreatedAt.Equal(ordered[j].RunCreatedAt) {
			return ordered[i].RunCreatedAt.Before(ordered[j].RunCreatedAt)
		}
		return ordered[i].RunID < ordered[j].RunID
	})

	type bucket struct {
		latestTerminal *ChainEntry
		active         []ChainEntry
	}

	buckets := map[string]*bucket{}
	var nodeOrder []string

	for index := range ordered {
		entry := ordered[index]
		current, found := buckets[entry.Occurrence.NodeID]
		if !found {
			current = &bucket{}
			buckets[entry.Occurrence.NodeID] = current
			nodeOrder = append(nodeOrder, entry.Occurrence.NodeID)
		}
		if entry.Occurrence.Status.Terminal() {
			copied := entry
			current.latestTerminal = &copied
			continue
		}
		current.active = append(current.active, entry)
	}

	var result []Effective
	for _, nodeID := range nodeOrder {
		current := buckets[nodeID]
		for _, entry := range current.active {
			result = append(result, Effective{
				NodeID:         nodeID,
				RunID:          entry.RunID,
				Status:         entry.Occurrence.Status,
				NeedsAttention: true,
				Occurrence:     entry.Occurrence,
			})
		}
		if current.latestTerminal != nil {
			entry := *current.latestTerminal
			result = append(result, Effective{
				NodeID:         nodeID,
				RunID:          entry.RunID,
				Status:         entry.Occurrence.Status,
				NeedsAttention: needsAttention(entry.Occurrence.Status),
				Occurrence:     entry.Occurrence,
			})
		}
	}

	return result
}

// needsAttention is true for terminal states a human must still act on.
// Pending and running are handled by the active branch above.
func needsAttention(status Status) bool {
	switch status {
	case StatusFailed, StatusErrored, StatusAborted:
		return true
	default:
		return false
	}
}
```

`StatusWaiting` is non-terminal, so a waiting node lands in the active branch and is always attention-worthy — which is what `TestResolveEffectiveWaitingNeedsAttention` asserts.

- [ ] **Step 4: Run the tests and confirm they pass**

```bash
go test ./agent/workflowrun/occurrence/ -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/workflowrun/occurrence/
git commit -m "feat(occurrence): resolve effective node state across retry closures"
```

### Task B5: Projection table migration

**Files:**
- Create: `atc/db/migration/migrations/1773106157_create_workflow_run_node_occurrences.up.sql`
- Create: `atc/db/migration/migrations/1773106157_create_workflow_run_node_occurrences.down.sql`
- Test: `atc/db/migration/migrations_test.go` (existing suite picks up new files)

- [ ] **Step 1: Confirm 1773106157 is free**

```bash
ls atc/db/migration/migrations/ | grep 1773106157 || echo "free"
```

Expected: `free`. If it is taken, use the next unused number and update every reference in this plan.

- [ ] **Step 2: Write the up migration**

Create `atc/db/migration/migrations/1773106157_create_workflow_run_node_occurrences.up.sql`:

```sql
-- One row per attempt of one semantic node within one workflow run. This is a
-- frozen projection of authoritative run/attempt/wait/publication state, not a
-- second execution truth: it is written once at run finalization, before
-- Concourse build and template GC can reclaim the records it was derived from.
-- Deterministic task steps exist only here after that point.
CREATE TABLE agent_workflow_run_node_occurrences (
    workflow_run_id        BIGINT NOT NULL
        REFERENCES agent_workflow_runs (id) ON DELETE CASCADE,
    node_id                TEXT NOT NULL CHECK (btrim(node_id) <> ''),
    attempt                INTEGER NOT NULL DEFAULT 1 CHECK (attempt > 0),

    team_id                INTEGER NOT NULL CHECK (team_id > 0),
    workflow_name          TEXT NOT NULL CHECK (btrim(workflow_name) <> ''),
    workflow_definition_id INTEGER NOT NULL,
    workflow_version       INTEGER NOT NULL CHECK (workflow_version > 0),

    node_kind              TEXT NOT NULL
        CHECK (node_kind IN ('agent', 'task', 'await', 'publish', 'load')),
    reusable_node_name     TEXT NOT NULL DEFAULT '',
    reusable_node_version  INTEGER,
    plan_id                TEXT NOT NULL DEFAULT '',

    status                 TEXT NOT NULL
        CHECK (status IN ('pending', 'running', 'waiting', 'succeeded',
                          'failed', 'errored', 'aborted', 'skipped')),

    wait_id                BIGINT REFERENCES agent_workflow_waits (id) ON DELETE SET NULL,
    publication_id         BIGINT,

    started_at             TIMESTAMPTZ,
    completed_at           TIMESTAMPTZ,
    duration_seconds       INTEGER NOT NULL DEFAULT 0 CHECK (duration_seconds >= 0),
    cost_usd               NUMERIC(12,6) NOT NULL DEFAULT 0 CHECK (cost_usd >= 0),

    frozen_at              TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),

    PRIMARY KEY (workflow_run_id, node_id, attempt),
    CHECK (completed_at IS NULL OR started_at IS NULL OR completed_at >= started_at),
    CHECK (reusable_node_version IS NULL OR reusable_node_version > 0),
    CHECK ((reusable_node_name = '') = (reusable_node_version IS NULL))
);

-- The cross-revision aggregate path: history for one workflow-local logical
-- role across every revision, bounded by the selected window.
CREATE INDEX agent_workflow_run_node_occurrences_history
    ON agent_workflow_run_node_occurrences
       (team_id, workflow_name, node_id, completed_at DESC);

-- The run page path.
CREATE INDEX agent_workflow_run_node_occurrences_run
    ON agent_workflow_run_node_occurrences (workflow_run_id, node_id);

-- A frozen occurrence is immutable history. Correcting one means the
-- derivation was wrong, which is a code fix plus a deliberate re-freeze, never
-- an in-place UPDATE.
CREATE FUNCTION enforce_agent_workflow_run_node_occurrence_immutability()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'agent workflow run node occurrences are immutable once frozen';
END;
$$;

CREATE TRIGGER agent_workflow_run_node_occurrences_immutable
BEFORE UPDATE ON agent_workflow_run_node_occurrences
FOR EACH ROW
EXECUTE FUNCTION enforce_agent_workflow_run_node_occurrence_immutability();

-- agent_publication_occurrences is keyed (publication_id, workflow_run_id,
-- build_id) and carries no plan identity, so a publish node cannot be joined
-- exactly to its occurrence. Add the plan ID going forward. Existing rows stay
-- NULL and fall back to build step state, which is correct because those runs
-- predate the projection entirely.
ALTER TABLE agent_publication_occurrences
    ADD COLUMN plan_id TEXT;

CREATE INDEX agent_publication_occurrences_plan
    ON agent_publication_occurrences (workflow_run_id, plan_id)
    WHERE plan_id IS NOT NULL;
```

- [ ] **Step 3: Write the down migration**

Create `atc/db/migration/migrations/1773106157_create_workflow_run_node_occurrences.down.sql`:

```sql
DROP INDEX IF EXISTS agent_publication_occurrences_plan;
ALTER TABLE agent_publication_occurrences DROP COLUMN IF EXISTS plan_id;

DROP TRIGGER IF EXISTS agent_workflow_run_node_occurrences_immutable
    ON agent_workflow_run_node_occurrences;
DROP FUNCTION IF EXISTS enforce_agent_workflow_run_node_occurrence_immutability();
DROP TABLE IF EXISTS agent_workflow_run_node_occurrences;
```

- [ ] **Step 4: Run the migration suite in both directions**

```bash
go test ./atc/db/migration/ -count=1
```

Expected: PASS. If the suite reports a checksum or immutability violation, you edited an existing migration by mistake — revert that file and keep changes inside `1773106157`.

- [ ] **Step 5: Commit**

```bash
git add atc/db/migration/migrations/1773106157_create_workflow_run_node_occurrences.up.sql atc/db/migration/migrations/1773106157_create_workflow_run_node_occurrences.down.sql
git commit -m "feat(db): durable workflow-run node occurrence projection"
```

### Task B6: Freeze occurrences at run finalization

**Files:**
- Create: `atc/db/agent_workflow_run_node_occurrences_factory.go`
- Test: `atc/db/agent_workflow_run_node_occurrences_factory_test.go`
- Modify: `agent/workflowrun/reconciler.go`

- [ ] **Step 1: Write the failing DB spec**

`atc/db` uses Ginkgo. Create `atc/db/agent_workflow_run_node_occurrences_factory_test.go`:

```go
package db_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/atc/db"
)

var _ = Describe("AgentWorkflowRunNodeOccurrencesFactory", func() {
	var factory db.AgentWorkflowRunNodeOccurrencesFactory

	BeforeEach(func() {
		factory = db.NewAgentWorkflowRunNodeOccurrencesFactory(dbConn)
	})

	Describe("Freeze", func() {
		It("persists one row per node attempt and reads them back in plan order", func() {
			run := createAgentWorkflowRunForOccurrences()

			occurrences := []db.AgentWorkflowRunNodeOccurrence{
				{
					WorkflowRunID: run.ID, NodeID: "implement", Attempt: 1,
					TeamID: run.TeamID, WorkflowName: run.WorkflowName,
					WorkflowDefinitionID: run.WorkflowDefinitionID,
					WorkflowVersion:      run.WorkflowVersion,
					NodeKind:             "agent", PlanID: "1/1", Status: "succeeded",
					CostUSD: 1.25,
				},
				{
					WorkflowRunID: run.ID, NodeID: "approval", Attempt: 1,
					TeamID: run.TeamID, WorkflowName: run.WorkflowName,
					WorkflowDefinitionID: run.WorkflowDefinitionID,
					WorkflowVersion:      run.WorkflowVersion,
					NodeKind:             "await", PlanID: "1/2", Status: "waiting",
				},
			}

			Expect(factory.Freeze(ctx, occurrences)).To(Succeed())

			stored, err := factory.ForRun(ctx, run.ID)
			Expect(err).ToNot(HaveOccurred())
			Expect(stored).To(HaveLen(2))
			Expect(stored[0].NodeID).To(Equal("implement"))
			Expect(stored[0].Status).To(Equal("succeeded"))
			Expect(stored[1].NodeID).To(Equal("approval"))
		})

		It("is idempotent so a retried finalization cannot double-write", func() {
			run := createAgentWorkflowRunForOccurrences()
			occurrences := []db.AgentWorkflowRunNodeOccurrence{{
				WorkflowRunID: run.ID, NodeID: "implement", Attempt: 1,
				TeamID: run.TeamID, WorkflowName: run.WorkflowName,
				WorkflowDefinitionID: run.WorkflowDefinitionID,
				WorkflowVersion:      run.WorkflowVersion,
				NodeKind:             "agent", PlanID: "1/1", Status: "succeeded",
			}}

			Expect(factory.Freeze(ctx, occurrences)).To(Succeed())
			Expect(factory.Freeze(ctx, occurrences)).To(Succeed())

			stored, err := factory.ForRun(ctx, run.ID)
			Expect(err).ToNot(HaveOccurred())
			Expect(stored).To(HaveLen(1))
		})

		It("rejects an in-place update of frozen history", func() {
			run := createAgentWorkflowRunForOccurrences()
			Expect(factory.Freeze(ctx, []db.AgentWorkflowRunNodeOccurrence{{
				WorkflowRunID: run.ID, NodeID: "implement", Attempt: 1,
				TeamID: run.TeamID, WorkflowName: run.WorkflowName,
				WorkflowDefinitionID: run.WorkflowDefinitionID,
				WorkflowVersion:      run.WorkflowVersion,
				NodeKind:             "agent", PlanID: "1/1", Status: "succeeded",
			}})).To(Succeed())

			_, err := dbConn.ExecContext(ctx,
				`UPDATE agent_workflow_run_node_occurrences SET status = 'failed' WHERE workflow_run_id = $1`,
				run.ID)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("immutable once frozen"))
		})
	})
})
```

Add a `createAgentWorkflowRunForOccurrences` helper alongside the other fixture helpers in the suite. Model it on the existing run-creation helper:

```bash
grep -rn "func createAgentWorkflowRun\|createWorkflowRun" atc/db/*_test.go | head -5
```

Reuse that helper directly if one exists, rather than writing a second fixture.

- [ ] **Step 2: Run the spec and confirm it fails**

```bash
ginkgo --focus="AgentWorkflowRunNodeOccurrencesFactory" ./atc/db/
```

Expected: FAIL — `undefined: db.AgentWorkflowRunNodeOccurrencesFactory`.

- [ ] **Step 3: Write the factory**

Create `atc/db/agent_workflow_run_node_occurrences_factory.go`:

```go
package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// AgentWorkflowRunNodeOccurrence is one frozen row of the node-occurrence
// projection.
type AgentWorkflowRunNodeOccurrence struct {
	WorkflowRunID        int64
	NodeID               string
	Attempt              int
	TeamID               int
	WorkflowName         string
	WorkflowDefinitionID int
	WorkflowVersion      int
	NodeKind             string
	ReusableNodeName     string
	ReusableNodeVersion  *int
	PlanID               string
	Status               string
	WaitID               *int64
	PublicationID        *int64
	StartedAt            *time.Time
	CompletedAt          *time.Time
	DurationSeconds      int
	CostUSD              float64
	FrozenAt             time.Time
}

//counterfeiter:generate . AgentWorkflowRunNodeOccurrencesFactory
type AgentWorkflowRunNodeOccurrencesFactory interface {
	// Freeze writes the projection for one terminal run. It is idempotent so a
	// retried finalization cannot double-write, and it never overwrites an
	// existing row, because frozen history is immutable.
	Freeze(context.Context, []AgentWorkflowRunNodeOccurrence) error
	ForRun(context.Context, int64) ([]AgentWorkflowRunNodeOccurrence, error)
}

type agentWorkflowRunNodeOccurrencesFactory struct {
	conn DbConn
}

func NewAgentWorkflowRunNodeOccurrencesFactory(conn DbConn) AgentWorkflowRunNodeOccurrencesFactory {
	return &agentWorkflowRunNodeOccurrencesFactory{conn: conn}
}

const agentWorkflowRunNodeOccurrenceColumns = `
	workflow_run_id, node_id, attempt, team_id, workflow_name,
	workflow_definition_id, workflow_version, node_kind, reusable_node_name,
	reusable_node_version, plan_id, status, wait_id, publication_id,
	started_at, completed_at, duration_seconds, cost_usd, frozen_at`

func (factory *agentWorkflowRunNodeOccurrencesFactory) Freeze(ctx context.Context, occurrences []AgentWorkflowRunNodeOccurrence) error {
	if len(occurrences) == 0 {
		return nil
	}

	tx, err := factory.conn.Begin()
	if err != nil {
		return fmt.Errorf("db: beginning node-occurrence freeze: %w", err)
	}
	defer Rollback(tx)

	for _, occurrence := range occurrences {
		var reusableVersion sql.NullInt64
		if occurrence.ReusableNodeVersion != nil {
			reusableVersion = sql.NullInt64{Int64: int64(*occurrence.ReusableNodeVersion), Valid: true}
		}

		_, err := tx.ExecContext(ctx, `
			INSERT INTO agent_workflow_run_node_occurrences (
				workflow_run_id, node_id, attempt, team_id, workflow_name,
				workflow_definition_id, workflow_version, node_kind,
				reusable_node_name, reusable_node_version, plan_id, status,
				wait_id, publication_id, started_at, completed_at,
				duration_seconds, cost_usd
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
			ON CONFLICT (workflow_run_id, node_id, attempt) DO NOTHING`,
			occurrence.WorkflowRunID, occurrence.NodeID, occurrence.Attempt,
			occurrence.TeamID, occurrence.WorkflowName,
			occurrence.WorkflowDefinitionID, occurrence.WorkflowVersion,
			occurrence.NodeKind, occurrence.ReusableNodeName, reusableVersion,
			occurrence.PlanID, occurrence.Status, occurrence.WaitID,
			occurrence.PublicationID, occurrence.StartedAt, occurrence.CompletedAt,
			occurrence.DurationSeconds, occurrence.CostUSD,
		)
		if err != nil {
			return fmt.Errorf("db: freezing node occurrence %s/%s: %w", occurrence.NodeID, occurrence.PlanID, err)
		}
	}

	return tx.Commit()
}

func (factory *agentWorkflowRunNodeOccurrencesFactory) ForRun(ctx context.Context, workflowRunID int64) ([]AgentWorkflowRunNodeOccurrence, error) {
	rows, err := factory.conn.QueryContext(ctx, `
		SELECT `+agentWorkflowRunNodeOccurrenceColumns+`
		FROM agent_workflow_run_node_occurrences
		WHERE workflow_run_id = $1
		ORDER BY plan_id, attempt`, workflowRunID)
	if err != nil {
		return nil, fmt.Errorf("db: reading node occurrences: %w", err)
	}
	defer Close(rows)

	var result []AgentWorkflowRunNodeOccurrence
	for rows.Next() {
		var occurrence AgentWorkflowRunNodeOccurrence
		var reusableVersion sql.NullInt64
		if err := rows.Scan(
			&occurrence.WorkflowRunID, &occurrence.NodeID, &occurrence.Attempt,
			&occurrence.TeamID, &occurrence.WorkflowName,
			&occurrence.WorkflowDefinitionID, &occurrence.WorkflowVersion,
			&occurrence.NodeKind, &occurrence.ReusableNodeName, &reusableVersion,
			&occurrence.PlanID, &occurrence.Status, &occurrence.WaitID,
			&occurrence.PublicationID, &occurrence.StartedAt, &occurrence.CompletedAt,
			&occurrence.DurationSeconds, &occurrence.CostUSD, &occurrence.FrozenAt,
		); err != nil {
			return nil, fmt.Errorf("db: scanning node occurrence: %w", err)
		}
		if reusableVersion.Valid {
			value := int(reusableVersion.Int64)
			occurrence.ReusableNodeVersion = &value
		}
		result = append(result, occurrence)
	}
	return result, rows.Err()
}
```

Confirm the connection type name and the `Rollback`/`Close` helper names against a neighbouring factory before running:

```bash
grep -n "conn DbConn\|func Rollback\|func Close" atc/db/agent_run_metrics_factory.go atc/db/open.go | head
```

- [ ] **Step 4: Run the spec and confirm it passes**

```bash
ginkgo --focus="AgentWorkflowRunNodeOccurrencesFactory" ./atc/db/
```

Expected: PASS, three specs.

- [ ] **Step 5: Call the freeze from run finalization**

In `agent/workflowrun/reconciler.go`, after the finalization that transitions a run to a terminal status succeeds, derive and freeze. Add the factory to the reconciler's dependencies, then:

```go
	occurrences, err := occurrence.Derive(occurrence.Sources{
		Run:             run,
		AttemptMetrics:  attemptMetrics,
		Waits:           waits,
		Publications:    publications,
		BuildStepStatus: buildStepStatus,
	})
	if err != nil {
		// A run must still finalize when its projection cannot be derived.
		// Losing history is bad; blocking terminalization is worse, because it
		// would strand the run in a nonterminal state forever.
		reconciler.logger.Error("derive-node-occurrences-failed", err, lager.Data{
			"workflow_run_id": run.ID.String(),
		})
	} else {
		rows := make([]db.AgentWorkflowRunNodeOccurrence, 0, len(occurrences))
		for _, item := range occurrences {
			rows = append(rows, db.AgentWorkflowRunNodeOccurrence{
				WorkflowRunID:        item.WorkflowRunID,
				NodeID:               item.NodeID,
				Attempt:              item.Attempt,
				TeamID:               item.TeamID,
				WorkflowName:         item.WorkflowName,
				WorkflowDefinitionID: item.WorkflowDefinitionID,
				WorkflowVersion:      item.WorkflowVersion,
				NodeKind:             item.NodeKind,
				ReusableNodeName:     item.ReusableNodeName,
				PlanID:               item.PlanID,
				Status:               string(item.Status),
				WaitID:               item.WaitID,
				PublicationID:        item.PublicationID,
				StartedAt:            item.StartedAt,
				CompletedAt:          item.CompletedAt,
				DurationSeconds:      item.DurationSeconds,
				CostUSD:              item.CostUSD,
			})
		}
		if err := reconciler.nodeOccurrences.Freeze(ctx, rows); err != nil {
			reconciler.logger.Error("freeze-node-occurrences-failed", err, lager.Data{
				"workflow_run_id": run.ID.String(),
			})
		}
	}
```

Locate the exact insertion point:

```bash
grep -n "Finalize\|TerminalStatus\|finalization" agent/workflowrun/reconciler.go | head -20
```

- [ ] **Step 6: Run the affected packages**

```bash
go test ./agent/workflowrun/... ./agent/workflow/... -count=1
```

Expected: PASS.

```bash
ginkgo --focus="AgentWorkflowRunNodeOccurrences" ./atc/db/
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add atc/db/agent_workflow_run_node_occurrences_factory.go atc/db/agent_workflow_run_node_occurrences_factory_test.go agent/workflowrun/reconciler.go
git commit -m "feat(workflowrun): freeze node occurrences at run finalization"
```

### Task B7: The production freezer

B6 wired the freeze call site against an interface. Nothing implements that
interface with real data — the plan's B6 sketch referenced source variables
that do not exist at the call site, and no earlier task builds the component
that produces them. Until this lands, the hook runs and freezes nothing, which
is worse than not having it, because it looks live.

**Files:**
- Create: `agent/workflowrun/occurrence/freezer.go`
- Test: `agent/workflowrun/occurrence/freezer_test.go`
- Modify: `atc/db/agent_publications_factory.go` (write `plan_id`)
- Modify: `atc/atccmd/command.go` (construct and inject)

- [ ] **Step 1: Give `agent_publication_occurrences.plan_id` a writer**

Migration `1773106157` adds the column; nothing populates it, so every publish
node would freeze as `pending`. Find where publication occurrences are
inserted, thread the plan ID through from the publishing step, and add a DB
spec asserting a freshly recorded occurrence carries it. Without this, one of
the five execution node kinds is permanently blank in the projection.

- [ ] **Step 2: Build the deterministic-task status reader**

This is the reason the projection exists. Agent steps have attempt metrics,
awaits have waits, publishes have publications — but a deterministic `task:`
step has **no durable record except build events**, which are GC-owned.

Read terminal step status from partitioned `pipeline_build_events` for the
run's build, keyed by plan ID. Confirm against the event schema which event
types carry per-step terminal status. Map to `occurrence.Status`, and treat a
step with no terminal event as `pending` rather than inventing a status.

Add a test that a task step with only build-event evidence still projects a
terminal status.

- [ ] **Step 3: Assemble the freezer**

Implement the interface B6 defined. For one terminal run it must gather:
attempt metrics by build, waits by run, publications by run, deterministic-task
status by plan ID, and the run's own compiled definition passed through
`graph.Build` for the execution-node set. Then call `occurrence.Derive` and
hand the rows to the factory's `Freeze`.

Load the definition by the run's **own** `workflow_version`, never the promoted
one — a run's projection must describe the revision that actually executed.

- [ ] **Step 4: Wire construction**

Construct it in `atc/atccmd/command.go` where the reconciler is built, and
inject it. Until this step the component is dead code.

- [ ] **Step 5: Prove it end to end**

An integration-level test that takes a workflow run through finalization and
asserts `agent_workflow_run_node_occurrences` contains one row per execution
node with the right statuses — including at least one deterministic task node,
which is the case that exists for no other reason.

- [ ] **Step 6: Commit**

```bash
git commit -m "feat(occurrence): assemble the production node-occurrence freezer"
```

- [ ] **Step 8: Phase B exit gate**

```bash
go test ./agent/... -count=1 && ginkgo ./atc/db/
```

Expected: PASS. The `atc/db` suite is roughly 1300 specs and takes two to three minutes.

---

## Phase C — API

### Task C1: Extend the run list filter

`AgentWorkflowRunListFilter` currently supports workflow, version, status, origin kind, origin reference, cursor, and limit. The overview needs window, scope, node participation, and indexed search.

**Files:**
- Modify: `atc/db/agent_workflow_run.go`
- Modify: `atc/db/agent_workflow_runs_factory.go`
- Test: `atc/db/agent_workflow_runs_factory_test.go`

- [ ] **Step 1: Write the failing DB spec**

Append to `atc/db/agent_workflow_runs_factory_test.go`, inside the existing `Describe("List")` block:

```go
		It("unions active runs older than the window with completed runs inside it", func() {
			old := createRunWithTimes(time.Now().Add(-30*24*time.Hour), nil, db.AgentWorkflowRunStatusRunning)
			recent := createRunWithTimes(time.Now().Add(-2*time.Hour), timePtr(time.Now().Add(-time.Hour)), db.AgentWorkflowRunStatusSucceeded)
			stale := createRunWithTimes(time.Now().Add(-30*24*time.Hour), timePtr(time.Now().Add(-29*24*time.Hour)), db.AgentWorkflowRunStatusSucceeded)

			since := time.Now().Add(-7 * 24 * time.Hour)
			runs, err := factory.List(ctx, db.AgentWorkflowRunListFilter{
				TeamID:            team.ID(),
				WorkflowName:      "small-fix",
				CompletedSince:    &since,
				IncludeActiveRuns: true,
			})
			Expect(err).ToNot(HaveOccurred())

			ids := runIDs(runs)
			Expect(ids).To(ContainElement(old.ID), "an active run older than the window must remain visible")
			Expect(ids).To(ContainElement(recent.ID))
			Expect(ids).ToNot(ContainElement(stale.ID), "a run completed before the window must be excluded")
		})

		It("excludes experiment origins from the operational scope", func() {
			operational := createRunWithOrigin("manual")
			experiment := createRunWithOrigin("experiment")

			runs, err := factory.List(ctx, db.AgentWorkflowRunListFilter{
				TeamID:       team.ID(),
				WorkflowName: "small-fix",
				Scope:        db.AgentWorkflowRunScopeOperational,
			})
			Expect(err).ToNot(HaveOccurred())

			ids := runIDs(runs)
			Expect(ids).To(ContainElement(operational.ID))
			Expect(ids).ToNot(ContainElement(experiment.ID))
		})

		It("filters to runs that reached a given node", func() {
			reached := createRunWithNodeOccurrence("implement", "succeeded")
			untouched := createRunWithNodeOccurrence("review", "succeeded")

			runs, err := factory.List(ctx, db.AgentWorkflowRunListFilter{
				TeamID:       team.ID(),
				WorkflowName: "small-fix",
				NodeID:       "implement",
			})
			Expect(err).ToNot(HaveOccurred())

			ids := runIDs(runs)
			Expect(ids).To(ContainElement(reached.ID))
			Expect(ids).ToNot(ContainElement(untouched.ID))
		})

		It("finds a run by its exact durable ID", func() {
			target := createRunWithOrigin("manual")

			runs, err := factory.List(ctx, db.AgentWorkflowRunListFilter{
				TeamID:       team.ID(),
				WorkflowName: "small-fix",
				Search:       target.ID.String(),
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(runIDs(runs)).To(Equal([]snapshot.WorkflowRunID{target.ID}))
		})
```

Add the fixture helpers `createRunWithTimes`, `createRunWithOrigin`, `createRunWithNodeOccurrence`, `runIDs`, and `timePtr` beside the existing helpers in that file, building on whatever run-creation helper already exists there.

- [ ] **Step 2: Run the specs and confirm they fail**

```bash
ginkgo --focus="unions active runs|excludes experiment origins|reached a given node|exact durable ID" ./atc/db/
```

Expected: FAIL — `unknown field CompletedSince in struct literal`.

- [ ] **Step 3: Extend the filter type**

In `atc/db/agent_workflow_run.go`, replace `AgentWorkflowRunListFilter` and add the scope type:

```go
// AgentWorkflowRunScope classifies a run population. Classification is an
// explicit server-side contract rather than a negative string comparison, so
// that a new origin kind must be classified deliberately when it is
// introduced.
type AgentWorkflowRunScope string

const (
	// AgentWorkflowRunScopeOperational is normal execution: ticket-associated,
	// manual, retry, resource-triggered, and follow-on runs.
	AgentWorkflowRunScopeOperational AgentWorkflowRunScope = "operational"
	// AgentWorkflowRunScopeExperiment is experiment cells only.
	AgentWorkflowRunScopeExperiment AgentWorkflowRunScope = "experiment"
	// AgentWorkflowRunScopeAll applies no scope filter.
	AgentWorkflowRunScopeAll AgentWorkflowRunScope = "all"
)

// ExperimentOriginKinds are the origin kinds classified as experiments. Adding
// an origin kind to the platform requires deciding, here, whether it is
// operational — mixing experiment cells into operational success, latency, or
// cost would distort the primary view.
var ExperimentOriginKinds = []string{"experiment"}

type AgentWorkflowRunListFilter struct {
	TeamID          int
	WorkflowName    string
	WorkflowVersion *int
	Status          AgentWorkflowRunStatus
	OriginKind      string
	OriginReference string

	// Scope classifies which run population to return. Empty means all.
	Scope AgentWorkflowRunScope

	// CompletedSince bounds terminal-run history by completed_at.
	CompletedSince *time.Time
	// IncludeActiveRuns unions in every nonterminal run regardless of age, so
	// changing the history window never changes the meaning of active state.
	IncludeActiveRuns bool

	// NodeID restricts results to runs that reached that semantic node.
	NodeID string
	// NodeStatus further restricts to a node occurrence status.
	NodeStatus string

	// Search matches an exact durable run ID, an exact or prefixed ticket
	// reference, or an exact snapshot ID bound to the run. It never scans
	// unbounded JSON.
	Search string

	Before *pagination.Cursor
	Limit  int
}
```

- [ ] **Step 4: Implement the predicates in the factory**

In `atc/db/agent_workflow_runs_factory.go`, inside the `List` query construction, add after the existing origin predicates:

```go
	switch filter.Scope {
	case AgentWorkflowRunScopeOperational:
		query = query.Where(sq.NotEq{"r.origin_kind": ExperimentOriginKinds})
	case AgentWorkflowRunScopeExperiment:
		query = query.Where(sq.Eq{"r.origin_kind": ExperimentOriginKinds})
	}

	// History is bounded by completed_at while active runs are unioned in
	// regardless of age. Without the OR, a long-running run created before the
	// window would silently vanish from the page whose primary job is showing
	// what needs attention now.
	if filter.CompletedSince != nil {
		if filter.IncludeActiveRuns {
			query = query.Where(sq.Or{
				sq.GtOrEq{"r.completed_at": *filter.CompletedSince},
				sq.Eq{"r.completed_at": nil},
			})
		} else {
			query = query.Where(sq.GtOrEq{"r.completed_at": *filter.CompletedSince})
		}
	}

	if filter.NodeID != "" {
		exists := `EXISTS (
			SELECT 1 FROM agent_workflow_run_node_occurrences o
			WHERE o.workflow_run_id = r.id AND o.node_id = ?`
		args := []any{filter.NodeID}
		if filter.NodeStatus != "" {
			exists += ` AND o.status = ?`
			args = append(args, filter.NodeStatus)
		}
		exists += `)`
		query = query.Where(exists, args...)
	}

	if search := strings.TrimSpace(filter.Search); search != "" {
		if runID, err := strconv.ParseInt(search, 10, 64); err == nil {
			query = query.Where(sq.Or{
				sq.Eq{"r.id": runID},
				sq.Expr(`EXISTS (SELECT 1 FROM agent_workflow_run_snapshots s
					WHERE s.workflow_run_id = r.id AND s.snapshot_id = ?)`, runID),
			})
		} else {
			query = query.Where(sq.Like{"r.ticket_reference": search + "%"})
		}
	}
```

The `ticket_reference` predicate references a column Phase F adds. Until Phase F lands, guard it:

```go
		} else if agentWorkflowRunsHaveTicketReference {
			query = query.Where(sq.Like{"r.ticket_reference": search + "%"})
		}
```

with `const agentWorkflowRunsHaveTicketReference = false` in the same file, flipped to `true` by Phase F Task F1. Delete the constant and the guard once F lands.

- [ ] **Step 5: Add the supporting index**

Append to `atc/db/migration/migrations/1773106157_create_workflow_run_node_occurrences.up.sql`:

```sql
-- The overview's dominant access path: one workflow's history window, newest
-- first, with active runs unioned in.
CREATE INDEX agent_workflow_runs_team_workflow_completed
    ON agent_workflow_runs (team_id, workflow_name, completed_at DESC NULLS FIRST, id DESC);
```

and to the down migration:

```sql
DROP INDEX IF EXISTS agent_workflow_runs_team_workflow_completed;
```

Only do this if `1773106157` has not yet been applied anywhere. If it has, add `1773106159` instead.

- [ ] **Step 6: Run the specs and confirm they pass**

```bash
ginkgo --focus="AgentWorkflowRunsFactory" ./atc/db/
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add atc/db/agent_workflow_run.go atc/db/agent_workflow_runs_factory.go atc/db/agent_workflow_runs_factory_test.go atc/db/migration/migrations/1773106157_create_workflow_run_node_occurrences.up.sql atc/db/migration/migrations/1773106157_create_workflow_run_node_occurrences.down.sql
git commit -m "feat(db): window, scope, node, and indexed search filters for workflow runs"
```

### Graph-derivation failure is a degraded page, not a 500

Decided at the end of Phase A. `graph.Build` returns an error for any step kind
it does not recognise, so adding a new step type to `atc` without updating
`agent/workflow/graph/build.go` would take the whole overview and run page
blank at request time. Nothing connects those two edits at compile time — the
builder uses a type switch rather than `atc.StepVisitor`.

Rather than refactor the builder onto `StepVisitor`, the API absorbs it: when
`graph.Build` fails, the overview and run-graph endpoints still return their
run data with the graph omitted and an explicit
`"graph_unavailable": true` field, logging the cause. The run list, node
state, and every other affordance keep working; only the canvas is missing.

This matches what the graph already is — a lossy semantic projection that
deliberately omits untyped artifact flow — and it keeps one unrecognised step
from destroying an otherwise usable page. Elm must render the
graph-unavailable state rather than assuming `graph` is present.

Anyone adding a step type to `atc` must update `agent/workflow/graph/build.go`;
there is no compiler enforcement of that today.

### Task C2: Overview endpoint

**Files:**
- Create: `agent/api/workflowoverview/types.go`
- Create: `agent/api/workflowoverview/handler.go`
- Test: `agent/api/workflowoverview/handler_test.go`
- Modify: `atc/routes.go`, `atc/api/handler.go`

- [ ] **Step 1: Write the failing handler test**

Create `agent/api/workflowoverview/handler_test.go`:

```go
package workflowoverview_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/api/workflowoverview"
)

func TestOverviewLabelsItsWindowExplicitly(t *testing.T) {
	handler := newTestHandler(t)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/?window=7d", nil)
	request.Form = map[string][]string{":workflow_name": {"small-fix"}, "window": {"7d"}}
	handler.Overview(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body)
	}

	var response workflowoverview.Response
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	if response.Window.Kind != "7d" {
		t.Fatalf("expected the window kind to be echoed, got %q", response.Window.Kind)
	}
	if !response.Window.IncludesActiveBeforeWindow {
		t.Fatal("the response must state that active runs bypass the window")
	}
	if response.Window.To.Sub(response.Window.From) < 6*24*time.Hour {
		t.Fatalf("expected roughly a seven-day window, got %s", response.Window.To.Sub(response.Window.From))
	}
}

func TestOverviewDefaultsToSevenDays(t *testing.T) {
	handler := newTestHandler(t)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Form = map[string][]string{":workflow_name": {"small-fix"}}
	handler.Overview(recorder, request)

	var response workflowoverview.Response
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if response.Window.Kind != "7d" {
		t.Fatalf("expected the default window to be 7d, got %q", response.Window.Kind)
	}
}

func TestOverviewRejectsUnknownWindow(t *testing.T) {
	handler := newTestHandler(t)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/?window=90d", nil)
	request.Form = map[string][]string{":workflow_name": {"small-fix"}, "window": {"90d"}}
	handler.Overview(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an unsupported window, got %d", recorder.Code)
	}
}

func TestOverviewNeverEmitsOneAggregateStatus(t *testing.T) {
	handler := newTestHandlerWithMixedNodeState(t)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Form = map[string][]string{":workflow_name": {"small-fix"}}
	handler.Overview(recorder, request)

	var raw map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	states, _ := raw["node_state"].([]any)
	if len(states) == 0 {
		t.Fatal("expected node state entries")
	}
	for _, entry := range states {
		object := entry.(map[string]any)
		if _, present := object["status"]; present {
			t.Fatal("node state must not collapse mixed concurrent runs into one status field")
		}
		if _, present := object["active"]; !present {
			t.Fatal("node state must report active counts")
		}
		if _, present := object["history"]; !present {
			t.Fatal("node state must report windowed history counts")
		}
	}
}

func TestOverviewUnpromotedWorkflowReportsLatestVersion(t *testing.T) {
	handler := newTestHandlerWithNoPromotedVersion(t)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Form = map[string][]string{":workflow_name": {"small-fix"}}
	handler.Overview(recorder, request)

	var response workflowoverview.Response
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if response.Workflow.HasPromotedVersion {
		t.Fatal("expected the workflow to report no promoted version")
	}
	if response.Workflow.GraphVersion == 0 {
		t.Fatal("expected the latest imported version to supply the graph")
	}
	if len(response.Graph.Nodes) == 0 {
		t.Fatal("an unpromoted workflow must still render a graph")
	}
}
```

Write `newTestHandler`, `newTestHandlerWithMixedNodeState`, and `newTestHandlerWithNoPromotedVersion` as small fakes in the same file, following the pattern in `agent/api/workflowruns/handler_test.go`:

```bash
sed -n '1,80p' agent/api/workflowruns/handler_test.go
```

- [ ] **Step 2: Run the tests and confirm they fail**

```bash
go test ./agent/api/workflowoverview/ -count=1
```

Expected: FAIL — the package does not exist.

- [ ] **Step 3: Write the response types**

Create `agent/api/workflowoverview/types.go`:

```go
// Package workflowoverview serves the aggregate state a workflow page needs:
// the promoted revision's semantic graph, per-node state across all relevant
// runs, and revision boundaries. It deliberately never emits one ambiguous
// aggregate node status, because a node representing several concurrent runs
// has no single canonical state.
package workflowoverview

import (
	"time"

	"github.com/concourse/concourse/agent/workflow/graph"
)

type Response struct {
	Workflow  Workflow            `json:"workflow"`
	Window    Window              `json:"window"`
	Graph     graph.Graph         `json:"graph"`
	NodeState []NodeState         `json:"node_state"`
	Revisions []RevisionBoundary  `json:"revision_boundaries"`
	// HasHistoricalOnlyNodes reports that runs in the window touched nodes the
	// promoted graph no longer contains. The UI surfaces a discovery
	// affordance; it never renders a union graph.
	HasHistoricalOnlyNodes bool `json:"has_historical_only_nodes"`
}

type Workflow struct {
	Name               string `json:"name"`
	HasPromotedVersion bool   `json:"has_promoted_version"`
	// GraphVersion is the revision that supplied the rendered graph: the
	// promoted version when one exists, otherwise the latest imported one.
	GraphVersion int    `json:"graph_version"`
	ContentHash  string `json:"content_hash"`
}

type Window struct {
	Kind string    `json:"kind"`
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
	// IncludesActiveBeforeWindow is always true and is stated rather than
	// implied: active runs are represented regardless of how long ago they
	// began, so changing the window never changes the meaning of active state.
	IncludesActiveBeforeWindow bool `json:"includes_active_before_window"`
}

// ActiveCounts are the nonterminal occurrences of one node right now.
type ActiveCounts struct {
	Running int `json:"running"`
	Waiting int `json:"waiting"`
	Pending int `json:"pending"`
}

// HistoryCounts are the terminal occurrences of one node inside the window.
type HistoryCounts struct {
	Succeeded int `json:"succeeded"`
	Failed    int `json:"failed"`
	Errored   int `json:"errored"`
	Aborted   int `json:"aborted"`
	Skipped   int `json:"skipped"`
}

// NodeState reports active and windowed-history counts separately, plus the
// resolved attention state. There is deliberately no single `status` field.
type NodeState struct {
	NodeID  string        `json:"node_id"`
	Active  ActiveCounts  `json:"active"`
	History HistoryCounts `json:"history"`
	// NeedsAttention is the retry-chain-resolved answer to "does this node
	// need action now". A successful retry clears an earlier failure here
	// while leaving that failure in History.
	NeedsAttention bool `json:"needs_attention"`
	// HasWindowActivity distinguishes a node with no data from a healthy one,
	// so the UI can render no-data distinctly rather than as green.
	HasWindowActivity bool `json:"has_window_activity"`
}

type RevisionBoundary struct {
	Version     int        `json:"version"`
	PromotedAt  *time.Time `json:"promoted_at"`
	FirstRunID  string     `json:"first_run_id"`
	FirstRunAt  time.Time  `json:"first_run_at"`
}
```

- [ ] **Step 4: Write the handler**

Create `agent/api/workflowoverview/handler.go`:

```go
package workflowoverview

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// windows is the complete supported set. Custom and adaptive windows are out
// of scope: one explicit global control keeps the graph, the selected node
// detail, and the run list on a single shared scope.
var windows = map[string]time.Duration{
	"24h": 24 * time.Hour,
	"7d":  7 * 24 * time.Hour,
	"30d": 30 * 24 * time.Hour,
}

const defaultWindow = "7d"

func (handler *Handler) Overview(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue(":workflow_name")

	kind := r.FormValue("window")
	if kind == "" {
		kind = defaultWindow
	}
	duration, supported := windows[kind]
	if !supported {
		http.Error(w, fmt.Sprintf("unsupported window %q; use 24h, 7d, or 30d", kind), http.StatusBadRequest)
		return
	}

	now := handler.now()
	window := Window{
		Kind:                       kind,
		From:                       now.Add(-duration),
		To:                         now,
		IncludesActiveBeforeWindow: true,
	}

	response, err := handler.build(r.Context(), name, window)
	if err != nil {
		handler.logger.Error("build-overview-failed", err)
		http.Error(w, "failed to build workflow overview", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		handler.logger.Error("encode-overview-failed", err)
	}
}
```

Then implement `build` to: resolve the promoted definition (falling back to the latest imported version, setting `HasPromotedVersion` accordingly); call `graph.Build` on its compiled function; list runs with `Scope: operational`, `CompletedSince: &window.From`, `IncludeActiveRuns: true`; derive occurrences for those runs; group them into retry closures via `RetryOfWorkflowRunID`; call `occurrence.ResolveEffective` per closure; and aggregate into `NodeState`. Set `HasHistoricalOnlyNodes` when any occurrence's `node_id` is absent from the built graph.

- [ ] **Step 5: Register the route**

In `atc/routes.go`, add to the constant block:

```go
	GetAgentWorkflowOverview = "GetAgentWorkflowOverview"
```

and to the route table, **above** the `:workflow_name` catch-alls so it is not shadowed:

```go
	{Path: "/api/v1/agent/workflows/:workflow_name/overview", Method: "GET", Name: GetAgentWorkflowOverview},
```

In `atc/api/handler.go`, add the handler parameter and the dispatch entry:

```go
		atc.GetAgentWorkflowOverview: http.HandlerFunc(workflowOverviewHandlers.Overview),
```

Rata panics on duplicate routes at startup, so a copy-pasted path fails loudly rather than silently.

- [ ] **Step 6: Run the tests and confirm they pass**

```bash
go test ./agent/api/... -count=1
```

Expected: PASS.

```bash
go test ./atc/api/ -count=1
```

Expected: PASS — this catches route registration mistakes.

- [ ] **Step 7: Commit**

```bash
git add agent/api/workflowoverview/ atc/routes.go atc/api/handler.go
git commit -m "feat(api): workflow overview endpoint with explicit window semantics"
```

### Task C3: Run list query parameters

**Files:**
- Modify: `agent/api/workflowruns/handler.go`
- Test: `agent/api/workflowruns/handler_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/api/workflowruns/handler_test.go`:

```go
func TestListParsesOverviewQueryParameters(t *testing.T) {
	store := &recordingRunStore{}
	handler := newHandlerWithStore(t, store)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Form = map[string][]string{
		":workflow_name": {"small-fix"},
		"window":         {"24h"},
		"scope":          {"operational"},
		"node":           {"implement"},
		"node_status":    {"failed"},
		"q":              {"1234"},
	}
	handler.List(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body)
	}

	filter := store.lastFilter
	if filter.Scope != db.AgentWorkflowRunScopeOperational {
		t.Fatalf("expected operational scope, got %q", filter.Scope)
	}
	if filter.CompletedSince == nil {
		t.Fatal("expected a completed-since bound from the window")
	}
	if !filter.IncludeActiveRuns {
		t.Fatal("active runs must always be unioned in")
	}
	if filter.NodeID != "implement" || filter.NodeStatus != "failed" {
		t.Fatalf("unexpected node filter: %+v", filter)
	}
	if filter.Search != "1234" {
		t.Fatalf("expected the search term to reach the store, got %q", filter.Search)
	}
}

func TestListDefaultsToOperationalScope(t *testing.T) {
	store := &recordingRunStore{}
	handler := newHandlerWithStore(t, store)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Form = map[string][]string{":workflow_name": {"small-fix"}}
	handler.List(recorder, request)

	if store.lastFilter.Scope != db.AgentWorkflowRunScopeOperational {
		t.Fatalf("experiments must be excluded by default, got scope %q", store.lastFilter.Scope)
	}
}
```

`recordingRunStore` implements `RunStore` and captures the filter. Model `newHandlerWithStore` on the existing constructor helper in that file.

- [ ] **Step 2: Run the tests and confirm they fail**

```bash
go test ./agent/api/workflowruns/ -run TestList -count=1
```

Expected: FAIL — the parameters are ignored, so `Scope` is empty.

- [ ] **Step 3: Parse the parameters**

In `agent/api/workflowruns/handler.go`, inside `List`, before calling the store:

```go
	scope := db.AgentWorkflowRunScope(r.FormValue("scope"))
	switch scope {
	case db.AgentWorkflowRunScopeOperational, db.AgentWorkflowRunScopeExperiment, db.AgentWorkflowRunScopeAll:
	case "":
		// Experiment runs are excluded from the default operational view.
		// Mixing experiment cells into normal success, latency, or cost would
		// distort the primary evaluation.
		scope = db.AgentWorkflowRunScopeOperational
	default:
		http.Error(w, fmt.Sprintf("unsupported scope %q; use operational, experiment, or all", scope), http.StatusBadRequest)
		return
	}
	filter.Scope = scope

	if kind := r.FormValue("window"); kind != "" {
		duration, supported := map[string]time.Duration{
			"24h": 24 * time.Hour,
			"7d":  7 * 24 * time.Hour,
			"30d": 30 * 24 * time.Hour,
		}[kind]
		if !supported {
			http.Error(w, fmt.Sprintf("unsupported window %q; use 24h, 7d, or 30d", kind), http.StatusBadRequest)
			return
		}
		since := handler.now().Add(-duration)
		filter.CompletedSince = &since
	}
	filter.IncludeActiveRuns = true

	filter.NodeID = r.FormValue("node")
	filter.NodeStatus = r.FormValue("node_status")
	filter.Search = r.FormValue("q")
```

- [ ] **Step 4: Run the tests and confirm they pass**

```bash
go test ./agent/api/workflowruns/ -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/api/workflowruns/
git commit -m "feat(api): window, scope, node, and search parameters on the run list"
```

- [ ] **Step 6: Phase C exit gate**

```bash
go test ./agent/... ./atc/api/ -count=1 && make test-integration
```

Expected: PASS, 24/24 integration specs.

---

## Phase D — workflow overview UI

Elm tests run with `yarn test`, which invokes `elm-test` in `web/elm`. Run a single suite with `cd web/elm && elm-test tests/AgentGraphLayoutTests.elm`.

### Task D1: Graph model and decoder

**Files:**
- Create: `web/elm/src/AgentGraph/Model.elm`
- Create: `web/elm/src/AgentGraph/Decoder.elm`
- Test: `web/elm/tests/AgentGraphDecoderTests.elm`

- [ ] **Step 1: Write the failing test**

Create `web/elm/tests/AgentGraphDecoderTests.elm`:

```elm
module AgentGraphDecoderTests exposing (all)

import AgentGraph.Decoder as Decoder
import AgentGraph.Model as Model
import Expect
import Json.Decode
import Test exposing (Test, describe, test)


all : Test
all =
    describe "agent graph decoder"
        [ test "decodes nodes, kinds, and edges" <|
            \_ ->
                Json.Decode.decodeString Decoder.graph payload
                    |> Expect.equal
                        (Ok
                            { nodes =
                                [ { id = "repository"
                                  , kind = Model.Input
                                  , displayName = "repository"
                                  , typeRef = "repository/v1"
                                  , decorations = []
                                  , reusableNode = Nothing
                                  }
                                , { id = "implement"
                                  , kind = Model.Agent
                                  , displayName = "implement"
                                  , typeRef = ""
                                  , decorations = [ Model.Retry ]
                                  , reusableNode = Nothing
                                  }
                                ]
                            , edges =
                                [ { from = "repository"
                                  , to = "implement"
                                  , portName = "repository"
                                  , typeRef = "repository/v1"
                                  }
                                ]
                            }
                        )
        , test "fails on an unknown node kind rather than silently dropping it" <|
            \_ ->
                Json.Decode.decodeString Decoder.graph unknownKindPayload
                    |> Expect.err
        ]


payload : String
payload =
    """
    { "nodes":
        [ {"id":"repository","kind":"input","display_name":"repository","type_ref":"repository/v1"}
        , {"id":"implement","kind":"agent","display_name":"implement","decorations":["retry"]}
        ]
    , "edges":
        [ {"from":"repository","to":"implement","port_name":"repository","type_ref":"repository/v1"}
        ]
    }
    """


unknownKindPayload : String
unknownKindPayload =
    """{"nodes":[{"id":"x","kind":"prompt","display_name":"x"}],"edges":[]}"""
```

- [ ] **Step 2: Run the test and confirm it fails**

```bash
cd web/elm && elm-test tests/AgentGraphDecoderTests.elm
```

Expected: FAIL — module `AgentGraph.Decoder` not found.

- [ ] **Step 3: Write the model**

Create `web/elm/src/AgentGraph/Model.elm`:

```elm
module AgentGraph.Model exposing
    ( Decoration(..)
    , Edge
    , Graph
    , Node
    , NodeKind(..)
    , ReusableNode
    , findNode
    )

{-| The semantic workflow graph, shared by the workflow overview and the
individual run page. Prompts, configs, and broker profiles are absent by
construction: the server never sends them, and this type could not hold them.
-}


type NodeKind
    = Input
    | ResourceSource
    | Load
    | Agent
    | Task
    | Await
    | Publish
    | Output


{-| Control machinery that decorates a node instead of becoming one.
-}
type Decoration
    = Retry
    | Timeout
    | Try
    | Ensure
    | OnFailure
    | OnError
    | OnAbort
    | OnSuccess


type alias ReusableNode =
    { name : String
    , version : Int
    }


type alias Node =
    { id : String
    , kind : NodeKind
    , displayName : String
    , typeRef : String
    , decorations : List Decoration
    , reusableNode : Maybe ReusableNode
    }


type alias Edge =
    { from : String
    , to : String
    , portName : String
    , typeRef : String
    }


type alias Graph =
    { nodes : List Node
    , edges : List Edge
    }


findNode : String -> Graph -> Maybe Node
findNode id graph =
    List.filter (\node -> node.id == id) graph.nodes |> List.head
```

- [ ] **Step 4: Write the decoder**

Create `web/elm/src/AgentGraph/Decoder.elm`:

```elm
module AgentGraph.Decoder exposing (graph, node)

import AgentGraph.Model as Model
import Json.Decode
import Json.Decode.Pipeline exposing (optional, required)


graph : Json.Decode.Decoder Model.Graph
graph =
    Json.Decode.succeed Model.Graph
        |> optional "nodes" (Json.Decode.list node) []
        |> optional "edges" (Json.Decode.list edge) []


node : Json.Decode.Decoder Model.Node
node =
    Json.Decode.succeed Model.Node
        |> required "id" Json.Decode.string
        |> required "kind" nodeKind
        |> required "display_name" Json.Decode.string
        |> optional "type_ref" Json.Decode.string ""
        |> optional "decorations" (Json.Decode.list decoration) []
        |> optional "reusable_node_name" (Json.Decode.maybe reusableNode) Nothing


reusableNode : Json.Decode.Decoder Model.ReusableNode
reusableNode =
    Json.Decode.map2 Model.ReusableNode
        (Json.Decode.field "reusable_node_name" Json.Decode.string)
        (Json.Decode.field "reusable_node_version" Json.Decode.int)


edge : Json.Decode.Decoder Model.Edge
edge =
    Json.Decode.succeed Model.Edge
        |> required "from" Json.Decode.string
        |> required "to" Json.Decode.string
        |> required "port_name" Json.Decode.string
        |> optional "type_ref" Json.Decode.string ""


{-| An unrecognised kind fails the decode. Silently dropping it would render a
graph missing a node, which is worse than an explicit error.
-}
nodeKind : Json.Decode.Decoder Model.NodeKind
nodeKind =
    Json.Decode.string
        |> Json.Decode.andThen
            (\raw ->
                case raw of
                    "input" ->
                        Json.Decode.succeed Model.Input

                    "resource_source" ->
                        Json.Decode.succeed Model.ResourceSource

                    "load" ->
                        Json.Decode.succeed Model.Load

                    "agent" ->
                        Json.Decode.succeed Model.Agent

                    "task" ->
                        Json.Decode.succeed Model.Task

                    "await" ->
                        Json.Decode.succeed Model.Await

                    "publish" ->
                        Json.Decode.succeed Model.Publish

                    "output" ->
                        Json.Decode.succeed Model.Output

                    other ->
                        Json.Decode.fail ("unknown node kind: " ++ other)
            )


decoration : Json.Decode.Decoder Model.Decoration
decoration =
    Json.Decode.string
        |> Json.Decode.andThen
            (\raw ->
                case raw of
                    "retry" ->
                        Json.Decode.succeed Model.Retry

                    "timeout" ->
                        Json.Decode.succeed Model.Timeout

                    "try" ->
                        Json.Decode.succeed Model.Try

                    "ensure" ->
                        Json.Decode.succeed Model.Ensure

                    "on_failure" ->
                        Json.Decode.succeed Model.OnFailure

                    "on_error" ->
                        Json.Decode.succeed Model.OnError

                    "on_abort" ->
                        Json.Decode.succeed Model.OnAbort

                    "on_success" ->
                        Json.Decode.succeed Model.OnSuccess

                    other ->
                        Json.Decode.fail ("unknown decoration: " ++ other)
            )
```

Confirm `Json.Decode.Pipeline` is already a dependency:

```bash
grep -n "json-decode-pipeline" web/elm/elm.json
```

If it is absent, use plain `Json.Decode.map6` and `Json.Decode.map4` instead of adding a dependency.

- [ ] **Step 5: Run the test and confirm it passes**

```bash
cd web/elm && elm-test tests/AgentGraphDecoderTests.elm
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add web/elm/src/AgentGraph/ web/elm/tests/AgentGraphDecoderTests.elm
git commit -m "feat(web): agent graph model and decoder"
```

### Task D2: Pure layout

This is the task that earns the Elm-native decision: layout is a pure function, so it can be fuzz-tested. `web/public/graph.mjs` is the reference implementation for the rank and column algorithm — read it before writing this.

**Files:**
- Create: `web/elm/src/AgentGraph/Layout.elm`
- Test: `web/elm/tests/AgentGraphLayoutTests.elm`

- [ ] **Step 1: Write the failing tests**

Create `web/elm/tests/AgentGraphLayoutTests.elm`:

```elm
module AgentGraphLayoutTests exposing (all)

import AgentGraph.Layout as Layout
import AgentGraph.Model as Model
import Expect
import Fuzz
import Set
import Test exposing (Test, describe, fuzz, test)


all : Test
all =
    describe "agent graph layout"
        [ test "assigns rank 0 to nodes with no incoming edges" <|
            \_ ->
                Layout.layout chain
                    |> .nodes
                    |> List.filter (\n -> n.node.id == "repository")
                    |> List.map .rank
                    |> Expect.equal [ 0 ]
        , test "ranks a consumer after its producer" <|
            \_ ->
                let
                    ranks =
                        Layout.layout chain
                            |> .nodes
                            |> List.map (\n -> ( n.node.id, n.rank ))
                in
                Expect.equal
                    (List.sortBy Tuple.first ranks)
                    [ ( "change", 2 ), ( "implement", 1 ), ( "repository", 0 ) ]
        , test "places parallel siblings at the same rank in different columns" <|
            \_ ->
                let
                    laid =
                        Layout.layout parallel

                    siblings =
                        laid.nodes
                            |> List.filter (\n -> List.member n.node.id [ "left", "right" ])
                in
                Expect.all
                    [ \s -> List.map .rank s |> Expect.equal [ 1, 1 ]
                    , \s -> List.map .column s |> Set.fromList >> Set.size >> Expect.equal 2
                    ]
                    siblings
        , test "is deterministic" <|
            \_ ->
                Expect.equal (Layout.layout parallel) (Layout.layout parallel)
        , fuzz (Fuzz.intRange 1 12) "never overlaps two nodes in one rank" <|
            \size ->
                let
                    laid =
                        Layout.layout (fanOut size)

                    positions =
                        laid.nodes |> List.map (\n -> ( n.rank, n.column ))
                in
                Expect.equal (List.length positions) (Set.size (Set.fromList positions))
        , fuzz (Fuzz.intRange 1 12) "assigns every node a position" <|
            \size ->
                let
                    graph =
                        fanOut size
                in
                Layout.layout graph
                    |> .nodes
                    |> List.length
                    |> Expect.equal (List.length graph.nodes)
        ]


agentNode : String -> Model.Node
agentNode id =
    { id = id
    , kind = Model.Agent
    , displayName = id
    , typeRef = ""
    , decorations = []
    , reusableNode = Nothing
    }


chain : Model.Graph
chain =
    { nodes =
        [ { id = "repository", kind = Model.Input, displayName = "repository", typeRef = "repository/v1", decorations = [], reusableNode = Nothing }
        , agentNode "implement"
        , { id = "change", kind = Model.Output, displayName = "change", typeRef = "repository-change/v1", decorations = [], reusableNode = Nothing }
        ]
    , edges =
        [ { from = "repository", to = "implement", portName = "repository", typeRef = "" }
        , { from = "implement", to = "change", portName = "draft", typeRef = "" }
        ]
    }


parallel : Model.Graph
parallel =
    { nodes =
        [ { id = "repository", kind = Model.Input, displayName = "repository", typeRef = "", decorations = [], reusableNode = Nothing }
        , agentNode "left"
        , agentNode "right"
        ]
    , edges =
        [ { from = "repository", to = "left", portName = "repository", typeRef = "" }
        , { from = "repository", to = "right", portName = "repository", typeRef = "" }
        ]
    }


fanOut : Int -> Model.Graph
fanOut size =
    let
        leaves =
            List.range 1 size |> List.map (\i -> agentNode ("leaf-" ++ String.fromInt i))
    in
    { nodes =
        { id = "root", kind = Model.Input, displayName = "root", typeRef = "", decorations = [], reusableNode = Nothing }
            :: leaves
    , edges =
        leaves
            |> List.map (\leaf -> { from = "root", to = leaf.id, portName = "p", typeRef = "" })
    }
```

- [ ] **Step 2: Run the tests and confirm they fail**

```bash
cd web/elm && elm-test tests/AgentGraphLayoutTests.elm
```

Expected: FAIL — module `AgentGraph.Layout` not found.

- [ ] **Step 3: Write the layout**

Create `web/elm/src/AgentGraph/Layout.elm`:

```elm
module AgentGraph.Layout exposing
    ( LaidOut
    , LaidOutNode
    , layout
    , nodeHeight
    , nodeWidth
    )

{-| Pure layered layout for the semantic workflow graph.

Ranks come from longest-path assignment over the dataflow edges: a node's rank
is one more than the deepest rank among its producers. Columns break ties
within a rank by the node's stable identity, which makes the result
deterministic — the same graph always lays out identically, so the canvas does
not shuffle between polls.

The rank and column conventions follow `web/public/graph.mjs`, the original
Concourse pipeline layout, so the shape reads the same way.
-}

import AgentGraph.Model as Model
import Dict exposing (Dict)


nodeWidth : Float
nodeWidth =
    200


nodeHeight : Float
nodeHeight =
    56


rankSpacing : Float
rankSpacing =
    120


columnSpacing : Float
columnSpacing =
    24


type alias LaidOutNode =
    { node : Model.Node
    , rank : Int
    , column : Int
    , x : Float
    , y : Float
    }


type alias LaidOut =
    { nodes : List LaidOutNode
    , edges : List Model.Edge
    , width : Float
    , height : Float
    }


layout : Model.Graph -> LaidOut
layout graph =
    let
        ranks =
            assignRanks graph

        ranked =
            graph.nodes
                |> List.map (\node -> ( Dict.get node.id ranks |> Maybe.withDefault 0, node ))

        columns =
            assignColumns ranked

        laidOut =
            ranked
                |> List.map
                    (\( rank, node ) ->
                        let
                            column =
                                Dict.get node.id columns |> Maybe.withDefault 0
                        in
                        { node = node
                        , rank = rank
                        , column = column
                        , x = toFloat rank * (nodeWidth + rankSpacing)
                        , y = toFloat column * (nodeHeight + columnSpacing)
                        }
                    )
                |> List.sortBy (\item -> ( item.rank, item.column ))
    in
    { nodes = laidOut
    , edges = graph.edges
    , width = extent .x nodeWidth laidOut
    , height = extent .y nodeHeight laidOut
    }


{-| Longest-path ranking. Iterating node-count times is enough to propagate
depth through any acyclic graph, and it terminates on a cyclic one rather than
looping forever — a cycle would be a server bug, and hanging the page is not an
acceptable way to report it.
-}
assignRanks : Model.Graph -> Dict String Int
assignRanks graph =
    let
        initial =
            graph.nodes |> List.map (\node -> ( node.id, 0 )) |> Dict.fromList

        relax ranks =
            graph.edges
                |> List.foldl
                    (\edge acc ->
                        let
                            fromRank =
                                Dict.get edge.from acc |> Maybe.withDefault 0

                            toRank =
                                Dict.get edge.to acc |> Maybe.withDefault 0
                        in
                        if toRank < fromRank + 1 then
                            Dict.insert edge.to (fromRank + 1) acc

                        else
                            acc
                    )
                    ranks
    in
    List.range 1 (List.length graph.nodes)
        |> List.foldl (\_ ranks -> relax ranks) initial


{-| Columns are assigned in stable identity order within each rank, so two
nodes never share a position and the layout does not depend on list order.
The fold carries a per-rank counter beside the result, which is what keeps
siblings in one rank from colliding.
-}
assignColumns : List ( Int, Model.Node ) -> Dict String Int
assignColumns ranked =
    ranked
        |> List.sortBy (\( rank, node ) -> ( rank, node.id ))
        |> List.foldl
            (\( rank, node ) ( counters, result ) ->
                let
                    column =
                        Dict.get rank counters |> Maybe.withDefault 0
                in
                ( Dict.insert rank (column + 1) counters
                , Dict.insert node.id column result
                )
            )
            ( Dict.empty, Dict.empty )
        |> Tuple.second


extent : (LaidOutNode -> Float) -> Float -> List LaidOutNode -> Float
extent accessor size nodes =
    nodes
        |> List.map (\node -> accessor node + size)
        |> List.maximum
        |> Maybe.withDefault 0
```

`Dict` keys must be comparable, so `assignColumns` keys its counter dictionary by `Int` rank and its result by `String` node id — two separate dictionaries carried through one fold, rather than one dictionary holding mixed value types.

- [ ] **Step 4: Run the tests and confirm they pass**

```bash
cd web/elm && elm-test tests/AgentGraphLayoutTests.elm
```

Expected: PASS, six tests including two fuzz tests.

- [ ] **Step 5: Commit**

```bash
git add web/elm/src/AgentGraph/Layout.elm web/elm/tests/AgentGraphLayoutTests.elm
git commit -m "feat(web): pure layered layout for the agent graph"
```

### Task D3: Graph view and the color-independent state language

**Files:**
- Create: `web/elm/src/AgentGraph/View.elm`
- Test: `web/elm/tests/AgentGraphViewTests.elm`

- [ ] **Step 1: Write the failing tests**

Create `web/elm/tests/AgentGraphViewTests.elm`:

```elm
module AgentGraphViewTests exposing (all)

import AgentGraph.Layout as Layout
import AgentGraph.Model as Model
import AgentGraph.View as View
import Expect
import Html
import Test exposing (Test, describe, test)
import Test.Html.Query as Query
import Test.Html.Selector exposing (class, classes, containing, text)


all : Test
all =
    describe "agent graph view"
        [ test "states running and waiting counts as words, not colour alone" <|
            \_ ->
                render (stateFor { running = 2, waiting = 1, failed = 0, activity = True })
                    |> Query.find [ class "agent-graph-node-state" ]
                    |> Expect.all
                        [ Query.has [ text "2 running" ]
                        , Query.has [ text "1 waiting" ]
                        ]
        , test "marks a node with no window activity as no-data, not success" <|
            \_ ->
                render (stateFor { running = 0, waiting = 0, failed = 0, activity = False })
                    |> Query.find [ class "agent-graph-node" ]
                    |> Query.has [ class "agent-graph-node--no-data" ]
        , test "shows a resolved indicator when nothing needs attention" <|
            \_ ->
                render (stateFor { running = 0, waiting = 0, failed = 0, activity = True })
                    |> Query.find [ class "agent-graph-node" ]
                    |> Query.has [ class "agent-graph-node--resolved" ]
        , test "never fills the whole node with one aggregate status colour" <|
            \_ ->
                render (stateFor { running = 2, waiting = 0, failed = 1, activity = True })
                    |> Query.find [ class "agent-graph-node" ]
                    |> Query.hasNot [ class "agent-graph-node--failed" ]
        , test "marks the selected node with a selection class rather than a colour swap" <|
            \_ ->
                View.view
                    { selected = Just "implement"
                    , nodeState = \_ -> stateFor { running = 0, waiting = 0, failed = 0, activity = True }
                    , onSelect = identity
                    }
                    (Layout.layout graph)
                    |> Query.fromHtml
                    |> Query.find [ class "agent-graph-node" ]
                    |> Query.has [ class "agent-graph-node--selected" ]
        , test "renders decorations as badges on the node" <|
            \_ ->
                render (stateFor { running = 0, waiting = 0, failed = 0, activity = True })
                    |> Query.find [ class "agent-graph-node-decorations" ]
                    |> Query.has [ text "retry" ]
        ]


stateFor : { running : Int, waiting : Int, failed : Int, activity : Bool } -> View.NodeState
stateFor counts =
    { running = counts.running
    , waiting = counts.waiting
    , pending = 0
    , failed = counts.failed
    , errored = 0
    , aborted = 0
    , succeeded = 0
    , needsAttention = counts.running > 0 || counts.waiting > 0 || counts.failed > 0
    , hasWindowActivity = counts.activity
    }


graph : Model.Graph
graph =
    { nodes =
        [ { id = "implement"
          , kind = Model.Agent
          , displayName = "implement"
          , typeRef = ""
          , decorations = [ Model.Retry ]
          , reusableNode = Nothing
          }
        ]
    , edges = []
    }


render : View.NodeState -> Query.Single String
render state =
    View.view
        { selected = Nothing
        , nodeState = \_ -> state
        , onSelect = identity
        }
        (Layout.layout graph)
        |> Query.fromHtml
```

`View.view` is parameterised over its message type, so these tests instantiate it at `String` with `onSelect = identity` and stay independent of whatever `Message.Message` constructor the page supplies in Task D6. That parameterisation is exactly what lets the workflow overview and the run page share the renderer.

- [ ] **Step 2: Run the tests and confirm they fail**

```bash
cd web/elm && elm-test tests/AgentGraphViewTests.elm
```

Expected: FAIL — module `AgentGraph.View` not found.

- [ ] **Step 3: Write the view**

Create `web/elm/src/AgentGraph/View.elm`:

```elm
module AgentGraph.View exposing (Config, NodeState, emptyState, view)

{-| SVG rendering for a laid-out semantic graph.

The renderer is parameterised by a `NodeState` lookup rather than owning state,
which is what lets the workflow overview (aggregate counts across many runs)
and the individual run page (one run's state) share one graph model instead of
growing two.

Node bodies stay neutral. State is expressed as a glyph, a count, and a word,
so colour is only ever reinforcement and the graph stays readable without it.
-}

import AgentGraph.Layout as Layout
import AgentGraph.Model as Model
import Html exposing (Html)
import Svg
import Svg.Attributes as SvgAttributes
import Svg.Events


type alias NodeState =
    { running : Int
    , waiting : Int
    , pending : Int
    , failed : Int
    , errored : Int
    , aborted : Int
    , succeeded : Int
    , needsAttention : Bool
    , hasWindowActivity : Bool
    }


emptyState : NodeState
emptyState =
    { running = 0
    , waiting = 0
    , pending = 0
    , failed = 0
    , errored = 0
    , aborted = 0
    , succeeded = 0
    , needsAttention = False
    , hasWindowActivity = False
    }


type alias Config msg =
    { selected : Maybe String
    , nodeState : String -> NodeState
    , onSelect : String -> msg
    }


view : Config msg -> Layout.LaidOut -> Html msg
view config laidOut =
    Svg.svg
        [ SvgAttributes.class "agent-graph"
        , SvgAttributes.viewBox
            ("0 0 " ++ String.fromFloat laidOut.width ++ " " ++ String.fromFloat laidOut.height)
        ]
        (List.map (viewEdge laidOut) laidOut.edges
            ++ List.map (viewNode config) laidOut.nodes
        )


viewNode : Config msg -> Layout.LaidOutNode -> Svg.Svg msg
viewNode config item =
    let
        state =
            config.nodeState item.node.id

        isSelected =
            config.selected == Just item.node.id
    in
    Svg.g
        [ SvgAttributes.class (String.join " " (nodeClasses state isSelected))
        , SvgAttributes.transform
            ("translate(" ++ String.fromFloat item.x ++ "," ++ String.fromFloat item.y ++ ")")
        , Svg.Events.onClick (config.onSelect item.node.id)
        ]
        [ Svg.rect
            [ SvgAttributes.class "agent-graph-node-body"
            , SvgAttributes.width (String.fromFloat Layout.nodeWidth)
            , SvgAttributes.height (String.fromFloat Layout.nodeHeight)
            , SvgAttributes.rx "3"
            ]
            []
        , Svg.text_
            [ SvgAttributes.class "agent-graph-node-name", SvgAttributes.x "8", SvgAttributes.y "20" ]
            [ Svg.text item.node.displayName ]
        , Svg.text_
            [ SvgAttributes.class "agent-graph-node-state", SvgAttributes.x "8", SvgAttributes.y "38" ]
            [ Svg.text (stateLabel state) ]
        , Svg.text_
            [ SvgAttributes.class "agent-graph-node-decorations", SvgAttributes.x "8", SvgAttributes.y "52" ]
            [ Svg.text (decorationLabel item.node.decorations) ]
        ]


{-| Classes describe presence of state, never a single winning status. A node
representing several concurrent runs has no canonical status, so there is
deliberately no `--failed` or `--running` modifier that could fill the body
with one misleading colour.
-}
nodeClasses : NodeState -> Bool -> List String
nodeClasses state isSelected =
    List.filterMap identity
        [ Just "agent-graph-node"
        , if not state.hasWindowActivity then
            Just "agent-graph-node--no-data"

          else if state.needsAttention then
            Just "agent-graph-node--attention"

          else
            Just "agent-graph-node--resolved"
        , if isSelected then
            Just "agent-graph-node--selected"

          else
            Nothing
        ]


{-| Every indicator carries a glyph, a count, and a word, so the state survives
being read in greyscale or by a screen reader.
-}
stateLabel : NodeState -> String
stateLabel state =
    let
        parts =
            List.filterMap identity
                [ labelFor "\u{25B6}" state.running "running"
                , labelFor "\u{23F8}" state.waiting "waiting"
                , labelFor "\u{2715}" (state.failed + state.errored) "failed"
                , labelFor "\u{2715}" state.aborted "aborted"
                ]
    in
    if List.isEmpty parts then
        if state.hasWindowActivity then
            "\u{2713}"

        else
            ""

    else
        String.join " \u{00B7} " parts


labelFor : String -> Int -> String -> Maybe String
labelFor glyph count word =
    if count <= 0 then
        Nothing

    else
        Just (glyph ++ " " ++ String.fromInt count ++ " " ++ word)


decorationLabel : List Model.Decoration -> String
decorationLabel decorations =
    decorations |> List.map decorationName |> String.join " \u{00B7} "


decorationName : Model.Decoration -> String
decorationName decoration =
    case decoration of
        Model.Retry ->
            "retry"

        Model.Timeout ->
            "timeout"

        Model.Try ->
            "try"

        Model.Ensure ->
            "ensure"

        Model.OnFailure ->
            "on_failure"

        Model.OnError ->
            "on_error"

        Model.OnAbort ->
            "on_abort"

        Model.OnSuccess ->
            "on_success"


viewEdge : Layout.LaidOut -> Model.Edge -> Svg.Svg msg
viewEdge laidOut edge =
    let
        position id =
            laidOut.nodes
                |> List.filter (\item -> item.node.id == id)
                |> List.head
                |> Maybe.map (\item -> ( item.x, item.y ))
    in
    case ( position edge.from, position edge.to ) of
        ( Just ( fromX, fromY ), Just ( toX, toY ) ) ->
            Svg.path
                [ SvgAttributes.class "agent-graph-edge"
                , SvgAttributes.d (edgePath fromX fromY toX toY)
                ]
                []

        _ ->
            Svg.g [] []


edgePath : Float -> Float -> Float -> Float -> String
edgePath fromX fromY toX toY =
    let
        startX =
            fromX + Layout.nodeWidth

        startY =
            fromY + Layout.nodeHeight / 2

        endY =
            toY + Layout.nodeHeight / 2

        controlX =
            startX + (toX - startX) / 2
    in
    "M " ++ String.fromFloat startX ++ " " ++ String.fromFloat startY
        ++ " C " ++ String.fromFloat controlX ++ " " ++ String.fromFloat startY
        ++ ", " ++ String.fromFloat controlX ++ " " ++ String.fromFloat endY
        ++ ", " ++ String.fromFloat toX ++ " " ++ String.fromFloat endY
```

- [ ] **Step 4: Add the styles**

Styles are Less, compiled from `web/assets/css/main.less` by `yarn run build-less`. Find the file carrying the agent page styles and append there:

```bash
grep -rln "agent-workflow" web/assets/css/ | head
```

Add rules keyed on the classes above. Selection must be a border and halo change, and `--no-data` must be a dashed border with no fill change, so no state depends on hue alone:

```css
.agent-graph-node-body { fill: var(--neutral-surface); stroke: var(--neutral-border); stroke-width: 1; }
.agent-graph-node--no-data .agent-graph-node-body { stroke-dasharray: 4 3; }
.agent-graph-node--selected .agent-graph-node-body { stroke-width: 3; filter: drop-shadow(0 0 4px var(--focus-halo)); }
.agent-graph-node--attention .agent-graph-node-state { font-weight: 600; }
.agent-graph-edge { fill: none; stroke: var(--neutral-border); stroke-width: 2; }
```

Substitute the repository's real custom-property names, found in `web/elm/src/ColorValues.elm` and the existing CSS.

- [ ] **Step 5: Run the tests and confirm they pass**

```bash
cd web/elm && elm-test tests/AgentGraphViewTests.elm
```

Expected: PASS, six tests.

- [ ] **Step 6: Commit**

```bash
git add web/elm/src/AgentGraph/View.elm web/elm/tests/AgentGraphViewTests.elm web/assets/css/
git commit -m "feat(web): render the agent graph with a colour-independent state language"
```

### Task D4: URL-addressable filter state

**Files:**
- Create: `web/elm/src/AgentWorkflow/Filters.elm`
- Test: `web/elm/tests/AgentWorkflowFiltersTests.elm`

- [ ] **Step 1: Write the failing test**

Create `web/elm/tests/AgentWorkflowFiltersTests.elm`:

```elm
module AgentWorkflowFiltersTests exposing (all)

import AgentWorkflow.Filters as Filters
import Expect
import Test exposing (Test, describe, test)
import Url.Builder


all : Test
all =
    describe "workflow page filters"
        [ test "defaults to a seven-day operational attention view" <|
            \_ ->
                Expect.all
                    [ \f -> Expect.equal Filters.SevenDays f.window
                    , \f -> Expect.equal Filters.Operational f.scope
                    , \f -> Expect.equal Filters.Attention f.status
                    , \f -> Expect.equal Nothing f.selectedNode
                    ]
                    Filters.default
        , test "omits defaults from the query so a clean page has a clean URL" <|
            \_ ->
                Filters.toQuery Filters.default
                    |> Expect.equal []
        , test "round-trips a fully specified filter through the query string" <|
            \_ ->
                let
                    filters =
                        { window = Filters.TwentyFourHours
                        , scope = Filters.Experiment
                        , status = Filters.All
                        , search = "ticket-42"
                        , selectedNode = Just "implement"
                        , selectedNodeStatus = Just "failed"
                        , version = Just 3
                        , origin = "manual"
                        }
                in
                Filters.fromQuery (Filters.toQueryPairs filters)
                    |> Expect.equal filters
        , test "builds the query parameters the API expects" <|
            \_ ->
                Filters.toQuery { Filters.default | window = Filters.ThirtyDays, selectedNode = Just "review" }
                    |> Expect.equal
                        [ Url.Builder.string "window" "30d"
                        , Url.Builder.string "node" "review"
                        ]
        , test "ignores an unrecognised window rather than failing the page" <|
            \_ ->
                Filters.fromQuery [ ( "window", "90d" ) ]
                    |> .window
                    |> Expect.equal Filters.SevenDays
        ]
```

- [ ] **Step 2: Run the test and confirm it fails**

```bash
cd web/elm && elm-test tests/AgentWorkflowFiltersTests.elm
```

Expected: FAIL — module not found.

- [ ] **Step 3: Write the module**

Create `web/elm/src/AgentWorkflow/Filters.elm`:

```elm
module AgentWorkflow.Filters exposing
    ( Filters
    , Scope(..)
    , Status(..)
    , Window(..)
    , default
    , fromQuery
    , toQuery
    , toQueryPairs
    , windowParam
    )

{-| Filter and selection state for the workflow overview.

Everything here lives in the URL so back, forward, links, and refresh behave
predictably. Scroll position deliberately does not: it is restored from a
per-route cache instead, which keeps shareable links clean.

Defaults are omitted from the query string, so an untouched page has a bare URL
and a shared link only carries what the sharer actually changed.
-}

import Url.Builder


type Window
    = TwentyFourHours
    | SevenDays
    | ThirtyDays


type Scope
    = Operational
    | Experiment
    | AllScopes


type Status
    = Attention
    | Active
    | All


type alias Filters =
    { window : Window
    , scope : Scope
    , status : Status
    , search : String
    , selectedNode : Maybe String
    , selectedNodeStatus : Maybe String
    , version : Maybe Int
    , origin : String
    }


default : Filters
default =
    { window = SevenDays
    , scope = Operational
    , status = Attention
    , search = ""
    , selectedNode = Nothing
    , selectedNodeStatus = Nothing
    , version = Nothing
    , origin = ""
    }


windowParam : Window -> String
windowParam window =
    case window of
        TwentyFourHours ->
            "24h"

        SevenDays ->
            "7d"

        ThirtyDays ->
            "30d"


scopeParam : Scope -> String
scopeParam scope =
    case scope of
        Operational ->
            "operational"

        Experiment ->
            "experiment"

        AllScopes ->
            "all"


statusParam : Status -> String
statusParam status =
    case status of
        Attention ->
            "attention"

        Active ->
            "active"

        All ->
            "all"


toQueryPairs : Filters -> List ( String, String )
toQueryPairs filters =
    List.filterMap identity
        [ optional "window" (windowParam filters.window) (windowParam default.window)
        , optional "scope" (scopeParam filters.scope) (scopeParam default.scope)
        , optional "status" (statusParam filters.status) (statusParam default.status)
        , optional "q" filters.search ""
        , Maybe.map (Tuple.pair "node") filters.selectedNode
        , Maybe.map (Tuple.pair "node_status") filters.selectedNodeStatus
        , Maybe.map (\v -> ( "version", String.fromInt v )) filters.version
        , optional "origin" filters.origin ""
        ]


toQuery : Filters -> List Url.Builder.QueryParameter
toQuery filters =
    toQueryPairs filters |> List.map (\( key, value ) -> Url.Builder.string key value)


optional : String -> String -> String -> Maybe ( String, String )
optional key value fallback =
    if value == fallback then
        Nothing

    else
        Just ( key, value )


{-| An unrecognised value falls back to the default rather than failing. A
stale or hand-edited link should still render the page.
-}
fromQuery : List ( String, String ) -> Filters
fromQuery pairs =
    let
        find key =
            pairs |> List.filter (\( k, _ ) -> k == key) |> List.head |> Maybe.map Tuple.second
    in
    { window =
        case find "window" of
            Just "24h" ->
                TwentyFourHours

            Just "30d" ->
                ThirtyDays

            _ ->
                SevenDays
    , scope =
        case find "scope" of
            Just "experiment" ->
                Experiment

            Just "all" ->
                AllScopes

            _ ->
                Operational
    , status =
        case find "status" of
            Just "active" ->
                Active

            Just "all" ->
                All

            _ ->
                Attention
    , search = find "q" |> Maybe.withDefault ""
    , selectedNode = find "node"
    , selectedNodeStatus = find "node_status"
    , version = find "version" |> Maybe.andThen String.toInt
    , origin = find "origin" |> Maybe.withDefault ""
    }
```

- [ ] **Step 4: Run the test and confirm it passes**

```bash
cd web/elm && elm-test tests/AgentWorkflowFiltersTests.elm
```

Expected: PASS, five tests.

- [ ] **Step 5: Commit**

```bash
git add web/elm/src/AgentWorkflow/Filters.elm web/elm/tests/AgentWorkflowFiltersTests.elm
git commit -m "feat(web): URL-addressable workflow overview filters"
```

### Task D5: Coupled run list

**Files:**
- Create: `web/elm/src/AgentWorkflow/RunList.elm`
- Test: `web/elm/tests/AgentWorkflowRunListTests.elm`

- [ ] **Step 1: Write the failing test**

Create `web/elm/tests/AgentWorkflowRunListTests.elm`:

```elm
module AgentWorkflowRunListTests exposing (all)

import AgentWorkflow.RunList as RunList
import Expect
import Test exposing (Test, describe, test)
import Test.Html.Query as Query
import Test.Html.Selector exposing (class, containing, tag, text)


all : Test
all =
    describe "workflow run list"
        [ test "shows only the small set of fields a row needs before expansion" <|
            \_ ->
                render [ succeededRow ]
                    |> Query.find [ class "agent-run-row" ]
                    |> Expect.all
                        [ Query.has [ text "9007199254740995" ]
                        , Query.has [ text "4m" ]
                        , Query.hasNot [ text "$" ]
                        ]
        , test "shows an attention cue for a waiting run" <|
            \_ ->
                render [ waitingRow ]
                    |> Query.find [ class "agent-run-row-attention" ]
                    |> Query.has [ text "waiting at approval" ]
        , test "shows the ticket reference when one is associated" <|
            \_ ->
                render [ { succeededRow | ticketReference = "ticket-42" } ]
                    |> Query.find [ class "agent-run-row-ticket" ]
                    |> Query.has [ text "ticket-42" ]
        , test "marks a revision boundary only on the row where the revision changes" <|
            \_ ->
                render [ { succeededRow | workflowVersion = 4 }, succeededRow, { succeededRow | id = "3" } ]
                    |> Query.findAll [ class "agent-run-row-revision-boundary" ]
                    |> Query.count (Expect.equal 2)
        , test "renders an empty state rather than a bare list" <|
            \_ ->
                render []
                    |> Query.find [ class "agent-run-list-empty" ]
                    |> Query.has [ text "No runs" ]
        ]


succeededRow : RunList.Row
succeededRow =
    { id = "9007199254740995"
    , status = "succeeded"
    , workflowVersion = 3
    , startedAt = Just 1000
    , completedAt = Just 1240
    , durationSeconds = 240
    , ticketReference = ""
    , attentionCue = ""
    }


waitingRow : RunList.Row
waitingRow =
    { succeededRow | status = "running", attentionCue = "waiting at approval", completedAt = Nothing }


render : List RunList.Row -> Query.Single String
render rows =
    RunList.view { onSelect = identity } rows |> Query.fromHtml
```

- [ ] **Step 2: Run the test and confirm it fails**

```bash
cd web/elm && elm-test tests/AgentWorkflowRunListTests.elm
```

Expected: FAIL — module not found.

- [ ] **Step 3: Write the module**

Create `web/elm/src/AgentWorkflow/RunList.elm` exposing `Row`, `Config`, and `view`. Keep the row deliberately small — effective-state glyph, run ID, relative time, duration, ticket reference, attention cue — and put the revision-boundary marker only on rows where `workflowVersion` differs from the previous row:

```elm
module AgentWorkflow.RunList exposing (Config, Row, view)

{-| The chronological run list coupled to the graph.

Row content is deliberately minimal. Cost, outcome, and per-node detail live
behind selection, because a list that shows everything competes with the graph
instead of supporting it.
-}

import Html exposing (Html)
import Html.Attributes exposing (class)
import Html.Events exposing (onClick)


type alias Row =
    { id : String
    , status : String
    , workflowVersion : Int
    , startedAt : Maybe Int
    , completedAt : Maybe Int
    , durationSeconds : Int
    , ticketReference : String
    , attentionCue : String
    }


type alias Config msg =
    { onSelect : String -> msg }


view : Config msg -> List Row -> Html msg
view config rows =
    if List.isEmpty rows then
        Html.div [ class "agent-run-list-empty" ] [ Html.text "No runs in this window" ]

    else
        Html.ul [ class "agent-run-list" ]
            (rows
                |> withRevisionBoundaries
                |> List.map (viewRow config)
            )


{-| A revision marker belongs on the row where the revision changes, not on
every row: repeating it would turn a subtle boundary cue into noise.
-}
withRevisionBoundaries : List Row -> List ( Row, Bool )
withRevisionBoundaries rows =
    let
        step row ( previous, acc ) =
            ( Just row.workflowVersion, ( row, previous /= Just row.workflowVersion ) :: acc )
    in
    rows |> List.foldl step ( Nothing, [] ) |> Tuple.second |> List.reverse


viewRow : Config msg -> ( Row, Bool ) -> Html msg
viewRow config ( row, isBoundary ) =
    Html.li
        [ class "agent-run-row", onClick (config.onSelect row.id) ]
        (List.filterMap identity
            [ if isBoundary then
                Just (Html.span [ class "agent-run-row-revision-boundary" ] [ Html.text ("v" ++ String.fromInt row.workflowVersion) ])

              else
                Nothing
            , Just (Html.span [ class "agent-run-row-status" ] [ Html.text (glyphFor row.status) ])
            , Just (Html.span [ class "agent-run-row-id" ] [ Html.text row.id ])
            , Just (Html.span [ class "agent-run-row-duration" ] [ Html.text (formatDuration row.durationSeconds) ])
            , if row.ticketReference == "" then
                Nothing

              else
                Just (Html.span [ class "agent-run-row-ticket" ] [ Html.text row.ticketReference ])
            , if row.attentionCue == "" then
                Nothing

              else
                Just (Html.span [ class "agent-run-row-attention" ] [ Html.text row.attentionCue ])
            ]
        )


glyphFor : String -> String
glyphFor status =
    case status of
        "succeeded" ->
            "\u{2713}"

        "failed" ->
            "\u{2715}"

        "errored" ->
            "\u{2715}"

        "aborted" ->
            "\u{2298}"

        "running" ->
            "\u{25B6}"

        _ ->
            "\u{00B7}"


formatDuration : Int -> String
formatDuration seconds =
    if seconds < 60 then
        String.fromInt seconds ++ "s"

    else if seconds < 3600 then
        String.fromInt (seconds // 60) ++ "m"

    else
        String.fromInt (seconds // 3600) ++ "h"
```

- [ ] **Step 4: Run the test and confirm it passes**

```bash
cd web/elm && elm-test tests/AgentWorkflowRunListTests.elm
```

Expected: PASS, five tests.

- [ ] **Step 5: Commit**

```bash
git add web/elm/src/AgentWorkflow/RunList.elm web/elm/tests/AgentWorkflowRunListTests.elm
git commit -m "feat(web): coupled chronological run list"
```

### Task D6: Endpoint, effect, callback, and messages

**Files:**
- Modify: `web/elm/src/Api/Endpoints.elm`
- Modify: `web/elm/src/Message/Effects.elm`
- Modify: `web/elm/src/Message/Callback.elm`
- Modify: `web/elm/src/Message/Message.elm`
- Create: `web/elm/src/Concourse/WorkflowOverview.elm`

- [ ] **Step 1: Add the endpoint**

In `web/elm/src/Api/Endpoints.elm`, add to the `Endpoint` type:

```elm
    | AgentWorkflowOverview String (List ( String, String ))
```

and to the path function, following the `PipelineRunsList` precedent of an endpoint carrying its own query:

```elm
        AgentWorkflowOverview workflowName query ->
            base
                |> appendPath [ "agent", "workflows", Url.percentEncode workflowName, "overview" ]
                |> appendQuery (List.map (\( key, value ) -> Url.Builder.string key value) query)
```

Add the same shape for `AgentWorkflowRuns` so the run list can carry filters:

```elm
    | AgentWorkflowRunsFiltered String (List ( String, String ))
```

Leave the existing unfiltered `AgentWorkflowRuns` in place until every caller migrates, then delete it in Task D7.

The matching effect and callback, used by Task D7's node-selection test:

```elm
    | FetchAgentWorkflowRunsFiltered String (List ( String, String ))
```

```elm
        FetchAgentWorkflowRunsFiltered workflowName query ->
            Api.get (Endpoints.AgentWorkflowRunsFiltered workflowName query)
                |> Api.expectJson (Json.Decode.list Concourse.WorkflowRun.decodeSummary)
                |> Api.request
                |> Task.attempt (AgentWorkflowRunsFetched workflowName)
```

- [ ] **Step 2: Add the decoder module**

Create `web/elm/src/Concourse/WorkflowOverview.elm` with `Overview`, `NodeState`, `Window`, `RevisionBoundary` types and `decodeOverview`, mirroring the Go `Response` field names exactly (`node_state`, `has_window_activity`, `needs_attention`, `includes_active_before_window`, `has_historical_only_nodes`, `revision_boundaries`).

- [ ] **Step 3: Add the effect and callback**

In `Message/Effects.elm`:

```elm
    | FetchAgentWorkflowOverview String (List ( String, String ))
```

```elm
        FetchAgentWorkflowOverview workflowName query ->
            Api.get (Endpoints.AgentWorkflowOverview workflowName query)
                |> Api.expectJson Concourse.WorkflowOverview.decodeOverview
                |> Api.request
                |> Task.attempt (AgentWorkflowOverviewFetched workflowName)
```

In `Message/Callback.elm`:

```elm
    | AgentWorkflowOverviewFetched String (Fetched Concourse.WorkflowOverview.Overview)
```

- [ ] **Step 4: Add the page messages**

In `Message/Message.elm`:

```elm
    | AgentWorkflowNodeSelected String
    | AgentWorkflowNodeCleared
    | AgentWorkflowWindowChanged String
    | AgentWorkflowScopeChanged String
    | AgentWorkflowStatusFilterChanged String
    | AgentWorkflowSearchChanged String
    | AgentWorkflowPanelOpened String
    | AgentWorkflowPanelClosed
```

- [ ] **Step 5: Compile**

```bash
cd web/elm && elm make src/Main.elm --output=/dev/null
```

Expected: success. Elm will list every unhandled `case` branch created by the new constructors — fix each one; a missing branch is a real gap, not noise.

- [ ] **Step 6: Commit**

```bash
git add web/elm/src/Api/Endpoints.elm web/elm/src/Message/ web/elm/src/Concourse/WorkflowOverview.elm
git commit -m "feat(web): overview endpoint, effect, callback, and page messages"
```

### Task D7: Recompose the workflow page

**Files:**
- Modify: `web/elm/src/AgentWorkflow/AgentWorkflow.elm`
- Create: `web/elm/src/AgentWorkflow/Panels.elm`
- Modify: `web/elm/tests/AgentWorkflowPageTests.elm`

- [ ] **Step 1: Write the failing tests**

Append to `web/elm/tests/AgentWorkflowPageTests.elm`:

```elm
        , test "renders the graph beside the run list by default" <|
            \_ ->
                initializedWithOverview
                    |> Common.queryView
                    |> Expect.all
                        [ Query.has [ class "agent-graph" ]
                        , Query.has [ class "agent-run-list" ]
                        ]
        , test "has no permanent metrics strip on the untouched page" <|
            \_ ->
                initializedWithOverview
                    |> Common.queryView
                    |> Query.hasNot [ class "agent-workflow-summary-strip" ]
        , test "keeps definition management behind Start and Versions actions" <|
            \_ ->
                initializedWithOverview
                    |> Common.queryView
                    |> Expect.all
                        [ Query.has [ id "agent-workflow-start" ]
                        , Query.has [ id "agent-workflow-versions" ]
                        , Query.hasNot [ class "agent-workflow-version-timeline" ]
                        ]
        , test "opens the versions panel on demand" <|
            \_ ->
                initializedWithOverview
                    |> Application.update (Msgs.Update (Message.AgentWorkflowPanelOpened "versions"))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.has [ class "agent-workflow-version-timeline" ]
        , test "selecting a node refetches the run list filtered to that node" <|
            \_ ->
                initializedWithOverview
                    |> Application.update (Msgs.Update (Message.AgentWorkflowNodeSelected "implement"))
                    |> Tuple.second
                    |> Common.contains
                        (Effects.FetchAgentWorkflowRunsFiltered "review-api"
                            [ ( "node", "implement" ) ]
                        )
        , test "labels an unpromoted workflow instead of rendering an empty canvas" <|
            \_ ->
                initializedWithUnpromotedOverview
                    |> Common.queryView
                    |> Query.find [ class "agent-workflow-revision-indicator" ]
                    |> Query.has [ text "not promoted" ]
```

Add `initializedWithOverview` and `initializedWithUnpromotedOverview` helpers beside the existing `initialized`, feeding `Callback.AgentWorkflowOverviewFetched` with fixture data from `AgenticData`.

- [ ] **Step 2: Run the tests and confirm they fail**

```bash
cd web/elm && elm-test tests/AgentWorkflowPageTests.elm
```

Expected: FAIL.

- [ ] **Step 3: Write the panels module**

Create `web/elm/src/AgentWorkflow/Panels.elm` exposing `Panel(..)` (`Start`, `Versions`), `fromString`, `toString`, and `view`. Move the existing manual run form and version timeline markup out of `AgentWorkflow.elm` into it unchanged, so this is mostly a relocation rather than a rewrite.

The Versions panel gains one new element: when the overview reports `has_historical_only_nodes`, it renders a note that runs in this window touched nodes the promoted graph no longer contains, with a link to the revision that contained them. This is the discovery affordance for removed nodes — the canvas itself never renders a union graph.

- [ ] **Step 4: Recompose the page**

In `AgentWorkflow.elm`, replace the model's flat fields with:

```elm
        { workflowName : String
        , filters : Filters.Filters
        , overview : Maybe Overview.Overview
        , runs : Maybe (List RunList.Row)
        , openPanel : Maybe Panels.Panel
        , versions : Maybe (List Agent.WorkflowVersion)
        , scrollTop : Float
        , loadError : Bool
        }
```

`init` fetches the overview and the filtered run list. `AgentWorkflowNodeSelected` updates `filters.selectedNode`, pushes the new URL from `Filters.toQueryPairs`, and refetches only the run list. Panel state comes from the `panel` query parameter so the panels are linkable.

Scroll restoration reads and writes `scrollTop` in the model around navigation and calls `Browser.Dom.setViewportOf "agent-run-list"` on re-entry. Do not put it in the URL.

- [ ] **Step 5: Run the tests and confirm they pass**

```bash
cd web/elm && elm-test
```

Expected: PASS, whole suite.

- [ ] **Step 6: Rebuild the bundle**

Web and Elm changes must be rebuilt and committed — a stale `elm.min.js` ships old UI even when the source is correct.

```bash
yarn build
```

- [ ] **Step 7: Commit**

```bash
git add web/elm/src/AgentWorkflow/ web/elm/tests/ web/public/
git commit -m "feat(web): workflow overview page with coupled graph and run list"
```

- [ ] **Step 8: Phase D exit gate**

```bash
cd web/elm && elm-test && cd ../.. && go test ./agent/... -count=1
```

Expected: PASS.

---

## Task C4: Server-side attention lens (found during Phase D)

Phase C shipped `window`, `scope`, `node`, `node_status`, and `q`, but no
filter matching the design's `attention | active | all` lens, so Phase D had to
apply it client-side to whatever page the server returned.

That is wrong for the default view. The run list is server-paginated at fifty
rows and `attention` is the DEFAULT lens, so the page currently shows
"attention-worthy runs among the newest fifty" rather than "attention-worthy
runs". An unresolved failure older than fifty runs is invisible on the page
whose primary job is answering "is anything unresolved?" and "where is the
problem?".

**Files:**
- Modify: `atc/db/agent_workflow_run.go`, `atc/db/agent_workflow_runs_factory.go`
- Modify: `agent/api/workflowruns/handler.go`
- Modify: `web/elm/src/AgentWorkflow/Filters.elm`, `AgentWorkflow.elm`

- [ ] **Step 1: Add the lens to the filter**

Add a `Lens` field to `AgentWorkflowRunListFilter` with values matching the UI
vocabulary. Define each as a server-side predicate:

- **`active`** — the run is nonterminal (`admitting`, `running`, `canceling`).
- **`attention`** — the run is active, OR it is terminal and unresolved:
  a failed/errored/aborted run with no later successful retry in its closure,
  or a run with an unresolved wait. Reuse the retry-resolution semantics in
  `agent/workflowrun/occurrence/attention.go` rather than inventing a second
  rule — if the two disagree, the canvas and the list will contradict each
  other, which is worse than either being wrong alone.
- **`all`** — no predicate.

- [ ] **Step 2: Write DB specs first**

Cover: an old failed run with a later successful retry is NOT attention-worthy;
the same run with no successful retry IS; a run waiting at an `await` IS; an
active run older than the window IS (it must survive the window union too); and
`all` returns everything. Assert against runs positioned beyond one page, since
that is the bug being fixed.

- [ ] **Step 3: Parse `lens` in the handler, defaulting to `attention`**

Reject an unrecognised value with 400, matching how `scope` and `window` are
already validated.

- [ ] **Step 4: Move the Elm lens from client-side to the query**

`Filters.runsQuery` emits `lens`; `AgentWorkflow.elm` stops filtering rows
locally. Keep the distinct empty-state copy that separates "no runs in this
window" from "no runs match this lens" — that distinction is still right, it
just now reflects a server answer.

- [ ] **Step 5: Verify and commit**

```bash
ginkgo --focus="AgentWorkflowRuns" ./atc/db/
```

```bash
git commit -m "feat(api): server-side attention lens for the run list"
```

---

## Phase E — exact run DAG

The run page renders one immutable run occurrence through the same renderer with a single-run state lookup. Existing durable detail is re-homed under the selected node, not discarded.

### Task E1: Run graph endpoint

**Files:**
- Create: `agent/api/workflowruns/graph.go`
- Test: `agent/api/workflowruns/graph_test.go`
- Modify: `atc/routes.go`, `atc/api/handler.go`

- [ ] **Step 1: Write the failing test**

Create `agent/api/workflowruns/graph_test.go`:

```go
package workflowruns_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRunGraphUsesTheRunsOwnRevision(t *testing.T) {
	// The run pinned version 2 while version 5 is promoted. The run page must
	// render version 2's shape, never the current promoted shape.
	handler := newHandlerWithRunAtVersion(t, 2, 5)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Form = map[string][]string{":workflow_name": {"small-fix"}, ":workflow_run_id": {"42"}}
	handler.Graph(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body)
	}

	var response struct {
		WorkflowVersion int `json:"workflow_version"`
		Graph           struct {
			Nodes []map[string]any `json:"nodes"`
		} `json:"graph"`
		Occurrences []map[string]any `json:"occurrences"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	if response.WorkflowVersion != 2 {
		t.Fatalf("expected the run's own revision, got %d", response.WorkflowVersion)
	}
	if len(response.Graph.Nodes) == 0 {
		t.Fatal("expected a graph")
	}
	if len(response.Occurrences) == 0 {
		t.Fatal("expected node occurrences for the run")
	}
}

func TestRunGraphNeverLeaksPrompts(t *testing.T) {
	handler := newHandlerWithRunCarryingPrompt(t, "SUPER SECRET PROMPT")

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Form = map[string][]string{":workflow_name": {"small-fix"}, ":workflow_run_id": {"42"}}
	handler.Graph(recorder, request)

	if strings.Contains(recorder.Body.String(), "SUPER SECRET PROMPT") {
		t.Fatal("the run graph response must never carry prompt text")
	}
}

func TestRunGraphIsTeamScoped(t *testing.T) {
	handler := newHandlerWithRunOwnedByAnotherTeam(t)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Form = map[string][]string{":workflow_name": {"small-fix"}, ":workflow_run_id": {"42"}}
	handler.Graph(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for another team's run, got %d", recorder.Code)
	}
}
```

- [ ] **Step 2: Run the tests and confirm they fail**

```bash
go test ./agent/api/workflowruns/ -run TestRunGraph -count=1
```

Expected: FAIL — `handler.Graph undefined`.

- [ ] **Step 3: Write the handler**

Create `agent/api/workflowruns/graph.go`:

```go
package workflowruns

import (
	"encoding/json"
	"net/http"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow/graph"
	"github.com/concourse/concourse/agent/workflowrun/occurrence"
)

// GraphResponse is the exact DAG of one immutable run occurrence together with
// that run's node state. It is built from the run's own frozen revision, never
// from the currently promoted one, so opening an old run shows the shape that
// actually executed.
type GraphResponse struct {
	WorkflowRunID   snapshot.WorkflowRunID `json:"workflow_run_id"`
	WorkflowName    string                 `json:"workflow_name"`
	WorkflowVersion int                    `json:"workflow_version"`
	Graph           graph.Graph            `json:"graph"`
	Occurrences     []NodeOccurrence       `json:"occurrences"`
}

// NodeOccurrence is the redacted per-node projection. Detail pointers let the
// UI fetch durable evidence on demand instead of inlining it here.
type NodeOccurrence struct {
	NodeID          string  `json:"node_id"`
	NodeKind        string  `json:"node_kind"`
	Attempt         int     `json:"attempt"`
	Status          string  `json:"status"`
	PlanID          string  `json:"plan_id"`
	WaitID          *int64  `json:"wait_id"`
	PublicationID   *int64  `json:"publication_id"`
	StartedAt       *int64  `json:"started_at"`
	CompletedAt     *int64  `json:"completed_at"`
	DurationSeconds int     `json:"duration_seconds"`
	CostUSD         float64 `json:"cost_usd"`
}

func (handler *Handler) Graph(w http.ResponseWriter, r *http.Request) {
	runID, ok := parseWorkflowRunID(r)
	if !ok {
		http.Error(w, "invalid workflow run id", http.StatusBadRequest)
		return
	}

	run, found, err := handler.runs.Get(r.Context(), handler.team.ID, runID)
	if err != nil {
		handler.logger.Error("get-run-failed", err)
		http.Error(w, "failed to read workflow run", http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "unknown workflow run", http.StatusNotFound)
		return
	}

	definition, found, err := handler.definitions.GetVersion(r.Context(), run.WorkflowName, run.WorkflowVersion)
	if err != nil || !found {
		handler.logger.Error("get-run-definition-failed", err)
		http.Error(w, "failed to read workflow revision", http.StatusInternalServerError)
		return
	}

	built, err := graph.Build(definition.Compiled.Function)
	if err != nil {
		handler.logger.Error("build-run-graph-failed", err)
		http.Error(w, "failed to build run graph", http.StatusInternalServerError)
		return
	}

	occurrences, err := handler.occurrencesFor(r.Context(), run)
	if err != nil {
		handler.logger.Error("read-run-occurrences-failed", err)
		http.Error(w, "failed to read run node state", http.StatusInternalServerError)
		return
	}

	response := GraphResponse{
		WorkflowRunID:   run.ID,
		WorkflowName:    run.WorkflowName,
		WorkflowVersion: run.WorkflowVersion,
		Graph:           built,
		Occurrences:     presentOccurrences(occurrences),
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		handler.logger.Error("encode-run-graph-failed", err)
	}
}

// occurrencesFor prefers the frozen projection and falls back to a live
// derivation for a run that has not terminated yet. Both paths call the same
// derivation, so an active run and a completed one describe their nodes
// identically.
func (handler *Handler) occurrencesFor(ctx context.Context, run db.AgentWorkflowRun) ([]occurrence.NodeOccurrence, error) {
	frozen, err := handler.nodeOccurrences.ForRun(ctx, int64(run.ID))
	if err != nil {
		return nil, err
	}
	if len(frozen) > 0 {
		return fromDBRows(frozen), nil
	}
	return handler.deriveLive(ctx, run)
}
```

Implement `presentOccurrences`, `fromDBRows`, and `deriveLive` alongside. `deriveLive` gathers the same `occurrence.Sources` the reconciler uses.

- [ ] **Step 4: Register the route**

In `atc/routes.go`:

```go
	GetAgentWorkflowRunGraph = "GetAgentWorkflowRunGraph"
```

```go
	{Path: "/api/v1/agent/workflows/:workflow_name/runs/:workflow_run_id/graph", Method: "GET", Name: GetAgentWorkflowRunGraph},
```

and dispatch in `atc/api/handler.go`:

```go
		atc.GetAgentWorkflowRunGraph: http.HandlerFunc(workflowRunHandlers.Graph),
```

- [ ] **Step 5: Run the tests and confirm they pass**

```bash
go test ./agent/api/... ./atc/api/ -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add agent/api/workflowruns/graph.go agent/api/workflowruns/graph_test.go atc/routes.go atc/api/handler.go
git commit -m "feat(api): exact run DAG endpoint pinned to the run's own revision"
```

### Task E2: Selected-node detail

**Files:**
- Create: `web/elm/src/AgentWorkflowRun/NodeDetail.elm`
- Test: `web/elm/tests/AgentWorkflowRunNodeDetailTests.elm`

- [ ] **Step 1: Write the failing test**

Create `web/elm/tests/AgentWorkflowRunNodeDetailTests.elm`:

```elm
module AgentWorkflowRunNodeDetailTests exposing (all)

import AgentWorkflowRun.NodeDetail as NodeDetail
import Expect
import Test exposing (Test, describe, test)
import Test.Html.Query as Query
import Test.Html.Selector exposing (class, text)


all : Test
all =
    describe "run node detail"
        [ test "shows attempts and effective state for an agent node" <|
            \_ ->
                render agentDetail
                    |> Query.find [ class "agent-node-detail-attempts" ]
                    |> Expect.all
                        [ Query.has [ text "attempt 2" ]
                        , Query.has [ text "succeeded" ]
                        ]
        , test "shows the human question and resolution audit for a wait" <|
            \_ ->
                render waitDetail
                    |> Query.find [ class "agent-node-detail-wait" ]
                    |> Expect.all
                        [ Query.has [ text "Approve this change?" ]
                        , Query.has [ text "resolved by alice" ]
                        ]
        , test "shows the publish result for a publish node" <|
            \_ ->
                render publishDetail
                    |> Query.find [ class "agent-node-detail-publication" ]
                    |> Query.has [ text "succeeded" ]
        , test "shows sealed outputs and cost" <|
            \_ ->
                render agentDetail
                    |> Expect.all
                        [ Query.has [ class "agent-node-detail-outputs" ]
                        , Query.has [ text "$1.25" ]
                        ]
        , test "prompts an empty selection rather than rendering a blank pane" <|
            \_ ->
                NodeDetail.view Nothing
                    |> Query.fromHtml
                    |> Query.find [ class "agent-node-detail-empty" ]
                    |> Query.has [ text "Select a node" ]
        ]
```

Define `agentDetail`, `waitDetail`, `publishDetail`, and `render` in the same file using `NodeDetail.Detail`.

- [ ] **Step 2: Run the test and confirm it fails**

```bash
cd web/elm && elm-test tests/AgentWorkflowRunNodeDetailTests.elm
```

Expected: FAIL — module not found.

- [ ] **Step 3: Write the module by relocating existing markup**

Create `web/elm/src/AgentWorkflowRun/NodeDetail.elm`. Move the node-scoped card markup out of `AgentWorkflowRun.elm` unchanged: inputs and sealed outputs, wait question/answer with resolution audit, review projections, repository-change projections, transcript, metrics, and validation evidence. Genuinely run-level cards — outcomes and overall run metrics — stay in `AgentWorkflowRun.elm` below the graph.

This is a relocation, not a rewrite. Do not change what the cards render; change only where they live.

- [ ] **Step 4: Run the test and confirm it passes**

```bash
cd web/elm && elm-test tests/AgentWorkflowRunNodeDetailTests.elm
```

Expected: PASS, five tests.

- [ ] **Step 5: Commit**

```bash
git add web/elm/src/AgentWorkflowRun/NodeDetail.elm web/elm/tests/AgentWorkflowRunNodeDetailTests.elm
git commit -m "feat(web): selected-node durable detail for the run page"
```

### Task E3: Recompose the run page

**Files:**
- Modify: `web/elm/src/AgentWorkflowRun/AgentWorkflowRun.elm`
- Modify: `web/elm/tests/AgentWorkflowRunPageTests.elm`
- Modify: `web/elm/src/Api/Endpoints.elm`, `Message/Effects.elm`, `Message/Callback.elm`

- [ ] **Step 1: Write the failing tests**

Append to `web/elm/tests/AgentWorkflowRunPageTests.elm`:

```elm
        , test "renders the exact run DAG" <|
            \_ ->
                initializedWithGraph
                    |> Common.queryView
                    |> Query.has [ class "agent-graph" ]
        , test "keeps the header to high-signal identity only" <|
            \_ ->
                initializedWithGraph
                    |> Common.queryView
                    |> Query.find [ class "agent-run-header" ]
                    |> Expect.all
                        [ Query.has [ text "v3" ]
                        , Query.has [ text "succeeded" ]
                        , Query.hasNot [ class "agent-run-transcript" ]
                        ]
        , test "reveals node detail on selection" <|
            \_ ->
                initializedWithGraph
                    |> Application.update (Msgs.Update (Message.AgentWorkflowNodeSelected "implement"))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ class "agent-node-detail" ]
                    |> Query.has [ text "implement" ]
        , test "links back to the associated ticket when one exists" <|
            \_ ->
                initializedWithTicket
                    |> Common.queryView
                    |> Query.find [ class "agent-run-header-ticket" ]
                    |> Query.has [ attribute (Attr.href "/agent-tickets/42") ]
        , test "renders a run with no ticket without an empty ticket slot" <|
            \_ ->
                initializedWithGraph
                    |> Common.queryView
                    |> Query.hasNot [ class "agent-run-header-ticket" ]
```

- [ ] **Step 2: Run the tests and confirm they fail**

```bash
cd web/elm && elm-test tests/AgentWorkflowRunPageTests.elm
```

Expected: FAIL.

- [ ] **Step 3: Add the graph fetch**

Add `AgentWorkflowRunGraph String String` to `Endpoints.elm`, `FetchAgentWorkflowRunGraph` to `Effects.elm`, and `AgentWorkflowRunGraphFetched` to `Callback.elm`, following the patterns established in Task D6.

- [ ] **Step 4: Recompose the page**

In `AgentWorkflowRun.elm`, put `AgentGraph.View` immediately below a slim header carrying run ID, effective state, revision, ticket link when present, timing and duration, retry relationship, and one attention cue. Node-scoped cards render through `NodeDetail.view model.selectedNode`. Run-level cards stay below.

Supply `AgentGraph.View`'s `NodeState` from this run's occurrences: exactly one of `running`, `waiting`, `failed`, or `succeeded` is non-zero per node, which is what makes one renderer serve both surfaces.

- [ ] **Step 5: Run the tests and rebuild**

```bash
cd web/elm && elm-test && cd ../.. && yarn build
```

Expected: PASS, then a successful build.

- [ ] **Step 6: Commit**

```bash
git add web/elm/src/AgentWorkflowRun/ web/elm/src/Api/Endpoints.elm web/elm/src/Message/ web/elm/tests/ web/public/
git commit -m "feat(web): organise the run page around its exact DAG"
```

- [ ] **Step 7: Phase E exit gate**

```bash
cd web/elm && elm-test && cd ../.. && go test ./agent/... ./atc/api/ -count=1
```

Expected: PASS.

---

## Phase F — ticket association and journal

Independent of Phases A–E. May run in parallel or first.

### Task F1: Durable ticket association

**Files:**
- Create: `atc/db/migration/migrations/1773106158_associate_runs_with_tickets.{up,down}.sql`
- Test: `atc/db/agent_workflow_runs_factory_test.go`

- [ ] **Step 1: Write the failing DB spec**

Append to `atc/db/agent_workflow_runs_factory_test.go`:

```go
		It("retains the ticket reference after the intake ticket is deleted", func() {
			ticket := createAgentTicket()
			run := createRunWithTicket(ticket.ID)

			_, err := dbConn.ExecContext(ctx, `DELETE FROM agent_tickets WHERE id = $1`, ticket.ID)
			Expect(err).ToNot(HaveOccurred())

			reloaded, found, err := factory.Get(ctx, team.ID(), run.ID)
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(reloaded.TicketID).To(BeNil(), "the live reference must clear")
			Expect(reloaded.TicketReference).ToNot(BeEmpty(), "the durable evidence must survive")
		})

		It("rejects mutating a run's ticket association after insert", func() {
			ticket := createAgentTicket()
			run := createRunWithTicket(ticket.ID)

			_, err := dbConn.ExecContext(ctx,
				`UPDATE agent_workflow_runs SET ticket_reference = 'ticket-999' WHERE id = $1`, run.ID)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("ticket association is immutable"))
		})

		It("allows at most one ticket per run and many runs per ticket", func() {
			ticket := createAgentTicket()
			first := createRunWithTicket(ticket.ID)
			second := createRunWithTicket(ticket.ID)

			runs, err := factory.List(ctx, db.AgentWorkflowRunListFilter{
				TeamID: team.ID(), TicketID: &ticket.ID,
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(runIDs(runs)).To(ConsistOf(first.ID, second.ID))
		})
```

- [ ] **Step 2: Run the specs and confirm they fail**

```bash
ginkgo --focus="ticket reference after the intake ticket|ticket association after insert|at most one ticket per run" ./atc/db/
```

Expected: FAIL — the columns do not exist.

- [ ] **Step 3: Write the up migration**

Create `atc/db/migration/migrations/1773106158_associate_runs_with_tickets.up.sql`:

```sql
-- A ticket may drive many runs across many workflows, while a run belongs to
-- at most one ticket. agent_tickets.workflow_run_id modelled the inverse and
-- could only ever name one run, so later retries and follow-on workflows were
-- unattributable.
--
-- The live FK plus immutable copied evidence follows the same idiom as
-- agent_workflow_waits.build_id / build_id_evidence: the reference survives
-- deletion or archival of the mutable intake ticket, so durable run history
-- never loses the context that explains it.
ALTER TABLE agent_workflow_runs
    ADD COLUMN ticket_id BIGINT REFERENCES agent_tickets (id) ON DELETE SET NULL,
    ADD COLUMN ticket_reference TEXT NOT NULL DEFAULT '';

ALTER TABLE agent_workflow_runs
    ADD CONSTRAINT agent_workflow_runs_ticket_evidence_check
        CHECK (ticket_id IS NULL OR btrim(ticket_reference) <> '');

CREATE INDEX agent_workflow_runs_ticket_journal
    ON agent_workflow_runs (ticket_id, created_at DESC, id DESC)
    WHERE ticket_id IS NOT NULL;

CREATE INDEX agent_workflow_runs_ticket_reference
    ON agent_workflow_runs (ticket_reference, created_at DESC)
    WHERE ticket_reference <> '';

-- Adopt the association the old column already expressed.
UPDATE agent_workflow_runs r
SET ticket_id = t.id,
    ticket_reference = CASE
        WHEN btrim(t.external_ref) <> '' THEN t.external_ref
        ELSE 'ticket-' || t.id::text
    END
FROM agent_tickets t
WHERE t.workflow_run_id = r.id;

-- Association is decided at admission and is audit evidence thereafter.
CREATE FUNCTION enforce_agent_workflow_run_ticket_immutability()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    -- ON DELETE SET NULL must still be able to clear the live reference; only
    -- the durable evidence and re-pointing are forbidden.
    IF NEW.ticket_reference IS DISTINCT FROM OLD.ticket_reference THEN
        RAISE EXCEPTION 'workflow run ticket association is immutable';
    END IF;
    IF OLD.ticket_id IS NOT NULL
       AND NEW.ticket_id IS NOT NULL
       AND NEW.ticket_id <> OLD.ticket_id THEN
        RAISE EXCEPTION 'workflow run ticket association is immutable';
    END IF;
    IF OLD.ticket_id IS NULL AND NEW.ticket_id IS NOT NULL THEN
        RAISE EXCEPTION 'workflow run ticket association is immutable';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER agent_workflow_runs_ticket_immutable
BEFORE UPDATE OF ticket_id, ticket_reference ON agent_workflow_runs
FOR EACH ROW
EXECUTE FUNCTION enforce_agent_workflow_run_ticket_immutability();

-- One truth. Keeping both directions would let them disagree.
DROP INDEX IF EXISTS agent_tickets_workflow_run;
ALTER TABLE agent_tickets DROP COLUMN workflow_run_id;
```

- [ ] **Step 4: Write the down migration**

Create `atc/db/migration/migrations/1773106158_associate_runs_with_tickets.down.sql`:

```sql
ALTER TABLE agent_tickets
    ADD COLUMN workflow_run_id BIGINT REFERENCES agent_workflow_runs (id) ON DELETE SET NULL;

-- Restore only the first run per ticket: the old column could never hold more.
UPDATE agent_tickets t
SET workflow_run_id = (
    SELECT r.id FROM agent_workflow_runs r
    WHERE r.ticket_id = t.id
    ORDER BY r.created_at, r.id
    LIMIT 1
);

CREATE UNIQUE INDEX agent_tickets_workflow_run
    ON agent_tickets (workflow_run_id)
    WHERE workflow_run_id IS NOT NULL;

DROP TRIGGER IF EXISTS agent_workflow_runs_ticket_immutable ON agent_workflow_runs;
DROP FUNCTION IF EXISTS enforce_agent_workflow_run_ticket_immutability();
DROP INDEX IF EXISTS agent_workflow_runs_ticket_reference;
DROP INDEX IF EXISTS agent_workflow_runs_ticket_journal;
ALTER TABLE agent_workflow_runs
    DROP CONSTRAINT IF EXISTS agent_workflow_runs_ticket_evidence_check;
ALTER TABLE agent_workflow_runs
    DROP COLUMN IF EXISTS ticket_reference,
    DROP COLUMN IF EXISTS ticket_id;
```

The down migration is lossy — a ticket with several runs keeps only its earliest. Say so in the commit message; it is acceptable because down is a rollback path, not a supported steady state.

- [ ] **Step 5: Extend the Go types**

In `atc/db/agent_workflow_run.go`, add to `AgentWorkflowRun`:

```go
	TicketID        *int64
	TicketReference string
```

add `TicketID *int64` to `AgentWorkflowRunListFilter`, add both columns to `agentWorkflowRunColumns` and every scan, and add the predicate to `List`:

```go
	if filter.TicketID != nil {
		query = query.Where(sq.Eq{"r.ticket_id": *filter.TicketID})
	}
```

Then flip Phase C's guard: delete `agentWorkflowRunsHaveTicketReference` and its `else if`, restoring the plain `sq.Like{"r.ticket_reference": search + "%"}` branch.

- [ ] **Step 6: Run the specs in both directions**

```bash
go test ./atc/db/migration/ -count=1 && ginkgo --focus="AgentWorkflowRunsFactory" ./atc/db/
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add atc/db/migration/migrations/1773106158_associate_runs_with_tickets.up.sql atc/db/migration/migrations/1773106158_associate_runs_with_tickets.down.sql atc/db/agent_workflow_run.go atc/db/agent_workflow_runs_factory.go atc/db/agent_workflow_runs_factory_test.go
git commit -m "feat(db): durable optional ticket association on every workflow run

Replaces agent_tickets.workflow_run_id, which could name only one run and so
lost every retry and follow-on workflow. The down migration is lossy by
necessity: a ticket with several runs keeps only its earliest."
```

### Task F2: Propagate association through every admission path

**Files:**
- Modify: `agent/workflowrun/types.go`, `binder.go`, `experiment_binder.go`
- Test: `agent/workflowrun/binder_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `agent/workflowrun/binder_test.go`:

```go
func TestBindCarriesTicketAssociationFromAdmission(t *testing.T) {
	binder, store := newTestBinder(t)

	_, err := binder.BindAndCreate(context.Background(), AdmissionContext{
		Origin: Origin{Kind: OriginKindTicket, Reference: "42"},
		Ticket: &TicketAssociation{ID: 42, Reference: "ticket-42"},
	}, validBindRequest())
	if err != nil {
		t.Fatalf("BindAndCreate returned an error: %v", err)
	}

	created := store.lastCreate
	if created.TicketID == nil || *created.TicketID != 42 {
		t.Fatalf("expected the ticket id to reach the durable run, got %+v", created.TicketID)
	}
	if created.TicketReference != "ticket-42" {
		t.Fatalf("expected the durable reference, got %q", created.TicketReference)
	}
}

func TestRetryInheritsTicketAssociation(t *testing.T) {
	binder, store := newTestBinder(t)
	source := storedRunWithTicket(store, 42, "ticket-42")

	_, err := binder.Retry(context.Background(), source.ID, RetryRequest{IdempotencyKey: "fresh"})
	if err != nil {
		t.Fatalf("Retry returned an error: %v", err)
	}

	created := store.lastCreate
	if created.TicketID == nil || *created.TicketID != 42 || created.TicketReference != "ticket-42" {
		t.Fatalf("a retry must inherit its source's ticket, got %+v / %q", created.TicketID, created.TicketReference)
	}
}

func TestManualLaunchWithoutTicketStaysUnattached(t *testing.T) {
	binder, store := newTestBinder(t)

	_, err := binder.BindAndCreate(context.Background(), AdmissionContext{
		Origin: Origin{Kind: "manual", Reference: "alice"},
	}, validBindRequest())
	if err != nil {
		t.Fatalf("BindAndCreate returned an error: %v", err)
	}

	if store.lastCreate.TicketID != nil || store.lastCreate.TicketReference != "" {
		t.Fatal("an unattached workflow must not acquire a ticket")
	}
}

func TestExperimentLaunchStaysUnattachedByDefault(t *testing.T) {
	binder, store := newTestExperimentBinder(t)

	if _, err := binder.BindAndCreate(context.Background(), AdmissionContext{
		Origin: Origin{Kind: "experiment", Reference: "7"},
	}, validBindRequest()); err != nil {
		t.Fatalf("BindAndCreate returned an error: %v", err)
	}

	if store.lastCreate.TicketID != nil {
		t.Fatal("experiments remain unattached unless explicitly launched in ticket context")
	}
}
```

- [ ] **Step 2: Run the tests and confirm they fail**

```bash
go test ./agent/workflowrun/ -run 'TicketAssociation|TicketInherit|Unattached' -count=1
```

Expected: FAIL — `unknown field Ticket in struct literal`.

- [ ] **Step 3: Add the association type**

In `agent/workflowrun/types.go`:

```go
// TicketAssociation is explicit run context, never inferred from origin
// strings or snapshot lineage. Reference is copied durably so the run can
// still explain itself after the mutable intake ticket is deleted or archived.
//
// It does not change the workflow's type signature and does not create a
// required work-item input port: a workflow may consume a work-item/v1
// snapshot without acquiring ticket membership, and an associated workflow
// need not expose that ticket as a semantic input.
type TicketAssociation struct {
	ID        int64
	Reference string
}

func (association TicketAssociation) Validate() error {
	if association.ID <= 0 || strings.TrimSpace(association.Reference) == "" {
		return fmt.Errorf("workflowrun: ticket association requires an id and a reference")
	}
	return nil
}
```

and add `Ticket *TicketAssociation` to `AdmissionContext`.

- [ ] **Step 4: Carry it into the durable create**

In `agent/workflowrun/binder.go`, at both `AgentWorkflowRunCreateRequest` construction sites (currently around lines 512 and 677):

```go
	if admission.Ticket != nil {
		if err := admission.Ticket.Validate(); err != nil {
			return BindResult{}, err
		}
		request.TicketID = &admission.Ticket.ID
		request.TicketReference = admission.Ticket.Reference
	}
```

In the retry path, copy from the source run:

```go
	if source.TicketID != nil {
		request.TicketID = source.TicketID
		request.TicketReference = source.TicketReference
	}
```

- [ ] **Step 5: Audit every admission path**

Walk each caller and confirm it either supplies a ticket or deliberately does not. Record the result in the commit message.

```bash
grep -rn "AdmissionContext{" --include='*.go' . | grep -v _test
```

Expected paths and their correct behaviour:

| Path | Association |
|---|---|
| ticket dispatch | supplies the ticket |
| manual launch from ticket context | supplies the ticket |
| direct manual launch | none |
| retry | inherits from source |
| resource-triggered admission | none unless the triggering run had one |
| automated follow-on launch | inherits from the launching run |
| experiment candidate/evaluator | none unless explicitly launched in ticket context |
| Publish/PR follow-up | inherits from the publishing run |

- [ ] **Step 6: Run the tests and confirm they pass**

```bash
go test ./agent/workflowrun/... -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add agent/workflowrun/
git commit -m "feat(workflowrun): propagate optional ticket association through admission"
```

### Task F3: Ticket journal endpoint

**Files:**
- Modify: `agent/api/tickets/handler.go`, `atc/routes.go`, `atc/api/handler.go`
- Test: `agent/api/tickets/handler_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/api/tickets/handler_test.go`:

```go
func TestTicketRunsAreChronologicalAcrossWorkflows(t *testing.T) {
	handler := newHandlerWithTicketRuns(t, []testRun{
		{ID: 3, WorkflowName: "qa", CreatedAt: at(30)},
		{ID: 1, WorkflowName: "small-fix", CreatedAt: at(10)},
		{ID: 2, WorkflowName: "pr-create", CreatedAt: at(20)},
		{ID: 4, WorkflowName: "small-fix", CreatedAt: at(40)},
	})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Form = map[string][]string{":ticket_id": {"42"}}
	handler.ListRuns(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body)
	}

	var response struct {
		Runs []struct {
			WorkflowRunID string `json:"workflow_run_id"`
			WorkflowName  string `json:"workflow_name"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	var order []string
	for _, run := range response.Runs {
		order = append(order, run.WorkflowRunID)
	}
	want := []string{"1", "2", "3", "4"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("expected chronological order %v, got %v", want, order)
	}

	// A repeated execution of the same workflow is its own entry, not a merge.
	if len(response.Runs) != 4 {
		t.Fatalf("expected four separate entries, got %d", len(response.Runs))
	}
}

func TestTicketRunsIssueOneQuery(t *testing.T) {
	store := &countingRunStore{}
	handler := newHandlerWithStore(t, store)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Form = map[string][]string{":ticket_id": {"42"}}
	handler.ListRuns(recorder, request)

	if store.listCalls != 1 {
		t.Fatalf("the journal must not issue one query per workflow name; got %d calls", store.listCalls)
	}
}
```

- [ ] **Step 2: Run the tests and confirm they fail**

```bash
go test ./agent/api/tickets/ -run TestTicketRuns -count=1
```

Expected: FAIL — `handler.ListRuns undefined`.

- [ ] **Step 3: Write the handler**

Add `ListRuns` to `agent/api/tickets/handler.go`, calling the run store once with `AgentWorkflowRunListFilter{TeamID: ..., TicketID: &ticketID}` and presenting journal entries: workflow name, exact revision, run status and timestamps, retry-of identity for grouping, outcome or publication state, outstanding action, and the run's own link identity.

Register the route in `atc/routes.go`:

```go
	ListAgentTicketRuns = "ListAgentTicketRuns"
```

```go
	{Path: "/api/v1/agent/tickets/:ticket_id/runs", Method: "GET", Name: ListAgentTicketRuns},
```

and dispatch it in `atc/api/handler.go`.

- [ ] **Step 4: Run the tests and confirm they pass**

```bash
go test ./agent/api/... ./atc/api/ -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/api/tickets/ atc/routes.go atc/api/handler.go
git commit -m "feat(api): chronological ticket run journal"
```

### Task F4: Journal UI

**Files:**
- Create: `web/elm/src/AgentTicket/Journal.elm`
- Test: `web/elm/tests/AgentTicketJournalTests.elm`
- Modify: `web/elm/src/AgentTickets/`, `Api/Endpoints.elm`, `Message/Effects.elm`, `Message/Callback.elm`

- [ ] **Step 1: Write the failing test**

Create `web/elm/tests/AgentTicketJournalTests.elm`:

```elm
module AgentTicketJournalTests exposing (all)

import AgentTicket.Journal as Journal
import Expect
import Test exposing (Test, describe, test)
import Test.Html.Query as Query
import Test.Html.Selector exposing (class, text)


all : Test
all =
    describe "ticket journal"
        [ test "lists every run occurrence in chronological order" <|
            \_ ->
                render entries
                    |> Query.findAll [ class "agent-journal-entry" ]
                    |> Query.count (Expect.equal 4)
        , test "keeps repeated executions of one workflow as separate entries" <|
            \_ ->
                render entries
                    |> Query.findAll [ class "agent-journal-entry-workflow" ]
                    |> Query.index 3
                    |> Query.has [ text "small-fix" ]
        , test "elevates an outstanding action" <|
            \_ ->
                render entries
                    |> Query.find [ class "agent-journal-entry--outstanding" ]
                    |> Query.has [ text "waiting at approval" ]
        , test "groups a retry with its source" <|
            \_ ->
                render entries
                    |> Query.find [ class "agent-journal-entry--retry" ]
                    |> Query.has [ text "retry of 1" ]
        , test "draws no causal edges between entries" <|
            \_ ->
                render entries
                    |> Query.hasNot [ class "agent-journal-edge" ]
        , test "shows an empty state for a ticket with no runs" <|
            \_ ->
                render []
                    |> Query.find [ class "agent-journal-empty" ]
                    |> Query.has [ text "No runs yet" ]
        ]
```

Define `entries` and `render` in the same file using `Journal.Entry`.

- [ ] **Step 2: Run the test and confirm it fails**

```bash
cd web/elm && elm-test tests/AgentTicketJournalTests.elm
```

Expected: FAIL — module not found.

- [ ] **Step 3: Write the module**

Create `web/elm/src/AgentTicket/Journal.elm` exposing `Entry` and `view`. Render a flat chronological list: workflow name and revision, status and timestamps, retry grouping, outcome or publication state, outstanding action, and a link to the run. Unresolved waits and failures get the `--outstanding` modifier; completed entries collapse. There is no graph and there are no edges between entries — ordering is by timestamp, never by invented causality.

- [ ] **Step 4: Wire the fetch and render it on the ticket page**

Add `AgentTicketRuns Int` to `Endpoints.elm`, `FetchAgentTicketRuns` to `Effects.elm`, and `AgentTicketRunsFetched` to `Callback.elm`. Render `Journal.view` on the ticket page.

- [ ] **Step 5: Run the tests and rebuild**

```bash
cd web/elm && elm-test && cd ../.. && yarn build
```

Expected: PASS, then a successful build.

- [ ] **Step 6: Commit**

```bash
git add web/elm/src/AgentTicket/ web/elm/src/Api/Endpoints.elm web/elm/src/Message/ web/elm/tests/ web/public/
git commit -m "feat(web): chronological ticket run journal"
```

---

## Final verification

Run once, at the end, not after every task.

- [ ] **Unit and Elm suites**

```bash
make test-unit
```

Expected: PASS, 121 Ginkgo suites, roughly eight minutes. PostgreSQL must be running — check with `pg_isready`.

```bash
yarn test
```

Expected: PASS.

- [ ] **Integration and migration**

```bash
make test-integration && go test ./atc/db/migration/ -count=1
```

Expected: PASS, 24/24 integration specs.

- [ ] **Fly integration** (routes changed, so the mock ATC contract matters)

```bash
make test-fly-integration
```

Expected: PASS, 666/666.

- [ ] **Live verification on theborg**

Deploy and confirm against real workflow shapes: open a workflow with an active run and a resolved retry, check that the retried node reads as resolved while the earlier failure is still findable in the run list, select a node and confirm the list narrows, open a run and confirm its DAG matches its own revision rather than the promoted one, and open a ticket with runs from more than one workflow.

Remember that a web or Elm change requires a rebuilt and committed `elm.min.js`; a stale bundle ships old UI from correct source.

---

## Deferred to a later slice

Recorded here rather than silently dropped:

- duration and cost distributions, revision-comparison charts, and outcome analytics on selected-node detail;
- free-text search over prompts and transcripts;
- custom and adaptive time windows;
- backfilling `agent_publication_occurrences.plan_id` for runs that predate migration `1773106157`;
- pan and zoom, plus rank-level virtualization, for very large graphs. Fit-to-width comes free from the SVG `viewBox` that `Layout` sizes to the full extent, so the first release is readable without either; add them only when a real workflow proves fit-to-width insufficient.
