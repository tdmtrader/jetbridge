package steps

// Implement scenarios use the public commands, real Git, real stdio and the
// real tmpfs session. The provider executable is the sole double: it edits the
// workspace deterministically and never calls a model.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/agent/implement"
	"github.com/concourse/concourse/agent/review"
)

type ImplementChange struct {
	// Review embeds the shared workspace, binaries, repository helpers and
	// process results; Input is the snapshot and Output the change.
	Review ReviewChange
	Brief  string
}

func ImplementWorkerDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, ImplementChange]("a committed repository and an implementation brief", []string{"review-binaries", "review-workspace"},
			func(_ brine.Empty, _ brine.Params, _ *brine.Recorder, res brine.Resources) (ImplementChange, error) {
				return newImplementChange(res)
			}),
		brine.DefineMap[ImplementChange, ImplementChange]("the implementation snapshot is captured from the command line", func(in ImplementChange, _ brine.Params, _ *brine.Recorder) (ImplementChange, error) {
			r := &in.Review
			r.Stdout, r.Stderr, r.CommandErr = reviewCommand(r.Binaries.CLI, []string{"implement", "capture", "--repo", r.Repo, "--base", r.Base, "--brief", in.Brief, "--output", r.Input}, "")
			if r.CommandErr != nil {
				return in, fmt.Errorf("capture: %w: %s", r.CommandErr, r.Stderr)
			}
			var reply struct {
				Digest string `json:"input_digest"`
				Base   string `json:"base_commit"`
			}
			if err := json.Unmarshal(r.Stdout, &reply); err != nil {
				return in, err
			}
			if reply.Base != r.Base {
				return in, errors.New("snapshot did not bind the requested base commit")
			}
			r.Digest = reply.Digest
			return in, nil
		}),
		brine.DefineMap[ImplementChange, ImplementChange](recaptureWithFindings, func(in ImplementChange, p brine.Params, _ *brine.Recorder) (ImplementChange, error) {
			run, err := intAt(recaptureWithFindings, p, 0)
			if err != nil {
				return in, err
			}
			return in.recaptureWithFindings(run)
		}),
		brine.DefineMap[ImplementChange, ImplementChange]("the implement worker receives model output {string}", func(in ImplementChange, p brine.Params, _ *brine.Recorder) (ImplementChange, error) {
			mode, _ := p.GetString(0)
			return in.runWorker(mode)
		}),
		CheckThat[ImplementChange]("the published change applies as a local commit on a new branch", checkImplementApplies),
		CheckThat[ImplementChange]("the implement worker fails without publishing a change", func(in ImplementChange) error {
			r := in.Review
			if r.CommandErr == nil {
				return errors.New("worker published a change outside the edit-only policy")
			}
			if len(r.Stderr) == 0 {
				return errors.New("worker failed without a diagnostic")
			}
			return reviewAbsent(r.Output)
		}),
		CheckThat[ImplementChange]("the implement session leaves no credential files or credential-bearing output", func(in ImplementChange) error {
			return reviewNoCredentials(in.Review)
		}),
		CheckThat[ImplementChange]("the captured implementation snapshot is unchanged", func(in ImplementChange) error {
			_, err := in.snapshot()
			return err
		}),
		CheckThat[ImplementChange]("the model read the review findings through the workspace tools", func(in ImplementChange) error {
			summary, _, err := implement.ReadResult(in.Review.Output)
			if err != nil {
				return err
			}
			// The fake provider reads /input/findings.json from the tool
			// server the worker configured and echoes the first title.
			if want := "Addressed review finding: " + brineFindingTitle + "."; !strings.Contains(summary.Summary, want) {
				return fmt.Errorf("summary %q does not show the findings were read", summary.Summary)
			}
			return nil
		}),
		check[ImplementChange](recordsFindingsRun, func(in ImplementChange, p brine.Params) error {
			run, err := intAt(recordsFindingsRun, p, 0)
			if err != nil {
				return err
			}
			summary, _, err := implement.ReadResult(in.Review.Output)
			if err != nil {
				return err
			}
			if got := summary.Provenance.FindingsRun; got == nil || *got != run || summary.Provenance.PriorRun != nil {
				return fmt.Errorf("provenance records findings_run %v and prior_run %v, want %d and none", got, summary.Provenance.PriorRun, run)
			}
			return nil
		}),
		CheckThat[ImplementChange]("the existing change is protected from a second invocation", func(in ImplementChange) error {
			before := map[string][]byte{}
			for _, name := range []string{implement.PatchFile, implement.SummaryFile} {
				b, err := os.ReadFile(filepath.Join(in.Review.Output, name))
				if err != nil {
					return err
				}
				before[name] = b
			}
			after, err := in.runWorker("edit")
			if err != nil {
				return err
			}
			if after.Review.CommandErr == nil {
				return errors.New("worker accepted an occupied change destination")
			}
			for name, want := range before {
				got, err := os.ReadFile(filepath.Join(in.Review.Output, name))
				if err != nil {
					return err
				}
				if !bytes.Equal(got, want) {
					return errors.New("second invocation changed a published change")
				}
			}
			return reviewNoCredentials(after.Review)
		}),
	}
}

const (
	recaptureWithFindings = "the implementation snapshot is recaptured carrying the findings of review Run {int}"
	recordsFindingsRun    = "the published change records review Run {int} as the findings it addresses"
	brineFindingTitle     = "Incorrect first-byte handling"
)

// recaptureWithFindings replaces the snapshot with one of the same base and
// brief that also carries one review/v1 finding from review Run run.
// Retrieving and verifying findings from a review Run needs a platform, which
// this tier does not have; `jb implement capture --findings-run` is covered by
// the jb command's tests. The verified findings are sealed here through the
// same capture function the command calls.
func (in ImplementChange) recaptureWithFindings(run int) (ImplementChange, error) {
	r := &in.Review
	if err := os.RemoveAll(r.Input); err != nil {
		return in, err
	}
	findings, err := json.MarshalIndent([]review.Finding{{
		ID: "f-001", Severity: "high", Dimension: "correctness", Title: brineFindingTitle,
		Explanation: "First returns the second byte.", Recommendation: "Return s[0].",
		Location: review.Location{Side: "head", Path: "parser.go", StartLine: 2, EndLine: 2},
	}}, "", "  ")
	if err != nil {
		return in, err
	}
	s, err := implement.CaptureSnapshot(context.Background(), implement.CaptureOptions{Repo: r.Repo, Base: r.Base, Brief: in.Brief, Output: r.Input,
		Findings: &implement.ReviewFindings{JSON: findings, RunID: run, ReviewedBase: r.Base, ReviewedHead: r.Head}})
	if err != nil {
		return in, err
	}
	if s.Manifest.FindingsRun == nil || *s.Manifest.FindingsRun != run {
		return in, errors.New("snapshot does not bind the review Run")
	}
	r.Digest = s.Digest
	return in, nil
}

func newImplementChange(res brine.Resources) (ImplementChange, error) {
	w := res.Get("review-workspace").(*reviewWorkspace)
	r := ReviewChange{Workspace: w, Binaries: res.Get("review-binaries").(reviewBinaries), Repo: filepath.Join(w.Root, "repo"), Input: filepath.Join(w.Root, "snapshot"), Output: filepath.Join(w.Root, "change")}
	in := ImplementChange{Review: r, Brief: filepath.Join(w.Root, "brief.md")}
	if err := os.Mkdir(r.Repo, 0700); err != nil {
		return in, err
	}
	// apply commits with the developer's configured identity, which lives in
	// the repository here rather than in a global file.
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.name", "Implement fixture"}, {"config", "user.email", "implement@example.test"}, {"config", "commit.gpgsign", "false"}} {
		if _, err := r.git(args...); err != nil {
			return in, err
		}
	}
	for name, text := range map[string]string{
		"repo/parser.go":      "package parser\nfunc First(s string) byte { return s[1] }\n",
		"repo/deleted.txt":    "removed line\n",
		"repo/.gitattributes": "parser.go export-ignore\n",
		"brief.md":            "Return the first byte, cover it with a test, and remove deleted.txt.\n",
		"auth.json":           reviewSyntheticAuth,
	} {
		if err := os.WriteFile(filepath.Join(w.Root, name), []byte(text), 0600); err != nil {
			return in, err
		}
	}
	var err error
	in.Review.Base, err = r.commit("base")
	in.Review.Head = in.Review.Base
	return in, err
}

func (in ImplementChange) snapshot() (*implement.Snapshot, error) {
	r := in.Review
	if r.Digest == "" {
		return nil, fmt.Errorf("capture did not produce a digest: %v: %s", r.CommandErr, r.Stderr)
	}
	s, err := implement.LoadSnapshot(r.Input)
	if err != nil {
		return nil, err
	}
	if s.Digest != r.Digest {
		return nil, errors.New("snapshot differs from CLI digest")
	}
	return s, nil
}

func (in ImplementChange) runWorker(mode string) (ImplementChange, error) {
	if _, err := in.snapshot(); err != nil {
		return in, err
	}
	r := &in.Review
	if err := r.memoryRuntime(); err != nil {
		return in, err
	}
	r.Mode = mode
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, r.Binaries.Worker, "implement",
		"--input", r.Input, "--output", r.Output, "--runtime-dir", r.Workspace.Runtime,
		"--codex", r.Binaries.Provider, "--model", mode, "--timeout", "10s", "--auth-stdin")
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "OPENAI_API_KEY=") && !strings.HasPrefix(v, "CODEX_API_KEY=") {
			cmd.Env = append(cmd.Env, v)
		}
	}
	cmd.Env = append(cmd.Env, "OPENAI_API_KEY=synthetic-must-not-inherit", "CODEX_API_KEY=synthetic-must-not-inherit")
	cmd.Stdin = strings.NewReader(reviewSyntheticAuth)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	r.CommandErr = cmd.Run()
	if ctx.Err() != nil {
		return in, errors.New("implement command exceeded the test deadline")
	}
	r.Stdout, r.Stderr = stdout.Bytes(), stderr.Bytes()
	return in, nil
}

// checkImplementApplies verifies the published change against the snapshot,
// then applies it with the public command and inspects the resulting commit.
func checkImplementApplies(in ImplementChange) error {
	r := in.Review
	if r.CommandErr != nil {
		return fmt.Errorf("worker failed: %w: %s", r.CommandErr, r.Stderr)
	}
	s, err := in.snapshot()
	if err != nil {
		return err
	}
	summary, patch, err := implement.ReadResult(r.Output)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(r.Output, implement.SummaryFile))
	if err != nil {
		return err
	}
	if _, err := implement.ParseChange(data, patch, s); err != nil {
		return err
	}
	if summary.Provenance.ExecutionPolicy != "edit-only" || !strings.Contains(strings.Join(summary.Limitations, " "), "not executed") {
		return errors.New("summary lacks the edit-only, non-execution disclosure")
	}
	var changed []string
	for _, f := range summary.ChangedFiles {
		changed = append(changed, f.Status+" "+f.Path)
	}
	if got := strings.Join(changed, ", "); got != "deleted deleted.txt, modified parser.go, added parser_test.go" {
		return fmt.Errorf("changed files are %s", got)
	}
	out, stderr, err := reviewCommand(r.Binaries.CLI, []string{"implement", "apply", "--repo", r.Repo, "--result-dir", r.Output}, "")
	if err != nil {
		return fmt.Errorf("apply: %w: %s", err, stderr)
	}
	return checkAppliedCommit(r, out, "impl/local-"+summary.PatchDigest[:12], s.Digest, 0)
}

// checkAppliedCommit inspects what `jb implement apply` reported and did: one
// commit on the named new branch, parented on the base, holding exactly the
// fixture edit, with the worktree switched to it and clean. A change from a
// Run also records the Run in the commit.
func checkAppliedCommit(r ReviewChange, out []byte, branch, inputDigest string, runID int) error {
	var applied struct {
		Branch     string `json:"branch"`
		Commit     string `json:"commit"`
		BaseCommit string `json:"base_commit"`
	}
	if err := json.Unmarshal(out, &applied); err != nil {
		return err
	}
	if applied.Branch != branch || applied.BaseCommit != r.Base {
		return fmt.Errorf("applied %+v, want branch %s on %s", applied, branch, r.Base)
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"rev-parse", "--abbrev-ref", "HEAD"}, applied.Branch},
		{[]string{"rev-parse", "HEAD"}, applied.Commit},
		{[]string{"rev-parse", "HEAD^"}, r.Base},
		{[]string{"diff", "--name-status", r.Base, "HEAD"}, "D\tdeleted.txt\nM\tparser.go\nA\tparser_test.go"},
		{[]string{"status", "--porcelain"}, ""},
	} {
		got, err := r.git(c.args...)
		if err != nil {
			return err
		}
		if got != c.want {
			return fmt.Errorf("git %s: %q, want %q", strings.Join(c.args, " "), got, c.want)
		}
	}
	message, err := r.git("log", "-1", "--format=%B")
	if err != nil {
		return err
	}
	if !strings.Contains(message, "JetBridge-Input: "+inputDigest) {
		return errors.New("commit does not record the snapshot it came from")
	}
	if runID > 0 && !strings.Contains(message, fmt.Sprintf("JetBridge-Run: %d\n", runID)) {
		return errors.New("commit does not record the Run it came from")
	}
	parser, err := os.ReadFile(filepath.Join(r.Repo, "parser.go"))
	if err != nil || !strings.Contains(string(parser), "s[0]") {
		return errors.New("applied worktree does not hold the edit")
	}
	return nil
}
