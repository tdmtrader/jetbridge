package runtime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/concourse/concourse/agent/broker"
	brokerworkspace "github.com/concourse/concourse/agent/broker/workspace"
	"github.com/concourse/concourse/agent/snapshot/contracts"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"k8s.io/apimachinery/pkg/api/resource"
)

const maxManagedAgentBrokerScratchBytes = int64(16 << 30)

// ValidateManagedAgentBroker rejects every shape other than the dedicated
// server-owned broker companion. Worker implementations call it again at the
// last boundary before constructing a pod.
func ValidateManagedAgentBroker(spec ContainerSpec) error {
	managed := spec.ManagedAgentBroker
	for _, env := range spec.Env {
		name, value, _ := strings.Cut(env, "=")
		if managed == nil && (name == ManagedAgentBrokerMarkerEnv || value == ManagedAgentBrokerMCPURL) {
			return errors.New("managed agent broker environment is reserved")
		}
	}
	for _, sidecar := range spec.Sidecars {
		if managed == nil && sidecar.Name == ManagedAgentBrokerName {
			return errors.New("managed agent broker sidecar name is reserved")
		}
		for _, port := range sidecar.Ports {
			if managed == nil && port.ContainerPort == ManagedAgentBrokerPort {
				return errors.New("managed agent broker port is reserved")
			}
		}
	}
	if managed == nil {
		return nil
	}
	if spec.Type != db.ContainerTypeAgent || !spec.Hermetic {
		return errors.New("managed agent broker requires a hermetic agent container")
	}
	if managed.ScratchSizeBytes <= 0 || managed.ScratchSizeBytes > maxManagedAgentBrokerScratchBytes {
		return errors.New("managed agent broker scratch size is invalid")
	}
	for label, value := range map[string]string{
		"cpu request": managed.Resources.Requests.CPU, "memory request": managed.Resources.Requests.Memory,
		"cpu limit": managed.Resources.Limits.CPU, "memory limit": managed.Resources.Limits.Memory,
	} {
		quantity, err := resource.ParseQuantity(value)
		if err != nil || quantity.Sign() <= 0 {
			return fmt.Errorf("managed agent broker %s is invalid", label)
		}
	}
	if managed.WorkspaceInputPath != "" && (!validManagedAuthorityPath(managed.WorkspaceInputPath) ||
		countContainerInputs(spec.Dir, spec.Inputs, managed.WorkspaceInputPath) != 1) {
		return errors.New("managed agent broker workspace does not bind one exact input")
	}
	seenAttachmentNames := map[string]struct{}{}
	for _, input := range managed.AttachmentInputs {
		expected := filepath.Join("/attachments", input.Name)
		if strings.TrimSpace(input.Name) == "" || filepath.Base(input.Name) != input.Name ||
			input.MountPath != expected || countContainerInputs(spec.Dir, spec.Inputs, input.InputPath) != 1 {
			return errors.New("managed agent broker attachment does not bind one exact input")
		}
		if _, duplicate := seenAttachmentNames[input.Name]; duplicate {
			return errors.New("managed agent broker attachment is duplicated")
		}
		seenAttachmentNames[input.Name] = struct{}{}
	}
	brokerImage, review, err := validateManagedAgentBrokerAuthority(*managed)
	if err != nil {
		return err
	}
	if review && managed.WorkspaceInputPath == "" {
		return errors.New("managed agent broker review profile requires workspace input")
	}
	if err := validateManagedAgentBrokerCredentials(managed.Credentials); err != nil {
		return err
	}
	marker := 0
	for _, env := range spec.Env {
		if env == ManagedAgentBrokerMarkerEnv+"=1" {
			marker++
		} else {
			name, value, _ := strings.Cut(env, "=")
			if name == ManagedAgentBrokerMarkerEnv || value == ManagedAgentBrokerMCPURL {
				return errors.New("managed agent broker marker is invalid")
			}
		}
	}
	if marker != 1 {
		return errors.New("managed agent broker marker must occur exactly once")
	}
	found := 0
	for _, sidecar := range spec.Sidecars {
		if sidecar.Name != ManagedAgentBrokerName {
			for _, port := range sidecar.Ports {
				if port.ContainerPort == ManagedAgentBrokerPort {
					return errors.New("managed agent broker port is reserved")
				}
			}
			continue
		}
		found++
		if len(sidecar.Command) != 1 || sidecar.Command[0] != "/usr/local/bin/agent-broker" ||
			len(sidecar.Args) != 0 || sidecar.WorkingDir != "/" || len(sidecar.Ports) != 1 ||
			sidecar.Ports[0] != (atc.SidecarPort{ContainerPort: ManagedAgentBrokerPort, Protocol: "TCP"}) ||
			len(sidecar.Env) != 0 || len(spec.SidecarEnv[ManagedAgentBrokerName]) != 0 {
			return errors.New("managed agent broker sidecar has an invalid fixed runtime shape")
		}
		if err := atc.ValidatePinnedOCIImage(sidecar.Image); err != nil {
			return fmt.Errorf("managed agent broker image: %w", err)
		}
		if sidecar.Image != brokerImage {
			return errors.New("managed agent broker image does not match frozen profiles")
		}
	}
	if found != 1 {
		return fmt.Errorf("managed agent broker sidecar must occur exactly once (found %d)", found)
	}
	return nil
}

type managedAgentBrokerAuthority struct {
	AuthorityEndpoint       string                                  `json:"authority_endpoint"`
	BootstrapCapabilityFile string                                  `json:"bootstrap_capability_file"`
	WorkspaceRoot           string                                  `json:"workspace_root"`
	ScratchRoot             string                                  `json:"scratch_root"`
	AdapterBinaries         map[string]string                       `json:"adapter_binaries"`
	OutputSchemas           map[string]string                       `json:"output_schemas"`
	CredentialSlots         map[string]string                       `json:"credential_slots"`
	Instructions            map[string]managedBrokerInstructionFile `json:"instructions"`
	Attachments             map[string]managedBrokerAttachmentFile  `json:"attachments"`
	Profiles                []broker.Profile                        `json:"profiles"`
	ProfileDigests          map[string]string                       `json:"profile_digests"`
	CaptureLimits           brokerworkspace.Limits                  `json:"capture_limits"`
}

type managedBrokerInstructionFile struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
}

type managedBrokerAttachmentFile struct {
	Path    string            `json:"path"`
	Subject contracts.Subject `json:"subject"`
}

func validateManagedAgentBrokerAuthority(managed ManagedAgentBroker) (string, bool, error) {
	if managed.Authority.MountPath != ManagedAgentBrokerAuthorityMountRoot ||
		len(managed.Authority.Files) != 2 ||
		len(managed.Authority.Files[ManagedAgentBrokerAuthorityFile]) == 0 ||
		len(managed.Authority.Files[ManagedAgentBrokerBootstrapFile]) == 0 {
		return "", false, errors.New("managed agent broker requires exact authority and bootstrap files")
	}
	raw := managed.Authority.Files[ManagedAgentBrokerAuthorityFile]
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var authority managedAgentBrokerAuthority
	if err := decoder.Decode(&authority); err != nil {
		return "", false, fmt.Errorf("managed agent broker authority: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return "", false, errors.New("managed agent broker authority contains trailing JSON")
	}
	if len(authority.Profiles) == 0 || authority.WorkspaceRoot != ManagedAgentBrokerWorkspaceMountPath ||
		authority.ScratchRoot != ManagedAgentBrokerScratchMountPath ||
		authority.BootstrapCapabilityFile != filepath.Join(ManagedAgentBrokerAuthorityMountRoot, ManagedAgentBrokerBootstrapFile) {
		return "", false, errors.New("managed agent broker authority has an invalid fixed layout")
	}
	endpoint, err := url.Parse(authority.AuthorityEndpoint)
	if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Host == "" ||
		endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return "", false, errors.New("managed agent broker authority endpoint is invalid")
	}
	if err := exactManagedBrokerKeys("adapter binaries", authority.AdapterBinaries, []string{"codex", "claude", "cursor-agent"}); err != nil {
		return "", false, err
	}
	if err := exactManagedBrokerKeys("output schemas", authority.OutputSchemas, []string{"request_review", "consult_agent"}); err != nil {
		return "", false, err
	}
	if err := exactManagedBrokerKeys("instructions", authority.Instructions, []string{"request_review", "consult_agent"}); err != nil {
		return "", false, err
	}
	for name, path := range authority.AdapterBinaries {
		if !validManagedAuthorityPath(path) {
			return "", false, fmt.Errorf("managed agent broker adapter binary %q is invalid", name)
		}
	}
	for name, path := range authority.OutputSchemas {
		if !validManagedAuthorityPath(path) {
			return "", false, fmt.Errorf("managed agent broker output schema %q is invalid", name)
		}
	}
	for name, instruction := range authority.Instructions {
		if !validManagedAuthorityPath(instruction.Path) || snapshotDigestInvalid(instruction.Digest) {
			return "", false, fmt.Errorf("managed agent broker instruction %q is invalid", name)
		}
	}
	if authority.CaptureLimits.MaxPatchBytes <= 0 || authority.CaptureLimits.MaxPatchBytes > broker.MaxWorkspacePatchBytes ||
		authority.CaptureLimits.MaxEntries <= 0 || authority.CaptureLimits.StabilityAttempts <= 0 {
		return "", false, errors.New("managed agent broker capture limits are invalid")
	}
	catalogInput := make([]broker.Profile, len(authority.Profiles))
	image := ""
	review := false
	for index, profile := range authority.Profiles {
		if profile.Digest != "" {
			return "", false, fmt.Errorf("managed agent broker profile %d carries a configurable digest", index)
		}
		if image == "" {
			image = profile.WorkerImage
		} else if image != profile.WorkerImage {
			return "", false, errors.New("managed agent broker profiles do not share one image")
		}
		for _, tool := range profile.Tools {
			review = review || tool == broker.ToolRequestReview
			instruction, found := authority.Instructions[string(tool)]
			if !found || instruction.Digest != profile.InstructionsDigest {
				return "", false, fmt.Errorf("managed agent broker profile %q instruction mismatch", profile.ID)
			}
		}
		if authority.CredentialSlots[profile.CredentialSlot] != filepath.Join(ManagedAgentBrokerCredentialMountRoot, profile.CredentialSlot) {
			return "", false, fmt.Errorf("managed agent broker profile %q credential mismatch", profile.ID)
		}
		catalogInput[index] = profile
	}
	if len(authority.ProfileDigests) != len(authority.Profiles) {
		return "", false, errors.New("managed agent broker profile digests are not exact")
	}
	catalog, err := broker.NewCatalog(catalogInput)
	if err != nil {
		return "", false, fmt.Errorf("managed agent broker catalog: %w", err)
	}
	for _, profile := range authority.Profiles {
		for _, tool := range profile.Tools {
			resolved, err := catalog.Resolve(tool, profile.Selector)
			if err != nil || resolved.ID != profile.ID || authority.ProfileDigests[profile.ID] != resolved.Digest {
				return "", false, fmt.Errorf("managed agent broker profile %q digest mismatch", profile.ID)
			}
		}
	}
	if len(authority.CredentialSlots) != len(managed.Credentials) {
		return "", false, errors.New("managed agent broker credential slots are not exact")
	}
	for _, credential := range managed.Credentials {
		if authority.CredentialSlots[credential.Slot] != credential.MountPath {
			return "", false, fmt.Errorf("managed agent broker credential slot %q is not authorized", credential.Slot)
		}
	}
	attachmentInputs := make(map[string]ManagedAgentBrokerAttachmentInput, len(managed.AttachmentInputs))
	for _, input := range managed.AttachmentInputs {
		attachmentInputs[input.Name] = input
	}
	if len(authority.Attachments) != len(attachmentInputs) {
		return "", false, errors.New("managed agent broker attachments are not exact")
	}
	for name, attachment := range authority.Attachments {
		input, found := attachmentInputs[name]
		if !found || attachment.Path != filepath.Join(input.MountPath, "record.json") ||
			attachment.Subject.Validate() != nil || attachment.Subject.Input != name {
			return "", false, fmt.Errorf("managed agent broker attachment %q is invalid", name)
		}
	}
	return image, review, nil
}

func exactManagedBrokerKeys[T any](label string, values map[string]T, wanted []string) error {
	if len(values) != len(wanted) {
		return fmt.Errorf("managed agent broker %s are not exact", label)
	}
	for _, name := range wanted {
		if _, found := values[name]; !found {
			return fmt.Errorf("managed agent broker %s are missing %q", label, name)
		}
	}
	return nil
}

func snapshotDigestInvalid(raw string) bool {
	return !strings.HasPrefix(raw, "sha256:") || len(raw) != len("sha256:")+64
}

func validateManagedAgentBrokerCredentials(credentials []SecretKeyMount) error {
	seen := map[string]struct{}{}
	for _, credential := range credentials {
		path := filepath.Join(ManagedAgentBrokerCredentialMountRoot, credential.Slot)
		if strings.TrimSpace(credential.Slot) == "" || filepath.Base(credential.Slot) != credential.Slot ||
			strings.TrimSpace(credential.SecretName) == "" || strings.TrimSpace(credential.Key) == "" ||
			credential.MountPath != path {
			return errors.New("managed agent broker credential projection is invalid")
		}
		if _, duplicate := seen[credential.Slot]; duplicate {
			return errors.New("managed agent broker credential slot is duplicated")
		}
		seen[credential.Slot] = struct{}{}
	}
	return nil
}
