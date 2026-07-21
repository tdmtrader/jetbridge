package deliverydiff_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/deliverydiff"
	"github.com/concourse/concourse/agent/gitcheck"
)

func TestCapture(t *testing.T) {
	repo, base, head := fixtureRepo(t, 2, false)

	got, err := deliverydiff.Capture(repo, base, head, "agent/ticket-42")
	if err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != deliverydiff.SchemaVersion {
		t.Fatalf("schema = %q", got.SchemaVersion)
	}
	if got.BaseSHA != base || got.PushedSHA != head {
		t.Fatalf("shas = %s..%s", got.BaseSHA, got.PushedSHA)
	}
	if got.DeliveredBranch != "agent/ticket-42" {
		t.Fatalf("branch = %q", got.DeliveredBranch)
	}
	if got.TotalFiles != 2 || got.CapturedFiles != 2 {
		t.Fatalf("counts = %d/%d", got.CapturedFiles, got.TotalFiles)
	}
	if got.Files[0].Path != "file-000.txt" || got.Files[1].Path != "file-001.txt" {
		t.Fatalf("order = %#v", got.Files)
	}
	if !strings.Contains(got.Files[0].Patch, "+changed 0") {
		t.Fatalf("patch = %q", got.Files[0].Patch)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("captured artifact is invalid: %v", err)
	}
}

func TestCaptureBoundsAndPage(t *testing.T) {
	repo, base, head := fixtureRepo(t, deliverydiff.MaxFiles+1, true)

	got, err := deliverydiff.Capture(repo, base, head, "agent/ticket-42")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Truncated || got.CapturedFiles > deliverydiff.MaxFiles || got.ByteLen > deliverydiff.MaxTotalPatchBytes {
		t.Fatalf("bounds not enforced: %#v", got)
	}
	if got.TotalFiles != deliverydiff.MaxFiles+1 {
		t.Fatalf("total files = %d", got.TotalFiles)
	}
	for _, file := range got.Files {
		if len(file.Patch) > deliverydiff.MaxPatchBytesPerFile {
			t.Fatalf("%s too large: %d", file.Path, len(file.Patch))
		}
	}

	row := deliverydiff.DeliveryDiff{
		Files:         got.Files,
		TotalFiles:    got.TotalFiles,
		CapturedFiles: got.CapturedFiles,
		Truncated:     got.Truncated,
	}
	page := row.Page(0, 50)
	if page.Limit != 50 || page.TotalFiles != got.TotalFiles || !page.HasMore || len(page.Files) != 50 {
		t.Fatalf("page = %#v", page)
	}
	page = row.Page(-10, 1000)
	if page.Offset != 0 || page.Limit != deliverydiff.MaxFiles || len(page.Files) != got.CapturedFiles {
		t.Fatalf("normalized page = %#v", page)
	}
	page = row.Page(got.CapturedFiles, 10)
	if len(page.Files) != 0 || !page.HasMore {
		t.Fatalf("past-capture page = %#v", page)
	}
}

func TestArtifactValidateRejectsMalformedArtifacts(t *testing.T) {
	valid := deliverydiff.Artifact{
		SchemaVersion:   deliverydiff.SchemaVersion,
		BaseSHA:         strings.Repeat("a", 40),
		PushedSHA:       strings.Repeat("b", 40),
		DeliveredBranch: "agent/ticket-42",
		Files:           []gitcheck.DiffFile{{Path: "a.txt", Patch: "patch"}},
		TotalFiles:      1,
		CapturedFiles:   1,
		ByteLen:         5,
	}

	tests := map[string]func(*deliverydiff.Artifact){
		"schema":         func(a *deliverydiff.Artifact) { a.SchemaVersion = "unknown" },
		"base SHA":       func(a *deliverydiff.Artifact) { a.BaseSHA = "not-a-sha" },
		"pushed SHA":     func(a *deliverydiff.Artifact) { a.PushedSHA = "" },
		"branch":         func(a *deliverydiff.Artifact) { a.DeliveredBranch = "bad branch" },
		"captured count": func(a *deliverydiff.Artifact) { a.CapturedFiles = 2 },
		"total count":    func(a *deliverydiff.Artifact) { a.TotalFiles = 0 },
		"byte length":    func(a *deliverydiff.Artifact) { a.ByteLen = 4 },
		"empty path":     func(a *deliverydiff.Artifact) { a.Files[0].Path = "" },
		"file limit": func(a *deliverydiff.Artifact) {
			a.Files[0].Patch = strings.Repeat("x", deliverydiff.MaxPatchBytesPerFile+1)
			a.ByteLen = len(a.Files[0].Patch)
		},
		"missing truncation": func(a *deliverydiff.Artifact) { a.TotalFiles = 2 },
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			artifact := valid
			artifact.Files = append([]gitcheck.DiffFile(nil), valid.Files...)
			mutate(&artifact)
			if err := artifact.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func fixtureRepo(t *testing.T, fileCount int, oversized bool) (string, string, string) {
	t.Helper()
	repo := t.TempDir()
	git(t, repo, "init", "-b", "main")
	git(t, repo, "config", "user.email", "agent@example.com")
	git(t, repo, "config", "user.name", "Agent")

	for i := 0; i < fileCount; i++ {
		path := filepath.Join(repo, fmt.Sprintf("file-%03d.txt", i))
		if err := os.WriteFile(path, []byte(fmt.Sprintf("base %d\n", i)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "base")
	base := git(t, repo, "rev-parse", "HEAD")

	for i := 0; i < fileCount; i++ {
		content := fmt.Sprintf("base %d\nchanged %d\n", i, i)
		if oversized && i == 0 {
			content += strings.Repeat("large line\n", deliverydiff.MaxPatchBytesPerFile)
		}
		path := filepath.Join(repo, fmt.Sprintf("file-%03d.txt", i))
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "delivered")
	head := git(t, repo, "rev-parse", "HEAD")
	return repo, base, head
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}
