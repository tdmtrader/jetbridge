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
	Schema   string                `json:"schema"`
	Selected []string              `json:"selected"`
	Mode     string                `json:"mode"`
	Ref      *snapshot.SnapshotRef `json:"ref,omitempty"`
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

	return CheckpointCaptureProvenance{
		Identity:         identity,
		ExecutionAttempt: request.ExecutionAttempt,
		ContainerHandle:  request.ContainerHandle,
		Provider:         request.Provider,
		RuntimeImage:     request.RuntimeImage,
		Model:            request.Plan.Model,
		ConfigDigest:     configDigest,
		InputDigest:      inputDigest,
		MCPDigest:        mcpDigest,
		SkillDigest:      skillDigest,
		// The current production legacy adapter cannot authenticate live
		// session/effect evidence to ATC. Empty values explicitly describe a
		// workspace-only capture; Task 17 decides recovery policy.
		SessionID:            "",
		TranscriptCursor:     0,
		CompletedToolCallIDs: nil,
		Effects:              nil,
	}, nil
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
		return identity, nil
	}

	hasSkillsInput := false
	for _, name := range plan.Inputs {
		if name == "skills" {
			hasSkillsInput = true
			break
		}
	}
	if !hasSkillsInput {
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
