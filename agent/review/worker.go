package review

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/concourse/concourse/agent/capture"
	"github.com/concourse/concourse/agent/session"
)

// CodexVersion is the pinned Codex release every worker mode runs.
func CodexVersion() string { return session.CodexVersion() }

type WorkerOptions struct {
	Input, Output string
	RuntimeDir    string
	Codex         string
	ReaderCommand string
	Model         string
	Profile       []byte
	Auth          io.ReadCloser
	RunID         *int
	Timeout       time.Duration
}

// RunWorker runs one inspection with a private, memory-backed runtime. Auth is a
// separate stream, never part of the bundle, parameters or model conversation.
// The owner-selected source file is not modified. RunWorker does not detach:
// JetBridge's eventual Run integration owns admission, handoff and lifetime.
func RunWorker(ctx context.Context, opts WorkerOptions) (*Report, error) {
	if err := session.RequireMemoryRuntime(opts.RuntimeDir); err != nil {
		if opts.Auth != nil {
			opts.Auth.Close()
		}
		return nil, err
	}
	return runInRuntime(ctx, opts)
}

func runInRuntime(ctx context.Context, opts WorkerOptions) (*Report, error) {
	if opts.Auth != nil {
		defer opts.Auth.Close()
	}
	if err := session.CheckRequest(opts.Model, opts.Auth, opts.Timeout, opts.RunID); err != nil {
		return nil, err
	}
	// Provider discovery belongs with execution, outside the command root.
	codex, err := exec.LookPath(opts.Codex)
	if err != nil {
		return nil, errors.New("Codex executable not found")
	}
	opts.Codex = codex
	paths, err := session.ResolvePaths(opts.Input, opts.Output, opts.RuntimeDir)
	if err != nil {
		return nil, err
	}
	bundle, err := LoadBundle(paths.Input)
	if err != nil {
		return nil, err
	}
	if len(opts.Profile) == 0 {
		opts.Profile = DefaultProfile()
	}
	ctx, stop := session.Bound(ctx, opts.Timeout, opts.Auth)
	defer stop()
	report, err := reviewSession(ctx, opts, bundle, paths.Runtime)
	if err != nil {
		return nil, err
	}
	// Session credentials are already destroyed before any result is published.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	after, err := LoadBundle(bundle.Dir)
	if err != nil {
		return nil, err
	}
	if after.Digest != bundle.Digest {
		return nil, errors.New("input changed during review")
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := paths.Publish(map[string][]byte{"review.json": append(data, '\n'), "review.md": []byte(report.Markdown())}); err != nil {
		return nil, err
	}
	return report, nil
}

const reviewInstructions = "\n\nUse only the review_input MCP list/read/search tools to inspect the captured files. If Code Mode is the tool interface, use it only to call these tools and read their results; never evaluate repository code. Paths are relative to that server's input root. Start with manifest.json and change.diff. Do not invoke any shell, repository code execution, editing, web or other tools. Return the assessment using the supplied schema.\n"

// reviewPolicy is inspection only: a read-only sandbox whose single tool
// server is the private reader over the sealed bundle.
func reviewPolicy(opts WorkerOptions, workspace, input, schema, output string) session.Policy {
	return session.Policy{
		Model: opts.Model, WorkDir: workspace,
		Tools:        session.ToolServer{Name: "review_input", Command: opts.ReaderCommand, Args: []string{"input-tools", "--input", input}, Tools: []string{"list", "read", "search"}},
		OutputSchema: schema, LastMessage: output,
	}
}

func reviewSession(ctx context.Context, opts WorkerOptions, bundle *Bundle, parent string) (report *Report, err error) {
	if !filepath.IsAbs(opts.ReaderCommand) {
		return nil, errors.New("Codex and input-reader executables must have absolute paths")
	}
	s, err := session.Open(ctx, session.Options{RuntimeDir: parent, Provider: session.Codex{}, Executable: opts.Codex, Auth: opts.Auth})
	if err != nil {
		return nil, err
	}
	defer func() {
		if cleanupErr := s.Close(); cleanupErr != nil {
			report = nil
			err = errors.Join(err, errors.New("failed to remove review runtime"))
		}
	}()
	workspace, err := s.Mkdir("workspace")
	if err != nil {
		return nil, err
	}
	schemaPath := filepath.Join(s.Dir, "assessment.schema.json")
	assessmentPath := filepath.Join(s.Dir, "assessment.json")
	if err := os.WriteFile(schemaPath, AssessmentSchema(), 0600); err != nil {
		return nil, err
	}
	policy := reviewPolicy(opts, workspace, bundle.Dir, schemaPath, assessmentPath)
	if err := s.Run(ctx, policy, string(opts.Profile)+reviewInstructions, reviewItem); err != nil {
		return nil, err
	}
	data, err := capture.ReadFileBounded(assessmentPath, maxFileBytes)
	if err != nil {
		return nil, errors.New("Codex produced no readable assessment")
	}
	return BuildReport(bundle, data, Metadata{RunID: opts.RunID, ModelRequested: opts.Model, CodexVersion: s.Version, Profile: opts.Profile})
}

// reviewItem keeps the inspection vocabulary closed: a newly introduced tool
// needs an explicit review before it can run in this inspection-only worker.
func reviewItem(e session.Event) error {
	switch e.Item {
	case session.ItemReasoning, session.ItemMessage, session.ItemTodo:
		return nil
	case session.ItemToolCall:
		if e.Server == "review_input" && (e.Tool == "list" || e.Tool == "read" || e.Tool == "search") {
			return nil
		}
	}
	return errors.New("Codex used a tool outside the inspection policy; no report published")
}
