package steps

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
	"syscall"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	reviewclient "github.com/concourse/concourse/agent/review/client"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2"
)

type reviewSubmitter interface {
	Submit(context.Context, reviewclient.SubmitOptions) (reviewclient.Submission, error)
}

func ReviewSubmitDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{brine.DefineMapUsing[RunInputAdmission, RunInputAdmission]("a saved review submission encounters {string}", []string{"auth-server", "real-cluster", "review-binaries", "review-workspace"}, func(in RunInputAdmission, p brine.Params, rec *brine.Recorder, res brine.Resources) (RunInputAdmission, error) {
		mode, _ := p.GetString(0)
		return in, exerciseReviewSubmit(in, mode, rec, res)
	})}
}

func exerciseReviewSubmit(in RunInputAdmission, mode string, rec *brine.Recorder, res brine.Resources) error {
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
	makeClient := func() (reviewSubmitter, error) {
		transport := oauth2.NewClient(context.WithValue(context.Background(), oauth2.HTTPClient, auth.Client), oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token.Value, TokenType: token.Type}))
		client, err := reviewclient.New(auth.URL, transport)
		if err != nil {
			return nil, err
		}
		port, ok := any(client).(reviewSubmitter)
		if !ok {
			return nil, fmt.Errorf("shared review client cannot submit with a durable local receipt")
		}
		return port, nil
	}
	client, err := makeClient()
	if err != nil {
		return err
	}
	change, err := newReviewChange(res)
	if err != nil {
		return err
	}
	if _, stderr, err := reviewCommand(change.Binaries.CLI, change.captureArgs(change.Input), ""); err != nil {
		return fmt.Errorf("capture: %v: %s", err, stderr)
	}
	authFile := filepath.Join(change.Workspace.Root, "auth.json")
	if err := os.WriteFile(authFile, []byte(reviewSyntheticAuth), 0600); err != nil {
		return err
	}
	options := reviewclient.SubmitOptions{Team: "output-start", Template: "input-review", Input: change.Input, Receipt: filepath.Join(change.Workspace.Root, "request.json"), AuthFile: authFile}
	if mode == "the installed review template" {
		if _, err := auth.fly("set-pipeline", "--non-interactive", "--team", options.Team, "-p", options.Template, "-c", filepath.Join(repoRoot(), "deploy", "review-template.yml"), "-v", "review_worker_image=example.test/reviewer@sha256:"+strings.Repeat("a", 64), "-v", "review_model=brine-model"); err != nil {
			return err
		}
	}
	if strings.HasPrefix(mode, "ready ") {
		// A completed review receives the owner's credentials, which are
		// delivered only into an operator-pinned worker image; the Run
		// snapshots its producer's image when the submission creates it.
		if in.Template, err = pinProducerImage(in.Source.Start.DB.TeamFactory, in.Template, brineCredentialWorkerImage); err != nil {
			return err
		}
	}
	var before int
	count := func() (int, error) {
		var n int
		err := in.Source.Start.DB.Conn.QueryRow(`SELECT count(*) FROM pipeline_runs WHERE template_pipeline_id=$1`, in.Template.ID()).Scan(&n)
		return n, err
	}
	before, err = count()
	if err != nil {
		return err
	}
	if mode == "a receipt inside the input" {
		options.Receipt = filepath.Join(change.Input, "request.json")
		if _, err := client.Submit(context.Background(), options); err == nil {
			return fmt.Errorf("submission wrote state into its own input bundle")
		}
		after, err := count()
		if err != nil || after != before {
			return fmt.Errorf("invalid receipt location admitted a Run: %v", err)
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	type outcome struct {
		result reviewclient.Submission
		err    error
	}
	done := make(chan outcome, 1)
	go func() { result, err := client.Submit(ctx, options); done <- outcome{result, err} }()
	// Observe the actual persisted receipt, not a sleep or an injected callback.
	var receipt struct {
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
	if mode == "the installed review template" {
		definition, found, err := db.NewPipelineRunFactory(in.Source.Start.DB.Conn, in.Source.Start.DB.LockFactory).Definition(receipt.RunID)
		if err != nil || !found || len(definition.Materialized.Jobs) != 1 {
			return fmt.Errorf("installed review template has no single-job definition: %v", err)
		}
		task, ok := definition.Materialized.Jobs[0].PlanSequence[0].Config.(*atc.TaskStep)
		if !ok || task.RunResult == nil || task.RunResult.Name != "findings" || len(task.RunInputs) != 1 || task.RunInputs[0].Name != "change" || task.Config.Run.Path != "/bin/sh" {
			return fmt.Errorf("installed template lost its review input, result or launcher")
		}
		args := task.Config.Run.Args
		if len(args) != 5 || args[3] != "brine-model" || args[4] != fmt.Sprintf("--run-id=%d", receipt.RunID) {
			return fmt.Errorf("installed template did not preserve the operator model and admitted Run ID: %v", args)
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
	if st.Mode().Perm() != 0600 || bytes.Contains(data, []byte("synthetic-")) || bytes.Contains(data, []byte("bearer")) || bytes.Contains(data, []byte("auth.json")) || receipt.Key == "" || receipt.SourceID == "" {
		return fmt.Errorf("local receipt is unsafe or incomplete")
	}
	if strings.HasPrefix(mode, "ready ") {
		return finishSubmittedReview(in, auth, change, options, first.result, strings.TrimPrefix(mode, "ready "), rec, res)
	}
	if mode == "a fresh CLI replay" {
		if err := exerciseSubmitCLI(auth, change, "review", options, first.result); err != nil {
			return err
		}
	} else if mode == "a changed input through MCP" {
		if err := exerciseSubmitMCP(auth, change, options); err != nil {
			return err
		}
	} else if mode != "interruption before readiness" && mode != "the installed review template" {
		fresh, err := makeClient()
		if err != nil {
			return err
		}
		switch mode {
		case "a changed input":
			if err := os.WriteFile(filepath.Join(change.Input, "plan.md"), []byte("changed input"), 0600); err != nil {
				return err
			}
		case "a different template":
			options.Template = "another-template"
		}
		retryCtx, retryCancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer retryCancel()
		replayed, err := fresh.Submit(retryCtx, options)
		if err == nil {
			return fmt.Errorf("unready or conflicting submission was reported complete")
		}
		if mode == "a fresh client replay" && (replayed.RunID != first.result.RunID || replayed.Handle != first.result.Handle || !errors.Is(err, context.DeadlineExceeded)) {
			return fmt.Errorf("fresh submit lost the original pending Run: %v", err)
		}
	}
	after, err := count()
	if err != nil || after != before+1 {
		return fmt.Errorf("submission admitted %d Runs, want one: %v", after-before, err)
	}
	var claims int
	if err := in.Source.Start.DB.Conn.QueryRow(`SELECT count(*) FROM pipeline_run_credential_handoffs WHERE run_id=$1`, receipt.RunID).Scan(&claims); err != nil || claims != 0 {
		return fmt.Errorf("unstarted review claimed credentials: %v", err)
	}
	return nil
}

// exerciseSubmitCLI interrupts `jb <workload> submit` once it holds the
// receipt lock, and requires it to report the saved Run.
func exerciseSubmitCLI(auth *AuthFixture, change ReviewChange, workload string, options reviewclient.SubmitOptions, want reviewclient.Submission) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, change.Binaries.CLI, workload, "submit", "--target", "auth", "--team", options.Team, "--template", options.Template, "--input", options.Input, "--receipt", options.Receipt, "--auth-file", options.AuthFile)
	command.Env = append(os.Environ(), "FLY_HOME="+auth.Home)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	joined := false
	defer func() {
		if !joined {
			_ = command.Process.Kill()
			<-done
		}
	}()
	lock, err := os.OpenFile(options.Receipt+".lock", os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	for {
		err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			break
		}
		if err != nil {
			return err
		}
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		select {
		case err := <-done:
			joined = true
			return fmt.Errorf("CLI never resumed its receipt: %v: %s", err, stderr.Bytes())
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := command.Process.Signal(os.Interrupt); err != nil {
		return err
	}
	err = <-done
	joined = true
	var got reviewclient.Submission
	if err == nil || json.Unmarshal(stdout.Bytes(), &got) != nil || got.RunID != want.RunID || got.Handle != want.Handle || got.Ready {
		return fmt.Errorf("CLI interruption lost its Run: %v: %s", err, stderr.Bytes())
	}
	return nil
}

func exerciseSubmitMCP(auth *AuthFixture, change ReviewChange, options reviewclient.SubmitOptions) error {
	if err := os.WriteFile(filepath.Join(options.Input, "plan.md"), []byte("changed input"), 0600); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, change.Binaries.CLI, "review", "mcp", "--target", "auth", "--team", options.Team, "--template", options.Template, "--auth-file", options.AuthFile)
	command.Env = append(os.Environ(), "FLY_HOME="+auth.Home)
	session, err := sdk.NewClient(&sdk.Implementation{Name: "brine-submit", Version: "1"}, nil).Connect(ctx, &sdk.CommandTransport{Command: command}, nil)
	if err != nil {
		return err
	}
	defer session.Close()
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		return err
	}
	found := false
	for _, tool := range listed.Tools {
		if tool.Name != "review_submit" {
			continue
		}
		found = true
		schema, err := json.Marshal(tool.InputSchema)
		if err != nil || tool.OutputSchema == nil || bytes.Contains(schema, []byte("auth_file")) {
			return fmt.Errorf("MCP submit has no safe typed artifact interface")
		}
	}
	if !found {
		return fmt.Errorf("MCP has no review_submit tool")
	}
	result, err := session.CallTool(ctx, &sdk.CallToolParams{Name: "review_submit", Arguments: map[string]any{"input": options.Input, "receipt": options.Receipt}})
	if err != nil || result == nil || !result.IsError || result.StructuredContent != nil {
		return fmt.Errorf("MCP accepted changed input: %v", err)
	}
	for _, content := range result.Content {
		if text, ok := content.(*sdk.TextContent); ok && bytes.Contains([]byte(text.Text), []byte("digest mismatch")) {
			return nil
		}
	}
	return fmt.Errorf("MCP did not use the shared bundle validation")
}
