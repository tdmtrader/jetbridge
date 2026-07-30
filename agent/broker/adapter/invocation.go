// Package adapter translates exact broker profiles into native harness
// invocations. It constructs argv directly and never through a shell.
package adapter

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/concourse/concourse/agent/broker"
)

type Paths struct {
	WorkDir      string
	ScratchDir   string
	OutputSchema string
}

type Invocation struct {
	Binary  string
	Args    []string
	Env     map[string]string
	WorkDir string
}

// Provenance returns a credential-safe description. Environment names are
// useful operational evidence; their values are deliberately never included.
func (invocation Invocation) Provenance() string {
	keys := make([]string, 0, len(invocation.Env))
	for key := range invocation.Env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	encoded, _ := json.Marshal(struct {
		Binary  string   `json:"binary"`
		Args    []string `json:"args"`
		EnvKeys []string `json:"env_keys"`
		WorkDir string   `json:"work_dir"`
	}{
		Binary: invocation.Binary, Args: invocation.Args, EnvKeys: keys,
		WorkDir: invocation.WorkDir,
	})
	return string(encoded)
}

type Builder interface {
	Build(broker.Profile, Paths, string) (Invocation, error)
}

type Codex struct{}
type Claude struct{}
type Cursor struct{}

func (Codex) Build(profile broker.Profile, paths Paths, credential string) (Invocation, error) {
	if err := validateBuild(profile, broker.AdapterCodex, paths, credential); err != nil {
		return Invocation{}, err
	}
	return Invocation{
		Binary: "codex",
		Args: []string{
			"exec",
			"--ephemeral",
			"--ignore-user-config",
			"--ignore-rules",
			"--sandbox", "read-only",
			"--ask-for-approval", "never",
			"--json",
			"--output-schema", paths.OutputSchema,
			"--output-last-message", filepath.Join(paths.ScratchDir, "result.json"),
			"--model", profile.Provider.Model,
			"-c", fmt.Sprintf("model_reasoning_effort=%q", profile.NativeEffort),
			"-",
		},
		Env: map[string]string{
			"CODEX_API_KEY":   credential,
			"HOME":            paths.ScratchDir,
			"XDG_CONFIG_HOME": filepath.Join(paths.ScratchDir, "config"),
		},
		WorkDir: paths.WorkDir,
	}, nil
}

func (Claude) Build(profile broker.Profile, paths Paths, credential string) (Invocation, error) {
	if err := validateBuild(profile, broker.AdapterClaude, paths, credential); err != nil {
		return Invocation{}, err
	}
	return Invocation{
		Binary: "claude",
		Args: []string{
			"-p",
			"--output-format", "stream-json",
			"--verbose",
			"--model", profile.Provider.Model,
			"--permission-mode", "dontAsk",
			"--allowedTools", "Read,Glob,Grep",
			"--strict-mcp-config",
			"--mcp-config", `{"mcpServers":{}}`,
			"--max-turns", "32",
		},
		Env: map[string]string{
			"ANTHROPIC_API_KEY": credential,
			"HOME":              paths.ScratchDir,
			"XDG_CONFIG_HOME":   filepath.Join(paths.ScratchDir, "config"),
		},
		WorkDir: paths.WorkDir,
	}, nil
}

func (Cursor) Build(profile broker.Profile, paths Paths, credential string) (Invocation, error) {
	if err := validateBuild(profile, broker.AdapterCursor, paths, credential); err != nil {
		return Invocation{}, err
	}
	return Invocation{
		Binary: "cursor-agent",
		Args: []string{
			"--print",
			"--output-format", "stream-json",
			"--model", profile.Provider.Model,
		},
		Env: map[string]string{
			"CURSOR_API_KEY":  credential,
			"HOME":            paths.ScratchDir,
			"XDG_CONFIG_HOME": filepath.Join(paths.ScratchDir, "config"),
		},
		WorkDir: paths.WorkDir,
	}, nil
}

func validateBuild(profile broker.Profile, wanted broker.AdapterName, paths Paths, credential string) error {
	if profile.Adapter.Name != wanted {
		return fmt.Errorf("broker adapter: profile selects adapter %q, not %q", profile.Adapter.Name, wanted)
	}
	if strings.TrimSpace(profile.Provider.Model) == "" {
		return fmt.Errorf("broker adapter: exact model is required")
	}
	if !filepath.IsAbs(paths.WorkDir) || !filepath.IsAbs(paths.ScratchDir) ||
		!filepath.IsAbs(paths.OutputSchema) {
		return fmt.Errorf("broker adapter: work, scratch, and output schema paths must be absolute")
	}
	if strings.TrimSpace(credential) == "" {
		return fmt.Errorf("broker adapter: credential is required")
	}
	return nil
}
