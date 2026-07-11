package workflow

import (
	"fmt"

	"github.com/goccy/go-yaml"
)

// Parse parses and eagerly validates a workflow definition
// (phaseconfig-style: any structural problem is an import-time error).
func Parse(raw []byte) (*Config, error) {
	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse workflow definition: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Validate checks the §6 grammar rules. Unknown YAML keys are ignored
// (forward compatibility); known keys are strictly checked.
func (c *Config) Validate() error {
	if c.SchemaVersion != 1 {
		return fmt.Errorf("workflow: schema_version must be 1, got %d", c.SchemaVersion)
	}
	if c.Name == "" {
		return fmt.Errorf("workflow: name is required")
	}
	if len(c.Steps) == 0 {
		return fmt.Errorf("workflow: at least one step is required")
	}
	return nil
}
