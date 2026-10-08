package steps

// What the artifact daemon ANSWERS about a capture's marker: the held marker,
// witnesses, refusals, containment. The marker is a capture's only node-local
// state -- held, sealed, or released (a tombstone) -- one file per step
// directory.
//
// This family is driven against the real binary through the fixture in
// hangar_fixture.go, for the reason realdaemon.go records -- a double can be
// made to refuse, but it cannot tell you the answer is RIGHT, and here half of
// what an operation does is a change to a node's filesystem that no response
// shows.
//
// NOTHING HERE COUNTS A REQUEST. "The daemon was not called" is never an
// assertion in this file; the scenarios that mean it say the source is still
// held, or that the store's copy is still the store's copy.
//
// The fixture plays the ATC and the supervisor. In Phase 4 execProcess takes
// both parts over; what does not change is that every answer below comes from
// the daemon over HTTP and is decoded with the production types.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/brine-dev/brine-go/pkg/brine"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	hangaroutput "github.com/concourse/concourse/hangar/output"
)

// heldFrom carries a draft's identities into the held state.
func heldFrom(draft CaptureDraft, ack hangaroutput.CaptureHoldAcknowledgement, answer controlAnswer) HeldSource {
	return HeldSource{
		Draft: HeldDraft{
			Handle: string(draft.Admission.Execution.ExecutionID),
			Output: draft.Output,
			Daemon: draft.Daemon,
		},
		DaemonURL:       draft.Daemon.Output.URL,
		StorageRoot:     draft.Daemon.Output.Root,
		Acknowledgement: ack,
		Execution:       draft.Admission.Execution,
		Admission:       draft.Admission,
		PodUID:          draft.PodUID,
		Status:          answer.Status,
		Body:            answer.Body,
		Err:             answer.Err,
	}
}

// answered replaces the last answer without disturbing the identities. A
// refusal is a VALUE here, which is what lets "the refusal says" be a step
// rather than a fatal.
func (source HeldSource) answered(answer controlAnswer) HeldSource {
	source.Status, source.Body, source.Err = answer.Status, answer.Body, answer.Err

	return source
}

// hold is the capture control init's request, made from inside the Pod: the
// execution, the declared output and the Pod UID the container read off the
// Downward API. It names no path: the daemon derives the step directory from
// the capture's key.
func (draft CaptureDraft) hold() controlAnswer {
	return draft.Daemon.capture("hold", "/capture/v1/hold", draft.Admission.Execution,
		holdBody(draft.Admission.Execution, draft.Admission.Output, draft.PodUID))
}

// HangarCaptureMarkerDefinitions is the capture-marker family.
func HangarCaptureMarkerDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMap[CaptureDraft, HeldSource](
			"the daemon writes the held marker",
			func(in CaptureDraft, _ brine.Params, _ *brine.Recorder) (HeldSource, error) {
				// The hold is the capture control init's request, made from
				// inside the Pod before any writer starts. It names the
				// execution, the output and the Pod; the daemon writes the held
				// marker and creates the step directory the key derives.
				answer := in.hold()
				ack, err := decodeControl[hangaroutput.CaptureHoldAcknowledgement](answer)
				if err != nil {
					return HeldSource{}, fmt.Errorf("establishing the hold: %w", err)
				}
				if err := ack.Validate(); err != nil {
					return HeldSource{}, fmt.Errorf("the hold answer does not validate: %w", err)
				}
				if ack.Marker.Key() != in.Admission.Key() || ack.Marker.PodUID != in.PodUID ||
					ack.Marker.State != hangaroutput.StepHeld {
					return HeldSource{}, fmt.Errorf("the daemon held %s for pod %s in state %s; "+
						"the hold asked for %s in pod %s", ack.Marker.Key(), ack.Marker.PodUID,
						ack.Marker.State, in.Admission.Key(), in.PodUID)
				}

				held := heldFrom(in, ack, answer)

				// The bytes a producer wrote, written HERE rather than at the
				// witness, and the move is the point. The source-preserving
				// stop scenario asserts that the step directory survives the
				// stop; writing into it while witnessing made a stop that
				// removed the directory fail on the `When` instead, so the
				// named `Then` was never reached. The content is deterministic
				// and shared, which is what lets two captures of "the same
				// canonical bytes" really be the same bytes.
				if err := os.WriteFile(filepath.Join(held.stepRoot(), "artifact.txt"),
					[]byte("the bytes a producer wrote\n"), 0o600); err != nil {
					return HeldSource{}, fmt.Errorf("writing the produced source: %w", err)
				}

				return held, nil
			},
		),

		brine.DefineMap[HeldSource, HeldSource](
			"the held marker is requested again with the same identity",
			func(in HeldSource, _ brine.Params, _ *brine.Recorder) (HeldSource, error) {
				answer := in.Draft.Daemon.capture("hold", "/capture/v1/hold",
					in.Execution, holdBody(in.Execution, in.Admission.Output, in.PodUID))
				repeated, err := decodeControl[hangaroutput.CaptureHoldAcknowledgement](answer)
				if err == nil {
					in.Repeated = repeated
				}

				return in.answered(answer), nil
			},
		),

		// The conflict twin. "Repeating the hold returns the same marker"
		// passes for a daemon that ignores the Pod entirely, so the twin has
		// to show that a hold from a DIFFERENT Pod -- a recreated one -- is a
		// typed conflict: a replacement Pod is a new writer and does not
		// inherit a hold.
		brine.DefineMap[HeldSource, HeldSource](
			"the held marker is requested again for a different pod",
			func(in HeldSource, _ brine.Params, _ *brine.Recorder) (HeldSource, error) {
				return in.answered(in.Draft.Daemon.capture("hold", "/capture/v1/hold",
					in.Execution, holdBody(in.Execution, in.Admission.Output,
						executioncontrol.PodUID(freshUUID())))), nil
			},
		),

		// A hold presented under the fence the takeover superseded. Every
		// capture route is admitted against the base ledger's CURRENT fence.
		brine.DefineMap[HeldSource, HeldSource](
			"a stale fence is presented",
			func(in HeldSource, _ brine.Params, _ *brine.Recorder) (HeldSource, error) {
				stale := in.Execution
				stale.Fence = in.Execution.Fence - 1

				return in.answered(in.Draft.Daemon.capture("hold", "/capture/v1/hold",
					stale, holdBody(stale, in.Admission.Output, in.PodUID))), nil
			},
		),

		// The base execution-control protocol's takeover: the execution is
		// admitted again under the next fence, which makes the old one stale.
		brine.DefineMap[HeldSource, HeldSource](
			"the execution is taken over under a newer fence",
			func(in HeldSource, _ brine.Params, _ *brine.Recorder) (HeldSource, error) {
				taken := in.Execution
				taken.Fence++

				answer := in.Draft.Daemon.base("admit", "/execution/v1/admit", taken,
					executioncontrol.Envelope{
						ProtocolVersion: executioncontrol.ProtocolVersion,
						Identity:        taken,
						NodeUID:         hangarNodeUID,
						Capability:      "opaque-takeover-capability",
					})
				if _, err := decodeControl[executioncontrol.ClassifyResult](answer); err != nil {
					return in, fmt.Errorf("taking the execution over: %w", err)
				}
				// The old fence is now stale, and the state carries the NEW one
				// so the line after this can ask the daemon about it.
				in.Execution = taken

				return in.answered(answer), nil
			},
		),

		// A path a hostile client really can send. It is a raw map because the
		// whole point is a field no production type declares.
		brine.DefineMap[HeldSource, HeldSource](
			"the held-marker request names a path instead of a step",
			func(in HeldSource, _ brine.Params, _ *brine.Recorder) (HeldSource, error) {
				return in.answered(in.Draft.Daemon.rawCapture("hold", "/capture/v1/hold",
					in.Execution, map[string]any{
						"protocol_version": hangaroutput.ProtocolVersion,
						"execution":        in.Execution,
						"output":           in.Admission.Output,
						"pod_uid":          in.PodUID,
						"path":             in.stepRoot(),
					})), nil
			},
		),

		// The step directory swapped for a symlink after the hold, and the
		// producer gone. The seal is the operation that resolves the directory
		// to canonicalize it, so it is the one that must refuse: the bytes a
		// capture seals are the ones the producer wrote there.
		brine.DefineMap[HeldSource, HeldSource](
			"the step directory is replaced by a symlink to {string}",
			func(in HeldSource, p brine.Params, _ *brine.Recorder) (HeldSource, error) {
				target, err := paramAt("the step directory is replaced by a symlink to {string}", p, 0)
				if err != nil {
					return in, err
				}
				root := in.stepRoot()
				if err := os.RemoveAll(root); err != nil {
					return in, err
				}
				if err := os.Symlink(target, root); err != nil {
					return in, err
				}
				if err := in.Draft.Daemon.terminate(in.PodUID); err != nil {
					return in, err
				}

				return in.answered(in.Draft.Daemon.capture("seal", "/capture/v1/seal",
					in.Execution, in.sealRequest())), nil
			},
		),

		// A VALID token, at a route it does not belong to. The control is the
		// same token succeeding at its own operation, which is why the facet is
		// a parameter of the client rather than derived from the path.
		brine.DefineMap[HeldSource, HeldSource](
			"a base control capability is used to {string}",
			func(in HeldSource, p brine.Params, _ *brine.Recorder) (HeldSource, error) {
				operation, err := paramAt("a base control capability is used to {string}", p, 0)
				if err != nil {
					return in, err
				}
				switch operation {
				case "classify":
					return in.answered(in.Draft.Daemon.base("classify", "/execution/v1/classify",
						in.Execution, identifiedBy(in.Execution))), nil
				case "publish":
					return in.answered(in.Draft.Daemon.control(executioncontrol.BaseFacet,
						"publish", "/capture/v1/publish", in.Execution,
						in.publishRequest(placeholderDigest))), nil
				case "hold":
					return in.answered(in.Draft.Daemon.control(executioncontrol.BaseFacet,
						"hold", "/capture/v1/hold", in.Execution,
						holdBody(in.Execution, in.Admission.Output, in.PodUID))), nil
				case "seal":
					return in.answered(in.Draft.Daemon.control(executioncontrol.BaseFacet,
						"seal", "/capture/v1/seal", in.Execution, in.sealRequest())), nil
				}

				return in, fmt.Errorf("no base control operation named %q; the scenario vocabulary "+
					"is classify, hold, seal and publish", operation)
			},
		),

		// Two sentences rather than one, because the two cases start from
		// different states: eligibility is asked after a witness in the
		// permitted case and before one in its absence twin, and brine's
		// registry gives one pattern exactly one input type.
		brine.DefineMap[FinishWitnessed, FinishWitnessed](
			"destructive cleanup is requested",
			func(in FinishWitnessed, _ brine.Params, _ *brine.Recorder) (FinishWitnessed, error) {
				// The release is the other half of the gate, and it is done
				// here rather than in a Given so that the permitted case really
				// has both halves and the refused case really has neither.
				released := in.Source.release()
				if _, err := decodeControl[hangaroutput.CaptureReleaseAcknowledgement](released); err != nil {
					return in, fmt.Errorf("releasing the hold: %w", err)
				}

				return in.asked(in.Source.Draft.Daemon.base("cleanup-eligible",
					"/execution/v1/cleanup-eligible", in.Source.Execution,
					identifiedBy(in.Source.Execution))), nil
			},
		),

		brine.DefineMap[HeldSource, FinishWitnessed](
			"destructive cleanup is requested before any witness",
			func(in HeldSource, _ brine.Params, _ *brine.Recorder) (FinishWitnessed, error) {
				witnessed := FinishWitnessed{Source: in}

				return witnessed.asked(in.Draft.Daemon.base("cleanup-eligible",
					"/execution/v1/cleanup-eligible", in.Execution,
					identifiedBy(in.Execution))), nil
			},
		),

		// A release of a failed producer's hold, and it takes a WITNESSED step
		// rather than a held source: there is no production path on which a
		// hold is released for a producer nobody has heard from -- the
		// control plane releases a capture whose row is terminal, and a row is
		// terminal only after the node's finish or stop.
		//
		// It returns the HeldSource so the two node-side checks either side of
		// it -- the held step directory is still on the node, the marker is
		// released and the step directory remains -- read
		// the same step directory on the same node.
		brine.DefineMap[FinishWitnessed, HeldSource](
			"the daemon releases the marker",
			func(in FinishWitnessed, _ brine.Params, _ *brine.Recorder) (HeldSource, error) {
				return in.Source.answered(in.Source.release()), nil
			},
		),

		brine.DefineMap[HeldSource, HeldSource](
			"the publish request also carries a caller-chosen {string}",
			func(in HeldSource, p brine.Params, _ *brine.Recorder) (HeldSource, error) {
				field, err := paramAt("the publish request also carries a caller-chosen {string}", p, 0)
				if err != nil {
					return in, err
				}
				publication := in.publishRequest(placeholderDigest)
				switch field {
				case "bucket":
					publication.Namespace.Bucket = "somebody-elses-bucket"
				case "scope":
					publication.Namespace.Scope = "o0000000000000000000000000000000000000000"
				case "key":
					publication.Namespace.Key = "hangar/v1/scopes/x/trees/sha256/dead.tar.zst"
				case "prefix":
					publication.Namespace.Prefix = "deployments/red"
				default:
					return in, fmt.Errorf("no caller-chosen field named %q", field)
				}

				return in.answered(in.Draft.Daemon.capture("publish", "/capture/v1/publish",
					in.Execution, publication)), nil
			},
		),

		// The supervisor's two writes, played by the fixture.
		brine.DefineMap[HeldSource, FinishWitnessed](
			"the step finishes and the daemon witnesses it",
			func(in HeldSource, _ brine.Params, _ *brine.Recorder) (FinishWitnessed, error) {
				return in.witness(executioncontrol.AcknowledgementFinish,
					executioncontrol.ExitOutcome{ExitCode: 0})
			},
		),

		brine.DefineMap[HeldSource, FinishWitnessed](
			"the step fails",
			func(in HeldSource, _ brine.Params, _ *brine.Recorder) (FinishWitnessed, error) {
				return in.witness(executioncontrol.AcknowledgementFinish,
					executioncontrol.ExitOutcome{ExitCode: 2})
			},
		),

		brine.DefineMap[HeldSource, FinishWitnessed](
			"the step is stopped without destroying its step directory",
			func(in HeldSource, _ brine.Params, _ *brine.Recorder) (FinishWitnessed, error) {
				if err := in.recordStart(); err != nil {
					return FinishWitnessed{}, err
				}
				stopped := in.Draft.Daemon.base("stop", "/execution/v1/stop", in.Execution,
					identifiedBy(in.Execution))
				if _, err := decodeControl[executioncontrol.RequestSourcePreservingStopResult](stopped); err != nil {
					return FinishWitnessed{}, fmt.Errorf("requesting the stop: %w", err)
				}

				return in.recordWitness(executioncontrol.AcknowledgementStop,
					executioncontrol.ExitOutcome{ExitCode: 143, Signalled: true, Signal: "TERM"})
			},
		),

		// Cancellation is a REQUEST and not a row. What the state carries is
		// the question; what the capture row then becomes is the plane's
		// answer, and no phrase here decides it.
		//
		// It takes a WITNESSED step rather than a held source, and that is the
		// whole point of the phrase: a cancelled capture is discarded as
		// run_cancelled and never as producer_failed, and the only way a
		// scenario can be false against that "never" is for the producer to
		// have a non-success outcome the plane could confuse it with.
		brine.DefineMap[FinishWitnessed, FinishWitnessed](
			"the capture is cancelled before it is sealed",
			func(in FinishWitnessed, _ brine.Params, _ *brine.Recorder) (FinishWitnessed, error) {
				in.Cancelled = true

				return in, nil
			},
		),

		// The absence: a capture cancelled with no hold ever made. The row
		// exists -- it is inserted when the step is admitted, before the Pod
		// -- and the control init never ran, so there is no marker on the
		// node to release. The scenario above is its control.
		brine.DefineMap[CaptureDraft, FinishWitnessed](
			"the capture is cancelled with no held marker",
			func(in CaptureDraft, _ brine.Params, _ *brine.Recorder) (FinishWitnessed, error) {
				source := HeldSource{
					Draft: HeldDraft{
						Handle: string(in.Admission.Execution.ExecutionID),
						Output: in.Output,
						Daemon: in.Daemon,
					},
					DaemonURL:   in.Daemon.Output.URL,
					StorageRoot: in.Daemon.Output.Root,
					Execution:   in.Admission.Execution,
					Admission:   in.Admission,
					PodUID:      in.PodUID,
				}

				return FinishWitnessed{Source: source, Cancelled: true}, nil
			},
		),

		// Checks over a held source. The daemon's own status line is spelled
		// "the Hangar daemon answers" rather than reusing the durable tier's
		// "the daemon's answer is {int}": one pattern has exactly one input
		// type, and the shadowing guard catches a second definition of a
		// sentence that already exists.
		CheckInt[HeldSource]("the Hangar daemon answers {int}, the marker held",
			"the daemon's status",
			func(in HeldSource) (int, error) {
				if in.Err != nil {
					return 0, in.Err
				}

				return in.Status, nil
			},
			func(in HeldSource) string { return "body: " + abbrev(string(in.Body)) }),

		// A REFUSAL, and one that names its reason. Both halves matter: a
		// status alone cannot tell a collision from an unauthorized scope from
		// a source that is sealed, and all three are 409, so the assertion is
		// on the body -- and a 200 fails here before the body is even looked
		// at, so "says nothing because it succeeded" cannot pass.
		CheckContains[HeldSource]("the daemon's refusal says {string}",
			"the daemon's refusal",
			func(in HeldSource) (string, error) {
				if in.Err != nil {
					return "", fmt.Errorf("no answer at all: %w", in.Err)
				}
				if in.Status/100 == 2 {
					return "", fmt.Errorf("the daemon did not refuse: %d %s",
						in.Status, abbrev(string(in.Body)))
				}

				return string(in.Body), nil
			},
			func(in HeldSource) string { return fmt.Sprintf("status %d", in.Status) }),

		CheckThat[HeldSource]("the held marker is the one the first request wrote",
			func(in HeldSource) error {
				if in.Repeated.Kind == "" {
					// The repeat was refused, so the marker in force is
					// whatever the daemon still says it is. Ask it the only
					// way the protocol allows: the original hold, replayed,
					// is idempotent and answers the marker it holds.
					answer := in.Draft.Daemon.capture("hold", "/capture/v1/hold", in.Execution,
						holdBody(in.Execution, in.Admission.Output, in.PodUID))
					current, err := decodeControl[hangaroutput.CaptureHoldAcknowledgement](answer)
					if err != nil {
						return fmt.Errorf("asking which hold is in force: %w", err)
					}
					in.Repeated = current
				}
				if in.Repeated.Marker != in.Acknowledgement.Marker {
					return fmt.Errorf("the hold in force is a different marker from the one "+
						"the first hold returned:\n first: %+v\n  now: %+v",
						in.Acknowledgement.Marker, in.Repeated.Marker)
				}

				return nil
			}),

		// An absence with a positive control on the line above it: a source
		// that is still there is an outcome, not a call count.
		CheckThat[HeldSource]("the held step directory is still on the node",
			func(in HeldSource) error {
				info, err := os.Lstat(in.stepRoot())
				if err != nil {
					return fmt.Errorf("the step directory is gone from the node: %v", err)
				}
				if !info.IsDir() {
					return fmt.Errorf("the step directory is a %s, not a directory", info.Mode().Type())
				}

				return nil
			}),

		// A release releases the HOLD, and never the bytes.
		//
		// So this asks the two questions separately, and both of them are
		// outcomes: the gate the hold held open is closed -- the daemon says
		// the execution is destructively cleanup-eligible, which it refuses
		// while any hold stands -- and the step directory is still
		// there, because it is the step's own output, aliased read-only at the
		// ordinary path, and a failed producer's output is kept for the
		// build's lifetime. Deletion is reclamation by policy, not a side
		// effect of a release.
		CheckThat[HeldSource]("the marker is released and the step directory remains",
			func(in HeldSource) error {
				answer, err := decodeControl[executioncontrol.DestructiveCleanupEligibleResult](
					in.Draft.Daemon.base("cleanup-eligible", "/execution/v1/cleanup-eligible",
						in.Execution, identifiedBy(in.Execution)))
				if err != nil {
					return fmt.Errorf("asking whether cleanup is eligible: %w", err)
				}
				if !answer.Eligible {
					return fmt.Errorf("the hold's gate is still open after a release: %s "+
						"(open gates %v)", answer.WithheldReason, answer.OpenExtensionGates)
				}
				if _, err := os.Lstat(in.stepRoot()); err != nil {
					return fmt.Errorf("the release deleted the step's output at %s: %v; a "+
						"release closes the hold and leaves the bytes to the artifact daemon's "+
						"ordinary lifecycle", in.stepRoot(), err)
				}

				return nil
			}),

		// Checks over the witness.
		CheckThat[FinishWitnessed]("the witness is what the step reports",
			func(in FinishWitnessed) error {
				if in.Err != nil {
					return in.Err
				}
				if in.Witness.Outcome == nil {
					return fmt.Errorf("the daemon witnessed no outcome")
				}
				if in.Reported.Outcome == nil {
					return fmt.Errorf("the step reported no outcome")
				}
				if *in.Witness.Outcome != *in.Reported.Outcome {
					return fmt.Errorf("the daemon witnessed %+v and the step reports %+v",
						*in.Witness.Outcome, *in.Reported.Outcome)
				}
				if in.Witness.LedgerSequence != in.Reported.LedgerSequence {
					return fmt.Errorf("the step reports an outcome under a different ledger statement " +
						"than the one the daemon witnessed")
				}

				return nil
			}),

		CheckInt[FinishWitnessed]("the witnessed exit status is {int}",
			"the exit status the daemon durably recorded",
			func(in FinishWitnessed) (int, error) {
				if in.Err != nil {
					return 0, in.Err
				}
				if in.Witness.Outcome == nil {
					return 0, fmt.Errorf("the daemon witnessed no outcome")
				}

				return in.Witness.Outcome.ExitCode, nil
			}),

		CheckThat[FinishWitnessed]("the step directory is still there after the stop",
			func(in FinishWitnessed) error {
				if _, err := os.Lstat(in.Source.stepRoot()); err != nil {
					return fmt.Errorf("a source-preserving stop removed the step directory: %v", err)
				}

				return nil
			}),

		CheckThat[FinishWitnessed]("destructive cleanup is permitted",
			func(in FinishWitnessed) error {
				answer, err := decodeControl[executioncontrol.DestructiveCleanupEligibleResult](in.Asked)
				if err != nil {
					return err
				}
				if !answer.Eligible {
					return fmt.Errorf("cleanup was refused: %s (open gates %v)",
						answer.WithheldReason, answer.OpenExtensionGates)
				}

				return nil
			}),

		CheckThat[FinishWitnessed]("destructive cleanup is refused",
			func(in FinishWitnessed) error {
				answer, err := decodeControl[executioncontrol.DestructiveCleanupEligibleResult](in.Asked)
				if err != nil {
					return err
				}
				if answer.Eligible {
					return fmt.Errorf("cleanup was permitted with classification %s and gates %v",
						answer.Classification, answer.OpenExtensionGates)
				}
				if answer.WithheldReason == "" {
					return fmt.Errorf("cleanup was withheld with no reason")
				}

				return nil
			}),
	}
}

// holdBody is the capture control init's request: the execution, the declared
// output, and the Pod UID the container read off the Downward API.
//
// The Pod UID is the first fact in the protocol sent from INSIDE the Pod, so
// this is the first message that can name it, and the daemon binds the held
// marker to it once.
func holdBody(execution executioncontrol.Identity, output hangaroutput.OutputName,
	pod executioncontrol.PodUID) hangaroutput.CaptureHoldRequest {
	return hangaroutput.CaptureHoldRequest{
		ProtocolVersion: hangaroutput.ProtocolVersion,
		Execution:       execution,
		Output:          output,
		PodUID:          pod,
	}
}

// identifiedExecution is the body a base route takes.
//
// The execution is FLAT rather than nested, because every frozen base type
// embeds Identity: a ClassifyRequest is execution_id and fence at the top
// level. The extension's bodies nest it under "execution"; the daemon's
// middleware reads either, and this is the base spelling.
type identifiedExecution struct {
	ProtocolVersion string                       `json:"protocol_version"`
	ExecutionID     executioncontrol.ExecutionID `json:"execution_id"`
	Fence           executioncontrol.Fence       `json:"fence"`
}

func identifiedBy(id executioncontrol.Identity) identifiedExecution {
	return identifiedExecution{
		ProtocolVersion: executioncontrol.ProtocolVersion,
		ExecutionID:     id.ExecutionID,
		Fence:           id.Fence,
	}
}

// witness plays the supervisor's two writes and then reads the answer back.
//
// The start is written before the child would launch and the outcome before the
// result would be exposed -- that is the ordering the base protocol exists to
// impose, and doing it in the wrong order here would make the fixture pass
// against a daemon that does not.
//
// Reported comes from OBSERVE, a separate route and a separate read of the
// record, so "the witness is what the step reports" compares two answers rather
// than one value with itself.
func (source HeldSource) witness(kind executioncontrol.AcknowledgementKind,
	outcome executioncontrol.ExitOutcome) (FinishWitnessed, error) {
	if err := source.recordStart(); err != nil {
		return FinishWitnessed{}, err
	}
	return source.recordWitness(kind, outcome)
}

func (source HeldSource) recordStart() error {
	started := source.Draft.Daemon.base("start", "/execution/v1/start", source.Execution,
		map[string]any{
			"execution":        source.Execution,
			"pod_uid":          source.PodUID,
			"process_identity": "brine-supervisor-" + freshUUID(),
		})
	if _, err := decodeControl[executioncontrol.Acknowledgement](started); err != nil {
		return fmt.Errorf("recording the start: %w", err)
	}
	return nil
}

func (source HeldSource) recordWitness(kind executioncontrol.AcknowledgementKind,
	outcome executioncontrol.ExitOutcome) (FinishWitnessed, error) {
	witnessed := FinishWitnessed{Source: source, Outcome: outcome}

	recorded := source.Draft.Daemon.base("outcome", "/execution/v1/outcome", source.Execution,
		map[string]any{
			"execution": source.Execution,
			"kind":      kind,
			"outcome":   outcome,
		})
	witness, err := decodeControl[executioncontrol.Acknowledgement](recorded)
	if err != nil {
		return witnessed, fmt.Errorf("recording the outcome: %w", err)
	}
	if err := witness.Validate(); err != nil {
		return witnessed, fmt.Errorf("the witness is not a well-formed ledger statement: %w", err)
	}
	witnessed.Witness = witness

	observed := source.Draft.Daemon.base("observe", "/execution/v1/observe", source.Execution,
		identifiedBy(source.Execution))
	result, err := decodeControl[executioncontrol.ObserveFinishOrStopResult](observed)
	if err != nil {
		return witnessed, fmt.Errorf("observing the outcome: %w", err)
	}
	if result.Acknowledgement == nil {
		return witnessed, fmt.Errorf("the daemon reports classification %s and no acknowledgement "+
			"after an outcome was recorded", result.Classification)
	}
	witnessed.Reported = *result.Acknowledgement

	return witnessed, nil
}

// placeholderDigest is a well-formed digest for a request that is refused
// before any digest is compared: the refusal scenarios are about a facet or a
// caller-chosen location, never about which bytes.
var placeholderDigest = hangar.Digest("sha256:" + strings.Repeat("0", 64))

// sealRequest, publishRequest and release re-derive their identities from the
// admission. No scenario supplies one, which is why none takes an identity.
func (source HeldSource) sealRequest() hangaroutput.CaptureSealRequest {
	return hangaroutput.CaptureSealRequest{
		ProtocolVersion: hangaroutput.ProtocolVersion,
		Execution:       source.Execution,
		Output:          source.Admission.Output,
		PodUID:          source.PodUID,
	}
}

func (source HeldSource) publishRequest(digest hangar.Digest) hangaroutput.CapturePublishRequest {
	return hangaroutput.CapturePublishRequest{
		ProtocolVersion: hangaroutput.ProtocolVersion,
		Execution:       source.Execution,
		Output:          source.Admission.Output,
		Digest:          digest,
	}
}

func (source HeldSource) release() controlAnswer {
	return source.Draft.Daemon.capture("release", "/capture/v1/release", source.Execution,
		hangaroutput.CaptureReleaseRequest{
			ProtocolVersion: hangaroutput.ProtocolVersion,
			Execution:       source.Execution,
			Output:          source.Admission.Output,
		})
}
