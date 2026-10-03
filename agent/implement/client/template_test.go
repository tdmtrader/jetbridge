package client

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/capture"
	"github.com/concourse/concourse/agent/implement"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/atctest"
	"sigs.k8s.io/yaml"
)

const (
	pinnedWorker    = atctest.Pin
	validateImage   = "registry.example/toolchain@sha256:fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	installedCheck  = "go test ./..."
	validateTaskArg = "validate"
	// validatedRun is the Run ID the ATC would fill into the script's run_id.
	validatedRun = 41
)

// installedTemplate reads the template the way fly set-pipeline installs it:
// every operator variable filled, run_id left for the ATC.
func installedTemplate(t *testing.T) atc.Config {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "implement-template.yml"))
	if err != nil {
		t.Fatal(err)
	}
	text := strings.NewReplacer("((review_worker_image))", pinnedWorker, "((implement_model))", "some-model",
		"((validate_image))", validateImage, "((validate_command))", installedCheck).Replace(string(data))
	if strings.Contains(strings.ReplaceAll(text, "((run_id))", ""), "((") {
		t.Fatal("the template has an operator variable this test does not know")
	}
	var config atc.Config
	if err := yaml.Unmarshal([]byte(text), &config); err != nil {
		t.Fatal(err)
	}
	return config
}

func templateTask(t *testing.T, config atc.Config, name string) *atc.TaskStep {
	t.Helper()
	for _, step := range config.Jobs[0].PlanSequence {
		if task, ok := step.Config.(*atc.TaskStep); ok && task.Name == name {
			return task
		}
	}
	t.Fatalf("template has no %s task", name)
	return nil
}

// The installed template is what the adapter talks to: its input and result
// names must be the adapter's, and only its change producer may run the
// pinned worker image, or the platform refuses the credential handoff.
func TestInstalledTemplateMatchesTheWorkload(t *testing.T) {
	config := installedTemplate(t)
	if !config.Template {
		t.Fatal("implement template is not a template")
	}
	declarations, err := atc.RunTaskDeclarations(config)
	if err != nil {
		t.Fatal(err)
	}
	if len(declarations) != 2 {
		t.Fatalf("expected the author and validate result producers, got %+v", declarations)
	}
	results := map[string]atc.RunTaskDeclaration{}
	for _, d := range declarations {
		if d.Result == nil || len(d.Inputs) != 1 || d.Inputs[0] != (atc.RunInput{Name: SnapshotInput, Input: "source"}) {
			t.Fatalf("a producer does not take %q on source: %+v", SnapshotInput, d)
		}
		results[d.Result.Name] = d
	}
	author, validate := results[ChangeResult], results[ValidationResult]
	if author.TaskID == "" || validate.TaskID == "" {
		t.Fatalf("template does not publish %q and %q: %+v", ChangeResult, ValidationResult, declarations)
	}
	if got := atc.RunTaskImage(config, author.TaskID); got != "docker:///"+pinnedWorker {
		t.Fatalf("the change producer runs %q, not the pinned worker image", got)
	}
	if got := atc.RunTaskImage(config, validate.TaskID); got != "docker:///"+validateImage {
		t.Fatalf("the validation producer runs %q, not the operator's validate image", got)
	}
	script := strings.Join(templateTask(t, config, "author").Config.Run.Args, "\n")
	for _, want := range []string{"jb-review-worker implement", "--input source/snapshot", "--auth-socket /dev/shm/jb-review/auth.sock", "some-model", "--run-id=((run_id))", "change/report/change.patch change/report/summary.json change/"} {
		if !strings.Contains(script, want) {
			t.Fatalf("author task does not run %q:\n%s", want, script)
		}
	}
	// validate reads the author's output and nothing the agent wrote decides
	// what it runs: the command is the operator's, passed as an argument.
	task := templateTask(t, config, "validate")
	var inputs []string
	for _, input := range task.Config.Inputs {
		inputs = append(inputs, input.Name)
	}
	if strings.Join(inputs, ",") != "source,change" || task.ConfigPath != "" || len(task.Vars) != 0 || task.Config.Run.Path != "/bin/sh" {
		t.Fatalf("validate task does not read source and change through its inline script: %+v", task)
	}
	args := task.Config.Run.Args
	if len(args) != 5 || args[0] != "-ec" || args[2] != validateTaskArg || args[3] != installedCheck || args[4] != "--run-id=((run_id))" {
		t.Fatalf("validate task does not take the installed command and the admitted Run ID: %q", args)
	}
}

// validateTask is one validate task container: the Run input as the platform
// materializes it, the author's published change, and an empty output.
type validateTask struct {
	dir            string
	snapshot       *implement.Snapshot
	summary, patch []byte
}

// newValidateTask seals a snapshot with an executable check script and
// publishes the change the worker would make to it.
func newValidateTask(t *testing.T) *validateTask {
	t.Helper()
	base := implement.Tree{
		"parser.go": {Data: []byte("package parser\nfunc First(s string) byte { return s[1] }\n"), Mode: "100644"},
		"check.sh":  {Data: []byte("#!/bin/sh\ngrep -q 's\\[0\\]' parser.go\n"), Mode: "100755"},
	}
	edited := implement.Tree{"check.sh": base["check.sh"], "parser.go": {Data: []byte("package parser\nfunc First(s string) byte { return s[0] }\n"), Mode: "100644"}}
	return newValidateTaskFrom(t, base, edited)
}

// newValidateTaskFrom seals base as a snapshot and publishes the change to
// edited as the worker would, laid out as the validate task receives it.
func newValidateTaskFrom(t *testing.T, base, edited implement.Tree) *validateTask {
	t.Helper()
	sealed := filepath.Join(t.TempDir(), "snapshot")
	if err := os.MkdirAll(filepath.Join(sealed, "base"), 0o700); err != nil {
		t.Fatal(err)
	}
	brief := "Return the first byte.\n"
	manifest := implement.Manifest{Version: implement.InputVersion, BaseCommit: strings.Repeat("a", 40), BriefDigest: capture.Digest([]byte(brief)), Files: []capture.File{}}
	names := make([]string, 0, len(base))
	for name := range base {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		file := base[name]
		if err := os.WriteFile(filepath.Join(sealed, "base", name), file.Data, 0o600); err != nil {
			t.Fatal(err)
		}
		manifest.Files = append(manifest.Files, capture.File{Side: "base", Path: name, Mode: file.Mode, Digest: capture.Digest(file.Data), Size: int64(len(file.Data))})
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sealed, "manifest.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sealed, "brief.md"), []byte(brief), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := implement.LoadSnapshot(sealed)
	if err != nil {
		t.Fatal(err)
	}
	patch, changes, err := implement.Diff(base, edited)
	if err != nil {
		t.Fatal(err)
	}
	run := validatedRun
	built, err := implement.BuildSummary(snapshot, patch, changes, []byte(`{"summary":"Return the first byte.","complete":true,"limitations":[]}`),
		implement.Metadata{RunID: &run, ModelRequested: "test-model", CodexVersion: "codex-cli test", Profile: implement.DefaultProfile()})
	if err != nil {
		t.Fatal(err)
	}
	summary, err := json.Marshal(built)
	if err != nil {
		t.Fatal(err)
	}

	task := &validateTask{dir: t.TempDir(), snapshot: snapshot, summary: summary, patch: patch}
	// The Run input is exactly what submission uploads, below source/.
	var archive bytes.Buffer
	if err := snapshot.WriteRunInputArchive(context.Background(), &archive); err != nil {
		t.Fatal(err)
	}
	reader := tar.NewReader(&archive)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(task.dir, "source", filepath.FromSlash(header.Name))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, body, os.FileMode(header.Mode)); err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range []string{"change", "validation"} {
		if err := os.Mkdir(filepath.Join(task.dir, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	task.setPatch(t, patch)
	return task
}

func (task *validateTask) setPatch(t *testing.T, patch []byte) {
	t.Helper()
	for name, data := range map[string][]byte{implement.PatchFile: patch, implement.SummaryFile: task.summary} {
		if err := os.WriteFile(filepath.Join(task.dir, "change", name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// run executes the template's own validate script, as installed, in the
// task directory and parses what it published.
func (task *validateTask) run(t *testing.T, command string) *implement.Validation {
	t.Helper()
	return task.runWithPath(t, command, "/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin")
}

// runWithPath runs the script with only the tools on path.
func (task *validateTask) runWithPath(t *testing.T, command, path string) *implement.Validation {
	t.Helper()
	script := templateTask(t, installedTemplate(t), "validate").Config.Run.Args[1]
	cmd := exec.Command("/bin/sh", "-ec", script, validateTaskArg, command, "--run-id="+strconv.Itoa(validatedRun))
	cmd.Dir = task.dir
	cmd.Env = []string{"PATH=" + path, "HOME=" + t.TempDir(), "TMPDIR=" + t.TempDir()}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("validate task failed instead of recording the outcome: %v\n%s", err, out)
	}
	entries, err := os.ReadDir(filepath.Join(task.dir, "validation"))
	if err != nil || len(entries) != 1 || entries[0].Name() != implement.ValidationFile {
		t.Fatalf("validate task did not publish exactly %s: %v %v", implement.ValidationFile, entries, err)
	}
	data, err := os.ReadFile(filepath.Join(task.dir, "validation", implement.ValidationFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(task.dir, "validation", implement.ValidationFile)); err != nil {
		t.Fatal(err)
	}
	validation, err := implement.ParseValidation(data)
	if err != nil {
		t.Fatalf("published validation does not parse: %v\n%s", err, data)
	}
	if validation.RunID != validatedRun || validation.Command != command || validation.InputDigest != task.snapshot.Digest {
		t.Fatalf("validation does not name the Run, command and snapshot: %+v", validation)
	}
	return validation
}

// The template's validate script is run here as installed, against the Run
// input's exact layout, so its recording of every outcome is checked where
// POSIX tools exist. It needs git, sha256sum and timeout, as the validate
// image does.
func TestValidateScriptRecordsEveryOutcome(t *testing.T) {
	for _, tool := range []string{"git", "sha256sum", "timeout"} {
		if _, err := exec.LookPath(tool); err != nil {
			if _, err := os.Stat("/sbin/" + tool); err != nil {
				t.Skipf("the validate script needs %s", tool)
			}
		}
	}
	summaryOf := func(task *validateTask) *implement.Summary {
		s, err := implement.ParsePublishedChange(task.summary, task.patch)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}

	t.Run("passed", func(t *testing.T) {
		task := newValidateTask(t)
		// check.sh passes only with the patch applied and its executable
		// bit, which the Run input does not carry, restored.
		v := task.run(t, "./check.sh")
		if v.Outcome != implement.ValidationPassed || !v.Applied || v.ExitCode == nil || *v.ExitCode != 0 {
			t.Fatalf("passing command was not recorded as passed: %+v", v)
		}
		if err := v.Attests(summaryOf(task), task.patch); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("failed", func(t *testing.T) {
		task := newValidateTask(t)
		command := `printf 'tab\there "quoted" back\\slash caf\303\251\001\n'; echo "FAIL: parser" >&2; exit 3`
		v := task.run(t, command)
		if v.Outcome != implement.ValidationFailed || !v.Applied || v.ExitCode == nil || *v.ExitCode != 3 {
			t.Fatalf("failing command was not recorded as failed: %+v", v)
		}
		if !strings.Contains(v.LogTail, "tab\there \"quoted\" back\\slash café\n") || !strings.Contains(v.LogTail, "FAIL: parser\n") || strings.ContainsRune(v.LogTail, '\x01') {
			t.Fatalf("the command's output was not recorded as sanitized text: %q", v.LogTail)
		}
		if err := v.Attests(summaryOf(task), task.patch); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("not applied", func(t *testing.T) {
		task := newValidateTask(t)
		if err := os.WriteFile(filepath.Join(task.dir, "source", "snapshot", "base", "parser.go"), []byte("package parser\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		v := task.run(t, "touch ran")
		if v.Outcome != implement.ValidationNotApplied || v.Applied || v.ExitCode != nil || v.LogTail == "" {
			t.Fatalf("a patch that does not apply was not recorded: %+v", v)
		}
		if err := v.Attests(summaryOf(task), task.patch); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("empty change", func(t *testing.T) {
		task := newValidateTask(t)
		task.setPatch(t, nil)
		v := task.run(t, "! grep -q 's\\[0\\]' parser.go")
		if v.Outcome != implement.ValidationPassed || !v.Applied || v.PatchDigest != capture.Digest(nil) {
			t.Fatalf("an empty change was not validated against the unchanged base: %+v", v)
		}
	})

	t.Run("the input is left untouched", func(t *testing.T) {
		task := newValidateTask(t)
		task.run(t, "echo changed > parser.go && rm check.sh")
		if _, err := os.Stat(filepath.Join(task.dir, "source", "snapshot", "base", "check.sh")); errors.Is(err, os.ErrNotExist) {
			t.Fatal("the command changed the Run input instead of its own copy")
		}
		data, err := os.ReadFile(filepath.Join(task.dir, "source", "snapshot", "base", "parser.go"))
		if err != nil || !strings.Contains(string(data), "s[1]") {
			t.Fatalf("the Run input's base was modified: %q %v", data, err)
		}
	})

	// The base's executable modes are restored for every name a captured
	// path may hold, and for no other file: the command passes only if
	// exactly the executable files are executable.
	t.Run("every executable name", func(t *testing.T) {
		executable := []string{"check.sh", "check{x}.sh", "a&b<c>.sh", "q\"uote.sh", "t\tab.sh", "caf\u00e9.sh", "sep\u2028.sh", "ctl\x01.sh", "back`tick$.sh", "back\\slash.sh"}
		base := implement.Tree{"parser.go": {Data: []byte("package parser\n"), Mode: "100644"}, "plain{x}.txt": {Data: []byte("x\n"), Mode: "100644"}}
		for _, name := range executable {
			if !capture.SafePath(name) {
				continue
			}
			base[name] = implement.TreeFile{Data: []byte("#!/bin/sh\n"), Mode: "100755"}
		}
		edited := implement.Tree{}
		for name, f := range base {
			edited[name] = f
		}
		edited["parser.go"] = implement.TreeFile{Data: []byte("package parser\n\n"), Mode: "100644"}
		want := len(base) - 2
		task := newValidateTaskFrom(t, base, edited)
		command := `n=0; for f in *; do if [ -x "$f" ]; then n=$((n + 1)); fi; done; echo "executable: $n"; [ "$n" -eq ` + strconv.Itoa(want) + ` ]`
		v := task.run(t, command)
		if v.Outcome != implement.ValidationPassed {
			t.Fatalf("executable modes were not restored exactly (want %d): %+v", want, v)
		}
	})

	// An empty base commit has no base/ directory in the Run input.
	t.Run("empty base", func(t *testing.T) {
		edited := implement.Tree{"check.sh": {Data: []byte("exit 0\n"), Mode: "100644"}}
		v := newValidateTaskFrom(t, implement.Tree{}, edited).run(t, "sh check.sh")
		if v.Outcome != implement.ValidationPassed || !v.Applied {
			t.Fatalf("a change to an empty base was not validated: %+v", v)
		}
	})

	// Without a working timeout the command is not run unbounded: the
	// outcome says so, and the change is still published.
	t.Run("no timeout", func(t *testing.T) {
		task := newValidateTask(t)
		tools := t.TempDir()
		for _, tool := range []string{"sh", "git", "sha256sum", "cut", "mktemp", "cp", "awk", "chmod", "tr", "sed", "tail", "mv", "rm", "dirname", "touch"} {
			path, err := exec.LookPath(tool)
			if err != nil {
				t.Fatalf("the validate script needs %s", tool)
			}
			if err := os.Symlink(path, filepath.Join(tools, tool)); err != nil {
				t.Fatal(err)
			}
		}
		marker := filepath.Join(t.TempDir(), "ran")
		v := task.runWithPath(t, "touch "+marker, tools)
		if v.Outcome != implement.ValidationNotRun || !v.Applied || v.ExitCode != nil || !strings.Contains(v.LogTail, "no working timeout") {
			t.Fatalf("a missing timeout was not recorded: %+v", v)
		}
		if _, err := os.Lstat(marker); !os.IsNotExist(err) {
			t.Fatal("the command ran without a timeout")
		}
	})

	// A base file whose mode cannot be restored leaves the tree unlike the
	// base, so the command is not run.
	t.Run("unrestorable mode", func(t *testing.T) {
		task := newValidateTask(t)
		if err := os.Remove(filepath.Join(task.dir, "source", "snapshot", "base", "check.sh")); err != nil {
			t.Fatal(err)
		}
		marker := filepath.Join(t.TempDir(), "ran")
		v := task.run(t, "touch "+marker)
		if v.Outcome != implement.ValidationNotRun || !v.Applied || !strings.Contains(v.LogTail, "executable mode") {
			t.Fatalf("an unrestorable mode was not recorded: %+v", v)
		}
		if _, err := os.Lstat(marker); !os.IsNotExist(err) {
			t.Fatal("the command ran in a tree unlike the base")
		}
	})
}
