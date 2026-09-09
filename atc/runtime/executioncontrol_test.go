package runtime_test

// The constructor and validation table for the optional control envelope.
//
// It is a table over a VALUE type, which is why it stays Go: there is no pod,
// no daemon and no process here, and every row is a shape a caller can present.
// Two of the rows are shapes no brine phrase can construct at all -- capture
// selected after the run, and a second selection -- because `CaptureDraft` is
// refinement-only, so the feature files cannot express them and this table is
// the only place they are pinned.

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	hangaroutput "github.com/concourse/concourse/hangar/output"
)

const (
	testExecutionID = executioncontrol.ExecutionID("11111111-1111-4111-8111-111111111111")
	testHandoffID   = hangaroutput.HandoffID("22222222-2222-4222-8222-222222222222")
	testLeaseID     = hangaroutput.SourceLeaseID("33333333-3333-4333-8333-333333333333")
	testEpoch       = executioncontrol.ActivationEpoch(7)
)

func baseEnvelope() runtime.ExecutionControl {
	return runtime.ExecutionControl{
		Version:         runtime.ExecutionControlVersion,
		Phase:           runtime.ControlPhaseAdmitted,
		Identity:        executioncontrol.Identity{ExecutionID: testExecutionID, Fence: 1},
		ActivationEpoch: testEpoch,
		Endpoint:        "http://127.0.0.1:7781",
		Capability:      "base-capability",
	}
}

func captureExtension() runtime.DurableOutputCapture {
	return runtime.DurableOutputCapture{
		Version:            runtime.DurableOutputCaptureVersion,
		Identity:           executioncontrol.Identity{ExecutionID: testExecutionID, Fence: 1},
		ActivationEpoch:    testEpoch,
		HandoffID:          testHandoffID,
		SourceLeaseID:      testLeaseID,
		Output:             "result",
		SourceControlGrant: "source-control-grant",
		CaptureDeadline:    time.Now().Add(time.Hour),

		// The daemon's answer, repeated. Every field here came off the wire;
		// nothing in atc/ composes one, and
		// TestNoATCCodeComposesAnIncarnationName is what keeps that true.
		ReservedIncarnation: reservedIncarnation(),
		ReservedDirectory:   reservedIncarnation().Directory(),
	}
}

func reservedIncarnation() hangaroutput.SourceIncarnation {
	return hangaroutput.SourceIncarnation{
		ExecutionID:      testExecutionID,
		NodeUID:          "node-1",
		HandleGeneration: 4,
		Output:           "result",
	}
}

func capturingSpec() runtime.ContainerSpec {
	return runtime.ContainerSpec{
		Outputs: runtime.OutputPaths{"result": "/tmp/build/result"},
	}
}

// The two shapes the type system must keep representable, first. Every refusal
// below is only meaningful because these two pass.
func TestABaseExecutionWithNoCaptureIsCompleteAndACaptureSelectedOnceIsAdmitted(t *testing.T) {
	base := baseEnvelope()
	if err := base.Validate(capturingSpec()); err != nil {
		t.Fatalf("a controlled execution that captures nothing was refused: %v", err)
	}
	if base.HasDurableOutputCapture() {
		t.Error("a base envelope reported a capture extension")
	}

	if err := base.SelectCapture(captureExtension()); err != nil {
		t.Fatalf("selecting capture for a declared output: %v", err)
	}
	if !base.HasDurableOutputCapture() {
		t.Error("the envelope does not report the capture that was just selected")
	}
	if err := base.Validate(capturingSpec()); err != nil {
		t.Fatalf("a capture-selected execution was refused: %v", err)
	}
}

// Late injection and a second selection: the two states Req 1 forbids, and the
// two no feature file can build.
func TestCaptureIsSelectableOnlyOnceAndOnlyBeforeTheExecutionStarts(t *testing.T) {
	late := baseEnvelope()
	late.MarkStarted()
	err := late.SelectCapture(captureExtension())
	if !errors.Is(err, runtime.ErrInvalidExecutionControl) {
		t.Errorf("capture selected for an already-started execution was admitted: %v", err)
	}
	if err != nil && !strings.Contains(err.Error(), "already-running") {
		t.Errorf("the late-selection refusal does not say what it refused: %v", err)
	}
	if late.HasDurableOutputCapture() {
		t.Error("a refused late selection still attached a capture")
	}

	twice := baseEnvelope()
	if err := twice.SelectCapture(captureExtension()); err != nil {
		t.Fatalf("first selection: %v", err)
	}
	second := captureExtension()
	second.Output = "report"
	if err := twice.SelectCapture(second); !errors.Is(err, runtime.ErrInvalidExecutionControl) {
		t.Errorf("a second output was also selected for capture: %v", err)
	}
	if twice.Capture.Output != "result" {
		t.Errorf("the refused second selection replaced the first: %q", twice.Capture.Output)
	}

	// And an extension offered with no base to extend is refused rather than
	// creating one.
	var absent *runtime.ExecutionControl
	if err := absent.SelectCapture(captureExtension()); !errors.Is(err, runtime.ErrInvalidExecutionControl) {
		t.Errorf("a capture extension attached itself to no envelope: %v", err)
	}
}

// The rest of the table. Each row mutates one field of a shape that passes
// above, so a row that stops failing is a rule that stopped being enforced
// rather than a fixture that drifted.
func TestTheControlEnvelopeRefusesEveryMalformedShape(t *testing.T) {
	for name, row := range map[string]struct {
		mutate func(*runtime.ExecutionControl, *runtime.ContainerSpec)
		says   string
	}{
		"an envelope from another cohort": {
			mutate: func(c *runtime.ExecutionControl, _ *runtime.ContainerSpec) { c.Version = "v0" },
			says:   "this cohort speaks",
		},
		"an extension from another cohort": {
			mutate: func(c *runtime.ExecutionControl, _ *runtime.ContainerSpec) {
				c.Capture.Version = "v0"
			},
			says: "this cohort speaks",
		},
		// The extension with no base: a zero envelope carrying a capture. It
		// is representable precisely so it can be refused.
		"an extension with no base": {
			mutate: func(c *runtime.ExecutionControl, _ *runtime.ContainerSpec) {
				capture := c.Capture
				*c = runtime.ExecutionControl{Capture: capture}
			},
			says: "this cohort speaks",
		},
		"an unknown phase": {
			mutate: func(c *runtime.ExecutionControl, _ *runtime.ContainerSpec) { c.Phase = "running" },
			says:   "is not an execution-control phase",
		},
		"no identity": {
			mutate: func(c *runtime.ExecutionControl, _ *runtime.ContainerSpec) {
				c.Identity.ExecutionID = ""
			},
			says: "execution id is empty",
		},
		"no activation epoch": {
			mutate: func(c *runtime.ExecutionControl, _ *runtime.ContainerSpec) { c.ActivationEpoch = 0 },
			says:   "activation epoch is zero",
		},
		"no endpoint": {
			mutate: func(c *runtime.ExecutionControl, _ *runtime.ContainerSpec) { c.Endpoint = "  " },
			says:   "no control endpoint",
		},
		"no capability": {
			mutate: func(c *runtime.ExecutionControl, _ *runtime.ContainerSpec) { c.Capability = "" },
			says:   "no control capability",
		},
		"a capture for another execution": {
			mutate: func(c *runtime.ExecutionControl, _ *runtime.ContainerSpec) {
				c.Capture.Identity.ExecutionID = "44444444-4444-4444-8444-444444444444"
			},
			says: "names execution",
		},
		"a capture at another fence": {
			mutate: func(c *runtime.ExecutionControl, _ *runtime.ContainerSpec) {
				c.Capture.Identity.Fence = 2
			},
			says: "at fence",
		},
		"a capture under another epoch": {
			mutate: func(c *runtime.ExecutionControl, _ *runtime.ContainerSpec) {
				c.Capture.ActivationEpoch = testEpoch + 1
			},
			says: "one epoch attests both facets",
		},
		"a capture with no predeclared handoff": {
			mutate: func(c *runtime.ExecutionControl, _ *runtime.ContainerSpec) { c.Capture.HandoffID = "" },
			says:   "handoff id",
		},
		"a capture with no predeclared source lease": {
			mutate: func(c *runtime.ExecutionControl, _ *runtime.ContainerSpec) {
				c.Capture.SourceLeaseID = ""
			},
			says: "source lease id",
		},
		// The reservation. An unreserved execution cannot be capture-selected:
		// the incarnation is issued by the daemon before the Pod is built, and
		// a capture with no reservation behind it is one whose producer would
		// write into a directory no hold protects -- the seam this pass closes,
		// stated where a caller can hit it.
		"a capture with no reserved incarnation": {
			mutate: func(c *runtime.ExecutionControl, _ *runtime.ContainerSpec) {
				c.Capture.ReservedIncarnation = hangaroutput.SourceIncarnation{}
				c.Capture.ReservedDirectory = ""
			},
			says: "no reserved source incarnation",
		},
		"a capture whose reservation belongs to another execution": {
			mutate: func(c *runtime.ExecutionControl, _ *runtime.ContainerSpec) {
				c.Capture.ReservedIncarnation.ExecutionID = "44444444-4444-4444-8444-444444444444"
			},
			says: "reserved incarnation",
		},
		"a capture whose reservation is for another output": {
			mutate: func(c *runtime.ExecutionControl, _ *runtime.ContainerSpec) {
				c.Capture.ReservedIncarnation.Output = "report"
			},
			says: "reserved incarnation",
		},
		// The ATC repeating a directory it composed itself rather than the one
		// the daemon answered with. It is the shape Req 7 refuses, and it is
		// representable here precisely so it can be refused before a Pod is
		// built around it.
		"a capture whose directory does not derive from its reservation": {
			mutate: func(c *runtime.ExecutionControl, _ *runtime.ContainerSpec) {
				c.Capture.ReservedDirectory = "steps/a-handle-i-chose/result"
			},
			says: "does not derive",
		},
		"a capture with no deadline": {
			mutate: func(c *runtime.ExecutionControl, _ *runtime.ContainerSpec) {
				c.Capture.CaptureDeadline = time.Time{}
			},
			says: "names no deadline",
		},
		"a capture with no grant of its own": {
			mutate: func(c *runtime.ExecutionControl, _ *runtime.ContainerSpec) {
				c.Capture.SourceControlGrant = ""
			},
			says: "carries no source-control grant",
		},
		// "Capture while its capability is held": the extension riding the
		// base capability instead of its own attenuated grant. The base
		// capability stops the execution; a capture holding it could stop the
		// process it is capturing from.
		"a capture riding the base capability": {
			mutate: func(c *runtime.ExecutionControl, _ *runtime.ContainerSpec) {
				c.Capture.SourceControlGrant = c.Capability
			},
			says: "the base control capability",
		},
		"a capture naming no output": {
			mutate: func(c *runtime.ExecutionControl, _ *runtime.ContainerSpec) { c.Capture.Output = " " },
			says:   "names no output",
		},
		"a capture of an output the task never declared": {
			mutate: func(c *runtime.ExecutionControl, _ *runtime.ContainerSpec) {
				c.Capture.Output = "not-declared"
			},
			says: "the task declares",
		},
		"a capture whose output overlaps a strict Hangar input": {
			mutate: func(_ *runtime.ExecutionControl, spec *runtime.ContainerSpec) {
				spec.Inputs = []runtime.Input{{
					HangarTree:      &hangar.TreeRef{},
					DestinationPath: "/tmp/build/result/inner",
				}}
			},
			says: "overlaps the strict Hangar input",
		},
	} {
		control := baseEnvelope()
		if err := control.SelectCapture(captureExtension()); err != nil {
			t.Fatalf("%s: building the shape to mutate: %v", name, err)
		}
		spec := capturingSpec()
		row.mutate(&control, &spec)

		err := control.Validate(spec)
		if !errors.Is(err, runtime.ErrInvalidExecutionControl) {
			t.Errorf("%s was admitted: %v", name, err)

			continue
		}
		if !strings.Contains(err.Error(), row.says) {
			t.Errorf("%s was refused with a message that does not say why:\n  want substring %q\n"+
				"  got %v", name, row.says, err)
		}
	}
}

// The base envelope names no output concept, and this is a walk over its own
// fields rather than a reading of it.
//
// "Output fields on the base" is the one refusal in the Red box that cannot be
// a value row: a field that does not exist cannot be set. So the assertion is
// structural, and it fails when somebody ADDS one -- which is the only way the
// rule can ever be broken.
func TestTheBaseEnvelopeNamesNoOutputConcept(t *testing.T) {
	forbidden := []string{
		"capture", "output", "handoff", "source", "lease", "bucket", "receipt",
		"scope", "seal", "publish", "incarnation", "grant", "claim",
	}

	fields := structFieldNames(t, runtime.ExecutionControl{})
	if len(fields) == 0 {
		t.Fatal("the walk found no fields; a guard that scanned nothing proves nothing")
	}

	for _, field := range fields {
		// Capture is the extension POINTER itself, which is how the extension
		// hangs off the base at all. Everything else is base-only.
		if field == "Capture" {
			continue
		}
		lowered := strings.ToLower(field)
		for _, word := range forbidden {
			if strings.Contains(lowered, word) {
				t.Errorf("ExecutionControl.%s names an output-plane concept. The base envelope "+
					"is what a controlled execution with no capture carries; a field like this "+
					"makes the extension a set of zeroed fields on the base instead", field)
			}
		}
	}

	// And the counter-check: the extension DOES name them, so the scan above
	// is looking for something that exists somewhere.
	extension := strings.ToLower(strings.Join(structFieldNames(t, runtime.DurableOutputCapture{}), " "))
	found := 0
	for _, word := range forbidden {
		if strings.Contains(extension, word) {
			found++
		}
	}
	if found < 3 {
		t.Errorf("the extension names %d of the forbidden concepts; the scan above is looking "+
			"for words nothing in this file uses", found)
	}
}
