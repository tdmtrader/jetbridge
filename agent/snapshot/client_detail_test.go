package snapshot_test

import (
	"errors"
	"fmt"
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
