package implement

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

// The edit-only vocabulary is narration and the workspace server's tools.
// A file change Codex reports making itself is refused like any other item:
// it is offered no tool that makes one.
func TestEditOnlyTraceVocabulary(t *testing.T) {
	tool := func(server, name string) string {
		return `{"type":"item.completed","item":{"type":"mcp_tool_call","server":"` + server + `","tool":"` + name + `"}}`
	}
	cases := map[string]bool{
		`{"type":"item.completed","item":{"type":"agent_message","text":"done"}}`:                       true,
		`{"type":"item.completed","item":{"type":"file_change","changes":[{"path":"a","kind":"add"}]}}`: false,
		tool("review_input", "read"): false,
		tool("workspace", "exec"):    false,
		tool("workspace", "move"):    false,
		`{"type":"item.started","item":{"type":"command_execution","command":"go test ./..."}}`: false,
		`{"type":"item.started","item":{"type":"web_search","query":"x"}}`:                      false,
		`{"type":"session.unknown"}`: false,
	}
	for _, name := range WorkspaceTools {
		cases[tool("workspace", name)] = true
	}
	for line, ok := range cases {
		e, err := session.Codex{}.Decode([]byte(line))
		if err == nil && e.Kind == session.EventItem {
			err = editItem(e)
		}
		if (err == nil) != ok {
			t.Errorf("%s: accepted=%v, want %v (%v)", line, err == nil, ok, err)
		}
	}
}

// An implement session runs Codex read-only, like review: its only way to
// change anything is the workspace server.
func TestImplementCommandIsEditOnly(t *testing.T) {
	opts := WorkerOptions{Model: "model-x", ToolsCommand: "/usr/local/bin/jb-review-worker"}
	args := strings.Join(session.Codex{}.Command(implementPolicy(opts, "/rt/s/workspace", "", "/rt/s/schema.json", "/rt/s/assessment.json"), "/rt/s/codex"), " ")
	for _, want := range []string{
		"--sandbox read-only", "--cd /rt/s/workspace", `model_catalog_json="/rt/s/codex/catalog.json"`,
		`features.shell_tool=false`, `features.unified_exec=false`, `features.js_repl=false`, `web_search="disabled"`,
		`mcp_servers.workspace.args=["workspace-tools","--root","/rt/s/workspace"]`,
		`mcp_servers.workspace.enabled_tools=["list","read","search","write","replace","delete"]`,
		`mcp_servers.workspace.tools.write.approval_mode="approve"`, `mcp_servers.workspace.tools.replace.approval_mode="approve"`,
		`mcp_servers.workspace.tools.delete.approval_mode="approve"`,
	} {
		if !strings.Contains(args, want) {
			t.Errorf("command lacks %s:\n%s", want, args)
		}
	}
	if strings.Contains(args, "review_input") || strings.Contains(args, "workspace-write") {
		t.Errorf("implement command carries another policy:\n%s", args)
	}
}

func TestWorkspaceReadsAreConfined(t *testing.T) {
	dir := t.TempDir()
	writeTest(t, filepath.Join(dir, "parser.go"), "package parser\nfunc First(s string) byte { return s[1] }\n")
	writeTest(t, filepath.Join(dir, "blob.bin"), "a\x00b")
	outside := filepath.Join(t.TempDir(), "auth.json")
	writeTest(t, outside, "synthetic-access")
	if err := os.Symlink(outside, filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	w, err := NewWorkspace(dir, nil)
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
		t.Fatal("workspace tools accepted a write without content")
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

// Every edit is confined before it happens: it names an ordinary relative
// path outside .git and the read-only inputs, reaches only plain
// directories and regular files, writes text within the session's budget,
// and on refusal leaves the workspace and everything outside it as it was.
func TestWorkspaceEditsAreConfined(t *testing.T) {
	dir := t.TempDir()
	writeTest(t, filepath.Join(dir, "parser.go"), "package parser\nfunc First(s string) byte { return s[1] }\n")
	writeTest(t, filepath.Join(dir, "run.sh"), "#!/bin/sh\necho one\n")
	if err := os.Chmod(filepath.Join(dir, "run.sh"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTest(t, filepath.Join(dir, "twice.txt"), "x\nx\n")
	writeTest(t, filepath.Join(dir, ".git", "config"), "[core]\n")
	outside := t.TempDir()
	writeTest(t, filepath.Join(outside, "auth.json"), "synthetic-access")
	for link, target := range map[string]string{"escape": filepath.Join(outside, "auth.json"), "linkdir": outside} {
		if err := os.Symlink(target, filepath.Join(dir, link)); err != nil {
			t.Fatal(err)
		}
	}
	w, err := NewWorkspace(dir, map[string][]byte{PriorInputPath: []byte("prior\n")})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	call := func(tool, args string) error {
		_, err := w.Call(tool, json.RawMessage(args))
		return err
	}

	// Edits inside the workspace.
	for _, c := range [][2]string{
		{"replace", `{"path":"parser.go","old":"s[1]","new":"s[0]"}`},
		{"write", `{"path":"nested/new.go","content":"package nested\n"}`},
		{"write", `{"path":"run.sh","content":"#!/bin/sh\necho two\n"}`},
		{"delete", `{"path":"twice.txt"}`},
	} {
		if err := call(c[0], c[1]); err != nil {
			t.Fatalf("%s %s: %v", c[0], c[1], err)
		}
	}
	for name, want := range map[string]struct {
		text string
		perm os.FileMode
	}{"parser.go": {"package parser\nfunc First(s string) byte { return s[0] }\n", 0o600}, "nested/new.go": {"package nested\n", 0o600}, "run.sh": {"#!/bin/sh\necho two\n", 0o700}} {
		st, err := os.Lstat(filepath.Join(dir, name))
		b, _ := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(b) != want.text || st.Mode().Perm() != want.perm {
			t.Errorf("%s: %q %v %v", name, b, st, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(dir, "twice.txt")); !os.IsNotExist(err) {
		t.Error("twice.txt was not deleted")
	}

	// Refusals, each leaving everything as it was.
	writeTest(t, filepath.Join(dir, "twice.txt"), "x\nx\n")
	before := snapshotDir(t, dir)
	for _, c := range [][2]string{
		{"write", `{"path":"../escaped.txt","content":"x"}`},
		{"write", `{"path":"` + filepath.Join(outside, "new.txt") + `","content":"x"}`},
		{"write", `{"path":".git/config","content":"[core]\n\thooksPath = /tmp\n"}`},
		{"write", `{"path":"sub/.GIT/hooks/pre-commit","content":"x"}`},
		{"write", `{"path":"/input/prior.patch","content":"x"}`},
		{"write", `{"path":"escape","content":"x"}`},
		{"write", `{"path":"linkdir/new.txt","content":"x"}`},
		{"write", `{"path":"parser.go/inner","content":"x"}`},
		{"write", `{"path":"blob.bin","content":"a\u0000b"}`},
		{"write", `{"path":"a//b","content":"x"}`},
		{"write", `{"path":"parser.go"}`},
		{"replace", `{"path":"twice.txt","old":"x","new":"y"}`},
		{"replace", `{"path":"parser.go","old":"absent","new":"y"}`},
		{"replace", `{"path":"missing.go","old":"a","new":"b"}`},
		{"replace", `{"path":"escape","old":"synthetic","new":"y"}`},
		{"delete", `{"path":"escape"}`},
		{"delete", `{"path":"linkdir/auth.json"}`},
		{"delete", `{"path":"missing.go"}`},
		{"delete", `{"path":"nested"}`},
		{"delete", `{"path":"parser.go","content":"x"}`},
		{"move", `{"path":"parser.go"}`},
	} {
		if err := call(c[0], c[1]); err == nil {
			t.Errorf("%s %s was accepted", c[0], c[1])
		}
	}
	if after := snapshotDir(t, dir); after != before {
		t.Errorf("a refused edit changed the workspace:\n%s\n%s", before, after)
	}
	if b, _ := os.ReadFile(filepath.Join(outside, "auth.json")); string(b) != "synthetic-access" {
		t.Error("an edit changed a file outside the workspace")
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 1 {
		t.Errorf("an edit created a file outside the workspace: %v", entries)
	}

	// The session's write budget.
	w.budget = w.written + 10
	if err := call("write", `{"path":"small.txt","content":"0123456789"}`); err != nil {
		t.Fatal(err)
	}
	if err := call("write", `{"path":"over.txt","content":"x"}`); err == nil {
		t.Fatal("an edit past the budget was accepted")
	}
}

// snapshotDir lists every entry under dir with its mode and content.
func snapshotDir(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		fmt.Fprintf(&b, "%s %v", p, info.Mode())
		if info.Mode().IsRegular() {
			data, _ := os.ReadFile(p)
			fmt.Fprintf(&b, " %q", data)
		}
		b.WriteString("\n")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}
