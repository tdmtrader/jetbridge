package runtime

// The optional control envelope a caller opts an execution into, and the
// optional durable-output-capture extension that hangs off it.
//
// These are ATC-side VALUE types. The wire contract they are assembled against
// is hangar/executioncontrol's, and they reference its Identity and
// ActivationEpoch rather than restating them, so an execution has one exact
// truth however many optional gates hang off it.
//
// Two shapes matter here and the type system carries both:
//
//   - a base execution with NO capture is representable and complete. That is
//     the shape the sibling `exact_execution_control` track wires every
//     ordinary job onto, and this track must not make it unspellable.
//   - a capture is an EXTENSION, never a set of extra fields on the base. A
//     base envelope with output fields zeroed would be a different thing that
//     merely looks the same, and `TestTheBaseEnvelopeNamesNoOutputConcept`
//     fails if one is ever added.

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/concourse/concourse/hangar/executioncontrol"
	hangaroutput "github.com/concourse/concourse/hangar/output"
)

// ExecutionControlVersion and DurableOutputCaptureVersion are SEPARATE
// versions, and that is the extension design stated as a constant: the base
// protocol can move without every capture-selected pod in flight becoming
// unreadable, and a capture extension can move without the base cohort caring.
const (
	ExecutionControlVersion     = "atc-execution-control-v1"
	DurableOutputCaptureVersion = "atc-durable-output-capture-v1"
)

// ControlPhase is where in an execution's life an envelope was assembled. It
// is a closed vocabulary of exactly two members, because the only distinction
// this type needs to make is "before anything ran" from "after".
type ControlPhase string

const (
	// ControlPhaseAdmitted is an envelope assembled at admission, before the
	// Pod exists. It is the ONLY phase in which a capture may be selected.
	ControlPhaseAdmitted ControlPhase = "admitted"
	// ControlPhaseStarted is an envelope whose exact start has been recorded.
	// A capture selected here would be Req 1's late request: a capture of an
	// already-running task.
	ControlPhaseStarted ControlPhase = "started"
)

func (phase ControlPhase) Validate() error {
	switch phase {
	case ControlPhaseAdmitted, ControlPhaseStarted:
		return nil
	default:
		return fmt.Errorf("%w: %q is not an execution-control phase", ErrInvalidExecutionControl, phase)
	}
}

// ErrInvalidExecutionControl is every refusal in this file. It is one sentinel
// rather than several because every one of them means the same thing to the
// caller: this execution may not run under this envelope.
var ErrInvalidExecutionControl = fmt.Errorf("runtime: invalid execution control")

// ExecutionControl is the base envelope.
//
// It contains an opaque exact identity, its fence, the activation epoch it was
// admitted under, the node-local control endpoint and an attenuated control
// capability. It contains no output, no handoff, no source hold, no bucket and
// no product-domain field of any kind -- there is a test that walks this
// struct's fields and fails if one appears.
type ExecutionControl struct {
	Version         string
	Phase           ControlPhase
	Identity        executioncontrol.Identity
	ActivationEpoch executioncontrol.ActivationEpoch

	// Endpoint is the node-local control API this execution's truth lives on.
	Endpoint string

	// Capability is the attenuated bearer capability for this execution's BASE
	// operations. It authorizes stop and observation and nothing else, and it
	// is never the capture extension's grant.
	Capability executioncontrol.ControlCapability

	// Node pins executions whose owning domain reserved an exact node before
	// Pod creation. Nil preserves scheduler-selected ordinary control.
	Node *ExecutionNode

	// Capture is the optional extension. Nil is not a degraded state: it is a
	// controlled execution that captures nothing, which is a complete and
	// intended shape.
	Capture *DurableOutputCapture
}

type ExecutionNode struct {
	Name string
	UID  executioncontrol.NodeUID
}

// DurableOutputCapture is the optional extension.
//
// It REFERENCES the base identity and epoch rather than declaring its own, so
// the two cannot disagree about which execution they are for; Validate refuses
// them when they do.
type DurableOutputCapture struct {
	Version         string
	Identity        executioncontrol.Identity
	ActivationEpoch executioncontrol.ActivationEpoch

	// The identities the control plane PREDECLARES before the producing Pod
	// may start. They are minted by the caller at admission; nothing on the
	// node and nothing in a task configuration chooses them.
	HandoffID    hangaroutput.HandoffID
	SourceHoldID hangaroutput.SourceHoldID

	// Output is the NAME of the one declared task output selected for capture.
	// It is a name into ContainerSpec.Outputs, never a path: a path here would
	// be a task choosing where the output plane reads from.
	Output string

	// SourceControlGrant is the capture extension's OWN one-shot capability,
	// carried by the capture control init container and by nothing else. It is
	// never the base Capability: a capture riding the base capability would be
	// a capture holding the authority to stop the execution.
	SourceControlGrant executioncontrol.ControlCapability

	// CaptureDeadline is the database-clock deadline the capture was admitted
	// under.
	CaptureDeadline time.Time

	// ReservedIncarnation is the location the output daemon issued for this
	// capture BEFORE the producing Pod was built, and ReservedDirectory is the
	// daemon's own name for it relative to the managed steps root.
	//
	// They are here because Phase 4 found the seam they close: the producer's
	// Pod used to mount `steps/<handle>/<output>` while the hold protected
	// `steps/<execution>.<generation>/<output>`, two sibling directories, so
	// every path-keyed guard correctly answered "unmanaged" for the bytes the
	// producer actually wrote. `Container.buildPod` now mounts the reserved
	// incarnation as the selected output's volume.
	//
	// The ATC REPEATS these. It does not compose them, it cannot compose them
	// -- the handle generation is the daemon's monotonic sequence -- and
	// TestNoATCCodeComposesAnIncarnationName fails if any file under atc/
	// starts to. Req 7 holds because the daemon names the path.
	ReservedIncarnation hangaroutput.SourceIncarnation
	ReservedDirectory   string

	// ReservingNode is the Kubernetes node whose daemon issued that
	// reservation, and it is here so the producing Pod can be pinned to it.
	//
	// A reservation is a directory on ONE node's disk. The ready labels pick a
	// COHORT -- nodes where a hold could be acknowledged at all -- and a cohort
	// is not a node: a multi-node cohort lets the scheduler place the producer
	// somewhere that reserved nothing, where the hostPath mount silently
	// creates an empty unheld directory and the control init's hold is refused.
	// That is a fail-closed outage rather than an exposure, and it is still an
	// outage, so `BuildAffinity` requires this node by name.
	//
	// It is the node NAME rather than the UID because a scheduling constraint
	// is expressed against `kubernetes.io/hostname`, which is the name. The
	// UID travels beside it inside ReservedIncarnation, and the daemon checks
	// that one: a hold naming an incarnation reserved on another node is
	// refused by the node that receives it.
	ReservingNode string
}

// SelectCapture attaches the extension, and is the only way to attach it.
//
// It refuses a second selection and a selection after the exact start, which
// are the two states Req 1 forbids and the two states no brine phrase can
// construct -- `CaptureDraft` is refinement-only, so the feature files cannot
// express them and this is the only place they are pinned.
func (control *ExecutionControl) SelectCapture(capture DurableOutputCapture) error {
	if control == nil {
		return fmt.Errorf("%w: a capture extension was offered with no base envelope to extend; "+
			"the extension references the base identity and cannot declare its own",
			ErrInvalidExecutionControl)
	}
	if control.Phase != ControlPhaseAdmitted {
		return fmt.Errorf("%w: capture was selected for an execution already %s. A capture is "+
			"selected as part of the admitted task configuration, before its pod starts; a late "+
			"request cannot capture an already-running or completed task",
			ErrInvalidExecutionControl, control.Phase)
	}
	if control.Capture != nil {
		return fmt.Errorf("%w: output %q is already selected for capture and %q was also offered; "+
			"exactly one declared output is captured, so a second selection is a refusal rather "+
			"than a second element", ErrInvalidExecutionControl, control.Capture.Output, capture.Output)
	}
	control.Capture = &capture

	return nil
}

// MarkStarted advances the phase past the point a capture may be selected.
func (control *ExecutionControl) MarkStarted() { control.Phase = ControlPhaseStarted }

// HasDurableOutputCapture reports whether this execution opted in.
func (control *ExecutionControl) HasDurableOutputCapture() bool {
	return control != nil && control.Capture != nil
}

// Validate checks the envelope against the container spec it was admitted for.
//
// It takes the spec because half of what makes a capture valid is a fact about
// the step: the selected output must be one the task DECLARED, and it must not
// overlap a strict Hangar input. Neither is knowable from the envelope alone,
// and a validation that could not see the spec would be a validation of the
// wire form rather than of the request.
func (control *ExecutionControl) Validate(spec ContainerSpec) error {
	if control == nil {
		return nil
	}
	if control.Version != ExecutionControlVersion {
		return fmt.Errorf("%w: envelope version %q, this cohort speaks %q",
			ErrInvalidExecutionControl, control.Version, ExecutionControlVersion)
	}
	if err := control.Phase.Validate(); err != nil {
		return err
	}
	if err := control.Identity.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidExecutionControl, err)
	}
	if control.ActivationEpoch == 0 {
		return fmt.Errorf("%w: activation epoch is zero; an execution is admitted under an "+
			"attested epoch or not at all", ErrInvalidExecutionControl)
	}
	if strings.TrimSpace(control.Endpoint) == "" {
		return fmt.Errorf("%w: no control endpoint; an envelope nobody can ask about is not "+
			"control", ErrInvalidExecutionControl)
	}
	if control.Capability == "" {
		return fmt.Errorf("%w: no control capability", ErrInvalidExecutionControl)
	}
	if control.Node != nil {
		if strings.TrimSpace(control.Node.Name) == "" || strings.TrimSpace(string(control.Node.UID)) == "" {
			return fmt.Errorf("%w: incomplete execution node identity", ErrInvalidExecutionControl)
		}
		if control.Capture != nil && (control.Node.Name != control.Capture.ReservingNode || control.Node.UID != control.Capture.ReservedIncarnation.NodeUID) {
			return fmt.Errorf("%w: execution and capture name different nodes", ErrInvalidExecutionControl)
		}
	}
	if control.Capture == nil {
		// A controlled execution that captures nothing. Complete, and
		// deliberately so.
		return nil
	}

	return control.validateCapture(spec)
}

func (control *ExecutionControl) validateCapture(spec ContainerSpec) error {
	capture := control.Capture

	if capture.Version != DurableOutputCaptureVersion {
		return fmt.Errorf("%w: capture extension version %q, this cohort speaks %q",
			ErrInvalidExecutionControl, capture.Version, DurableOutputCaptureVersion)
	}
	if control.Phase != ControlPhaseAdmitted {
		return fmt.Errorf("%w: a capture rides an envelope already %s", ErrInvalidExecutionControl,
			control.Phase)
	}
	if capture.Identity != control.Identity {
		return fmt.Errorf("%w: the capture extension names execution %s at fence %d and the "+
			"envelope it extends names %s at fence %d", ErrInvalidExecutionControl,
			capture.Identity.ExecutionID, capture.Identity.Fence,
			control.Identity.ExecutionID, control.Identity.Fence)
	}
	if capture.ActivationEpoch != control.ActivationEpoch {
		return fmt.Errorf("%w: the capture extension was admitted under epoch %d and the envelope "+
			"under %d; one epoch attests both facets", ErrInvalidExecutionControl,
			capture.ActivationEpoch, control.ActivationEpoch)
	}
	if err := capture.HandoffID.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidExecutionControl, err)
	}
	if err := capture.SourceHoldID.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidExecutionControl, err)
	}
	if capture.CaptureDeadline.IsZero() {
		return fmt.Errorf("%w: the capture names no deadline", ErrInvalidExecutionControl)
	}
	if capture.SourceControlGrant == "" {
		return fmt.Errorf("%w: the capture carries no source-control grant; the control init "+
			"has nothing to present and the hold could never be established",
			ErrInvalidExecutionControl)
	}
	// The capture may not ride the base capability. The base capability
	// authorizes stopping the execution; a capture holding it would hold the
	// authority to stop the process it is capturing from.
	if capture.SourceControlGrant == control.Capability {
		return fmt.Errorf("%w: the capture's source-control grant is the base control "+
			"capability. They are different facets: one stops and observes an execution, the "+
			"other holds a source, and a capture that held both would be able to stop the "+
			"process whose output it is taking", ErrInvalidExecutionControl)
	}

	if err := ValidateCaptureOutput(spec, capture.Output); err != nil {
		return err
	}

	// The reservation. An unreserved execution cannot be capture-selected: the
	// producing Pod's output volume IS the reserved incarnation, so a capture
	// with none is one whose producer would write into a directory no hold
	// protects and whose seal would seal an empty tree.
	if err := capture.ReservedIncarnation.Validate(); err != nil {
		return fmt.Errorf("%w: this capture has no reserved source incarnation. The output daemon "+
			"issues one before the Pod is built and the selected output's volume is that "+
			"location; an unreserved execution cannot be capture-selected: %v",
			ErrInvalidExecutionControl, err)
	}
	if capture.ReservedIncarnation.ExecutionID != control.Identity.ExecutionID {
		return fmt.Errorf("%w: the reserved incarnation belongs to execution %s and this envelope "+
			"names %s", ErrInvalidExecutionControl,
			capture.ReservedIncarnation.ExecutionID, control.Identity.ExecutionID)
	}
	if string(capture.ReservedIncarnation.Output) != capture.Output {
		return fmt.Errorf("%w: the reserved incarnation is for output %q and this capture selects "+
			"%q", ErrInvalidExecutionControl, capture.ReservedIncarnation.Output, capture.Output)
	}
	if capture.ReservingNode == "" {
		return fmt.Errorf("%w: this capture names no reserving node. The reservation is a "+
			"directory on one node's disk and the producing Pod is pinned to that node; a "+
			"capture that cannot say which node it reserved on cannot be scheduled safely",
			ErrInvalidExecutionControl)
	}
	// The directory the pod builder will mount must be the daemon's own answer.
	// A directory that does not derive from the incarnation beside it is the
	// control plane having composed a path, which is what Req 7 forbids and
	// what this whole pair of fields exists to make unnecessary.
	if capture.ReservedDirectory != capture.ReservedIncarnation.Directory() {
		return fmt.Errorf("%w: the capture names directory %q and the reserved incarnation it "+
			"carries does not derive it. The ATC repeats the daemon's answer; it never composes "+
			"a source path", ErrInvalidExecutionControl, capture.ReservedDirectory)
	}

	return nil
}

// ValidateCaptureOutput checks the selected output before source dispatch. The
// final envelope validation repeats it so a later caller cannot skip the gate.
func ValidateCaptureOutput(spec ContainerSpec, name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("%w: the capture names no output", ErrInvalidExecutionControl)
	}

	selectedPath, declared := spec.Outputs[name]
	if !declared {
		names := make([]string, 0, len(spec.Outputs))
		for name := range spec.Outputs {
			names = append(names, name)
		}

		return fmt.Errorf("%w: output %q is selected for capture and the task declares %v. "+
			"Capture is selected for a DECLARED ordinary task output; an undeclared one has no "+
			"path, no volume and nothing to hold", ErrInvalidExecutionControl, name, names)
	}

	for _, input := range spec.Inputs {
		if input.HangarTree == nil {
			continue
		}
		if outputOverlapsInput(selectedPath, input.DestinationPath) {
			return fmt.Errorf("%w: the captured output %q at %q overlaps the strict Hangar input "+
				"at %q. A strict input is an exact immutable tree; an output written over it "+
				"would be a capture of somebody else's bytes", ErrInvalidExecutionControl,
				name, selectedPath, input.DestinationPath)
		}
	}

	return nil
}

// outputOverlapsInput is the same containment rule Container.validateInputs
// applies, restated here because atc/runtime cannot import the runtime that
// applies it and a capture selected against an overlapping path must be
// refused before any pod is built.
func outputOverlapsInput(output, input string) bool {
	output = filepath.Clean(output)
	input = filepath.Clean(input)

	return pathWithin(output, input) || pathWithin(input, output)
}

func pathWithin(parent, child string) bool {
	if parent == child {
		return true
	}
	if parent == string(filepath.Separator) {
		return filepath.IsAbs(child)
	}

	return strings.HasPrefix(child, parent+string(filepath.Separator))
}
