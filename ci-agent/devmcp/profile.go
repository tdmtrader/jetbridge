package devmcp

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
)

const (
	ValidationScopeFull     = "full"
	ValidationScopeRequired = "required"
	ValidationScopeAffected = "affected"

	maxProfileCheckTimeout = 24 * time.Hour
)

// ValidationProfile is promoted, immutable policy. It is parsed from
// platform-supplied bytes, never from a candidate workspace.
type ValidationProfile struct {
	SchemaVersion int            `json:"schema_version" yaml:"schema_version"`
	Name          string         `json:"name" yaml:"name"`
	Checks        []ProfileCheck `json:"checks" yaml:"checks"`

	verification profileVerification
}

// ProfileCheck is an ordered deterministic check. Required and affected
// scopes hold the complete conservative component set.
type ProfileCheck struct {
	ID         string    `json:"id" yaml:"id"`
	Operation  Operation `json:"operation" yaml:"operation"`
	Scope      string    `json:"scope" yaml:"scope"`
	Components []string  `json:"components,omitempty" yaml:"components,omitempty"`
	Timeout    string    `json:"timeout" yaml:"timeout"`
	Retries    int       `json:"retries" yaml:"retries"`
}

// ProfileIdentity binds a validation run to the exact profile and protected
// configuration byte streams, including formatting and comments.
type ProfileIdentity struct {
	ProfileDigest         string `json:"profile_digest"`
	ProtectedConfigDigest string `json:"protected_config_digest"`
}

type profileVerification struct {
	parsed         bool
	identity       ProfileIdentity
	semanticDigest [sha256.Size]byte
}

// ParseValidationProfile strictly parses platform-owned policy and protected
// dev-capability config bytes. It deliberately does not accept file paths so
// callers cannot accidentally load candidate-owned replacements.
func ParseValidationProfile(profileBytes, protectedConfigBytes []byte) (ValidationProfile, ProfileIdentity, error) {
	var config Config
	if err := decodeSingleYAML(protectedConfigBytes, &config, "protected config"); err != nil {
		return ValidationProfile{}, ProfileIdentity{}, err
	}
	if err := validateConfig(config); err != nil {
		return ValidationProfile{}, ProfileIdentity{}, fmt.Errorf("invalid protected config: %w", err)
	}

	var profile ValidationProfile
	if err := decodeSingleYAML(profileBytes, &profile, "validation profile"); err != nil {
		return ValidationProfile{}, ProfileIdentity{}, err
	}
	if err := validateProfile(profile, config); err != nil {
		return ValidationProfile{}, ProfileIdentity{}, err
	}

	identity := ProfileIdentity{
		ProfileDigest:         digestBytes(profileBytes),
		ProtectedConfigDigest: digestBytes(protectedConfigBytes),
	}
	semanticDigest, err := profileSemanticDigest(profile)
	if err != nil {
		return ValidationProfile{}, ProfileIdentity{}, fmt.Errorf("seal validation profile: %w", err)
	}
	profile.verification = profileVerification{
		parsed:         true,
		identity:       identity,
		semanticDigest: semanticDigest,
	}
	return profile, identity, nil
}

func decodeSingleYAML(raw []byte, destination any, label string) error {
	decoder := yaml.NewDecoder(bytes.NewReader(raw), yaml.Strict())
	if err := decoder.Decode(destination); err != nil {
		if err == io.EOF {
			return fmt.Errorf("parse %s: document is required", label)
		}
		return fmt.Errorf("parse %s: %w", label, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return fmt.Errorf("parse %s: trailing document is not allowed", label)
	} else if err != io.EOF {
		return fmt.Errorf("parse %s trailing document: %w", label, err)
	}
	return nil
}

func validateProfile(profile ValidationProfile, config Config) error {
	knownComponents := make(map[string]struct{}, len(config.Components))
	for _, component := range config.Components {
		knownComponents[component.ID] = struct{}{}
	}
	if err := validateProfileAgainstComponents(profile, knownComponents); err != nil {
		return err
	}
	for _, check := range profile.Checks {
		if check.Scope == ValidationScopeFull {
			if config.Repo == nil || check.Operation.pick(config.Repo.Build, config.Repo.Test, config.Repo.Lint) == nil {
				return fmt.Errorf("check %q: repo-wide %s operation is not configured", check.ID, check.Operation)
			}
			continue
		}
		for _, componentID := range check.Components {
			component, found := config.Component(componentID)
			if !found {
				return fmt.Errorf("check %q: unknown component %q", check.ID, componentID)
			}
			if check.Operation.pick(component.Build, component.Test, component.Lint) == nil {
				return fmt.Errorf("check %q: component %q does not configure %s", check.ID, componentID, check.Operation)
			}
		}
	}
	return nil
}

func validateProfileShape(profile ValidationProfile) error {
	return validateProfileAgainstComponents(profile, nil)
}

func validateProfileAgainstComponents(profile ValidationProfile, knownComponents map[string]struct{}) error {
	if profile.SchemaVersion != 1 {
		return fmt.Errorf("unsupported validation profile schema_version %d (want 1)", profile.SchemaVersion)
	}
	if strings.TrimSpace(profile.Name) == "" {
		return fmt.Errorf("validation profile name is required")
	}
	if len(profile.Checks) == 0 {
		return fmt.Errorf("validation profile checks are required")
	}

	seenChecks := make(map[string]struct{}, len(profile.Checks))
	for index, check := range profile.Checks {
		if strings.TrimSpace(check.ID) == "" {
			return fmt.Errorf("checks[%d]: id is required", index)
		}
		if _, exists := seenChecks[check.ID]; exists {
			return fmt.Errorf("checks[%d]: duplicate id %q", index, check.ID)
		}
		seenChecks[check.ID] = struct{}{}
		if index > 0 && profile.Checks[index-1].ID > check.ID {
			return fmt.Errorf("checks: ids must be sorted")
		}
		if !check.Operation.valid() {
			return fmt.Errorf("check %q: invalid operation %q", check.ID, check.Operation)
		}
		if err := validateProfileCheckScope(check, knownComponents); err != nil {
			return err
		}
		if _, err := profileCheckTimeout(check); err != nil {
			return err
		}
		if check.Retries < 0 || check.Retries > 2 {
			return fmt.Errorf("check %q: retries must be between 0 and 2", check.ID)
		}
	}
	return nil
}

func validateProfileCheckScope(check ProfileCheck, knownComponents map[string]struct{}) error {
	switch check.Scope {
	case ValidationScopeFull:
		if len(check.Components) != 0 {
			return fmt.Errorf("check %q: full scope cannot declare components", check.ID)
		}
		return nil
	case ValidationScopeRequired, ValidationScopeAffected:
		if len(check.Components) == 0 {
			return fmt.Errorf("check %q: %s scope requires components", check.ID, check.Scope)
		}
	default:
		return fmt.Errorf("check %q: invalid scope %q", check.ID, check.Scope)
	}

	seen := make(map[string]struct{}, len(check.Components))
	for index, component := range check.Components {
		if _, exists := seen[component]; exists {
			return fmt.Errorf("check %q components[%d]: duplicate id %q", check.ID, index, component)
		}
		seen[component] = struct{}{}
		if index > 0 && check.Components[index-1] > component {
			return fmt.Errorf("check %q: component ids must be sorted", check.ID)
		}
		if knownComponents != nil {
			if _, exists := knownComponents[component]; !exists {
				return fmt.Errorf("check %q: unknown component %q", check.ID, component)
			}
		}
	}
	return nil
}

func profileCheckTimeout(check ProfileCheck) (time.Duration, error) {
	if check.Timeout == "" || len(check.Timeout) > 64 {
		return 0, fmt.Errorf("check %q: invalid timeout", check.ID)
	}
	timeout, err := time.ParseDuration(check.Timeout)
	if err != nil {
		return 0, fmt.Errorf("check %q: invalid timeout %q: %w", check.ID, check.Timeout, err)
	}
	if timeout <= 0 {
		return 0, fmt.Errorf("check %q: timeout must be positive", check.ID)
	}
	if timeout > maxProfileCheckTimeout {
		return 0, fmt.Errorf("check %q: timeout must not exceed %s", check.ID, maxProfileCheckTimeout)
	}
	return timeout, nil
}

func verifyParsedProfile(profile ValidationProfile, identity ProfileIdentity) error {
	if !profile.verification.parsed {
		return fmt.Errorf("validation profile was not produced by the strict parser")
	}
	if identity.ProfileDigest == "" || identity.ProtectedConfigDigest == "" {
		return fmt.Errorf("validation profile identity is required")
	}
	if identity != profile.verification.identity {
		return fmt.Errorf("validation profile identity does not match parsed profile")
	}
	semanticDigest, err := profileSemanticDigest(profile)
	if err != nil {
		return fmt.Errorf("verify validation profile: %w", err)
	}
	if semanticDigest != profile.verification.semanticDigest {
		return fmt.Errorf("validation profile changed after parsing")
	}
	return nil
}

func cloneValidationProfile(profile ValidationProfile) ValidationProfile {
	cloned := profile
	cloned.Checks = make([]ProfileCheck, len(profile.Checks))
	for index, check := range profile.Checks {
		cloned.Checks[index] = check
		cloned.Checks[index].Components = append([]string(nil), check.Components...)
	}
	return cloned
}

func profileSemanticDigest(profile ValidationProfile) ([sha256.Size]byte, error) {
	type semanticCheck struct {
		ID         string    `json:"id"`
		Operation  Operation `json:"operation"`
		Scope      string    `json:"scope"`
		Components []string  `json:"components"`
		Timeout    string    `json:"timeout"`
		Retries    int       `json:"retries"`
	}
	semantic := struct {
		SchemaVersion int             `json:"schema_version"`
		Name          string          `json:"name"`
		Checks        []semanticCheck `json:"checks"`
	}{SchemaVersion: profile.SchemaVersion, Name: profile.Name, Checks: make([]semanticCheck, len(profile.Checks))}
	for index, check := range profile.Checks {
		semantic.Checks[index] = semanticCheck{ID: check.ID, Operation: check.Operation, Scope: check.Scope, Components: check.Components, Timeout: check.Timeout, Retries: check.Retries}
	}
	encoded, err := json.Marshal(semantic)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return sha256.Sum256(encoded), nil
}

func digestBytes(raw []byte) string {
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("sha256:%x", sum)
}
