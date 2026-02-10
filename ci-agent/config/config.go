package config

import (
	"fmt"
	"path/filepath"

	"github.com/goccy/go-yaml"
)

// ReviewConfig controls how the review agent operates.
type ReviewConfig struct {
	Version         string                    `yaml:"version"`
	SeverityWeights SeverityWeights           `yaml:"severity_weights"`
	Categories      map[string]CategoryConfig `yaml:"categories"`
	Include         []string                  `yaml:"include"`
	Exclude         []string                  `yaml:"exclude"`
}

// SeverityWeights defines the score deduction per severity level.
type SeverityWeights struct {
	Critical float64 `yaml:"critical"`
	High     float64 `yaml:"high"`
	Medium   float64 `yaml:"medium"`
	Low      float64 `yaml:"low"`
}

// CategoryConfig controls whether a review category is enabled.
type CategoryConfig struct {
	Enabled bool `yaml:"enabled"`
}

// DefaultConfig returns the default review configuration.
func DefaultConfig() *ReviewConfig {
	return &ReviewConfig{
		Version:         "1",
		SeverityWeights: defaultWeights(),
		Categories: map[string]CategoryConfig{
			"security":        {Enabled: true},
			"correctness":     {Enabled: true},
			"performance":     {Enabled: true},
			"maintainability": {Enabled: true},
			"testing":         {Enabled: true},
		},
		Include: []string{"**/*.go"},
		Exclude: []string{"vendor/**", "**/*_test.go", "**/fakes/**"},
	}
}

func defaultWeights() SeverityWeights {
	return SeverityWeights{
		Critical: 3.0,
		High:     1.5,
		Medium:   1.0,
		Low:      0.5,
	}
}

// LoadConfig parses a review.yml from YAML bytes. Missing fields are filled
// with defaults.
func LoadConfig(data []byte) (*ReviewConfig, error) {
	cfg := DefaultConfig()

	if len(data) == 0 {
		return cfg, nil
	}

	// Parse into a separate struct to detect which fields were set.
	var parsed ReviewConfig
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("parsing review config: %w", err)
	}

	// Overlay parsed values onto defaults.
	if parsed.SeverityWeights.Critical != 0 {
		cfg.SeverityWeights.Critical = parsed.SeverityWeights.Critical
	}
	if parsed.SeverityWeights.High != 0 {
		cfg.SeverityWeights.High = parsed.SeverityWeights.High
	}
	if parsed.SeverityWeights.Medium != 0 {
		cfg.SeverityWeights.Medium = parsed.SeverityWeights.Medium
	}
	if parsed.SeverityWeights.Low != 0 {
		cfg.SeverityWeights.Low = parsed.SeverityWeights.Low
	}
	if parsed.Categories != nil {
		cfg.Categories = parsed.Categories
	}
	if parsed.Include != nil {
		cfg.Include = parsed.Include
	}
	if parsed.Exclude != nil {
		cfg.Exclude = parsed.Exclude
	}

	return cfg, nil
}

// LoadProfile returns a built-in profile by name.
func LoadProfile(name string) (*ReviewConfig, error) {
	switch name {
	case "default":
		return DefaultConfig(), nil
	case "security":
		cfg := DefaultConfig()
		cfg.SeverityWeights.Critical = 5.0
		cfg.SeverityWeights.High = 3.0
		return cfg, nil
	case "strict":
		cfg := DefaultConfig()
		cfg.SeverityWeights.Critical = 4.0
		cfg.SeverityWeights.High = 2.5
		cfg.SeverityWeights.Medium = 1.5
		cfg.SeverityWeights.Low = 1.0
		return cfg, nil
	default:
		return nil, fmt.Errorf("unknown profile %q: must be one of default, security, strict", name)
	}
}

// ShouldReview returns true if the file path matches the include patterns
// and does not match any exclude patterns. If no include patterns are set,
// all files are included.
func (cfg *ReviewConfig) ShouldReview(filePath string) bool {
	if len(cfg.Include) > 0 {
		matched := false
		for _, pattern := range cfg.Include {
			if matchGlob(pattern, filePath) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	for _, pattern := range cfg.Exclude {
		if matchGlob(pattern, filePath) {
			return false
		}
	}

	return true
}

// matchGlob matches a file path against a glob pattern, supporting ** for
// recursive directory matching. filepath.Match doesn't support **, so we
// handle it here.
func matchGlob(pattern, filePath string) bool {
	// Handle ** prefix: match any directory depth.
	if len(pattern) >= 3 && pattern[:3] == "**/" {
		suffix := pattern[3:]
		// Match against the full path.
		if m, _ := filepath.Match(suffix, filePath); m {
			return true
		}
		// Match against just the filename.
		if m, _ := filepath.Match(suffix, filepath.Base(filePath)); m {
			return true
		}
		// Match against each path suffix (e.g., "pkg/util.go" matches "**/*.go").
		for i := 0; i < len(filePath); i++ {
			if filePath[i] == '/' {
				if m, _ := filepath.Match(suffix, filePath[i+1:]); m {
					return true
				}
			}
		}
		return false
	}

	// Handle ** suffix: e.g., "vendor/**" matches anything under vendor/.
	if len(pattern) >= 3 && pattern[len(pattern)-3:] == "/**" {
		prefix := pattern[:len(pattern)-3]
		if filePath == prefix {
			return true
		}
		if len(filePath) > len(prefix)+1 && filePath[:len(prefix)+1] == prefix+"/" {
			return true
		}
		return false
	}

	// Standard filepath.Match for non-** patterns.
	m, _ := filepath.Match(pattern, filePath)
	return m
}
