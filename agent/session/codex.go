package session

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

//go:embed codex-version
var codexVersion string

// CodexVersion is the only Codex release a worker runs. The worker image
// verifies the downloaded binaries against codex-checksums.txt at build time;
// the session checks the executable's self-report at run time.
func CodexVersion() string { return strings.TrimSpace(codexVersion) }

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

// Command removes every capability except the policy's private tool server
// and, for an edit policy, the file-edit tool confined to the working
// directory. Shell, unified exec, JS, web search, plugins, memories and
// network stay off in every policy.
func (Codex) Command(p Policy) []string {
	sandbox := "read-only"
	if p.Edit {
		sandbox = "workspace-write"
	}
	args := []string{"exec", "--ignore-user-config", "--ignore-rules", "--strict-config", "--ephemeral", "--skip-git-repo-check", "--sandbox", sandbox, "--color", "never", "--json", "--model", p.Model, "--cd", p.WorkDir}
	if p.OutputSchema != "" {
		args = append(args, "--output-schema", p.OutputSchema)
	}
	if p.LastMessage != "" {
		args = append(args, "--output-last-message", p.LastMessage)
	}
	quote := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	toolArgs, _ := json.Marshal(p.Tools.Args)
	enabled, _ := json.Marshal(p.Tools.Tools)
	// An edit policy enables Codex's own apply_patch tool, and nothing else
	// that could write: the sandbox confines it to the working directory.
	applyPatch := `features.apply_patch_freeform=false`
	if p.Edit {
		applyPatch = `features.apply_patch_freeform=true`
	}
	settings := []string{
		`approval_policy="never"`, `forced_login_method="chatgpt"`, `cli_auth_credentials_store="file"`,
		`model_provider="openai"`, `history.persistence="none"`, `project_doc_max_bytes=0`, `web_search="disabled"`,
		`features.shell_tool=false`, `features.unified_exec=false`, `features.shell_snapshot=false`,
		applyPatch, `features.multi_agent=false`, `features.js_repl=false`,
		`features.apps=false`, `features.plugins=false`, `features.remote_plugin=false`,
		`features.multi_agent_v2=false`, `features.memories=false`, `features.skip_host_skill_discovery=true`,
		`suppress_unstable_features_warning=true`,
		`features.skill_mcp_dependency_install=false`, `shell_environment_policy.inherit="none"`,
	}
	if p.Edit {
		settings = append(settings, `sandbox_workspace_write.network_access=false`)
	}
	name := p.Tools.Name
	settings = append(settings,
		`mcp_servers.`+name+`.command=`+quote(p.Tools.Command),
		`mcp_servers.`+name+`.args=`+string(toolArgs),
		`mcp_servers.`+name+`.required=true`,
		`mcp_servers.`+name+`.enabled_tools=`+string(enabled),
	)
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
			Type    string          `json:"type"`
			Server  string          `json:"server"`
			Tool    string          `json:"tool"`
			Changes json.RawMessage `json:"changes"`
		} `json:"item"`
	}
	if json.Unmarshal(line, &event) != nil {
		return Event{}, errors.New("invalid Codex event")
	}
	switch event.Type {
	case "thread.started", "turn.started":
		return Event{Kind: EventStarted}, nil
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
	case ItemFileChange:
		var changes []struct {
			Path     *string `json:"path"`
			Kind     *string `json:"kind"`
			MovePath *string `json:"move_path"`
		}
		if json.Unmarshal(event.Item.Changes, &changes) != nil {
			return Event{}, errors.New("malformed Codex file change; nothing was published")
		}
		e.Item = kind
		for _, c := range changes {
			if c.Path == nil || *c.Path == "" || c.Kind == nil || (*c.Kind != "add" && *c.Kind != "delete" && *c.Kind != "update") {
				return Event{}, errors.New("malformed Codex file change; nothing was published")
			}
			change := FileChange{Path: *c.Path, Kind: *c.Kind}
			if c.MovePath != nil {
				change.MovePath = *c.MovePath
			}
			e.Changes = append(e.Changes, change)
		}
		// A file-change item names at least one path; an empty or null
		// change list cannot be checked against the workspace.
		if len(e.Changes) == 0 {
			return Event{}, errors.New("malformed Codex file change; nothing was published")
		}
	default:
		e.Item = ItemOther
	}
	return e, nil
}
