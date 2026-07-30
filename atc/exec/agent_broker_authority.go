package exec

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/concourse/concourse/agent/broker"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
)

// AgentBrokerAuthorityRequest is the complete trusted execution occurrence
// given to the command-scoped broker factory. The factory may mint a bootstrap
// capability, but never returns or projects its root signing key.
type AgentBrokerAuthorityRequest struct {
	TeamID               int
	TeamName             string
	BuildID              int
	SnapshotCreatedBy    string
	WorkflowDefinitionID int
	WorkflowRunID        int64
	NodePlanID           string
	ParentAttempt        int
	BrokerInstance       string
	Profiles             []broker.Profile
	ScopeInputs          map[string]snapshot.SnapshotRef
	InputPaths           map[string]string
	WorkspaceInputPath   string
}

type AgentBrokerAuthorityFactory interface {
	BuildAgentBroker(AgentBrokerAuthorityRequest) (*runtime.ManagedAgentBroker, error)
}

type AgentBrokerAuthorityFactoryFunc func(AgentBrokerAuthorityRequest) (*runtime.ManagedAgentBroker, error)

func (f AgentBrokerAuthorityFactoryFunc) BuildAgentBroker(request AgentBrokerAuthorityRequest) (*runtime.ManagedAgentBroker, error) {
	return f(request)
}

func agentBrokerAuthorityRequest(
	planID atc.PlanID,
	plan atc.AgentPlan,
	metadata StepMetadata,
	container db.ContainerMetadata,
	inputs snapshotInputBindings,
	workRoot string,
) (AgentBrokerAuthorityRequest, bool, error) {
	if len(plan.BrokerAuthority) == 0 {
		return AgentBrokerAuthorityRequest{}, false, nil
	}
	profiles, review, err := decodeAgentBrokerProfiles(plan)
	if err != nil {
		return AgentBrokerAuthorityRequest{}, false, err
	}
	if !plan.Hermetic || metadata.TeamID <= 0 || metadata.BuildID <= 0 ||
		strings.TrimSpace(metadata.TeamName) == "" || strings.TrimSpace(metadata.SnapshotCreatedBy) == "" ||
		metadata.WorkflowDefinitionID == nil || *metadata.WorkflowDefinitionID <= 0 ||
		metadata.WorkflowRunID == nil || *metadata.WorkflowRunID <= 0 || strings.TrimSpace(string(planID)) == "" {
		return AgentBrokerAuthorityRequest{}, false, fmt.Errorf("agent broker requires complete server-derived workflow identity")
	}
	parentAttempt, err := strconv.Atoi(container.Attempt)
	if err != nil || parentAttempt <= 0 || strconv.Itoa(parentAttempt) != container.Attempt {
		return AgentBrokerAuthorityRequest{}, false, fmt.Errorf("agent broker requires one positive parent attempt")
	}
	scopeInputs := make(map[string]snapshot.SnapshotRef, len(inputs.refs))
	inputPaths := make(map[string]string, len(inputs.refs))
	for name, ref := range inputs.refs {
		declaration, declared := plan.SnapshotInputs[name]
		exposure, exposed := inputs.exposures[name]
		if !declared || ref.Validate() != nil || declaration.Validate() != nil || ref.Type != declaration.Type ||
			!exposed || exposure.Validate() != nil || exposure.TreeDigest != ref.Digest {
			return AgentBrokerAuthorityRequest{}, false, fmt.Errorf("agent broker input %q is not exact typed authority", name)
		}
		scopeInputs[name] = ref
		inputPaths[name] = exposure.MountPath
	}
	workspacePath := ""
	if review {
		declaration, declared := plan.SnapshotInputs["workspace"]
		ref, bound := inputs.refs["workspace"]
		exposure, exposed := inputs.exposures["workspace"]
		expectedPath := filepath.Join(workRoot, "workspace")
		if !declared || declaration.Optional || declaration.Type != "repository/v1" || !bound ||
			ref.Type != declaration.Type || ref.Validate() != nil || !exposed || exposure.Validate() != nil ||
			exposure.MountPath != expectedPath || exposure.TreeDigest != ref.Digest {
			return AgentBrokerAuthorityRequest{}, false, fmt.Errorf("agent broker request_review requires exact repository/v1 workspace")
		}
		workspacePath = expectedPath
	}
	instanceHash := sha256.Sum256([]byte("agent-broker-instance/v1\x00" +
		strconv.Itoa(metadata.TeamID) + "\x00" + strconv.Itoa(metadata.BuildID) + "\x00" +
		strconv.FormatInt(int64(*metadata.WorkflowRunID), 10) + "\x00" + string(planID) + "\x00" +
		strconv.Itoa(parentAttempt)))
	return AgentBrokerAuthorityRequest{
		TeamID: metadata.TeamID, TeamName: metadata.TeamName, BuildID: metadata.BuildID,
		SnapshotCreatedBy: metadata.SnapshotCreatedBy, WorkflowDefinitionID: *metadata.WorkflowDefinitionID,
		WorkflowRunID: int64(*metadata.WorkflowRunID), NodePlanID: string(planID), ParentAttempt: parentAttempt,
		BrokerInstance: "broker-" + hex.EncodeToString(instanceHash[:16]), Profiles: profiles,
		ScopeInputs: scopeInputs, InputPaths: inputPaths, WorkspaceInputPath: workspacePath,
	}, true, nil
}

func decodeAgentBrokerProfiles(plan atc.AgentPlan) ([]broker.Profile, bool, error) {
	if strings.TrimSpace(plan.FunctionID) == "" {
		return nil, false, fmt.Errorf("agent broker authority requires function identity")
	}
	result := make([]broker.Profile, 0, len(plan.BrokerAuthority))
	image := ""
	review := false
	seen := map[string]struct{}{}
	seenProfiles := map[string]struct{}{}
	for index, outer := range plan.BrokerAuthority {
		decoder := json.NewDecoder(bytes.NewReader(outer.Profile))
		decoder.DisallowUnknownFields()
		var profile broker.Profile
		if err := decoder.Decode(&profile); err != nil {
			return nil, false, fmt.Errorf("agent broker profile %d: %w", index, err)
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			return nil, false, fmt.Errorf("agent broker profile %d has trailing JSON", index)
		}
		canonical, err := json.Marshal(profile)
		if err != nil || !bytes.Equal(canonical, outer.Profile) || broker.ValidateResolvedProfile(profile) != nil {
			return nil, false, fmt.Errorf("agent broker profile %d is not canonical resolved authority", index)
		}
		if outer.FunctionID != plan.FunctionID || outer.ProfileID != profile.ID ||
			outer.ProfileRevision != profile.Revision || outer.ProfileDigest != profile.Digest ||
			outer.WorkerImage != profile.WorkerImage || outer.Tier != string(profile.Selector.Tier) ||
			outer.Effort != string(profile.Selector.Effort) {
			return nil, false, fmt.Errorf("agent broker profile %d outer authority mismatch", index)
		}
		toolFound := false
		for _, tool := range profile.Tools {
			if string(tool) == outer.Tool {
				toolFound = true
				if tool == broker.ToolRequestReview {
					review = true
				}
			}
		}
		if !toolFound {
			return nil, false, fmt.Errorf("agent broker profile %d tool mismatch", index)
		}
		key := outer.Tool + "\x00" + outer.Tier + "\x00" + outer.Effort
		if _, duplicate := seen[key]; duplicate {
			return nil, false, fmt.Errorf("agent broker profile selector is duplicated")
		}
		seen[key] = struct{}{}
		if image == "" {
			image = profile.WorkerImage
		} else if image != profile.WorkerImage {
			return nil, false, fmt.Errorf("agent broker profiles must share one worker image")
		}
		profileKey := profile.ID + "\x00" + profile.Digest
		if _, found := seenProfiles[profileKey]; !found {
			result = append(result, profile)
			seenProfiles[profileKey] = struct{}{}
		}
	}
	return result, review, nil
}

func reserveAgentBroker(sidecars []atc.SidecarConfig, env []string) error {
	for _, row := range env {
		name, value, _ := strings.Cut(row, "=")
		if name == runtime.ManagedAgentBrokerMarkerEnv || value == runtime.ManagedAgentBrokerMCPURL {
			return fmt.Errorf("managed agent broker configuration is reserved by the platform")
		}
	}
	for _, sidecar := range sidecars {
		if sidecar.Name == runtime.ManagedAgentBrokerName {
			return fmt.Errorf("managed agent broker sidecar name is reserved")
		}
		for _, port := range sidecar.Ports {
			if port.ContainerPort == runtime.ManagedAgentBrokerPort {
				return fmt.Errorf("managed agent broker port is reserved")
			}
		}
		for _, row := range sidecar.Env {
			if row.Name == runtime.ManagedAgentBrokerMarkerEnv || row.Value == runtime.ManagedAgentBrokerMCPURL {
				return fmt.Errorf("managed agent broker endpoint is reserved")
			}
		}
	}
	return nil
}

func injectManagedAgentBroker(spec *runtime.ContainerSpec, managed *runtime.ManagedAgentBroker, profiles []broker.Profile) error {
	if managed == nil {
		return nil
	}
	if len(profiles) == 0 {
		return fmt.Errorf("managed agent broker requires frozen profiles")
	}
	image := profiles[0].WorkerImage
	spec.ManagedAgentBroker = managed
	spec.Sidecars = append(spec.Sidecars, atc.SidecarConfig{
		Name: runtime.ManagedAgentBrokerName, Image: image,
		Command: []string{"/usr/local/bin/agent-broker"}, WorkingDir: "/",
		Ports: []atc.SidecarPort{{ContainerPort: runtime.ManagedAgentBrokerPort, Protocol: "TCP"}},
	})
	spec.Env = append(spec.Env, runtime.ManagedAgentBrokerMarkerEnv+"=1")
	return runtime.ValidateManagedAgentBroker(*spec)
}
