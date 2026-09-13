package output

import (
	"encoding/json"
	"fmt"

	"github.com/concourse/concourse/hangar/executioncontrol"
)

// InventoryCursor is the single durable position of the whole-bucket sweep.
//
// It is a validated lexicographic (object key, generation) after-key plus a
// cycle counter, never an opaque continuation token. A provider's token is a
// promise about a session; this is a fact about the bucket, and only a fact
// survives a crash, a takeover and a restart. It advances only after every
// object in the reserved page has a committed disposition, and it wraps at
// bucket end, so an object inserted behind it is revisited next cycle rather
// than skipped forever.
//
// There is exactly one cursor and one lease owner per output bucket and
// activation epoch. Parallel cursor owners and caller-created partitions are
// forbidden: two sweeps sharing one after-key is how an object is neither
// adopted nor collected by either.
type InventoryCursor struct {
	ProtocolVersion string                           `json:"protocol_version"`
	ActivationEpoch executioncontrol.ActivationEpoch `json:"activation_epoch"`
	CursorFence     CursorFence                      `json:"cursor_fence"`
	AfterKey        string                           `json:"after_key"`
	AfterGeneration int64                            `json:"after_generation"`
	Cycle           uint64                           `json:"cycle"`
	UpdatedAt       Timestamp                        `json:"updated_at"`
}

func (cursor InventoryCursor) Validate() error {
	if err := validateProtocol(cursor.ProtocolVersion); err != nil {
		return err
	}
	if cursor.ActivationEpoch == 0 {
		return fmt.Errorf("%w: activation epoch is zero", ErrIncomplete)
	}
	if cursor.CursorFence == 0 {
		return fmt.Errorf("%w: cursor fence is zero; only the current fencing epoch may reserve "+
			"a page or advance the cursor", ErrIncomplete)
	}
	// An empty after-key is the legitimate start of a cycle. A generation
	// without a key is not: it would order against nothing.
	if cursor.AfterKey == "" && cursor.AfterGeneration != 0 {
		return fmt.Errorf("%w: cursor names generation %d with no after-key",
			ErrCorrupt, cursor.AfterGeneration)
	}
	if cursor.AfterGeneration < 0 {
		return fmt.Errorf("%w: cursor generation is negative", ErrCorrupt)
	}

	return cursor.UpdatedAt.Validate()
}

// AtCycleStart reports whether the cursor is positioned at the beginning of the
// output prefix. Recovery from a corrupt after-key restarts here, having first
// recorded the corruption as debt.
func (cursor InventoryCursor) AtCycleStart() bool {
	return cursor.AfterKey == ""
}

// DebtReason is the closed set of reasons one object could not be dispositioned.
//
// Debt is what keeps a single bad object from starving every key after it. It
// is committed before the cursor advances, so poison, stale ownership and
// cursor corruption cannot strand later objects, and none of them ever becomes
// authoritative absence.
type DebtReason string

const (
	// DebtPoisonMetadata is metadata that cannot be decoded at all.
	DebtPoisonMetadata DebtReason = "poison_metadata"

	// DebtCorruptCursor is an after-key that does not validate. The pass
	// records it and restarts at the validated output prefix.
	DebtCorruptCursor DebtReason = "corrupt_cursor"

	// DebtStatFailure is an exact-generation stat that did not answer.
	DebtStatFailure DebtReason = "stat_failure"

	// DebtListFailure is a listing that did not answer. The pass stops without
	// advancing: a short list is not the end of a bucket.
	DebtListFailure DebtReason = "list_failure"

	// DebtUnmanagedObject is an object with no marker. It is recorded and left
	// entirely alone -- never relabelled, never deleted.
	DebtUnmanagedObject DebtReason = "unmanaged_object"

	// DebtMarkerMismatch is a marker of the wrong version or one that does not
	// describe the object it is on.
	DebtMarkerMismatch DebtReason = "marker_mismatch"

	// DebtGenerationConflict is a replacement generation or metageneration
	// found where an exact one was expected. It becomes debt and never broadens
	// into an unconditional delete.
	DebtGenerationConflict DebtReason = "generation_conflict"
)

func DebtReasons() []DebtReason {
	return []DebtReason{
		DebtPoisonMetadata,
		DebtCorruptCursor,
		DebtStatFailure,
		DebtListFailure,
		DebtUnmanagedObject,
		DebtMarkerMismatch,
		DebtGenerationConflict,
	}
}

func ParseDebtReason(value string) (DebtReason, error) {
	for _, member := range DebtReasons() {
		if string(member) == value {
			return member, nil
		}
	}

	return "", fmt.Errorf("%w: debt reason %q; the vocabulary is %v",
		ErrUnknownMember, value, DebtReasons())
}

func (reason *DebtReason) UnmarshalJSON(raw []byte) error {
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return err
	}
	parsed, err := ParseDebtReason(text)
	if err != nil {
		return err
	}
	*reason = parsed

	return nil
}

func (reason DebtReason) Validate() error {
	_, err := ParseDebtReason(string(reason))

	return err
}

// InventoryDebt is one recorded per-object failure.
//
// ObjectKey is a *server-observed* value read out of a listing of the
// deployment's own bucket, not something a caller supplied. That is why it can
// appear here while no API in this package accepts a key as a parameter: the
// rule is about who chooses a location, not about whether a location can ever
// be written down.
type InventoryDebt struct {
	ProtocolVersion string                           `json:"protocol_version"`
	ActivationEpoch executioncontrol.ActivationEpoch `json:"activation_epoch"`
	ObjectKey       string                           `json:"object_key"`
	Generation      int64                            `json:"generation"`
	Reason          DebtReason                       `json:"reason"`
	Attempts        int                              `json:"attempts"`
	ObservedAt      Timestamp                        `json:"observed_at"`
	Detail          string                           `json:"detail"`
}

// MaxDebtDetailBytes bounds the free-text diagnosis so a poisoned object cannot
// turn a debt row into an unbounded write.
const MaxDebtDetailBytes = 2048

func (debt InventoryDebt) Validate() error {
	if err := validateProtocol(debt.ProtocolVersion); err != nil {
		return err
	}
	if debt.ActivationEpoch == 0 {
		return fmt.Errorf("%w: activation epoch is zero", ErrIncomplete)
	}
	if debt.ObjectKey == "" {
		return fmt.Errorf("%w: debt names no object", ErrIncomplete)
	}
	if err := debt.Reason.Validate(); err != nil {
		return err
	}
	if debt.Attempts < 1 {
		return fmt.Errorf("%w: debt records %d attempts", ErrIncomplete, debt.Attempts)
	}
	if len(debt.Detail) > MaxDebtDetailBytes {
		return fmt.Errorf("%w: debt detail is %d bytes, the bound is %d",
			ErrLimitExceeded, len(debt.Detail), MaxDebtDetailBytes)
	}

	return debt.ObservedAt.Validate()
}
