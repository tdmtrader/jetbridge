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
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type Limits struct {
	MaxPatchBytes     int64
	MaxEntries        int
	StabilityAttempts int
}

// Observation records one stable, policy-relevant workspace path state. A
// deleted path is included as a deletion record; an excluded path is never
// present in ResultTree.
type Observation struct {
	Path      string
	Mode      string
	Included  bool
	Excluded  bool
	Deleted   bool
	Binary    bool
	Symlink   bool
	Submodule bool
	Staged    bool
	Unstaged  bool
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
	Observations   []Observation
}

// Materialize creates a clean, disposable worktree for a captured result.
// It intentionally clones from the repository's Git object database and
// applies the verified patch; it never exposes the parent's live working tree
// to a child harness. The returned cleanup must be called when the synchronous
// child execution finishes.
func Materialize(scratchRoot string, capture Result) (string, func() error, error) {
	if err := validateMaterialization(scratchRoot, capture); err != nil {
		return "", nil, err
	}
	workdir, err := os.MkdirTemp(scratchRoot, "broker-review-")
	if err != nil {
		return "", nil, fmt.Errorf("workspace materialize: create workdir: %w", err)
	}
	cleanup := func() error { return os.RemoveAll(workdir) }
	fail := func(err error) (string, func() error, error) {
		_ = cleanup()
		return "", nil, err
	}
	if _, err := runGit(scratchRoot, nil, nil, "clone", "--quiet", "--no-local", "--no-checkout", capture.RepositoryRoot, workdir); err != nil {
		return fail(fmt.Errorf("workspace materialize: clone base: %w", err))
	}
	if _, err := runGit(workdir, nil, nil, "checkout", "--detach", "--force", capture.BaseCommit); err != nil {
		return fail(fmt.Errorf("workspace materialize: checkout base: %w", err))
	}
	if len(capture.Patch) > 0 {
		if _, err := runGit(workdir, nil, capture.Patch, "apply", "--cached", "--binary", "--whitespace=nowarn", "-"); err != nil {
			return fail(fmt.Errorf("workspace materialize: apply capture: %w", err))
		}
		if _, err := runGit(workdir, nil, nil, "checkout-index", "-a", "-f"); err != nil {
			return fail(fmt.Errorf("workspace materialize: write captured worktree: %w", err))
		}
	}
	tree, err := gitValue(workdir, nil, "write-tree")
	if err != nil {
		return fail(fmt.Errorf("workspace materialize: verify tree: %w", err))
	}
	if tree != capture.ResultTree {
		return fail(fmt.Errorf("workspace materialize: result tree mismatch"))
	}
	return workdir, cleanup, nil
}

func validateMaterialization(scratchRoot string, capture Result) error {
	if !filepath.IsAbs(scratchRoot) || filepath.Clean(scratchRoot) != scratchRoot || scratchRoot == string(filepath.Separator) {
		return fmt.Errorf("workspace materialize: scratch root must be an absolute clean non-root directory")
	}
	info, err := os.Lstat(scratchRoot)
	if err != nil {
		return fmt.Errorf("workspace materialize: inspect scratch root: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("workspace materialize: scratch root must be a directory and not a symlink")
	}
	if !filepath.IsAbs(capture.RepositoryRoot) || strings.TrimSpace(capture.BaseCommit) == "" ||
		strings.TrimSpace(capture.ResultTree) == "" || !captureDigestPattern.MatchString(capture.PatchDigest) {
		return fmt.Errorf("workspace materialize: capture identity is incomplete")
	}
	return nil
}

var (
	ErrNotGit            = errors.New("workspace is not a Git worktree")
	ErrUnstable          = errors.New("workspace changed during capture")
	ErrCaptureLimit      = errors.New("workspace capture limit exceeded")
	ErrConflictingState  = errors.New("workspace has conflicting staged and unstaged changes")
	ErrDirtySubmodule    = errors.New("workspace has dirty submodule contents")
	errVerificationDrift = errors.New("captured patch does not reproduce result tree")
)

const policyRevision = "git-workspace-capture/v2"

var captureDigestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// Capture defines the desired tree as the caller's staged tree plus unstaged
// changes on paths not already staged. A path changed in both states is
// ambiguous and fails closed rather than selecting either version. Capture
// compares two independently built temporary indexes. Returning only when
// they agree makes concurrent workspace mutation a retryable error while
// leaving the caller's real index byte-for-byte untouched.
func Capture(directory, scratchRoot string, limits Limits) (Result, error) {
	if limits.MaxPatchBytes <= 0 || limits.MaxEntries <= 0 || limits.StabilityAttempts <= 0 {
		return Result{}, fmt.Errorf("workspace capture: positive patch, entry, and stability limits are required")
	}
	root, err := repositoryRoot(directory)
	if err != nil {
		return Result{}, err
	}
	if err := validateCaptureScratch(root, scratchRoot); err != nil {
		return Result{}, err
	}
	var previous Result
	for attempt := 0; attempt < limits.StabilityAttempts; attempt++ {
		first, err := captureOnce(root, scratchRoot, limits)
		if err != nil {
			return Result{}, err
		}
		second, err := captureOnce(root, scratchRoot, limits)
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

func validateCaptureScratch(repositoryRoot, scratchRoot string) error {
	if !filepath.IsAbs(scratchRoot) || filepath.Clean(scratchRoot) != scratchRoot || scratchRoot == string(filepath.Separator) {
		return fmt.Errorf("workspace capture: scratch root must be an absolute clean non-root directory")
	}
	info, err := os.Lstat(scratchRoot)
	if err != nil {
		return fmt.Errorf("workspace capture: inspect scratch root: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("workspace capture: scratch root must be a directory and not a symlink")
	}
	if pathContains(repositoryRoot, scratchRoot) || pathContains(scratchRoot, repositoryRoot) {
		return fmt.Errorf("workspace capture: scratch root and repository must not overlap")
	}
	return nil
}

func pathContains(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
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

func captureOnce(root, scratchRoot string, limits Limits) (Result, error) {
	baseCommit, err := gitValue(root, nil, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return Result{}, fmt.Errorf("workspace capture: resolve HEAD: %w", err)
	}
	baseTree, err := gitValue(root, nil, "rev-parse", "--verify", baseCommit+"^{tree}")
	if err != nil {
		return Result{}, fmt.Errorf("workspace capture: resolve base tree: %w", err)
	}
	state, err := inspectWorkspace(root, baseCommit)
	if err != nil {
		return Result{}, err
	}
	if len(state.dirtySubmodules) > 0 {
		return Result{}, fmt.Errorf("%w: %s", ErrDirtySubmodule, strings.Join(state.dirtySubmodules, ", "))
	}
	if paths := conflicts(state.staged, state.unstaged); len(paths) > 0 {
		return Result{}, fmt.Errorf("%w: %s", ErrConflictingState, strings.Join(paths, ", "))
	}
	scratch, err := os.MkdirTemp(scratchRoot, "concourse-broker-capture-")
	if err != nil {
		return Result{}, fmt.Errorf("workspace capture: create scratch: %w", err)
	}
	defer os.RemoveAll(scratch)

	sourceObjects, err := gitObjectDirectory(root)
	if err != nil {
		return Result{}, err
	}
	scratchObjects := filepath.Join(scratch, "objects")
	if err := os.Mkdir(scratchObjects, 0o700); err != nil {
		return Result{}, fmt.Errorf("workspace capture: create scratch object database: %w", err)
	}
	index := filepath.Join(scratch, "index")
	environment := []string{
		"GIT_INDEX_FILE=" + index,
		"GIT_OBJECT_DIRECTORY=" + scratchObjects,
		"GIT_ALTERNATE_OBJECT_DIRECTORIES=" + sourceObjects,
	}
	if _, err := runGit(root, environment, nil, "read-tree", baseCommit); err != nil {
		return Result{}, fmt.Errorf("workspace capture: initialize temporary index: %w", err)
	}
	stagedPatch, err := runGit(root, nil, nil,
		"diff", "--cached", "--binary", "--full-index", "--no-ext-diff", "--no-renames", baseCommit, "--")
	if err != nil {
		return Result{}, fmt.Errorf("workspace capture: read caller index: %w", err)
	}
	if len(stagedPatch) > 0 {
		if _, err := runGit(root, environment, stagedPatch,
			"apply", "--cached", "--binary", "--whitespace=nowarn", "-"); err != nil {
			return Result{}, fmt.Errorf("workspace capture: copy caller index: %w", err)
		}
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
	observations, err := buildObservations(root, environment, baseCommit, state)
	if err != nil {
		return Result{}, err
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
		PolicyRevision: policyRevision,
		Observations:   observations,
	}, nil
}

func gitObjectDirectory(root string) (string, error) {
	value, err := gitValue(root, nil, "rev-parse", "--git-path", "objects")
	if err != nil {
		return "", fmt.Errorf("workspace capture: resolve source object database: %w", err)
	}
	if !filepath.IsAbs(value) {
		value = filepath.Join(root, value)
	}
	value = filepath.Clean(value)
	info, err := os.Lstat(value)
	if err != nil {
		return "", fmt.Errorf("workspace capture: inspect source object database: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workspace capture: source object database is not a directory")
	}
	if strings.ContainsRune(value, os.PathListSeparator) {
		return "", fmt.Errorf("workspace capture: source object database path contains a path-list separator")
	}
	return value, nil
}

type workspaceState struct {
	staged          map[string]struct{}
	unstaged        map[string]struct{}
	excluded        map[string]struct{}
	dirtySubmodules []string
}

func inspectWorkspace(root, baseCommit string) (workspaceState, error) {
	staged, err := changedPaths(root, "diff", "--name-only", "-z", "--no-renames", "--cached", baseCommit, "--")
	if err != nil {
		return workspaceState{}, fmt.Errorf("workspace capture: inspect staged changes: %w", err)
	}
	unstaged, err := changedPaths(root, "diff", "--name-only", "-z", "--no-renames", "--")
	if err != nil {
		return workspaceState{}, fmt.Errorf("workspace capture: inspect unstaged changes: %w", err)
	}
	untracked, err := changedPaths(root, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return workspaceState{}, fmt.Errorf("workspace capture: inspect untracked changes: %w", err)
	}
	for path := range untracked {
		unstaged[path] = struct{}{}
	}
	excluded, err := changedPaths(root, "ls-files", "--others", "--ignored", "--exclude-standard", "-z")
	if err != nil {
		return workspaceState{}, fmt.Errorf("workspace capture: inspect excluded changes: %w", err)
	}
	dirtySubmodules, err := dirtySubmodulePaths(root)
	if err != nil {
		return workspaceState{}, err
	}
	return workspaceState{
		staged:          staged,
		unstaged:        unstaged,
		excluded:        excluded,
		dirtySubmodules: dirtySubmodules,
	}, nil
}

func conflicts(staged, unstaged map[string]struct{}) []string {
	conflicts := make([]string, 0)
	for path := range staged {
		if _, ok := unstaged[path]; ok {
			conflicts = append(conflicts, path)
		}
	}
	sort.Strings(conflicts)
	return conflicts
}

func changedPaths(root string, arguments ...string) (map[string]struct{}, error) {
	output, err := runGit(root, nil, nil, arguments...)
	if err != nil {
		return nil, err
	}
	paths := make(map[string]struct{})
	for _, path := range bytes.Split(output, []byte{0}) {
		if len(path) != 0 {
			paths[string(path)] = struct{}{}
		}
	}
	return paths, nil
}

func dirtySubmodulePaths(root string) ([]string, error) {
	output, err := runGit(root, nil, nil, "status", "--porcelain=v2", "-z", "--ignore-submodules=none")
	if err != nil {
		return nil, fmt.Errorf("workspace capture: inspect submodule state: %w", err)
	}
	paths := make([]string, 0)
	for _, record := range bytes.Split(output, []byte{0}) {
		if len(record) == 0 || (record[0] != '1' && record[0] != '2') {
			continue
		}
		fields := bytes.Fields(record)
		if len(fields) < 3 || len(fields[2]) != 4 || fields[2][0] != 'S' {
			continue
		}
		// The second and third submodule-status bytes mean tracked and
		// untracked nested worktree content. A changed gitlink commit (SC..)
		// remains representable and is therefore allowed.
		if fields[2][2] != '.' || fields[2][3] != '.' {
			paths = append(paths, porcelainPath(record))
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func porcelainPath(record []byte) string {
	fieldCount := 8
	if record[0] == '2' {
		fieldCount = 9
	}
	remainder := record
	for field := 0; field < fieldCount; field++ {
		separator := bytes.IndexByte(remainder, ' ')
		if separator < 0 {
			return ""
		}
		remainder = remainder[separator+1:]
	}
	return string(remainder)
}

type indexEntry struct {
	mode string
}

func buildObservations(root string, environment []string, baseCommit string, state workspaceState) ([]Observation, error) {
	changes, err := nameStatuses(root, environment, "diff", "--cached", "--name-status", "-z", "--no-renames", baseCommit, "--")
	if err != nil {
		return nil, fmt.Errorf("workspace capture: observe result changes: %w", err)
	}
	resultEntries, err := indexEntries(root, environment, "ls-files", "-s", "-z")
	if err != nil {
		return nil, fmt.Errorf("workspace capture: observe result entries: %w", err)
	}
	baseEntries, err := indexEntries(root, nil, "ls-tree", "-r", "-z", baseCommit, "--")
	if err != nil {
		return nil, fmt.Errorf("workspace capture: observe base entries: %w", err)
	}
	binary, err := binaryPaths(root, environment, baseCommit)
	if err != nil {
		return nil, fmt.Errorf("workspace capture: observe binary entries: %w", err)
	}
	paths := make(map[string]struct{}, len(changes)+len(state.excluded))
	for path := range changes {
		paths[path] = struct{}{}
	}
	for path := range state.excluded {
		paths[path] = struct{}{}
	}
	ordered := make([]string, 0, len(paths))
	for path := range paths {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)
	observations := make([]Observation, 0, len(ordered))
	for _, path := range ordered {
		_, excluded := state.excluded[path]
		status := changes[path]
		entry := resultEntries[path]
		if status == "D" {
			entry = baseEntries[path]
		}
		_, staged := state.staged[path]
		_, unstaged := state.unstaged[path]
		_, isBinary := binary[path]
		observations = append(observations, Observation{
			Path:      path,
			Mode:      entry.mode,
			Included:  !excluded,
			Excluded:  excluded,
			Deleted:   status == "D",
			Binary:    isBinary,
			Symlink:   entry.mode == "120000",
			Submodule: entry.mode == "160000",
			Staged:    staged,
			Unstaged:  unstaged,
		})
	}
	return observations, nil
}

func nameStatuses(root string, environment []string, arguments ...string) (map[string]string, error) {
	output, err := runGit(root, environment, nil, arguments...)
	if err != nil {
		return nil, err
	}
	fields := bytes.Split(output, []byte{0})
	statuses := make(map[string]string)
	for index := 0; index+1 < len(fields); index += 2 {
		if len(fields[index]) != 0 && len(fields[index+1]) != 0 {
			statuses[string(fields[index+1])] = string(fields[index])
		}
	}
	return statuses, nil
}

func indexEntries(root string, environment []string, arguments ...string) (map[string]indexEntry, error) {
	output, err := runGit(root, environment, nil, arguments...)
	if err != nil {
		return nil, err
	}
	entries := make(map[string]indexEntry)
	for _, record := range bytes.Split(output, []byte{0}) {
		parts := bytes.SplitN(record, []byte{'\t'}, 2)
		if len(parts) != 2 || len(parts[1]) == 0 {
			continue
		}
		fields := bytes.Fields(parts[0])
		if len(fields) == 0 {
			continue
		}
		entries[string(parts[1])] = indexEntry{mode: string(fields[0])}
	}
	return entries, nil
}

func binaryPaths(root string, environment []string, baseCommit string) (map[string]struct{}, error) {
	output, err := runGit(root, environment, nil, "diff", "--cached", "--numstat", "-z", "--no-renames", baseCommit, "--")
	if err != nil {
		return nil, err
	}
	paths := make(map[string]struct{})
	for _, record := range bytes.Split(output, []byte{0}) {
		fields := bytes.SplitN(record, []byte{'\t'}, 3)
		if len(fields) == 3 && (bytes.Equal(fields[0], []byte("-")) || bytes.Equal(fields[1], []byte("-"))) {
			paths[string(fields[2])] = struct{}{}
		}
	}
	return paths, nil
}

func verifyPatch(root, scratch, baseCommit, baseTree, resultTree string, patch []byte) error {
	checkout := filepath.Join(scratch, "verification-checkout")
	if _, err := runGit(root, nil, nil, "clone", "--quiet", "--no-checkout", root, checkout); err != nil {
		return fmt.Errorf("workspace capture: create clean verification checkout: %w", err)
	}
	if _, err := runGit(checkout, nil, nil, "checkout", "--detach", "--force", baseCommit); err != nil {
		return fmt.Errorf("workspace capture: check out verification base: %w", err)
	}
	if len(patch) > 0 {
		if _, err := runGit(checkout, nil, patch,
			"apply", "--cached", "--binary", "--whitespace=nowarn", "-"); err != nil {
			return fmt.Errorf("workspace capture: verify patch application: %w", err)
		}
	}
	verified, err := gitValue(checkout, nil, "write-tree")
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
		sameObservations(left.Observations, right.Observations) &&
		bytes.Equal(left.Patch, right.Patch)
}

func sameObservations(left, right []Observation) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
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
