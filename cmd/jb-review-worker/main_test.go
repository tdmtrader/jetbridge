package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/capture"
	"github.com/concourse/concourse/agent/review"
	"github.com/concourse/concourse/agent/session"
	"github.com/concourse/concourse/agent/session/codextest"
)

// The worker is tested as the gate runs it: the built executable, its exit
// status and everything it prints.
func TestMain(m *testing.M) { codextest.Main(m) }

func worker(t *testing.T, stdin []byte, args ...string) (string, int) {
	t.Helper()
	c := exec.Command(codextest.Worker(t), args...)
	c.Stdin = bytes.NewReader(stdin)
	var out bytes.Buffer
	c.Stdout, c.Stderr = &out, &out
	err := c.Run()
	if exit, ok := err.(*exec.ExitError); ok {
		return out.String(), exit.ExitCode()
	}
	if err != nil {
		t.Fatal(err)
	}
	return out.String(), 0
}

// The review gate's verdict: any finding at high or blocker fails the gate,
// naming each blocking finding's id, severity and title; medium and low pass.
func TestVerdictRefusesHighAndBlockerAndPassesMediumAndLow(t *testing.T) {
	for name, tc := range map[string]struct {
		exit    int
		says    []string
		notSays []string
	}{
		"review-blocker.json":     {1, []string{"f-002 [blocker] Gate verdicts replay after a restart (head/atc/landing/gates.go:12)", "review gate failed: 1 finding(s) at or above high"}, []string{"Comment typo"}},
		"review-high.json":        {1, []string{"f-001 [high] Overdue Run is never cancelled", "review gate failed: 1 finding(s) at or above high"}, []string{"Long function"}},
		"review-medium-low.json":  {0, []string{"review gate passed: verdict findings, 2 finding(s), none at or above high"}, nil},
		"review-no-findings.json": {0, []string{"review gate passed: verdict no_findings, 0 finding(s)"}, nil},
		"review-incomplete.json":  {3, []string{"the review is incomplete", "Changed files not listed as reviewed", "review gate: no verdict"}, []string{"review gate passed", "review gate failed"}},
	} {
		t.Run(name, func(t *testing.T) {
			out, exit := worker(t, nil, "verdict", "--report", filepath.Join("testdata", name), "--block-at", "high")
			if exit != tc.exit {
				t.Fatalf("exit %d, want %d:\n%s", exit, tc.exit, out)
			}
			for _, s := range tc.says {
				if !strings.Contains(out, s) {
					t.Errorf("output does not say %q:\n%s", s, out)
				}
			}
			for _, s := range tc.notSays {
				if strings.Contains(out, s) {
					t.Errorf("output names %q, which does not block:\n%s", s, out)
				}
			}
		})
	}
}

// An incomplete review rejects on what it did find: a finding at or above the
// floor fails it like a complete one.
func TestVerdictFailsAnIncompleteReviewOnABlockingFinding(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "review-incomplete.json"))
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Replace(string(body), `"severity": "low"`, `"severity": "high"`, 1)
	if text == string(body) {
		t.Fatal("the incomplete fixture has no low finding to raise")
	}
	path := filepath.Join(t.TempDir(), "review.json")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	out, exit := worker(t, nil, "verdict", "--report", path, "--block-at", "high")
	if exit != 1 || !strings.Contains(out, "review gate failed: 1 finding(s) at or above high") {
		t.Fatalf("exit %d, want 1:\n%s", exit, out)
	}
}

// A report the worker could not have published, or a floor verdict cannot
// apply, is no verdict: neither a pass nor the failed answer (exit 1) that
// rejects a candidate.
func TestVerdictGivesNoVerdictOnAReportItCannotTrust(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "review-high.json"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for name, text := range map[string]string{
		"verdict does not match": strings.Replace(string(body), `"verdict": "findings"`, `"verdict": "no_findings"`, 1),
		"not JSON":               "{",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, "review.json")
			if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
				t.Fatal(err)
			}
			out, exit := worker(t, nil, "verdict", "--report", path)
			if exit != 3 || !strings.Contains(out, "review report") || !strings.Contains(out, "review gate: no verdict") {
				t.Fatalf("exit %d:\n%s", exit, out)
			}
		})
	}
	if out, exit := worker(t, nil, "verdict", "--report", filepath.Join("testdata", "review-high.json"), "--block-at", "critical"); exit != 3 || !strings.Contains(out, "unknown severity floor") {
		t.Fatalf("an unknown floor: exit %d:\n%s", exit, out)
	}
}

// --auth-file never prints the credential, whatever it refuses, and never
// takes it alongside another credential source.
func TestAuthFileNeverPrintsTheCredential(t *testing.T) {
	write := func(dir, name string, mode os.FileMode) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, codextest.Auth(), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		return path
	}
	disk := t.TempDir()
	diskIsMemory := session.RequireMemoryRuntime(disk) == nil
	private := write(disk, "auth.json", 0o600)
	// Where there is a tmpfs, the readable file is on it, so the mode check is
	// the one that refuses it; elsewhere the tmpfs check refuses first.
	sharedDir, readableSays := disk, "--auth-file must be on a private tmpfs mount"
	if runtime.GOOS == "linux" {
		shm, err := os.MkdirTemp("/dev/shm", "auth-file-test-")
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(shm)
		sharedDir, readableSays = shm, "--auth-file must be a regular file readable only by its owner (mode 0600)"
	}
	shared := write(sharedDir, "shared.json", 0o644)
	common := []string{"--input", t.TempDir(), "--output", filepath.Join(t.TempDir(), "report"), "--model", "gpt-5.5"}
	for name, tc := range map[string]struct {
		args []string
		says string
	}{
		"a relative path":    {[]string{"--auth-file", "auth.json"}, "--auth-file must be an absolute path"},
		"a disk-backed file": {[]string{"--auth-file", private}, "--auth-file must be on a private tmpfs mount"},
		"a readable file":    {[]string{"--auth-file", shared}, readableSays},
		"two sources":        {[]string{"--auth-file", private, "--auth-stdin"}, "exactly one of --auth-stdin, --auth-socket or --auth-file"},
		"three sources":      {[]string{"--auth-file", private, "--auth-socket", "/dev/shm/x.sock", "--auth-stdin"}, "exactly one of"},
	} {
		t.Run(name, func(t *testing.T) {
			if name == "a disk-backed file" && diskIsMemory {
				t.Skip("this machine's temporary directory is itself a tmpfs")
			}
			out, exit := worker(t, codextest.Auth(), append(common, tc.args...)...)
			if exit == 0 || !strings.Contains(out, tc.says) {
				t.Fatalf("exit %d, want a refusal saying %q:\n%s", exit, tc.says, out)
			}
			if strings.Contains(out, codextest.Secret) {
				t.Fatalf("the credential was printed:\n%s", out)
			}
		})
	}
	if _, err := os.Stat(shared); err != nil {
		t.Fatalf("a refused auth file is left for its writer to remove: %v", err)
	}
}

// On Linux, the gate's whole path: the credential in an owner-only file on
// /dev/shm, a review by the pinned Codex against a scripted model, a
// published report, and the file gone. Nothing printed or published holds
// the credential.
func TestAuthFileRunsAReviewAndConsumesTheFile(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("--auth-file needs a tmpfs, which only the Linux worker has; the refusal test runs everywhere")
	}
	shm, err := os.MkdirTemp("/dev/shm", "auth-file-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(shm)
	path := filepath.Join(shm, "auth.json")
	if err := os.WriteFile(path, codextest.Auth(), 0o600); err != nil {
		t.Fatal(err)
	}
	runtimeDir := filepath.Join(shm, "runtime")
	if err := os.Mkdir(runtimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	bundle := captured(t)
	assessment := `{"summary":"Inspected the change.","complete":true,"reviewed_files":["parser.go"],"limitations":[],"findings":[]}`
	model := codextest.NewModel(t, codextest.Steps(codextest.Say(assessment))...)
	output := filepath.Join(t.TempDir(), "report")
	out, exit := worker(t, nil, "--input", bundle, "--output", output, "--runtime-dir", runtimeDir, "--codex", codextest.Launcher(t, model),
		"--model", "gpt-5.5", "--auth-file", path, "--timeout", "1m")
	if exit != 0 {
		t.Fatalf("exit %d:\n%s", exit, out)
	}
	if strings.Contains(out, codextest.Secret) {
		t.Fatalf("the credential was printed:\n%s", out)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("the auth file outlived the worker: %v", err)
	}
	if entries, _ := os.ReadDir(runtimeDir); len(entries) != 0 {
		t.Fatalf("the session survived: %v", entries)
	}
	for _, name := range []string{"review.json", "review.md"} {
		b, err := os.ReadFile(filepath.Join(output, name))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(b, []byte(codextest.Secret)) {
			t.Fatalf("the credential reached %s", name)
		}
	}
	verdict, exit := worker(t, nil, "verdict", "--report", filepath.Join(output, "review.json"))
	if exit != 0 || !strings.Contains(verdict, "review gate passed: verdict no_findings") {
		t.Fatalf("exit %d:\n%s", exit, verdict)
	}
}

// captured is a review bundle of a one-file change, as jb review capture
// makes it.
func captured(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	git := func(args ...string) string {
		c := exec.Command("git", append([]string{"-C", repo, "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false",
			"-c", "user.email=review@example.test", "-c", "user.name=Review fixture"}, args...)...)
		c.Env = capture.GitEnvironment()
		b, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, b)
		}
		return strings.TrimSpace(string(b))
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(repo, "parser.go"), []byte("package parser\nfunc First(s string) byte { return s[0] }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-qm", "base")
	base := git("rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(repo, "parser.go"), []byte("package parser\nfunc First(s string) byte { return s[1] }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("commit", "-qam", "head")
	b, err := review.Capture(context.Background(), review.CaptureOptions{Repo: repo, Base: base, Head: "HEAD", Output: filepath.Join(t.TempDir(), "bundle")})
	if err != nil {
		t.Fatal(err)
	}
	return b.Dir
}
