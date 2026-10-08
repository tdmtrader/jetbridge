package steps

// The named domain states of the Hangar output family.
//
// The rules these follow are domain.go's, applied to a plane that does not
// exist yet: a state carries what its SUCCESSORS need and nothing else, an
// error is a VALUE so a refusal is assertable rather than fatal, and there is
// one nominal type per reachable set of assertions — because the chain walk
// matches on nominal type, and a scenario that never published should not be
// able to reach a publication assertion.
//
// WHAT A PHRASE MAY NOT BUILD (convention 3). None of these states can be
// constructed with a caller-chosen bucket, scope or object key, with a capture
// row the test wrote, or with capture selected after the run:
//
//   - CaptureDraft is reached ONLY by a refinement over a draft, so "capture
//     requested after the task started" has no sentence. Capture is selected
//     before the run, spelled in the type system rather than asserted.
//   - HangarDaemon names its own bucket. There is no phrase that sets it.
//   - CaptureOutcome's Capture is the row production's coordinator left; the
//     only phrase that fills it is `the capture settles`.
//   - PublishedTree's BucketKeys is a read of the bucket, not a memory of what
//     was put there.
//
// The one place a caller-chosen key appears at all is as raw REQUEST INPUT in
// the refusal scenarios — `the publish request also carries a caller-chosen
// "bucket"` — which is the state a hostile client can really produce and the
// thing the daemon must refuse.

import (
	"crypto/rand"
	"fmt"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"

	corev1 "k8s.io/api/core/v1"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	hangaroutput "github.com/concourse/concourse/hangar/output"
)

// pendingPhase is the typed failure every phrase whose production code has not
// been written yet returns.
//
// It is deliberately NOT a skip and NOT a nil return. A step that quietly
// passed would make its scenario green against nothing at all, which is the
// exact defect MIGRATION-EVIDENCE.md records as "a test that cannot fail". The
// error names the phase that closes it, so a red step reads as work outstanding
// rather than as a broken fixture.
type pendingPhase struct {
	phrase string
	phase  string
	needs  string
}

func (e pendingPhase) Error() string {
	return fmt.Sprintf(
		"%q is not yet implemented in production: %s adds %s. "+
			"This step is defined so `brine check` can walk the chain and so the "+
			"vocabulary guard can see it; it fails rather than passes.",
		e.phrase, e.phase, e.needs)
}

// pending returns the typed failure above. Every stubbed body in the Hangar
// family returns exactly this.
func pending(phrase, phase, needs string) error {
	return pendingPhase{phrase: phrase, phase: phase, needs: needs}
}

// captureAdmission is what the control plane knows about a capture before its
// producing Pod exists: the exact execution being extended, the activation
// epoch it is admitted under, the one declared output, and the deadline. It
// is the fixture's spelling of what a pending capture row carries; nothing
// about success, scope or digest is knowable here, and the type says so.
type captureAdmission struct {
	Execution       executioncontrol.Identity
	ActivationEpoch executioncontrol.ActivationEpoch
	Output          hangaroutput.OutputName
	CaptureDeadline time.Time
}

// newCaptureAdmission mints a fresh execution for one output. No feature file
// chooses the identity: a scenario that could name an execution could make
// two scenarios collide on one node's markers.
func newCaptureAdmission(output hangaroutput.OutputName) captureAdmission {
	return captureAdmission{
		Execution: executioncontrol.Identity{
			ExecutionID: executioncontrol.ExecutionID(freshUUID()),
			Fence:       1,
		},
		ActivationEpoch: executioncontrol.ActivationEpoch(hangarEpoch),
		Output:          output,
		CaptureDeadline: time.Now().UTC().Add(24 * time.Hour),
	}
}

// Key is the capture row's key: the execution and the declared output.
func (admission captureAdmission) Key() hangaroutput.CaptureKey {
	return hangaroutput.CaptureKey{ExecutionID: admission.Execution.ExecutionID, Output: admission.Output}
}

// CaptureDraft is a container spec under description whose ONE declared output
// has been selected for capture, plus the identities the server issues before
// anything runs.
//
// It embeds ContainerDraft rather than copying its fields, so every existing
// refinement's meaning is unchanged and the capture selection is visibly an
// addition to a draft rather than a second draft model.
//
// It is a separate nominal type from ContainerDraft because the assertions it
// reaches — the hold init before every writer, the source-control grant in
// exactly one container, the ready-label pair — are meaningless for a pod that
// captures nothing, and the control scenario for each of them is an ORDINARY
// pod built from an ordinary ContainerDraft.
type CaptureDraft struct {
	Draft ContainerDraft

	// Daemon is present when the chain entered through the daemon fixture, and
	// zero when it entered through the plain worker. The pod-shape scenarios
	// need no daemon; the hold and settlement scenarios do, and reach it
	// through here rather than through a second live state.
	Daemon HangarDaemon

	// Output is the one output selected for capture. A second selection is a
	// refusal, not a second element, which is why this is not a slice.
	Output hangaroutput.OutputName

	// SecondOutput records a second selection ATTEMPT, so the refusal scenario
	// has something to build. Production never reaches a state where both are
	// selected; it reaches a state where a second was asked for.
	SecondOutput hangaroutput.OutputName

	// Admission is what the capture row carries before the producing Pod may
	// start.
	Admission captureAdmission

	// ReadyFacets and ControlEpochMatches are the scheduling refinements. A
	// label is not authority — the matching control epoch is — so they are two
	// fields and not one.
	ReadyFacets         []string
	ControlEpochMatches bool

	// PausePodTerminal is the regression the ordinary path must keep: a
	// terminal pause pod is recreated for an ordinary source and refused for a
	// capture-held one.
	PausePodTerminal bool

	// PodUID is the Pod the execution's capture is held for. The draft mints
	// it because the hold is the first message sent from inside the Pod, and
	// that is where the daemon binds it.
	PodUID executioncontrol.PodUID

	// StrictInput is the destination of a strict-input Hangar tree this step
	// also takes, empty when it takes none. It is the strict-input regression twin's
	// whole subject: a capture-selected step that ALSO consumes an exact
	// immutable input must still get the ordinary strict-input materialization,
	// unchanged, beside its capture control init.
	StrictInput string
}

// freshUUID mints an identity no feature file chose.
//
// Every identity in this family is server- or fixture-generated for the same
// reason: a scenario that could name an execution could make two scenarios
// collide on one node's markers.
func freshUUID() string {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		panic("brine: no randomness for a Hangar identity: " + err.Error())
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80

	return fmt.Sprintf("%x-%x-%x-%x-%x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16])
}

// CapturePodCreated is the capture pod as the cluster hands it back — or the
// refusal that stopped it being built.
//
// Err is a value for the same reason VolumeRead.Err is: "a second selection is
// refused" and "a worker whose output facet is not enabled builds no capture
// pod" are both outcomes a scenario asserts on, and a fixture that died on them
// could not.
type CapturePodCreated struct {
	Draft CaptureDraft
	Pod   *corev1.Pod
	Err   error
}

// HeldSource is a capture whose step directory the daemon has acknowledged
// holding: the node's held marker is durable.
//
// It carries the daemon URL and the storage root because the containment and
// stop scenarios assert on what is STILL ON THE NODE afterwards. It carries no
// request log: "the daemon was not called" is never assertable here, and the
// scenarios that mean it say it as an outcome instead.
type HeldSource struct {
	Draft HeldDraft

	// Pod is the producing pod, present when the chain built one.
	Pod *corev1.Pod

	// DaemonURL and StorageRoot are the node-side facts a stop or a cleanup
	// changes or preserves.
	DaemonURL   string
	StorageRoot string

	// Acknowledgement is the hold the daemon returned: the held marker. A
	// repeat with the same Pod must return the same marker; a repeat for a
	// different Pod must be a typed conflict.
	Acknowledgement hangaroutput.CaptureHoldAcknowledgement

	// Execution is the exact identity every control operation is admitted
	// against. It is the BASE ledger's, carried here so a takeover can advance
	// it and the line after can ask the daemon about the new one.
	Execution executioncontrol.Identity

	// Admission is what the capture row carries. Every later request
	// re-derives its identities from this rather than from anything a
	// scenario said.
	Admission captureAdmission

	// PodUID is the pod the hold names.
	PodUID executioncontrol.PodUID

	// Repeated is what a repeated hold returned, so the idempotency pair can
	// compare two statements rather than one statement with itself.
	Repeated hangaroutput.CaptureHoldAcknowledgement

	// Status, Body and Err are the last answer, the same shape the daemon
	// families already use.
	Status int
	Body   []byte
	Err    error
}

// key is the capture this source is held for.
func (source HeldSource) key() hangaroutput.CaptureKey { return source.Admission.Key() }

// stepRoot is the step directory the daemon derives for this capture.
func (source HeldSource) stepRoot() string { return source.Draft.Daemon.stepRoot(source.key()) }

// HeldDraft is the part of the draft a held source still needs. It exists so
// HeldSource does not carry a whole ContainerDraft it cannot use.
type HeldDraft struct {
	Handle string
	Output hangaroutput.OutputName
	Daemon HangarDaemon
}

// FinishWitnessed is the exit outcome plus the witness the daemon recorded.
//
// Both halves are here because the contract is that they AGREE: the witness is
// what the step reports, and a scenario that only had one of them could not say
// so.
type FinishWitnessed struct {
	Source HeldSource

	// Outcome is what the process did, as the supervisor recorded it.
	Outcome executioncontrol.ExitOutcome

	// Witness is what the daemon durably acknowledged about it, as the outcome
	// route returned it.
	Witness executioncontrol.Acknowledgement

	// Reported is what the step told the build, which is the value the contract
	// says must equal the witness. It is read back through the daemon's OBSERVE
	// route rather than remembered from the write, which is what makes "the
	// witness is what the step reports" a claim about durability rather than a
	// value compared with itself.
	Reported executioncontrol.Acknowledgement

	// Asked is the last eligibility answer, kept as a value so a refusal is
	// assertable.
	Asked controlAnswer

	// Cancelled records that a caller asked to cancel, which is a REQUEST and
	// not a row: what the row then becomes is the plane's answer, and this
	// state carries the question rather than the answer.
	Cancelled bool

	Err error
}

// asked records an eligibility answer.
func (witnessed FinishWitnessed) asked(answer controlAnswer) FinishWitnessed {
	witnessed.Asked = answer

	return witnessed
}

// CaptureOutcome is what a capture became, in the shape of VolumeRead: the
// capture row production left, or the daemon's typed refusal.
//
// The typed failure is the point. Absence, collision, cancellation, seal
// failure and infrastructure failure stay distinct in hangar/output's sentinel
// set, and "the capture is refused as …" names which one — never "an error
// happened", and never a cache miss.
type CaptureOutcome struct {
	Source HeldSource

	// Capture is the row as production left it, read back through the
	// production repository. Zero when the chain never settled on a control
	// plane.
	Capture hangaroutput.Capture

	// Plane is the control plane this capture settled on, kept so a later step
	// can ask it another question.
	Plane *settlementPlane

	// Sealed is what the node's seal answered: the scope and digest the
	// sealed tree IS.
	Sealed hangaroutput.CaptureSealResult

	// Published is the tree reference the store assigned, as the publish
	// route reported it, or as the settled row records it.
	Published hangaroutput.CapturePublishResult

	// Answer is the raw publish answer, kept as a value so a typed refusal --
	// collision, unauthorized, sealed -- is assertable rather than fatal.
	Answer controlAnswer

	Err error
}

// PublishedTree is the tree reference, its strict attributes, and A READ OF
// THE OUTPUT BUCKET.
//
// BucketKeys and MarkerVersions are read back from the store at assertion time
// rather than remembered from the publish, which is what makes "holds exactly
// one object" an outcome instead of a call count (convention 10).
type PublishedTree struct {
	Outcome CaptureOutcome

	Ref hangar.TreeRef

	// BucketKeys and MarkerVersions are read back from the store at assertion
	// time. They are what makes "holds exactly one object" an outcome.
	BucketKeys     []string
	MarkerVersions []string

	// Second is the second capture of the same bytes, for the dedup pair.
	Second CaptureOutcome

	// Superseded is the ref a replacement generation displaced, so "the old ref
	// no longer resolves" has an old ref to fail on.
	Superseded hangar.TreeRef

	// ClaimAbsent marks the consumer refinement that removes the active claim,
	// carried here because the binding step is what discovers it.
	ClaimAbsent bool

	// UnregisteredRef marks the refinement that points the consumer at a ref
	// the lifecycle never registered.
	UnregisteredRef bool

	Err error
}

// ConsumerDraft is a later step described as taking a published output. It is
// the consumer's counterpart to CaptureDraft and, like it, is refinement-only.
type ConsumerDraft struct {
	Tree PublishedTree

	// Cluster is the worker the consuming step runs on. It is here rather than
	// carried down from the capture chain because a capture chain has no
	// cluster in it: the daemon fixture is a process and a bucket, and a
	// consumer needs a worker to build a pod on.
	Cluster WorkerReady

	StepName    string
	Output      hangaroutput.OutputName
	Destination string
}

// BoundOutput is the consumer's binding AS PRODUCTION READS IT BACK, plus the
// claim ledger view.
//
// Every field here comes from a production read rather than from a raw SQL
// select, which is what stops a repository that writes the right row through
// the wrong API from passing.
type BoundOutput struct {
	Tree PublishedTree

	// Consumer is the product-neutral test consumer this binding belongs to,
	// carried so later steps read the binding back through the same API that
	// wrote it rather than through a select of their own.
	Consumer neutralConsumer

	// Binding is the consumer's own opaque binding id.
	Binding string

	Acquisition hangaroutput.ClaimAcquisition
	Release     hangaroutput.ClaimRelease

	// Claims is the ledger view: every claim the repository reports for this
	// ref, active and tombstoned, so "exactly one", "none left behind" and "the
	// tombstone is permanent" are counts of what is THERE rather than of what
	// was asked for.
	Claims []hangaroutput.ClaimRecord

	// RolledBackClaimID and RolledBackBinding are the attempt that did not
	// commit. They are separate fields from the committed pair because the
	// scenario asserts about both at once: exactly one claim, and not this one.
	RolledBackClaimID hangaroutput.ClaimID
	RolledBackBinding string

	// HeldClaimID is what the consumer's binding names AFTER a hidden-to-
	// published transition, read back rather than remembered -- which is what
	// makes "the candidate claim ID is unchanged" a claim about the row.
	HeldClaimID hangaroutput.ClaimID

	// Visible is what the consumer's own read says about the binding. It is
	// false before verification and true after, and the scenario asserts both
	// halves.
	Visible bool

	// Lease is the read lease a warranted managed read produced.
	Lease hangaroutput.ReadLease

	Err error
}

// stubMap and stubCheck define a phrase whose production code the plan has not
// reached yet.
//
// They exist so the Hangar vocabulary is complete before its first scenario is
// written — `brine check` can walk every planned chain, and the vocabulary guard
// can see a definition nobody says. What they do NOT do is pass. Each names the
// phase that closes it and the production it is waiting for, so a red step in a
// pending feature reads as outstanding work rather than as a broken fixture,
// and no scenario can go green against nothing.
func stubMap[In, Out any](pattern, phase, needs string) brine.StepDefinition {
	return brine.DefineMap[In, Out](pattern, func(_ In, _ brine.Params, _ *brine.Recorder) (Out, error) {
		var zero Out
		return zero, pending(pattern, phase, needs)
	})
}

func stubCheck[In any](pattern, phase, needs string) brine.StepDefinition {
	return CheckThat[In](pattern, func(In) error { return pending(pattern, phase, needs) })
}
