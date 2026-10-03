package session

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCheckRequestRefusesAnIncompleteInvocation(t *testing.T) {
	auth := io.NopCloser(strings.NewReader("{}"))
	one, zero := 1, 0
	for name, tc := range map[string]struct {
		model   string
		auth    io.ReadCloser
		timeout time.Duration
		runID   *int
		ok      bool
	}{
		"complete":       {"m", auth, time.Minute, &one, true},
		"without run":    {"m", auth, time.Minute, nil, true},
		"no model":       {"", auth, time.Minute, nil, false},
		"no credential":  {"m", nil, time.Minute, nil, false},
		"no timeout":     {"m", auth, 0, nil, false},
		"negative":       {"m", auth, -time.Second, nil, false},
		"run ID of zero": {"m", auth, time.Minute, &zero, false},
	} {
		if err := CheckRequest(tc.model, tc.auth, tc.timeout, tc.runID); (err == nil) != tc.ok {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestResolvePathsRequiresSeparateDirectoriesAndAnEmptyOutput(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := func(name string) string {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
		return p
	}
	input, runtime, out := dir("input"), dir("runtime"), dir("out")
	dir("runtime/sub")
	if err := os.Symlink(input, filepath.Join(root, "linked-input")); err != nil {
		t.Fatal(err)
	}
	full := dir("full")
	if err := os.WriteFile(filepath.Join(full, "review.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		input, output, runtime string
		ok                     bool
	}{
		"new output":                   {input, filepath.Join(out, "result"), runtime, true},
		"empty output":                 {input, out, runtime, true},
		"output inside input":          {input, filepath.Join(input, "result"), runtime, false},
		"output inside linked input":   {filepath.Join(root, "linked-input"), filepath.Join(input, "result"), runtime, false},
		"runtime inside input":         {input, filepath.Join(out, "result"), filepath.Join(input), false},
		"input inside runtime":         {filepath.Join(runtime), filepath.Join(out, "result"), runtime, false},
		"output holding the runtime":   {input, root, runtime, false},
		"output with a result":         {input, full, runtime, false},
		"output is a file":             {input, file, runtime, false},
		"input does not exist":         {filepath.Join(root, "missing"), filepath.Join(out, "result"), runtime, false},
		"output parent does not exist": {input, filepath.Join(root, "missing", "result"), runtime, false},
	} {
		p, err := ResolvePaths(tc.input, tc.output, tc.runtime)
		if (err == nil) != tc.ok {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if tc.ok && (p.Input != input || p.Runtime != runtime || !strings.HasPrefix(p.Output, out)) {
			t.Errorf("%s: resolved %+v", name, p)
		}
	}
}

// A relative input is related to an absolute output: Rel cannot compare the
// two, so an unnormalized pair would pass as separate.
func TestResolvePathsRelatesRelativeAndAbsolutePaths(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"bundle", "runtime"} {
		if err := os.Mkdir(filepath.Join(root, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(root)
	if _, err := ResolvePaths("bundle", filepath.Join(root, "bundle", "result"), "runtime"); err == nil {
		t.Fatal("an output inside a relative input was accepted")
	}
	if _, err := ResolvePaths("bundle", "bundle/result", filepath.Join(root, "runtime")); err == nil {
		t.Fatal("a relative output inside a relative input was accepted")
	}
	p, err := ResolvePaths("bundle", "result", "runtime")
	if err != nil {
		t.Fatal(err)
	}
	if p.Input != filepath.Join(root, "bundle") || p.Output != filepath.Join(root, "result") || p.Runtime != filepath.Join(root, "runtime") {
		t.Fatalf("resolved %+v", p)
	}
}

func TestPublishRenamesTheWholeResultIntoPlace(t *testing.T) {
	root := t.TempDir()
	input, runtime := filepath.Join(root, "input"), filepath.Join(root, "runtime")
	for _, d := range []string{input, runtime} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	p, err := ResolvePaths(input, filepath.Join(root, "result"), runtime)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Publish(map[string][]byte{"a.json": []byte("{}\n"), "b.md": []byte("# b\n")}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(p.Output)
	if err != nil || len(entries) != 2 {
		t.Fatalf("published %v, %v", entries, err)
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s: %v %v", e.Name(), info.Mode(), err)
		}
	}
	// No stage is left beside the result, and a second publication cannot
	// replace the first.
	siblings, err := os.ReadDir(root)
	if err != nil || len(siblings) != 3 {
		t.Fatalf("beside the result: %v %v", siblings, err)
	}
	if err := p.Publish(map[string][]byte{"a.json": []byte("[]\n")}); err == nil {
		t.Fatal("a published result was replaced")
	}
	if b, _ := os.ReadFile(filepath.Join(p.Output, "a.json")); string(b) != "{}\n" {
		t.Fatalf("a.json is %q", b)
	}

	q, err := ResolvePaths(input, filepath.Join(root, "other"), runtime)
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Publish(map[string][]byte{"../escape": nil}); err == nil {
		t.Fatal("published a file outside the result")
	}
	if _, err := os.Stat(q.Output); !os.IsNotExist(err) {
		t.Fatalf("a refused publication left %s: %v", q.Output, err)
	}
}
