package gitcheck

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

// State values mirror the outcome merge-state vocabulary (§1.11) without
// importing agent/api/outcomes (gitcheck is a leaf package).
const (
	StateMerged          = "merged"
	StateMergedWithFixes = "merged_with_fixes"
)

// Result is a detected merge fact (nil from Detect means "still open").
type Result struct {
	State             string // StateMerged | StateMergedWithFixes
	MergedSha         string
	HumanCommitCount  int
	HumanLinesAdded   int
	HumanLinesDeleted int
}

// Detect runs the §1.11.1 heuristics against a freshly-fetched mirror:
// ancestor-primary, then patch-id squash fallback. Returns nil when the
// branch is neither reachable nor patch-id-matched (stays open) — an
// edited-squash/rebase is never guessed at.
func (m *Mirror) Detect(base, pushed, branch, target string, scanLimit int) (*Result, error) {
	// Primary: reachability.
	anc, err := m.IsAncestor(pushed, target)
	if err != nil {
		return nil, err
	}
	if anc {
		mp, err := m.MergePoint(pushed, target)
		if err != nil {
			return nil, err
		}
		// §1.11.1 fast-forward refinement: MergePoint only knows the target
		// branch, so it falls back to pushed for a fast-forward. Detect owns
		// the agent branch name, so it resolves the branch's remote head as
		// tip-at-merge (the delta window covers human commits that fast-
		// forwarded onto the branch before it merged); if the branch was
		// deleted, the documented fallback is pushed. True merges (a merge
		// commit exists) already carry the correct second-parent tip.
		tip := mp.TipAtMerge
		mergedSha := mp.MergedSha
		if mp.FastForwarded {
			if head, err := m.run(m.dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err == nil && head != "" {
				tip = head
				mergedSha = head // §1.11.1: merged_sha = tip-at-merge for a pure ff
			}
		}
		delta, err := m.HumanDelta(pushed, tip)
		if err != nil {
			return nil, err
		}
		return resultFrom(mergedSha, delta), nil
	}

	// Squash fallback (needs a known base).
	if base != "" {
		branchTip := branch
		if tip, err := m.run(m.dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err == nil && tip != "" {
			branchTip = tip
		}
		match, err := m.PatchIDMatch(base, branchTip, target, scanLimit)
		if err != nil {
			return nil, err
		}
		if match.Found {
			// Human delta still measured on the agent branch (pushed..tip).
			delta, err := m.HumanDelta(pushed, branchTip)
			if err != nil {
				return nil, err
			}
			return resultFrom(match.Sha, delta), nil
		}
	}

	return nil, nil // still open — the honest v1 answer
}

func resultFrom(mergedSha string, d Delta) *Result {
	r := &Result{
		State:             StateMerged,
		MergedSha:         mergedSha,
		HumanCommitCount:  d.CommitCount,
		HumanLinesAdded:   d.LinesAdded,
		HumanLinesDeleted: d.LinesDeleted,
	}
	if d.CommitCount > 0 {
		r.State = StateMergedWithFixes
	}
	return r
}

// DiffFile is one file's unified-diff patch in a windowed diff.
type DiffFile struct {
	Path      string `json:"path"`
	Patch     string `json:"patch"`
	Truncated bool   `json:"truncated,omitempty"`
}

// DiffPage is a file-windowed diff (§1.11.1 diff API contract).
type DiffPage struct {
	Files      []DiffFile `json:"files"`
	Offset     int        `json:"offset"`
	Limit      int        `json:"limit"`
	TotalFiles int        `json:"total_files"`
	HasMore    bool       `json:"has_more"`
}

// perFilePatchCap bounds any single file's patch (§1.11.1: 64 KiB).
const perFilePatchCap = 64 << 10

// BoundedUnifiedDiffBytes is the durable projection budget. Unlike the live
// mirror fallback, a repository-change projection is computed once and kept
// after its source bytes expire, so the complete stored diff must be bounded.
const BoundedUnifiedDiffBytes = 64 << 10

type ChangeStatus string

const (
	ChangeAdded       ChangeStatus = "added"
	ChangeModified    ChangeStatus = "modified"
	ChangeDeleted     ChangeStatus = "deleted"
	ChangeRenamed     ChangeStatus = "renamed"
	ChangeCopied      ChangeStatus = "copied"
	ChangeTypeChanged ChangeStatus = "type_changed"
	ChangeUnmerged    ChangeStatus = "unmerged"
)

// ChangedFile is semantic diff metadata plus the bounded part of this file's
// patch. Binary files intentionally report zero line counts.
type ChangedFile struct {
	Path         string       `json:"path"`
	PreviousPath string       `json:"previous_path,omitempty"`
	Status       ChangeStatus `json:"status"`
	LinesAdded   int          `json:"lines_added"`
	LinesDeleted int          `json:"lines_deleted"`
	Binary       bool         `json:"binary,omitempty"`
	Patch        string       `json:"patch,omitempty"`
	Truncated    bool         `json:"truncated,omitempty"`
}

// RepositoryDiff is the deterministic, offline read model persisted by the
// repository-change projector.
type RepositoryDiff struct {
	Files            []ChangedFile `json:"files"`
	FileCount        int           `json:"file_count"`
	LinesAdded       int           `json:"lines_added"`
	LinesDeleted     int           `json:"lines_deleted"`
	UnifiedDiff      string        `json:"unified_diff"`
	Truncated        bool          `json:"truncated"`
	TruncationReason string        `json:"truncation_reason,omitempty"`
}

// DeriveRepositoryDiff compares two already-local immutable Git objects. It
// never resolves remotes and rejects revisions that are not present in the
// supplied repository.
func DeriveRepositoryDiff(ctx context.Context, repositoryDir, base, result string) (RepositoryDiff, error) {
	if ctx == nil {
		return RepositoryDiff{}, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return RepositoryDiff{}, err
	}
	for _, candidate := range []struct{ label, revision string }{{"base", base}, {"result", result}} {
		label, revision := candidate.label, candidate.revision
		if !fullObjectID(revision) {
			return RepositoryDiff{}, fmt.Errorf("gitcheck: %s revision must be a full object ID", label)
		}
		if _, _, err := runLocalGit(ctx, repositoryDir, 64<<10, "rev-parse", "--verify", revision+"^{tree}"); err != nil {
			return RepositoryDiff{}, fmt.Errorf("gitcheck: %s revision is unavailable: %w", label, err)
		}
	}

	nameStatus, truncated, err := runLocalGit(ctx, repositoryDir, 64<<20,
		"diff", "--name-status", "-z", "--find-renames=50%", "--no-ext-diff", "--no-textconv", base, result)
	if err != nil {
		return RepositoryDiff{}, err
	}
	if truncated {
		return RepositoryDiff{}, fmt.Errorf("gitcheck: changed-file listing exceeds safety limit")
	}
	files, err := parseNameStatus(nameStatus)
	if err != nil {
		return RepositoryDiff{}, err
	}

	numstat, truncated, err := runLocalGit(ctx, repositoryDir, 64<<20,
		"diff", "--numstat", "-z", "--find-renames=50%", "--no-ext-diff", "--no-textconv", base, result)
	if err != nil {
		return RepositoryDiff{}, err
	}
	if truncated {
		return RepositoryDiff{}, fmt.Errorf("gitcheck: diff statistics exceed safety limit")
	}
	stats, err := parseNumstat(numstat)
	if err != nil {
		return RepositoryDiff{}, err
	}

	projection := RepositoryDiff{Files: files, FileCount: len(files)}
	for index := range projection.Files {
		stat := stats[projection.Files[index].Path]
		projection.Files[index].LinesAdded = stat.added
		projection.Files[index].LinesDeleted = stat.deleted
		projection.Files[index].Binary = stat.binary
		projection.LinesAdded += stat.added
		projection.LinesDeleted += stat.deleted
	}

	rawDiff, outputTruncated, err := runLocalGit(ctx, repositoryDir, BoundedUnifiedDiffBytes+1,
		"diff", "--binary", "--full-index", "--find-renames=50%", "--no-ext-diff", "--no-textconv", base, result)
	if err != nil {
		return RepositoryDiff{}, err
	}
	if outputTruncated || len(rawDiff) > BoundedUnifiedDiffBytes {
		const marker = "\n... [repository diff truncated at 65536 bytes]\n"
		limit := BoundedUnifiedDiffBytes - len(marker)
		if len(rawDiff) > limit {
			rawDiff = rawDiff[:limit]
		}
		for !utf8.Valid(rawDiff) && len(rawDiff) > 0 {
			rawDiff = rawDiff[:len(rawDiff)-1]
		}
		rawDiff = append(rawDiff, marker...)
		projection.Truncated = true
		projection.TruncationReason = "unified diff exceeds 65536-byte projection limit"
	}
	projection.UnifiedDiff = string(rawDiff)
	attachPatches(&projection)
	return projection, nil
}

type fileStat struct {
	added   int
	deleted int
	binary  bool
}

func parseNameStatus(output []byte) ([]ChangedFile, error) {
	tokens := bytes.Split(output, []byte{0})
	files := make([]ChangedFile, 0, len(tokens)/2)
	for index := 0; index < len(tokens); {
		if len(tokens[index]) == 0 {
			index++
			continue
		}
		code := string(tokens[index])
		index++
		status, twoPaths := changeStatus(code)
		if status == "" || index >= len(tokens) || len(tokens[index]) == 0 {
			return nil, fmt.Errorf("gitcheck: malformed name-status output")
		}
		file := ChangedFile{Status: status}
		if twoPaths {
			file.PreviousPath = string(tokens[index])
			index++
			if index >= len(tokens) || len(tokens[index]) == 0 {
				return nil, fmt.Errorf("gitcheck: malformed rename/copy output")
			}
		}
		file.Path = string(tokens[index])
		index++
		files = append(files, file)
	}
	return files, nil
}

func changeStatus(code string) (ChangeStatus, bool) {
	if code == "" {
		return "", false
	}
	switch code[0] {
	case 'A':
		return ChangeAdded, false
	case 'M':
		return ChangeModified, false
	case 'D':
		return ChangeDeleted, false
	case 'R':
		return ChangeRenamed, true
	case 'C':
		return ChangeCopied, true
	case 'T':
		return ChangeTypeChanged, false
	case 'U':
		return ChangeUnmerged, false
	default:
		return "", false
	}
}

func parseNumstat(output []byte) (map[string]fileStat, error) {
	stats := make(map[string]fileStat)
	for cursor := 0; cursor < len(output); {
		addedRaw, next, ok := fieldUntil(output, cursor, '\t')
		if !ok {
			if len(bytes.Trim(output[cursor:], "\x00")) == 0 {
				break
			}
			return nil, fmt.Errorf("gitcheck: malformed numstat additions")
		}
		cursor = next
		deletedRaw, next, ok := fieldUntil(output, cursor, '\t')
		if !ok {
			return nil, fmt.Errorf("gitcheck: malformed numstat deletions")
		}
		cursor = next
		var path string
		if cursor < len(output) && output[cursor] == 0 {
			cursor++
			_, next, ok = fieldUntil(output, cursor, 0)
			if !ok {
				return nil, fmt.Errorf("gitcheck: malformed numstat old path")
			}
			cursor = next
			newPath, next, ok := fieldUntil(output, cursor, 0)
			if !ok {
				return nil, fmt.Errorf("gitcheck: malformed numstat new path")
			}
			path = string(newPath)
			cursor = next
		} else {
			pathRaw, next, ok := fieldUntil(output, cursor, 0)
			if !ok {
				return nil, fmt.Errorf("gitcheck: malformed numstat path")
			}
			path = string(pathRaw)
			cursor = next
		}
		stat := fileStat{}
		if string(addedRaw) == "-" || string(deletedRaw) == "-" {
			stat.binary = true
		} else {
			var err error
			stat.added, err = strconv.Atoi(string(addedRaw))
			if err != nil {
				return nil, fmt.Errorf("gitcheck: malformed numstat additions: %w", err)
			}
			stat.deleted, err = strconv.Atoi(string(deletedRaw))
			if err != nil {
				return nil, fmt.Errorf("gitcheck: malformed numstat deletions: %w", err)
			}
		}
		stats[path] = stat
	}
	return stats, nil
}

func fieldUntil(value []byte, start int, separator byte) ([]byte, int, bool) {
	if start >= len(value) {
		return nil, start, false
	}
	relative := bytes.IndexByte(value[start:], separator)
	if relative < 0 {
		return nil, start, false
	}
	end := start + relative
	return value[start:end], end + 1, true
}

func attachPatches(projection *RepositoryDiff) {
	if projection == nil || len(projection.Files) == 0 || projection.UnifiedDiff == "" {
		return
	}
	contents := []byte(projection.UnifiedDiff)
	starts := []int{}
	if bytes.HasPrefix(contents, []byte("diff --git ")) {
		starts = append(starts, 0)
	}
	for cursor := 0; cursor < len(contents); {
		relative := bytes.Index(contents[cursor:], []byte("\ndiff --git "))
		if relative < 0 {
			break
		}
		start := cursor + relative + 1
		starts = append(starts, start)
		cursor = start + len("diff --git ")
	}
	for index := range projection.Files {
		if index >= len(starts) {
			if projection.Truncated {
				projection.Files[index].Truncated = true
			}
			continue
		}
		end := len(contents)
		if index+1 < len(starts) {
			end = starts[index+1]
		}
		projection.Files[index].Patch = string(contents[starts[index]:end])
		if projection.Truncated && index == len(starts)-1 {
			projection.Files[index].Truncated = true
		}
	}
}

func fullObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return true
}

type prefixWriter struct {
	buffer bytes.Buffer
	limit  int
	total  int64
}

func (writer *prefixWriter) Write(contents []byte) (int, error) {
	written := len(contents)
	writer.total += int64(written)
	remaining := writer.limit - writer.buffer.Len()
	if remaining > 0 {
		if remaining > written {
			remaining = written
		}
		_, _ = writer.buffer.Write(contents[:remaining])
	}
	return written, nil
}

func runLocalGit(ctx context.Context, repositoryDir string, limit int, arguments ...string) ([]byte, bool, error) {
	configured := []string{
		"--git-dir=" + filepath.Join(repositoryDir, ".git"),
		"--work-tree=" + repositoryDir,
		"-c", "core.hooksPath=" + os.DevNull,
		"-c", "core.fsmonitor=false",
		"-c", "core.untrackedCache=false",
		"-c", "core.autocrlf=false",
		"-c", "core.safecrlf=false",
		"-c", "core.attributesFile=" + os.DevNull,
		"-c", "core.excludesFile=" + os.DevNull,
		"-c", "credential.helper=",
		"-c", "protocol.allow=never",
		"-c", "fetch.recurseSubmodules=false",
		"-c", "submodule.recurse=false",
		"-c", "filter.lfs.process=",
		"-c", "filter.lfs.smudge=",
		"-c", "filter.lfs.clean=",
	}
	configured = append(configured, arguments...)
	command := exec.CommandContext(ctx, "git", configured...)
	command.Dir = repositoryDir
	command.Env = localGitEnvironment()
	stdout := &prefixWriter{limit: limit}
	stderr := &prefixWriter{limit: 64 << 10}
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		return nil, false, fmt.Errorf("gitcheck: git %s: %w: %s", strings.Join(arguments, " "), err, strings.TrimSpace(stderr.buffer.String()))
	}
	return append([]byte(nil), stdout.buffer.Bytes()...), stdout.total > int64(limit), nil
}

func localGitEnvironment() []string {
	environment := make([]string, 0, len(os.Environ())+7)
	for _, variable := range os.Environ() {
		name := variable
		if separator := strings.IndexByte(variable, '='); separator >= 0 {
			name = variable[:separator]
		}
		if strings.HasPrefix(name, "GIT_") || name == "LC_ALL" {
			continue
		}
		environment = append(environment, variable)
	}
	return append(environment,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_ATTR_NOSYSTEM=1",
		"LC_ALL=C",
	)
}

// FileDiff returns the base..pushed unified diff windowed to [offset, offset+limit)
// files, each capped at perFilePatchCap bytes.
func (m *Mirror) FileDiff(base, pushed string, offset, limit int) (DiffPage, error) {
	if limit <= 0 {
		limit = 50
	}
	names, err := m.run(m.dir, "diff", "--name-only", base+".."+pushed)
	if err != nil {
		return DiffPage{}, err
	}
	all := nonEmptyLines(names)
	page := DiffPage{Offset: offset, Limit: limit, TotalFiles: len(all)}
	if offset >= len(all) {
		page.Files = []DiffFile{}
		return page, nil
	}
	end := offset + limit
	if end > len(all) {
		end = len(all)
	}
	page.HasMore = end < len(all)
	for _, path := range all[offset:end] {
		patch, err := m.run(m.dir, "diff", base+".."+pushed, "--", path)
		if err != nil {
			return DiffPage{}, err
		}
		df := DiffFile{Path: path, Patch: patch}
		if len(df.Patch) > perFilePatchCap {
			df.Patch = df.Patch[:perFilePatchCap] + "\n... [diff truncated]\n"
			df.Truncated = true
		}
		page.Files = append(page.Files, df)
	}
	return page, nil
}
