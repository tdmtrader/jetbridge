package steps

// The implement tools of `jb mcp`, the one local stdio MCP server for every
// detached workload, driven as a local agent would: a fresh process started
// under the saved fly login, with its destination and auth file fixed at
// startup.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/concourse/concourse/agent/detached"
	"github.com/concourse/concourse/agent/implement"
	implementclient "github.com/concourse/concourse/agent/implement/client"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// detachedTools is every tool `jb mcp` serves; `jb review mcp` serves only
// the review ones.
const (
	detachedTools = "implement_result implement_status implement_submit review_result review_status review_submit"
	reviewTools   = "review_result review_status review_submit"
)

// jbMCP starts `jb mcp` for the submission's destination. An empty authFile
// starts a reader that cannot submit.
func jbMCP(ctx context.Context, auth *AuthFixture, cli, name string, options implementclient.SubmitOptions, authFile string) (*sdk.ClientSession, error) {
	args := []string{"mcp", "--target", "auth", "--team", options.Team, "--implement-template", options.Template}
	if authFile != "" {
		args = append(args, "--auth-file", authFile)
	}
	return startJBMCP(ctx, auth, cli, name, args...)
}

func startJBMCP(ctx context.Context, auth *AuthFixture, cli, name string, args ...string) (*sdk.ClientSession, error) {
	cmd := exec.CommandContext(ctx, cli, args...)
	cmd.Env = append(os.Environ(), "FLY_HOME="+auth.Home)
	return sdk.NewClient(&sdk.Implementation{Name: name, Version: "1"}, nil).Connect(ctx, &sdk.CommandTransport{Command: cmd}, nil)
}

// callJBTool calls a tool that must succeed and decodes its typed output.
func callJBTool(ctx context.Context, session *sdk.ClientSession, name string, arguments map[string]any, out any) error {
	response, err := session.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if response.IsError || response.StructuredContent == nil {
		return fmt.Errorf("%s returned no typed result: %s", name, toolResponseText(response))
	}
	data, err := json.Marshal(response.StructuredContent)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func toolResponseText(response *sdk.CallToolResult) string {
	var text []string
	for _, content := range response.Content {
		if t, ok := content.(*sdk.TextContent); ok {
			text = append(text, t.Text)
		}
	}
	return strings.Join(text, "\n")
}

// listJBTools returns the sorted tool names a server lists, requiring every
// tool to have a typed output and no credential argument.
func listJBTools(ctx context.Context, session *sdk.ClientSession) (string, error) {
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		return "", err
	}
	var names []string
	for _, tool := range listed.Tools {
		schema, err := json.Marshal(tool.InputSchema)
		if err != nil {
			return "", err
		}
		if tool.OutputSchema == nil || bytes.Contains(schema, []byte("auth")) {
			return "", fmt.Errorf("MCP tool %s has no safe typed interface", tool.Name)
		}
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	return strings.Join(names, " "), nil
}

// exerciseImplementSubmitMCP resumes the interrupted, unready submission from
// a fresh `jb mcp` process with the same receipt. The server takes the
// receipt and waits on the saved Run's credential session; cancelling the
// call reaches the server, which lets the receipt go. The same server then
// observes the saved Run, still pending, and the receipt is unchanged. An
// existing `jb review mcp` configuration for the same destination still
// serves only the review tools.
func exerciseImplementSubmitMCP(auth *AuthFixture, change ImplementChange, options implementclient.SubmitOptions, saved []byte, runID, number int) error {
	cli := change.Review.Binaries.CLI
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	review, err := startJBMCP(ctx, auth, cli, "brine-review", "review", "mcp", "--target", "auth", "--team", options.Team, "--template", options.Template, "--auth-file", options.AuthFile)
	if err != nil {
		return err
	}
	names, err := listJBTools(ctx, review)
	if err != nil {
		return err
	}
	if names != reviewTools {
		return fmt.Errorf("jb review mcp serves %s, want only %s", names, reviewTools)
	}
	if err := review.Close(); err != nil {
		return err
	}

	session, err := jbMCP(ctx, auth, cli, "brine-submit", options, options.AuthFile)
	if err != nil {
		return err
	}
	defer session.Close()
	if names, err = listJBTools(ctx, session); err != nil {
		return err
	}
	if names != detachedTools {
		return fmt.Errorf("jb mcp serves %s, want %s", names, detachedTools)
	}
	call, stop := context.WithCancel(ctx)
	defer stop()
	done := make(chan error, 1)
	go func() {
		response, err := session.CallTool(call, &sdk.CallToolParams{Name: "implement_submit", Arguments: map[string]any{"input": options.Input, "receipt": options.Receipt}})
		if err == nil {
			err = fmt.Errorf("an unready submission returned: %s", toolResponseText(response))
		}
		done <- err
	}()
	if err := awaitReceiptLock(ctx, options.Receipt, true, done); err != nil {
		return fmt.Errorf("MCP never resumed its receipt: %w", err)
	}
	stop()
	if err := <-done; !errors.Is(err, context.Canceled) {
		return fmt.Errorf("MCP submission was not cancelled: %v", err)
	}
	if err := awaitReceiptLock(ctx, options.Receipt, false, nil); err != nil {
		return fmt.Errorf("a cancelled MCP submission kept its receipt: %w", err)
	}
	var run detached.RunObservation
	if err := callJBTool(ctx, session, "implement_status", map[string]any{"run": number}, &run); err != nil {
		return err
	}
	if run.ID != runID || run.Number != number || run.Terminal != nil {
		return fmt.Errorf("MCP does not observe the saved, pending Run: %+v", run)
	}
	if err := session.Close(); err != nil {
		return err
	}
	after, err := os.ReadFile(options.Receipt)
	if err != nil {
		return err
	}
	if !bytes.Equal(after, saved) {
		return fmt.Errorf("MCP replay rewrote the saved receipt")
	}
	return nil
}

// awaitReceiptLock waits until another process holds the receipt's lock, or
// until none does. done, when set, reports the holder exiting first.
func awaitReceiptLock(ctx context.Context, receipt string, held bool, done <-chan error) error {
	for {
		locked, err := receiptLocked(receipt)
		if err != nil {
			return err
		}
		if locked == held {
			return nil
		}
		select {
		case err := <-done:
			return fmt.Errorf("exited first: %v", err)
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func receiptLocked(receipt string) (bool, error) {
	lock, err := os.OpenFile(receipt+".lock", os.O_RDWR, 0600)
	if err != nil {
		return false, err
	}
	defer lock.Close()
	err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return false, syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
}

// readCompletedImplementationMCP reads the completed Run from a fresh `jb mcp`
// reader, started without an auth file, and applies its change with the CLI.
func readCompletedImplementationMCP(ctx context.Context, in RunInputAdmission, auth *AuthFixture, change ImplementChange, options implementclient.SubmitOptions, pending implementclient.Submission, original *implement.Snapshot, expected validationCase) error {
	r := change.Review
	session, err := jbMCP(ctx, auth, r.Binaries.CLI, "brine-fresh", options, "")
	if err != nil {
		return err
	}
	defer session.Close()
	number := pending.Handle.Number
	var run detached.RunObservation
	if err := callJBTool(ctx, session, "implement_status", map[string]any{"run": number}, &run); err != nil {
		return err
	}
	if err := checkImplementTerminal(in, pending, run.ID, run.Terminal); err != nil {
		return err
	}
	var fetched implementclient.ResultOutput
	if err := callJBTool(ctx, session, "implement_result", map[string]any{"run": number}, &fetched); err != nil {
		return err
	}
	if fetched.Result != implementclient.ChangeResult || fetched.Summary == nil || fetched.Patch == nil || fetched.Validation != nil {
		return fmt.Errorf("MCP result is not the Run's change: %+v", fetched)
	}
	summaryJSON, err := json.Marshal(fetched.Summary)
	if err != nil {
		return err
	}
	if err := checkRetrievedChange(pending, original, fetched.Summary, summaryJSON, []byte(*fetched.Patch)); err != nil {
		return err
	}
	// A failing validation is a typed result, not a tool error.
	var attested implementclient.ResultOutput
	if err := callJBTool(ctx, session, "implement_result", map[string]any{"run": number, "result": implementclient.ValidationResult}, &attested); err != nil {
		return err
	}
	if attested.Result != implementclient.ValidationResult || attested.Validation == nil || attested.Summary != nil || attested.Patch != nil {
		return fmt.Errorf("MCP result is not the Run's validation: %+v", attested)
	}
	if err := checkValidation(attested.Validation, fetched.Summary, original, expected); err != nil {
		return err
	}
	// A reader started without an auth file cannot submit.
	response, err := session.CallTool(ctx, &sdk.CallToolParams{Name: "implement_submit", Arguments: map[string]any{"input": options.Input, "receipt": options.Receipt}})
	if err != nil || !response.IsError || response.StructuredContent != nil || !strings.Contains(toolResponseText(response), "--auth-file") {
		return fmt.Errorf("an MCP reader without an auth file submitted: %v", err)
	}
	if err := session.Close(); err != nil {
		return err
	}
	destination := []string{"--target", "auth", "--team", options.Team, "--template", options.Template, "--run", strconv.Itoa(number)}
	return applyRetrievedChange(ctx, auth, r, destination, original, pending)
}
