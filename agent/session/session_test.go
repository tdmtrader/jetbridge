package session

import (
	"context"
	"errors"
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
		`{"type":"thread.started","thread_id":"x"}`:                                                        "started",
		`{"type":"turn.completed","usage":{}}`:                                                             "completed",
		`{"type":"turn.failed","error":{}}`:                                                                "failed",
		`{"type":"item.completed","item":{"type":"reasoning","text":"t"}}`:                                 "item:reasoning",
		`{"type":"item.completed","item":{"type":"mcp_tool_call","server":"s","tool":"t"}}`:                "item:mcp_tool_call",
		`{"type":"item.completed","item":{"type":"file_change","changes":[{"path":"a","kind":"add"}]}}`:    "item:file_change",
		`{"type":"item.started","item":{"type":"command_execution","command":"ls"}}`:                       "item:other",
		`{"type":"item.completed","item":{"type":"file_change","changes":[{"path":"","kind":"add"}]}}`:     "error",
		`{"type":"item.completed","item":{"type":"file_change","changes":[{"path":"a","kind":"rename"}]}}`: "error",
		`{"type":"item.completed","item":{"type":"file_change"}}`:                                          "error",
		`{"type":"session.configured"}`:                                                                    "error",
		`not json`:                                                                                         "error",
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

func TestPoliciesNeverEnableExecution(t *testing.T) {
	for _, edit := range []bool{false, true} {
		args := strings.Join(Codex{}.Command(Policy{Model: "m", WorkDir: "/w", Edit: edit, Tools: ToolServer{Name: "t", Command: "/bin/t", Tools: []string{"read"}}}), " ")
		for _, off := range []string{"features.shell_tool=false", "features.unified_exec=false", "features.js_repl=false", `web_search="disabled"`, `approval_policy="never"`, `shell_environment_policy.inherit="none"`} {
			if !strings.Contains(args, off) {
				t.Errorf("edit=%v lacks %s", edit, off)
			}
		}
		if edit != strings.Contains(args, "--sandbox workspace-write") {
			t.Errorf("edit=%v sandbox: %s", edit, args)
		}
	}
}
