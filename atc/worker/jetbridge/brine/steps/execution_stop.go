package steps

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/hangar/executioncontrol"
)

func ExecutionStopDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[HeldSource, HeldSource]("the output daemon restarts with the same control ledger", func(in HeldSource, _ brine.Params, rec *brine.Recorder) (HeldSource, error) {
			old := in.Draft.Daemon.Output
			if err := old.stop(); err != nil {
				return in, err
			}
			scheme := strings.SplitN(old.URL, ":", 2)[0]
			// Reuse the existing keys and ledger, but bind a fresh listener so the
			// old process's asynchronous reap cannot race the new listener.
			next, err := startNamedDaemonInRoot("hangar-output-daemon", old.Root, scheme, func(url string) error {
				response, err := in.Draft.Daemon.HTTP.Get(url + "/readyz")
				if err != nil {
					return err
				}
				defer response.Body.Close()
				if response.StatusCode != http.StatusOK {
					return fmt.Errorf("restarted daemon is not ready")
				}
				return nil
			}, old.cmd.Args[1+len(addressableArgs("hangar-output-daemon", "ignored", 1, old.Root)):]...)
			if err != nil {
				return in, err
			}
			TrackDisposer(rec, "the restarted output daemon", next.stop)
			in.Draft.Daemon.Output = next
			in.DaemonURL = next.URL
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
