package phaseconfig

import (
	"crypto/sha256"
	"fmt"
	"os"

	"github.com/goccy/go-yaml"
)

// Config is the top-level phase configuration loaded from YAML.
type Config struct {
	Name    string            `yaml:"name" json:"name"`
	Env     map[string]EnvVar `yaml:"env,omitempty" json:"env,omitempty"`
	MCP     []string          `yaml:"mcp,omitempty" json:"mcp,omitempty"`
	Steps   []Step            `yaml:"steps" json:"steps"`
	Scoring *Scoring          `yaml:"scoring,omitempty" json:"scoring,omitempty"`
}

// EnvVar maps an environment variable to a config field.
type EnvVar struct {
	Var      string `yaml:"var" json:"var"`
	Default  string `yaml:"default,omitempty" json:"default,omitempty"`
	Required bool   `yaml:"required,omitempty" json:"required,omitempty"`
}

// Step is a single prompt step within a phase.
type Step struct {
	Name         string     `yaml:"name" json:"name"`
	Template     string     `yaml:"template" json:"template"`
	OutputSchema string     `yaml:"output_schema,omitempty" json:"output_schema,omitempty"`
	InputFrom    []string   `yaml:"input_from,omitempty" json:"input_from,omitempty"`
	VerifyCmd    string     `yaml:"verify_cmd,omitempty" json:"verify_cmd,omitempty"`
	Artifacts    []Artifact `yaml:"artifacts,omitempty" json:"artifacts,omitempty"`
}

// Artifact describes a file produced by a step.
type Artifact struct {
	Name      string `yaml:"name" json:"name"`
	Path      string `yaml:"path" json:"path"`
	MediaType string `yaml:"media_type" json:"media_type"`
}

// Scoring configures result scoring for a phase.
type Scoring struct {
	Threshold float64            `yaml:"threshold" json:"threshold"`
	Weights   map[string]float64 `yaml:"weights,omitempty" json:"weights,omitempty"`
}

// LoadFile loads a phase config from a YAML file.
func LoadFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read phase config: %w", err)
	}
	return Parse(data)
}

// Parse parses phase config from YAML bytes.
func Parse(data []byte) (*Config, error) {
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse phase config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Validate checks that all required fields are present and input_from references are valid.
func (c *Config) Validate() error {
	if c.Name == "" {
		return fmt.Errorf("phase config: name is required")
	}
	if len(c.Steps) == 0 {
		return fmt.Errorf("phase config: at least one step is required")
	}

	stepIndex := make(map[string]int, len(c.Steps))
	for i, s := range c.Steps {
		if s.Name == "" {
			return fmt.Errorf("phase config: step %d: name is required", i)
		}
		if s.Template == "" {
			return fmt.Errorf("phase config: step %q: template is required", s.Name)
		}
		stepIndex[s.Name] = i
	}

	for i, s := range c.Steps {
		for _, ref := range s.InputFrom {
			refIdx, exists := stepIndex[ref]
			if !exists {
				return fmt.Errorf("phase config: step %q: input_from references unknown step %q", s.Name, ref)
			}
			if refIdx >= i {
				return fmt.Errorf("phase config: step %q: input_from references step %q which is not defined earlier", s.Name, ref)
			}
		}
	}

	return nil
}

// Warning represents a non-fatal validation issue found during suite validation.
type Warning struct {
	Phase   string
	Message string
}

// ValidateSuite checks cross-phase env var wiring. It returns warnings (not errors)
// because phases can be run independently with explicit env vars.
func ValidateSuite(configs []*Config) []Warning {
	if len(configs) == 0 {
		return nil
	}

	// Collect all env config keys provided across all phases.
	provided := make(map[string]bool)
	for _, cfg := range configs {
		for key := range cfg.Env {
			provided[key] = true
		}
	}

	// Check that required env vars have some provider across the suite.
	var warnings []Warning
	for _, cfg := range configs {
		for key, ev := range cfg.Env {
			if !ev.Required {
				continue
			}
			// A required var is satisfied if any other phase provides a config key
			// that could supply it. We check by key name: if no other phase has
			// a key that could map to this one, warn.
			hasProvider := false
			for _, other := range configs {
				if other.Name == cfg.Name {
					continue
				}
				for otherKey := range other.Env {
					if otherKey == key {
						hasProvider = true
						break
					}
				}
				if hasProvider {
					break
				}
			}
			if !hasProvider {
				warnings = append(warnings, Warning{
					Phase:   cfg.Name,
					Message: fmt.Sprintf("required env var %q has no matching provider in other phases", key),
				})
			}
		}
	}

	return warnings
}

// ResolveEnv resolves environment variables declared in the config.
// Returns a map of config key → resolved value.
func (c *Config) ResolveEnv() (map[string]string, error) {
	resolved := make(map[string]string)
	for key, ev := range c.Env {
		val := os.Getenv(ev.Var)
		if val == "" {
			val = ev.Default
		}
		if val == "" && ev.Required {
			return nil, fmt.Errorf("required env var %s is not set", ev.Var)
		}
		resolved[key] = val
	}
	return resolved, nil
}

// Hash returns the SHA256 hash of the raw YAML config bytes.
func Hash(data []byte) string {
	h := sha256.Sum256(data)
	return fmt.Sprintf("%x", h)
}
