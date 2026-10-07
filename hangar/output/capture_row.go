package output

// The capture row: the only durable state a capture has.
//
// One row per (execution, output). Every step of the capture sequence is a
// compare-and-set on its state, made by the control plane; the node daemon
// answers questions and never writes it. The node-local half is one marker
// file per step directory (CaptureMarker), and nothing here mirrors it.
//
//	pending ──▶ publishing ──▶ published
//	   │             │
//	   ├──▶ discarded └──▶ failed
//	   └──▶ failed
//
// A row leaves the plane's protection only through the release pass: a
// terminal row whose released_at is still empty has a marker on its node, and
// the node's sweeper refuses that directory until the release lands.

import (
	"fmt"
	"slices"
	"time"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/google/uuid"
)

// CaptureState is the closed vocabulary of a capture row.
type CaptureState string

const (
	CapturePending    CaptureState = "pending"
	CapturePublishing CaptureState = "publishing"
	CapturePublished  CaptureState = "published"
	CaptureDiscarded  CaptureState = "discarded"
	CaptureFailed     CaptureState = "failed"
)

// CaptureStates is the closed set, in lifecycle order.
func CaptureStates() []CaptureState {
	return []CaptureState{CapturePending, CapturePublishing, CapturePublished, CaptureDiscarded, CaptureFailed}
}

func (state CaptureState) Validate() error {
	if !slices.Contains(CaptureStates(), state) {
		return fmt.Errorf("%w: capture state %q; the vocabulary is %v", ErrUnknownMember, state, CaptureStates())
	}

	return nil
}

// Terminal reports whether no further state transition exists.
func (state CaptureState) Terminal() bool {
	return state == CapturePublished || state == CaptureDiscarded || state == CaptureFailed
}

// Discard reasons: why a pending capture became discarded. A closed set, so a
// watcher can tell a cancellation from a producer that did not succeed.
const (
	DiscardRunCancelled    = "run_cancelled"
	DiscardBuildAborted    = "build_aborted"
	DiscardProducerFailed  = "producer_failed"
	DiscardProducerStopped = "producer_stopped"
)

// CaptureKey names one capture: which execution, which declared output.
type CaptureKey struct {
	Execution executioncontrol.ExecutionID `json:"execution_id"`
	Output    OutputName                   `json:"output"`
}

func (key CaptureKey) Validate() error {
	if err := key.Execution.Validate(); err != nil {
		return err
	}

	return key.Output.Validate()
}

func (key CaptureKey) String() string { return string(key.Execution) + "/" + string(key.Output) }

// CaptureDirectory is the ONE derivation of a step directory from a capture,
// relative to the node daemon's managed steps directory. The pod's hostPath,
// the daemon's marker and the sweeper's classifier all key on this string;
// hangar/output/ledger restates it (a reader must not import its writer) and a
// test pins the two spellings together.
func (key CaptureKey) Directory() string {
	return fmt.Sprintf("%s.capture/%s", key.Execution, key.Output)
}

// captureNamespace is the fixed UUID namespace capture-derived identities are
// minted under. Changing it orphans every claim a published row holds.
var captureNamespace = uuid.MustParse("6b1f9a52-3c0e-5d6a-9f7b-2c4e8d1a0b37")

// ClaimID is the claim the capture's own publication holds on its tree ref.
//
// Derived rather than minted, so that the transaction that moves a row to
// published and any later reader agree on it without a column: the claim is
// acquired in that transaction, idempotently, and a consumer that binds the
// result names the same id.
func (key CaptureKey) ClaimID() ClaimID {
	return ClaimID(uuid.NewSHA1(captureNamespace, []byte("claim\x00"+key.String())).String())
}

// MarkerID is the identity an object created for this capture is marked
// with. Deterministic for the same reason: a recreate after a lost answer
// writes the same marker.
func (key CaptureKey) MarkerID() ReservationID {
	return ReservationID(uuid.NewSHA1(captureNamespace, []byte("marker\x00"+key.String())).String())
}

// Capture is one row.
type Capture struct {
	Key        CaptureKey
	State      CaptureState
	Node       string
	NodeUID    executioncontrol.NodeUID
	PodUID     executioncontrol.PodUID
	Scope      hangar.Scope
	Digest     hangar.Digest
	Generation int64
	Error      string

	CaptureDeadline time.Time
	CreatedAt       time.Time
	FinishedAt      *time.Time
	ReleasedAt      *time.Time
}

// Ref is the published tree ref. Only a published row has one.
func (capture Capture) Ref() (hangar.TreeRef, error) {
	if capture.State != CapturePublished {
		return hangar.TreeRef{}, fmt.Errorf("%w: capture %s is %s, not published",
			ErrUnresolved, capture.Key, capture.State)
	}

	ref := hangar.TreeRef{Scope: capture.Scope, Digest: capture.Digest, Generation: capture.Generation}

	return ref, ref.Validate()
}

// Released reports whether the node's marker has been cleared.
func (capture Capture) Released() bool { return capture.ReleasedAt != nil }
