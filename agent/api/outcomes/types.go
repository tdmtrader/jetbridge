// Package outcomes holds the delivery-outcome domain types (shared-contracts
// §1.11/§1.11.1/§2.5): merge facts recorded by the outcome watcher and
// ticket-level dispositions recorded by humans.
package outcomes

import "errors"

//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 -generate

type MergeState string

const (
	MergeOpen       MergeState = "open"
	Merged          MergeState = "merged"
	MergedWithFixes MergeState = "merged_with_fixes"
	ClosedUnmerged  MergeState = "closed_unmerged"
	// MergeConcluded is the neutral terminal (flow-decoupling 2026-07-09,
	// FLOWS.md §4): run finished, human reviewed, no merge intended —
	// spike/research flows. Set only by a 'concluded' disposition; never
	// re-armed, never merge-scanned, and NOT a failure (scorecards exclude
	// it from merge-rate denominators, §1.11.1).
	MergeConcluded MergeState = "concluded"
)

// BotAuthor is the platform git identity (§8.3). Commits with this author
// are excluded from the human-touch delta (decision 18).
const BotAuthor = "concourse-agent[bot]"

const (
	DispositionSentBack  = "sent_back"
	DispositionAbandoned = "abandoned"
	// DispositionConcluded is the positive sibling of abandoned: the run is
	// done and reviewed, and no merge was ever intended (spike/research).
	// It maps to the needs_review → concluded lifecycle edge.
	DispositionConcluded = "concluded"
)

// DispositionReasons is the §1.11 reason taxonomy, in display order.
// research_complete: spike/research complete, no merge intended (pairs
// with the 'concluded' disposition).
var DispositionReasons = []string{
	"wrong_approach", "incomplete", "defective",
	"superseded", "not_needed", "style", "research_complete", "other",
}

func ValidDisposition(d string) bool {
	return d == DispositionSentBack || d == DispositionAbandoned || d == DispositionConcluded
}

func ValidDispositionReason(r string) bool {
	for _, v := range DispositionReasons {
		if r == v {
			return true
		}
	}
	return false
}

var (
	ErrOutcomeNotFound = errors.New("agent outcome not found")
	ErrNotOpen         = errors.New("agent outcome is not open")
)

// Outcome mirrors agent_outcomes (§2.5 plus §1.11.1 additive fields).
// Timestamps are epoch seconds.
type Outcome struct {
	TicketID          int        `json:"ticket_id"`
	Repo              string     `json:"repo"`
	Branch            string     `json:"branch"`
	PushedSha         string     `json:"pushed_sha"`
	BaseSha           string     `json:"base_sha,omitempty"`
	MergeState        MergeState `json:"merge_state"`
	MergedSha         string     `json:"merged_sha,omitempty"`
	MergedAt          int64      `json:"merged_at,omitempty"`
	HumanCommitCount  int        `json:"human_commit_count"`
	HumanLinesAdded   int        `json:"human_lines_added"`
	HumanLinesDeleted int        `json:"human_lines_deleted"`
	Disposition       string     `json:"disposition,omitempty"`
	DispositionReason string     `json:"disposition_reason,omitempty"`
	DispositionNotes  string     `json:"disposition_notes,omitempty"`
	DisposedBy        string     `json:"disposed_by,omitempty"`
	LastCheckedAt     int64      `json:"last_checked_at,omitempty"`
	CreatedAt         int64      `json:"created_at,omitempty"`
	UpdatedAt         int64      `json:"updated_at,omitempty"`
}

// MergeResult is what the watcher records when it detects a merge.
type MergeResult struct {
	State             MergeState // Merged or MergedWithFixes only
	MergedSha         string
	HumanCommitCount  int
	HumanLinesAdded   int
	HumanLinesDeleted int
}

// DispositionInput is a human's explicit ticket-level verdict.
type DispositionInput struct {
	Disposition string // sent_back | abandoned | concluded
	Reason      string // §1.11 taxonomy
	Notes       string // free text
	By          string // username
}

// Store is the persistence contract, implemented by
// atc/db.NewAgentOutcomesFactory and MemoryStore.
//
//counterfeiter:generate . Store
type Store interface {
	// Ensure inserts the row if absent (unique ticket_id). When the row
	// exists and merge_state = 'open', it refreshes branch/pushed_sha/
	// base_sha (re-push during the same review). It also RE-ARMS a row
	// that a send-back disposition drove to closed_unmerged: when
	// merge_state = 'closed_unmerged' AND disposition = 'sent_back', the
	// row is reset to 'open' with fresh branch/pushed_sha/base_sha so the
	// re-dispatch loop's eventual human merge is detected (F6). Other
	// terminal rows (merged/merged_with_fixes, closed_unmerged via
	// 'abandoned', or concluded) are untouched. Called by BOTH
	// exec.HarvestStep (push-time seeding, authoritative shas) and the
	// outcome watcher's seedRows backstop (fallback shas, create-if-absent
	// + re-arm only — see §1.11.1).
	Ensure(o *Outcome) error
	Get(ticketID int) (*Outcome, bool, error)
	// ListOpen returns rows with merge_state = 'open', oldest-first.
	ListOpen() ([]Outcome, error)
	// RecordMerge moves an open row to merged/merged_with_fixes and
	// stamps merged_at. ErrNotOpen if the row is terminal,
	// ErrOutcomeNotFound if absent.
	RecordMerge(ticketID int, res MergeResult) error
	// SetDisposition records the human verdict; an open row's
	// merge_state becomes closed_unmerged ('concluded' when the
	// disposition is 'concluded'), terminal states are kept.
	SetDisposition(ticketID int, d DispositionInput) error
	// Close closes an OPEN row to the given terminal merge_state without
	// touching disposition fields — the watcher's terminal sweep for tickets
	// closed by a bypassing raw-transition writer (§1.11.1 writer
	// reconciliation). No-op (nil) when the row is absent or not open.
	// state must be ClosedUnmerged or MergeConcluded.
	Close(ticketID int, state MergeState) error
	// Touch stamps last_checked_at = now.
	Touch(ticketID int) error
}
