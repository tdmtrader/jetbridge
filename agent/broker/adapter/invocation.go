// Package adapter translates exact broker profiles into native harness
// invocations. It constructs argv directly and never through a shell.
package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
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

// PreparedInvocation is an opaque, preflight-bound invocation. Only Prepare
// can bind a process launch to the discovered binary identity and exact
// harness version.
type PreparedInvocation struct {
	invocation Invocation
	identity   Identity
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

const maxInlineOutputSchemaBytes = 1 << 20

// Prepare runs local binary/version preflight before constructing an
// invocation that contains a provider credential. Its returned opaque value is
// the only input Execute accepts.
func Prepare(
	ctx context.Context,
	profile broker.Profile,
	paths Paths,
	credential string,
	probe VersionProbe,
) (PreparedInvocation, error) {
	identity, err := Preflight(ctx, profile, probe)
	if err != nil {
		return PreparedInvocation{}, err
	}
	builder, err := builderFor(profile.Adapter.Name)
	if err != nil {
		return PreparedInvocation{}, err
	}
	invocation, err := builder.Build(profile, paths, credential)
	if err != nil {
		return PreparedInvocation{}, err
	}
	invocation.Binary = identity.Binary
	return PreparedInvocation{invocation: invocation, identity: identity}, nil
}

func builderFor(name broker.AdapterName) (Builder, error) {
	switch name {
	case broker.AdapterCodex:
		return Codex{}, nil
	case broker.AdapterClaude:
		return Claude{}, nil
	case broker.AdapterCursor:
		return Cursor{}, nil
	default:
		return nil, fmt.Errorf("%w: unsupported adapter %q", ErrHarnessIncompatible, name)
	}
}

func (Codex) Build(profile broker.Profile, paths Paths, credential string) (Invocation, error) {
	if _, err := validateBuild(profile, broker.AdapterCodex, paths, credential); err != nil {
		return Invocation{}, err
	}
	return Invocation{
		Binary: "codex",
		Args: []string{
			"exec",
			"--strict-config",
			"--ephemeral",
			"--ignore-user-config",
			"--ignore-rules",
			"--sandbox", "read-only",
			"--model", profile.Provider.Model,
			"-c", `approval_policy="never"`,
			"-c", fmt.Sprintf("model_reasoning_effort=%q", profile.NativeEffort),
			"-c", "project_doc_max_bytes=0",
			"-c", "project_doc_fallback_filenames=[]",
			"--json",
			"--output-schema", paths.OutputSchema,
			"--output-last-message", filepath.Join(paths.ScratchDir, "result.json"),
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
	capabilities, err := validateBuild(profile, broker.AdapterClaude, paths, credential)
	if err != nil {
		return Invocation{}, err
	}
	schema, err := loadInlineOutputSchema(paths.OutputSchema)
	if err != nil {
		return Invocation{}, err
	}
	args := []string{
		"-p",
		"--bare",
		"--output-format", "stream-json",
		"--verbose",
		"--model", profile.Provider.Model,
		"--permission-mode", "dontAsk",
		"--allowedTools", "Read,Glob,Grep",
		"--strict-mcp-config",
		"--mcp-config", `{"mcpServers":{}}`,
		"--max-turns", "32",
	}
	if capabilities.NativeOutputSchema && profile.Controls.NativeOutputSchema {
		args = append(args, "--json-schema", string(schema))
	}
	return Invocation{
		Binary: "claude",
		Args:   args,
		Env: map[string]string{
			"ANTHROPIC_API_KEY":   credential,
			"HOME":                paths.ScratchDir,
			"XDG_CONFIG_HOME":     filepath.Join(paths.ScratchDir, "config"),
			"DISABLE_UPDATES":     "1",
			"DISABLE_AUTOUPDATER": "1",
		},
		WorkDir: paths.WorkDir,
	}, nil
}

func loadInlineOutputSchema(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("broker adapter: inspect output schema: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("broker adapter: output schema must be a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("broker adapter: open output schema: %w", err)
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxInlineOutputSchemaBytes+1))
	if err != nil {
		return nil, fmt.Errorf("broker adapter: read output schema: %w", err)
	}
	if len(raw) > maxInlineOutputSchemaBytes {
		return nil, fmt.Errorf("broker adapter: output schema exceeds %d bytes", maxInlineOutputSchemaBytes)
	}
	if !json.Valid(raw) {
		return nil, fmt.Errorf("broker adapter: output schema is not valid JSON")
	}
	return raw, nil
}

func (Cursor) Build(profile broker.Profile, paths Paths, credential string) (Invocation, error) {
	if _, err := validateBuild(profile, broker.AdapterCursor, paths, credential); err != nil {
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

func validateBuild(profile broker.Profile, wanted broker.AdapterName, paths Paths, credential string) (Capabilities, error) {
	if profile.Adapter.Name != wanted {
		return Capabilities{}, fmt.Errorf("broker adapter: profile selects adapter %q, not %q", profile.Adapter.Name, wanted)
	}
	if strings.TrimSpace(profile.Provider.Model) == "" {
		return Capabilities{}, fmt.Errorf("broker adapter: exact model is required")
	}
	if !filepath.IsAbs(paths.WorkDir) || !filepath.IsAbs(paths.ScratchDir) ||
		!filepath.IsAbs(paths.OutputSchema) {
		return Capabilities{}, fmt.Errorf("broker adapter: work, scratch, and output schema paths must be absolute")
	}
	if strings.TrimSpace(credential) == "" {
		return Capabilities{}, fmt.Errorf("broker adapter: credential is required")
	}
	capabilities, err := CapabilitiesFor(profile)
	if err != nil {
		return Capabilities{}, err
	}
	return capabilities, nil
}
