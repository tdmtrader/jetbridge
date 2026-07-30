// Package workspace creates an exact, reviewable Git representation of the
// parent agent's current workspace without modifying its index.
package workspace

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type Limits struct {
	MaxPatchBytes     int64
	MaxEntries        int
	StabilityAttempts int
}

type Result struct {
	RepositoryRoot string
	BaseCommit     string
	BaseTree       string
	ResultTree     string
	Patch          []byte
	PatchDigest    string
	EntryCount     int
	PolicyRevision string
}

var (
	ErrNotGit            = errors.New("workspace is not a Git worktree")
	ErrUnstable          = errors.New("workspace changed during capture")
	ErrCaptureLimit      = errors.New("workspace capture limit exceeded")
	errVerificationDrift = errors.New("captured patch does not reproduce result tree")
)

// Capture compares two independently built temporary indexes. Returning only
// when they agree makes concurrent workspace mutation a retryable error while
// leaving the caller's real index byte-for-byte untouched.
func Capture(directory string, limits Limits) (Result, error) {
	if limits.MaxPatchBytes <= 0 || limits.MaxEntries <= 0 || limits.StabilityAttempts <= 0 {
		return Result{}, fmt.Errorf("workspace capture: positive patch, entry, and stability limits are required")
	}
	root, err := repositoryRoot(directory)
	if err != nil {
		return Result{}, err
	}
	var previous Result
	for attempt := 0; attempt < limits.StabilityAttempts; attempt++ {
		first, err := captureOnce(root, limits)
		if err != nil {
			return Result{}, err
		}
		second, err := captureOnce(root, limits)
		if err != nil {
			return Result{}, err
		}
		if sameCapture(first, second) {
			return second, nil
		}
		previous = second
	}
	return Result{}, fmt.Errorf("%w after %d attempts (last result tree %s)",
		ErrUnstable, limits.StabilityAttempts, previous.ResultTree)
}

func repositoryRoot(directory string) (string, error) {
	if strings.TrimSpace(directory) == "" {
		return "", fmt.Errorf("%w: directory is required", ErrNotGit)
	}
	output, err := runGit(directory, nil, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrNotGit, err)
	}
	root := strings.TrimSpace(string(output))
	if root == "" || !filepath.IsAbs(root) {
		return "", fmt.Errorf("%w: Git returned an invalid root", ErrNotGit)
	}
	return root, nil
}

func captureOnce(root string, limits Limits) (Result, error) {
	baseCommit, err := gitValue(root, nil, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return Result{}, fmt.Errorf("workspace capture: resolve HEAD: %w", err)
	}
	baseTree, err := gitValue(root, nil, "rev-parse", "--verify", "HEAD^{tree}")
	if err != nil {
		return Result{}, fmt.Errorf("workspace capture: resolve base tree: %w", err)
	}
	scratch, err := os.MkdirTemp("", "concourse-broker-index-")
	if err != nil {
		return Result{}, fmt.Errorf("workspace capture: create scratch: %w", err)
	}
	defer os.RemoveAll(scratch)

	index := filepath.Join(scratch, "index")
	environment := []string{"GIT_INDEX_FILE=" + index}
	if _, err := runGit(root, environment, nil, "read-tree", baseCommit); err != nil {
		return Result{}, fmt.Errorf("workspace capture: initialize temporary index: %w", err)
	}
	if _, err := runGit(root, environment, nil, "add", "-A", "--", "."); err != nil {
		return Result{}, fmt.Errorf("workspace capture: index desired state: %w", err)
	}
	resultTree, err := gitValue(root, environment, "write-tree")
	if err != nil {
		return Result{}, fmt.Errorf("workspace capture: write result tree: %w", err)
	}
	entries, err := runGit(root, environment, nil, "ls-files", "-z")
	if err != nil {
		return Result{}, fmt.Errorf("workspace capture: count result entries: %w", err)
	}
	entryCount := bytes.Count(entries, []byte{0})
	if entryCount > limits.MaxEntries {
		return Result{}, fmt.Errorf("%w: %d entries exceeds %d", ErrCaptureLimit, entryCount, limits.MaxEntries)
	}
	patch, err := runGit(root, environment, nil,
		"diff", "--cached", "--binary", "--full-index", "--no-ext-diff", "--no-renames", baseCommit, "--")
	if err != nil {
		return Result{}, fmt.Errorf("workspace capture: create patch: %w", err)
	}
	if int64(len(patch)) > limits.MaxPatchBytes {
		return Result{}, fmt.Errorf("%w: patch has %d bytes, limit is %d",
			ErrCaptureLimit, len(patch), limits.MaxPatchBytes)
	}
	if err := verifyPatch(root, scratch, baseCommit, baseTree, resultTree, patch); err != nil {
		return Result{}, err
	}
	sum := sha256.Sum256(patch)
	return Result{
		RepositoryRoot: root,
		BaseCommit:     baseCommit,
		BaseTree:       baseTree,
		ResultTree:     resultTree,
		Patch:          append([]byte(nil), patch...),
		PatchDigest:    "sha256:" + hex.EncodeToString(sum[:]),
		EntryCount:     entryCount,
		PolicyRevision: "git-workspace-capture/v1",
	}, nil
}

func verifyPatch(root, scratch, baseCommit, baseTree, resultTree string, patch []byte) error {
	if len(patch) == 0 {
		if resultTree != baseTree {
			return errVerificationDrift
		}
		return nil
	}
	index := filepath.Join(scratch, "verify-index")
	environment := []string{"GIT_INDEX_FILE=" + index}
	if _, err := runGit(root, environment, nil, "read-tree", baseCommit); err != nil {
		return fmt.Errorf("workspace capture: initialize verification index: %w", err)
	}
	if _, err := runGit(root, environment, patch,
		"apply", "--cached", "--binary", "--whitespace=nowarn", "-"); err != nil {
		return fmt.Errorf("workspace capture: verify patch application: %w", err)
	}
	verified, err := gitValue(root, environment, "write-tree")
	if err != nil {
		return fmt.Errorf("workspace capture: write verification tree: %w", err)
	}
	if verified != resultTree {
		return fmt.Errorf("%w: got %s, want %s", errVerificationDrift, verified, resultTree)
	}
	return nil
}

func sameCapture(left, right Result) bool {
	return left.BaseCommit == right.BaseCommit &&
		left.BaseTree == right.BaseTree &&
		left.ResultTree == right.ResultTree &&
		left.EntryCount == right.EntryCount &&
		bytes.Equal(left.Patch, right.Patch)
}

func gitValue(root string, environment []string, arguments ...string) (string, error) {
	output, err := runGit(root, environment, nil, arguments...)
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(output))
	if value == "" {
		return "", fmt.Errorf("git %s returned an empty value", strings.Join(arguments, " "))
	}
	return value, nil
}

func runGit(root string, environment []string, stdin []byte, arguments ...string) ([]byte, error) {
	command := exec.Command("git", arguments...)
	command.Dir = root
	command.Env = append(os.Environ(),
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=core.quotePath",
		"GIT_CONFIG_VALUE_0=false",
	)
	command.Env = append(command.Env, environment...)
	if stdin != nil {
		command.Stdin = bytes.NewReader(stdin)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = strconv.Quote(err.Error())
		}
		return nil, fmt.Errorf("git %s: %s", strings.Join(arguments, " "), message)
	}
	return output, nil
}
