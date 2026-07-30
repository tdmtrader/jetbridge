// Package broker contains the provider-independent child execution contract.
package broker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

type Tool string

const (
	ToolRequestReview Tool = "request_review"
	ToolConsultAgent  Tool = "consult_agent"
)

type Tier string

const (
	TierEconomy  Tier = "economy"
	TierBalanced Tier = "balanced"
	TierFrontier Tier = "frontier"
)

type Effort string

const (
	EffortMedium Effort = "medium"
	EffortHigh   Effort = "high"
)

type AdapterName string

const (
	AdapterClaude AdapterName = "claude-code"
	AdapterCodex  AdapterName = "codex"
	AdapterCursor AdapterName = "cursor-cli"
)

type Selector struct {
	Tier   Tier   `json:"tier" yaml:"tier"`
	Effort Effort `json:"effort" yaml:"effort"`
}

type AdapterSpec struct {
	Name    AdapterName `json:"name" yaml:"name"`
	Version string      `json:"version" yaml:"version"`
}

type ProviderSpec struct {
	Name  string `json:"name" yaml:"name"`
	Model string `json:"model" yaml:"model"`
}

type Limits struct {
	Timeout        time.Duration `json:"timeout" yaml:"timeout"`
	MaxInputBytes  int64         `json:"max_input_bytes" yaml:"max_input_bytes"`
	MaxOutputBytes int64         `json:"max_output_bytes" yaml:"max_output_bytes"`
}

// Controls records the restrictions a profile can actually provide. It is
// provenance and admission input, not a wish list supplied by the caller.
type Controls struct {
	ReadOnlyWorkspace  bool `json:"read_only_workspace" yaml:"read_only_workspace"`
	NoBrokerRecursion  bool `json:"no_broker_recursion" yaml:"no_broker_recursion"`
	TestsUnavailable   bool `json:"tests_unavailable" yaml:"tests_unavailable"`
	NativeOutputSchema bool `json:"native_output_schema" yaml:"native_output_schema"`
	IgnoresUserConfig  bool `json:"ignores_user_config" yaml:"ignores_user_config"`
}

// Profile is the operator-only exact resolution of one neutral selector.
// Digest is derived by NewCatalog and cannot be configured.
type Profile struct {
	ID                 string       `json:"id" yaml:"id"`
	Revision           int          `json:"revision" yaml:"revision"`
	Selector           Selector     `json:"selector" yaml:"selector"`
	Tools              []Tool       `json:"tools" yaml:"tools"`
	Purpose            string       `json:"purpose,omitempty" yaml:"purpose,omitempty"`
	WorkerImage        string       `json:"worker_image" yaml:"worker_image"`
	Adapter            AdapterSpec  `json:"adapter" yaml:"adapter"`
	Provider           ProviderSpec `json:"provider" yaml:"provider"`
	NativeEffort       string       `json:"native_effort" yaml:"native_effort"`
	InstructionsDigest string       `json:"instructions_digest" yaml:"instructions_digest"`
	CredentialSlot     string       `json:"credential_slot" yaml:"credential_slot"`
	Limits             Limits       `json:"limits" yaml:"limits"`
	Controls           Controls     `json:"controls" yaml:"controls"`
	Digest             string       `json:"digest" yaml:"digest"`
}

type VisibleProfile struct {
	Tier    Tier   `json:"tier"`
	Effort  Effort `json:"effort"`
	Tools   []Tool `json:"tools"`
	Purpose string `json:"purpose,omitempty"`
}

type catalogKey struct {
	tool   Tool
	tier   Tier
	effort Effort
}

type Catalog struct {
	byKey   map[catalogKey]Profile
	visible []VisibleProfile
}

var (
	digestPattern  = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	imagePattern   = regexp.MustCompile(`^[^@\s]+@sha256:[0-9a-f]{64}$`)
	versionPattern = regexp.MustCompile(
		`^[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?$`,
	)
)

func NewCatalog(profiles []Profile) (*Catalog, error) {
	if len(profiles) == 0 {
		return nil, fmt.Errorf("broker catalog: at least one profile is required")
	}
	catalog := &Catalog{byKey: make(map[catalogKey]Profile)}
	for index, candidate := range profiles {
		profile := cloneProfile(candidate)
		if profile.Digest != "" {
			return nil, fmt.Errorf("broker catalog: profile[%d]: digest is derived and must be empty", index)
		}
		if err := profile.validate(); err != nil {
			return nil, fmt.Errorf("broker catalog: profile[%d]: %w", index, err)
		}
		sort.Slice(profile.Tools, func(i, j int) bool { return profile.Tools[i] < profile.Tools[j] })
		digest, err := profileDigest(profile)
		if err != nil {
			return nil, fmt.Errorf("broker catalog: profile[%d]: %w", index, err)
		}
		profile.Digest = digest
		for _, tool := range profile.Tools {
			key := catalogKey{tool: tool, tier: profile.Selector.Tier, effort: profile.Selector.Effort}
			if prior, found := catalog.byKey[key]; found {
				return nil, fmt.Errorf(
					"broker catalog: duplicate resolution for %s/%s/%s in profiles %q and %q",
					tool, key.tier, key.effort, prior.ID, profile.ID,
				)
			}
			catalog.byKey[key] = profile
		}
		catalog.visible = append(catalog.visible, VisibleProfile{
			Tier: profile.Selector.Tier, Effort: profile.Selector.Effort,
			Tools: append([]Tool(nil), profile.Tools...), Purpose: profile.Purpose,
		})
	}
	sort.Slice(catalog.visible, func(i, j int) bool {
		if catalog.visible[i].Tier != catalog.visible[j].Tier {
			return catalog.visible[i].Tier < catalog.visible[j].Tier
		}
		return catalog.visible[i].Effort < catalog.visible[j].Effort
	})
	return catalog, nil
}

func (catalog *Catalog) Resolve(tool Tool, selector Selector) (Profile, error) {
	if catalog == nil {
		return Profile{}, fmt.Errorf("broker catalog: catalog is required")
	}
	if err := ValidateSelection(tool, selector); err != nil {
		return Profile{}, err
	}
	profile, found := catalog.byKey[catalogKey{
		tool: tool, tier: selector.Tier, effort: selector.Effort,
	}]
	if !found {
		return Profile{}, fmt.Errorf(
			"broker catalog: selector unsupported for %s: tier=%s effort=%s",
			tool, selector.Tier, selector.Effort,
		)
	}
	return cloneProfile(profile), nil
}

// ValidateSelection validates the complete provider-neutral caller surface
// without consulting an operator catalog. Workflow compilation uses this to
// distinguish malformed authored selectors from valid selectors that require
// authoritative server-side resolution.
func ValidateSelection(tool Tool, selector Selector) error {
	if err := selector.validate(); err != nil {
		return err
	}
	return validateTool(tool)
}

// ValidateResolvedProfile verifies an exact profile copied from a trusted
// catalog into another immutable authority boundary. Unlike NewCatalog, the
// supplied digest is required and must match the complete resolved profile.
func ValidateResolvedProfile(profile Profile) error {
	actual := profile.Digest
	if !digestPattern.MatchString(actual) {
		return fmt.Errorf("broker profile: digest must be an exact sha256 digest")
	}
	profile.Digest = ""
	if err := profile.validate(); err != nil {
		return err
	}
	sort.Slice(profile.Tools, func(i, j int) bool { return profile.Tools[i] < profile.Tools[j] })
	expected, err := profileDigest(profile)
	if err != nil {
		return fmt.Errorf("broker profile: calculate digest: %w", err)
	}
	if actual != expected {
		return fmt.Errorf("broker profile: digest does not match exact resolved profile")
	}
	return nil
}

func (catalog *Catalog) Visible() []VisibleProfile {
	if catalog == nil {
		return nil
	}
	result := make([]VisibleProfile, len(catalog.visible))
	for index, profile := range catalog.visible {
		result[index] = profile
		result[index].Tools = append([]Tool(nil), profile.Tools...)
	}
	return result
}

func (profile Profile) validate() error {
	if strings.TrimSpace(profile.ID) == "" {
		return fmt.Errorf("profile ID is required")
	}
	if profile.Revision < 1 {
		return fmt.Errorf("profile revision must be positive")
	}
	if err := profile.Selector.validate(); err != nil {
		return err
	}
	if len(profile.Tools) == 0 {
		return fmt.Errorf("at least one tool is required")
	}
	seenTools := make(map[Tool]struct{}, len(profile.Tools))
	for _, tool := range profile.Tools {
		if err := validateTool(tool); err != nil {
			return err
		}
		if _, found := seenTools[tool]; found {
			return fmt.Errorf("tool %q is duplicate", tool)
		}
		seenTools[tool] = struct{}{}
	}
	if !imagePattern.MatchString(profile.WorkerImage) {
		return fmt.Errorf("worker image must be pinned to an exact lowercase sha256 digest")
	}
	switch profile.Adapter.Name {
	case AdapterClaude, AdapterCodex, AdapterCursor:
	default:
		return fmt.Errorf("adapter name %q is unsupported", profile.Adapter.Name)
	}
	if !versionPattern.MatchString(profile.Adapter.Version) {
		return fmt.Errorf("adapter harness version must be exact semver")
	}
	if strings.TrimSpace(profile.Provider.Name) == "" || strings.TrimSpace(profile.Provider.Model) == "" {
		return fmt.Errorf("exact provider and model are required")
	}
	if strings.TrimSpace(profile.NativeEffort) == "" {
		return fmt.Errorf("native effort is required")
	}
	if !digestPattern.MatchString(profile.InstructionsDigest) {
		return fmt.Errorf("instructions digest must be an exact sha256 digest")
	}
	if strings.TrimSpace(profile.CredentialSlot) == "" {
		return fmt.Errorf("credential slot is required")
	}
	if profile.Limits.Timeout <= 0 {
		return fmt.Errorf("timeout must be positive")
	}
	if profile.Limits.MaxInputBytes <= 0 || profile.Limits.MaxOutputBytes <= 0 {
		return fmt.Errorf("input and output byte limits must be positive")
	}
	if !profile.Controls.ReadOnlyWorkspace || !profile.Controls.NoBrokerRecursion ||
		!profile.Controls.TestsUnavailable {
		return fmt.Errorf("review broker profiles require read-only workspace, no recursion, and unavailable tests")
	}
	if profile.Adapter.Name == AdapterCursor && profile.Controls.NativeOutputSchema {
		return fmt.Errorf("cursor adapter cannot claim native output schema enforcement")
	}
	return nil
}

func (selector Selector) validate() error {
	switch selector.Tier {
	case TierEconomy, TierBalanced, TierFrontier:
	default:
		return fmt.Errorf("broker selector: tier must be economy, balanced, or frontier")
	}
	switch selector.Effort {
	case EffortMedium, EffortHigh:
	default:
		return fmt.Errorf("broker selector: effort must be medium or high")
	}
	return nil
}

func validateTool(tool Tool) error {
	switch tool {
	case ToolRequestReview, ToolConsultAgent:
		return nil
	default:
		return fmt.Errorf("broker profile: tool %q is unsupported", tool)
	}
}

func profileDigest(profile Profile) (string, error) {
	authority := struct {
		ID                 string
		Revision           int
		Selector           Selector
		Tools              []Tool
		WorkerImage        string
		Adapter            AdapterSpec
		Provider           ProviderSpec
		NativeEffort       string
		InstructionsDigest string
		CredentialSlot     string
		Limits             Limits
		Controls           Controls
	}{
		ID: profile.ID, Revision: profile.Revision, Selector: profile.Selector,
		Tools: profile.Tools, WorkerImage: profile.WorkerImage, Adapter: profile.Adapter,
		Provider: profile.Provider, NativeEffort: profile.NativeEffort,
		InstructionsDigest: profile.InstructionsDigest, CredentialSlot: profile.CredentialSlot,
		Limits: profile.Limits, Controls: profile.Controls,
	}
	encoded, err := json.Marshal(authority)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func cloneProfile(profile Profile) Profile {
	profile.Tools = append([]Tool(nil), profile.Tools...)
	return profile
}
