package steps

// What the output daemon ANSWERS: holds, witnesses, refusals, containment.
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

	"github.com/brine-dev/brine-go/pkg/brine"

	"github.com/concourse/concourse/hangar/executioncontrol"
	hangaroutput "github.com/concourse/concourse/hangar/output"
)

const handoffPhase = "Phase 3 Green"

// heldFrom carries a draft's identities into the held state.
func heldFrom(draft CaptureDraft, ack hangaroutput.CaptureAcknowledgement, answer controlAnswer) HeldSource {
	return HeldSource{
		Draft: HeldDraft{
			Handle: string(draft.Admission.HandoffID),
			Output: draft.Output,
			Daemon: draft.Daemon,
		},
		DaemonURL:       draft.Daemon.Output.URL,
		StorageRoot:     draft.Daemon.Output.Root,
		Acknowledgement: ack,
		Incarnation:     ack.Incarnation,
		Fence:           hangaroutput.CaptureFence(draft.Admission.Execution.Fence),
		Execution:       draft.Admission.Execution,
		ReservationID:   freshUUID(),
		Admission:       draft.Admission,
		Reserved:        draft.Reserved,
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

func (source HeldSource) writerAdmission(ticket string) hangaroutput.WriterAdmission {
	return hangaroutput.WriterAdmission{
		ProtocolVersion: hangaroutput.ProtocolVersion,
		Execution:       source.Execution,
		ActivationEpoch: source.Admission.ActivationEpoch,
		HandoffID:       source.Admission.HandoffID,
		Incarnation:     source.Incarnation,
		WriterTicketID:  hangaroutput.WriterTicketID(ticket),
		WriterFence:     1,
		PodUID:          source.PodUID,
	}
}

// incarnationRoot is the directory the daemon issued, derived the way the
// daemon derives it. The fixture knows the shape because it is the one that
// swaps a symlink under it; no scenario names it, and no request carries it.
func (source HeldSource) incarnationRoot() string {
	return filepath.Join(source.StorageRoot, "steps",
		fmt.Sprintf("%s.%d", source.Incarnation.ExecutionID, source.Incarnation.HandleGeneration),
		string(source.Incarnation.Output))
}

// HangarHandoffDefinitions is the daemon-handoff family.
func HangarHandoffDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMap[CaptureDraft, HeldSource](
			"the daemon holds the source",
			func(in CaptureDraft, _ brine.Params, _ *brine.Recorder) (HeldSource, error) {
				// The reservation comes first, because in production it comes
				// before the Pod exists at all: the ATC asks the daemon for the
				// location, mounts it as the producer's output volume, and puts
				// it in the control init's environment. The hold PRESENTS it.
				reserving := in.Daemon.capture("reserve-incarnation",
					"/capture/v1/reserve-incarnation", in.Admission.Execution, in.Admission)
				reserved, err := decodeControl[hangaroutput.ReservedIncarnation](reserving)
				if err != nil {
					return HeldSource{}, fmt.Errorf("reserving the incarnation: %w", err)
				}
				in.Reserved = reserved

				answer := in.Daemon.capture("hold", "/capture/v1/hold",
					in.Admission.Execution, holdBody(in.Admission, reserved.Incarnation, in.PodUID))
				ack, err := decodeControl[hangaroutput.CaptureAcknowledgement](answer)
				if err != nil {
					return HeldSource{}, fmt.Errorf("establishing the hold: %w", err)
				}
				if err := hangaroutput.VerifyCaptureAcknowledgement(ack,
					in.Daemon.ControlPublic); err != nil {
					return HeldSource{}, fmt.Errorf("the hold statement does not verify under the "+
						"activation-pinned public key: %w", err)
				}

				held := heldFrom(in, ack, answer)

				// The bytes a producer wrote, written HERE rather than at the
				// witness, and the move is the point. The source-preserving
				// stop scenario asserts that the incarnation survives the stop;
				// writing into it while witnessing made a stop that removed the
				// directory fail on the `When` instead, so the named `Then` was
				// never reached. The content is deterministic and shared, which
				// is what lets two captures of "the same canonical bytes"
				// really be the same bytes.
				if err := os.WriteFile(filepath.Join(held.incarnationRoot(), "artifact.txt"),
					[]byte("the bytes a producer wrote\n"), 0o600); err != nil {
					return HeldSource{}, fmt.Errorf("writing the produced source: %w", err)
				}

				return held, nil
			},
		),

		brine.DefineMap[HeldSource, HeldSource](
			"the same hold is repeated with the same identity",
			func(in HeldSource, _ brine.Params, _ *brine.Recorder) (HeldSource, error) {
				answer := in.Draft.Daemon.capture("hold", "/capture/v1/hold",
					in.Execution, holdBody(in.Admission, in.Incarnation, in.PodUID))
				repeated, err := decodeControl[hangaroutput.CaptureAcknowledgement](answer)
				if err == nil {
					in.Repeated = repeated
				}

				return in.answered(answer), nil
			},
		),

		// The conflict twin. "Repeating the handoff returns the same state"
		// passes for a daemon that ignores the identity entirely, so the twin
		// has to show that reuse for DIFFERENT facts is a typed conflict.
		brine.DefineMap[HeldSource, HeldSource](
			"the same hold is repeated with a different fence",
			func(in HeldSource, _ brine.Params, _ *brine.Recorder) (HeldSource, error) {
				different := in.Admission
				different.Execution.Fence++

				return in.answered(in.Draft.Daemon.capture("hold", "/capture/v1/hold",
					different.Execution, holdBody(different, in.Incarnation, in.PodUID))), nil
			},
		),

		brine.DefineMap[HeldSource, HeldSource](
			"a stale fence is presented",
			func(in HeldSource, _ brine.Params, _ *brine.Recorder) (HeldSource, error) {
				stale := in.writerAdmission(freshUUID())
				stale.Execution.Fence = in.Execution.Fence - 1

				return in.answered(in.Draft.Daemon.capture("issue-writer-ticket",
					"/capture/v1/writer-ticket", stale.Execution, stale)), nil
			},
		),

		brine.DefineMap[HeldSource, HeldSource](
			"the owner's lease is taken over",
			func(in HeldSource, _ brine.Params, _ *brine.Recorder) (HeldSource, error) {
				taken := in.Execution
				taken.Fence++

				answer := in.Draft.Daemon.base("admit", "/execution/v1/admit", taken,
					executioncontrol.Envelope{
						ProtocolVersion: executioncontrol.ProtocolVersion,
						Identity:        taken,
						ActivationEpoch: in.Admission.ActivationEpoch,
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
			"the hold request names a path instead of an incarnation",
			func(in HeldSource, _ brine.Params, _ *brine.Recorder) (HeldSource, error) {
				return in.answered(in.Draft.Daemon.rawCapture("hold", "/capture/v1/hold",
					in.Execution, map[string]any{
						"protocol_version":    hangaroutput.ProtocolVersion,
						"execution":           in.Execution,
						"activation_epoch":    in.Admission.ActivationEpoch,
						"handoff_id":          in.Admission.HandoffID,
						"source_lease_id":     in.Admission.SourceLeaseID,
						"output":              in.Admission.Output,
						"capture_deadline_at": in.Admission.CaptureDeadline,
						"path":                in.incarnationRoot(),
					})), nil
			},
		),

		brine.DefineMap[HeldSource, HeldSource](
			"the source path is replaced by a symlink to {string}",
			func(in HeldSource, p brine.Params, _ *brine.Recorder) (HeldSource, error) {
				target, err := paramAt("the source path is replaced by a symlink to {string}", p, 0)
				if err != nil {
					return in, err
				}
				root := in.incarnationRoot()
				if err := os.RemoveAll(root); err != nil {
					return in, err
				}
				if err := os.Symlink(target, root); err != nil {
					return in, err
				}

				// Any control operation that has to resolve the incarnation
				// will do; a writer ticket is the cheapest, and it is one of
				// the operations Req 12 says must go through the ledger.
				admission := in.writerAdmission(freshUUID())

				return in.answered(in.Draft.Daemon.capture("issue-writer-ticket",
					"/capture/v1/writer-ticket", in.Execution, admission)), nil
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
						in.publicationRequest())), nil
				case "hold":
					return in.answered(in.Draft.Daemon.control(executioncontrol.BaseFacet,
						"hold", "/capture/v1/hold", in.Execution,
						holdBody(in.Admission, in.Incarnation, in.PodUID))), nil
				case "seal":
					return in.answered(in.Draft.Daemon.control(executioncontrol.BaseFacet,
						"begin-seal", "/capture/v1/seal", in.Execution, in.sealRequest())), nil
				}

				return in, fmt.Errorf("no base control operation named %q; the scenario vocabulary "+
					"is classify, hold, seal and publish", operation)
			},
		),

		brine.DefineMap[HeldSource, HeldSource](
			"a writer ticket is issued after the seal",
			func(in HeldSource, _ brine.Params, _ *brine.Recorder) (HeldSource, error) {
				sealAnswer := in.Draft.Daemon.capture("begin-seal", "/capture/v1/seal",
					in.Execution, in.sealRequest())
				if _, err := decodeControl[hangaroutput.SealStarted](sealAnswer); err != nil {
					return in, fmt.Errorf("beginning the seal: %w", err)
				}

				admission := in.writerAdmission(freshUUID())

				return in.answered(in.Draft.Daemon.capture("issue-writer-ticket",
					"/capture/v1/writer-ticket", in.Execution, admission)), nil
			},
		),

		brine.DefineMap[HeldSource, HeldSource](
			"a writer ticket is issued before the seal",
			func(in HeldSource, _ brine.Params, _ *brine.Recorder) (HeldSource, error) {
				admission := in.writerAdmission(freshUUID())
				answer := in.Draft.Daemon.capture("issue-writer-ticket",
					"/capture/v1/writer-ticket", in.Execution, admission)
				if _, err := decodeControl[hangaroutput.CaptureAcknowledgement](answer); err == nil {
					// The ticket has to be retired, or the seal in the line
					// after this one waits for it forever.
					in.Draft.Daemon.capture("close-writer-ticket",
						"/capture/v1/writer-ticket/close", in.Execution, admission)
				}

				return in.answered(answer), nil
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
				release := hangaroutput.ReleaseIntent{
					ProtocolVersion: hangaroutput.ProtocolVersion,
					Disposition:     hangaroutput.DispositionNoCapture,
					Execution:       in.Source.Execution,
					ActivationEpoch: in.Source.Admission.ActivationEpoch,
					HandoffID:       in.Source.Admission.HandoffID,
					SourceLeaseID:   in.Source.Admission.SourceLeaseID,
					ReleaseIntentID: hangaroutput.ReleaseIntentID(freshUUID()),
					Incarnation:     in.Source.Incarnation,
				}
				released := in.Source.Draft.Daemon.capture("release-hold", "/capture/v1/release",
					in.Source.Execution, release)
				if _, err := decodeControl[hangaroutput.ReleaseAcknowledgement](released); err != nil {
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

		// A no_capture release, and it takes a WITNESSED step rather than a held
		// source.
		//
		// That is Req 5 in the type system rather than in a comment: the
		// no_capture handoff follows an authoritative non-success witness, and
		// there is no production path on which a hold is released for a
		// producer nobody has heard from. Written over HeldSource the phrase
		// constructed a state production cannot reach -- convention 3 -- and the
		// daemon said so at runtime, refusing with "is never_started; only a
		// durable finish or stop may authorize destroying anything". It was a
		// pending scenario, so nothing ran it and nothing noticed.
		//
		// It returns the HeldSource so the two node-side checks either side of
		// it -- the source is still held, the source has been released -- read
		// the same incarnation on the same node.
		brine.DefineMap[FinishWitnessed, HeldSource](
			"the hold is released",
			func(in FinishWitnessed, _ brine.Params, _ *brine.Recorder) (HeldSource, error) {
				source := in.Source

				return source.answered(source.Draft.Daemon.capture("release-hold",
					"/capture/v1/release", source.Execution, hangaroutput.ReleaseIntent{
						ProtocolVersion: hangaroutput.ProtocolVersion,
						Disposition:     hangaroutput.DispositionNoCapture,
						Execution:       source.Execution,
						ActivationEpoch: source.Admission.ActivationEpoch,
						HandoffID:       source.Admission.HandoffID,
						SourceLeaseID:   source.Admission.SourceLeaseID,
						ReleaseIntentID: hangaroutput.ReleaseIntentID(freshUUID()),
						Incarnation:     source.Incarnation,
					})), nil
			},
		),

		brine.DefineMap[HeldSource, HeldSource](
			"the publish request also carries a caller-chosen {string}",
			func(in HeldSource, p brine.Params, _ *brine.Recorder) (HeldSource, error) {
				field, err := paramAt("the publish request also carries a caller-chosen {string}", p, 0)
				if err != nil {
					return in, err
				}
				publication := in.publicationRequest()
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

		// The supervisor's two writes, played by the fixture. In Phase 4
		// execProcess makes these calls; what they say does not change.
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
			"the step is stopped without destroying its source",
			func(in HeldSource, _ brine.Params, _ *brine.Recorder) (FinishWitnessed, error) {
				stopped := in.Draft.Daemon.base("stop", "/execution/v1/stop", in.Execution,
					identifiedBy(in.Execution))
				if _, err := decodeControl[executioncontrol.RequestSourcePreservingStopResult](stopped); err != nil {
					return FinishWitnessed{}, fmt.Errorf("requesting the stop: %w", err)
				}

				return in.witness(executioncontrol.AcknowledgementStop,
					executioncontrol.ExitOutcome{ExitCode: 143, Signalled: true, Signal: "TERM"})
			},
		),

		// Cancellation is a REQUEST and not a row. What the state carries is
		// the question; which branch the arbiter then wins is the plane's
		// answer, and no phrase here decides it.
		//
		// It takes a WITNESSED step rather than a held source, and that is the
		// whole point of the phrase: Req 11 says cancellation before Stage 2
		// selects only pre_reservation_cancel, never no_capture, and the only
		// way a scenario can be false against that "never" is for the producer
		// to have a non-success outcome the arbiter could confuse it with. A
		// cancellation with no witness beside it is answered
		// `pre_reservation_cancel` by an arbiter that asks the outcome FIRST
		// too, so it pins "cancellation is honoured" and not the branch order.
		brine.DefineMap[FinishWitnessed, FinishWitnessed](
			"the step is cancelled before Stage 2",
			func(in FinishWitnessed, _ brine.Params, _ *brine.Recorder) (FinishWitnessed, error) {
				in.Cancelled = true

				return in, nil
			},
		),

		// The absence: a handoff cancelled with no hold ever acknowledged.
		//
		// It still RESERVED, because a reservation exists before the producing
		// Pod does -- so there is a directory on a node to release, and the
		// scenario above is its control. The reservation is taken here rather
		// than in a Given because this is the one chain where the control init
		// never runs.
		brine.DefineMap[CaptureDraft, FinishWitnessed](
			"the handoff is cancelled with no acknowledged hold",
			func(in CaptureDraft, _ brine.Params, _ *brine.Recorder) (FinishWitnessed, error) {
				reserving := in.Daemon.capture("reserve-incarnation",
					"/capture/v1/reserve-incarnation", in.Admission.Execution, in.Admission)
				reserved, err := decodeControl[hangaroutput.ReservedIncarnation](reserving)
				if err != nil {
					return FinishWitnessed{}, fmt.Errorf("reserving the incarnation: %w", err)
				}

				source := HeldSource{
					Draft: HeldDraft{
						Handle: string(in.Admission.HandoffID),
						Output: in.Output,
						Daemon: in.Daemon,
					},
					DaemonURL:   in.Daemon.Output.URL,
					StorageRoot: in.Daemon.Output.Root,
					Incarnation: reserved.Incarnation,
					Reserved:    reserved,
					Fence:       hangaroutput.CaptureFence(in.Admission.Execution.Fence),
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
		CheckInt[HeldSource]("the Hangar daemon answers {int}, holding the source",
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

		CheckThat[HeldSource]("the hold acknowledgement is the one the first hold returned",
			func(in HeldSource) error {
				if in.Repeated.Signature == "" {
					// The repeat was refused, so the statement in force is
					// whatever the daemon still says it is. Ask it.
					answer := in.Draft.Daemon.capture("inspect-hold", "/capture/v1/hold/inspect",
						in.Execution, holdInspection{
							Execution: in.Execution,
							HandoffID: in.Admission.HandoffID,
						})
					current, err := decodeControl[hangaroutput.CaptureAcknowledgement](answer)
					if err != nil {
						return fmt.Errorf("asking which hold is in force: %w", err)
					}
					in.Repeated = current
				}
				if in.Repeated.Signature != in.Acknowledgement.Signature {
					return fmt.Errorf("the hold in force is a different statement from the one "+
						"the first hold returned:\n first: %s\n  now: %s",
						abbrev(in.Acknowledgement.Signature), abbrev(in.Repeated.Signature))
				}

				return nil
			}),

		// An absence with a positive control on the line above it: a source
		// that is still there is an outcome, not a call count.
		CheckThat[HeldSource]("the source is still held on the node",
			func(in HeldSource) error {
				info, err := os.Lstat(in.incarnationRoot())
				if err != nil {
					return fmt.Errorf("the source incarnation is gone from the node: %v", err)
				}
				if !info.IsDir() {
					return fmt.Errorf("the source incarnation is a %s, not a directory", info.Mode().Type())
				}

				return nil
			}),

		// A release releases the HOLD, and never the bytes.
		//
		// So this asks the two questions separately, and both of them are
		// outcomes: the gate the hold held open is closed -- the daemon says
		// the execution is destructively cleanup-eligible, which it refuses
		// while any hold stands -- and the incarnation directory is still
		// there, because it is the step's own output, aliased read-only at the
		// ordinary path, and Req 2 keeps a failed producer's output for the
		// build's lifetime. Deletion is reclamation by policy, not a side
		// effect of a release.
		CheckThat[HeldSource]("the source has been released",
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
				if _, err := os.Lstat(in.incarnationRoot()); err != nil {
					return fmt.Errorf("the release deleted the step's output at %s: %v; a "+
						"release closes the hold and leaves the bytes to the artifact daemon's "+
						"ordinary lifecycle", in.incarnationRoot(), err)
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
				if in.Witness.Signature != in.Reported.Signature {
					return fmt.Errorf("the step reports an outcome under a different statement " +
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

		CheckThat[FinishWitnessed]("the source is still there after the stop",
			func(in FinishWitnessed) error {
				if _, err := os.Lstat(in.Source.incarnationRoot()); err != nil {
					return fmt.Errorf("a source-preserving stop removed the incarnation: %v", err)
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

// holdBody is the capture control init's request: the admission the
// reservation was made for, the incarnation the daemon answered with, and the
// Pod UID the container read off the Downward API.
//
// The incarnation is not a path and not a choice. It is four server-issued
// identity fields, and presenting them is how an init container proves it is
// running in the Pod the reservation was made for.
//
// The Pod UID is here and not on the admission because the admission and the
// reservation both happen before the Pod exists. This is the first message in
// the protocol sent from INSIDE the Pod, so it is the first one that can name
// it, and the daemon binds it once.
func holdBody(admission hangaroutput.CaptureAdmission,
	incarnation hangaroutput.SourceIncarnation,
	pod executioncontrol.PodUID) map[string]any {
	return map[string]any{
		"protocol_version":    admission.ProtocolVersion,
		"execution":           admission.Execution,
		"activation_epoch":    admission.ActivationEpoch,
		"handoff_id":          admission.HandoffID,
		"source_lease_id":     admission.SourceLeaseID,
		"output":              admission.Output,
		"capture_deadline_at": admission.CaptureDeadline,
		"incarnation":         incarnation,
		"pod_uid":             pod,
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

// holdInspection names ids and nothing else.
type holdInspection struct {
	Execution executioncontrol.Identity `json:"execution"`
	HandoffID hangaroutput.HandoffID    `json:"handoff_id"`
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
	witnessed := FinishWitnessed{Source: source, Outcome: outcome}

	// The producer's bytes are already there: they are written when the hold is
	// established, not here. Writing them at the witness made a stop that
	// removed the incarnation fail on the `When` line, so the `Then` the box
	// names -- "the source is still there after the stop" -- was never reached.

	started := source.Draft.Daemon.base("start", "/execution/v1/start", source.Execution,
		map[string]any{
			"execution":        source.Execution,
			"pod_uid":          source.PodUID,
			"process_identity": "brine-supervisor-" + freshUUID(),
		})
	if _, err := decodeControl[executioncontrol.Acknowledgement](started); err != nil {
		return witnessed, fmt.Errorf("recording the start: %w", err)
	}

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
	if err := executioncontrol.VerifyAcknowledgement(witness,
		source.Draft.Daemon.ControlPublic); err != nil {
		return witnessed, fmt.Errorf("the witness does not verify under the activation-pinned "+
			"public key: %w", err)
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

// sealRequest and publicationRequest re-derive their identities from the
// admission. No scenario supplies one, which is why neither takes a parameter.
func (source HeldSource) sealRequest() hangaroutput.SealRequest {
	return hangaroutput.SealRequest{
		ProtocolVersion: hangaroutput.ProtocolVersion,
		Execution:       source.Execution,
		ActivationEpoch: source.Admission.ActivationEpoch,
		HandoffID:       source.Admission.HandoffID,
		Incarnation:     source.Incarnation,
		CaptureFence:    hangaroutput.CaptureFence(source.Execution.Fence),
		DeadlineAt:      source.Admission.CaptureDeadline,
	}
}

func (source HeldSource) publicationRequest() hangaroutput.PublicationRequest {
	return hangaroutput.PublicationRequest{
		ProtocolVersion: hangaroutput.ProtocolVersion,
		Execution:       source.Execution,
		ActivationEpoch: source.Admission.ActivationEpoch,
		HandoffID:       source.Admission.HandoffID,
		ReservationID:   hangaroutput.ReservationID(source.ReservationID),
		CaptureFence:    hangaroutput.CaptureFence(source.Execution.Fence),
	}
}
