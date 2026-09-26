package steps

// Implement submission joins the real snapshot capture, the shared detached
// client, public upload and admission, the Run's receipt-keyed replay and,
// for a ready Run, the real worker in implement mode through to a local
// commit. Only the model process is substituted.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/agent/detached"
	"github.com/concourse/concourse/agent/implement"
	implementclient "github.com/concourse/concourse/agent/implement/client"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"golang.org/x/oauth2"
)

func ImplementSubmitDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{brine.DefineMapUsing[RunInputAdmission, RunInputAdmission]("a saved implement submission encounters {string}", []string{"auth-server", "real-cluster", "review-binaries", "review-workspace"}, func(in RunInputAdmission, p brine.Params, rec *brine.Recorder, res brine.Resources) (RunInputAdmission, error) {
		mode, _ := p.GetString(0)
		return in, exerciseImplementSubmit(in, mode, rec, res)
	})}
}

func exerciseImplementSubmit(in RunInputAdmission, mode string, rec *brine.Recorder, res brine.Resources) error {
	auth, _, err := configureRunInputAPI(in, "owner", rec, res)
	if err != nil {
		return err
	}
	if _, err = auth.fly("login", "-c", auth.URL, "-n", "auth-team", "-u", "owner", "-p", authPassword); err != nil {
		return err
	}
	token, err := auth.savedFlyToken()
	if err != nil {
		return err
	}
	transport := func() *http.Client {
		return oauth2.NewClient(context.WithValue(context.Background(), oauth2.HTTPClient, auth.Client), oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token.Value, TokenType: token.Type}))
	}
	client, err := implementclient.New(auth.URL, transport())
	if err != nil {
		return err
	}
	change, err := newImplementChange(res)
	if err != nil {
		return err
	}
	r := change.Review
	stdout, stderr, err := reviewCommand(r.Binaries.CLI, []string{"implement", "capture", "--repo", r.Repo, "--base", r.Base, "--brief", change.Brief, "--output", r.Input}, "")
	if err != nil {
		return fmt.Errorf("capture: %v: %s", err, stderr)
	}
	var captured struct {
		Digest string `json:"input_digest"`
	}
	if err := json.Unmarshal(stdout, &captured); err != nil {
		return err
	}
	change.Review.Digest = captured.Digest
	// newImplementChange wrote the synthetic owner auth file beside the repository.
	options := implementclient.SubmitOptions{Team: "output-start", Template: "input-review", Input: r.Input, Receipt: filepath.Join(r.Workspace.Root, "request.json"), AuthFile: filepath.Join(r.Workspace.Root, "auth.json")}
	if mode == "the installed implement template" {
		if _, err := auth.fly("set-pipeline", "--non-interactive", "--team", options.Team, "-p", options.Template, "-c", filepath.Join(repoRoot(), "deploy", "implement-template.yml"), "-v", "review_worker_image=example.test/reviewer@sha256:"+strings.Repeat("a", 64), "-v", "implement_model=brine-model"); err != nil {
			return err
		}
	}
	if mode == "ready CLI" {
		// The author task receives the owner's credentials only from the
		// operator-pinned worker image the Run snapshots at admission.
		if in.Template, err = pinProducerImage(in.Source.Start.DB.TeamFactory, in.Template, brineCredentialWorkerImage); err != nil {
			return err
		}
	}
	count := func() (int, error) {
		var n int
		err := in.Source.Start.DB.Conn.QueryRow(`SELECT count(*) FROM pipeline_runs WHERE template_pipeline_id=$1`, in.Template.ID()).Scan(&n)
		return n, err
	}
	before, err := count()
	if err != nil {
		return err
	}

	// Interrupt the first submission as soon as its receipt records the
	// admitted Run: the worker never started, so the Run is unready.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	type outcome struct {
		result implementclient.Submission
		err    error
	}
	done := make(chan outcome, 1)
	go func() { result, err := client.Submit(ctx, options); done <- outcome{result, err} }()
	var receipt struct {
		Workload string `json:"workload"`
		RunID    int    `json:"run_id"`
		Number   int    `json:"number"`
		SourceID string `json:"source_id"`
		Key      string `json:"invocation_key"`
	}
	for {
		if data, err := os.ReadFile(options.Receipt); err == nil && json.Unmarshal(data, &receipt) == nil && receipt.RunID > 0 {
			break
		}
		select {
		case result := <-done:
			return fmt.Errorf("submission exited without an admitted receipt: %v", result.err)
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	first := <-done
	if first.err == nil || !errors.Is(first.err, context.Canceled) || first.result.Ready || first.result.RunID != receipt.RunID || first.result.Handle.Number != receipt.Number {
		return fmt.Errorf("interrupted submission lost its admitted Run: %v", first.err)
	}
	if mode == "the installed implement template" {
		if err := checkInstalledImplementTemplate(in, receipt.RunID); err != nil {
			return err
		}
	}
	data, err := os.ReadFile(options.Receipt)
	if err != nil {
		return err
	}
	st, err := os.Stat(options.Receipt)
	if err != nil {
		return err
	}
	if st.Mode().Perm() != 0600 || bytes.Contains(data, []byte("synthetic-")) || bytes.Contains(data, []byte("bearer")) || bytes.Contains(data, []byte("auth.json")) || receipt.Key == "" || receipt.SourceID == "" || receipt.Workload != "implement" {
		return fmt.Errorf("local receipt is unsafe, incomplete or names another workload")
	}

	switch mode {
	case "ready CLI":
		return finishSubmittedImplement(in, auth, change, options, first.result, rec, res)
	case "a fresh CLI replay":
		if err := exerciseSubmitCLI(auth, change.Review, "implement", options, first.result); err != nil {
			return err
		}
	case "a receipt replayed by another workload":
		if err := replayUnderAnotherWorkload(auth, change, options, data, transport()); err != nil {
			return err
		}
	case "a fresh client replay", "a changed input":
		fresh, err := implementclient.New(auth.URL, transport())
		if err != nil {
			return err
		}
		if mode == "a changed input" {
			if err := os.WriteFile(filepath.Join(options.Input, "brief.md"), []byte("Do something else.\n"), 0600); err != nil {
				return err
			}
		}
		retryCtx, retryCancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer retryCancel()
		replayed, err := fresh.Submit(retryCtx, options)
		if err == nil {
			return fmt.Errorf("unready or changed submission was reported complete")
		}
		if mode == "a fresh client replay" && (replayed.RunID != first.result.RunID || replayed.Handle != first.result.Handle || !errors.Is(err, context.DeadlineExceeded)) {
			return fmt.Errorf("fresh submit lost the original pending Run: %v", err)
		}
		if mode == "a changed input" && !strings.Contains(err.Error(), "digest mismatch") {
			return fmt.Errorf("a changed snapshot was not refused by its own verification: %v", err)
		}
	case "interruption before readiness", "the installed implement template":
	default:
		return fmt.Errorf("unknown implement submission case %q", mode)
	}
	after, err := count()
	if err != nil || after != before+1 {
		return fmt.Errorf("submission admitted %d Runs, want one: %v", after-before, err)
	}
	var claims int
	if err := in.Source.Start.DB.Conn.QueryRow(`SELECT count(*) FROM pipeline_run_credential_handoffs WHERE run_id=$1`, receipt.RunID).Scan(&claims); err != nil || claims != 0 {
		return fmt.Errorf("unstarted implementation claimed credentials: %v", err)
	}
	return nil
}

func checkInstalledImplementTemplate(in RunInputAdmission, runID int) error {
	definition, found, err := db.NewPipelineRunFactory(in.Source.Start.DB.Conn, in.Source.Start.DB.LockFactory).Definition(runID)
	if err != nil || !found || len(definition.Materialized.Jobs) != 1 || len(definition.Materialized.Jobs[0].PlanSequence) != 1 {
		return fmt.Errorf("installed implement template has no single-task definition: %v", err)
	}
	task, ok := definition.Materialized.Jobs[0].PlanSequence[0].Config.(*atc.TaskStep)
	if !ok || task.RunResult == nil || task.RunResult.Name != implementclient.ChangeResult || len(task.RunInputs) != 1 || task.RunInputs[0].Name != implementclient.SnapshotInput || task.Config.Run.Path != "/bin/sh" {
		return fmt.Errorf("installed template lost its snapshot input, change result or launcher")
	}
	args := task.Config.Run.Args
	if len(args) != 5 || !strings.Contains(args[1], "jb-review-worker implement") || args[3] != "brine-model" || args[4] != fmt.Sprintf("--run-id=%d", runID) {
		return fmt.Errorf("installed template did not preserve the implement mode, operator model and admitted Run ID: %v", args)
	}
	return nil
}

// replayUnderAnotherWorkload reuses the implement receipt for a review. The
// real review command, given a valid review bundle of the same repository, is
// refused before it reaches the platform. Then the same snapshot, destination
// and receipt under a workload of another name is refused too: the workload
// recorded in the receipt decides on its own. The receipt is left unchanged.
func replayUnderAnotherWorkload(auth *AuthFixture, change ImplementChange, options implementclient.SubmitOptions, saved []byte, transport *http.Client) error {
	r := change.Review
	bundle := filepath.Join(r.Workspace.Root, "review-input")
	if _, stderr, err := reviewCommand(r.Binaries.CLI, []string{"review", "capture", "--repo", r.Repo, "--base", r.Base, "--head", r.Base, "--output", bundle}, ""); err != nil {
		return fmt.Errorf("review capture: %v: %s", err, stderr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, r.Binaries.CLI, "review", "submit", "--target", "auth", "--team", options.Team, "--template", options.Template, "--input", bundle, "--receipt", options.Receipt, "--auth-file", options.AuthFile)
	cmd.Env = append(os.Environ(), "FLY_HOME="+auth.Home)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err == nil || stdout.Len() != 0 || !bytes.Contains(stderr.Bytes(), []byte("another workload")) {
		return fmt.Errorf("review submission resumed an implement receipt: %v: %s%s", err, stdout.Bytes(), stderr.Bytes())
	}
	shared, err := detached.New(auth.URL, transport)
	if err != nil {
		return err
	}
	renamed := implementclient.Workload()
	renamed.Name = "review"
	if _, err := shared.Submit(ctx, renamed, options); err == nil || !strings.Contains(err.Error(), "another workload") {
		return fmt.Errorf("the same input and destination under another workload resumed the receipt: %v", err)
	}
	after, err := os.ReadFile(options.Receipt)
	if err != nil {
		return err
	}
	if !bytes.Equal(after, saved) {
		return fmt.Errorf("a refused workload rewrote the receipt")
	}
	return nil
}

// finishSubmittedImplement drives the ready Run through the real worker in
// implement mode, then retrieves and applies its change from fresh processes.
func finishSubmittedImplement(in RunInputAdmission, auth *AuthFixture, change ImplementChange, options implementclient.SubmitOptions, pending implementclient.Submission, rec *brine.Recorder, res brine.Resources) error {
	r := change.Review
	original, err := implement.LoadSnapshot(r.Input)
	if err != nil {
		return err
	}
	return finishSubmittedRun(in, auth, r, pending, submittedWorkload{
		Input: implementclient.SnapshotInput,
		Receive: func(materialized string) (string, string, error) {
			path := filepath.Join(materialized, implement.RunInputSnapshotDir)
			received, err := implement.LoadSnapshot(path)
			if err != nil || received.Digest != original.Digest {
				return "", "", fmt.Errorf("uploaded snapshot changed in transit: %v", err)
			}
			return path, received.Digest, nil
		},
		Mode:    []string{"implement"},
		Model:   "handoff-edit",
		Results: []string{implement.PatchFile, implement.SummaryFile},
		Submit: func(ctx context.Context) (detached.Submission, error) {
			var result detached.Submission
			out, err := jbCommand(ctx, auth, r.Binaries.CLI, "implement", "submit", "--target", "auth", "--team", options.Team, "--template", options.Template, "--input", options.Input, "--receipt", options.Receipt, "--auth-file", options.AuthFile)
			if err != nil {
				return result, err
			}
			return result, json.Unmarshal(out, &result)
		},
		Read: func(ctx context.Context) error {
			return readCompletedImplementation(ctx, auth, change, options, pending, original)
		},
	}, rec, res)
}

func readCompletedImplementation(ctx context.Context, auth *AuthFixture, change ImplementChange, options implementclient.SubmitOptions, pending implementclient.Submission, original *implement.Snapshot) error {
	r := change.Review
	destination := []string{"--target", "auth", "--team", options.Team, "--template", options.Template, "--run", strconv.Itoa(pending.Handle.Number)}
	out, err := jbCommand(ctx, auth, r.Binaries.CLI, append([]string{"implement", "status"}, destination...)...)
	if err != nil {
		return err
	}
	var run atc.PipelineRun
	if err := json.Unmarshal(out, &run); err != nil {
		return err
	}
	if run.ID != pending.RunID || run.Terminal == nil || run.Terminal.Status != atc.RunStatusSucceeded {
		return fmt.Errorf("fresh status lost the submitted Run's terminal observation")
	}
	if _, bound := run.Terminal.Results[implementclient.ChangeResult]; !bound {
		return fmt.Errorf("the Run bound no %s result", implementclient.ChangeResult)
	}
	retrieved := filepath.Join(r.Workspace.Root, "retrieved")
	out, err = jbCommand(ctx, auth, r.Binaries.CLI, append([]string{"implement", "result", "--output", retrieved}, destination...)...)
	if err != nil {
		return err
	}
	var fetched struct {
		Summary *implement.Summary `json:"summary"`
		Patch   string             `json:"patch"`
	}
	if err := json.Unmarshal(out, &fetched); err != nil || fetched.Summary == nil {
		return fmt.Errorf("fresh result has no typed change: %v", err)
	}
	s := fetched.Summary
	if s.RunID == nil || *s.RunID != pending.RunID || s.Provenance.InputDigest != original.Digest || s.Provenance.ExecutionPolicy != "edit-only" {
		return fmt.Errorf("fresh result lost the Run identity or snapshot provenance")
	}
	var changed []string
	for _, f := range s.ChangedFiles {
		changed = append(changed, f.Status+" "+f.Path)
	}
	if got := strings.Join(changed, ", "); got != "deleted deleted.txt, modified parser.go, added parser_test.go" {
		return fmt.Errorf("retrieved change touches %s", got)
	}
	written, patch, err := implement.ReadResult(retrieved)
	if err != nil || written.PatchDigest != s.PatchDigest || string(patch) != fetched.Patch {
		return fmt.Errorf("retrieved change was not written as published: %v", err)
	}
	summaryJSON, err := os.ReadFile(filepath.Join(retrieved, implement.SummaryFile))
	if err != nil {
		return err
	}
	if _, err := implement.ParseChange(summaryJSON, patch, original); err != nil {
		return fmt.Errorf("retrieved change does not apply to its snapshot: %v", err)
	}
	out, err = jbCommand(ctx, auth, r.Binaries.CLI, append([]string{"implement", "apply", "--repo", r.Repo}, destination...)...)
	if err != nil {
		return err
	}
	return checkAppliedCommit(r, out, fmt.Sprintf("impl/run-%d", pending.RunID), original.Digest, pending.RunID)
}

// jbCommand runs the jb CLI under the saved fly login and returns its stdout.
func jbCommand(ctx context.Context, auth *AuthFixture, cli string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, cli, args...)
	cmd.Env = append(os.Environ(), "FLY_HOME="+auth.Home)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("jb %s: %w: %s", strings.Join(args[:2], " "), err, stderr.Bytes())
	}
	return stdout.Bytes(), nil
}
