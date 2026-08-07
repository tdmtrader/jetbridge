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
// "Mirrors" is literal, not aspirational: the producer environment is scoped
// and merged exactly the way snapshotEnvironment is (see environment.go), and
// every wrapper case below names the typecheck.go function it copies. A hook
// walked against the wrong environment does not merely look odd — it asserts
// a typed dependency the type checker specifically disproved.
//
// Build assumes the function already type-checked. Step kinds the type
// checker rejects are treated as programmer error; agent, task, await,
// publish, and load leaves are handled (A2-A4). Wrapped/conditional steps
// (do, in_parallel, retry, timeout, try, ensure, hooks) decorate the node or
// branch they affect rather than becoming nodes of their own (A5) — the
// graph shows semantic workflow meaning, not serialized plan syntax.
func Build(function *workflow.FunctionConfig) (Graph, error) {
	if function == nil {
		return Graph{}, fmt.Errorf("graph: function config is required")
	}

	// Nodes and Edges are initialized rather than left nil so an empty graph
	// marshals as {"nodes":[],"edges":[]}. The Elm frontend decodes both with
	// Json.Decode.list, which fails outright on null, so a nil slice here
	// would break the page instead of rendering an empty workflow.
	builder := &builder{
		graph: Graph{Nodes: []Node{}, Edges: []Edge{}},
		seen:  map[string]bool{},
	}

	// analyzeFunctionFlow seeds its environment with inputs then resource
	// sources, before walking the plan. Mirror that order.
	env := environment{}
	for _, port := range function.Inputs {
		nodeID := inputNodeID(port.Name)
		if err := builder.addNode(Node{
			ID:          nodeID,
			Kind:        KindInput,
			DisplayName: port.Name,
			TypeRef:     string(port.Type),
			Optional:    port.Optional,
		}); err != nil {
			return Graph{}, err
		}
		env[port.Name] = typedBinding(nodeID, string(port.Type), port.Optional)
	}

	for _, source := range function.ResourceSources {
		nodeID := resourceSourceNodeID(source.Name)
		if err := builder.addNode(Node{
			ID:          nodeID,
			Kind:        KindResourceSource,
			DisplayName: source.Name,
			TypeRef:     string(source.Type),
		}); err != nil {
			return Graph{}, err
		}
		env[source.Name] = typedBinding(nodeID, string(source.Type), false)
	}

	result, err := builder.walkSequence(function.Plan, env, nil)
	if err != nil {
		return Graph{}, err
	}

	for _, output := range function.Outputs {
		nodeID := outputNodeID(output.Port.Name)
		if err := builder.addNode(Node{
			ID:          nodeID,
			Kind:        KindOutput,
			DisplayName: output.Port.Name,
			TypeRef:     string(output.Port.Type),
			Optional:    output.Port.Optional,
		}); err != nil {
			return Graph{}, err
		}
		// The edge is labelled with the PUBLIC port name, not with the binding
		// it consumed. Everything downstream of this graph identifies a
		// run-level output by the public port: typecheck.go's
		// AnnotatePublicOutputs writes output.Port.Name into the producer's
		// WorkflowPort, that becomes agent_workflow_run_snapshots.port_name at
		// seal time, and workflowruns.OutputManifest.Port serves it. Labelling
		// the edge with output.From instead put the two names in different
		// namespaces whenever a workflow declares `from:` different from the
		// port name — which every shipped seed that produces a
		// repository-change does (`{name: change, from: candidate}`) — so the
		// run page could never attribute a workflow's own deliverable to the
		// node that produced it.
		//
		// The producer is still resolved through output.From: that is the
		// binding the type checker proved, and it is what decides WHICH node
		// the edge leaves. Only the label changes.
		if err := builder.linkAs(result.env, output.From, nodeID, output.Port.Name); err != nil {
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

// builder accumulates the graph. It deliberately holds no environment: the
// producer environment is threaded as an explicit parameter, exactly as
// snapshotFlowChecker threads snapshotEnvironment, so a wrapper physically
// cannot leak its scope into a sibling.
type builder struct {
	graph Graph
	seen  map[string]bool
}

// addNode fails on an ID collision rather than silently dropping the second
// node.
//
// The type checker's registerNodeIdentity already makes execution-node IDs
// unique among themselves, so the only reachable collision is an execution
// node whose authored identity is literally "input:x", "output:x", or
// "source:x" — which type-checks fine and, under a silent drop, would make
// the node vanish from the graph with no diagnostic. That is the same class
// of silent-loss bug the kind-qualified endpoint prefixes were introduced to
// fix.
//
// Reserving those three prefixes in typecheck.go's registerNodeIdentity was
// the alternative. It is rejected here: it would retroactively invalidate
// already-admitted workflow definitions sitting in the database (admission is
// immutable, so a definition that type-checked once must keep type-checking),
// and it would move a graph-rendering identity concern into the immutable
// function boundary. Failing loudly at render time keeps the blast radius to
// the page that cannot be drawn.
func (b *builder) addNode(node Node) error {
	if b.seen[node.ID] {
		return fmt.Errorf("graph: duplicate node id %q; an execution node cannot share an identity with an endpoint node", node.ID)
	}
	b.seen[node.ID] = true
	b.graph.Nodes = append(b.graph.Nodes, node)
	return nil
}

// link records an edge from whichever node produced bindingName in env into
// nodeID.
//
// An unknown binding produces no edge: Build assumes the function already
// type-checked, so a consumed binding with no producer in scope cannot occur.
// An untyped binding produces no edge either — checkLeaf refuses to bind a
// typed consumer to one ("occupied by an untyped producer" / "ambiguous
// possible writes"), so drawing an edge would assert a link the type checker
// rejects. That second guard is unreachable today rather than merely untaken
// (see addLeaf on why no untyped producer binding survives type checking, and
// note that a merge of two differently typed alternatives has no legal
// consumer either), and is kept because it is what the mirror actually says.
//
// A binding with more than one possible producer draws one edge per producer.
// The type checker reduces such a binding to "no single provable producer";
// the graph instead shows every candidate, marked Optional, because a reader
// asking "where could this have come from?" needs the alternatives and a
// specific run will have taken exactly one of them.
//
// producer == nodeID (a genuine self-loop) is an invariant violation rather
// than a case to route around. It is unreachable given the current identity
// scheme: execution nodes have a single addLeaf call per unique function_id
// and check their inputs against the environment as it stood before their own
// outputs are added, so a node cannot consume its own production; endpoint
// nodes are exclusively a producer (input, resource_source) or exclusively a
// consumer (output), never both. Kept as a hard error, not a silent skip, so
// a future node kind that violates the invariant fails loudly instead of
// silently mislinking the graph.
func (b *builder) link(env environment, bindingName, nodeID string) error {
	return b.linkAs(env, bindingName, nodeID, bindingName)
}

// linkAll is link over several consumed bindings, with the two guards a
// hand-written loop kept getting wrong: an empty name is skipped rather than
// looked up, and a name consumed twice by the same node draws one edge rather
// than two.
//
// The dedup is not cosmetic. Two byte-identical Edges are a contradiction of
// what an Edge means (Edge has no identity beyond
// From/To/PortName/TypeRef/Optional, so "the labelled connection between a
// producer and a consumer" cannot occur twice) and would double the line every
// consumer of the contract draws.
func (b *builder) linkAll(env environment, bindingNames []string, nodeID string) error {
	linked := make(map[string]bool, len(bindingNames))
	for _, name := range bindingNames {
		if name == "" || linked[name] {
			continue
		}
		linked[name] = true
		if err := b.link(env, name, nodeID); err != nil {
			return err
		}
	}
	return nil
}

// linkAs is link with an explicit edge label. The binding still selects the
// producer; portName is what the edge is called. It exists for the public
// output boundary, where the consumed binding and the port a reader (and every
// durable record) knows the value by are two different names. See the call
// site in Build.
func (b *builder) linkAs(env environment, bindingName, nodeID, portName string) error {
	source, found := env[bindingName]
	if !found || !source.typed {
		return nil
	}
	// A single guaranteed producer is the only case where the dataflow itself
	// is certain; anything conditional or path-dependent is marked so Phase D
	// and E can render "this may not have run".
	optional := source.optional || source.ambiguous || len(source.producers) > 1
	for _, producer := range source.producers {
		if producer == nodeID {
			return fmt.Errorf("graph: internal error: node %q cannot consume binding %q that it produced itself", nodeID, bindingName)
		}
		b.graph.Edges = append(b.graph.Edges, Edge{
			From:     producer,
			To:       nodeID,
			PortName: portName,
			TypeRef:  source.typeRef,
			Optional: optional,
		})
	}
	return nil
}

// walkSequence mirrors typecheck.go's checkSequence.
func (b *builder) walkSequence(steps []atc.Step, entry environment, decorations []Decoration) (flow, error) {
	env := entry.clone()
	produced := bindings{}
	mayProduced := bindings{}
	allProduced := bindings{}
	for index := range steps {
		child, err := b.walkStep(steps[index], env, decorations)
		if err != nil {
			return flow{}, err
		}
		env = child.env
		mergeProduced(produced, child.produced)
		mergeMayProduced(mayProduced, child.mayProduced)
		mergeMayProduced(allProduced, child.allProduced)
	}
	return flow{env: env, produced: produced, mayProduced: mayProduced, allProduced: allProduced}, nil
}

func (b *builder) walkStep(step atc.Step, env environment, decorations []Decoration) (flow, error) {
	return b.walkStepConfig(step.Config, env, decorations)
}

// walkStepConfig is the StepConfig-typed entry point and mirrors
// typecheck.go's checkStep. atc's wrapper steps are not uniform:
// TimeoutStep.Step, RetryStep.Step, and every hook's .Step are StepConfig,
// while TryStep.Step, DoStep.Steps, and each hook's .Hook are Step. Splitting
// walkStep/walkStepConfig keeps both shapes available without forcing every
// wrapper through an artificial atc.Step wrapping — checkWrapped is the type
// checker's equivalent seam.
func (b *builder) walkStepConfig(stepConfig atc.StepConfig, env environment, decorations []Decoration) (flow, error) {
	if stepConfig == nil {
		return flow{}, fmt.Errorf("graph: step config is required")
	}

	switch config := stepConfig.(type) {
	case *atc.AgentStep:
		// Mirrors checkAgent, which resolves SnapshotInputs and SnapshotOutputs
		// through InputMapping/OutputMapping before consulting the snapshot
		// environment (workflow.MappedSnapshotInputs/MappedSnapshotOutputs), so
		// the map keys here are pre-mapping logical names. Mirror that so an
		// agent step using input_mapping/output_mapping links on the same
		// binding names the type checker actually bound.
		return b.addLeaf(
			config.FunctionID, KindAgent, config.Name, decorations, env,
			workflow.MappedSnapshotInputs(config.SnapshotInputs, config.InputMapping),
			workflow.MappedSnapshotOutputs(config.SnapshotOutputs, config.OutputMapping),
		)
	case *atc.TaskStep:
		// Mirrors checkTask, which passes step.SnapshotInputs and
		// step.SnapshotOutputs straight through with no mapping translation
		// (unlike checkAgent). Mirror that asymmetry rather than inventing a
		// different contract for tasks.
		return b.addLeaf(config.FunctionID, KindTask, config.Name, decorations, env, config.SnapshotInputs, config.SnapshotOutputs)
	case *atc.AwaitSnapshotStep:
		return b.walkAwaitSnapshot(config, env, decorations)
	case *atc.PublishSnapshotStep:
		return b.walkPublishSnapshot(config, env, decorations)
	case *atc.LoadSnapshotStep:
		return b.walkLoadSnapshot(config, env, decorations)
	case *atc.DoStep:
		// checkStep dispatches do to checkSequence.
		return b.walkSequence(config.Steps, env, decorations)
	case *atc.InParallelStep:
		return b.walkParallel(config, env, decorations)
	case *atc.TryStep:
		// Mirrors checkStep's TryStep arm: the child is checked against a
		// clone of the entry environment and folded back through
		// conditionalTryFlow, so nothing inside a try is guaranteed on the way
		// out. TryStep.Step is atc.Step, unlike Timeout/Retry below.
		child, err := b.walkStep(config.Step, env.clone(), appendDecoration(decorations, DecorationTry))
		if err != nil {
			return flow{}, err
		}
		return conditionalTryFlow(env, child), nil
	case *atc.RetryStep:
		// Mirrors checkStep's RetryStep arm: snapshot attempt scopes discard
		// every failed attempt, so only a child's successful writes can become
		// externally observable through retry.
		child, err := b.walkStepConfig(config.Step, env, appendDecoration(decorations, DecorationRetry))
		if err != nil {
			return flow{}, err
		}
		child.allProduced = cloneBindings(child.mayProduced)
		return child, nil
	case *atc.TimeoutStep:
		// Mirrors checkStep's TimeoutStep arm: a pass-through to checkWrapped.
		return b.walkStepConfig(config.Step, env, appendDecoration(decorations, DecorationTimeout))
	case *atc.OnSuccessStep:
		// Mirrors checkStep's OnSuccessStep arm, the one hook the checker
		// genuinely evaluates against main.env: on_success runs only when the
		// main step succeeded, so the main step's writes are facts for it, and
		// its own writes continue down the success path.
		main, err := b.walkStepConfig(config.Step, env, decorations)
		if err != nil {
			return flow{}, err
		}
		hook, err := b.walkStep(config.Hook, main.env, appendDecoration(decorations, DecorationOnSuccess))
		if err != nil {
			return flow{}, err
		}
		mergeProduced(main.produced, hook.produced)
		mergeMayProduced(main.mayProduced, hook.mayProduced)
		mergeMayProduced(main.allProduced, hook.allProduced)
		return flow{env: hook.env, produced: main.produced, mayProduced: main.mayProduced, allProduced: main.allProduced}, nil
	case *atc.OnFailureStep:
		return b.walkNonSuccessHook(config.Step, config.Hook, env, decorations, DecorationOnFailure)
	case *atc.OnErrorStep:
		return b.walkNonSuccessHook(config.Step, config.Hook, env, decorations, DecorationOnError)
	case *atc.OnAbortStep:
		return b.walkNonSuccessHook(config.Step, config.Hook, env, decorations, DecorationOnAbort)
	case *atc.EnsureStep:
		return b.walkEnsure(config, env, decorations)
	default:
		return flow{}, fmt.Errorf("graph: unsupported step config %T", config)
	}
}

// walkNonSuccessHook mirrors typecheck.go's checkNonSuccessHook, which backs
// on_failure, on_error, and on_abort.
//
// Two scoping rules are load-bearing and were both violated by threading one
// mutable producer map through main and hook:
//
//   - The hook is walked against a clone of the *entry* environment, not
//     against the main step's outgoing environment. A hook runs precisely
//     because the main step did not succeed, so the main step's writes are not
//     available to it. Linking a hook consumer to a producer inside the main
//     step asserts a dependency the type checker resolves elsewhere.
//   - The hook's writes never enter the outgoing environment. Failure, error,
//     and abort remain non-success results, so a node after the wrapper binds
//     to whatever produced the name before the wrapper. Drawing an edge out of
//     a failure-only hook into the success path renders a dependency that
//     could never have executed in a successful run.
//
// The hook's writes are still recorded in allProduced, so an enclosing try or
// ensure sees them as possible non-success writes.
func (b *builder) walkNonSuccessHook(
	mainStep atc.StepConfig,
	hookStep atc.Step,
	entry environment,
	decorations []Decoration,
	hookDecoration Decoration,
) (flow, error) {
	main, err := b.walkStepConfig(mainStep, entry, decorations)
	if err != nil {
		return flow{}, err
	}
	hook, err := b.walkStep(hookStep, entry.clone(), appendDecoration(decorations, hookDecoration))
	if err != nil {
		return flow{}, err
	}
	mergeMayProduced(main.allProduced, hook.allProduced)
	return main, nil
}

// walkEnsure mirrors typecheck.go's checkEnsure. An ensure hook runs on both
// outcomes, so its entry environment is the conservative merge built by
// conservativeEnsureEnvironment: the main step's writes are alternatives, not
// facts. On the wrapper's successful exit the main step necessarily succeeded,
// so applySuccessfulFlow layers only the hook's own successful writes onto the
// main step's exact environment.
func (b *builder) walkEnsure(config *atc.EnsureStep, entry environment, decorations []Decoration) (flow, error) {
	main, err := b.walkStepConfig(config.Step, entry, decorations)
	if err != nil {
		return flow{}, err
	}
	hook, err := b.walkStep(config.Hook, conservativeEnsureEnvironment(entry, main), appendDecoration(decorations, DecorationEnsure))
	if err != nil {
		return flow{}, err
	}

	produced := cloneBindings(main.produced)
	mergeProduced(produced, hook.produced)
	mayProduced := cloneBindings(main.mayProduced)
	mergeMayProduced(mayProduced, hook.mayProduced)
	allProduced := cloneBindings(main.allProduced)
	mergeMayProduced(allProduced, hook.allProduced)
	return flow{
		env:         applySuccessfulFlow(main.env, hook),
		produced:    produced,
		mayProduced: mayProduced,
		allProduced: allProduced,
	}, nil
}

// walkParallel mirrors typecheck.go's checkParallel. Each branch is walked
// against its own clone of the entry environment, so a later branch never sees
// an earlier branch's writes; the outgoing environment takes each produced
// name from the single branch that produced it.
//
// The checker additionally rejects two branches producing the same name. Build
// assumes a type-checked function, so first-branch-wins below is unreachable
// rather than a policy.
func (b *builder) walkParallel(config *atc.InParallelStep, entry environment, decorations []Decoration) (flow, error) {
	branches := make([]flow, len(config.Config.Steps))
	producerBranch := map[string]int{}
	for index := range config.Config.Steps {
		branch, err := b.walkStep(config.Config.Steps[index], entry.clone(), decorations)
		if err != nil {
			return flow{}, err
		}
		branches[index] = branch
		for _, name := range sortedBindingNames(branch.mayProduced) {
			if _, found := producerBranch[name]; found {
				continue
			}
			producerBranch[name] = index
		}
	}

	env := entry.clone()
	produced := bindings{}
	mayProduced := bindings{}
	allProduced := bindings{}
	for _, name := range sortedBranchNames(producerBranch) {
		branch := branches[producerBranch[name]]
		if value, found := branch.env[name]; found {
			env[name] = value
		}
		if value, found := branch.produced[name]; found {
			produced[name] = value
		}
		mayProduced[name] = branch.mayProduced[name]
	}
	for index := range branches {
		mergeMayProduced(allProduced, branches[index].allProduced)
	}
	return flow{env: env, produced: produced, mayProduced: mayProduced, allProduced: allProduced}, nil
}

// walkAwaitSnapshot mirrors typecheck.go's checkAwaitSnapshot.
//
// An await is an execution node: it has a durable wait record and is often
// exactly where attention is required. It consumes exactly one of three
// shapes (checkAwaitSnapshot dispatches the same way): a plain question
// binding, or a merge-approval intent over one repository-change/v1 input plus
// its authoritative validation. It always produces its own name, and always
// produces it — an await is never conditional.
func (b *builder) walkAwaitSnapshot(config *atc.AwaitSnapshotStep, env environment, decorations []Decoration) (flow, error) {
	if err := b.addNode(Node{
		ID:          config.Name,
		Kind:        KindAwait,
		DisplayName: config.Name,
		TypeRef:     string(config.Type),
		Decorations: copyDecorations(decorations),
	}); err != nil {
		return flow{}, err
	}

	var consumed []string
	if config.MergeApproval != nil {
		consumed = []string{config.MergeApproval.Input, config.Validation}
	} else {
		consumed = []string{config.Question}
	}
	if err := b.linkAll(env, consumed, config.Name); err != nil {
		return flow{}, err
	}

	write := typedBinding(config.Name, string(config.Type), false)
	outgoing := env.clone()
	outgoing[config.Name] = write
	return leafFlow(outgoing, bindings{config.Name: write}), nil
}

// walkPublishSnapshot mirrors typecheck.go's checkPublishSnapshot.
//
// A publish is an execution node: it is an explicit external side-effect
// boundary. checkPublishSnapshot always consumes Input, and consumes
// Approval/Validation whenever they are set (merge mode keys off the Approval
// binding name). Publish produces nothing — it is a terminal side effect, not
// a snapshot producer — so it returns emptySnapshotFlow's graph equivalent.
func (b *builder) walkPublishSnapshot(config *atc.PublishSnapshotStep, env environment, decorations []Decoration) (flow, error) {
	if err := b.addNode(Node{
		ID:          config.Name,
		Kind:        KindPublish,
		DisplayName: config.Name,
		TypeRef:     string(config.InputType),
		Decorations: copyDecorations(decorations),
	}); err != nil {
		return flow{}, err
	}

	consumed := []string{config.Input, config.Approval, config.Validation}
	if err := b.linkAll(env, consumed, config.Name); err != nil {
		return flow{}, err
	}
	return emptyFlow(env), nil
}

// walkLoadSnapshot mirrors typecheck.go's checkLoadSnapshot.
//
// A load is an execution node: it materializes an existing snapshot into a new
// binding. checkLoadSnapshot consumes no bindings (WorkflowRunID/ID are
// renderer-owned parameters, not snapshot-flow bindings) and only ever
// produces its own name — conditionally when the step is optional, which is
// exactly what makes downstream consumers conditional too.
func (b *builder) walkLoadSnapshot(config *atc.LoadSnapshotStep, env environment, decorations []Decoration) (flow, error) {
	if err := b.addNode(Node{
		ID:          config.Name,
		Kind:        KindLoad,
		DisplayName: config.Name,
		TypeRef:     string(config.Type),
		Optional:    config.Optional,
		Decorations: copyDecorations(decorations),
	}); err != nil {
		return flow{}, err
	}
	write := typedBinding(config.Name, string(config.Type), config.Optional)
	outgoing := env.clone()
	outgoing[config.Name] = write
	return leafFlow(outgoing, bindings{config.Name: write}), nil
}

// addLeaf mirrors typecheck.go's checkLeaf for agent and task steps: inputs
// are linked against the entry environment, then outputs are written into a
// clone, so a leaf can never consume its own production.
//
// Untyped (ordinary, undeclared) outputs are deliberately not modelled.
// checkLeaf's validateExactTypedCoverage requires every declared ordinary
// output to have a typed declaration, and the one case that skips it — a
// file-backed task, where step.Config is nil — has no ordinary output names to
// enumerate either (effectiveTaskArtifactNames returns nil for a nil Config).
// An untyped producer binding is therefore unreachable in a function that
// type-checked.
func (b *builder) addLeaf(
	nodeID string,
	kind NodeKind,
	displayName string,
	decorations []Decoration,
	env environment,
	typedInputs map[string]atc.SnapshotInputConfig,
	typedOutputs map[string]atc.SnapshotOutputConfig,
) (flow, error) {
	if err := b.addNode(Node{
		ID:          nodeID,
		Kind:        kind,
		DisplayName: displayName,
		Decorations: copyDecorations(decorations),
	}); err != nil {
		return flow{}, err
	}
	for _, name := range workflow.SortedSnapshotInputKeys(typedInputs) {
		if err := b.link(env, name, nodeID); err != nil {
			return flow{}, err
		}
	}

	outgoing := env.clone()
	writes := bindings{}
	for _, name := range workflow.SortedSnapshotOutputKeys(typedOutputs) {
		declaration := typedOutputs[name]
		write := typedBinding(nodeID, string(declaration.Type), declaration.Optional)
		outgoing[name] = write
		writes[name] = write
	}
	return leafFlow(outgoing, writes), nil
}

// appendDecoration returns a new slice with decoration appended, never
// mutating or aliasing decorations' backing array. walkSequence calls
// walkStep/walkStepConfig once per sibling with the same decorations slice; a
// plain append(decorations, X) can reuse spare capacity in that shared backing
// array, so sibling A's decoration and sibling B's would be written to the
// same index.
//
// appendDecoration and copyDecorations below are each individually sufficient
// to make that unobservable — the append never shares, and the copy is taken
// before any sibling could overwrite — which mutation testing confirms:
// removing either alone changes no output, removing both does. They are kept
// as a pair deliberately. Losing the copy would make node decorations alias
// live walk state, and losing the fresh append would make correctness depend
// on every future consumer copying immediately. Neither is a property worth
// betting the contract on.
func appendDecoration(decorations []Decoration, decoration Decoration) []Decoration {
	next := make([]Decoration, len(decorations), len(decorations)+1)
	copy(next, decorations)
	return append(next, decoration)
}

// copyDecorations snapshots the decoration list onto a node so the node never
// aliases the walk's slice. An empty list stays nil so it is omitted from the
// JSON contract. See appendDecoration for why both copies exist.
func copyDecorations(decorations []Decoration) []Decoration {
	if len(decorations) == 0 {
		return nil
	}
	return append([]Decoration(nil), decorations...)
}
