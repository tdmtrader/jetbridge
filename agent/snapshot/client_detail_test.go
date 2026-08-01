package snapshot_test

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
)

func TestClientDetailIsFoundThroughWrappingAndJoining(t *testing.T) {
	marked := snapshot.ClientDetailf("archive path %q has a trailing separator", ".claude/")
	wrapped := errors.Join(
		snapshot.ErrInvalidArchive,
		fmt.Errorf("snapshot: capture upload: %w", marked),
	)

	detail, ok := snapshot.ClientDetail(wrapped)
	if !ok {
		t.Fatal("client detail was not found through the wrapped, joined chain")
	}
	if detail != `archive path ".claude/" has a trailing separator` {
		t.Fatalf("detail = %q", detail)
	}
	if !errors.Is(wrapped, snapshot.ErrInvalidArchive) {
		t.Fatal("marking broke the error class")
	}
}

func TestUnmarkedErrorsCarryNoClientDetail(t *testing.T) {
	unmarked := errors.Join(snapshot.ErrValidation, errors.New("secret /tmp/storage-node"))
	if detail, ok := snapshot.ClientDetail(unmarked); ok {
		t.Fatalf("unmarked error exposed detail %q", detail)
	}
}

// The outermost mark wins: a caller that adds context closer to the boundary
// is describing the same failure in more useful terms.
func TestOutermostClientDetailWins(t *testing.T) {
	inner := snapshot.ClientDetailf("adapter is required")
	outer := snapshot.WrapClientDetailf(inner, "work-item.json: %s", "adapter is required")
	detail, ok := snapshot.ClientDetail(outer)
	if !ok || detail != "work-item.json: adapter is required" {
		t.Fatalf("detail = %q ok = %v", detail, ok)
	}
}

// errors.As is a pre-order DFS over errors.Join's branches: within a single
// wrap chain the outermost mark wins (above), but across join branches the
// LEFTMOST branch wins regardless of how deep its mark sits relative to the
// other branch's mark. Every join in the seal path puts the causal error in
// branch 0 and cleanup errors after it, so this ordering is the one the
// mechanism relies on — verify it by construction rather than assuming it.
func TestClientDetailPicksLeftmostJoinBranchEvenWhenItsMarkIsDeeper(t *testing.T) {
	deepInBranch0 := fmt.Errorf("a: %w", fmt.Errorf("b: %w", snapshot.ClientDetailf("DEEP-B0")))
	shallowInBranch1 := snapshot.ClientDetailf("SHALLOW-B1")

	joined := errors.Join(deepInBranch0, shallowInBranch1)

	detail, ok := snapshot.ClientDetail(joined)
	if !ok {
		t.Fatal("expected a client detail")
	}
	if detail != "DEEP-B0" {
		t.Fatalf("detail = %q, want the leftmost branch's mark (DEEP-B0) even though it is deeper than SHALLOW-B1", detail)
	}
}

// Error() must not drop the wrapped error's own text: writeSnapshotError logs
// the full chain, and a marking site wrapping e.g. a decode failure would
// otherwise erase that text from the pod log. ClientDetail must still expose
// only the disclosable half.
func TestWrapClientDetailErrorComposesLogTextButClientDetailStaysBounded(t *testing.T) {
	underlying := errors.New("open /var/lib/concourse/scratch/xyz: permission denied")
	marked := snapshot.WrapClientDetailf(underlying, "work-item.json: adapter is required")

	if !strings.Contains(marked.Error(), underlying.Error()) {
		t.Fatalf("Error() = %q, want it to contain the wrapped error's text %q", marked.Error(), underlying.Error())
	}
	if !strings.Contains(marked.Error(), "work-item.json: adapter is required") {
		t.Fatalf("Error() = %q, want it to also contain the disclosable detail", marked.Error())
	}

	detail, ok := snapshot.ClientDetail(marked)
	if !ok || detail != "work-item.json: adapter is required" {
		t.Fatalf("detail = %q ok = %v", detail, ok)
	}
	if strings.Contains(detail, underlying.Error()) {
		t.Fatalf("ClientDetail leaked the wrapped error's text: %q", detail)
	}
}

// The wrapped error must stay reachable via errors.Is/As through the mark, so
// that e.g. fs.ErrNotExist is still classifiable after a validator wraps and
// marks it.
func TestWrapClientDetailPreservesTheWrappedError(t *testing.T) {
	marked := snapshot.WrapClientDetailf(fs.ErrNotExist, "required regular file %q is missing", "work-item.json")
	wrapped := errors.Join(snapshot.ErrValidation, fmt.Errorf("snapshot: validate upload: %w", marked))
	if !errors.Is(wrapped, fs.ErrNotExist) {
		t.Fatal("marking dropped the wrapped error; errors.Is no longer reaches fs.ErrNotExist")
	}
}
