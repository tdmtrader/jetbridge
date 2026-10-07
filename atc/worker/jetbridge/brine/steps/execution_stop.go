package steps

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/hangar/executioncontrol"
)

func ExecutionStopDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[HeldSource, HeldSource]("the output daemon restarts with the same control ledger", func(in HeldSource, _ brine.Params, _ *brine.Recorder) (HeldSource, error) {
			// The output plane is mounted in the artifact daemon: restarting it
			// is restarting that one process, over the same storage root, keys
			// and address, so the control ledger it reloads is the one it wrote.
			daemon := in.Draft.Daemon.Output
			if err := daemon.crash(); err != nil {
				return in, err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := daemon.restart(ctx, in.Draft.Daemon.HTTP); err != nil {
				return in, err
			}
			in.DaemonURL = daemon.URL
			return in, nil
		}),
		brine.DefineMap[HeldSource, HeldSource]("the daemon accepts stop before the first start", func(in HeldSource, _ brine.Params, _ *brine.Recorder) (HeldSource, error) {
			for i := 0; i < 2; i++ {
				answer := in.Draft.Daemon.base("stop", "/execution/v1/stop", in.Execution, identifiedBy(in.Execution))
				result, err := decodeControl[executioncontrol.RequestSourcePreservingStopResult](answer)
				if err != nil {
					return in, err
				}
				if !result.Accepted || result.Classification != executioncontrol.ClassificationNeverStarted {
					return in, fmt.Errorf("pre-start stop did not preserve its never-started classification")
				}
			}
			return in, nil
		}),
		brine.DefineMap[HeldSource, HeldSource]("a delayed supervisor attempts the stopped execution's first start", func(in HeldSource, _ brine.Params, _ *brine.Recorder) (HeldSource, error) {
			answer := in.Draft.Daemon.base("start", "/execution/v1/start", in.Execution, map[string]any{"execution": in.Execution, "pod_uid": in.PodUID, "process_identity": "delayed-supervisor"})
			return in.answered(answer), nil
		}),
		CheckThat[HeldSource]("the stopped execution refuses its first start and has no finish witness", func(in HeldSource) error {
			if in.Err != nil {
				return in.Err
			}
			if in.Status != http.StatusConflict {
				return fmt.Errorf("a stop accepted before execution still allowed first start: HTTP %d", in.Status)
			}
			answer := in.Draft.Daemon.base("observe", "/execution/v1/observe", in.Execution, identifiedBy(in.Execution))
			result, err := decodeControl[executioncontrol.ObserveFinishOrStopResult](answer)
			if err != nil {
				return err
			}
			if result.Classification != executioncontrol.ClassificationNeverStarted || result.Acknowledgement != nil {
				return fmt.Errorf("refused first start fabricated a process or finish witness")
			}
			return nil
		}),
	}
}
