package session_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/session"
	"github.com/concourse/concourse/agent/session/codextest"
)

// TestMain lets this test binary be the tool server Codex starts: run as
// "serve-tools <pidfile>" it records its PID and serves a fixed file set
// through session.ServeTools, the same server every workload uses.
func TestMain(m *testing.M) {
	if len(os.Args) == 3 && os.Args[1] == "serve-tools" {
		os.WriteFile(os.Args[2], []byte(strconv.Itoa(os.Getpid())), 0o600)
		files := map[string]string{"notes.txt": "first line\nsecond line\n"}
		text := session.TextFiles{
			Paths: func() ([]string, error) { return []string{"notes.txt"}, nil },
			Read: func(name string) ([]byte, error) {
				if data, ok := files[name]; ok {
					return []byte(data), nil
				}
				return nil, errors.New("no such file")
			},
		}
		tools := session.TextTools(session.TextToolDescriptions{List: "List files.", Read: "Read a file.", Search: "Search files."})
		if err := session.ServeTools("fixture", tools, text.Call, os.Stdin, os.Stdout); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// A session's model name is one the pinned release's bundled catalog knows,
// so Codex runs it with that model's real tool set.
const model = "gpt-5.5"

type run struct {
	runtime, codex, pidfile string
	model                   *codextest.Model
}

func newRun(t *testing.T, turns ...codextest.Turn) *run {
	t.Helper()
	m := codextest.NewModel(t, turns...)
	return &run{runtime: t.TempDir(), codex: codextest.Launcher(t, m), pidfile: filepath.Join(t.TempDir(), "tools.pid"), model: m}
}

func (r *run) open(t *testing.T) *session.Session {
	t.Helper()
	s, err := session.Open(context.Background(), session.Options{RuntimeDir: r.runtime, Provider: session.Codex{}, Executable: r.codex, Auth: bytes.NewReader(codextest.Auth())})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func (r *run) policy(t *testing.T, s *session.Session) session.Policy {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := s.Mkdir("workspace")
	if err != nil {
		t.Fatal(err)
	}
	return session.Policy{Model: model, WorkDir: workspace, LastMessage: filepath.Join(s.Dir, "last-message"),
		Tools: session.ToolServer{Name: "fixture", Command: self, Args: []string{"serve-tools", r.pidfile}, Tools: []string{"list", "read", "search"}}}
}

// toolServerExits waits for the tool server Codex started to exit. Codex
// starts it in a process group of its own, so it is not killed with Codex:
// it exits because its standard input, Codex's end of the MCP pipe, closes.
func (r *run) toolServerExits(t *testing.T) {
	t.Helper()
	b, err := os.ReadFile(r.pidfile)
	if err != nil {
		t.Fatalf("Codex never started the tool server: %v", err)
	}
	pid, _ := strconv.Atoi(string(b))
	for deadline := time.Now().Add(10 * time.Second); running(pid); time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the tool server (pid %d) outlived its session", pid)
		}
	}
}

// running reports whether pid is a live process. An exited process whose
// parent died can stay a zombie under an init that does not reap, as in a
// container; it is not running.
func running(pid int) bool {
	if stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat"); err == nil {
		_, rest, _ := strings.Cut(string(stat), ") ")
		return !strings.HasPrefix(rest, "Z")
	}
	return syscall.Kill(pid, 0) == nil
}

func empty(t *testing.T, dir string) {
	t.Helper()
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("%s holds %v (%v)", dir, entries, err)
	}
}

// One real turn end to end: the credential is staged privately, the pinned
// release is checked, Codex calls the policy's tool server and presents the
// staged credential to the model, every item reaches allow, and Close
// destroys the credential with everything Codex wrote beside it.
func TestSessionRunsOneCodexTurn(t *testing.T) {
	read := codextest.ToolCall("fixture", "read", map[string]any{"path": "notes.txt"})
	r := newRun(t, codextest.Steps(read, codextest.Say("the notes have two lines"))...)
	s := r.open(t)
	if s.Version != "codex-cli "+session.CodexVersion() {
		t.Fatalf("version %q", s.Version)
	}
	for path, mode := range map[string]os.FileMode{s.Dir: 0o700, s.Home: 0o700, filepath.Join(s.Dir, "tmp"): 0o700, filepath.Join(s.Home, "auth.json"): 0o600} {
		if st, err := os.Stat(path); err != nil || st.Mode().Perm() != mode {
			t.Fatalf("%s: %v %v, want %v", path, st.Mode(), err, mode)
		}
	}
	if staged, _ := os.ReadFile(filepath.Join(s.Home, "auth.json")); !bytes.Equal(staged, codextest.Auth()) {
		t.Fatal("the staged credential is not the owner's")
	}
	var seen []string
	p := r.policy(t, s)
	err := s.Run(context.Background(), p, "Summarise notes.txt.", func(e session.Event) error {
		seen = append(seen, string(e.Item)+":"+e.Server+"/"+e.Tool)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(seen, " "); !strings.Contains(got, "mcp_tool_call:fixture/read") || !strings.HasSuffix(got, "agent_message:/") {
		t.Fatalf("items %s", got)
	}
	if out, _ := r.model.Output(read); !strings.Contains(out, "1: first line") {
		t.Fatalf("the tool server's answer did not reach the model: %q", out)
	}
	if first, _ := json.Marshal(r.model.Requests()[0].Input); !bytes.Contains(first, []byte("Summarise notes.txt.")) {
		t.Fatal("the prompt did not reach the model")
	}
	if last, _ := os.ReadFile(p.LastMessage); string(last) != "the notes have two lines" {
		t.Fatalf("last message %q", last)
	}
	r.toolServerExits(t)
	// Codex keeps state, logs and a helper directory beside the credential;
	// all of it is the session's and goes with it.
	if entries, _ := os.ReadDir(s.Home); len(entries) < 2 {
		t.Fatalf("Codex wrote nothing beside the credential: %v", entries)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	empty(t, r.runtime)
}

// A refused item stops Codex at once: no further model turn is served and
// its tool server does not outlive the refusal.
func TestSessionStopsCodexOnARefusedItem(t *testing.T) {
	listed := codextest.ToolCall("fixture", "list", map[string]any{})
	r := newRun(t, codextest.Steps(listed, codextest.Say("unreachable"))...)
	s := r.open(t)
	refusal := errors.New("refused by policy")
	err := s.Run(context.Background(), r.policy(t, s), "List.", func(e session.Event) error {
		if e.Item == session.ItemToolCall {
			return refusal
		}
		return nil
	})
	if !errors.Is(err, refusal) {
		t.Fatalf("got %v", err)
	}
	if n := len(r.model.Requests()); n != 1 {
		t.Fatalf("Codex went on to model request %d after the refusal", n)
	}
	r.toolServerExits(t)
}

// Everything Codex reports as a failure fails the turn: a model request the
// backend refuses, an error item, and a tool server that cannot start.
func TestSessionFailsWhenCodexReportsFailure(t *testing.T) {
	for name, tc := range map[string]struct {
		turns  []codextest.Turn
		model  string
		tools  string
		reason string
	}{
		"refused request": {turns: codextest.Steps(codextest.Fail("usage_not_included", "The plan does not include this model.")), model: model, reason: "Codex turn failed"},
		// An unknown model is an error item before the turn starts.
		"error item":     {turns: codextest.Steps(codextest.Say("unreachable")), model: "no-such-model", reason: "Codex reported an error"},
		"no tool server": {model: model, tools: "/nonexistent/tools", reason: "Codex failed"},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRun(t, tc.turns...)
			s := r.open(t)
			p := r.policy(t, s)
			p.Model = tc.model
			if tc.tools != "" {
				p.Tools.Command = tc.tools
			}
			err := s.Run(context.Background(), p, "Go.", func(session.Event) error { return nil })
			if err == nil || !strings.HasPrefix(err.Error(), tc.reason) {
				t.Fatalf("got %v, want %q", err, tc.reason)
			}
		})
	}
}

// A session whose context ends is stopped with its whole process group,
// however long the model would have taken.
func TestSessionTimeoutKillsCodexAndItsToolServer(t *testing.T) {
	// The context ends only once Codex, and with it the tool server, is up
	// and waiting on the model, so startup time is never part of the check.
	waiting := make(chan struct{})
	var once sync.Once
	r := newRun(t, func(req codextest.Request) []codextest.Item {
		once.Do(func() { close(waiting) })
		<-req.Done
		return nil
	})
	s := r.open(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stopped time.Time
	go func() {
		select {
		case <-waiting:
		case <-time.After(time.Minute):
		}
		stopped = time.Now()
		cancel()
	}()
	err := s.Run(ctx, r.policy(t, s), "Wait.", func(session.Event) error { return nil })
	if !errors.Is(err, context.Canceled) || time.Since(stopped) > 10*time.Second {
		t.Fatalf("got %v, %v after the context ended", err, time.Since(stopped))
	}
	select {
	case <-waiting:
	default:
		t.Fatal("Codex never reached the model")
	}
	r.toolServerExits(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	empty(t, r.runtime)
}

func TestRunRefusesAProviderThatCannotStart(t *testing.T) {
	r := newRun(t)
	s := r.open(t)
	if err := os.Remove(r.codex); err != nil {
		t.Fatal(err)
	}
	if err := s.Run(context.Background(), r.policy(t, s), "Go.", func(session.Event) error { return nil }); err == nil || err.Error() != "could not start Codex" {
		t.Fatalf("got %v", err)
	}
}

// Open leaves nothing behind when it refuses: not the credential, not the
// session directory, and Codex never talks to the model.
func TestOpenRefusesAndLeavesNothing(t *testing.T) {
	apiKey := `{"OPENAI_API_KEY":"k","tokens":{"access_token":"a","refresh_token":"r","id_token":"i"}}`
	for name, tc := range map[string]struct {
		executable func(codex string) string
		auth       string
		reason     string
	}{
		"another release":    {executable: func(string) string { return "/bin/echo" }, auth: string(codextest.Auth()), reason: "worker requires codex-cli " + session.CodexVersion()},
		"no version":         {executable: func(string) string { return "/usr/bin/false" }, auth: string(codextest.Auth()), reason: "could not determine Codex version"},
		"relative path":      {executable: func(string) string { return "codex" }, auth: string(codextest.Auth()), reason: "provider executable must have an absolute path"},
		"API key credential": {executable: func(c string) string { return c }, auth: apiKey, reason: "valid Codex subscription auth.json required"},
		"oversized":          {executable: func(c string) string { return c }, auth: strings.Repeat(" ", 64<<10) + string(codextest.Auth()), reason: "valid Codex subscription auth.json required"},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRun(t)
			_, err := session.Open(context.Background(), session.Options{RuntimeDir: r.runtime, Provider: session.Codex{}, Executable: tc.executable(r.codex), Auth: strings.NewReader(tc.auth)})
			if err == nil || !strings.HasPrefix(err.Error(), tc.reason) {
				t.Fatalf("got %v, want %q", err, tc.reason)
			}
			empty(t, r.runtime)
			if len(r.model.Requests()) != 0 {
				t.Fatal("a refused session reached the model")
			}
		})
	}
}

// Open reports a cancelled context rather than blaming the credential.
func TestOpenReportsCancellation(t *testing.T) {
	r := newRun(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := session.Open(ctx, session.Options{RuntimeDir: r.runtime, Provider: session.Codex{}, Executable: r.codex, Auth: bytes.NewReader(codextest.Auth())})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	empty(t, r.runtime)
}

// Bound closes the credential stream when the session's time runs out, so a
// handoff still waiting cannot outlive the session; a session stopped in time
// leaves the stream to its owner.
func TestBoundClosesTheCredentialStreamOnExpiry(t *testing.T) {
	closed := func(r *os.File, within time.Duration) bool {
		for deadline := time.Now().Add(within); time.Now().Before(deadline); {
			r.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
			if _, err := r.Read(make([]byte, 1)); errors.Is(err, os.ErrClosed) {
				return true
			}
		}
		return false
	}
	pipe := func() *os.File {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { r.Close(); w.Close() })
		return r
	}
	expiring := pipe()
	_, stop := session.Bound(context.Background(), 50*time.Millisecond, expiring)
	if !closed(expiring, 5*time.Second) {
		t.Error("an expired session left the credential stream open")
	}
	stop()
	for range 50 {
		stopped := pipe()
		_, stop := session.Bound(context.Background(), time.Hour, stopped)
		stop()
		if closed(stopped, 40*time.Millisecond) {
			t.Fatal("stopping a session closed the credential stream")
		}
	}
}

func addFile(path, line string) string {
	return "*** Begin Patch\n*** Add File: " + path + "\n+" + line + "\n*** End Patch"
}

// Open derives the session's model catalog from the one the pinned release
// bundles: every model, with its file-edit tool removed and nothing else
// changed.
func TestOpenDerivesACatalogWithoutTheEditTool(t *testing.T) {
	r := newRun(t)
	s := r.open(t)
	derived, err := os.ReadFile(filepath.Join(s.Home, session.CatalogFile))
	if err != nil {
		t.Fatal(err)
	}
	bundled, err := exec.Command(codextest.Binary(t), "debug", "models", "--bundled").Output()
	if err != nil {
		t.Fatal(err)
	}
	var want, got struct{ Models []map[string]any }
	if json.Unmarshal(bundled, &want) != nil || json.Unmarshal(derived, &got) != nil || len(want.Models) == 0 || len(got.Models) != len(want.Models) {
		t.Fatalf("derived %d models from %d", len(got.Models), len(want.Models))
	}
	for i, m := range got.Models {
		if v, ok := m["apply_patch_tool_type"]; !ok || v != nil {
			t.Errorf("%v still offers apply_patch: %v", m["slug"], v)
		}
		want.Models[i]["apply_patch_tool_type"] = nil
	}
	if a, b := fmt.Sprint(want.Models), fmt.Sprint(got.Models); a != b {
		t.Error("the derived catalog changed more than the file-edit tool")
	}
}

// No policy gives Codex a tool that writes or executes. Its own file-edit
// tool is not offered at all, so a model that calls it anyway, or a shell,
// gets an unsupported call; and every escape is attempted for real and
// checked on disk: the working directory, the credential beside it, the
// session's temporary directory and /tmp all stay as they were.
func TestCodexHasNoToolThatWritesOrExecutes(t *testing.T) {
	var s *session.Session
	tmp := filepath.Join("/tmp", "codextest-"+strconv.Itoa(os.Getpid())+"-"+strconv.FormatInt(time.Now().UnixNano(), 36))
	t.Cleanup(func() { os.Remove(tmp); os.Remove(tmp + "-shell") })
	calls := map[string]codextest.Item{}
	call := func(name string, item func() codextest.Item) codextest.Turn {
		return func(codextest.Request) []codextest.Item {
			calls[name] = item()
			return []codextest.Item{calls[name]}
		}
	}
	turns := []codextest.Turn{
		call("inside", func() codextest.Item { return codextest.Patch(addFile("added.txt", "inside")) }),
		call("/tmp", func() codextest.Item { return codextest.Patch(addFile(tmp, "escaped")) }),
		call("relative", func() codextest.Item { return codextest.Patch(addFile("../escaped.txt", "escaped")) }),
		call("session tmp", func() codextest.Item {
			return codextest.Patch(addFile(filepath.Join(s.Dir, "tmp", "escaped.txt"), "escaped"))
		}),
		call("credential", func() codextest.Item {
			return codextest.Patch("*** Begin Patch\n*** Delete File: " + filepath.Join(s.Home, "auth.json") + "\n*** End Patch")
		}),
		call("exec_command", func() codextest.Item {
			return codextest.FunctionCall("exec_command", map[string]any{"cmd": "touch " + tmp + "-shell"})
		}),
		call("shell", func() codextest.Item {
			return codextest.FunctionCall("shell", map[string]any{"command": []string{"touch", tmp + "-shell"}})
		}),
	}
	turns = append(turns, codextest.Steps(codextest.Say("done"))...)
	r := newRun(t, turns...)
	s = r.open(t)
	p := r.policy(t, s)
	var items []session.ItemKind
	if err := s.Run(context.Background(), p, "Edit.", func(e session.Event) error { items = append(items, e.Item); return nil }); err != nil {
		t.Fatal(err)
	}
	for _, offered := range r.model.Requests()[0].Offered() {
		if offered == "apply_patch" || offered == "exec_command" || offered == "shell" || offered == "write_stdin" {
			t.Errorf("Codex offered %s", offered)
		}
	}
	for name, item := range calls {
		if out, _ := r.model.Output(item); !strings.HasPrefix(out, "unsupported") {
			t.Errorf("%s was not refused as unsupported: %s", name, out)
		}
	}
	empty(t, p.WorkDir)
	for _, path := range []string{tmp, tmp + "-shell", filepath.Join(s.Dir, "escaped.txt"), filepath.Join(s.Dir, "tmp", "escaped.txt")} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Errorf("Codex wrote outside its working directory: %s", path)
		}
	}
	if staged, _ := os.ReadFile(filepath.Join(s.Home, "auth.json")); !bytes.Equal(staged, codextest.Auth()) {
		t.Error("Codex changed the staged credential")
	}
	if slices.Contains(items, session.ItemOther) {
		t.Errorf("Codex reported an item outside the vocabulary: %v", items)
	}
}
