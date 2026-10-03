package session

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestCodexAcceptsOnlySubscriptionSessions(t *testing.T) {
	for auth, ok := range map[string]bool{
		`{"auth_mode":"chatgpt","tokens":{"access_token":"a","refresh_token":"r","id_token":"i"}}`: true,
		`{"tokens":{"access_token":"a","refresh_token":"r","id_token":"i"}}`:                       true,
		`{"auth_mode":"apikey","tokens":{"access_token":"a","refresh_token":"r","id_token":"i"}}`:  false,
		`{"OPENAI_API_KEY":"k","tokens":{"access_token":"a","refresh_token":"r","id_token":"i"}}`:  false,
		`{"auth_mode":"chatgpt","tokens":{"access_token":"a","refresh_token":"r"}}`:                false,
		`{}`: false, `invalid`: false,
	} {
		if (Codex{}.CheckAuth([]byte(auth)) == nil) != ok {
			t.Errorf("CheckAuth(%s) accepted=%v", auth, !ok)
		}
	}
}

func TestCodexVersionIsPinned(t *testing.T) {
	run := func(out string, err error) func(...string) ([]byte, error) {
		return func(args ...string) ([]byte, error) {
			if len(args) != 1 || args[0] != "--version" {
				t.Fatalf("version query %v", args)
			}
			return []byte(out), err
		}
	}
	if v, err := (Codex{}).Version(context.Background(), run("codex-cli "+CodexVersion()+"\n", nil)); err != nil || v != "codex-cli "+CodexVersion() {
		t.Fatalf("pinned version refused: %q %v", v, err)
	}
	if _, err := (Codex{}).Version(context.Background(), run("codex-cli 0.0.1\n", nil)); err == nil {
		t.Fatal("accepted another release")
	}
	if _, err := (Codex{}).Version(context.Background(), run("", errors.New("exit 1"))); err == nil {
		t.Fatal("accepted a failed version query")
	}
}

func TestCodexDecodeIsClosed(t *testing.T) {
	for line, want := range map[string]string{
		`{"type":"thread.started","thread_id":"x"}`:                                                     "started",
		`{"type":"turn.completed","usage":{}}`:                                                          "completed",
		`{"type":"turn.failed","error":{}}`:                                                             "failed",
		`{"type":"item.completed","item":{"type":"reasoning","text":"t"}}`:                              "item:reasoning",
		`{"type":"item.completed","item":{"type":"mcp_tool_call","server":"s","tool":"t"}}`:             "item:mcp_tool_call",
		`{"type":"item.completed","item":{"type":"file_change","changes":[{"path":"a","kind":"add"}]}}`: "item:other",
		`{"type":"item.started","item":{"type":"command_execution","command":"ls"}}`:                    "item:other",
		`{"type":"session.configured"}`:                                                                 "error",
		`not json`:                                                                                      "error",
	} {
		e, err := Codex{}.Decode([]byte(line))
		got := string(e.Kind)
		if e.Kind == EventItem {
			got += ":" + string(e.Item)
		}
		if err != nil {
			got = "error"
		}
		if got != want {
			t.Errorf("%s: %s, want %s", line, got, want)
		}
	}
}

// Every policy runs read-only, from the derived catalog in the provider
// home, with shell, exec, JS and web search off.
func TestPoliciesNeverEnableExecution(t *testing.T) {
	args := strings.Join(Codex{}.Command(Policy{Model: "m", WorkDir: "/w", Tools: ToolServer{Name: "t", Command: "/bin/t", Tools: []string{"read"}}}, "/session/codex"), " ")
	for _, want := range []string{"--sandbox read-only", `model_catalog_json="/session/codex/catalog.json"`, "features.shell_tool=false", "features.unified_exec=false",
		"features.js_repl=false", `web_search="disabled"`, `approval_policy="never"`, `shell_environment_policy.inherit="none"`} {
		if !strings.Contains(args, want) {
			t.Errorf("command lacks %s: %s", want, args)
		}
	}
	if strings.Contains(args, "workspace-write") {
		t.Errorf("a policy can write: %s", args)
	}
}

// Credentials are staged only on tmpfs: the package's own source directory
// is disk, and is refused everywhere; only Linux has a memory runtime.
func TestRequireMemoryRuntimeRefusesDisk(t *testing.T) {
	source, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := RequireMemoryRuntime(source); err == nil {
		t.Fatal("accepted a disk-backed runtime")
	}
	if err := RequireMemoryRuntime(""); err == nil {
		t.Fatal("accepted no runtime")
	}
}
