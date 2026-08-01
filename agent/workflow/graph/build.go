package graph

import (
	"fmt"
	"sort"

	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc"
)

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
		builder.addNode(Node{
			ID:          port.Name,
			Kind:        KindInput,
			DisplayName: port.Name,
			TypeRef:     string(port.Type),
			Optional:    port.Optional,
		})
		builder.produce(port.Name, port.Name, string(port.Type))
	}

	for _, source := range function.ResourceSources {
		builder.addNode(Node{
			ID:          source.Name,
			Kind:        KindResourceSource,
			DisplayName: source.Name,
			TypeRef:     string(source.Type),
		})
		builder.produce(source.Name, source.Name, string(source.Type))
	}

	if err := builder.walkSequence(function.Plan); err != nil {
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
// An unknown binding produces no edge: Build assumes the function already
// type-checked, so the only way a consumed binding has no known producer here
// is a step kind A2/A3 does not yet model (await/publish/load, added by later
// tasks).
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

func (b *builder) walkSequence(steps []atc.Step) error {
	for index := range steps {
		if err := b.walkStep(steps[index]); err != nil {
			return err
		}
	}
	return nil
}

func (b *builder) walkStep(step atc.Step) error {
	if step.Config == nil {
		return fmt.Errorf("graph: step config is required")
	}

	switch config := step.Config.(type) {
	case *atc.AgentStep:
		// agent/workflow/typecheck.go's checkAgent applies InputMapping and
		// OutputMapping to SnapshotInputs/SnapshotOutputs before consulting the
		// snapshot environment (mappedSnapshotInputs/mappedSnapshotOutputs), so
		// the map keys here are pre-mapping logical names. Mirror that so an
		// agent step using input_mapping/output_mapping links on the same
		// binding names the type checker actually bound.
		typedInputs := mappedSnapshotInputs(config.SnapshotInputs, config.InputMapping)
		typedOutputs := mappedSnapshotOutputs(config.SnapshotOutputs, config.OutputMapping)
		b.addLeaf(config.FunctionID, KindAgent, config.Name, typedInputs, typedOutputs)
		return nil
	case *atc.TaskStep:
		// agent/workflow/typecheck.go's checkTask passes step.SnapshotInputs and
		// step.SnapshotOutputs straight through with no mapping translation
		// (unlike checkAgent). Mirror that asymmetry rather than inventing a
		// different contract for tasks.
		b.addLeaf(config.FunctionID, KindTask, config.Name, config.SnapshotInputs, config.SnapshotOutputs)
		return nil
	default:
		return fmt.Errorf("graph: unsupported step config %T", config)
	}
}

func (b *builder) addLeaf(
	nodeID string,
	kind NodeKind,
	displayName string,
	typedInputs map[string]atc.SnapshotInputConfig,
	typedOutputs map[string]atc.SnapshotOutputConfig,
) {
	b.addNode(Node{
		ID:          nodeID,
		Kind:        kind,
		DisplayName: displayName,
	})
	for _, name := range sortedSnapshotInputKeys(typedInputs) {
		b.link(name, nodeID)
	}
	for _, name := range sortedSnapshotOutputKeys(typedOutputs) {
		b.produce(name, nodeID, string(typedOutputs[name].Type))
	}
}

// mappedSnapshotInputs replicates agent/workflow's unexported helper of the
// same name: it translates each declared input's logical name through
// InputMapping to the binding name actually consumed from the snapshot
// environment.
func mappedSnapshotInputs(source map[string]atc.SnapshotInputConfig, mapping map[string]string) map[string]atc.SnapshotInputConfig {
	if source == nil {
		return nil
	}
	mapped := make(map[string]atc.SnapshotInputConfig, len(source))
	for name, config := range source {
		if replacement, found := mapping[name]; found {
			name = replacement
		}
		mapped[name] = config
	}
	return mapped
}

// mappedSnapshotOutputs is the output-side counterpart of mappedSnapshotInputs.
func mappedSnapshotOutputs(source map[string]atc.SnapshotOutputConfig, mapping map[string]string) map[string]atc.SnapshotOutputConfig {
	if source == nil {
		return nil
	}
	mapped := make(map[string]atc.SnapshotOutputConfig, len(source))
	for name, config := range source {
		if replacement, found := mapping[name]; found {
			name = replacement
		}
		mapped[name] = config
	}
	return mapped
}

func sortedSnapshotInputKeys(values map[string]atc.SnapshotInputConfig) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedSnapshotOutputKeys(values map[string]atc.SnapshotOutputConfig) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
