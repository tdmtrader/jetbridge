package steps

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/configvalidate"
)

type ConfigvalidateObservation struct {
	Warnings []string
	Errors   []string
}

// ConfigvalidateBehaviorDefinitions exercises the production pipeline
// validator with concrete atc.Config values. The snapshot selects the same
// diagnostic channel asserted by the paired source spec, then hashes the full
// ordered slice so omitted, added, reordered, or rewritten diagnostics fail.
func ConfigvalidateBehaviorDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[brine.Empty, ConfigvalidateObservation](
			"configvalidate runs the production validator for profile {string}",
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder) (ConfigvalidateObservation, error) {
				profile, ok := p.GetString(0)
				if !ok {
					return ConfigvalidateObservation{}, fmt.Errorf("expected configvalidate profile")
				}
				config, err := configValidationProfile(profile)
				if err != nil {
					return ConfigvalidateObservation{}, err
				}
				warnings, errors := configvalidate.Validate(config)
				observation := ConfigvalidateObservation{Errors: errors}
				for _, warning := range warnings {
					observation.Warnings = append(observation.Warnings, warning.Message)
				}
				return observation, nil
			},
		),
		brine.DefineCheck[ConfigvalidateObservation](
			"the configvalidate {string} snapshot has SHA-256 {string}",
			func(in ConfigvalidateObservation, p brine.Params, _ *brine.Recorder) error {
				channel, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected configvalidate diagnostic channel")
				}
				expected, ok := p.GetString(1)
				if !ok {
					return fmt.Errorf("expected configvalidate snapshot hash")
				}
				var selected []string
				switch channel {
				case "warnings":
					selected = in.Warnings
				case "errors":
					selected = in.Errors
				default:
					return fmt.Errorf("unknown configvalidate diagnostic channel %q", channel)
				}
				canonical, err := json.Marshal(canonicalizeConfigvalidateDiagnostics(selected))
				if err != nil {
					return fmt.Errorf("marshal configvalidate snapshot: %w", err)
				}
				sum := sha256.Sum256(canonical)
				actual := hex.EncodeToString(sum[:])
				if actual != expected {
					return fmt.Errorf("configvalidate %s snapshot SHA-256 is %s, want %s; snapshot=%s", channel, actual, expected, canonical)
				}
				return nil
			},
		),
	}
}

func canonicalizeConfigvalidateDiagnostics(diagnostics []string) []string {
	canonical := append([]string(nil), diagnostics...)
	for index, diagnostic := range canonical {
		lines := strings.Split(diagnostic, "\n")
		sort.Strings(lines)
		canonical[index] = strings.Join(lines, "\n")
	}
	sort.Strings(canonical)
	return canonical
}
