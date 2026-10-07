package steps

// The capture row: what a finished producer's capture becomes.
//
// This family is brine-shaped because it is sequential and observable: the
// coordinator moves one row through a closed vocabulary of states, and the row
// is what production writes and production reads back.
//
// NOTHING HERE COUNTS A CALL. Every assertion is over the row read back
// through the production repository, or over what is still on the node.
//
// What stays in Go and is cited rather than duplicated: the CAS conflicts (a
// race), deadline expiry (a clock, and convention 9 forbids a chain that
// waits), and every crash half around a commit or a node answer, which is the
// class brine cannot interpose on. Those are atc/hangaroutput's own suites.

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/brine-dev/brine-go/pkg/brine"

	hangaroutput "github.com/concourse/concourse/hangar/output"
)

// HangarCaptureRowDefinitions is the capture-row family.
func HangarCaptureRowDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		// The control plane's half, as far as it goes. See hangar_settle.go.
		brine.DefineMapUsing[FinishWitnessed, CaptureOutcome](
			"the capture settles",
			[]string{"jetbridge-db"},
			func(in FinishWitnessed, _ brine.Params, _ *brine.Recorder,
				res brine.Resources) (CaptureOutcome, error) {
				return settle(in, res)
			},
		),

		CheckString[CaptureOutcome]("the capture row is {string}",
			"the state the coordinator left the capture row in",
			func(in CaptureOutcome) (string, error) {
				if in.Err != nil {
					return "", in.Err
				}

				return string(in.Capture.State), nil
			}),

		// The reason is the closed discard vocabulary, so a watcher can tell a
		// cancellation from a producer that did not succeed.
		CheckString[CaptureOutcome]("the discard reason is {string}",
			"the reason the capture row was discarded",
			func(in CaptureOutcome) (string, error) {
				if in.Err != nil {
					return "", in.Err
				}
				if in.Capture.State != hangaroutput.CaptureDiscarded {
					return "", fmt.Errorf("the capture row is %s, not discarded", in.Capture.State)
				}

				return in.Capture.Error, nil
			}),

		// A terminal row whose node marker has been cleared. Only the node
		// that holds the marker can say it is no longer held, so the row's
		// released_at is stamped only after its answer.
		CheckThat[CaptureOutcome]("the capture is released",
			func(in CaptureOutcome) error {
				if in.Err != nil {
					return in.Err
				}
				if !in.Capture.State.Terminal() {
					return fmt.Errorf("the capture row is %s, so nothing has released it",
						in.Capture.State)
				}
				if !in.Capture.Released() {
					return fmt.Errorf("the capture row is %s and was never released; its node "+
						"marker still protects the step directory", in.Capture.State)
				}

				return nil
			}),

		// The other half, and the reason it is its own line: a producer that
		// did not succeed follows existing task semantics, and existing
		// semantics keep a failed task's outputs on the node for the build's
		// lifetime -- on_failure, hijack and artifact passing to a later step
		// all read them. A release clears the marker and never the bytes.
		CheckThat[CaptureOutcome]("the produced output is still on the node",
			func(in CaptureOutcome) error {
				root := in.Source.stepRoot()
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
	}
}
