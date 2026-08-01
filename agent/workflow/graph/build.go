package graph

import (
	"fmt"
	"sort"

	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc"
)

// Endpoint nodes (input, output, resource_source) never execute and never
// carry a durable occurrence, so their IDs are kind-qualified: nothing
// upstream (compile or render time) prevents a public output port from
// sharing its bare name with an execution node's function_id, and the
// shipped agent/workflow/seeds/code-review-v3 seed does exactly that (output
// port "review", from: review, alongside agent function_id "review"). See
// docs/superpowers/specs/2026-07-31-workflow-run-first-ui-design.md, "Stable
// node identity".
//
// Execution nodes (agent, task, await, publish, load) keep their bare
// workflow-local identity: the durable node-occurrence projection is keyed on
// exactly that, and function_id uniqueness is already enforced by
// agent/workflow/typecheck.go.
const (
	inputNodePrefix          = "input:"
	outputNodePrefix         = "output:"
	resourceSourceNodePrefix = "source:"
)

func inputNodeID(name string) string          { return inputNodePrefix + name }
func outputNodeID(name string) string         { return outputNodePrefix + name }
func resourceSourceNodeID(name string) string { return resourceSourceNodePrefix + name }

// Build derives the semantic DAG of a compiled function. It mirrors the step
// dispatch in agent/workflow/typecheck.go so that node identity and typed
// producer-to-consumer linkage stay consistent with the type checker, but
// records nodes and edges instead of validating flow.
//
// Build assumes the function already type-checked. Step kinds the type
// checker rejects are treated as programmer error; A2/A3 only handle agent
// and task leaves. Wrapped/conditional steps, and await/publish/load nodes,
// are added by later tasks.
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
		nodeID := inputNodeID(port.Name)
		builder.addNode(Node{
			ID:          nodeID,
			Kind:        KindInput,
			DisplayName: port.Name,
			TypeRef:     string(port.Type),
			Optional:    port.Optional,
		})
		builder.produce(port.Name, nodeID, string(port.Type))
	}

	for _, source := range function.ResourceSources {
		nodeID := resourceSourceNodeID(source.Name)
		builder.addNode(Node{
			ID:          nodeID,
			Kind:        KindResourceSource,
			DisplayName: source.Name,
			TypeRef:     string(source.Type),
		})
		builder.produce(source.Name, nodeID, string(source.Type))
	}

	if err := builder.walkSequence(function.Plan, nil); err != nil {
		return Graph{}, err
	}

	for _, output := range function.Outputs {
		nodeID := outputNodeID(output.Port.Name)
		builder.addNode(Node{
			ID:          nodeID,
			Kind:        KindOutput,
			DisplayName: output.Port.Name,
			TypeRef:     string(output.Port.Type),
			Optional:    output.Port.Optional,
		})
		if err := builder.link(output.From, nodeID); err != nil {
			return Graph{}, err
		}
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
	// producers maps a snapshot binding name (the name the type checker's
	// snapshotEnvironment uses, not a graph node ID) to the node ID that most
	// recently produced it.
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
// An unknown binding produces no edge: Build assumes the function already
// type-checked, so the only way a consumed binding has no known producer here
// is a step kind A2/A3 does not yet model (await/publish/load, added by later
// tasks) — and walkSequence already fails the whole Build before reaching a
// consumer of such a binding, since the plan is walked in the same order the
// type checker builds its environment.
//
// producer == nodeID (a genuine self-loop) is now an invariant violation
// rather than a case to route around. It is unreachable given the current
// identity scheme: execution nodes have a single addLeaf call per unique
// function_id and check their inputs against the environment as it stood
// before their own outputs are added, so a node cannot consume its own
// production; endpoint nodes are exclusively a producer (input,
// resource_source) or exclusively a consumer (output), never both. Kept as a
// hard error, not a silent skip, so a future node kind that violates the
// invariant fails loudly instead of silently mislinking the graph.
func (b *builder) link(bindingName, nodeID string) error {
	producer, found := b.producers[bindingName]
	if !found {
		return nil
	}
	if producer == nodeID {
		return fmt.Errorf("graph: internal error: node %q cannot consume binding %q that it produced itself", nodeID, bindingName)
	}
	b.graph.Edges = append(b.graph.Edges, Edge{
		From:     producer,
		To:       nodeID,
		PortName: bindingName,
		TypeRef:  b.types[bindingName],
	})
	return nil
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
// StepConfig, while TryStep.Step, DoStep.Steps, and each hook's .Hook are
// Step. Splitting walkStep/walkStepConfig keeps both shapes available for
// Task A5 to dispatch on, without forcing every wrapper through an
// artificial atc.Step wrapping.
func (b *builder) walkStepConfig(stepConfig atc.StepConfig, decorations []Decoration) error {
	if stepConfig == nil {
		return fmt.Errorf("graph: step config is required")
	}

	switch config := stepConfig.(type) {
	case *atc.AgentStep:
		// agent/workflow/typecheck.go's checkAgent resolves SnapshotInputs and
		// SnapshotOutputs through InputMapping/OutputMapping before consulting
		// the snapshot environment (workflow.MappedSnapshotInputs/
		// MappedSnapshotOutputs), so the map keys here are pre-mapping logical
		// names. Mirror that so an agent step using input_mapping/
		// output_mapping links on the same binding names the type checker
		// actually bound.
		typedInputs := workflow.MappedSnapshotInputs(config.SnapshotInputs, config.InputMapping)
		typedOutputs := workflow.MappedSnapshotOutputs(config.SnapshotOutputs, config.OutputMapping)
		return b.addLeaf(config.FunctionID, KindAgent, config.Name, decorations, typedInputs, typedOutputs)
	case *atc.TaskStep:
		// agent/workflow/typecheck.go's checkTask passes step.SnapshotInputs and
		// step.SnapshotOutputs straight through with no mapping translation
		// (unlike checkAgent). Mirror that asymmetry rather than inventing a
		// different contract for tasks.
		return b.addLeaf(config.FunctionID, KindTask, config.Name, decorations, config.SnapshotInputs, config.SnapshotOutputs)
	default:
		return fmt.Errorf("graph: unsupported step config %T", config)
	}
}

func (b *builder) addLeaf(
	nodeID string,
	kind NodeKind,
	displayName string,
	decorations []Decoration,
	typedInputs map[string]atc.SnapshotInputConfig,
	typedOutputs map[string]atc.SnapshotOutputConfig,
) error {
	b.addNode(Node{
		ID:          nodeID,
		Kind:        kind,
		DisplayName: displayName,
		Decorations: append([]Decoration(nil), decorations...),
	})
	for _, name := range workflow.SortedSnapshotInputKeys(typedInputs) {
		if err := b.link(name, nodeID); err != nil {
			return err
		}
	}
	for _, name := range workflow.SortedSnapshotOutputKeys(typedOutputs) {
		b.produce(name, nodeID, string(typedOutputs[name].Type))
	}
	return nil
}
