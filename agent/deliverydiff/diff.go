package deliverydiff

//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 -generate

import (
	"bytes"
	"fmt"
	"os/exec"
	"regexp"
	"strings"

	"github.com/concourse/concourse/agent/gitcheck"
)

const (
	SchemaVersion        = "delivery-diff/1"
	MaxFiles             = 200
	MaxPatchBytesPerFile = 64 << 10
	MaxTotalPatchBytes   = 2 << 20

	defaultPageLimit = 50
	truncatedMarker  = "\n... [diff truncated]\n"
)

var shaPattern = regexp.MustCompile(`^[0-9a-fA-F]{40}([0-9a-fA-F]{24})?$`)

type Artifact struct {
	SchemaVersion   string              `json:"schema_version"`
	BaseSHA         string              `json:"base_sha"`
	PushedSHA       string              `json:"pushed_sha"`
	DeliveredBranch string              `json:"delivered_branch"`
	Files           []gitcheck.DiffFile `json:"files"`
	TotalFiles      int                 `json:"total_files"`
	CapturedFiles   int                 `json:"captured_files"`
	ByteLen         int                 `json:"byte_len"`
	Truncated       bool                `json:"truncated"`
}

type DeliveryDiff struct {
	BuildID         int
	TicketID        int
	PipelineRunID   int
	PlanID          string
	Repo            string
	TargetBranch    string
	DeliveredBranch string
	BaseSHA         string
	PushedSHA       string
	Files           []gitcheck.DiffFile
	TotalFiles      int
	CapturedFiles   int
	ByteLen         int
	Truncated       bool
}

//counterfeiter:generate . Store
type Store interface {
	Upsert(DeliveryDiff) error
	GetLatest(ticketID int) (DeliveryDiff, bool, error)
}

func (a Artifact) Validate() error {
	if a.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported schema version %q", a.SchemaVersion)
	}
	if !shaPattern.MatchString(a.BaseSHA) {
		return fmt.Errorf("invalid base SHA %q", a.BaseSHA)
	}
	if !shaPattern.MatchString(a.PushedSHA) {
		return fmt.Errorf("invalid pushed SHA %q", a.PushedSHA)
	}
	if !validBranch(a.DeliveredBranch) {
		return fmt.Errorf("invalid delivered branch %q", a.DeliveredBranch)
	}
	if a.CapturedFiles != len(a.Files) {
		return fmt.Errorf("captured file count %d does not match %d files", a.CapturedFiles, len(a.Files))
	}
	if a.CapturedFiles > MaxFiles {
		return fmt.Errorf("captured file count %d exceeds limit %d", a.CapturedFiles, MaxFiles)
	}
	if a.TotalFiles < a.CapturedFiles {
		return fmt.Errorf("total file count %d is less than captured count %d", a.TotalFiles, a.CapturedFiles)
	}

	byteLen := 0
	truncated := a.TotalFiles > a.CapturedFiles
	for _, file := range a.Files {
		if file.Path == "" {
			return fmt.Errorf("diff file path is empty")
		}
		if len(file.Patch) > MaxPatchBytesPerFile {
			return fmt.Errorf("patch for %q exceeds limit %d", file.Path, MaxPatchBytesPerFile)
		}
		byteLen += len(file.Patch)
		truncated = truncated || file.Truncated
	}
	if byteLen != a.ByteLen {
		return fmt.Errorf("byte length %d does not match stored patches %d", a.ByteLen, byteLen)
	}
	if a.ByteLen > MaxTotalPatchBytes {
		return fmt.Errorf("total patch bytes %d exceeds limit %d", a.ByteLen, MaxTotalPatchBytes)
	}
	if truncated && !a.Truncated {
		return fmt.Errorf("artifact must be marked truncated")
	}
	return nil
}

func (d DeliveryDiff) Page(offset, limit int) gitcheck.DiffPage {
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = defaultPageLimit
	}
	if limit > MaxFiles {
		limit = MaxFiles
	}

	start := offset
	if start > len(d.Files) {
		start = len(d.Files)
	}
	end := start + limit
	if end > len(d.Files) {
		end = len(d.Files)
	}
	files := append([]gitcheck.DiffFile(nil), d.Files[start:end]...)
	return gitcheck.DiffPage{
		Files:      files,
		Offset:     offset,
		Limit:      limit,
		TotalFiles: d.TotalFiles,
		HasMore:    offset+len(files) < d.TotalFiles,
	}
}

func Capture(dir, baseSHA, pushedSHA, deliveredBranch string) (Artifact, error) {
	artifact := Artifact{
		SchemaVersion:   SchemaVersion,
		BaseSHA:         baseSHA,
		PushedSHA:       pushedSHA,
		DeliveredBranch: deliveredBranch,
	}
	if !shaPattern.MatchString(baseSHA) || !shaPattern.MatchString(pushedSHA) || !validBranch(deliveredBranch) {
		return Artifact{}, fmt.Errorf("invalid delivery identity")
	}

	rangeSpec := baseSHA + ".." + pushedSHA
	pathsOutput, err := gitOutput(dir, "diff", "--name-only", "-z", rangeSpec)
	if err != nil {
		return Artifact{}, fmt.Errorf("list changed files: %w", err)
	}
	paths := splitNull(pathsOutput)
	artifact.TotalFiles = len(paths)

	for _, path := range paths {
		if len(artifact.Files) == MaxFiles || artifact.ByteLen == MaxTotalPatchBytes {
			artifact.Truncated = true
			break
		}
		raw, err := gitOutput(dir, "diff", rangeSpec, "--", path)
		if err != nil {
			return Artifact{}, fmt.Errorf("diff %q: %w", path, err)
		}
		remaining := MaxTotalPatchBytes - artifact.ByteLen
		limit := MaxPatchBytesPerFile
		if remaining < limit {
			limit = remaining
		}
		patch, truncated := boundedPatch(raw, limit)
		artifact.Files = append(artifact.Files, gitcheck.DiffFile{
			Path:      path,
			Patch:     patch,
			Truncated: truncated,
		})
		artifact.ByteLen += len(patch)
		artifact.Truncated = artifact.Truncated || truncated
	}
	artifact.CapturedFiles = len(artifact.Files)
	artifact.Truncated = artifact.Truncated || artifact.CapturedFiles < artifact.TotalFiles
	if err := artifact.Validate(); err != nil {
		return Artifact{}, err
	}
	return artifact, nil
}

func boundedPatch(raw []byte, limit int) (string, bool) {
	if len(raw) <= limit {
		return string(raw), false
	}
	if limit <= len(truncatedMarker) {
		return truncatedMarker[:limit], true
	}
	keep := limit - len(truncatedMarker)
	return string(raw[:keep]) + truncatedMarker, true
}

func gitOutput(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, err
	}
	return out, nil
}

func splitNull(output []byte) []string {
	output = bytes.TrimSuffix(output, []byte{0})
	if len(output) == 0 {
		return nil
	}
	parts := bytes.Split(output, []byte{0})
	paths := make([]string, len(parts))
	for i, part := range parts {
		paths[i] = string(part)
	}
	return paths
}

func validBranch(branch string) bool {
	return branch != "" &&
		!strings.HasPrefix(branch, "-") &&
		!strings.HasPrefix(branch, ".") &&
		!strings.HasSuffix(branch, ".") &&
		!strings.HasSuffix(branch, "/") &&
		!strings.Contains(branch, "..") &&
		!strings.Contains(branch, "@{") &&
		!strings.ContainsAny(branch, " ~^:?*[\\\t\r\n")
}
