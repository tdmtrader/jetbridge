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

// Validate checks that all required fields are present.
func (c *Config) Validate() error {
	if c.Name == "" {
		return fmt.Errorf("phase config: name is required")
	}
	if len(c.Steps) == 0 {
		return fmt.Errorf("phase config: at least one step is required")
	}
	for i, s := range c.Steps {
		if s.Name == "" {
			return fmt.Errorf("phase config: step %d: name is required", i)
		}
		if s.Template == "" {
			return fmt.Errorf("phase config: step %q: template is required", s.Name)
		}
	}
	return nil
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
