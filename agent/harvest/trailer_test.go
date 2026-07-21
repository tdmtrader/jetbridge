package harvest_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/harvest"
)

func trailerGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=concourse-agent[bot]",
		"GIT_AUTHOR_EMAIL=agent@concourse.invalid",
		"GIT_COMMITTER_NAME=concourse-agent[bot]",
		"GIT_COMMITTER_EMAIL=agent@concourse.invalid",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// workspaceWith builds a throwaway repo whose tip commit has the given
// message.
func workspaceWith(t *testing.T, message string) string {
	t.Helper()
	dir := t.TempDir()
	trailerGit(t, dir, "init", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	trailerGit(t, dir, "add", ".")
	trailerGit(t, dir, "commit", "-m", message)
	return dir
}

func TestStampTrailerAppendsTicketTrailer(t *testing.T) {
	dir := workspaceWith(t, "implement the thing")

	newHead, err := harvest.StampTrailer(dir, 12)
	if err != nil {
		t.Fatal(err)
	}
	body := trailerGit(t, dir, "log", "-1", "--format=%B")
	if !strings.Contains(body, "Agent-Ticket: 12") {
		t.Fatalf("expected the trailer in the message, got:\n%s", body)
	}
	if !strings.HasPrefix(body, "implement the thing") {
		t.Fatalf("the original subject must survive, got:\n%s", body)
	}
	if newHead != trailerGit(t, dir, "rev-parse", "HEAD") {
		t.Fatal("StampTrailer must return the new HEAD sha")
	}
}

// The safety property that makes this cheap: amending a MESSAGE cannot
// invalidate gates, because the tree is byte-identical.
func TestStampTrailerLeavesTreeIdentical(t *testing.T) {
	dir := workspaceWith(t, "implement the thing")
	before := trailerGit(t, dir, "rev-parse", "HEAD^{tree}")

	if _, err := harvest.StampTrailer(dir, 12); err != nil {
		t.Fatal(err)
	}
	if after := trailerGit(t, dir, "rev-parse", "HEAD^{tree}"); after != before {
		t.Fatalf("tree must be unchanged: %s != %s", after, before)
	}
}

func TestStampTrailerIsIdempotent(t *testing.T) {
	dir := workspaceWith(t, "implement the thing")

	first, err := harvest.StampTrailer(dir, 12)
	if err != nil {
		t.Fatal(err)
	}
	second, err := harvest.StampTrailer(dir, 12)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("a second stamp must not create a new commit")
	}
	body := trailerGit(t, dir, "log", "-1", "--format=%B")
	if n := strings.Count(body, "Agent-Ticket:"); n != 1 {
		t.Fatalf("expected exactly one trailer, got %d:\n%s", n, body)
	}
}

// A message that already ends in a trailer block must gain the new trailer
// inside that block, not after a blank line that would split it in two.
func TestStampTrailerJoinsAnExistingTrailerBlock(t *testing.T) {
	dir := workspaceWith(t, "implement the thing\n\nCo-Authored-By: Someone <s@example.com>")

	if _, err := harvest.StampTrailer(dir, 7); err != nil {
		t.Fatal(err)
	}
	body := strings.TrimRight(trailerGit(t, dir, "log", "-1", "--format=%B"), "\n")
	lines := strings.Split(body, "\n")
	last, prev := lines[len(lines)-1], lines[len(lines)-2]
	if last != "Agent-Ticket: 7" {
		t.Fatalf("trailer must be the last line, got %q", last)
	}
	if strings.TrimSpace(prev) == "" {
		t.Fatalf("trailer must join the existing block, not sit after a blank line:\n%s", body)
	}
}

func TestStampTrailerRejectsNonPositiveTicket(t *testing.T) {
	dir := workspaceWith(t, "implement the thing")
	if _, err := harvest.StampTrailer(dir, 0); err == nil {
		t.Fatal("a non-positive ticket id must be rejected rather than stamped")
	}
}
