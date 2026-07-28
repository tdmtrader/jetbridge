package workflow

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
	"github.com/goccy/go-yaml"
)

const (
	DevValidationCapabilityContract   = "dev-mcp/v1"
	DevValidationCLIPath              = "/usr/local/bin/dev-capability"
	DevValidationCLIValidateCommand   = "validate"
	devValidationProvenanceHashDomain = "workflow-dev-validation-provenance/v1\x00"
)

// DevValidationContract deliberately contains only the immutable input
// contract. It is not an ATC snapshot configuration and cannot express a
// runtime mount, optionality, or a candidate selector.
type DevValidationContract struct {
	Name string           `json:"name" yaml:"name"`
	Type snapshot.TypeRef `json:"type" yaml:"type"`
}

type DevValidationProfile struct {
	Name        string                  `json:"name" yaml:"name"`
	Capability  string                  `json:"capability" yaml:"capability"`
	ProfileFile string                  `json:"profile_file" yaml:"profile_file"`
	ConfigFile  string                  `json:"config_file" yaml:"config_file"`
	Candidate   DevValidationContract   `json:"candidate" yaml:"candidate"`
	BaseInputs  []DevValidationContract `json:"base_inputs" yaml:"base_inputs"`
}

type CompiledDevValidationProfile struct {
	Name                  string                  `json:"name" yaml:"name"`
	Candidate             DevValidationContract   `json:"candidate" yaml:"candidate"`
	BaseInputs            []DevValidationContract `json:"base_inputs" yaml:"base_inputs"`
	CapabilityImage       string                  `json:"capability_image" yaml:"capability_image"`
	CapabilityImageDigest snapshot.Digest         `json:"capability_image_digest" yaml:"capability_image_digest"`
	Command               []string                `json:"command" yaml:"command"`
	Profile               []byte                  `json:"profile" yaml:"profile"`
	ProfileDigest         snapshot.Digest         `json:"profile_digest" yaml:"profile_digest"`
	ProtectedConfig       []byte                  `json:"protected_config" yaml:"protected_config"`
	ProtectedConfigDigest snapshot.Digest         `json:"protected_config_digest" yaml:"protected_config_digest"`
}

func (profile *CompiledDevValidationProfile) UnmarshalJSON(data []byte) error {
	type wire CompiledDevValidationProfile
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var decoded wire
	if err := decoder.Decode(&decoded); err != nil {
		return fmt.Errorf("workflow: parse compiled dev validation profile: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("workflow: compiled dev validation profile has a trailing JSON value")
		}
		return fmt.Errorf("workflow: compiled dev validation profile trailing JSON: %w", err)
	}
	parsed := CompiledDevValidationProfile(decoded)
	if err := validateCompiledDevValidationProfile(parsed); err != nil {
		return fmt.Errorf("workflow: compiled dev validation profile: %w", err)
	}
	*profile = parsed
	return nil
}

func DevValidationProvenanceHash(profiles []CompiledDevValidationProfile) (string, error) {
	if len(profiles) == 0 {
		return "", nil
	}
	canonical, err := json.Marshal(profiles)
	if err != nil {
		return "", fmt.Errorf("workflow: canonicalize dev validation provenance: %w", err)
	}
	return devValidationDomainHash(devValidationProvenanceHashDomain, canonical), nil
}

func devValidationDomainHash(domain string, canonical []byte) string {
	hasher := sha256.New()
	_, _ = hasher.Write([]byte(domain))
	_, _ = hasher.Write(canonical)
	return hex.EncodeToString(hasher.Sum(nil))
}

func ValidateDevValidationAuthority(profiles []CompiledDevValidationProfile, provenance string) error {
	return validateCompiledDevValidationProfiles(profiles, provenance)
}

func validateCompiledDevValidationProfiles(profiles []CompiledDevValidationProfile, provenance string) error {
	if len(profiles) == 0 {
		if provenance != "" {
			return fmt.Errorf("workflow: dev validation provenance hash requires compiled profiles")
		}
		return nil
	}
	if provenance == "" {
		return fmt.Errorf("workflow: compiled dev validation profiles require a provenance hash")
	}
	seen := map[string]struct{}{}
	for i, profile := range profiles {
		if err := validateCompiledDevValidationProfile(profile); err != nil {
			return fmt.Errorf("workflow: compiled dev validation profile %d: %w", i, err)
		}
		if _, ok := seen[profile.Name]; ok {
			return fmt.Errorf("workflow: duplicate compiled dev validation profile %q", profile.Name)
		}
		seen[profile.Name] = struct{}{}
	}
	actual, err := DevValidationProvenanceHash(profiles)
	if err != nil {
		return err
	}
	if actual != provenance {
		return fmt.Errorf("workflow: compiled dev validation provenance hash does not match exact authority bytes")
	}
	return nil
}

func validateCompiledDevValidationProfile(profile CompiledDevValidationProfile) error {
	if err := validateDevValidationProfileName(profile.Name); err != nil {
		return err
	}
	if err := validateDevValidationContracts(profile.Candidate, profile.BaseInputs); err != nil {
		return err
	}
	if err := atc.ValidatePinnedOCIImage(profile.CapabilityImage); err != nil {
		return fmt.Errorf("capability image: %w", err)
	}
	if err := rejectDevValidationInterpolation("capability image", []byte(profile.CapabilityImage)); err != nil {
		return err
	}
	digest, err := devValidationImageDigest(profile.CapabilityImage)
	if err != nil {
		return err
	}
	if profile.CapabilityImageDigest != digest {
		return fmt.Errorf("capability image digest %q does not match image %q", profile.CapabilityImageDigest, profile.CapabilityImage)
	}
	if !equalDevValidationCommand(profile.Command) {
		return fmt.Errorf("command must be exactly %q %q", DevValidationCLIPath, DevValidationCLIValidateCommand)
	}
	if len(profile.Profile) == 0 || len(profile.ProtectedConfig) == 0 {
		return fmt.Errorf("profile and protected config bytes are required")
	}
	if len(profile.Profile) > MaxManifestFileBytes || len(profile.ProtectedConfig) > MaxManifestFileBytes {
		return fmt.Errorf("validation authority asset exceeds %d bytes", MaxManifestFileBytes)
	}
	for _, asset := range [][]byte{profile.Profile, profile.ProtectedConfig} {
		if err := rejectDevValidationInterpolation("validation authority bytes", asset); err != nil {
			return err
		}
	}
	name, err := validateDevValidationProfileWire(profile.Profile)
	if err != nil {
		return err
	}
	if name != profile.Name {
		return fmt.Errorf("promoted profile name %q does not match compiled name %q", name, profile.Name)
	}
	if err := validateDevValidationConfigWire(profile.ProtectedConfig); err != nil {
		return err
	}
	if profile.ProfileDigest != digestDevValidationBytes(profile.Profile) {
		return fmt.Errorf("profile digest does not match exact promoted bytes")
	}
	if profile.ProtectedConfigDigest != digestDevValidationBytes(profile.ProtectedConfig) {
		return fmt.Errorf("protected config digest does not match exact promoted bytes")
	}
	return nil
}

func validateDevValidationContracts(candidate DevValidationContract, bases []DevValidationContract) error {
	if err := validateDevValidationContract(candidate, "candidate"); err != nil {
		return err
	}
	seen := map[string]struct{}{candidate.Name: {}}
	for i, base := range bases {
		if err := validateDevValidationContract(base, fmt.Sprintf("base_inputs[%d]", i)); err != nil {
			return err
		}
		if _, ok := seen[base.Name]; ok {
			return fmt.Errorf("workflow: validation candidate/base input name %q is duplicated", base.Name)
		}
		seen[base.Name] = struct{}{}
	}
	return nil
}
func validateDevValidationContract(contract DevValidationContract, label string) error {
	if !validCapabilityName(contract.Name) {
		return fmt.Errorf("workflow: validation %s name %q must match [a-z][a-z0-9-]*", label, contract.Name)
	}
	if err := contract.Type.Validate(); err != nil {
		return fmt.Errorf("workflow: validation %s type: %w", label, err)
	}
	return nil
}
func cloneCompiledDevValidationProfiles(source []CompiledDevValidationProfile) []CompiledDevValidationProfile {
	if source == nil {
		return nil
	}
	out := make([]CompiledDevValidationProfile, len(source))
	for i, profile := range source {
		out[i] = profile
		out[i].BaseInputs = append([]DevValidationContract(nil), profile.BaseInputs...)
		out[i].Command = append([]string(nil), profile.Command...)
		out[i].Profile = append([]byte(nil), profile.Profile...)
		out[i].ProtectedConfig = append([]byte(nil), profile.ProtectedConfig...)
	}
	return out
}
func devValidationCommand() []string {
	return []string{DevValidationCLIPath, DevValidationCLIValidateCommand}
}
func equalDevValidationCommand(command []string) bool {
	return len(command) == 2 && command[0] == DevValidationCLIPath && command[1] == DevValidationCLIValidateCommand
}
func digestDevValidationBytes(raw []byte) snapshot.Digest {
	return snapshot.Digest("sha256:" + Hash(raw))
}
func devValidationImageDigest(image string) (snapshot.Digest, error) {
	at := strings.LastIndexByte(image, '@')
	if at <= 0 {
		return "", fmt.Errorf("capability image must contain an exact OCI digest")
	}
	digest, err := snapshot.ParseDigest(image[at+1:])
	if err != nil {
		return "", fmt.Errorf("capability image digest: %w", err)
	}
	return digest, nil
}
func validateDevValidationProfileName(name string) error {
	if !validCapabilityName(name) {
		return fmt.Errorf("profile name %q must match [a-z][a-z0-9-]*", name)
	}
	return nil
}
func rejectDevValidationInterpolation(label string, raw []byte) error {
	if bytes.Contains(raw, []byte("((")) {
		return fmt.Errorf("%s must not contain runtime interpolation", label)
	}
	return nil
}
func validateDevValidationAssetReference(path string, mutableRoots map[string]struct{}) error {
	first := strings.Split(path, "/")[0]
	if strings.Contains(first, ":") || strings.Contains(path, "://") {
		return fmt.Errorf("immutable manifest path is required, got %q", path)
	}
	if err := validateManifestPath(path); err != nil {
		return fmt.Errorf("immutable manifest path: %w", err)
	}
	if _, mutable := mutableRoots[first]; mutable {
		return fmt.Errorf("manifest path %q is beneath mutable execution output %q", path, first)
	}
	return nil
}
func devValidationMutableRoots(function *FunctionConfig) map[string]struct{} {
	roots := map[string]struct{}{"candidate": {}, "output": {}, "outputs": {}, "workspace": {}}
	add := func(name string) {
		if name != "" {
			roots[strings.Split(name, "/")[0]] = struct{}{}
		}
	}
	for _, output := range function.Outputs {
		add(output.Name)
		add(output.From)
	}
	_ = walkFunctionSteps(function.Plan, func(step atc.Step, _ string, _ bool) error {
		switch leaf := step.Config.(type) {
		case *atc.TaskStep:
			_, outs := effectiveTaskArtifactNames(leaf)
			for _, out := range outs {
				add(out)
			}
		case *atc.AgentStep:
			for _, out := range leaf.Outputs {
				add(out)
			}
		}
		return nil
	})
	return roots
}

// The compiler validates only the stable, provider-independent profile and
// config envelope. The deterministic CLI owns semantic command validation;
// strict decoding here prevents promoted bytes from carrying unknown control
// fields while still preserving the exact original bytes for provenance.
type devValidationProfileWire struct {
	SchemaVersion int                             `json:"schema_version" yaml:"schema_version"`
	Name          string                          `json:"name" yaml:"name"`
	Checks        []devValidationProfileCheckWire `json:"checks" yaml:"checks"`
}
type devValidationProfileCheckWire struct {
	ID         string   `json:"id" yaml:"id"`
	Operation  string   `json:"operation" yaml:"operation"`
	Scope      string   `json:"scope" yaml:"scope"`
	Components []string `json:"components,omitempty" yaml:"components,omitempty"`
	Timeout    string   `json:"timeout" yaml:"timeout"`
	Retries    int      `json:"retries" yaml:"retries"`
}
type devValidationConfigWire struct {
	SchemaVersion int                                `json:"schema_version" yaml:"schema_version"`
	Repo          *devValidationToolCommandsWire     `json:"repo,omitempty" yaml:"repo,omitempty"`
	Components    []devValidationComponentConfigWire `json:"components" yaml:"components"`
}
type devValidationToolCommandsWire struct {
	Build *devValidationCommandWire `json:"build,omitempty" yaml:"build,omitempty"`
	Test  *devValidationCommandWire `json:"test,omitempty" yaml:"test,omitempty"`
	Lint  *devValidationCommandWire `json:"lint,omitempty" yaml:"lint,omitempty"`
}
type devValidationComponentConfigWire struct {
	ID          string                    `json:"id" yaml:"id"`
	Description string                    `json:"description" yaml:"description"`
	Paths       []string                  `json:"paths" yaml:"paths"`
	Kind        string                    `json:"kind" yaml:"kind"`
	Build       *devValidationCommandWire `json:"build,omitempty" yaml:"build,omitempty"`
	Test        *devValidationCommandWire `json:"test,omitempty" yaml:"test,omitempty"`
	Lint        *devValidationCommandWire `json:"lint,omitempty" yaml:"lint,omitempty"`
}
type devValidationCommandWire struct {
	Cmd             []string `json:"cmd" yaml:"cmd"`
	Dir             string   `json:"dir,omitempty" yaml:"dir,omitempty"`
	FocusFlag       string   `json:"focus_flag,omitempty" yaml:"focus_flag,omitempty"`
	FailedExitCodes []int    `json:"failed_exit_codes,omitempty" yaml:"failed_exit_codes,omitempty"`
}

func validateDevValidationProfileWire(raw []byte) (string, error) {
	var profile devValidationProfileWire
	if err := decodeStrictDevValidationWire(raw, &profile, "validation profile"); err != nil {
		return "", err
	}
	return profile.Name, nil
}
func validateDevValidationConfigWire(raw []byte) error {
	var config devValidationConfigWire
	return decodeStrictDevValidationWire(raw, &config, "protected config")
}
func decodeStrictDevValidationWire(raw []byte, destination any, label string) error {
	decoder := yaml.NewDecoder(bytes.NewReader(raw), yaml.Strict())
	if err := decoder.Decode(destination); err != nil {
		if err == io.EOF {
			return fmt.Errorf("%s document is required", label)
		}
		return fmt.Errorf("strict %s outer wire shape: %w", label, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return fmt.Errorf("%s trailing document is not allowed", label)
	} else if err != io.EOF {
		return fmt.Errorf("%s trailing document: %w", label, err)
	}
	return nil
}
