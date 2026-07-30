package workflow

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/goccy/go-yaml"
)

type NodeUpgradeStatus string

const (
	NodeUpgradeCreated               NodeUpgradeStatus = "created"
	NodeUpgradeUnchanged             NodeUpgradeStatus = "unchanged"
	NodeUpgradeFailed                NodeUpgradeStatus = "failed"
	NodeUpgradeRecompositionRequired NodeUpgradeStatus = "recomposition_required"
)

type NodeUpgradeRequest struct {
	NodeName  string   `json:"node_name"`
	Version   int      `json:"version"`
	Workflows []string `json:"workflows"`
	CreatedBy string   `json:"created_by"`
}

type NodeUpgradeResult struct {
	NodeName  string                      `json:"node_name"`
	Version   int                         `json:"version"`
	Workflows []NodeUpgradeWorkflowResult `json:"workflows"`
}

type NodeUpgradeWorkflowResult struct {
	Workflow    string                  `json:"workflow"`
	OldVersion  int                     `json:"old_version"`
	NewVersion  int                     `json:"new_version"`
	Status      NodeUpgradeStatus       `json:"status"`
	Error       string                  `json:"error,omitempty"`
	Obligations *NodeUpgradeObligations `json:"obligations,omitempty"`
}

// NodeUpgradeObligations describes the explicit recomposition work between a
// released successor and its immutable released predecessor. Names in every
// category are sorted so API and Fly callers receive a stable contract.
type NodeUpgradeObligations struct {
	Inputs     NodeContractChanges `json:"inputs"`
	Outputs    NodeContractChanges `json:"outputs"`
	Parameters NodeContractChanges `json:"parameters"`
}

type NodeContractChanges struct {
	Added   []string `json:"added"`
	Removed []string `json:"removed"`
	Changed []string `json:"changed"`
}

type NodeUpgradeService interface {
	Upgrade(context.Context, NodeUpgradeRequest) (NodeUpgradeResult, error)
}

type nodeUpgradeService struct {
	nodes     NodeStore
	workflows Store
}

func NewNodeUpgradeService(nodes NodeStore, workflows Store) NodeUpgradeService {
	if nodes == nil {
		panic("workflow: node upgrade service requires a node store")
	}
	if workflows == nil {
		panic("workflow: node upgrade service requires a workflow store")
	}
	return &nodeUpgradeService{nodes: nodes, workflows: workflows}
}

func (service *nodeUpgradeService) Upgrade(
	ctx context.Context,
	request NodeUpgradeRequest,
) (NodeUpgradeResult, error) {
	result := NodeUpgradeResult{NodeName: request.NodeName, Version: request.Version, Workflows: []NodeUpgradeWorkflowResult{}}
	if ctx == nil {
		return result, fmt.Errorf("workflow: node upgrade context is required")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := validateSafeIdentifier(request.NodeName, "node name"); err != nil {
		return result, err
	}
	if request.Version <= 0 {
		return result, fmt.Errorf("workflow: node upgrade requires a positive exact successor version")
	}
	if strings.TrimSpace(request.CreatedBy) == "" {
		return result, fmt.Errorf("workflow: node upgrade creator is required")
	}

	selected, err := validateNodeUpgradeSelections(request.Workflows)
	if err != nil {
		return result, err
	}
	successor, found, err := service.nodes.Released(request.NodeName, request.Version)
	if err != nil {
		return result, fmt.Errorf("workflow: load released successor %s@%d: %w", request.NodeName, request.Version, err)
	}
	if !found {
		return result, fmt.Errorf("workflow: released successor %s@%d not found", request.NodeName, request.Version)
	}
	if successor.Name != request.NodeName || successor.Version != request.Version {
		return result, fmt.Errorf(
			"workflow: node store returned %s@%d for released successor %s@%d",
			successor.Name,
			successor.Version,
			request.NodeName,
			request.Version,
		)
	}
	if successor.Release.ReleasedAt == 0 {
		return result, fmt.Errorf("workflow: node successor %s@%d is not released", successor.Name, successor.Version)
	}
	if successor.Release.Compatibility != ReleaseCompatible && successor.Release.Compatibility != ReleaseBreaking {
		return result, fmt.Errorf(
			"workflow: released successor %s@%d has invalid compatibility %q",
			successor.Name,
			successor.Version,
			successor.Release.Compatibility,
		)
	}
	if successor.Release.PredecessorVersion <= 0 {
		return failedNodeUpgradeResults(
			result,
			selected,
			fmt.Sprintf("workflow: released successor %s@%d has no released predecessor", successor.Name, successor.Version),
		), nil
	}

	predecessor, found, err := service.nodes.Released(successor.Name, successor.Release.PredecessorVersion)
	if err != nil {
		return result, fmt.Errorf(
			"workflow: load released predecessor %s@%d: %w",
			successor.Name,
			successor.Release.PredecessorVersion,
			err,
		)
	}
	if !found {
		return failedNodeUpgradeResults(
			result,
			selected,
			fmt.Sprintf(
				"workflow: released predecessor %s@%d for successor %s@%d was not found",
				successor.Name,
				successor.Release.PredecessorVersion,
				successor.Name,
				successor.Version,
			),
		), nil
	}
	if predecessor.Name != successor.Name || predecessor.Version != successor.Release.PredecessorVersion {
		return failedNodeUpgradeResults(
			result,
			selected,
			fmt.Sprintf(
				"workflow: persisted predecessor for %s@%d does not match release metadata",
				successor.Name,
				successor.Version,
			),
		), nil
	}
	if predecessor.Release.ReleasedAt == 0 {
		return failedNodeUpgradeResults(
			result,
			selected,
			fmt.Sprintf("workflow: predecessor %s@%d is not released", predecessor.Name, predecessor.Version),
		), nil
	}

	var obligations *NodeUpgradeObligations
	if successor.Release.Compatibility == ReleaseBreaking {
		diff := nodeUpgradeContractDiff(predecessor.Compiled, successor.Compiled)
		obligations = &diff
	}
	for _, workflowName := range selected {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		item := service.upgradeWorkflow(
			workflowName,
			request.CreatedBy,
			predecessor,
			successor,
			obligations,
		)
		result.Workflows = append(result.Workflows, item)
	}
	return result, nil
}

func validateNodeUpgradeSelections(workflows []string) ([]string, error) {
	if len(workflows) == 0 {
		return nil, fmt.Errorf("workflow: node upgrade requires at least one workflow selection")
	}
	selected := append([]string(nil), workflows...)
	sort.Strings(selected)
	for index, name := range selected {
		if strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("workflow: workflow selection is required")
		}
		if index > 0 && selected[index-1] == name {
			return nil, fmt.Errorf("workflow: duplicate workflow selection %q", name)
		}
	}
	return selected, nil
}

func failedNodeUpgradeResults(
	result NodeUpgradeResult,
	workflows []string,
	message string,
) NodeUpgradeResult {
	for _, name := range workflows {
		result.Workflows = append(result.Workflows, NodeUpgradeWorkflowResult{
			Workflow: name,
			Status:   NodeUpgradeFailed,
			Error:    message,
		})
	}
	return result
}

func (service *nodeUpgradeService) upgradeWorkflow(
	workflowName string,
	createdBy string,
	predecessor NodeDefinition,
	successor NodeDefinition,
	obligations *NodeUpgradeObligations,
) NodeUpgradeWorkflowResult {
	result := NodeUpgradeWorkflowResult{Workflow: workflowName}
	live, found, err := service.workflows.Live(workflowName)
	if err != nil {
		return failNodeUpgrade(result, fmt.Errorf("workflow: load live revision %q: %w", workflowName, err))
	}
	if !found {
		return failNodeUpgrade(result, fmt.Errorf("workflow: selected workflow %q has no live revision", workflowName))
	}
	result.OldVersion = live.Version
	if live.ID <= 0 {
		return failNodeUpgrade(result, fmt.Errorf("workflow: live revision %q@%d has no durable definition ID", workflowName, live.Version))
	}
	if len(live.SourceManifest) == 0 {
		return failNodeUpgrade(result, fmt.Errorf("workflow: live revision %q@%d has no immutable source manifest", workflowName, live.Version))
	}

	rewritten, references, err := rewriteNodeUpgradeManifest(
		live.SourceManifest,
		predecessor.Name,
		predecessor.Version,
		successor.Version,
	)
	if err != nil {
		return failNodeUpgrade(result, fmt.Errorf("workflow: inspect live revision %q@%d: %w", workflowName, live.Version, err))
	}
	liveBindings, err := service.nodes.Bindings(live.ID)
	if err != nil {
		return failNodeUpgrade(result, fmt.Errorf("workflow: load live node bindings for %q@%d: %w", workflowName, live.Version, err))
	}
	if len(references) == 0 {
		if hasBindingForNodeVersion(liveBindings, predecessor.Name, predecessor.Version) {
			return failNodeUpgrade(result, fmt.Errorf("workflow: live revision %q@%d has stale node bindings", workflowName, live.Version))
		}
		return failNodeUpgrade(result, fmt.Errorf(
			"workflow: live revision %q@%d does not reference predecessor %s@%d",
			workflowName,
			live.Version,
			predecessor.Name,
			predecessor.Version,
		))
	}
	if err := validateNodeUpgradeBindings(references, liveBindings, predecessor, false); err != nil {
		return failNodeUpgrade(result, fmt.Errorf("workflow: live revision %q@%d has stale node bindings: %w", workflowName, live.Version, err))
	}

	if obligations != nil {
		result.Status = NodeUpgradeRecompositionRequired
		result.Obligations = cloneNodeUpgradeObligations(obligations)
		return result
	}

	outcome, err := service.workflows.ImportManifestWithOutcome(workflowName, rewritten, createdBy)
	if err != nil {
		return failNodeUpgrade(result, fmt.Errorf("workflow: compile and import upgraded revision %q: %w", workflowName, err))
	}
	if outcome.Definition == nil {
		return failNodeUpgrade(result, fmt.Errorf("workflow: upgraded import %q returned no durable definition", workflowName))
	}
	imported := outcome.Definition
	result.NewVersion = imported.Version
	if imported.ID <= 0 || imported.Name != workflowName || imported.Version <= live.Version ||
		imported.ContentHash != rewritten.Hash() || imported.Live {
		return failNodeUpgrade(result, fmt.Errorf("workflow: upgraded import %q returned inconsistent durable identity", workflowName))
	}
	importedBindings, err := service.nodes.Bindings(imported.ID)
	if err != nil {
		return failNodeUpgrade(result, fmt.Errorf(
			"workflow: load upgraded node bindings for %q@%d: %w",
			workflowName,
			imported.Version,
			err,
		))
	}
	if err := validateNodeUpgradeBindings(references, importedBindings, successor, true); err != nil {
		return failNodeUpgrade(result, fmt.Errorf(
			"workflow: upgraded revision %q@%d did not bind exact successor content: %w",
			workflowName,
			imported.Version,
			err,
		))
	}
	if outcome.Inserted {
		result.Status = NodeUpgradeCreated
	} else {
		result.Status = NodeUpgradeUnchanged
	}
	return result
}

func failNodeUpgrade(result NodeUpgradeWorkflowResult, err error) NodeUpgradeWorkflowResult {
	result.Status = NodeUpgradeFailed
	result.Error = err.Error()
	result.Obligations = nil
	return result
}

func hasBindingForNodeVersion(bindings []ResolvedNodeBinding, nodeName string, version int) bool {
	for _, binding := range bindings {
		if binding.NodeName == nodeName && binding.NodeVersion == version {
			return true
		}
	}
	return false
}

func validateNodeUpgradeBindings(
	references map[string]NodeReference,
	bindings []ResolvedNodeBinding,
	node NodeDefinition,
	allowUnreferencedTargetBindings bool,
) error {
	byInstance := make(map[string]ResolvedNodeBinding, len(bindings))
	for _, binding := range bindings {
		if _, duplicate := byInstance[binding.InstanceName]; duplicate {
			return fmt.Errorf("duplicate durable binding for instance %q", binding.InstanceName)
		}
		byInstance[binding.InstanceName] = binding
		if binding.NodeName == node.Name && binding.NodeVersion == node.Version {
			if _, expected := references[binding.InstanceName]; !expected && !allowUnreferencedTargetBindings {
				return fmt.Errorf("unexpected binding for instance %q", binding.InstanceName)
			}
		}
	}
	for instance, reference := range references {
		binding, found := byInstance[instance]
		if !found {
			return fmt.Errorf("missing binding for instance %q", instance)
		}
		if binding.NodeDefinitionID != node.ID || binding.NodeName != node.Name ||
			binding.NodeVersion != node.Version || binding.NodeContentHash != node.ContentHash {
			return fmt.Errorf("binding for instance %q does not match %s@%d content", instance, node.Name, node.Version)
		}
		if !equalNodeUpgradeStringMaps(binding.InputMapping, reference.InputMapping) ||
			!equalNodeUpgradeStringMaps(binding.OutputMapping, reference.OutputMapping) ||
			!equalNodeUpgradeStringMaps(binding.Parameters, reference.Parameters) {
			return fmt.Errorf("binding for instance %q does not match authored mappings", instance)
		}
	}
	return nil
}

func equalNodeUpgradeStringMaps(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		candidate, found := right[key]
		if !found || candidate != value {
			return false
		}
	}
	return true
}

func rewriteNodeUpgradeManifest(
	source Manifest,
	nodeName string,
	predecessorVersion int,
	successorVersion int,
) (Manifest, map[string]NodeReference, error) {
	rewritten := make(Manifest, len(source))
	for path, content := range source {
		rewritten[path] = content
	}
	sourcePath := WorkflowFileName
	raw, found := source[sourcePath]
	if !found {
		sourcePath = LegacyWorkflowFileName
		raw, found = source[sourcePath]
	}
	if !found {
		return nil, nil, fmt.Errorf("manifest has no %s (or legacy %s)", WorkflowFileName, LegacyWorkflowFileName)
	}
	document, err := decodeNodeReferenceDocument([]byte(raw))
	if err != nil {
		return nil, nil, err
	}
	rewriter := nodeUpgradeReferenceRewriter{
		nodeName:           nodeName,
		predecessorVersion: predecessorVersion,
		successorVersion:   successorVersion,
		instances:          map[string]struct{}{},
		references:         map[string]NodeReference{},
	}
	if err := rewriter.rewriteWorkflow(document); err != nil {
		return nil, nil, err
	}
	encoded, err := yaml.Marshal(document)
	if err != nil {
		return nil, nil, fmt.Errorf("workflow: encode upgraded node references: %w", err)
	}
	rewritten[sourcePath] = string(encoded)
	if err := rewritten.Validate(); err != nil {
		return nil, nil, err
	}
	return rewritten, rewriter.references, nil
}

type nodeUpgradeReferenceRewriter struct {
	nodeName           string
	predecessorVersion int
	successorVersion   int
	instances          map[string]struct{}
	references         map[string]NodeReference
}

func (rewriter *nodeUpgradeReferenceRewriter) rewriteWorkflow(value any) error {
	root, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	plan, found := root["plan"]
	if !found {
		return nil
	}
	return rewriter.rewriteStepList(plan, "workflow.plan")
}

func (rewriter *nodeUpgradeReferenceRewriter) rewriteStepList(value any, path string) error {
	steps, ok := value.([]any)
	if !ok {
		return nil
	}
	for index, value := range steps {
		step, ok := value.(map[string]any)
		if !ok {
			continue
		}
		if err := rewriter.rewriteStep(step, fmt.Sprintf("%s[%d]", path, index)); err != nil {
			return err
		}
	}
	return nil
}

func (rewriter *nodeUpgradeReferenceRewriter) rewriteStep(step map[string]any, path string) error {
	_, hasNode := step["node"]
	_, hasUses := step["uses"]
	if hasNode || hasUses {
		referenceSource, _, err := splitNodeReferenceStep(step, path)
		if err != nil {
			return err
		}
		reference, err := decodeNodeReference(referenceSource)
		if err != nil {
			return fmt.Errorf("workflow: %s: %w", path, err)
		}
		if err := validateSafeIdentifier(reference.InstanceName, "node instance name"); err != nil {
			return err
		}
		if _, duplicate := rewriter.instances[reference.InstanceName]; duplicate {
			return fmt.Errorf("workflow: duplicate node instance %q", reference.InstanceName)
		}
		rewriter.instances[reference.InstanceName] = struct{}{}
		name, version, err := parseExactNodeUse(reference.Uses)
		if err != nil {
			return err
		}
		if name == rewriter.nodeName && version == rewriter.predecessorVersion {
			rewriter.references[reference.InstanceName] = reference
			step["uses"] = fmt.Sprintf("%s@%d", name, rewriter.successorVersion)
		}
	}

	for _, field := range []string{"do", "try", "timeout", "across", "on_success", "on_failure", "on_abort", "on_error", "ensure"} {
		child, found := step[field]
		if !found {
			continue
		}
		if field == "do" {
			if err := rewriter.rewriteStepList(child, path+"."+field); err != nil {
				return err
			}
			continue
		}
		if childStep, ok := child.(map[string]any); ok {
			if err := rewriter.rewriteStep(childStep, path+"."+field); err != nil {
				return err
			}
		}
	}
	if parallel, found := step["in_parallel"]; found {
		switch config := parallel.(type) {
		case []any:
			if err := rewriter.rewriteStepList(config, path+".in_parallel"); err != nil {
				return err
			}
		case map[string]any:
			if err := rewriter.rewriteStepList(config["steps"], path+".in_parallel.steps"); err != nil {
				return err
			}
		}
	}
	return nil
}

func nodeUpgradeContractDiff(
	predecessor CompiledNodeDefinition,
	successor CompiledNodeDefinition,
) NodeUpgradeObligations {
	inputsBefore := map[string]nodeUpgradePortContract{}
	inputsAfter := map[string]nodeUpgradePortContract{}
	for _, port := range predecessor.Function.Inputs {
		inputsBefore[port.Name] = nodeUpgradePortContract{Type: string(port.Type), Optional: port.Optional}
	}
	for _, port := range successor.Function.Inputs {
		inputsAfter[port.Name] = nodeUpgradePortContract{Type: string(port.Type), Optional: port.Optional}
	}
	outputsBefore := map[string]nodeUpgradePortContract{}
	outputsAfter := map[string]nodeUpgradePortContract{}
	for _, output := range predecessor.Function.Outputs {
		outputsBefore[output.Name] = nodeUpgradePortContract{Type: string(output.Type), Optional: output.Optional}
	}
	for _, output := range successor.Function.Outputs {
		outputsAfter[output.Name] = nodeUpgradePortContract{Type: string(output.Type), Optional: output.Optional}
	}
	parametersBefore := map[string]nodeUpgradeParameterContract{}
	parametersAfter := map[string]nodeUpgradeParameterContract{}
	for _, parameter := range predecessor.Parameters {
		parametersBefore[parameter.Name] = newNodeUpgradeParameterContract(parameter.Default)
	}
	for _, parameter := range successor.Parameters {
		parametersAfter[parameter.Name] = newNodeUpgradeParameterContract(parameter.Default)
	}
	return NodeUpgradeObligations{
		Inputs:     diffNodeUpgradeContracts(inputsBefore, inputsAfter),
		Outputs:    diffNodeUpgradeContracts(outputsBefore, outputsAfter),
		Parameters: diffNodeUpgradeContracts(parametersBefore, parametersAfter),
	}
}

type nodeUpgradePortContract struct {
	Type     string
	Optional bool
}

type nodeUpgradeParameterContract struct {
	Required bool
	Default  string
}

func newNodeUpgradeParameterContract(value *string) nodeUpgradeParameterContract {
	if value == nil {
		return nodeUpgradeParameterContract{Required: true}
	}
	return nodeUpgradeParameterContract{Default: *value}
}

func diffNodeUpgradeContracts[T comparable](before, after map[string]T) NodeContractChanges {
	changes := NodeContractChanges{Added: []string{}, Removed: []string{}, Changed: []string{}}
	for name, previous := range before {
		candidate, found := after[name]
		if !found {
			changes.Removed = append(changes.Removed, name)
		} else if candidate != previous {
			changes.Changed = append(changes.Changed, name)
		}
	}
	for name := range after {
		if _, found := before[name]; !found {
			changes.Added = append(changes.Added, name)
		}
	}
	sort.Strings(changes.Added)
	sort.Strings(changes.Removed)
	sort.Strings(changes.Changed)
	return changes
}

func cloneNodeUpgradeObligations(source *NodeUpgradeObligations) *NodeUpgradeObligations {
	if source == nil {
		return nil
	}
	return &NodeUpgradeObligations{
		Inputs:     cloneNodeContractChanges(source.Inputs),
		Outputs:    cloneNodeContractChanges(source.Outputs),
		Parameters: cloneNodeContractChanges(source.Parameters),
	}
}

func cloneNodeContractChanges(source NodeContractChanges) NodeContractChanges {
	return NodeContractChanges{
		Added:   append([]string(nil), source.Added...),
		Removed: append([]string(nil), source.Removed...),
		Changed: append([]string(nil), source.Changed...),
	}
}
