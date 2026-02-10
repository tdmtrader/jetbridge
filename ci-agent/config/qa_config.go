package config

import (
	"path/filepath"

	"github.com/goccy/go-yaml"
)

// QAConfig controls how the QA agent operates.
type QAConfig struct {
	Threshold     float64  `yaml:"threshold" json:"threshold"`
	GenerateTests bool     `yaml:"generate_tests" json:"generate_tests"`
	BrowserPlan   bool     `yaml:"browser_plan" json:"browser_plan"`
	Include       []string `yaml:"include" json:"include,omitempty"`
	Exclude       []string `yaml:"exclude" json:"exclude,omitempty"`
	TargetURL     string   `yaml:"target_url" json:"target_url,omitempty"`
}

// DefaultQAConfig returns a config with sane defaults.
func DefaultQAConfig() *QAConfig {
	return &QAConfig{
		Threshold:     7.0,
		GenerateTests: true,
		BrowserPlan:   true,
	}
}

// LoadQAConfig parses a YAML configuration.
func LoadQAConfig(yamlBytes []byte) (*QAConfig, error) {
	cfg := DefaultQAConfig()
	if err := yaml.Unmarshal(yamlBytes, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// MatchesFile returns true if a file path matches the include/exclude patterns.
func (c *QAConfig) MatchesFile(path string) bool {
	if len(c.Include) > 0 {
		matched := false
		for _, pattern := range c.Include {
			if m, _ := filepath.Match(pattern, path); m {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	for _, pattern := range c.Exclude {
		if m, _ := filepath.Match(pattern, path); m {
			return false
		}
	}
	return true
}
