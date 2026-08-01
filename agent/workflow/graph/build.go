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
// checker rejects are treated as programmer error; agent, task, await,
// publish, and load leaves are handled (A2-A4). Wrapped/conditional steps
// (retry, timeout, try, ensure, hooks) are added by a later task (A5).
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
	case *atc.AwaitSnapshotStep:
		// An await is an execution node: it has a durable wait record and is
		// often exactly where attention is required. It consumes exactly one
		// of three shapes (checkAwaitSnapshot in typecheck.go dispatches the
		// same way): a plain question binding, a merge-approval intent over
		// one repository-change/v1 input plus its authoritative validation,
		// or a PR-reapproval intent over four named artifacts plus the
		// accepted-review authority's own three bindings and its validation.
		// It always produces its own name.
		b.addNode(Node{
			ID:          config.Name,
			Kind:        KindAwait,
			DisplayName: config.Name,
			TypeRef:     string(config.Type),
			Decorations: append([]Decoration(nil), decorations...),
		})
		switch {
		case config.PRApproval != nil:
			for _, binding := range []string{
				config.PRApproval.Observation,
				config.PRApproval.Candidate,
				config.PRApproval.Impact,
				config.PRApproval.Response,
			} {
				if err := b.link(binding, config.Name); err != nil {
					return err
				}
			}
			if config.PRApproval.AcceptedReview != nil {
				for _, binding := range []string{
					config.PRApproval.AcceptedReview.Review,
					config.PRApproval.AcceptedReview.Candidate,
					config.PRApproval.AcceptedReview.Validation,
				} {
					if err := b.link(binding, config.Name); err != nil {
						return err
					}
				}
			}
			if config.Validation != "" {
				if err := b.link(config.Validation, config.Name); err != nil {
					return err
				}
			}
		case config.MergeApproval != nil:
			if err := b.link(config.MergeApproval.Input, config.Name); err != nil {
				return err
			}
			if config.Validation != "" {
				if err := b.link(config.Validation, config.Name); err != nil {
					return err
				}
			}
		default:
			if config.Question != "" {
				if err := b.link(config.Question, config.Name); err != nil {
					return err
				}
			}
		}
		b.produce(config.Name, config.Name, string(config.Type))
		return nil
	case *atc.PublishSnapshotStep:
		// A publish is an execution node: it is an explicit external
		// side-effect boundary. checkPublishSnapshot in typecheck.go always
		// consumes Input, and consumes Approval/Validation whenever they are
		// set (merge mode and the PR-reapproval fast path both key off the
		// same Approval binding name). When a PR-reapproval intent is
		// present, it also consumes the three accepted-review bindings that
		// prove no new human wait was required. Publish produces nothing —
		// it is a terminal side effect, not a snapshot producer.
		b.addNode(Node{
			ID:          config.Name,
			Kind:        KindPublish,
			DisplayName: config.Name,
			TypeRef:     string(config.InputType),
			Decorations: append([]Decoration(nil), decorations...),
		})
		if err := b.link(config.Input, config.Name); err != nil {
			return err
		}
		if config.Approval != "" {
			if err := b.link(config.Approval, config.Name); err != nil {
				return err
			}
		}
		if config.Validation != "" {
			if err := b.link(config.Validation, config.Name); err != nil {
				return err
			}
		}
		if config.PRApproval != nil && config.PRApproval.AcceptedReview != nil {
			for _, binding := range []string{
				config.PRApproval.AcceptedReview.Review,
				config.PRApproval.AcceptedReview.Candidate,
				config.PRApproval.AcceptedReview.Validation,
			} {
				if err := b.link(binding, config.Name); err != nil {
					return err
				}
			}
		}
		return nil
	case *atc.LoadSnapshotStep:
		// A load is an execution node: it materializes an existing snapshot
		// into a new binding. checkLoadSnapshot in typecheck.go consumes no
		// bindings (WorkflowRunID/ID are renderer-owned parameters, not
		// snapshot-flow bindings) and only ever produces its own name.
		b.addNode(Node{
			ID:          config.Name,
			Kind:        KindLoad,
			DisplayName: config.Name,
			TypeRef:     string(config.Type),
			Decorations: append([]Decoration(nil), decorations...),
		})
		b.produce(config.Name, config.Name, string(config.Type))
		return nil
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
