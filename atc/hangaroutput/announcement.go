package hangaroutput

// What a capture tells the thing that owns the execution.
//
// Requirement 18: a capture-enabled task exposes the loss of post-completion
// hijack and the producer-checkpoint, sealing and capture outcomes in existing
// diagnostics. Nothing else in this plane emits anything a watcher can see --
// the daemon has logs and metrics, and those are for an operator. This is for
// whoever is watching the execution, and it is the only reason they ever learn
// that hijack went away.
//
// THE PAYLOAD IS THE WHOLE ASSERTION. "Never a warrant, key, path or consumer
// ref" can only fail if the payload is a closed set of fields, so it is: a
// kind, a disposition and a reason, and there is nowhere to put anything else.
// A struct with an escape hatch -- a map, a free-text detail, an error value --
// would be a struct through which an object key eventually travels, and no test
// over it could say otherwise.

import (
	"context"
	"fmt"

	"github.com/concourse/concourse/hangar/output"
)

// AnnouncementKind is the closed set of moments a watcher needs.
type AnnouncementKind string

const (
	// AnnouncementSelected is the one a user is owed BEFORE anything happens:
	// this execution's output is being captured, and post-completion hijack is
	// therefore unavailable. Announced at selection rather than at sealing,
	// because by the time sealing starts the hijack a user wanted is already
	// gone and telling them then explains a refusal instead of preventing one.
	AnnouncementSelected AnnouncementKind = "capture-selected"

	// AnnouncementSealStarted is the boundary: writers are being fenced and
	// drained, the producing pod, its sidecars and any active hijack session
	// are terminated here.
	AnnouncementSealStarted AnnouncementKind = "capture-seal-started"

	// AnnouncementDisposition is the terminal outcome, whichever it was.
	AnnouncementDisposition AnnouncementKind = "capture-disposition"
)

func AnnouncementKinds() []AnnouncementKind {
	return []AnnouncementKind{
		AnnouncementSelected,
		AnnouncementSealStarted,
		AnnouncementDisposition,
	}
}

// Announcement is a kind, a disposition and a reason. That is the whole type.
type Announcement struct {
	Kind AnnouncementKind

	// Disposition is empty until one is decided. It is the arbiter's branch,
	// never a guess: an announcement that named a disposition before the
	// arbiter was won would be telling a watcher an outcome the plane has not
	// committed to.
	Disposition output.Disposition

	// Reason is a closed-vocabulary word, not a message. `producer_failed`,
	// `seal_unconfirmed`, `cancelled`, `source_lost`, `captured`. A free-text
	// reason is where an object key ends up.
	Reason string
}

func (announcement Announcement) Validate() error {
	known := false
	for _, kind := range AnnouncementKinds() {
		if announcement.Kind == kind {
			known = true
		}
	}
	if !known {
		return fmt.Errorf("%w: announcement kind %q; the vocabulary is %v",
			output.ErrUnknownMember, announcement.Kind, AnnouncementKinds())
	}
	if announcement.Disposition != "" {
		if err := announcement.Disposition.Validate(); err != nil {
			return err
		}
	}
	if announcement.Kind == AnnouncementDisposition {
		if announcement.Reason == "" {
			return fmt.Errorf("%w: a terminal announcement carries no reason; a watcher told "+
				"only that something ended learns nothing they did not already know",
				output.ErrIncomplete)
		}
	}

	return nil
}

// announce composes one of the three. Every caller hands it to `say` inside the
// transaction that commits the fact being announced.
func (coordinator *Coordinator) announce(kind AnnouncementKind, disposition output.Disposition, reason string) Announcement {
	return Announcement{Kind: kind, Disposition: disposition, Reason: reason}
}

// terminalReason maps a settled record onto the closed reason vocabulary.
//
// It is a function over the record rather than a value each branch passes in,
// because the reason has to be the one the durable state says -- a branch that
// announced its own idea of why would be the one place a watcher and the plane
// could disagree.
func terminalReason(record output.HandoffRecord) string {
	if record.Disposition == nil {
		return ""
	}

	switch *record.Disposition {
	case output.DispositionCapture:
		if record.TerminalFailure != "" {
			return record.TerminalFailure
		}
		if record.State == output.CaptureStateCancelled {
			return "cancelled"
		}

		return "captured"

	case output.DispositionNoCapture:
		if record.FinishUnresolvable != "" {
			return string(record.FinishUnresolvable)
		}
		if record.FinishWitness != nil {
			return "producer_failed"
		}

		return string(output.NoCaptureAuthoritativeNonSuccess)

	case output.DispositionPreReservationCancel:
		return "cancelled"
	}

	return ""
}

// AnnouncerFunc adapts a plain function to the Announcer port.
//
// It takes the announcement APART into strings on the way out, so that the
// thing that stores or renders it does not have to import this package. A
// deployment's diagnostics are the deployment's; what stays here is the closed
// vocabulary and the rule that there is nothing else in the payload.
type AnnouncerFunc func(ctx context.Context, tx output.Tx, handoff output.HandoffID,
	kind, disposition, reason string) error

func (announce AnnouncerFunc) Announce(ctx context.Context, tx output.Tx,
	handoff output.HandoffID, announcement Announcement) error {
	return announce(ctx, tx, handoff, string(announcement.Kind),
		string(announcement.Disposition), announcement.Reason)
}
