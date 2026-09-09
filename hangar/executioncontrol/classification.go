package executioncontrol

import (
	"encoding/json"
	"fmt"
)

// Classification is what is *durably known* about an exact execution. It is
// deliberately not what a Pod, a process table or a build row appears to say.
//
// The vocabulary is closed and it is frozen in
// testdata/protocol-v1/classifications.json. The two unhappy members carry the
// weight: `unresolved` and `lost` are the honest answers when the truth cannot
// be proved, and neither may ever be rounded down to "it finished" or up to
// "re-run it". Requirement 4 turns on exactly that distinction.
type Classification string

const (
	// ClassificationNeverStarted means the control ledger holds no start record
	// for this exact identity, so the command has provably not run. It is the
	// only classification from which a first start is admissible.
	ClassificationNeverStarted Classification = "never_started"

	// ClassificationExecuting means a start record exists and no outcome does.
	ClassificationExecuting Classification = "executing"

	// ClassificationAuthoritativeFinish means the supervisor durably recorded
	// the real exit outcome of the real process. This is the only member that
	// may authorize a durable capture.
	ClassificationAuthoritativeFinish Classification = "authoritative_finish"

	// ClassificationAuthoritativeStop means the process was durably recorded as
	// interrupted by a source-preserving stop. It is authoritative about the
	// interruption, not about success.
	ClassificationAuthoritativeStop Classification = "authoritative_stop"

	// ClassificationUnresolved means a start record exists, the outcome record
	// does not, and the node is still reachable. The answer may still arrive;
	// nothing destructive may happen while it might.
	ClassificationUnresolved Classification = "unresolved"

	// ClassificationLost means the ledger that would hold the answer is gone --
	// typically with its node. It is terminal and it is a failure: it can never
	// be read as success, and it can never authorize re-executing the command.
	ClassificationLost Classification = "lost"
)

// Classifications returns the closed vocabulary in its frozen order.
func Classifications() []Classification {
	return []Classification{
		ClassificationNeverStarted,
		ClassificationExecuting,
		ClassificationAuthoritativeFinish,
		ClassificationAuthoritativeStop,
		ClassificationUnresolved,
		ClassificationLost,
	}
}

// ParseClassification refuses anything outside the closed vocabulary, including
// the empty string.
func ParseClassification(value string) (Classification, error) {
	for _, member := range Classifications() {
		if string(member) == value {
			return member, nil
		}
	}

	return "", fmt.Errorf("%w: classification %q; the vocabulary is %v",
		ErrUnknownMember, value, Classifications())
}

// UnmarshalJSON refuses an unknown member rather than absorbing it into the
// zero value. A newer peer that learned a seventh outcome must be refused here,
// where the refusal is typed and visible, and not silently read as "".
func (classification *Classification) UnmarshalJSON(raw []byte) error {
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return err
	}
	parsed, err := ParseClassification(text)
	if err != nil {
		return err
	}
	*classification = parsed

	return nil
}

func (classification Classification) Validate() error {
	_, err := ParseClassification(string(classification))

	return err
}

// Authoritative reports whether this classification is backed by a durable
// acknowledgement of the real process. Only these two are.
func (classification Classification) Authoritative() bool {
	return classification == ClassificationAuthoritativeFinish ||
		classification == ClassificationAuthoritativeStop
}

// Terminal reports whether no further answer can arrive for this identity.
func (classification Classification) Terminal() bool {
	return classification.Authoritative() || classification == ClassificationLost
}

// AcknowledgementKind is the closed set of base ledger statements. There are
// three, and the extension in hangar/output adds its own set rather than
// widening this one -- which is what "base-only acknowledgements omit the
// extension fields" means structurally rather than by convention.
type AcknowledgementKind string

const (
	// AcknowledgementStart is written before the child process is launched, so
	// that a crash between the two is `unresolved` and never `never_started`.
	AcknowledgementStart AcknowledgementKind = "start"

	// AcknowledgementFinish carries the real exit outcome of the real process.
	AcknowledgementFinish AcknowledgementKind = "finish"

	// AcknowledgementStop records a source-preserving interruption.
	AcknowledgementStop AcknowledgementKind = "stop"
)

func AcknowledgementKinds() []AcknowledgementKind {
	return []AcknowledgementKind{
		AcknowledgementStart,
		AcknowledgementFinish,
		AcknowledgementStop,
	}
}

func ParseAcknowledgementKind(value string) (AcknowledgementKind, error) {
	for _, member := range AcknowledgementKinds() {
		if string(member) == value {
			return member, nil
		}
	}

	return "", fmt.Errorf("%w: acknowledgement kind %q; the vocabulary is %v",
		ErrUnknownMember, value, AcknowledgementKinds())
}

func (kind *AcknowledgementKind) UnmarshalJSON(raw []byte) error {
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return err
	}
	parsed, err := ParseAcknowledgementKind(text)
	if err != nil {
		return err
	}
	*kind = parsed

	return nil
}

func (kind AcknowledgementKind) Validate() error {
	_, err := ParseAcknowledgementKind(string(kind))

	return err
}

// Classification maps a kind to the classification it establishes.
func (kind AcknowledgementKind) Classification() Classification {
	switch kind {
	case AcknowledgementStart:
		return ClassificationExecuting
	case AcknowledgementFinish:
		return ClassificationAuthoritativeFinish
	case AcknowledgementStop:
		return ClassificationAuthoritativeStop
	default:
		return ""
	}
}
