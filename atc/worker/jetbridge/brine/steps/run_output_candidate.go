package steps

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type RunOutputCandidate struct {
	Runtime RunOutputRuntime
	Start   RunOutputStart
	Hold    output.CaptureHoldAcknowledgement
	Record  runCaptureRecord
	Err     error
}

func RunOutputCandidateDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[RunOutputCandidate, RunOutputCandidate]("its published producer is aborted", func(in RunOutputCandidate, _ brine.Params, _ *brine.Recorder) (RunOutputCandidate, error) {
			_, err := in.Start.DB.Conn.Exec(`UPDATE builds SET aborted=true WHERE id=$1`, in.Start.Creation.EntryBuilds[0].ID())
			return in, err
		}),
		brine.DefineMap[RunOutputCandidate, RunOutputCandidate]("its published producer finishes as {string}", func(in RunOutputCandidate, p brine.Params, _ *brine.Recorder) (RunOutputCandidate, error) {
			status, _ := p.GetString(0)
			in.Err = in.Start.Creation.EntryBuilds[0].Finish(db.BuildStatus(status))
			return in, nil
		}),
		CheckThat[RunOutputCandidate]("its build is complete while its Run result remains unpublished", checkCandidateBuildComplete),
		CheckThat[RunOutputCandidate]("the published producer outcome is refused", func(in RunOutputCandidate) error {
			if in.Err == nil {
				return fmt.Errorf("aborted producer was reported as successful")
			}
			var completed bool
			if err := in.Start.DB.Conn.QueryRow(`SELECT completed FROM builds WHERE id=$1`, in.Start.Creation.EntryBuilds[0].ID()).Scan(&completed); err != nil {
				return err
			}
			if completed {
				return fmt.Errorf("refused completion still completed the build")
			}
			return nil
		}),
		brine.DefineMap[RunOutputRuntime, RunOutputCandidate]("its runtime producer publishes a successful review", func(in RunOutputRuntime, _ brine.Params, rec *brine.Recorder) (RunOutputCandidate, error) {
			return publishRunCandidate(in, rec)
		}),
		// The capture's publication takes a claim of its own, in the same
		// transaction that moves the row to published: that claim is what
		// protects the generation until the Run binds it as a result.
		CheckThat[RunOutputCandidate]("one Run capture claim protects that exact generation", func(in RunOutputCandidate) error {
			if in.Err != nil {
				return in.Err
			}
			claims, err := in.claims()
			if err != nil {
				return err
			}
			own := in.Record.Key.ClaimID()
			if len(claims) != 1 || !claims[0].Active() || claims[0].Ref != in.Record.Ref || claims[0].ClaimID != own {
				return fmt.Errorf("the published Run capture has %d claims on its generation; want its own claim %s, active", len(claims), own)
			}
			if !in.Record.Released() {
				return fmt.Errorf("the capture's claim became visible before its node marker was released")
			}
			return nil
		}),
	}
}

func checkCandidateBuildComplete(in RunOutputCandidate) error {
	if in.Err != nil {
		return in.Err
	}
	var completed bool
	var status string
	err := in.Start.DB.Conn.QueryRow(`SELECT b.completed,r.status FROM builds b JOIN pipeline_runs r ON r.id=b.pipeline_run_id WHERE b.id=$1`, in.Start.Creation.EntryBuilds[0].ID()).Scan(&completed, &status)
	if err != nil {
		return err
	}
	if !completed || status != "running" {
		return fmt.Errorf("build complete=%t, Run status=%s before aggregate result publication", completed, status)
	}
	return nil
}

func (in RunOutputCandidate) claims() ([]output.ClaimRecord, error) {
	tx, err := in.Start.DB.Conn.Begin()
	if err != nil {
		return nil, err
	}
	defer db.Rollback(tx)
	return runCaptureRepository().ReadClaims(context.Background(), tx, in.Record.Ref)
}

// The model's fixed output is the only substituted content. All admission,
// control, publication, verification, release and storage operations are real.
// envtest supplies Pod identity; the fixture supplies kubelet termination status.
func publishRunCandidate(in RunOutputRuntime, rec *brine.Recorder, publish ...func(string) error) (RunOutputCandidate, error) {
	return publishRunCandidateStarted(in, rec, func(directory string, _ executioncontrol.Acknowledgement) error {
		if len(publish) > 0 {
			return publish[0](directory)
		}
		return writeRunFindings(directory)
	})
}

func writeRunFindings(directory string) error {
	return os.WriteFile(filepath.Join(directory, "findings.json"), []byte("{\"findings\":[]}\n"), 0600)
}

func publishRunCandidateStarted(in RunOutputRuntime, rec *brine.Recorder, publish func(string, executioncontrol.Acknowledgement) error) (RunOutputCandidate, error) {
	return advanceRunCapture(in, rec, publish, func(r runCaptureRecord) bool {
		return r.State == output.CapturePublished && r.Released()
	})
}

// advanceRunCapture runs the producer and then the production capture
// coordinator until the Run's capture row satisfies until.
//
// The producer is played the way a deployment's Pod plays it: the runtime's
// own hold grant writes the held marker from a real Pod on the capture's
// node, the supervisor records the start, the task writes its output into the
// capture's step directory, the supervisor records the finish, and the Pod's
// containers stop. Everything after that is the coordinator's.
func advanceRunCapture(in RunOutputRuntime, rec *brine.Recorder, publish func(string, executioncontrol.Acknowledgement) error, until func(runCaptureRecord) bool) (RunOutputCandidate, error) {
	out := RunOutputCandidate{Runtime: in, Start: in.Start}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var err error
	in.Control, err = in.prepare()
	if err != nil {
		return out, err
	}
	out.Runtime = in
	out.Hold, err = holdRuntimeCapture(in, rec)
	if err != nil {
		return out, err
	}
	r, err := in.readSource()
	if err != nil {
		return out, err
	}
	out.Start.Record = r
	client := jetbridge.NewOutputControlClient(in.Start.Daemon.Output.URL, in.Start.Daemon.HTTP, in.Start.Daemon.Minter, executioncontrol.ActivationEpoch(hangarEpoch))
	start, err := client.RecordStart(ctx, r.Execution, out.Hold.Marker.PodUID, executioncontrol.ProcessIdentity(freshUUID()))
	if err != nil {
		return out, err
	}
	if err := publish(in.Start.Daemon.stepRoot(r.Key), start); err != nil {
		return out, err
	}
	if _, err := client.RecordOutcome(ctx, r.Execution, executioncontrol.AcknowledgementFinish, executioncontrol.ExitOutcome{ExitCode: 0}); err != nil {
		return out, err
	}
	if err := terminateRunProducer(ctx, in, out.Hold.Marker.PodUID); err != nil {
		return out, err
	}
	coordinator := runOutputCoordinator(in)
	for i := 0; i < 5; i++ {
		if err := coordinator.Advance(ctx, r.Key); err != nil {
			return out, err
		}
		out.Record, err = in.readSource()
		if err != nil {
			return out, err
		}
		if until(out.Record) {
			out.Start.Record = out.Record
			return out, nil
		}
	}
	return out, fmt.Errorf("Run capture did not reach its stopping state; it is %s (released %t, error %q)",
		out.Record.State, out.Record.Released(), out.Record.Error)
}

// terminateRunProducer stops the producing Pod's containers: its status in
// the API server, the way a kubelet reports it, and the standalone daemon's
// own declaration, which is what a seal on a daemon with no Kubernetes API
// waits for.
func terminateRunProducer(ctx context.Context, in RunOutputRuntime, uid executioncontrol.PodUID) error {
	pods, err := in.Client.CoreV1().Pods("default").List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	found := false
	for _, pod := range pods.Items {
		if string(pod.UID) != string(uid) {
			continue
		}
		found = true
		pod.Status.Phase = corev1.PodSucceeded
		pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "main", Image: "busybox", ImageID: "brine-image", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0, Reason: "Completed"}}}}
		if _, err := in.Client.CoreV1().Pods("default").UpdateStatus(ctx, &pod, metav1.UpdateOptions{}); err != nil {
			return err
		}
	}
	if !found {
		return fmt.Errorf("the hold names no real producing Pod")
	}
	return in.Start.Daemon.terminate(uid)
}

// runOutputCoordinator is atccmd's hangarOutputCoordinator over this fixture:
// the capture rows in this scenario's PostgreSQL, and the node daemons the
// rows name reached through the production output source.
func runOutputCoordinator(in RunOutputRuntime) *hangaroutput.Coordinator {
	source := in.source()
	return &hangaroutput.Coordinator{
		Transactor: brineTransactor{conn: in.Start.DB.Conn},
		Rows:       runCaptureRepository(),
		Dialer: hangaroutput.SourceDialerFunc(func(ctx context.Context, node string, uid executioncontrol.NodeUID) (hangaroutput.SourceControl, error) {
			dial, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			return source.CaptureControl(dial, node, uid)
		}),
		ActivationEpoch: executioncontrol.ActivationEpoch(hangarEpoch),
	}
}

// runCaptureRepository is the component-held repository production's
// coordinator and Run finalization read capture rows and claims through.
func runCaptureRepository() *db.HangarOutputRepository {
	return db.NewHangarOutputRepository(db.HangarConsumerPrefixForComponent())
}
