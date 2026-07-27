// Package repodiff derives deterministic, offline repository-change
// projections from already-local immutable Git objects. It never resolves
// remotes and never fetches: the durable repository-change snapshot projector
// is its only consumer. (The former web-node mirror/auth/fetch and
// merge-detection APIs were retired with the legacy outcome watcher.)
package repodiff

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
			return RepositoryDiff{}, fmt.Errorf("repodiff: %s revision must be a full object ID", label)
		}
		if _, _, err := runLocalGit(ctx, repositoryDir, 64<<10, "rev-parse", "--verify", revision+"^{tree}"); err != nil {
			return RepositoryDiff{}, fmt.Errorf("repodiff: %s revision is unavailable: %w", label, err)
		}
	}

	nameStatus, truncated, err := runLocalGit(ctx, repositoryDir, 64<<20,
		"diff", "--name-status", "-z", "--find-renames=50%", "--no-ext-diff", "--no-textconv", base, result)
	if err != nil {
		return RepositoryDiff{}, err
	}
	if truncated {
		return RepositoryDiff{}, fmt.Errorf("repodiff: changed-file listing exceeds safety limit")
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
		return RepositoryDiff{}, fmt.Errorf("repodiff: diff statistics exceed safety limit")
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
			return nil, fmt.Errorf("repodiff: malformed name-status output")
		}
		file := ChangedFile{Status: status}
		if twoPaths {
			file.PreviousPath = string(tokens[index])
			index++
			if index >= len(tokens) || len(tokens[index]) == 0 {
				return nil, fmt.Errorf("repodiff: malformed rename/copy output")
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
			return nil, fmt.Errorf("repodiff: malformed numstat additions")
		}
		cursor = next
		deletedRaw, next, ok := fieldUntil(output, cursor, '\t')
		if !ok {
			return nil, fmt.Errorf("repodiff: malformed numstat deletions")
		}
		cursor = next
		var path string
		if cursor < len(output) && output[cursor] == 0 {
			cursor++
			_, next, ok = fieldUntil(output, cursor, 0)
			if !ok {
				return nil, fmt.Errorf("repodiff: malformed numstat old path")
			}
			cursor = next
			newPath, next, ok := fieldUntil(output, cursor, 0)
			if !ok {
				return nil, fmt.Errorf("repodiff: malformed numstat new path")
			}
			path = string(newPath)
			cursor = next
		} else {
			pathRaw, next, ok := fieldUntil(output, cursor, 0)
			if !ok {
				return nil, fmt.Errorf("repodiff: malformed numstat path")
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
				return nil, fmt.Errorf("repodiff: malformed numstat additions: %w", err)
			}
			stat.deleted, err = strconv.Atoi(string(deletedRaw))
			if err != nil {
				return nil, fmt.Errorf("repodiff: malformed numstat deletions: %w", err)
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
		return nil, false, fmt.Errorf("repodiff: git %s: %w: %s", strings.Join(arguments, " "), err, strings.TrimSpace(stderr.buffer.String()))
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
