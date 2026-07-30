package workflow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/concourse/concourse/agent/broker"
	"github.com/concourse/concourse/atc"
)

const brokerProfileProvenanceHashDomain = "workflow-broker-profile-provenance/v1\x00"

// BrokerCatalogRequiredError means a source definition is otherwise valid at
// the provider-neutral broker selector boundary but needs the deployment's
// operator-only catalog for authoritative compilation.
type BrokerCatalogRequiredError struct {
	DefinitionName string
}

func (err BrokerCatalogRequiredError) Error() string {
	if err.DefinitionName == "" {
		return "workflow: broker catalog is required for source broker profiles"
	}
	return fmt.Sprintf("workflow: broker catalog is required to compile %q", err.DefinitionName)
}

// BrokerProfileSelector is the provider-neutral source declaration attached
// to one agent node. It deliberately cannot name a provider, model, harness,
// credential, or control.
type BrokerProfileSelector struct {
	Tool   broker.Tool   `json:"tool" yaml:"tool"`
	Tier   broker.Tier   `json:"tier" yaml:"tier"`
	Effort broker.Effort `json:"effort" yaml:"effort"`
}

// CompiledBrokerProfile is the exact operator profile frozen for one agent
// node and one neutral tool/selector combination.
type CompiledBrokerProfile struct {
	FunctionID string          `json:"function_id" yaml:"function_id"`
	Tool       broker.Tool     `json:"tool" yaml:"tool"`
	Selector   broker.Selector `json:"selector" yaml:"selector"`
	Profile    broker.Profile  `json:"profile" yaml:"profile"`
}

func (profile *CompiledBrokerProfile) UnmarshalJSON(data []byte) error {
	var wire struct {
		FunctionID string          `json:"function_id"`
		Tool       broker.Tool     `json:"tool"`
		Selector   broker.Selector `json:"selector"`
		Profile    json.RawMessage `json:"profile"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return fmt.Errorf("workflow: parse compiled broker profile: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("workflow: compiled broker profile has a trailing JSON value")
		}
		return fmt.Errorf("workflow: compiled broker profile trailing JSON: %w", err)
	}
	profileDecoder := json.NewDecoder(bytes.NewReader(wire.Profile))
	profileDecoder.DisallowUnknownFields()
	var resolved broker.Profile
	if err := profileDecoder.Decode(&resolved); err != nil {
		return fmt.Errorf("workflow: parse compiled broker profile authority: %w", err)
	}
	if err := profileDecoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("workflow: compiled broker profile authority has a trailing JSON value")
		}
		return fmt.Errorf("workflow: compiled broker profile authority trailing JSON: %w", err)
	}
	parsed := CompiledBrokerProfile{FunctionID: wire.FunctionID, Tool: wire.Tool, Selector: wire.Selector, Profile: resolved}
	if err := validateCompiledBrokerProfile(parsed); err != nil {
		return fmt.Errorf("workflow: compiled broker profile: %w", err)
	}
	*profile = parsed
	return nil
}

func BrokerProfileProvenanceHash(profiles []CompiledBrokerProfile) (string, error) {
	if len(profiles) == 0 {
		return "", nil
	}
	canonical, err := json.Marshal(profiles)
	if err != nil {
		return "", fmt.Errorf("workflow: canonicalize broker profile provenance: %w", err)
	}
	return hashDomainSeparated(brokerProfileProvenanceHashDomain, canonical), nil
}

func validateCompiledBrokerProfiles(profiles []CompiledBrokerProfile, provenance string) error {
	if len(profiles) == 0 {
		if provenance != "" {
			return fmt.Errorf("workflow: broker profile provenance hash requires compiled profiles")
		}
		return nil
	}
	if provenance == "" {
		return fmt.Errorf("workflow: compiled broker profiles require a provenance hash")
	}
	seen := make(map[brokerProfileKey]struct{}, len(profiles))
	for index, profile := range profiles {
		if err := validateCompiledBrokerProfile(profile); err != nil {
			return fmt.Errorf("workflow: compiled broker profile %d: %w", index, err)
		}
		key := brokerProfileKey{functionID: profile.FunctionID, tool: profile.Tool, tier: profile.Selector.Tier, effort: profile.Selector.Effort}
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("workflow: duplicate compiled broker profile for node %q tool %q selector %s/%s", profile.FunctionID, profile.Tool, profile.Selector.Tier, profile.Selector.Effort)
		}
		seen[key] = struct{}{}
	}
	actual, err := BrokerProfileProvenanceHash(profiles)
	if err != nil {
		return err
	}
	if actual != provenance {
		return fmt.Errorf("workflow: broker profile provenance hash does not match exact authority bytes")
	}
	return nil
}

func validateCompiledBrokerProfileNodes(plan []atc.Step, profiles []CompiledBrokerProfile) error {
	if len(profiles) == 0 {
		return nil
	}
	nodes := make(map[string]struct{})
	for index := range plan {
		if err := plan[index].Config.Visit(atc.StepRecursor{OnAgent: func(step *atc.AgentStep) error {
			nodes[step.FunctionID] = struct{}{}
			return nil
		}}); err != nil {
			return err
		}
	}
	for _, profile := range profiles {
		if _, found := nodes[profile.FunctionID]; !found {
			return fmt.Errorf("workflow: compiled broker profile node %q is not an agent function", profile.FunctionID)
		}
	}
	return nil
}

func validateCompiledBrokerProfile(profile CompiledBrokerProfile) error {
	if strings.TrimSpace(profile.FunctionID) == "" {
		return fmt.Errorf("function_id is required")
	}
	if err := broker.ValidateResolvedProfile(profile.Profile); err != nil {
		return err
	}
	if profile.Profile.Selector != profile.Selector {
		return fmt.Errorf("selector does not match exact resolved profile")
	}
	found := false
	for _, tool := range profile.Profile.Tools {
		if tool == profile.Tool {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("tool %q is not supported by exact resolved profile", profile.Tool)
	}
	return nil
}

type brokerProfileKey struct {
	functionID string
	tool       broker.Tool
	tier       broker.Tier
	effort     broker.Effort
}

// ResolveBrokerProfile admits only calls that were selected by this exact
// agent node at compile time; it never consults a mutable deployment catalog.
func (config *FunctionConfig) ResolveBrokerProfile(functionID string, tool broker.Tool, selector broker.Selector) (broker.Profile, error) {
	if config == nil {
		return broker.Profile{}, fmt.Errorf("workflow: function is required")
	}
	if err := validateCompiledBrokerProfiles(config.BrokerProfiles, config.BrokerProfileProvenanceHash); err != nil {
		return broker.Profile{}, err
	}
	if err := validateCompiledBrokerProfileNodes(config.Plan, config.BrokerProfiles); err != nil {
		return broker.Profile{}, err
	}
	for _, candidate := range config.BrokerProfiles {
		if candidate.FunctionID == functionID && candidate.Tool == tool && candidate.Selector == selector {
			copy := candidate.Profile
			copy.Tools = append([]broker.Tool(nil), candidate.Profile.Tools...)
			return copy, nil
		}
	}
	return broker.Profile{}, fmt.Errorf("workflow: broker tool %q selector %s/%s is not frozen for node %q", tool, selector.Tier, selector.Effort, functionID)
}

func cloneCompiledBrokerProfiles(source []CompiledBrokerProfile) []CompiledBrokerProfile {
	if len(source) == 0 {
		return nil
	}
	result := make([]CompiledBrokerProfile, len(source))
	for index, profile := range source {
		result[index] = profile
		result[index].Profile.Tools = append([]broker.Tool(nil), profile.Profile.Tools...)
	}
	return result
}

// PublicDefinition removes operator-only exact profile authority from an API
// copy while preserving the stored definition. Provider-neutral authored
// selectors remain available in SourceManifest.
func PublicDefinition(source Definition) Definition {
	result := source
	if source.Compiled.Function != nil {
		function := *source.Compiled.Function
		function.BrokerProfiles = nil
		function.BrokerProfileProvenanceHash = ""
		result.Compiled.Function = &function
	}
	return result
}

// PublicNodeDefinition is the reusable-node equivalent of PublicDefinition.
func PublicNodeDefinition(source NodeDefinition) NodeDefinition {
	result := source
	function := source.Compiled.Function
	function.BrokerProfiles = nil
	function.BrokerProfileProvenanceHash = ""
	result.Compiled.Function = function
	return result
}

func sortCompiledBrokerProfiles(profiles []CompiledBrokerProfile) {
	sort.Slice(profiles, func(i, j int) bool {
		if profiles[i].FunctionID != profiles[j].FunctionID {
			return profiles[i].FunctionID < profiles[j].FunctionID
		}
		if profiles[i].Tool != profiles[j].Tool {
			return profiles[i].Tool < profiles[j].Tool
		}
		if profiles[i].Selector.Tier != profiles[j].Selector.Tier {
			return profiles[i].Selector.Tier < profiles[j].Selector.Tier
		}
		return profiles[i].Selector.Effort < profiles[j].Selector.Effort
	})
}

func renderBrokerAuthority(functionID string, profiles []CompiledBrokerProfile) ([]atc.AgentBrokerProfile, error) {
	selected := make([]atc.AgentBrokerProfile, 0)
	for _, compiled := range profiles {
		if compiled.FunctionID != functionID {
			continue
		}
		if err := validateCompiledBrokerProfile(compiled); err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(compiled.Profile)
		if err != nil {
			return nil, fmt.Errorf("workflow: encode frozen broker profile: %w", err)
		}
		selected = append(selected, atc.AgentBrokerProfile{
			FunctionID: functionID, Tool: string(compiled.Tool), Tier: string(compiled.Selector.Tier), Effort: string(compiled.Selector.Effort),
			ProfileID: compiled.Profile.ID, ProfileRevision: compiled.Profile.Revision, ProfileDigest: compiled.Profile.Digest,
			WorkerImage: compiled.Profile.WorkerImage, Profile: encoded,
		})
	}
	if len(selected) == 0 {
		return nil, nil
	}
	sort.Slice(selected, func(i, j int) bool {
		if selected[i].Tool != selected[j].Tool {
			return selected[i].Tool < selected[j].Tool
		}
		if selected[i].Tier != selected[j].Tier {
			return selected[i].Tier < selected[j].Tier
		}
		return selected[i].Effort < selected[j].Effort
	})
	if err := validateRenderedBrokerAuthority(functionID, selected); err != nil {
		return nil, err
	}
	step := &atc.AgentStep{FunctionID: functionID, BrokerAuthority: selected}
	if err := step.ValidateBrokerAuthority(); err != nil {
		return nil, fmt.Errorf("workflow: rendered broker authority: %w", err)
	}
	return selected, nil
}

func validateRenderedBrokerAuthority(functionID string, authority []atc.AgentBrokerProfile) error {
	for index, entry := range authority {
		decoder := json.NewDecoder(bytes.NewReader(entry.Profile))
		decoder.DisallowUnknownFields()
		var profile broker.Profile
		if err := decoder.Decode(&profile); err != nil {
			return fmt.Errorf("workflow: broker authority %d has invalid profile: %w", index, err)
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			if err == nil {
				return fmt.Errorf("workflow: broker authority %d has trailing profile JSON", index)
			}
			return fmt.Errorf("workflow: broker authority %d trailing profile JSON: %w", index, err)
		}
		canonical, err := json.Marshal(profile)
		if err != nil || !bytes.Equal(canonical, entry.Profile) {
			return fmt.Errorf("workflow: broker authority %d profile is noncanonical", index)
		}
		if err := broker.ValidateResolvedProfile(profile); err != nil {
			return fmt.Errorf("workflow: broker authority %d profile: %w", index, err)
		}
		if entry.FunctionID != functionID || entry.ProfileID != profile.ID || entry.ProfileRevision != profile.Revision || entry.ProfileDigest != profile.Digest || entry.WorkerImage != profile.WorkerImage || entry.Tier != string(profile.Selector.Tier) || entry.Effort != string(profile.Selector.Effort) {
			return fmt.Errorf("workflow: broker authority %d outer identity does not match frozen profile", index)
		}
		matchedTool := false
		for _, tool := range profile.Tools {
			if entry.Tool == string(tool) {
				matchedTool = true
				break
			}
		}
		if !matchedTool {
			return fmt.Errorf("workflow: broker authority %d tool does not match frozen profile", index)
		}
	}
	return nil
}
