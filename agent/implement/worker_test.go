package implement

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/session"
)

func TestWorkerRequiresMemoryRuntime(t *testing.T) {
	// Ordinary temporary directories must not become a credential store merely
	// because they will be deleted later. No real credentials are read here.
	if err := session.RequireMemoryRuntime(t.TempDir()); err == nil {
		t.Skip("host temporary directory is itself memory backed")
	}
	if _, err := RunWorker(context.Background(), WorkerOptions{RuntimeDir: t.TempDir()}); err == nil {
		t.Fatal("worker accepted disk runtime")
	}
}

func TestEditOnlyTraceVocabulary(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "session", "workspace")
	allow := editItem(workspace)
	change := func(paths ...string) string {
		var changes []map[string]string
		for _, p := range paths {
			changes = append(changes, map[string]string{"path": p, "kind": "update"})
		}
		b, _ := json.Marshal(map[string]any{"type": "item.completed", "item": map[string]any{"type": "file_change", "changes": changes}})
		return string(b)
	}
	for line, ok := range map[string]bool{
		`{"type":"item.completed","item":{"type":"mcp_tool_call","server":"workspace","tool":"read"}}`: true,
		`{"type":"item.completed","item":{"type":"agent_message","text":"done"}}`:                      true,
		change(filepath.Join(workspace, "parser.go"), "nested/new.go"):                                 true,
		change(filepath.Join(workspace, "..", "codex", "auth.json")):                                   false,
		change("../codex/auth.json"): false,
		change("/etc/passwd"):        false,
		change(workspace):            false,
		`{"type":"item.completed","item":{"type":"file_change","changes":[{"path":"a","kind":"update","move_path":"/tmp/x"}]}}`: false,
		`{"type":"item.completed","item":{"type":"file_change","changes":[{"kind":"update"}]}}`:                                 false,
		`{"type":"item.completed","item":{"type":"file_change","changes":[{"path":"a","kind":"chmod"}]}}`:                       false,
		`{"type":"item.completed","item":{"type":"file_change","changes":"a"}}`:                                                 false,
		`{"type":"item.completed","item":{"type":"file_change","changes":[]}}`:                                                  false,
		`{"type":"item.completed","item":{"type":"mcp_tool_call","server":"review_input","tool":"read"}}`:                       false,
		`{"type":"item.completed","item":{"type":"mcp_tool_call","server":"workspace","tool":"write"}}`:                         false,
		`{"type":"item.started","item":{"type":"command_execution","command":"go test ./..."}}`:                                 false,
		`{"type":"item.started","item":{"type":"web_search","query":"x"}}`:                                                      false,
		`{"type":"session.unknown"}`: false,
	} {
		e, err := session.Codex{}.Decode([]byte(line))
		if err == nil && e.Kind == session.EventItem {
			err = allow(e)
		}
		if (err == nil) != ok {
			t.Errorf("%s: accepted=%v, want %v (%v)", line, err == nil, ok, err)
		}
	}
}

func TestImplementCommandIsEditOnly(t *testing.T) {
	opts := WorkerOptions{Model: "model-x", ToolsCommand: "/usr/local/bin/jb-review-worker"}
	args := strings.Join(session.Codex{}.Command(implementPolicy(opts, "/rt/s/workspace", "", "/rt/s/schema.json", "/rt/s/assessment.json")), " ")
	for _, want := range []string{
		"--sandbox workspace-write", "--cd /rt/s/workspace", `features.apply_patch_freeform=true`,
		`features.shell_tool=false`, `features.unified_exec=false`, `features.js_repl=false`, `web_search="disabled"`,
		`sandbox_workspace_write.network_access=false`, `mcp_servers.workspace.args=["workspace-tools","--root","/rt/s/workspace"]`,
		`mcp_servers.workspace.enabled_tools=["list","read","search"]`,
	} {
		if !strings.Contains(args, want) {
			t.Errorf("command lacks %s:\n%s", want, args)
		}
	}
	if strings.Contains(args, "review_input") || strings.Contains(args, "read-only") {
		t.Errorf("implement command carries the review policy:\n%s", args)
	}
}

func TestWorkspaceReaderIsConfined(t *testing.T) {
	dir := t.TempDir()
	writeTest(t, filepath.Join(dir, "parser.go"), "package parser\nfunc First(s string) byte { return s[1] }\n")
	writeTest(t, filepath.Join(dir, "blob.bin"), "a\x00b")
	outside := filepath.Join(t.TempDir(), "auth.json")
	writeTest(t, outside, "synthetic-access")
	if err := os.Symlink(outside, filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	w, err := NewWorkspaceReader(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if _, err := w.Call("read", json.RawMessage(`{"path":"parser.go"}`)); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../auth.json", outside, "escape", "blob.bin", "missing"} {
		if _, err := w.Call("read", json.RawMessage(`{"path":"`+name+`"}`)); err == nil {
			t.Errorf("read %s", name)
		}
	}
	listed, err := w.Call("list", nil)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(listed)
	if strings.Contains(string(data), "escape") || !strings.Contains(string(data), "parser.go") {
		t.Fatalf("list %s", data)
	}
	found, err := w.Call("search", json.RawMessage(`{"text":"s[1]"}`))
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := json.Marshal(found); !strings.Contains(string(data), "parser.go") {
		t.Fatalf("search %s", data)
	}
	if _, err := w.Call("write", json.RawMessage(`{"path":"parser.go"}`)); err == nil {
		t.Fatal("workspace tools accepted a write")
	}
	// The workspace cannot be published while it holds a link.
	if _, err := readWorkspace(dir); err == nil {
		t.Fatal("read a workspace containing a symlink")
	}
}

func TestWorkspaceToolsFraming(t *testing.T) {
	dir := t.TempDir()
	writeTest(t, filepath.Join(dir, "parser.go"), "package parser\n")
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read","arguments":{"path":"parser.go"}}}`,
	}, "\n") + "\n"
	var out bytes.Buffer
	if err := ServeWorkspaceTools(dir, nil, strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 || !strings.Contains(lines[0], "jetbridge-implement-workspace") || !strings.Contains(lines[2], "package parser") {
		t.Fatalf("framing:\n%s", out.String())
	}
}

func TestWorkspaceRoundTripPreservesModes(t *testing.T) {
	_, s := snapshotFixture(t)
	base := treeOf(t, s)
	dir := t.TempDir()
	if err := populateWorkspace(dir, base); err != nil {
		t.Fatal(err)
	}
	read, err := readWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	patch, changes, err := Diff(base, read)
	if err != nil || len(patch) != 0 || len(changes) != 0 {
		t.Fatalf("untouched workspace differs from base: %v %v\n%s", changes, err, patch)
	}
}
