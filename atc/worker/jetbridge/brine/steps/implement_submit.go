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
	"io"
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
	"sigs.k8s.io/yaml"
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
		if _, err := auth.fly("set-pipeline", "--non-interactive", "--team", options.Team, "-p", options.Template, "-c", filepath.Join(repoRoot(), "deploy", "implement-template.yml"), "-v", "review_worker_image=example.test/reviewer@sha256:"+strings.Repeat("a", 64), "-v", "implement_model=brine-model", "-v", "validate_image=example.test/toolchain@sha256:"+strings.Repeat("b", 64), "-v", "validate_command="+installedValidateCommand); err != nil {
			return err
		}
	}
	expected, surface, ready := readyValidation(mode)
	if ready {
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

	if ready {
		return finishSubmittedImplement(in, auth, client, change, options, first.result, surface, expected, rec, res)
	}
	switch mode {
	case "a fresh CLI replay":
		if err := exerciseSubmitCLI(auth, change.Review, "implement", options, first.result); err != nil {
			return err
		}
	case "a fresh MCP replay":
		if err := exerciseImplementSubmitMCP(auth, change, options, data, receipt.RunID, receipt.Number); err != nil {
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

// installedValidateCommand is the operator's validation command the
// installed-template row sets; it is passed to the validate task unchanged.
const installedValidateCommand = "go test ./..."

func checkInstalledImplementTemplate(in RunInputAdmission, runID int) error {
	definition, found, err := db.NewPipelineRunFactory(in.Source.Start.DB.Conn, in.Source.Start.DB.LockFactory).Definition(runID)
	if err != nil || !found || len(definition.Materialized.Jobs) != 1 || len(definition.Materialized.Jobs[0].PlanSequence) != 2 {
		return fmt.Errorf("installed implement template has no author and validate definition: %v", err)
	}
	author, ok := definition.Materialized.Jobs[0].PlanSequence[0].Config.(*atc.TaskStep)
	if !ok || author.RunResult == nil || author.RunResult.Name != implementclient.ChangeResult || len(author.RunInputs) != 1 || author.RunInputs[0].Name != implementclient.SnapshotInput || author.Config.Run.Path != "/bin/sh" {
		return fmt.Errorf("installed template lost its snapshot input, change result or launcher")
	}
	args := author.Config.Run.Args
	if len(args) != 5 || !strings.Contains(args[1], "jb-review-worker implement") || args[3] != "brine-model" || args[4] != fmt.Sprintf("--run-id=%d", runID) {
		return fmt.Errorf("installed template did not preserve the implement mode, operator model and admitted Run ID: %v", args)
	}
	// The validate task routes the same snapshot input, publishes the
	// validation result, and runs the operator's command on the operator's
	// image: never the pinned worker image that receives credentials.
	validate, ok := definition.Materialized.Jobs[0].PlanSequence[1].Config.(*atc.TaskStep)
	if !ok || validate.RunResult == nil || validate.RunResult.Name != implementclient.ValidationResult || len(validate.RunInputs) != 1 || validate.RunInputs[0].Name != implementclient.SnapshotInput || validate.Config.Run.Path != "/bin/sh" {
		return fmt.Errorf("installed template lost its validate task's snapshot input, validation result or launcher")
	}
	if image := atc.RunTaskImage(definition.Materialized, validate.TaskID); image != "docker:///example.test/toolchain@sha256:"+strings.Repeat("b", 64) {
		return fmt.Errorf("installed validate task runs %q, not the operator's validate image", image)
	}
	args = validate.Config.Run.Args
	if len(args) != 5 || args[3] != installedValidateCommand || args[4] != fmt.Sprintf("--run-id=%d", runID) {
		return fmt.Errorf("installed template did not preserve the operator's validate command and admitted Run ID: %v", args)
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

// validationCase is what one ready row's validate task runs and must record.
type validationCase struct {
	// Command is the operator's validate_command.
	Command string
	// Tamper replaces the validate container's copy of the snapshot's base so
	// the published change cannot apply to it.
	Tamper  bool
	Outcome string
	// Exit is the command's recorded exit code; -1 when it did not run.
	Exit int
}

// readyValidation selects the surface ("CLI" or "MCP") a ready row submits
// and reads through, and the validate outcome it must record. Every command
// first proves the change was applied: the edit added parser_test.go.
func readyValidation(mode string) (validationCase, string, bool) {
	surface, outcome, found := strings.Cut(strings.TrimPrefix(mode, "ready "), ", ")
	if !strings.HasPrefix(mode, "ready ") || !found || (surface != "CLI" && surface != "MCP") {
		return validationCase{}, "", false
	}
	switch outcome {
	case "validation passes":
		return validationCase{Command: `test -f parser_test.go && test ! -e deleted.txt && grep -q 's\[0\]' parser.go`, Outcome: implement.ValidationPassed}, surface, true
	case "validation fails":
		return validationCase{Command: `test -f parser_test.go && echo "FAIL: parser_test.go" && exit 7`, Outcome: implement.ValidationFailed, Exit: 7}, surface, true
	case "patch does not apply":
		return validationCase{Command: "touch ran", Tamper: true, Outcome: implement.ValidationNotApplied, Exit: -1}, surface, true
	}
	return validationCase{}, "", false
}

// finishSubmittedImplement drives the ready Run through the real worker in
// implement mode, then the template's own validate script as the Run's second
// result producer, and retrieves and applies its change from fresh processes.
// The surface, "CLI" or "MCP", is what resumes the submission and reads the
// results; the change is applied by the CLI either way.
func finishSubmittedImplement(in RunInputAdmission, auth *AuthFixture, client *implementclient.Client, change ImplementChange, options implementclient.SubmitOptions, pending implementclient.Submission, surface string, expected validationCase, rec *brine.Recorder, res brine.Resources) error {
	r := change.Review
	original, err := implement.LoadSnapshot(r.Input)
	if err != nil {
		return err
	}
	published := filepath.Join(r.Workspace.Root, "published-change")
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
			if surface == "MCP" {
				session, err := jbMCP(ctx, auth, r.Binaries.CLI, "brine-detached", options, options.AuthFile)
				if err != nil {
					return result, err
				}
				defer session.Close()
				if err := callJBTool(ctx, session, "implement_submit", map[string]any{"input": options.Input, "receipt": options.Receipt}, &result); err != nil {
					return result, err
				}
				return result, session.Close()
			}
			out, err := jbCommand(ctx, auth, r.Binaries.CLI, "implement", "submit", "--target", "auth", "--team", options.Team, "--template", options.Template, "--input", options.Input, "--receipt", options.Receipt, "--auth-file", options.AuthFile)
			if err != nil {
				return result, err
			}
			return result, json.Unmarshal(out, &result)
		},
		// The author's output is what the validate task's change input
		// receives; keep it as published for that container.
		Published: func(directory string) error {
			return os.CopyFS(published, os.DirFS(directory))
		},
		Then: func(ctx context.Context, run submittedRun) error {
			return runValidateTask(ctx, run, client, pending, published, filepath.Join(r.Workspace.Root, "validate-task"), expected, rec)
		},
		Read: func(ctx context.Context) error {
			if surface == "MCP" {
				return readCompletedImplementationMCP(ctx, in, auth, change, options, pending, original, expected)
			}
			return readCompletedImplementation(ctx, in, auth, change, options, pending, original, expected)
		},
	}, rec, res)
}

// runValidateTask drives the validate task as the build's second result
// producer: its own Run start, execution and capture on the same node. Its
// container is laid out as the template declares it -- the materialized Run
// input as source, the author's published change as change, an empty
// validation output -- and the installed template's own inline script runs
// in it with the row's command.
func runValidateTask(ctx context.Context, run submittedRun, client *implementclient.Client, pending implementclient.Submission, published, container string, expected validationCase, rec *brine.Recorder) error {
	var task *atc.TaskStep
	for _, step := range run.Definition.Jobs[0].PlanSequence {
		if t, ok := step.Config.(*atc.TaskStep); ok && t.RunResult != nil && t.RunResult.Name == implementclient.ValidationResult {
			task = t
		}
	}
	if task == nil {
		return fmt.Errorf("submitted Run has no %s producer", implementclient.ValidationResult)
	}
	script, err := installedValidateScript()
	if err != nil {
		return err
	}
	runtime := run.Runtime
	runtime.Start.Plan = atc.TaskPlan{Name: task.Name, TaskID: task.TaskID, RunInputs: task.RunInputs, RunResult: task.RunResult, Config: task.Config}
	runtime.Spec.Outputs = map[string]string{task.RunResult.Output: "/workspace/" + task.RunResult.Output}
	_, err = driveSubmittedProducer(ctx, runtime, run.Factory, run.BuildID, "submitted-validate", run.Keys, rec, func(directory string) error {
		if err := refuseValidationHandoff(ctx, run, client, pending); err != nil {
			return err
		}
		if err := os.CopyFS(filepath.Join(container, "source"), os.DirFS(run.Input)); err != nil {
			return err
		}
		if err := os.CopyFS(filepath.Join(container, "change"), os.DirFS(published)); err != nil {
			return err
		}
		if err := os.Mkdir(filepath.Join(container, "validation"), 0700); err != nil {
			return err
		}
		if expected.Tamper {
			if err := os.WriteFile(filepath.Join(container, "source", implement.RunInputSnapshotDir, "base", "parser.go"), []byte("package parser\n"), 0600); err != nil {
				return err
			}
		}
		cmd := exec.CommandContext(ctx, "/bin/sh", "-ec", script, "validate", expected.Command, "--run-id="+strconv.Itoa(run.RunID))
		cmd.Dir = container
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("validate task failed instead of recording its outcome: %w: %s", err, out)
		}
		if _, err := os.Stat(filepath.Join(container, "ran")); expected.Tamper && err == nil {
			return fmt.Errorf("the validate command ran against a change that did not apply")
		}
		entries, err := os.ReadDir(filepath.Join(container, "validation"))
		if err != nil || len(entries) != 1 || entries[0].Name() != implement.ValidationFile {
			return fmt.Errorf("validate task did not publish exactly %s: %v", implement.ValidationFile, err)
		}
		// The container and the reserved output need not share a filesystem.
		data, err := os.ReadFile(filepath.Join(container, "validation", implement.ValidationFile))
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(directory, implement.ValidationFile), data, 0600)
	})
	return err
}

// installedValidateScript is the validate task's inline script exactly as
// deploy/implement-template.yml installs it.
func installedValidateScript() (string, error) {
	data, err := os.ReadFile(filepath.Join(repoRoot(), "deploy", "implement-template.yml"))
	if err != nil {
		return "", err
	}
	var config atc.Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return "", err
	}
	for _, step := range config.Jobs[0].PlanSequence {
		if task, ok := step.Config.(*atc.TaskStep); ok && task.Name == "validate" && task.Config != nil && len(task.Config.Run.Args) == 5 {
			return task.Config.Run.Args[1], nil
		}
	}
	return "", fmt.Errorf("implement template has no validate script")
}

// refuseValidationHandoff runs while the validate task is executing. The
// owner's credentials went to the change producer; the platform refuses a
// session or a delivery naming the validation producer, and records nothing.
func refuseValidationHandoff(ctx context.Context, run submittedRun, client *implementclient.Client, pending implementclient.Submission) error {
	if _, err := client.CredentialSession(ctx, pending.Handle, implementclient.ValidationResult); !isHTTPStatus(err, http.StatusConflict) {
		return fmt.Errorf("a credential session for the validation producer was not refused: %v", err)
	}
	body := io.NopCloser(strings.NewReader(reviewSyntheticAuth))
	if _, err := client.HandoffCredentials(ctx, pending.Handle, implementclient.ValidationResult, body); !isHTTPStatus(err, http.StatusConflict) {
		return fmt.Errorf("a credential handoff to the validation producer was not refused: %v", err)
	}
	rows, err := run.Runtime.Start.DB.Conn.QueryContext(ctx, `SELECT s.result_name FROM pipeline_run_credential_handoffs h
 JOIN pipeline_run_output_starts s USING(handoff_id) WHERE h.run_id=$1`, run.RunID)
	if err != nil {
		return err
	}
	defer rows.Close()
	var results []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		results = append(results, name)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(results) != 1 || results[0] != implementclient.ChangeResult {
		return fmt.Errorf("the Run's credential handoffs name %v, want only %s", results, implementclient.ChangeResult)
	}
	return nil
}

func isHTTPStatus(err error, status int) bool {
	var refused detached.HTTPError
	return errors.As(err, &refused) && refused.StatusCode == status
}

func readCompletedImplementation(ctx context.Context, in RunInputAdmission, auth *AuthFixture, change ImplementChange, options implementclient.SubmitOptions, pending implementclient.Submission, original *implement.Snapshot, expected validationCase) error {
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
	if err := checkImplementTerminal(in, pending, run.ID, run.Terminal); err != nil {
		return err
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
	written, patch, err := implement.ReadResult(retrieved)
	if err != nil || written.PatchDigest != s.PatchDigest || string(patch) != fetched.Patch {
		return fmt.Errorf("retrieved change was not written as published: %v", err)
	}
	summaryJSON, err := os.ReadFile(filepath.Join(retrieved, implement.SummaryFile))
	if err != nil {
		return err
	}
	if err := checkRetrievedChange(pending, original, s, summaryJSON, patch); err != nil {
		return err
	}
	if err := readCompletedValidation(ctx, auth, r, destination, s, original, expected); err != nil {
		return err
	}
	return applyRetrievedChange(ctx, auth, r, destination, original, pending)
}

// checkImplementTerminal requires a fresh observation of the submitted Run to
// be its succeeded terminal state with both results bound.
func checkImplementTerminal(in RunInputAdmission, pending implementclient.Submission, id int, terminal *atc.RunTerminalResult) error {
	if id != pending.RunID || terminal == nil || terminal.Status != atc.RunStatusSucceeded {
		return fmt.Errorf("fresh status lost the submitted Run's terminal observation")
	}
	// Two result producers in one build: the Run binds both entries, each
	// with its own active claim, even when the validation records a failure.
	if len(terminal.Results) != 2 {
		return fmt.Errorf("the Run bound %d results, want %s and %s", len(terminal.Results), implementclient.ChangeResult, implementclient.ValidationResult)
	}
	for _, name := range []string{implementclient.ChangeResult, implementclient.ValidationResult} {
		binding, bound := terminal.Results[name]
		if !bound {
			return fmt.Errorf("the Run bound no %s result", name)
		}
		var active bool
		if err := in.Source.Start.DB.Conn.QueryRow(`SELECT released_at IS NULL FROM hangar_claims WHERE claim_id=$1`, string(binding.ClaimID)).Scan(&active); err != nil || !active {
			return fmt.Errorf("the %s result has no active claim: %v", name, err)
		}
	}
	return nil
}

// checkRetrievedChange requires a retrieved change to be the submitted Run's
// edit of its snapshot, and to still apply to that snapshot.
func checkRetrievedChange(pending implementclient.Submission, original *implement.Snapshot, s *implement.Summary, summaryJSON, patch []byte) error {
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
	if _, err := implement.ParseChange(summaryJSON, patch, original); err != nil {
		return fmt.Errorf("retrieved change does not apply to its snapshot: %v", err)
	}
	return nil
}

// applyRetrievedChange applies the Run's change from a fresh CLI process and
// requires one commit on the base carrying the Run's provenance.
func applyRetrievedChange(ctx context.Context, auth *AuthFixture, r ReviewChange, destination []string, original *implement.Snapshot, pending implementclient.Submission) error {
	out, err := jbCommand(ctx, auth, r.Binaries.CLI, append([]string{"implement", "apply", "--repo", r.Repo}, destination...)...)
	if err != nil {
		return err
	}
	return checkAppliedCommit(r, out, fmt.Sprintf("impl/run-%d", pending.RunID), original.Digest, pending.RunID)
}

// readCompletedValidation retrieves the validation from a fresh process and
// requires it to record the row's outcome against exactly the retrieved change.
func readCompletedValidation(ctx context.Context, auth *AuthFixture, r ReviewChange, destination []string, change *implement.Summary, original *implement.Snapshot, expected validationCase) error {
	out, err := jbCommand(ctx, auth, r.Binaries.CLI, append([]string{"implement", "result", "--result", implementclient.ValidationResult}, destination...)...)
	if err != nil {
		return err
	}
	var v implement.Validation
	if err := json.Unmarshal(out, &v); err != nil {
		return fmt.Errorf("fresh result has no typed validation: %v", err)
	}
	if err := checkValidation(&v, change, original, expected); err != nil {
		return err
	}
	out, err = jbCommand(ctx, auth, r.Binaries.CLI, append([]string{"implement", "result", "--result", implementclient.ValidationResult, "--format", "markdown"}, destination...)...)
	if err != nil {
		return err
	}
	if heading := "## Validation: " + strings.ReplaceAll(expected.Outcome, "_", " "); !strings.Contains(string(out), heading) || !strings.Contains(string(out), "## Patch") {
		return fmt.Errorf("markdown does not show the change with its validation:\n%s", out)
	}
	return nil
}

// checkValidation requires a retrieved validation to record the row's outcome
// against exactly the retrieved change.
func checkValidation(v *implement.Validation, change *implement.Summary, original *implement.Snapshot, expected validationCase) error {
	if change.RunID == nil || v.RunID != *change.RunID || v.PatchDigest != change.PatchDigest || v.InputDigest != original.Digest || v.Command != expected.Command {
		return fmt.Errorf("validation does not name the Run, change, snapshot and command it ran: %+v", v)
	}
	switch {
	case v.Outcome != expected.Outcome:
		return fmt.Errorf("validation recorded %s, want %s: %s", v.Outcome, expected.Outcome, v.LogTail)
	case expected.Exit < 0 && (v.Applied || v.ExitCode != nil):
		return fmt.Errorf("validation ran a command against a change that did not apply: %+v", v)
	case expected.Exit >= 0 && (!v.Applied || v.ExitCode == nil || *v.ExitCode != expected.Exit):
		return fmt.Errorf("validation did not record the command's exit %d: %+v", expected.Exit, v)
	case expected.Outcome == implement.ValidationFailed && !strings.Contains(v.LogTail, "FAIL: parser_test.go"):
		return fmt.Errorf("validation did not keep the failing command's output: %q", v.LogTail)
	}
	return nil
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
