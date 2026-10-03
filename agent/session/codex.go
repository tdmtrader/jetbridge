package session

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

//go:embed codex-version
var codexVersion string

//go:embed codex-checksums.txt
var codexChecksums string

// CodexVersion is the only Codex release a worker runs. The worker image
// verifies the downloaded binaries against codex-checksums.txt at build time;
// the session checks the executable's self-report at run time.
func CodexVersion() string { return strings.TrimSpace(codexVersion) }

// CodexChecksum is the SHA-256 the pinned release publishes for one of its
// assets, as recorded in codex-checksums.txt. Every download of the pinned
// release, the worker image's and the test harness's, is verified against it.
func CodexChecksum(asset string) (string, bool) {
	for _, line := range strings.Split(codexChecksums, "\n") {
		if sum, name, ok := strings.Cut(strings.TrimSpace(line), "  "); ok && name == asset {
			return sum, true
		}
	}
	return "", false
}

// Codex is the Codex CLI provider, run as `codex exec --json`.
type Codex struct{}

var _ Provider = Codex{}

func (Codex) Name() string           { return "codex" }
func (Codex) CredentialFile() string { return "auth.json" }
func (Codex) Environment(home string) []string {
	return []string{"CODEX_HOME=" + home}
}

// CheckAuth accepts a ChatGPT session auth.json only: no API key, and all
// three session tokens present.
func (Codex) CheckAuth(data []byte) error {
	var auth struct {
		Mode   string  `json:"auth_mode"`
		Key    *string `json:"OPENAI_API_KEY"`
		Tokens struct {
			Access  string `json:"access_token"`
			Refresh string `json:"refresh_token"`
			ID      string `json:"id_token"`
		} `json:"tokens"`
	}
	if json.Unmarshal(data, &auth) == nil && (auth.Mode == "" || auth.Mode == "chatgpt") && auth.Key == nil && auth.Tokens.Access != "" && auth.Tokens.Refresh != "" && auth.Tokens.ID != "" {
		return nil
	}
	return errors.New("valid Codex subscription auth.json required")
}

func (Codex) Version(ctx context.Context, run func(args ...string) ([]byte, error)) (string, error) {
	out, err := run("--version")
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", errors.New("could not determine Codex version")
	}
	version := strings.TrimSpace(string(out))
	if version != "codex-cli "+CodexVersion() {
		return "", fmt.Errorf("worker requires codex-cli %s", CodexVersion())
	}
	return version, nil
}

// CatalogFile is the model catalog Prepare derives, in the provider home.
const CatalogFile = "catalog.json"

// Prepare derives the session's model catalog from the one bundled with the
// pinned release, with every model's apply_patch tool removed. Codex decides
// whether to offer its own file-edit tool from the selected model's catalog
// entry, and no command-line setting turns it off; with the derived catalog
// it is never offered, so Codex has no tool that writes. A model the bundled
// catalog does not name fails the session.
func (Codex) Prepare(ctx context.Context, run func(args ...string) ([]byte, error), home string) error {
	out, err := run("debug", "models", "--bundled")
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("could not read the pinned Codex model catalog")
	}
	var catalog struct {
		Models []map[string]json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal(out, &catalog); err != nil || len(catalog.Models) == 0 {
		return errors.New("the pinned Codex reported no model catalog")
	}
	for _, model := range catalog.Models {
		if _, ok := model["apply_patch_tool_type"]; !ok {
			return errors.New("the pinned Codex model catalog has an unexpected shape")
		}
		model["apply_patch_tool_type"] = json.RawMessage("null")
	}
	data, err := json.Marshal(catalog)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(home, CatalogFile), data, 0600)
}

// Command removes every capability except the policy's private tool server:
// a read-only sandbox, the derived catalog in home that offers no file-edit
// tool, and shell, unified exec, JS, web search, plugins, memories and
// network off.
func (Codex) Command(p Policy, home string) []string {
	args := []string{"exec", "--ignore-user-config", "--ignore-rules", "--strict-config", "--ephemeral", "--skip-git-repo-check", "--sandbox", "read-only", "--color", "never", "--json", "--model", p.Model, "--cd", p.WorkDir}
	if p.OutputSchema != "" {
		args = append(args, "--output-schema", p.OutputSchema)
	}
	if p.LastMessage != "" {
		args = append(args, "--output-last-message", p.LastMessage)
	}
	quote := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	toolArgs, _ := json.Marshal(p.Tools.Args)
	enabled, _ := json.Marshal(p.Tools.Tools)
	settings := []string{
		`model_catalog_json=` + quote(filepath.Join(home, CatalogFile)),
		`approval_policy="never"`, `forced_login_method="chatgpt"`, `cli_auth_credentials_store="file"`,
		`model_provider="openai"`, `history.persistence="none"`, `project_doc_max_bytes=0`, `web_search="disabled"`,
		`features.shell_tool=false`, `features.unified_exec=false`, `features.shell_snapshot=false`,
		`features.multi_agent=false`, `features.js_repl=false`,
		`features.apps=false`, `features.plugins=false`, `features.remote_plugin=false`,
		`features.multi_agent_v2=false`, `features.memories=false`, `features.skip_host_skill_discovery=true`,
		`suppress_unstable_features_warning=true`,
		`features.skill_mcp_dependency_install=false`, `shell_environment_policy.inherit="none"`,
	}
	name := p.Tools.Name
	settings = append(settings,
		`mcp_servers.`+name+`.command=`+quote(p.Tools.Command),
		`mcp_servers.`+name+`.args=`+string(toolArgs),
		`mcp_servers.`+name+`.required=true`,
		`mcp_servers.`+name+`.enabled_tools=`+string(enabled),
	)
	for _, tool := range p.Tools.Writes {
		if slices.Contains(p.Tools.Tools, tool) {
			settings = append(settings, `mcp_servers.`+name+`.tools.`+tool+`.approval_mode="approve"`)
		}
	}
	for _, setting := range settings {
		args = append(args, "-c", setting)
	}
	return append(args, "-")
}

// Decode maps a Codex `exec --json` line onto the closed Event vocabulary.
func (Codex) Decode(line []byte) (Event, error) {
	var event struct {
		Type string `json:"type"`
		Item struct {
			Type   string `json:"type"`
			Server string `json:"server"`
			Tool   string `json:"tool"`
		} `json:"item"`
	}
	if json.Unmarshal(line, &event) != nil {
		return Event{}, errors.New("invalid Codex event")
	}
	switch event.Type {
	case "thread.started", "turn.started":
		return Event{Kind: eventStarted}, nil
	case "turn.completed":
		return Event{Kind: EventCompleted}, nil
	case "error", "turn.failed":
		return Event{Kind: EventFailed}, nil
	case "item.started", "item.updated", "item.completed":
	default:
		return Event{}, errors.New("unsupported Codex event; nothing was published")
	}
	e := Event{Kind: EventItem}
	switch kind := ItemKind(event.Item.Type); kind {
	case ItemReasoning, ItemMessage, ItemTodo, ItemError:
		e.Item = kind
	case ItemToolCall:
		e.Item, e.Server, e.Tool = kind, event.Item.Server, event.Item.Tool
	default:
		e.Item = ItemOther
	}
	return e, nil
}
