package review

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

//go:embed codex-version
var codexVersion string

func CodexVersion() string { return strings.TrimSpace(codexVersion) }

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
	if err := requireMemoryRuntime(opts.RuntimeDir); err != nil {
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
	if opts.Timeout <= 0 || opts.Model == "" || opts.Auth == nil {
		return nil, errors.New("model, credential stream and positive timeout are required")
	}
	if opts.RunID != nil && *opts.RunID < 1 {
		return nil, errors.New("run ID must be positive")
	}
	// Provider discovery belongs with execution, outside the command root.
	codex, err := exec.LookPath(opts.Codex)
	if err != nil {
		return nil, errors.New("Codex executable not found")
	}
	opts.Codex = codex
	input, err := filepath.EvalSymlinks(opts.Input)
	if err != nil {
		return nil, err
	}
	bundle, err := LoadBundle(input)
	if err != nil {
		return nil, err
	}
	output, err := filepath.Abs(opts.Output)
	if err != nil {
		return nil, err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(output))
	if err != nil {
		return nil, err
	}
	output = filepath.Join(parent, filepath.Base(output))
	runtimeDir, err := filepath.EvalSymlinks(opts.RuntimeDir)
	if err != nil {
		return nil, err
	}
	if within(bundle.Dir, output) || within(output, bundle.Dir) || within(runtimeDir, output) || within(output, runtimeDir) || within(bundle.Dir, runtimeDir) || within(runtimeDir, bundle.Dir) {
		return nil, errors.New("input, output and runtime must be separate directories")
	}
	if st, err := os.Lstat(output); err == nil {
		if !st.IsDir() {
			return nil, errors.New("output must be a new or empty directory")
		}
		entries, err := os.ReadDir(output)
		if err != nil {
			return nil, err
		}
		if len(entries) > 0 {
			return nil, errors.New("output must be empty; existing reports are immutable")
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if len(opts.Profile) == 0 {
		opts.Profile = DefaultProfile()
	}
	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	finished := make(chan struct{})
	defer close(finished)
	go func() {
		select {
		case <-ctx.Done():
			opts.Auth.Close()
		case <-finished:
		}
	}()
	report, err := reviewSession(ctx, opts, bundle, runtimeDir)
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
	stage, err := os.MkdirTemp(parent, ".review-output-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(stage, "review.json"), append(data, '\n'), 0600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(stage, "review.md"), []byte(report.Markdown()), 0600); err != nil {
		return nil, err
	}
	if err := os.Rename(stage, output); err != nil {
		return nil, err
	}
	return report, nil
}

func reviewSession(ctx context.Context, opts WorkerOptions, bundle *Bundle, parent string) (report *Report, err error) {
	session, err := os.MkdirTemp(parent, "jb-review-")
	if err != nil {
		return nil, err
	}
	defer func() {
		if cleanupErr := os.RemoveAll(session); cleanupErr != nil {
			report = nil
			err = errors.Join(err, errors.New("failed to remove review runtime"))
		}
	}()
	home := filepath.Join(session, "codex")
	workspace := filepath.Join(session, "workspace")
	for _, name := range []string{home, workspace, filepath.Join(session, "tmp")} {
		if err := os.Mkdir(name, 0700); err != nil {
			return nil, err
		}
	}
	auth, err := io.ReadAll(io.LimitReader(opts.Auth, (64<<10)+1))
	if err != nil || len(auth) > 64<<10 || !subscriptionAuth(auth) {
		clear(auth)
		if errors.Is(err, ErrCredentialHandoffTimeout) {
			return nil, err
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("valid Codex subscription auth.json required; reauthenticate locally if needed")
	}
	err = os.WriteFile(filepath.Join(home, "auth.json"), auth, 0600)
	clear(auth)
	if err != nil {
		return nil, errors.New("cannot stage session credentials")
	}
	if !filepath.IsAbs(opts.Codex) || !filepath.IsAbs(opts.ReaderCommand) {
		return nil, errors.New("Codex and input-reader executables must have absolute paths")
	}
	schemaPath := filepath.Join(session, "assessment.schema.json")
	assessmentPath := filepath.Join(session, "assessment.json")
	if err := os.WriteFile(schemaPath, AssessmentSchema(), 0600); err != nil {
		return nil, err
	}
	env := []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + session, "CODEX_HOME=" + home, "TMPDIR=" + filepath.Join(session, "tmp"),
		"XDG_CONFIG_HOME=" + session, "XDG_CACHE_HOME=" + session, "XDG_DATA_HOME=" + session, "XDG_STATE_HOME=" + session, "LANG=C.UTF-8"}
	versionCmd := exec.CommandContext(ctx, opts.Codex, "--version")
	versionCmd.Env = env
	versionCmd.Dir = workspace
	configureProcess(versionCmd)
	version, err := versionCmd.Output()
	stopProcess(versionCmd)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("could not determine Codex version")
	}
	if strings.TrimSpace(string(version)) != "codex-cli "+CodexVersion() {
		return nil, fmt.Errorf("worker requires codex-cli %s", CodexVersion())
	}
	if handoff, ok := opts.Auth.(interface{ Acknowledge() error }); ok {
		if err := handoff.Acknowledge(); err != nil {
			return nil, err
		}
	}
	args := codexArguments(opts, workspace, bundle.Dir, schemaPath, assessmentPath)
	cmd := exec.CommandContext(ctx, opts.Codex, args...)
	cmd.Dir = workspace
	cmd.Env = env
	cmd.Stdin = strings.NewReader(string(opts.Profile) + "\n\nUse only the review_input MCP list/read/search tools to inspect the captured files. If Code Mode is the tool interface, use it only to call these tools and read their results; never evaluate repository code. Paths are relative to that server's input root. Start with manifest.json and change.diff. Do not invoke any shell, repository code execution, editing, web or other tools. Return the assessment using the supplied schema.\n")
	cmd.Stderr = io.Discard // Provider diagnostics can contain credentials. Never forward them.
	configureProcess(cmd)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, errors.New("could not start Codex")
	}
	defer stopProcess(cmd)
	trace := new(reviewTrace)
	scanner := bufio.NewScanner(out)
	scanner.Buffer(make([]byte, 4096), maxFileBytes)
	var traceErr error
	for scanner.Scan() {
		if err := trace.observe(scanner.Bytes()); err != nil {
			traceErr = err
			stopProcess(cmd)
			break
		}
	}
	if scanner.Err() != nil {
		traceErr = errors.New("invalid or oversized Codex event stream")
		stopProcess(cmd)
	}
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if traceErr != nil {
		return nil, traceErr
	}
	if waitErr != nil {
		return nil, errors.New("Codex failed; check subscription authentication and model availability")
	}
	if !trace.completed {
		return nil, errors.New("Codex did not complete the review turn")
	}
	data, err := readFileBounded(assessmentPath, maxFileBytes)
	if err != nil {
		return nil, errors.New("Codex produced no readable assessment")
	}
	return BuildReport(bundle, data, Metadata{RunID: opts.RunID, ModelRequested: opts.Model, CodexVersion: strings.TrimSpace(string(version)), Profile: opts.Profile})
}

func subscriptionAuth(data []byte) bool {
	var auth struct {
		Mode   string  `json:"auth_mode"`
		Key    *string `json:"OPENAI_API_KEY"`
		Tokens struct {
			Access  string `json:"access_token"`
			Refresh string `json:"refresh_token"`
			ID      string `json:"id_token"`
		} `json:"tokens"`
	}
	return json.Unmarshal(data, &auth) == nil && (auth.Mode == "" || auth.Mode == "chatgpt") && auth.Key == nil && auth.Tokens.Access != "" && auth.Tokens.Refresh != "" && auth.Tokens.ID != ""
}

func codexArguments(opts WorkerOptions, workspace, input, schema, output string) []string {
	args := []string{"exec", "--ignore-user-config", "--ignore-rules", "--strict-config", "--ephemeral", "--skip-git-repo-check", "--sandbox", "read-only", "--color", "never", "--json", "--model", opts.Model, "--cd", workspace, "--output-schema", schema, "--output-last-message", output}
	quote := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	readerArgs, _ := json.Marshal([]string{"input-tools", "--input", input})
	for _, setting := range []string{
		`approval_policy="never"`, `forced_login_method="chatgpt"`, `cli_auth_credentials_store="file"`,
		`model_provider="openai"`, `history.persistence="none"`, `project_doc_max_bytes=0`, `web_search="disabled"`,
		`features.shell_tool=false`, `features.unified_exec=false`, `features.shell_snapshot=false`,
		`features.apply_patch_freeform=false`, `features.multi_agent=false`, `features.js_repl=false`,
		`features.apps=false`, `features.plugins=false`, `features.remote_plugin=false`,
		`features.multi_agent_v2=false`, `features.memories=false`, `features.skip_host_skill_discovery=true`,
		`suppress_unstable_features_warning=true`,
		`features.skill_mcp_dependency_install=false`, `shell_environment_policy.inherit="none"`,
		`mcp_servers.review_input.command=` + quote(opts.ReaderCommand),
		`mcp_servers.review_input.args=` + string(readerArgs),
		`mcp_servers.review_input.required=true`,
		`mcp_servers.review_input.enabled_tools=["list","read","search"]`,
	} {
		args = append(args, "-c", setting)
	}
	return append(args, "-")
}

// Raw provider events are checked and discarded, never saved or logged. The
// known event vocabulary is deliberately closed: a newly introduced tool needs
// an explicit review before it can run in this inspection-only worker.
type reviewTrace struct{ completed bool }

func (t *reviewTrace) observe(data []byte) error {
	var event struct {
		Type string `json:"type"`
		Item struct {
			Type   string `json:"type"`
			Server string `json:"server"`
			Tool   string `json:"tool"`
		} `json:"item"`
	}
	if json.Unmarshal(data, &event) != nil {
		return errors.New("invalid Codex event")
	}
	switch event.Type {
	case "thread.started", "turn.started":
		return nil
	case "turn.completed":
		t.completed = true
		return nil
	case "item.started", "item.updated", "item.completed":
		switch event.Item.Type {
		case "reasoning", "agent_message", "todo_list":
			return nil
		case "error":
			return errors.New("Codex reported an error; no report published")
		case "mcp_tool_call":
			if event.Item.Server == "review_input" && (event.Item.Tool == "list" || event.Item.Tool == "read" || event.Item.Tool == "search") {
				return nil
			}
		}
		return errors.New("Codex used a tool outside the inspection policy; no report published")
	case "error", "turn.failed":
		return errors.New("Codex review failed; check subscription authentication and model availability")
	default:
		return errors.New("unsupported Codex event; no report published")
	}
}
