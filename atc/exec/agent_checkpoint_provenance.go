package exec

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/concourse/concourse/agent/checkpoint"
	"github.com/concourse/concourse/agent/hangar"
	"github.com/concourse/concourse/agent/provider"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
)

const agentCheckpointProvenanceSchema = "concourse-agent-checkpoint-provenance/v1"

// agentCheckpointProvenanceRequest contains only ATC-owned execution facts.
// Runtime pod identity is deliberately absent: the authenticated Jetbridge
// capture lease supplies that independently when capture begins.
type agentCheckpointProvenanceRequest struct {
	PlanID           atc.PlanID
	Plan             atc.AgentPlan
	Metadata         StepMetadata
	RuntimeImage     string
	Provider         string
	Adapter          provider.Identity
	ExecutionAttempt int
	ContainerHandle  string
	Inputs           map[string]snapshot.SnapshotRef
	Sidecars         []atc.SidecarConfig
}

// agentCheckpointImmutableProvenanceRequest omits all fresh allocation facts.
// It is the admission-time input to recovery policy.
type agentCheckpointImmutableProvenanceRequest struct {
	PlanID       atc.PlanID
	Plan         atc.AgentPlan
	Metadata     StepMetadata
	RuntimeImage string
	Provider     string
	Adapter      provider.Identity
	Inputs       map[string]snapshot.SnapshotRef
	Sidecars     []atc.SidecarConfig
}

type agentCheckpointConfigIdentity struct {
	Schema   string
	Provider string
	Adapter  provider.Identity
	Plan     atc.AgentPlan
}

type agentCheckpointInputIdentity struct {
	Name string                `json:"name"`
	Mode string                `json:"mode"`
	Ref  *snapshot.SnapshotRef `json:"ref,omitempty"`
}

type agentCheckpointSkillIdentity struct {
	Schema   string                             `json:"schema"`
	Selected []string                           `json:"selected"`
	Mode     string                             `json:"mode"`
	Ref      *snapshot.SnapshotRef              `json:"ref,omitempty"`
	Files    []agentCheckpointSkillFileIdentity `json:"files,omitempty"`
}

type agentCheckpointSkillFileIdentity struct {
	Path     string `json:"path"`
	Contents string `json:"contents"`
}

// AgentCheckpointImmutableProvenance is the admitted, server-owned portion of
// checkpoint provenance. Recovery compares it verbatim with the frozen source
// manifest; an attempt or a newly allocated container cannot change it.
type AgentCheckpointImmutableProvenance struct {
	Identity     checkpoint.Identity
	Provider     string
	Adapter      provider.Identity
	RuntimeImage string
	Model        string
	ConfigDigest string
	InputDigest  string
	MCPDigest    string
	SkillDigest  string
}

func deriveAgentCheckpointImmutableProvenance(request agentCheckpointImmutableProvenanceRequest) (AgentCheckpointImmutableProvenance, error) {
	// Existing derivation is retained as the single canonical digest algorithm;
	// these inert binding values are discarded before the immutable value is
	// returned and do not represent a real allocation.
	capture, err := deriveAgentCheckpointProvenance(agentCheckpointProvenanceRequest{
		PlanID: request.PlanID, Plan: request.Plan, Metadata: request.Metadata,
		RuntimeImage: request.RuntimeImage, Provider: request.Provider, Adapter: request.Adapter,
		ExecutionAttempt: 1, ContainerHandle: "immutable-provenance", Inputs: request.Inputs, Sidecars: request.Sidecars,
	})
	if err != nil {
		return AgentCheckpointImmutableProvenance{}, err
	}
	return AgentCheckpointImmutableProvenance{
		Identity: capture.Identity.Clone(), Provider: capture.Provider, Adapter: request.Adapter,
		RuntimeImage: capture.RuntimeImage, Model: capture.Model, ConfigDigest: capture.ConfigDigest,
		InputDigest: capture.InputDigest, MCPDigest: capture.MCPDigest, SkillDigest: capture.SkillDigest,
	}, nil
}

func (provenance AgentCheckpointImmutableProvenance) Validate() error {
	if err := provenance.Identity.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(provenance.Provider) == "" || strings.TrimSpace(provenance.Provider) != provenance.Provider {
		return errors.New("agent checkpoint provenance requires a canonical provider")
	}
	if err := provenance.Adapter.Validate(); err != nil {
		return err
	}
	if err := atc.ValidatePinnedOCIImage(provenance.RuntimeImage); err != nil {
		return fmt.Errorf("agent checkpoint runtime image is not immutable: %w", err)
	}
	if strings.TrimSpace(provenance.Model) == "" || strings.TrimSpace(provenance.Model) != provenance.Model {
		return errors.New("agent checkpoint provenance requires a canonical model pin")
	}
	for name, digest := range map[string]string{
		"config": provenance.ConfigDigest, "input": provenance.InputDigest,
		"MCP": provenance.MCPDigest, "skill": provenance.SkillDigest,
	} {
		if err := hangar.Digest(digest).Validate(); err != nil {
			return fmt.Errorf("agent checkpoint %s digest: %w", name, err)
		}
	}
	return nil
}

// BindCheckpointCaptureProvenance adds only per-attempt allocation facts to
// immutable admitted provenance. Callers cannot substitute recovery identity
// data through capture plumbing.
func (provenance AgentCheckpointImmutableProvenance) BindCheckpointCaptureProvenance(executionAttempt int, containerHandle string) (CheckpointCaptureProvenance, error) {
	if err := provenance.Validate(); err != nil {
		return CheckpointCaptureProvenance{}, err
	}
	if executionAttempt <= 0 {
		return CheckpointCaptureProvenance{}, errors.New("agent checkpoint execution attempt must be positive")
	}
	if strings.TrimSpace(containerHandle) == "" || strings.TrimSpace(containerHandle) != containerHandle {
		return CheckpointCaptureProvenance{}, errors.New("agent checkpoint container handle is required")
	}
	return CheckpointCaptureProvenance{
		Identity: provenance.Identity.Clone(), ExecutionAttempt: executionAttempt, ContainerHandle: containerHandle,
		Provider: provenance.Provider, RuntimeImage: provenance.RuntimeImage, Model: provenance.Model,
		ConfigDigest: provenance.ConfigDigest, InputDigest: provenance.InputDigest,
		MCPDigest: provenance.MCPDigest, SkillDigest: provenance.SkillDigest,
	}, nil
}

func deriveAgentCheckpointProvenance(request agentCheckpointProvenanceRequest) (CheckpointCaptureProvenance, error) {
	if !request.Plan.Hermetic {
		return CheckpointCaptureProvenance{}, errors.New("agent checkpoint capture requires a hermetic plan")
	}
	if request.Metadata.WorkflowRunID == nil {
		return CheckpointCaptureProvenance{}, errors.New("agent checkpoint capture requires an authenticated workflow run")
	}
	if err := atc.ValidatePinnedOCIImage(request.RuntimeImage); err != nil {
		return CheckpointCaptureProvenance{}, fmt.Errorf("agent checkpoint runtime image is not immutable: %w", err)
	}
	if request.Plan.RuntimeImage == "" || request.Plan.RuntimeImage != request.RuntimeImage {
		return CheckpointCaptureProvenance{}, errors.New("agent checkpoint runtime image differs from the admitted plan")
	}
	if strings.TrimSpace(request.Plan.Model) == "" || strings.TrimSpace(request.Plan.Model) != request.Plan.Model {
		return CheckpointCaptureProvenance{}, errors.New("agent checkpoint capture requires a canonical model pin")
	}
	if strings.TrimSpace(request.Provider) == "" || strings.TrimSpace(request.Provider) != request.Provider {
		return CheckpointCaptureProvenance{}, errors.New("agent checkpoint capture requires a provider identity")
	}
	if err := request.Adapter.Validate(); err != nil {
		return CheckpointCaptureProvenance{}, err
	}
	if request.ExecutionAttempt <= 0 {
		return CheckpointCaptureProvenance{}, errors.New("agent checkpoint execution attempt must be positive")
	}
	if strings.TrimSpace(request.ContainerHandle) == "" || strings.TrimSpace(request.ContainerHandle) != request.ContainerHandle {
		return CheckpointCaptureProvenance{}, errors.New("agent checkpoint container handle is required")
	}

	identity := checkpointIdentityForAgent(request)
	if err := identity.Validate(); err != nil {
		return CheckpointCaptureProvenance{}, err
	}

	config := request.Plan
	config.RuntimeImage = request.RuntimeImage
	config.Inputs = sortedCheckpointStrings(config.Inputs)
	config.Outputs = sortedCheckpointStrings(config.Outputs)
	// These have their own digest domains so a change has one clear cause.
	config.Sidecars = nil
	config.Skills = nil
	config.SkillFiles = nil
	configDigest, err := digestAgentCheckpointIdentity(
		"config",
		agentCheckpointConfigIdentity{
			Schema:   agentCheckpointProvenanceSchema,
			Provider: request.Provider,
			Adapter:  request.Adapter,
			Plan:     config,
		},
	)
	if err != nil {
		return CheckpointCaptureProvenance{}, err
	}

	inputRows, err := checkpointInputIdentities(request.Plan, request.Inputs)
	if err != nil {
		return CheckpointCaptureProvenance{}, err
	}
	inputDigest, err := digestAgentCheckpointIdentity("inputs", inputRows)
	if err != nil {
		return CheckpointCaptureProvenance{}, err
	}

	sidecars := cloneCheckpointSidecars(request.Sidecars)
	sort.Slice(sidecars, func(left, right int) bool {
		return sidecars[left].Name < sidecars[right].Name
	})
	seenSidecars := make(map[string]struct{}, len(sidecars))
	for _, sidecar := range sidecars {
		if strings.TrimSpace(sidecar.Name) == "" || strings.TrimSpace(sidecar.Name) != sidecar.Name {
			return CheckpointCaptureProvenance{}, errors.New("agent checkpoint sidecar name is not canonical")
		}
		if _, duplicate := seenSidecars[sidecar.Name]; duplicate {
			return CheckpointCaptureProvenance{}, fmt.Errorf("agent checkpoint sidecar %q is duplicated", sidecar.Name)
		}
		seenSidecars[sidecar.Name] = struct{}{}
		if err := atc.ValidatePinnedOCIImage(sidecar.Image); err != nil {
			return CheckpointCaptureProvenance{}, fmt.Errorf("agent checkpoint sidecar %q image is not immutable: %w", sidecar.Name, err)
		}
		if sidecar.ImageArtifact != "" {
			return CheckpointCaptureProvenance{}, fmt.Errorf("agent checkpoint sidecar %q retains an unresolved image artifact", sidecar.Name)
		}
	}
	mcpDigest, err := digestAgentCheckpointIdentity("mcp", sidecars)
	if err != nil {
		return CheckpointCaptureProvenance{}, err
	}

	skillIdentity, err := checkpointSkillIdentity(request.Plan, request.Inputs)
	if err != nil {
		return CheckpointCaptureProvenance{}, err
	}
	skillDigest, err := digestAgentCheckpointIdentity("skills", skillIdentity)
	if err != nil {
		return CheckpointCaptureProvenance{}, err
	}

	provenance, err := (AgentCheckpointImmutableProvenance{
		Identity: identity, Provider: request.Provider, Adapter: request.Adapter,
		RuntimeImage: request.RuntimeImage, Model: request.Plan.Model,
		ConfigDigest: configDigest, InputDigest: inputDigest, MCPDigest: mcpDigest, SkillDigest: skillDigest,
	}).BindCheckpointCaptureProvenance(request.ExecutionAttempt, request.ContainerHandle)
	if err != nil {
		return CheckpointCaptureProvenance{}, err
	}
	provenance = CheckpointCaptureProvenance{
		Identity: provenance.Identity, ExecutionAttempt: provenance.ExecutionAttempt, ContainerHandle: provenance.ContainerHandle,
		Provider: provenance.Provider, RuntimeImage: provenance.RuntimeImage, Model: provenance.Model,
		ConfigDigest: provenance.ConfigDigest, InputDigest: provenance.InputDigest, MCPDigest: provenance.MCPDigest, SkillDigest: provenance.SkillDigest,
		// The current production legacy adapter cannot authenticate live
		// session/effect evidence to ATC. Empty values explicitly describe a
		// workspace-only capture; Task 17 decides recovery policy.
		SessionID:            "",
		TranscriptCursor:     0,
		CompletedToolCallIDs: nil,
		Effects:              nil,
	}
	return provenance, nil
}

func checkpointIdentityForAgent(request agentCheckpointProvenanceRequest) checkpoint.Identity {
	return checkpoint.Identity{
		WorkflowRunID: request.Metadata.WorkflowRunID,
		BuildID:       int64(request.Metadata.BuildID),
		PlanID:        string(request.PlanID),
		FunctionID:    request.Plan.FunctionID,
	}
}

func checkpointInputIdentities(plan atc.AgentPlan, refs map[string]snapshot.SnapshotRef) ([]agentCheckpointInputIdentity, error) {
	declared := make(map[string]struct{}, len(plan.Inputs))
	names := append([]string(nil), plan.Inputs...)
	sort.Strings(names)
	rows := make([]agentCheckpointInputIdentity, 0, len(names))
	for _, name := range names {
		if strings.TrimSpace(name) == "" || strings.TrimSpace(name) != name {
			return nil, errors.New("agent checkpoint input name is not canonical")
		}
		if _, duplicate := declared[name]; duplicate {
			return nil, fmt.Errorf("agent checkpoint input %q is duplicated", name)
		}
		declared[name] = struct{}{}
		declaration, typed := plan.SnapshotInputs[name]
		ref, found := refs[name]
		switch {
		case typed && found:
			if err := ref.Validate(); err != nil {
				return nil, fmt.Errorf("agent checkpoint input %q: %w", name, err)
			}
			if ref.Type != declaration.Type {
				return nil, fmt.Errorf("agent checkpoint input %q type %q differs from declaration %q", name, ref.Type, declaration.Type)
			}
			refCopy := ref
			rows = append(rows, agentCheckpointInputIdentity{Name: name, Mode: "snapshot", Ref: &refCopy})
		case typed && declaration.Optional:
			rows = append(rows, agentCheckpointInputIdentity{Name: name, Mode: "optional-absent"})
		case typed:
			return nil, fmt.Errorf("agent checkpoint typed input %q has no immutable snapshot ref", name)
		case found:
			return nil, fmt.Errorf("agent checkpoint untyped input %q unexpectedly has a snapshot ref", name)
		default:
			rows = append(rows, agentCheckpointInputIdentity{Name: name, Mode: "archive-only"})
		}
	}
	for name := range refs {
		if _, found := declared[name]; !found {
			return nil, fmt.Errorf("agent checkpoint snapshot ref %q is not a declared input", name)
		}
	}
	return rows, nil
}

func checkpointSkillIdentity(plan atc.AgentPlan, refs map[string]snapshot.SnapshotRef) (agentCheckpointSkillIdentity, error) {
	selected := sortedCheckpointStrings(plan.Skills)
	identity := agentCheckpointSkillIdentity{
		Schema:   agentCheckpointProvenanceSchema,
		Selected: selected,
		Mode:     "none",
	}
	for index, name := range selected {
		if strings.TrimSpace(name) == "" || strings.TrimSpace(name) != name {
			return agentCheckpointSkillIdentity{}, errors.New("agent checkpoint skill name is not canonical")
		}
		if index > 0 && selected[index-1] == name {
			return agentCheckpointSkillIdentity{}, fmt.Errorf("agent checkpoint skill %q is duplicated", name)
		}
	}
	if len(selected) == 0 {
		if len(plan.SkillFiles) > 0 {
			return agentCheckpointSkillIdentity{}, errors.New("agent checkpoint compiled skill files have no selected skills")
		}
		return identity, nil
	}
	if len(plan.SkillFiles) > 0 {
		if hasAgentPlanInput(plan, "skills") {
			return agentCheckpointSkillIdentity{}, errors.New("agent checkpoint compiled skills collide with a declared skills input")
		}
		allowed := map[string]struct{}{}
		for _, name := range selected {
			allowed[name] = struct{}{}
		}
		files := make([]agentCheckpointSkillFileIdentity, 0, len(plan.SkillFiles))
		for file, contents := range plan.SkillFiles {
			name, err := checkpointSkillFileName(file)
			if err != nil {
				return agentCheckpointSkillIdentity{}, err
			}
			if _, selected := allowed[name]; !selected {
				return agentCheckpointSkillIdentity{}, fmt.Errorf("agent checkpoint compiled skill file %q is not selected", file)
			}
			files = append(files, agentCheckpointSkillFileIdentity{Path: file, Contents: contents})
		}
		sort.Slice(files, func(left, right int) bool { return files[left].Path < files[right].Path })
		for _, name := range selected {
			root := "skills/" + name + "/SKILL.md"
			found := false
			for _, file := range files {
				if file.Path == root {
					found = true
					break
				}
			}
			if !found {
				return agentCheckpointSkillIdentity{}, fmt.Errorf("agent checkpoint selected skill %q is missing %s", name, root)
			}
		}
		identity.Mode = "compiled"
		identity.Files = files
		return identity, nil
	}

	if !hasAgentPlanInput(plan, "skills") {
		return agentCheckpointSkillIdentity{}, errors.New("agent checkpoint selected skills have no declared skills input")
	}
	if ref, found := refs["skills"]; found {
		if err := ref.Validate(); err != nil {
			return agentCheckpointSkillIdentity{}, fmt.Errorf("agent checkpoint skills input: %w", err)
		}
		if declaration, typed := plan.SnapshotInputs["skills"]; !typed || ref.Type != declaration.Type {
			return agentCheckpointSkillIdentity{}, errors.New("agent checkpoint skills snapshot does not match its typed declaration")
		}
		refCopy := ref
		identity.Mode = "snapshot"
		identity.Ref = &refCopy
		return identity, nil
	}
	if _, typed := plan.SnapshotInputs["skills"]; typed {
		return agentCheckpointSkillIdentity{}, errors.New("agent checkpoint selected skills have no immutable snapshot ref")
	}
	identity.Mode = "archive-only"
	return identity, nil
}

func hasAgentPlanInput(plan atc.AgentPlan, wanted string) bool {
	for _, name := range plan.Inputs {
		if name == wanted {
			return true
		}
	}
	return false
}

func checkpointSkillFileName(file string) (string, error) {
	if strings.Contains(file, `\`) || strings.HasPrefix(file, "/") {
		return "", fmt.Errorf("agent checkpoint compiled skill file %q is unsafe", file)
	}
	parts := strings.Split(file, "/")
	if len(parts) < 3 || parts[0] != "skills" || parts[1] == "" {
		return "", fmt.Errorf("agent checkpoint compiled skill file %q must be below skills/<name>", file)
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.HasPrefix(part, ".") {
			return "", fmt.Errorf("agent checkpoint compiled skill file %q is unsafe", file)
		}
	}
	return parts[1], nil
}

func digestAgentCheckpointIdentity(domain string, value any) (string, error) {
	raw, err := json.Marshal(struct {
		Schema string `json:"schema"`
		Domain string `json:"domain"`
		Value  any    `json:"value"`
	}{
		Schema: agentCheckpointProvenanceSchema,
		Domain: domain,
		Value:  value,
	})
	if err != nil {
		return "", fmt.Errorf("marshal immutable agent checkpoint %s identity: %w", domain, err)
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func sortedCheckpointStrings(values []string) []string {
	cloned := append([]string(nil), values...)
	sort.Strings(cloned)
	return cloned
}

func cloneCheckpointSidecars(values []atc.SidecarConfig) []atc.SidecarConfig {
	cloned := make([]atc.SidecarConfig, len(values))
	for index, sidecar := range values {
		cloned[index] = sidecar
		cloned[index].Command = append([]string(nil), sidecar.Command...)
		cloned[index].Args = append([]string(nil), sidecar.Args...)
		cloned[index].Env = append([]atc.SidecarEnvVar(nil), sidecar.Env...)
		cloned[index].Ports = append([]atc.SidecarPort(nil), sidecar.Ports...)
		if sidecar.Resources != nil {
			resources := *sidecar.Resources
			cloned[index].Resources = &resources
		}
	}
	return cloned
}
