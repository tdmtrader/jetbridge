package steps

import (
	"fmt"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
)

// ProcessGapDefinitions retains real scheduling refusal and exit-result checks.
// Literal terminal status fallbacks are covered by TestPodStatusPolicy.
func ProcessGapDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMap[LiveTaskPlan, StepOutcome](
			"a resource step waits for an unschedulable pod with {int} milliseconds for startup and {int} milliseconds for scheduling",
			func(in LiveTaskPlan, p brine.Params, rec *brine.Recorder) (StepOutcome, error) {
				startup, ok := p.GetInt(0)
				scheduling, scheduleOK := p.GetInt(1)
				if !ok || !scheduleOK || startup <= 0 || scheduling <= 0 {
					return StepOutcome{}, fmt.Errorf("expected positive startup and scheduling budgets")
				}
				return diagnoseLiveScheduling(in, rec, time.Duration(startup)*time.Millisecond, time.Duration(scheduling)*time.Millisecond)
			},
		),
		CheckThat[StepOutcome]("the step reports the scheduler's complete refusal",
			func(in StepOutcome) error {
				if in.schedulingRefusal == "" {
					return fmt.Errorf("no real scheduler refusal was observed")
				}
				if in.Err == nil || !strings.Contains(in.Message, in.schedulingRefusal) {
					return fmt.Errorf("expected the failure to include the scheduler's complete refusal %q, got %q", in.schedulingRefusal, in.Message)
				}
				return nil
			}),

		// A non-zero exit is a RESULT, not an error: Process.Wait returns
		// ProcessResult{ExitStatus: 1} with a nil error for a failed task. An
		// assertion on Err alone cannot tell a failed step from a passing one,
		// which is how the first version of these scenarios passed while
		// asserting nothing.
		//
		// Worded distinctly from step-integration's "the step reports exit
		// status {int}", which sits on a different state.
		//
		// A step that failed outright has no exit status to compare at all, so
		// that case reaches the reader from the getter rather than as a
		// mismatch against a zero it never reported.
		CheckInt[StepOutcome]("the step's exit status is {int}",
			"the step's exit status",
			func(in StepOutcome) (int, error) {
				if in.Err != nil {
					return 0, fmt.Errorf("expected an exit status, the step failed outright with %q", in.Message)
				}
				return in.ExitStatus, nil
			}),
	}
}
