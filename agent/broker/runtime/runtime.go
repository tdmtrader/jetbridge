// Package runtime composes the fixed broker engine with local adapter and
// workspace isolation. It has no provider fallback or dynamic profile logic.
package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/broker"
	"github.com/concourse/concourse/agent/broker/adapter"
	"github.com/concourse/concourse/agent/broker/sandbox"
	"github.com/concourse/concourse/agent/broker/workspace"
)

type RunnerConfig struct {
	WorkspaceRoot string
	ScratchRoot   string
	OutputSchemas map[broker.Tool]string
	// SandboxReadPaths are immutable image-owned runtime assets needed by
	// native harnesses. Parent workspace, broker authority, /proc, and the
	// scratch parent are never valid entries.
	SandboxReadPaths []string
	CaptureLimits    workspace.Limits
	Probe            adapter.VersionProbe
}

type Runner struct{ config RunnerConfig }

func NewRunner(config RunnerConfig) (*Runner, error) {
	if err := validateRunnerConfig(config); err != nil {
		return nil, err
	}
	return &Runner{config: config}, nil
}

// Preflight makes every exact configured profile executable before the sidecar
// becomes reachable. Credentials and prompts are never supplied to this step.
func Preflight(ctx context.Context, catalog *broker.Catalog, probe adapter.VersionProbe) error {
	if catalog == nil || probe == nil {
		return fmt.Errorf("broker runtime: catalog and version probe are required")
	}
	seen := map[string]struct{}{}
	for _, visible := range catalog.Visible() {
		for _, tool := range visible.Tools {
			profile, err := catalog.Resolve(tool, broker.Selector{Tier: visible.Tier, Effort: visible.Effort})
			if err != nil {
				return err
			}
			if _, ok := seen[profile.Digest]; ok {
				continue
			}
			seen[profile.Digest] = struct{}{}
			if _, err := adapter.Preflight(ctx, profile, probe); err != nil {
				return fmt.Errorf("broker runtime: preflight profile %q: %w", profile.ID, err)
			}
		}
	}
	return nil
}

func (r *Runner) Run(ctx context.Context, request broker.RunRequest) (broker.RunResult, error) {
	runScratch, err := os.MkdirTemp(r.config.ScratchRoot, "broker-run-")
	if err != nil {
		return broker.RunResult{}, fmt.Errorf("broker runtime: create run scratch: %w", err)
	}
	defer os.RemoveAll(runScratch)
	workdir := runScratch
	if request.Tool == broker.ToolRequestReview {
		if request.Workspace == nil {
			return broker.RunResult{}, fmt.Errorf("broker runtime: authoritative workspace capture is required")
		}
		var cleanup func() error
		workdir, cleanup, err = workspace.Materialize(runScratch, *request.Workspace)
		if err != nil {
			return broker.RunResult{}, err
		}
		defer cleanup()
	}
	schema := r.config.OutputSchemas[request.Tool]
	prepared, err := adapter.Prepare(ctx, request.Profile, adapter.Paths{WorkDir: workdir, ScratchDir: workdir, OutputSchema: schema}, request.Credential, r.config.Probe)
	if err != nil {
		return broker.RunResult{}, fmt.Errorf("broker runtime: prepare adapter: %w", err)
	}
	readPaths := append([]string(nil), r.config.SandboxReadPaths...)
	readPaths = append(readPaths, schema)
	stream, err := adapter.ExecuteSandboxed(
		ctx,
		prepared,
		request.Prompt,
		int(request.Profile.Limits.MaxInputBytes),
		sandbox.Policy{WritableRoot: runScratch, ReadOnlyPaths: readPaths},
	)
	if err != nil {
		return broker.RunResult{}, err
	}
	duration := time.Duration(0)
	if stream.Usage.Duration != nil {
		duration = *stream.Usage.Duration
	}
	return broker.RunResult{Output: stream.Output, Events: stream.Events, Duration: duration, InputTokens: stream.Usage.InputTokens, OutputTokens: stream.Usage.OutputTokens, CostUSD: stream.Usage.CostUSD}, nil
}

// CaptureWorkspace reads the parent mount before child execution. Run only
// materializes this exact result and never re-reads the live parent tree.
func (r *Runner) CaptureWorkspace(ctx context.Context) (workspace.Result, error) {
	if err := ctx.Err(); err != nil {
		return workspace.Result{}, err
	}
	capture, err := workspace.Capture(r.config.WorkspaceRoot, r.config.ScratchRoot, r.config.CaptureLimits)
	if err != nil {
		return workspace.Result{}, fmt.Errorf("broker runtime: capture workspace: %w", err)
	}
	return capture, nil
}

func validateRunnerConfig(config RunnerConfig) error {
	for label, path := range map[string]string{"workspace root": config.WorkspaceRoot, "scratch root": config.ScratchRoot} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) {
			return fmt.Errorf("broker runtime: %s must be an absolute clean non-root path", label)
		}
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("broker runtime: inspect %s: %w", label, err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("broker runtime: %s must be a directory and not a symlink", label)
		}
	}
	if strings.HasPrefix(config.ScratchRoot+string(filepath.Separator), config.WorkspaceRoot+string(filepath.Separator)) || strings.HasPrefix(config.WorkspaceRoot+string(filepath.Separator), config.ScratchRoot+string(filepath.Separator)) {
		return fmt.Errorf("broker runtime: workspace and scratch roots must not overlap")
	}
	if config.CaptureLimits.MaxPatchBytes <= 0 || config.CaptureLimits.MaxEntries <= 0 || config.CaptureLimits.StabilityAttempts <= 0 {
		return fmt.Errorf("broker runtime: positive capture limits are required")
	}
	for _, tool := range []broker.Tool{broker.ToolRequestReview, broker.ToolConsultAgent} {
		if path := config.OutputSchemas[tool]; !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return fmt.Errorf("broker runtime: absolute output schema for %s is required", tool)
		}
	}
	for _, path := range config.SandboxReadPaths {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
			return fmt.Errorf("broker runtime: sandbox read path must be absolute, clean, and non-root")
		}
	}
	if config.Probe == nil {
		return fmt.Errorf("broker runtime: version probe is required")
	}
	return nil
}

var _ broker.Runner = (*Runner)(nil)
