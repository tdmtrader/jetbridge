package harvest

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// TrailerKey ties a delivered commit to its ticket. It is the SCM-agnostic
// half of outcome tracking (design 2026-07-20 §5): `git log --grep` on the
// target branch finds it after a squash (forges concatenate messages), after
// a rebase (messages are preserved), and after a cherry-pick — all cases the
// patch-id heuristic in agent/gitcheck misses. Gerrit's Change-Id exists for
// exactly this reason.
const TrailerKey = "Agent-Ticket"

// Bot identity for the trailer amend. Matches outcomes.BotAuthor so a
// platform-authored commit is never counted as human touch.
const (
	BotName  = "concourse-agent[bot]"
	BotEmail = "agent@concourse.invalid"
)

// trailerLine matches a conventional git trailer ("Key: value"), used to
// decide whether the new trailer joins an existing block or starts one.
var trailerLine = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]*:\s`)

// StampTrailer ensures the tip commit carries "Agent-Ticket: <id>" and
// returns the resulting HEAD sha.
//
// The amend rewrites only the MESSAGE, so the tree hash is byte-identical
// and no gate result is invalidated by it. Idempotent: a commit that already
// carries the trailer is left alone and its sha returned unchanged, which
// matters for the fix-loop's re-push.
//
// CALLER ORDERING (load-bearing): call this AFTER the no-op guard — amending
// a HEAD that still equals the base would mint a fresh sha and silently
// defeat that guard — and BEFORE anything that records the head sha (the
// manifest, the push, deliverydiff.Capture, the push-done event).
func StampTrailer(dir string, ticketID int) (string, error) {
	if ticketID <= 0 {
		return "", fmt.Errorf("refusing to stamp a non-positive ticket id (%d)", ticketID)
	}
	want := TrailerKey + ": " + strconv.Itoa(ticketID)

	body, err := trailerGitRun(dir, "log", "-1", "--format=%B")
	if err != nil {
		return "", err
	}
	msg := strings.TrimRight(body, "\n")

	for _, line := range strings.Split(msg, "\n") {
		if strings.TrimSpace(line) == want {
			// already stamped — return the existing head untouched
			return trailerGitRun(dir, "rev-parse", "HEAD")
		}
	}

	if _, err := trailerGitRun(dir, "commit", "--amend", "--no-verify", "-m", appendTrailer(msg, want)); err != nil {
		return "", err
	}
	return trailerGitRun(dir, "rev-parse", "HEAD")
}

// appendTrailer joins an existing trailer block when the message already
// ends in one, and otherwise starts a new block after a blank line. Getting
// this wrong splits the block and breaks trailer parsers.
func appendTrailer(msg, trailer string) string {
	if msg == "" {
		return trailer
	}
	lines := strings.Split(msg, "\n")
	if trailerLine.MatchString(lines[len(lines)-1]) {
		return msg + "\n" + trailer
	}
	return msg + "\n\n" + trailer
}

func trailerGitRun(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	// A deterministic committer identity is REQUIRED, not cosmetic: the
	// harvest pod never commits (it only pushes), so it has no git identity
	// and `commit --amend` fails with "Committer identity unknown" — which
	// silently degraded every trailer to the best-effort error path. Caught
	// by CI, masked locally by an ambient ~/.gitconfig.
	//
	// --amend PRESERVES the original author (verified), so a human's commit
	// keeps its attribution and the human-touch delta — which filters on
	// author, not committer — is unaffected.
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME="+BotName,
		"GIT_AUTHOR_EMAIL="+BotEmail,
		"GIT_COMMITTER_NAME="+BotName,
		"GIT_COMMITTER_EMAIL="+BotEmail,
	)
	out, err := cmd.CombinedOutput()
	trimmed := strings.TrimSpace(string(out))
	if err != nil {
		return trimmed, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, trimmed)
	}
	return trimmed, nil
}
