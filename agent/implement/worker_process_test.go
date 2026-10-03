package implement

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/session"
	"github.com/concourse/concourse/agent/session/codextest"
)

func TestMain(m *testing.M) { codextest.Main(m) }

// model names a model the pinned release's bundled catalog knows.
const model = "gpt-5.5"

// fixtureEdits is the canonical fixture edit, made through the workspace
// tools: update parser.go, add parser_test.go, delete deleted.txt.
func fixtureEdits() []codextest.Item {
	return []codextest.Item{
		codextest.ToolCall(workspaceServer, "replace", map[string]any{"path": "parser.go", "old": "return s[1]", "new": "return s[0]"}),
		codextest.ToolCall(workspaceServer, "write", map[string]any{"path": "parser_test.go",
			"content": "package parser\n\nimport \"testing\"\n\nfunc TestFirst(t *testing.T) {\n\tif First(\"ab\") != 'a' {\n\t\tt.Fatal(\"wrong byte\")\n\t}\n}\n"}),
		codextest.ToolCall(workspaceServer, "delete", map[string]any{"path": "deleted.txt"}),
	}
}

const done = `{"summary":"Return the first byte and cover it with a test.","complete":true,"limitations":[]}`

type implementation struct {
	runtime, output string
	model           *codextest.Model
	opts            WorkerOptions
}

func newImplementation(t *testing.T, turns ...codextest.Turn) *implementation {
	t.Helper()
	return &implementation{runtime: t.TempDir(), output: filepath.Join(t.TempDir(), "change"), model: codextest.NewModel(t, turns...)}
}

// run runs the implement worker, minus its tmpfs gate, on snapshot with the
// pinned Codex answering to the scripted model. It checks what holds after
// every session: the session is gone and no credential reached the change.
func (r *implementation) run(t *testing.T, s *Snapshot) (*Summary, error) {
	t.Helper()
	run := 11
	r.opts = WorkerOptions{Input: s.Dir, Output: r.output, RuntimeDir: r.runtime, Codex: codextest.Launcher(t, r.model),
		ToolsCommand: codextest.Worker(t), Model: model, RunID: &run, Auth: io.NopCloser(bytes.NewReader(codextest.Auth())), Timeout: time.Minute}
	summary, err := runInRuntime(context.Background(), r.opts)
	if entries, _ := os.ReadDir(r.runtime); len(entries) != 0 {
		t.Fatalf("the session survived: %v", entries)
	}
	filepath.WalkDir(r.output, func(path string, d os.DirEntry, err error) error {
		if b, _ := os.ReadFile(path); bytes.Contains(b, []byte(codextest.Secret)) {
			t.Errorf("the credential reached %s", path)
		}
		return nil
	})
	return summary, err
}

// landed fails unless the session succeeded and the workspace server made
// every edit, quoting what the model was told when it did not.
func (r *implementation) landed(t *testing.T, edits []codextest.Item, err error) {
	t.Helper()
	for _, edit := range edits {
		out, _ := r.model.Output(edit)
		if err != nil || !strings.Contains(out, `"path"`) || strings.Contains(out, "cannot") {
			t.Fatalf("the edit did not land (%v); the model was told: %s", err, out)
		}
	}
}

// The model reads and edits the workspace through the tools Codex serves
// from the worker executable; the change the worker publishes applies to the
// repository as exactly that edit, once.
func TestImplementSessionPublishesTheModelsEdit(t *testing.T) {
	repo, s := snapshotFixture(t)
	read := codextest.ToolCall(workspaceServer, "read", map[string]any{"path": "parser.go"})
	edits := fixtureEdits()
	r := newImplementation(t, codextest.Steps(append(append([]codextest.Item{read}, edits...), codextest.Say(done))...)...)
	summary, err := r.run(t, s)
	r.landed(t, edits, err)
	if out, _ := r.model.Output(read); !strings.Contains(out, "return s[1]") {
		t.Fatalf("the workspace tools did not serve parser.go: %q", out)
	}
	if summary.Provenance.CodexVersion != "codex-cli "+session.CodexVersion() || summary.Provenance.ModelRequested != model || !summary.Complete {
		t.Fatalf("summary %+v", summary)
	}
	applied, err := Apply(context.Background(), ApplyOptions{Repo: repo, ResultDir: r.output})
	if err != nil {
		t.Fatal(err)
	}
	if got := gitTest(t, repo, "diff", "--name-status", s.Manifest.BaseCommit, applied.Commit); got != "D\tdeleted.txt\nM\tparser.go\nA\tparser_test.go" {
		t.Fatalf("applied change:\n%s", got)
	}
	// A published change is immutable.
	again := codextest.NewModel(t, codextest.Steps(codextest.Say(done))...)
	r.opts.Codex, r.opts.Auth = codextest.Launcher(t, again), io.NopCloser(bytes.NewReader(codextest.Auth()))
	if _, err := runInRuntime(context.Background(), r.opts); err == nil {
		t.Fatal("overwrote a published change")
	}
}

// Edit-only means the working copy and nothing else. The model tries every
// way out, through the workspace tools and through tools Codex would offer
// on its own: the staged credential beside the workspace, a sibling of the
// workspace, the session's temporary directory, /tmp, the repository's .git,
// Codex's file-edit tool and a shell. The workspace server refuses each
// before anything changes and Codex offers neither of its own, so the change
// is published with the edit inside the workspace, and nothing outside it
// exists or changed.
func TestImplementSessionCannotEscapeTheWorkspace(t *testing.T) {
	_, s := snapshotFixture(t)
	tmp := filepath.Join("/tmp", "codextest-"+strconv.Itoa(os.Getpid())+"-"+strconv.FormatInt(time.Now().UnixNano(), 36))
	t.Cleanup(func() { os.Remove(tmp); os.Remove(tmp + "-shell"); os.Remove(tmp + "-patch") })
	var r *implementation
	// session is the live session directory, looked up while Codex runs.
	session := func() string {
		dirs, _ := filepath.Glob(filepath.Join(r.runtime, "jb-session-*"))
		if len(dirs) != 1 {
			t.Errorf("sessions during the run: %v", dirs)
			return r.runtime
		}
		return dirs[0]
	}
	credential := func() []byte { b, _ := os.ReadFile(filepath.Join(session(), "codex", "auth.json")); return b }
	escapes := map[string]codextest.Item{}
	escape := func(name, tool string, args func() map[string]any) codextest.Turn {
		return func(codextest.Request) []codextest.Item {
			escapes[name] = codextest.ToolCall(workspaceServer, tool, args())
			return []codextest.Item{escapes[name]}
		}
	}
	write := func(path string) map[string]any { return map[string]any{"path": path, "content": "escaped\n"} }
	edits := fixtureEdits()
	native := codextest.Patch("*** Begin Patch\n*** Add File: " + tmp + "-patch\n+escaped\n*** End Patch")
	shell := codextest.FunctionCall("exec_command", map[string]any{"cmd": "touch " + tmp + "-shell"})
	var during []byte
	turns := append(codextest.Steps(edits...),
		escape("staged credential", "write", func() map[string]any { return write("../codex/auth.json") }),
		escape("staged credential by path", "delete", func() map[string]any {
			return map[string]any{"path": filepath.Join(session(), "codex", "auth.json")}
		}),
		escape("workspace sibling", "write", func() map[string]any { return write("../escaped.txt") }),
		escape("session temporary directory", "write", func() map[string]any { return write(filepath.Join(session(), "tmp", "escaped.txt")) }),
		escape("/tmp", "write", func() map[string]any { return write(tmp) }),
		escape(".git", "write", func() map[string]any { return write(".git/hooks/post-checkout") }),
	)
	turns = append(turns, codextest.Steps(native, shell)...)
	turns = append(turns, func(codextest.Request) []codextest.Item {
		during = credential()
		return []codextest.Item{codextest.Say(done)}
	})
	r = newImplementation(t, turns...)
	summary, err := r.run(t, s)
	r.landed(t, edits, err)
	for name, item := range escapes {
		if out, _ := r.model.Output(item); !strings.Contains(out, "outside the workspace") && !strings.Contains(out, ".git cannot be edited") {
			t.Errorf("the escape to the %s was not refused; the model was told: %s", name, out)
		}
	}
	if !bytes.Equal(during, codextest.Auth()) {
		t.Error("the staged credential changed during the session")
	}
	for _, call := range []codextest.Item{native, shell} {
		if out, _ := r.model.Output(call); !strings.HasPrefix(out, "unsupported") {
			t.Errorf("%s was not refused as unsupported: %s", call["name"], out)
		}
	}
	for _, offered := range r.model.Requests()[0].Offered() {
		if offered == "apply_patch" || offered == "exec_command" || offered == "shell" {
			t.Errorf("Codex offered %s", offered)
		}
	}
	for _, path := range []string{tmp, tmp + "-shell", tmp + "-patch"} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Errorf("Codex wrote outside the workspace: %s", path)
		}
	}
	if len(summary.ChangedFiles) != 3 {
		t.Fatalf("the published change is not exactly the edit inside the workspace: %+v", summary.ChangedFiles)
	}
}

// An item outside the edit-only vocabulary, or a failed turn, stops the
// session and publishes nothing, even after an edit landed.
func TestImplementSessionPublishesNothingWhenRefused(t *testing.T) {
	for name, tc := range map[string]struct {
		last   codextest.Item
		reason string
	}{
		// Codex reports its own MCP resource tool as a call on server
		// "codex": offered by the pinned release, outside the policy.
		"Codex's own tool": {codextest.FunctionCall("list_mcp_resources", map[string]any{}), "outside the edit-only policy"},
		"refused request":  {codextest.Fail("usage_not_included", "The plan does not include this model."), "Codex turn failed"},
		"malformed":        {codextest.Say("{"), ""},
	} {
		t.Run(name, func(t *testing.T) {
			_, s := snapshotFixture(t)
			r := newImplementation(t, codextest.Steps(append(fixtureEdits(), tc.last, codextest.Say(done))...)...)
			_, err := r.run(t, s)
			if err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("got %v, want a refusal naming %q", err, tc.reason)
			}
			if _, statErr := os.Lstat(r.output); !os.IsNotExist(statErr) {
				t.Fatal("a refused session published a change")
			}
		})
	}
}
