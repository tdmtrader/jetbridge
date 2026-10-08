package output

import (
	"fmt"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
)

// The read warrant: hangar.PurposeReadResult.
//
// It is what a consuming Pod carries to the daemon. It is the one Hangar
// warrant under its own purpose, so a materialization warrant or a control
// warrant presented at a read route is refused, and it binds the READER'S
// CLAIM, because a managed read is only ever authorized by a committed claim
// and a token that did not name one could outlive the protection it was issued
// under.
//
// A read is: the web, in the consumer's transaction, acquires a claim with a
// term (the materialization timeout plus ReadClaimMargin) on the registered
// generation; then it mints a warrant bound to that claim's id, the ref, the
// destination handle and volume, the node, and the claim's own acquired-at and
// expires-at. The node daemon verifies the warrant against the Hangar key and
// the token's window, keeps it single-use per claim id on itself, and never
// calls the web. When the read ends the web releases the claim; an abandoned
// read's claim expires on its own.
//
// THE WARRANT'S WINDOW IS THE CLAIM'S WINDOW, and nothing in the token comes
// from the instant it was minted: no nonce, no mint time. Two mints of one
// committed claim are byte-identical, so a mint whose commit answer was lost
// is retried by acquiring the same claim again (idempotent) and minting again.
// A mint cannot extend the authority the database committed.

// ReadDestination is where a materialization may land, in the only vocabulary
// the node has: a step handle and one of its volumes.
//
// It is never a path. The daemon derives the path from these beneath its own
// managed root, exactly as the strict-input materializer does, so no caller --
// task, consumer or API -- can point a read anywhere of its own choosing.
type ReadDestination struct {
	Handle string `json:"handle"`
	Volume string `json:"volume"`
}

func (destination ReadDestination) Validate() error {
	if !validDestinationSegment(destination.Handle) {
		return fmt.Errorf("%w: destination handle %q is not a canonical path segment; a "+
			"destination is a handle and a volume, never a path", ErrInvalidIdentity,
			destination.Handle)
	}
	if !validDestinationSegment(destination.Volume) {
		return fmt.Errorf("%w: destination volume %q is not a canonical path segment",
			ErrInvalidIdentity, destination.Volume)
	}

	return nil
}

// validDestinationSegment is the foundation's rule, restated here rather than
// imported, because hangar's copy is unexported and this package may not widen
// it. Both must stay the same rule; readgrant_test.go's cross-check is what
// says so.
func validDestinationSegment(segment string) bool {
	if len(segment) < 1 || len(segment) > 128 {
		return false
	}
	alphanumeric := func(character byte) bool {
		return character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9'
	}
	if !alphanumeric(segment[0]) || !alphanumeric(segment[len(segment)-1]) {
		return false
	}
	for index := 0; index < len(segment); index++ {
		character := segment[index]
		if !alphanumeric(character) && character != '.' && character != '_' && character != '-' {
			return false
		}
	}

	return true
}

// ReadWarrant is the bound fields of a read warrant over one committed
// reader's claim: every dated value comes from the claim row itself.
//
// The claim is the parameter rather than a pile of fields because every dated
// value in the token must come from the committed row: a signature over a
// caller's idea of the claim would be a signature over a claim that may never
// have existed. A consumer's claim -- one with no expiry -- is refused: a read
// warrant has a window, and the window is the claim's. So is a released one.
func ReadWarrant(claim ClaimRecord, destination ReadDestination, node executioncontrol.NodeUID) (hangar.Warrant, error) {
	if err := claim.Validate(); err != nil {
		return hangar.Warrant{}, err
	}
	if err := destination.Validate(); err != nil {
		return hangar.Warrant{}, err
	}
	if claim.ExpiresAt == nil {
		return hangar.Warrant{}, fmt.Errorf("%w: claim %s has no expiry; a read warrant is minted over a "+
			"reader's expiring claim, never a consumer's hold", ErrIncomplete, claim.ClaimID)
	}
	if !claim.Active() {
		return hangar.Warrant{}, fmt.Errorf("%w: claim %s was released; a read warrant is minted over a live claim",
			ErrConflict, claim.ClaimID)
	}
	if node == "" {
		return hangar.Warrant{}, fmt.Errorf("%w: read warrant names no node", ErrIncomplete)
	}
	if !claim.ExpiresAt.After(claim.AcquiredAt.Time) {
		return hangar.Warrant{}, fmt.Errorf("%w: read warrant expires at or before it was issued", ErrIncomplete)
	}

	return hangar.Warrant{
		Purpose:   hangar.PurposeReadResult,
		Ref:       claim.Ref,
		Handle:    destination.Handle,
		Volume:    destination.Volume,
		ClaimID:   string(claim.ClaimID),
		NodeUID:   string(node),
		IssuedAt:  claim.AcquiredAt.UTC().UnixNano(),
		ExpiresAt: claim.ExpiresAt.UTC().UnixNano(),
	}, nil
}

// ReadWarrantMinter mints read warrants with the one Hangar signer. It is the
// web's managed-read signing authority (atc/hangaroutput.WarrantMinter).
type ReadWarrantMinter struct {
	Signer *hangar.Signer
}

// Sign mints the warrant for an already-committed reader's claim.
func (minter ReadWarrantMinter) Sign(claim ClaimRecord, destination ReadDestination, node executioncontrol.NodeUID) (string, error) {
	if minter.Signer == nil {
		return "", fmt.Errorf("%w: a read warrant minter needs the Hangar signer", ErrIncomplete)
	}
	warrant, err := ReadWarrant(claim, destination, node)
	if err != nil {
		return "", err
	}

	return minter.Signer.Sign(warrant)
}
