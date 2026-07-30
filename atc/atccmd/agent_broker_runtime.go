package atccmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/broker"
	"github.com/concourse/concourse/agent/broker/workspace"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/agentchildexecutions"
	"github.com/concourse/concourse/atc/exec"
	atcruntime "github.com/concourse/concourse/atc/runtime"
	"k8s.io/apimachinery/pkg/api/resource"
)

type agentBrokerRuntimeConfig struct {
	AuthorityEndpoint string                                   `json:"authority_endpoint"`
	AdapterBinaries   map[string]string                        `json:"adapter_binaries"`
	OutputSchemas     map[string]string                        `json:"output_schemas"`
	Instructions      map[string]agentBrokerRuntimeInstruction `json:"instructions"`
	CredentialSlots   map[string]agentBrokerRuntimeCredential  `json:"credential_slots"`
	CaptureLimits     workspace.Limits                         `json:"capture_limits"`
	ScratchSizeBytes  int64                                    `json:"scratch_size_bytes"`
	Resources         atc.SidecarResources                     `json:"resources"`
}

type agentBrokerRuntimeInstruction struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
}

type agentBrokerRuntimeCredential struct {
	SecretName string `json:"secret_name"`
	Key        string `json:"key"`
}

func loadAgentBrokerRuntime(path string) (agentBrokerRuntimeConfig, error) {
	if strings.TrimSpace(path) == "" {
		return agentBrokerRuntimeConfig{}, errors.New("agent broker runtime configuration is required")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return agentBrokerRuntimeConfig{}, fmt.Errorf("read agent broker runtime configuration: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var config agentBrokerRuntimeConfig
	if err := decoder.Decode(&config); err != nil {
		return agentBrokerRuntimeConfig{}, fmt.Errorf("decode agent broker runtime configuration: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return agentBrokerRuntimeConfig{}, errors.New("decode agent broker runtime configuration: trailing JSON")
	}
	endpoint, err := url.Parse(config.AuthorityEndpoint)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return agentBrokerRuntimeConfig{}, errors.New("agent broker runtime authority endpoint must be an absolute HTTPS URL")
	}
	if err := exactAgentBrokerRuntimeKeys("adapter_binaries", config.AdapterBinaries, []string{"codex", "claude", "cursor-agent"}); err != nil {
		return agentBrokerRuntimeConfig{}, err
	}
	if err := exactAgentBrokerRuntimeKeys("output_schemas", config.OutputSchemas, []string{"request_review", "consult_agent"}); err != nil {
		return agentBrokerRuntimeConfig{}, err
	}
	if err := exactAgentBrokerRuntimeKeys("instructions", config.Instructions, []string{"request_review", "consult_agent"}); err != nil {
		return agentBrokerRuntimeConfig{}, err
	}
	if len(config.CredentialSlots) == 0 || config.CaptureLimits.MaxPatchBytes <= 0 ||
		config.CaptureLimits.MaxPatchBytes > broker.MaxWorkspacePatchBytes ||
		config.CaptureLimits.MaxEntries <= 0 || config.CaptureLimits.StabilityAttempts <= 0 ||
		config.ScratchSizeBytes <= 0 || config.ScratchSizeBytes > 16<<30 {
		return agentBrokerRuntimeConfig{}, errors.New("agent broker runtime bounds and credential slots are required")
	}
	for name, path := range config.AdapterBinaries {
		if !agentBrokerImagePath(path) {
			return agentBrokerRuntimeConfig{}, fmt.Errorf("agent broker adapter path %q is invalid", name)
		}
	}
	for name, path := range config.OutputSchemas {
		if !agentBrokerImagePath(path) {
			return agentBrokerRuntimeConfig{}, fmt.Errorf("agent broker schema path %q is invalid", name)
		}
	}
	for name, instruction := range config.Instructions {
		if !agentBrokerImagePath(instruction.Path) || !agentBrokerDigest(instruction.Digest) {
			return agentBrokerRuntimeConfig{}, fmt.Errorf("agent broker instruction %q is invalid", name)
		}
	}
	for slot, credential := range config.CredentialSlots {
		if strings.TrimSpace(slot) == "" || filepath.Base(slot) != slot ||
			strings.TrimSpace(credential.SecretName) == "" || strings.TrimSpace(credential.Key) == "" {
			return agentBrokerRuntimeConfig{}, fmt.Errorf("agent broker credential slot %q is invalid", slot)
		}
	}
	for label, value := range map[string]string{
		"cpu request": config.Resources.Requests.CPU, "memory request": config.Resources.Requests.Memory,
		"cpu limit": config.Resources.Limits.CPU, "memory limit": config.Resources.Limits.Memory,
	} {
		quantity, err := resource.ParseQuantity(value)
		if err != nil || quantity.Sign() <= 0 {
			return agentBrokerRuntimeConfig{}, fmt.Errorf("agent broker runtime %s must be a positive quantity", label)
		}
	}
	return config, nil
}

func exactAgentBrokerRuntimeKeys[T any](label string, values map[string]T, wanted []string) error {
	if len(values) != len(wanted) {
		return fmt.Errorf("agent broker runtime %s must contain exact keys", label)
	}
	for _, name := range wanted {
		if _, found := values[name]; !found {
			return fmt.Errorf("agent broker runtime %s is missing %q", label, name)
		}
	}
	return nil
}

func agentBrokerImagePath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && path != "/"
}
func agentBrokerDigest(value string) bool {
	return len(value) == len("sha256:")+64 && strings.HasPrefix(value, "sha256:") &&
		strings.Trim(value[len("sha256:"):], "0123456789abcdef") == ""
}

type agentBrokerAuthorityFactory struct {
	signer        *agentchildexecutions.CapabilitySigner
	runtime       agentBrokerRuntimeConfig
	leaseDuration time.Duration
	capabilityTTL time.Duration
	now           func() time.Time
}

func (factory agentBrokerAuthorityFactory) BuildAgentBroker(request exec.AgentBrokerAuthorityRequest) (*atcruntime.ManagedAgentBroker, error) {
	if factory.signer == nil || factory.now == nil || request.ParentAttempt <= 0 || len(request.Profiles) == 0 {
		return nil, errors.New("agent broker authority factory is incomplete")
	}
	scopeInputs := make(map[string]snapshot.SnapshotRef, len(request.ScopeInputs))
	var workspaceBase *snapshot.SnapshotRef
	for name, ref := range request.ScopeInputs {
		if name == "workspace" && request.WorkspaceInputPath != "" {
			copied := ref
			workspaceBase = &copied
			continue
		}
		scopeInputs[name] = ref
	}
	scope := agentchildexecutions.Scope{
		TeamID: request.TeamID, TeamName: request.TeamName, BuildID: request.BuildID,
		SnapshotCreatedBy: request.SnapshotCreatedBy, WorkflowDefinitionID: request.WorkflowDefinitionID,
		WorkflowRunID: request.WorkflowRunID, NodePlanID: request.NodePlanID, ParentAttempt: request.ParentAttempt,
		BrokerInstance: request.BrokerInstance, LeaseDuration: factory.leaseDuration,
		Inputs: scopeInputs, WorkspaceBase: workspaceBase,
	}
	now := factory.now().UTC()
	capability, err := factory.signer.MintBootstrap(scope, request.Profiles, now.Add(-5*time.Second), now.Add(factory.capabilityTTL))
	if err != nil {
		return nil, err
	}
	profileDigests := make(map[string]string, len(request.Profiles))
	commandProfiles := make([]broker.Profile, len(request.Profiles))
	credentialSlots := map[string]string{}
	credentials := []atcruntime.SecretKeyMount{}
	seenSlots := map[string]struct{}{}
	for index, profile := range request.Profiles {
		profileDigests[profile.ID] = profile.Digest
		commandProfiles[index] = profile
		commandProfiles[index].Digest = ""
		for _, tool := range profile.Tools {
			instruction := factory.runtime.Instructions[string(tool)]
			if instruction.Digest != profile.InstructionsDigest {
				return nil, fmt.Errorf("agent broker profile %q instruction is not configured", profile.ID)
			}
		}
		coordinate, found := factory.runtime.CredentialSlots[profile.CredentialSlot]
		if !found {
			return nil, fmt.Errorf("agent broker profile %q credential slot is unavailable", profile.ID)
		}
		if _, duplicate := seenSlots[profile.CredentialSlot]; !duplicate {
			path := filepath.Join(atcruntime.ManagedAgentBrokerCredentialMountRoot, profile.CredentialSlot)
			credentialSlots[profile.CredentialSlot] = path
			credentials = append(credentials, atcruntime.SecretKeyMount{
				Slot: profile.CredentialSlot, SecretName: coordinate.SecretName, Key: coordinate.Key, MountPath: path,
			})
			seenSlots[profile.CredentialSlot] = struct{}{}
		}
	}
	sort.Slice(credentials, func(i, j int) bool { return credentials[i].Slot < credentials[j].Slot })
	attachments := map[string]any{}
	attachmentInputs := []atcruntime.ManagedAgentBrokerAttachmentInput{}
	for name, ref := range scopeInputs {
		inputPath := request.InputPaths[name]
		mountPath := filepath.Join("/attachments", name)
		attachmentInputs = append(attachmentInputs, atcruntime.ManagedAgentBrokerAttachmentInput{Name: name, InputPath: inputPath, MountPath: mountPath})
		role := contracts.SubjectRoleContext
		if ref.Type == "validation/v1" {
			role = contracts.SubjectRoleEvidence
		}
		attachments[name] = map[string]any{
			"path":    filepath.Join(mountPath, "record.json"),
			"subject": contracts.SubjectFromInput(name, role, name, ref),
		}
	}
	sort.Slice(attachmentInputs, func(i, j int) bool { return attachmentInputs[i].Name < attachmentInputs[j].Name })
	authority := map[string]any{
		"authority_endpoint":        factory.runtime.AuthorityEndpoint,
		"bootstrap_capability_file": filepath.Join(atcruntime.ManagedAgentBrokerAuthorityMountRoot, atcruntime.ManagedAgentBrokerBootstrapFile),
		"workspace_root":            atcruntime.ManagedAgentBrokerWorkspaceMountPath,
		"scratch_root":              atcruntime.ManagedAgentBrokerScratchMountPath,
		"adapter_binaries":          factory.runtime.AdapterBinaries, "output_schemas": factory.runtime.OutputSchemas,
		"credential_slots": credentialSlots, "instructions": factory.runtime.Instructions,
		"attachments": attachments, "profiles": commandProfiles, "profile_digests": profileDigests,
		"capture_limits": factory.runtime.CaptureLimits,
	}
	raw, err := json.Marshal(authority)
	if err != nil {
		return nil, err
	}
	managed := &atcruntime.ManagedAgentBroker{
		Authority: atcruntime.PrivateFileMount{MountPath: atcruntime.ManagedAgentBrokerAuthorityMountRoot, Files: map[string][]byte{
			atcruntime.ManagedAgentBrokerAuthorityFile: raw, atcruntime.ManagedAgentBrokerBootstrapFile: []byte(capability),
		}},
		WorkspaceInputPath: request.WorkspaceInputPath, AttachmentInputs: attachmentInputs,
		ScratchSizeBytes: factory.runtime.ScratchSizeBytes, Credentials: credentials, Resources: factory.runtime.Resources,
	}
	return managed, nil
}
