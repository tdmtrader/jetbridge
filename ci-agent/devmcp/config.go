package devmcp

import (
	"fmt"
	"os"

	"github.com/goccy/go-yaml"
)

// Config is the dev-mcp.yml reference-implementation config: the repo's
// component inventory and the commands backing each tool. Format pinned in
// 00-shared-contracts.md §11 (dev-mcp interface finalization).
type Config struct {
	SchemaVersion int               `yaml:"schema_version"`
	Repo          *ToolCommands     `yaml:"repo,omitempty"` // whole-repo commands (component omitted)
	Components    []ComponentConfig `yaml:"components"`
}

// ToolCommands groups the three command slots.
type ToolCommands struct {
	Build *CommandSpec `yaml:"build,omitempty"`
	Test  *CommandSpec `yaml:"test,omitempty"`
	Lint  *CommandSpec `yaml:"lint,omitempty"`
}

// ComponentConfig declares one component and its commands.
type ComponentConfig struct {
	ID          string       `yaml:"id"`
	Description string       `yaml:"description"`
	Paths       []string     `yaml:"paths"`
	Kind        string       `yaml:"kind"`
	Build       *CommandSpec `yaml:"build,omitempty"`
	Test        *CommandSpec `yaml:"test,omitempty"`
	Lint        *CommandSpec `yaml:"lint,omitempty"`
}

// CommandSpec is one runnable command.
type CommandSpec struct {
	Cmd             []string `yaml:"cmd"`
	Dir             string   `yaml:"dir,omitempty"`               // workdir-relative
	FocusFlag       string   `yaml:"focus_flag,omitempty"`        // run_tests only; appended as <flag>=<focus>
	FailedExitCodes []int    `yaml:"failed_exit_codes,omitempty"` // exit codes meaning "failed"; default [1]
}

var validKinds = map[string]bool{
	"service": true, "library": true, "cli": true,
	"web": true, "docs": true, "other": true,
}

// Load reads and validates a dev-mcp.yml file.
func Load(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	return Parse(raw)
}

// Parse validates eagerly (phaseconfig style): all config errors surface at
// startup, never mid-tool-call.
func Parse(raw []byte) (Config, error) {
	var cfg Config
	if err := yaml.UnmarshalWithOptions(raw, &cfg, yaml.Strict()); err != nil {
		return Config{}, fmt.Errorf("parse dev-mcp.yml: %w", err)
	}
	if err := validateConfig(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func validateConfig(cfg Config) error {
	if cfg.SchemaVersion != 1 {
		return fmt.Errorf("unsupported schema_version %d (want 1)", cfg.SchemaVersion)
	}
	if len(cfg.Components) == 0 {
		return fmt.Errorf("at least one component is required")
	}
	seen := map[string]bool{}
	for i, comp := range cfg.Components {
		if comp.ID == "" {
			return fmt.Errorf("components[%d]: id is required", i)
		}
		if seen[comp.ID] {
			return fmt.Errorf("components[%d]: duplicate id %q", i, comp.ID)
		}
		seen[comp.ID] = true
		if !validKinds[comp.Kind] {
			return fmt.Errorf("component %q: invalid kind %q", comp.ID, comp.Kind)
		}
		if len(comp.Paths) == 0 {
			return fmt.Errorf("component %q: paths is required", comp.ID)
		}
		if err := validateSpecs(comp.ID, comp.Build, comp.Test, comp.Lint); err != nil {
			return err
		}
	}
	if cfg.Repo != nil {
		if err := validateSpecs("repo", cfg.Repo.Build, cfg.Repo.Test, cfg.Repo.Lint); err != nil {
			return err
		}
	}
	return nil
}

func validateSpecs(owner string, specs ...*CommandSpec) error {
	for _, spec := range specs {
		if spec != nil && len(spec.Cmd) == 0 {
			return fmt.Errorf("%s: cmd must be non-empty", owner)
		}
	}
	return nil
}

// Component returns the component with the given id.
func (c Config) Component(id string) (ComponentConfig, bool) {
	for _, comp := range c.Components {
		if comp.ID == id {
			return comp, true
		}
	}
	return ComponentConfig{}, false
}
