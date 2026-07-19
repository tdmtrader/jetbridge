package outcomes_test

import (
	"testing"

	"github.com/concourse/concourse/agent/api/outcomes"
)

func TestDispositionValidation(t *testing.T) {
	if !outcomes.ValidDisposition("sent_back") || !outcomes.ValidDisposition("abandoned") {
		t.Error("sent_back and abandoned must be valid dispositions")
	}
	if !outcomes.ValidDisposition("concluded") {
		t.Error("concluded must be a valid disposition (flow-decoupling 2026-07-09)")
	}
	if outcomes.ValidDisposition("") || outcomes.ValidDisposition("merged") {
		t.Error("empty and merged must be invalid dispositions")
	}
	for _, r := range []string{"wrong_approach", "incomplete", "defective", "superseded", "not_needed", "style", "research_complete", "other"} {
		if !outcomes.ValidDispositionReason(r) {
			t.Errorf("reason %q must be valid", r)
		}
	}
	if outcomes.ValidDispositionReason("") || outcomes.ValidDispositionReason("meh") {
		t.Error("empty and unknown reasons must be invalid")
	}
}

func TestMemoryStoreEnsureRefreshesOnlyOpenRows(t *testing.T) {
	s := outcomes.NewMemoryStore()

	if err := s.Ensure(&outcomes.Outcome{TicketID: 7, Repo: "tdmtrader/concourse", Branch: "agent/ticket-7", PushedSha: "aaa", BaseSha: "bbb"}); err != nil {
		t.Fatal(err)
	}
	got, found, err := s.Get(7)
	if err != nil || !found {
		t.Fatalf("get after ensure: %v found=%v", err, found)
	}
	if got.MergeState != outcomes.MergeOpen || got.PushedSha != "aaa" {
		t.Fatalf("fresh row: %+v", got)
	}

	// open rows refresh branch/pushed/base on re-push
	if err := s.Ensure(&outcomes.Outcome{TicketID: 7, Repo: "tdmtrader/concourse", Branch: "agent/ticket-7", PushedSha: "ccc", BaseSha: "ddd"}); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.Get(7)
	if got.PushedSha != "ccc" || got.BaseSha != "ddd" {
		t.Fatalf("open row must refresh shas: %+v", got)
	}

	// merged rows do NOT refresh
	if err := s.RecordMerge(7, outcomes.MergeResult{State: outcomes.Merged, MergedSha: "eee"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Ensure(&outcomes.Outcome{TicketID: 7, Repo: "tdmtrader/concourse", Branch: "agent/ticket-7", PushedSha: "zzz"}); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.Get(7)
	if got.PushedSha != "ccc" {
		t.Fatalf("merged row must not refresh: %+v", got)
	}

	// F6: a send-back row (closed_unmerged + disposition='sent_back') is
	// RE-ARMED by Ensure — reset to open with fresh shas and cleared
	// disposition — so the re-dispatch loop's merge is detected.
	if err := s.Ensure(&outcomes.Outcome{TicketID: 8, Repo: "r", Branch: "agent/ticket-8", PushedSha: "p1", BaseSha: "b1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDisposition(8, outcomes.DispositionInput{Disposition: outcomes.DispositionSentBack, Reason: "incomplete", By: "u"}); err != nil {
		t.Fatal(err)
	}
	if g, _, _ := s.Get(8); g.MergeState != outcomes.ClosedUnmerged {
		t.Fatalf("send-back must close the row: %+v", g)
	}
	if err := s.Ensure(&outcomes.Outcome{TicketID: 8, Repo: "r", Branch: "agent/ticket-8", PushedSha: "p2", BaseSha: "b2"}); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.Get(8)
	if got.MergeState != outcomes.MergeOpen || got.PushedSha != "p2" || got.BaseSha != "b2" || got.Disposition != "" {
		t.Fatalf("sent_back row must re-arm to open with fresh shas + cleared disposition: %+v", got)
	}

	// An abandoned closed row is terminal — Ensure must NOT re-arm it.
	if err := s.Ensure(&outcomes.Outcome{TicketID: 9, Repo: "r", Branch: "agent/ticket-9", PushedSha: "q1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDisposition(9, outcomes.DispositionInput{Disposition: outcomes.DispositionAbandoned, Reason: "wont_do", By: "u"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Ensure(&outcomes.Outcome{TicketID: 9, Repo: "r", Branch: "agent/ticket-9", PushedSha: "q2"}); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.Get(9)
	if got.MergeState != outcomes.ClosedUnmerged || got.PushedSha != "q1" {
		t.Fatalf("abandoned row must stay closed and not refresh: %+v", got)
	}
}

func TestMemoryStoreConcludedDisposition(t *testing.T) {
	s := outcomes.NewMemoryStore()
	_ = s.Ensure(&outcomes.Outcome{TicketID: 3, Repo: "r", Branch: "agent/ticket-3", PushedSha: "p1", BaseSha: "b1"})

	// a concluded disposition closes the open row as 'concluded', NOT
	// closed_unmerged — it is a positive terminal, not a failure.
	if err := s.SetDisposition(3, outcomes.DispositionInput{
		Disposition: outcomes.DispositionConcluded, Reason: "research_complete",
		Notes: "findings in ticket body", By: "tdmtrader",
	}); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.Get(3)
	if got.MergeState != outcomes.MergeConcluded || got.Disposition != outcomes.DispositionConcluded {
		t.Fatalf("concluded must close the row as concluded: %+v", got)
	}

	// concluded is terminal: Ensure must NOT re-arm it (unlike sent_back).
	if err := s.Ensure(&outcomes.Outcome{TicketID: 3, Repo: "r", Branch: "agent/ticket-3", PushedSha: "p2", BaseSha: "b2"}); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.Get(3)
	if got.MergeState != outcomes.MergeConcluded || got.PushedSha != "p1" || got.Disposition != outcomes.DispositionConcluded {
		t.Fatalf("concluded row must stay terminal, keep its shas, and keep its disposition: %+v", got)
	}

	// and it leaves the watcher's open work-list permanently.
	open, err := s.ListOpen()
	if err != nil || len(open) != 0 {
		t.Fatalf("concluded row must leave ListOpen: %v %v", open, err)
	}
}

func TestMemoryStoreMergeAndDisposition(t *testing.T) {
	s := outcomes.NewMemoryStore()
	if err := s.RecordMerge(1, outcomes.MergeResult{State: outcomes.Merged}); err != outcomes.ErrOutcomeNotFound {
		t.Fatalf("merge on missing row: got %v", err)
	}
	_ = s.Ensure(&outcomes.Outcome{TicketID: 1, Repo: "r", Branch: "b", PushedSha: "p"})

	if err := s.RecordMerge(1, outcomes.MergeResult{State: outcomes.MergeOpen}); err == nil {
		t.Fatal("RecordMerge must reject a non-merged target state")
	}
	if err := s.RecordMerge(1, outcomes.MergeResult{
		State: outcomes.MergedWithFixes, MergedSha: "m",
		HumanCommitCount: 2, HumanLinesAdded: 10, HumanLinesDeleted: 3,
	}); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.Get(1)
	if got.MergeState != outcomes.MergedWithFixes || got.HumanCommitCount != 2 || got.MergedAt == 0 {
		t.Fatalf("merge fields not recorded: %+v", got)
	}
	if err := s.RecordMerge(1, outcomes.MergeResult{State: outcomes.Merged}); err != outcomes.ErrNotOpen {
		t.Fatalf("second merge: got %v, want ErrNotOpen", err)
	}

	open, err := s.ListOpen()
	if err != nil || len(open) != 0 {
		t.Fatalf("merged row must leave ListOpen: %v %v", open, err)
	}

	// disposition on an open row closes it
	_ = s.Ensure(&outcomes.Outcome{TicketID: 2, Repo: "r", Branch: "b2"})
	if err := s.SetDisposition(2, outcomes.DispositionInput{
		Disposition: "sent_back", Reason: "incomplete", Notes: "missing tests", By: "tdmtrader",
	}); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.Get(2)
	if got.MergeState != outcomes.ClosedUnmerged || got.Disposition != "sent_back" || got.DispositionReason != "incomplete" || got.DisposedBy != "tdmtrader" {
		t.Fatalf("disposition not recorded: %+v", got)
	}

	// disposition on a merged row keeps merge_state
	if err := s.SetDisposition(1, outcomes.DispositionInput{Disposition: "abandoned", Reason: "other", By: "x"}); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.Get(1)
	if got.MergeState != outcomes.MergedWithFixes {
		t.Fatalf("disposition must not overwrite a terminal merge_state: %+v", got)
	}

	if err := s.Touch(2); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.Get(2)
	if got.LastCheckedAt == 0 {
		t.Fatal("Touch must set last_checked_at")
	}
}

func TestMemoryStoreCloseSweepsOnlyOpenRows(t *testing.T) {
	s := outcomes.NewMemoryStore()
	_ = s.Ensure(&outcomes.Outcome{TicketID: 4, Repo: "r", Branch: "agent/ticket-4", PushedSha: "p", BaseSha: "b"})

	if err := s.Close(4, outcomes.ClosedUnmerged); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.Get(4)
	if got.MergeState != outcomes.ClosedUnmerged || got.Disposition != "" {
		t.Fatalf("close must set the state and never invent a disposition: %+v", got)
	}

	// closed rows are terminal for Close too
	if err := s.Close(4, outcomes.MergeConcluded); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := s.Get(4); got.MergeState != outcomes.ClosedUnmerged {
		t.Fatalf("close must not rewrite a closed row: %+v", got)
	}

	// absent row: benign no-op
	if err := s.Close(999, outcomes.ClosedUnmerged); err != nil {
		t.Fatal(err)
	}

	// invalid target state: error
	if err := s.Close(4, outcomes.Merged); err == nil {
		t.Fatal("Close(Merged) must be rejected — merges go through RecordMerge")
	}
}
