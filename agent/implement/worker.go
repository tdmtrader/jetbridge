package implement

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"time"

	"github.com/concourse/concourse/agent/capture"
	"github.com/concourse/concourse/agent/session"
)

// WorkspaceServer is the name of the workspace tool server in the provider's
// configuration and trace.
const WorkspaceServer = "workspace"

type WorkerOptions struct {
	Input, Output string
	RuntimeDir    string
	Codex         string
	// ToolsCommand is the absolute path of the executable that serves the
	// workspace tools (the worker itself, run as workspace-tools).
	ToolsCommand string
	Model        string
	Profile      []byte
	Auth         io.ReadCloser
	RunID        *int
	Timeout      time.Duration
}

// RunWorker runs one edit-only implementation session in a private,
// memory-backed runtime and publishes change.patch and summary.json once.
// Auth is a separate stream, never part of the snapshot, parameters or model
// conversation. The snapshot is never modified: the provider edits a scratch
// copy, and the patch is the difference between that copy and the base.
func RunWorker(ctx context.Context, opts WorkerOptions) (*Summary, error) {
	if err := session.RequireMemoryRuntime(opts.RuntimeDir); err != nil {
		if opts.Auth != nil {
			opts.Auth.Close()
		}
		return nil, err
	}
	return runInRuntime(ctx, opts)
}

func runInRuntime(ctx context.Context, opts WorkerOptions) (*Summary, error) {
	if opts.Auth != nil {
		defer opts.Auth.Close()
	}
	if err := session.CheckRequest(opts.Model, opts.Auth, opts.Timeout, opts.RunID); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(opts.ToolsCommand) {
		return nil, errors.New("the workspace tools executable must have an absolute path")
	}
	codex, err := exec.LookPath(opts.Codex)
	if err != nil {
		return nil, errors.New("Codex executable not found")
	}
	opts.Codex = codex
	paths, err := session.ResolvePaths(opts.Input, opts.Output, opts.RuntimeDir)
	if err != nil {
		return nil, err
	}
	snapshot, err := LoadSnapshot(paths.Input)
	if err != nil {
		return nil, err
	}
	if len(opts.Profile) == 0 {
		opts.Profile = DefaultProfile()
	}
	brief, err := snapshot.Brief()
	if err != nil {
		return nil, err
	}
	base, err := snapshot.tree()
	if err != nil {
		return nil, err
	}
	start, err := snapshot.start(base)
	if err != nil {
		return nil, err
	}
	ctx, stop := session.Bound(ctx, opts.Timeout, opts.Auth)
	defer stop()
	edited, assessment, version, err := implementSession(ctx, opts, snapshot, start, brief, paths.Runtime)
	if err != nil {
		return nil, err
	}
	// Session credentials and the workspace are destroyed before anything is
	// derived from the edits or published.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	after, err := LoadSnapshot(snapshot.Dir)
	if err != nil {
		return nil, err
	}
	if after.Digest != snapshot.Digest {
		return nil, errors.New("input changed during implementation")
	}
	patch, changes, err := Diff(base, edited)
	if err != nil {
		return nil, err
	}
	verified, err := VerifyPatch(base, edited, patch)
	if err != nil {
		return nil, err
	}
	if len(verified) != len(changes) {
		return nil, errors.New("patch does not reproduce the edited workspace")
	}
	summary, err := BuildSummary(snapshot, patch, changes, assessment, Metadata{RunID: opts.RunID, ModelRequested: opts.Model, CodexVersion: version, Profile: opts.Profile})
	if err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := paths.Publish(map[string][]byte{SummaryFile: append(data, '\n'), PatchFile: patch}); err != nil {
		return nil, err
	}
	return summary, nil
}

const implementInstructions = "\n\nUse only the workspace MCP tools: list, read and search to inspect the repository, and write, replace and delete to change it. If Code Mode is the tool interface, use it only to call these tools and read their results; never evaluate repository code. Paths are relative to the workspace root. Do not invoke any shell, repository code execution, web or other tools. Finish with the assessment using the supplied schema.\n"

const (
	priorInstructions    = "\nThe workspace already contains a prior change, applied to the base: read it as the patch " + PriorInputPath + ". Build on it; your patch is published against the base and carries it forward.\n"
	findingsInstructions = "\nA review of the prior work reported findings: read them as the JSON array " + FindingsInputPath + ". Address each one, or say in limitations why it was not addressed.\n"
)

// instructions are the fixed session instructions, naming the read-only
// inputs the snapshot carries.
func instructions(s *Snapshot) string {
	text := implementInstructions
	if s.Manifest.PriorPatchDigest != nil {
		text += priorInstructions
	}
	if s.Manifest.FindingsDigest != nil {
		text += findingsInstructions
	}
	return text + "\n## Brief\n\n"
}

// implementPolicy is edit-only: the workspace server is the provider's only
// tool, and its write, replace and delete are the only way anything changes.
// The provider itself runs read-only and is offered no file-edit tool of its
// own. When the snapshot carries a prior change or review findings, the
// server also serves them, read-only, from the sealed snapshot at
// snapshotDir.
func implementPolicy(opts WorkerOptions, workspace, snapshotDir, schema, output string) session.Policy {
	args := []string{"workspace-tools", "--root", workspace}
	if snapshotDir != "" {
		args = append(args, "--snapshot", snapshotDir)
	}
	return session.Policy{
		Model: opts.Model, WorkDir: workspace,
		Tools:        session.ToolServer{Name: WorkspaceServer, Command: opts.ToolsCommand, Args: args, Tools: WorkspaceTools, Writes: workspaceWrites},
		OutputSchema: schema, LastMessage: output,
	}
}

// implementSession returns the edited workspace and the model's assessment.
// Both are read into memory before the session, and with it the credential
// and the workspace, is destroyed.
func implementSession(ctx context.Context, opts WorkerOptions, snapshot *Snapshot, start Tree, brief []byte, parent string) (edited Tree, assessment []byte, version string, err error) {
	s, err := session.Open(ctx, session.Options{RuntimeDir: parent, Provider: session.Codex{}, Executable: opts.Codex, Auth: opts.Auth})
	if err != nil {
		return nil, nil, "", err
	}
	defer func() {
		if cleanupErr := s.Close(); cleanupErr != nil {
			edited, assessment, version = nil, nil, ""
			err = errors.Join(err, errors.New("failed to remove implementation runtime"))
		}
	}()
	workspace, err := s.Mkdir("workspace")
	if err != nil {
		return nil, nil, "", err
	}
	if err := populateWorkspace(workspace, start); err != nil {
		return nil, nil, "", err
	}
	schemaPath := filepath.Join(s.Dir, "assessment.schema.json")
	assessmentPath := filepath.Join(s.Dir, "assessment.json")
	if err := os.WriteFile(schemaPath, AssessmentSchema(), 0600); err != nil {
		return nil, nil, "", err
	}
	snapshotDir := ""
	if snapshot.Manifest.PriorPatchDigest != nil || snapshot.Manifest.FindingsDigest != nil {
		snapshotDir = snapshot.Dir
	}
	prompt := string(opts.Profile) + instructions(snapshot) + string(brief)
	if err := s.Run(ctx, implementPolicy(opts, workspace, snapshotDir, schemaPath, assessmentPath), prompt, editItem); err != nil {
		return nil, nil, "", err
	}
	assessment, err = capture.ReadFileBounded(assessmentPath, capture.MaxFileBytes)
	if err != nil {
		return nil, nil, "", errors.New("Codex produced no readable assessment")
	}
	edited, err = readWorkspace(workspace)
	if err != nil {
		return nil, nil, "", err
	}
	return edited, assessment, s.Version, nil
}

// editItem keeps the edit-only vocabulary closed: narration and the
// workspace server's tools, whose handlers confine every edit before it
// happens. Anything else, including a file change the provider reports
// making itself or any command execution, stops the provider.
func editItem(e session.Event) error {
	switch e.Item {
	case session.ItemReasoning, session.ItemMessage, session.ItemTodo:
		return nil
	case session.ItemToolCall:
		if e.Server == WorkspaceServer && slices.Contains(WorkspaceTools, e.Tool) {
			return nil
		}
	}
	return errors.New("Codex used a tool outside the edit-only policy; nothing was published")
}
