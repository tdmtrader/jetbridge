package graph

import "sort"

// This file is the graph package's mirror of the snapshot-flow model in
// agent/workflow/typecheck.go. The package's central claim is that it agrees
// with the type checker about typed producer-to-consumer linkage, so the
// environment must be scoped exactly the way the checker scopes it: cloned
// into a wrapper, merged back out under that wrapper's own rules. A single
// flat mutable producer map cannot express "the on_failure hook does not see
// the main step's writes, and the main path does not see the hook's".
//
// Every type and function here names the typecheck.go construct it mirrors.
// Keep them in step: a divergence is a wrong edge on the run page, which is a
// dependency claim the type checker never made.

// binding mirrors the graph-relevant fields of typecheck.go's snapshotBinding.
//
// It deliberately differs in one place. snapshotBinding.producer is a single
// pointer that mergeBindingAlternatives drops to nil when two alternatives
// disagree, because the checker only ever needs to know whether one exact
// producer is provable. The graph wants the opposite: the set of nodes that
// could have produced the binding, so a reader can see every possible
// dataflow. producers is therefore a sorted, deduplicated union, and
// ambiguous records that no single one of them is guaranteed.
type binding struct {
	// producers are the node IDs that may have produced this binding, sorted
	// and deduplicated. More than one means the write is path-dependent.
	producers []string
	typeRef   string
	// typed mirrors snapshotBinding.typed. An untyped binding is one the type
	// checker refuses to link a typed consumer to, so it produces no edges.
	typed bool
	// optional mirrors presence == snapshotConditional: the binding may not
	// exist at all in a given run.
	optional bool
	// ambiguous mirrors snapshotBinding.ambiguous: the binding survived a
	// merge of alternative writes, so any single producer is conditional even
	// when the binding itself is guaranteed to exist.
	ambiguous bool
}

// typedBinding is the graph analogue of the snapshotBinding a leaf writes for
// one of its declared typed outputs.
func typedBinding(nodeID, typeRef string, optional bool) binding {
	return binding{producers: []string{nodeID}, typeRef: typeRef, typed: true, optional: optional}
}

// conditional returns the binding with its write marked non-guaranteed. It is
// the graph's stand-in for typecheck.go's `write.presence = snapshotConditional`
// in conditionalTryFlow and conservativeEnsureEnvironment.
func (b binding) conditional() binding {
	b.optional = true
	return b
}

// environment mirrors typecheck.go's snapshotEnvironment.
type environment map[string]binding

func (env environment) clone() environment {
	clone := make(environment, len(env))
	for name, value := range env {
		clone[name] = value
	}
	return clone
}

// bindings mirrors the map[string]snapshotBinding write sets a snapshotFlow
// carries (produced, mayProduced, allProduced).
type bindings map[string]binding

func cloneBindings(source bindings) bindings {
	clone := make(bindings, len(source))
	for name, value := range source {
		clone[name] = value
	}
	return clone
}

// mergeProduced mirrors typecheck.go's mergeProduced: a later write replaces
// an earlier one outright.
func mergeProduced(destination, source bindings) {
	for _, name := range sortedBindingNames(source) {
		destination[name] = source[name]
	}
}

// mergeMayProduced mirrors typecheck.go's mergeMayProduced: a later write
// becomes an alternative to an earlier one rather than replacing it.
func mergeMayProduced(destination, source bindings) {
	for _, name := range sortedBindingNames(source) {
		value := source[name]
		if previous, found := destination[name]; found {
			value = mergeBindingAlternatives(previous, value)
		}
		destination[name] = value
	}
}

// mergeBindingAlternatives mirrors typecheck.go's function of the same name.
// The result is typed only when both alternatives are typed with the exact
// same type — the checker refuses to let a typed consumer read anything else,
// so the graph must not draw an edge for it either.
func mergeBindingAlternatives(left, right binding) binding {
	merged := binding{
		producers: mergeProducers(left.producers, right.producers),
		optional:  left.optional || right.optional,
		ambiguous: true,
	}
	if left.typed && right.typed && left.typeRef == right.typeRef {
		merged.typed = true
		merged.typeRef = left.typeRef
	}
	return merged
}

func mergeProducers(left, right []string) []string {
	seen := make(map[string]bool, len(left)+len(right))
	merged := make([]string, 0, len(left)+len(right))
	for _, group := range [][]string{left, right} {
		for _, producer := range group {
			if seen[producer] {
				continue
			}
			seen[producer] = true
			merged = append(merged, producer)
		}
	}
	sort.Strings(merged)
	return merged
}

// flow mirrors typecheck.go's snapshotFlow. The three write sets are not
// redundant: produced is the success-path write set, mayProduced additionally
// covers writes a nested try may or may not have made, and allProduced covers
// writes made on any outcome including failure hooks. Wrapper composition
// reads different ones (see conditionalTryFlow and applySuccessfulFlow), so
// collapsing them would reintroduce exactly the class of divergence this
// model exists to prevent.
type flow struct {
	env         environment
	produced    bindings
	mayProduced bindings
	allProduced bindings
}

// emptyFlow mirrors typecheck.go's emptySnapshotFlow: a step that consumes
// but never produces (publish_snapshot).
func emptyFlow(entry environment) flow {
	return flow{
		env:         entry.clone(),
		produced:    bindings{},
		mayProduced: bindings{},
		allProduced: bindings{},
	}
}

// leafFlow is the shape every producing leaf returns in typecheck.go: its
// writes are simultaneously produced, mayProduced, and allProduced.
func leafFlow(env environment, writes bindings) flow {
	return flow{
		env:         env,
		produced:    writes,
		mayProduced: cloneBindings(writes),
		allProduced: cloneBindings(writes),
	}
}

// conditionalTryFlow mirrors typecheck.go's function of the same name. A try
// contributes nothing to produced (nothing inside it is guaranteed), and its
// writes reach the outgoing environment as conditional alternatives.
func conditionalTryFlow(entry environment, child flow) flow {
	env := entry.clone()
	for _, name := range sortedBindingNames(child.allProduced) {
		write := child.allProduced[name]
		previous, existed := entry[name]
		if !existed {
			env[name] = write.conditional()
			continue
		}
		env[name] = mergeBindingAlternatives(previous, write)
	}
	return flow{
		env:         env,
		produced:    bindings{},
		mayProduced: cloneBindings(child.allProduced),
		allProduced: cloneBindings(child.allProduced),
	}
}

// conservativeEnsureEnvironment mirrors typecheck.go's function of the same
// name: the ensure hook must be checkable on the main step's failure path
// too, so the main step's writes enter the hook's entry environment as
// alternatives rather than as facts.
func conservativeEnsureEnvironment(entry environment, main flow) environment {
	env := entry.clone()
	for _, name := range sortedBindingNames(main.allProduced) {
		write := main.allProduced[name]
		previous, existed := entry[name]
		if !existed {
			env[name] = write.conditional()
			continue
		}
		env[name] = mergeBindingAlternatives(previous, write)
	}
	return env
}

// applySuccessfulFlow mirrors typecheck.go's function of the same name: on the
// ensure wrapper's successful exit the main step's environment is
// authoritative, and only the hook's own successful writes are layered on.
func applySuccessfulFlow(base environment, child flow) environment {
	env := base.clone()
	for _, name := range sortedBindingNames(child.mayProduced) {
		if _, definite := child.produced[name]; definite {
			if final, found := child.env[name]; found {
				env[name] = final
			} else {
				env[name] = child.produced[name]
			}
			continue
		}
		previous, existed := env[name]
		if !existed {
			continue
		}
		env[name] = mergeBindingAlternatives(previous, child.mayProduced[name])
	}
	return env
}

// sortedBindingNames mirrors typecheck.go's sortedSnapshotBindingKeys. Merge
// order must be deterministic so a graph never depends on map iteration.
func sortedBindingNames(values bindings) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func sortedBranchNames(values map[string]int) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
