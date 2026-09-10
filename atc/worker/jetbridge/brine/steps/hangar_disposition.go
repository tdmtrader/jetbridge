package steps

// Disposition: which outcome a finished producer selects, and what the build is
// told about it.
//
// This family is brine-shaped because it is sequential and observable: an
// arbiter picks one member of a closed vocabulary, and an announcement is a row
// production writes and production reads back — the same shape as the exit
// annotation step-closing.feature reads.
//
// NOTHING HERE COUNTS A CALL. Every assertion is over a durable row read back
// through a production reader, or over what is still on the node.
//
// What stays in Go and is cited rather than duplicated: conflicting replay (a
// race), deadline expiry (a clock, and convention 9 forbids a chain that
// waits), and every crash half — before/after a commit, an acknowledgement or a
// registration — which is the class brine cannot interpose on. Those are
// atc/hangaroutput/transitions_test.go and atc/hangaroutput/ambiguity_test.go.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/brine-dev/brine-go/pkg/brine"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/hangar/executioncontrol"
	hangaroutput "github.com/concourse/concourse/hangar/output"
)

// announcementOrder is the sequence Req 18 owes a watcher.
//
// It is a constant here rather than derived from the store, because ORDER is
// the assertion: `the build announces …` compares the position of what it was
// given against this, so three consecutive lines together say the whole
// sequence and each one still says something on its own.
var announcementOrder = []string{
	"capture-selected", "capture-seal-started", "capture-disposition",
}

// HangarDispositionDefinitions is the disposition family.
func HangarDispositionDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		// The whole control plane, driven to quiescence. See hangar_settle.go.
		brine.DefineMapUsing[FinishWitnessed, CaptureOutcome](
			"the capture settles",
			[]string{"jetbridge-db"},
			func(in FinishWitnessed, _ brine.Params, _ *brine.Recorder,
				res brine.Resources) (CaptureOutcome, error) {
				return settle(in, res)
			},
		),

		// A second execution beside the capture, selecting nothing.
		//
		// It is what makes the Req 18 absence assertable: "an ordinary step
		// announces none of them" passes on an emitter that announces nothing
		// at all, so the capture step's three announcements are asserted in the
		// same scenario and this adds a step that must produce none. The
		// recovery pass afterwards is the emitter's chance to get it wrong.
		brine.DefineMap[CaptureOutcome, CaptureOutcome](
			"an ordinary step runs beside it",
			func(in CaptureOutcome, _ brine.Params, _ *brine.Recorder) (CaptureOutcome, error) {
				ordinary := executioncontrol.Identity{
					ExecutionID: executioncontrol.ExecutionID(freshUUID()),
					Fence:       1,
				}

				admitted := in.Source.Draft.Daemon.base("admit", "/execution/v1/admit", ordinary,
					executioncontrol.Envelope{
						ProtocolVersion: executioncontrol.ProtocolVersion,
						Identity:        ordinary,
						ActivationEpoch: executioncontrol.ActivationEpoch(hangarEpoch),
						NodeUID:         hangarNodeUID,
						Capability:      "opaque-capability",
					})
				if _, err := decodeControl[executioncontrol.ClassifyResult](admitted); err != nil {
					return in, fmt.Errorf("admitting the ordinary execution: %w", err)
				}

				if in.Plane == nil {
					return in, fmt.Errorf("this capture never settled on a control plane")
				}
				if err := in.Plane.Recoverer.Run(context.Background()); err != nil {
					return in, fmt.Errorf("running the recovery pass: %w", err)
				}

				stored, err := in.Plane.allAnnouncements()
				if err != nil {
					return in, err
				}
				in.AllAnnouncements = stored

				return in, nil
			},
		),

		// Seal and publish from a PREDECLARATION, which is the one thing no
		// publication API may accept. The control -- the same operations served
		// from a committed Stage 2 reservation -- is the scenario above it.
		brine.DefineMapUsing[CaptureDraft, CaptureOutcome](
			"seal and publish are attempted from the predeclaration",
			[]string{"jetbridge-db"},
			func(in CaptureDraft, _ brine.Params, _ *brine.Recorder,
				res brine.Resources) (CaptureOutcome, error) {
				return sealFromPredeclaration(in, res)
			},
		),

		CheckString[CaptureOutcome]("the disposition is {string}",
			"the branch the arbiter selected",
			func(in CaptureOutcome) (string, error) {
				if in.Err != nil {
					return "", fmt.Errorf("the capture did not settle: %w", in.Err)
				}

				return string(in.Disposition), nil
			},
			func(in CaptureOutcome) string {
				return "transitions " + strings.Join(in.Transitions, " → ")
			}),

		CheckString[CaptureOutcome]("the no_capture reason is {string}",
			"the closed no_capture reason",
			func(in CaptureOutcome) (string, error) { return string(in.Reason), nil }),

		// The ORDERING assertion Req 5 makes: at the moment the checkpoint was
		// committed, the reservation carried no logical identity, no generation
		// and no receipt. A settled capture no longer shows that, which is why
		// the settle records the durable record after every transition.
		CheckThat[CaptureOutcome](
			"the producer checkpoint is committed with an unresolved reservation",
			func(in CaptureOutcome) error {
				stage, ok := in.snapshotAfter("commit_capture_reservation")
				if !ok {
					return fmt.Errorf("no Stage 2 commit happened at all (transitions: %v)",
						in.Transitions)
				}
				if !stage.HasCaptureReservation() {
					return fmt.Errorf("Stage 2 committed no reservation row")
				}
				if stage.ProducerCheckpointID == "" {
					return fmt.Errorf("the reservation carries no producer checkpoint")
				}
				if stage.State != hangaroutput.CaptureStateUnresolved {
					return fmt.Errorf("the reservation was already %s when the checkpoint "+
						"committed; Stage 2 creates a DISTINCT unresolved reservation and "+
						"resolves nothing", stage.State)
				}
				if stage.LogicalResolved {
					return fmt.Errorf("Stage 2 bound a scope and digest; the server derives " +
						"those after canonicalization, not from a finish witness")
				}
				if stage.Receipt != nil {
					return fmt.Errorf("Stage 2 produced a receipt")
				}

				return nil
			}),

		// Between the two halves of a no_capture close, the outcome is pending:
		// the source is still held, and nothing is settled. It is a claim about
		// a moment, so it is read from the snapshot after the first half.
		CheckThat[CaptureOutcome]("the outcome was pending between the two halves",
			func(in CaptureOutcome) error {
				first, ok := in.snapshotAfter("record_no_capture_intent")
				if !ok {
					return fmt.Errorf("no no_capture intent was recorded (transitions: %v)",
						in.Transitions)
				}
				if first.Settled {
					return fmt.Errorf("the handoff was settled as soon as no_capture was " +
						"recorded; the source is on a node until that node says otherwise")
				}
				if first.ReleaseAcknowledged {
					return fmt.Errorf("the release was acknowledged inside the same transaction " +
						"that recorded the intent; the two halves are two systems")
				}
				if first.ReleaseIntentID == "" {
					return fmt.Errorf("the first half recorded no release intent, so nothing " +
						"addresses the node holding the source")
				}

				return nil
			}),

		CheckThat[CaptureDraft]("the new build's handoff identity is not the old one",
			func(in CaptureDraft) error {
				if in.PreviousAdmission.HandoffID == "" {
					return fmt.Errorf("there is no previous admission to compare with")
				}
				if in.Admission.HandoffID == in.PreviousAdmission.HandoffID {
					return fmt.Errorf("the new build reused handoff %s", in.Admission.HandoffID)
				}
				if in.Admission.SourceLeaseID == in.PreviousAdmission.SourceLeaseID {
					return fmt.Errorf("the new build reused source lease %s",
						in.Admission.SourceLeaseID)
				}
				if in.Admission.Execution.ExecutionID ==
					in.PreviousAdmission.Execution.ExecutionID {
					return fmt.Errorf("the new build reused execution %s",
						in.Admission.Execution.ExecutionID)
				}

				return nil
			}),

		// Req 18's announcements, read back through the production reader in
		// EMISSION ORDER.
		//
		// Each line asserts that the announcement it names is there AND that it
		// stands where the sequence says it should. Three consecutive lines
		// therefore say the whole order, and each one still says something on
		// its own -- which a check that only asked for membership could not.
		brine.DefineCheck[CaptureOutcome]("the build announces {string}",
			func(in CaptureOutcome, p brine.Params, _ *brine.Recorder) error {
				want, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("the sentence names no announcement")
				}

				position := -1
				for i, announcement := range in.Announcements {
					if announcement.Kind == want {
						position = i
					}
				}
				if position < 0 {
					return fmt.Errorf("the capture announced %v and this line expects %q",
						announcementKinds(in.Announcements), want)
				}

				expected := -1
				for i, kind := range announcementOrder {
					if kind == want {
						expected = i
					}
				}
				if position != expected {
					return fmt.Errorf("%q is announcement %d and it is owed at %d; the sequence "+
						"was %v and it must be %v", want, position+1, expected+1,
						announcementKinds(in.Announcements), announcementOrder)
				}

				return nil
			}),

		// The release, as store state and never as a call count: the handoff is
		// settled, which on every branch means the daemon acknowledged the
		// exact fenced release of the source, and the durable acknowledgement
		// says so.
		//
		// It does NOT say the bytes are gone. A release releases the hold: the
		// incarnation is the step's own output, the artifact daemon has aliased
		// it read-only at the ordinary path, and deleting it here would be a
		// settlement destroying a step's output while the alias still pointed
		// at it. The two questions are asked separately below.
		CheckThat[CaptureOutcome]("the reserved incarnation is released",
			func(in CaptureOutcome) error {
				if !in.Final.Settled {
					return fmt.Errorf("the handoff is not settled, so the release has not run "+
						"(transitions %v)", in.Transitions)
				}
				if !in.Final.ReleaseAcknowledged {
					return fmt.Errorf("the handoff settled with no acknowledged release of the " +
						"reservation the ATC took before the Pod; only the node holding it can " +
						"say it is no longer held")
				}

				return nil
			}),

		// The other half, and the reason it is its own line: Req 2 says a
		// producer that did not succeed follows existing task semantics, and
		// existing semantics keep a failed task's outputs on the node for the
		// build's lifetime -- on_failure, hijack and artifact passing to a
		// later step all read them.
		CheckThat[CaptureOutcome]("the produced output is still on the node",
			func(in CaptureOutcome) error {
				root := in.Source.incarnationRoot()
				info, err := os.Stat(root)
				if err != nil {
					return fmt.Errorf("the settlement deleted the step's output at %s: %v", root, err)
				}
				if !info.IsDir() {
					return fmt.Errorf("%s is a %s, not the output directory", root, info.Mode().Type())
				}
				produced := filepath.Join(root, "artifact.txt")
				if _, err := os.Stat(produced); err != nil {
					return fmt.Errorf("the bytes the producer wrote are gone from %s: %v",
						produced, err)
				}

				return nil
			}),

		CheckThat[CaptureOutcome]("the build announces nothing about capture",
			func(in CaptureOutcome) error {
				if len(in.AllAnnouncements) == 0 {
					return fmt.Errorf("the store holds no announcements at all, so the capture " +
						"step beside it announced nothing either and this absence is vacuous")
				}
				for _, announcement := range in.AllAnnouncements {
					if announcement.Handoff != string(in.Source.Admission.HandoffID) {
						return fmt.Errorf("handoff %s was announced about, and it selected no "+
							"output for capture", announcement.Handoff)
					}
				}

				return nil
			}),

		// The payload asserted WHOLE, which is the only form in which "never a
		// grant, key, path or consumer ref" can fail.
		CheckThat[CaptureOutcome](
			"the announcement carries the disposition and reason and nothing else",
			func(in CaptureOutcome) error {
				if len(in.Announcements) == 0 {
					return fmt.Errorf("nothing was announced")
				}

				terminal := in.Announcements[len(in.Announcements)-1]
				if terminal.Kind != "capture-disposition" {
					return fmt.Errorf("the last announcement is %q", terminal.Kind)
				}
				if terminal.Disposition != string(in.Disposition) {
					return fmt.Errorf("the announcement says %q and the arbiter selected %q",
						terminal.Disposition, in.Disposition)
				}
				if terminal.Reason == "" {
					return fmt.Errorf("the announcement carries no reason")
				}

				// And the payload as a WHOLE: the three fields, and every one
				// of them a closed word. A grant, a key, a path or a consumer
				// reference would have to arrive in one of these, so each is
				// checked against what it may be rather than for what it must
				// not contain -- an absence check passes on anything it did not
				// think of.
				if !isClosedWord(terminal.Reason) {
					return fmt.Errorf("the reason is %q, which is not a closed vocabulary word; "+
						"a free-text reason is where an object key ends up", terminal.Reason)
				}
				if !isClosedWord(terminal.Kind) {
					return fmt.Errorf("the kind is %q", terminal.Kind)
				}

				return nil
			}),
	}
}

// isClosedWord reports whether a payload field is a vocabulary member rather
// than prose: lower-case letters, digits, hyphens and underscores, and short.
//
// It is a shape rule and not a list, because the list is the production
// vocabulary and duplicating it here would make this check pass whenever the
// two copies were edited together. What it really refuses is the thing a leak
// looks like: a path, a URL, a base64 grant, a sentence.
func isClosedWord(value string) bool {
	if value == "" || len(value) > 40 {
		return false
	}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}

	return true
}

func announcementKinds(announcements []db.HangarAnnouncement) []string {
	var kinds []string
	for _, announcement := range announcements {
		kinds = append(kinds, announcement.Kind)
	}

	return kinds
}
