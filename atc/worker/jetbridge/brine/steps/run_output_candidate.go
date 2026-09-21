package steps

import (
	"context"
	"encoding/base64"
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
	Finish  RunOutputFinish
	Record  output.HandoffRecord
	Err     error
}

func RunOutputCandidateDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[RunOutputCandidate, RunOutputCandidate]("its published producer is aborted", func(in RunOutputCandidate, _ brine.Params, _ *brine.Recorder) (RunOutputCandidate, error) {
			_, err := in.Finish.Start.DB.Conn.Exec(`UPDATE builds SET aborted=true WHERE id=$1`, in.Finish.Start.Creation.EntryBuilds[0].ID())
			return in, err
		}),
		brine.DefineMap[RunOutputCandidate, RunOutputCandidate]("its published producer finishes as {string}", func(in RunOutputCandidate, p brine.Params, _ *brine.Recorder) (RunOutputCandidate, error) {
			status, _ := p.GetString(0)
			in.Err = in.Finish.Start.Creation.EntryBuilds[0].Finish(db.BuildStatus(status))
			return in, nil
		}),
		CheckThat[RunOutputCandidate]("its build is complete while its Run result remains unpublished", checkCandidateBuildComplete),
		CheckThat[RunOutputCandidate]("its build is complete with no candidate claim", func(in RunOutputCandidate) error {
			if err := checkCandidateBuildComplete(in); err != nil {
				return err
			}
			claims, r, err := in.claims()
			if err != nil {
				return err
			}
			if len(claims) != 0 || !r.Settled {
				return fmt.Errorf("cancelled publication retained a candidate claim or left source settlement pending")
			}
			return nil
		}),
		CheckThat[RunOutputCandidate]("the published producer outcome is refused", func(in RunOutputCandidate) error {
			if in.Err == nil {
				return fmt.Errorf("aborted producer was reported as successful")
			}
			var completed bool
			if err := in.Finish.Start.DB.Conn.QueryRow(`SELECT completed FROM builds WHERE id=$1`, in.Finish.Start.Creation.EntryBuilds[0].ID()).Scan(&completed); err != nil {
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
		brine.DefineMap[RunOutputCandidate, RunOutputCandidate]("its Run records the published source release", func(in RunOutputCandidate, _ brine.Params, _ *brine.Recorder) (RunOutputCandidate, error) {
			var err error
			in.Finish.Release, err = in.Finish.daemonRelease()
			if err == nil {
				err = in.Finish.recordRelease(in.Finish.Release, false)
			}
			return in, err
		}),
		brine.DefineMap[RunOutputCandidate, RunOutputCandidate]("its Run rolls back the published source release", func(in RunOutputCandidate, _ brine.Params, _ *brine.Recorder) (RunOutputCandidate, error) {
			var err error
			in.Finish.Release, err = in.Finish.daemonRelease()
			if err == nil {
				err = in.Finish.recordRelease(in.Finish.Release, true)
			}
			return in, err
		}),
		brine.DefineMap[RunOutputCandidate, RunOutputCandidate]("another Run controller repeats the published source release", func(in RunOutputCandidate, _ brine.Params, _ *brine.Recorder) (RunOutputCandidate, error) {
			return in, in.Finish.recordRelease(in.Finish.Release, false)
		}),
		CheckThat[RunOutputCandidate]("one Run candidate claim protects that exact generation", func(in RunOutputCandidate) error {
			claims, r, err := in.claims()
			if err != nil {
				return err
			}
			if len(claims) != 1 || !claims[0].Active() || claims[0].Ref != in.Record.Ref {
				return fmt.Errorf("settled Run capture has %d claims protecting its generation", len(claims))
			}
			if !r.Settled || !r.ReleaseAcknowledged {
				return fmt.Errorf("candidate became visible without source settlement")
			}
			return nil
		}),
		CheckThat[RunOutputCandidate]("neither a candidate claim nor settled capture is visible", func(in RunOutputCandidate) error {
			claims, r, err := in.claims()
			if err != nil {
				return err
			}
			if len(claims) != 0 || r.Settled || r.ReleaseAcknowledged {
				return fmt.Errorf("rollback exposed a claim or settled capture")
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
	err := in.Finish.Start.DB.Conn.QueryRow(`SELECT b.completed,r.status FROM builds b JOIN pipeline_runs r ON r.id=b.pipeline_run_id WHERE b.id=$1`, in.Finish.Start.Creation.EntryBuilds[0].ID()).Scan(&completed, &status)
	if err != nil {
		return err
	}
	if !completed || status != "running" {
		return fmt.Errorf("build complete=%t, Run status=%s before aggregate result publication", completed, status)
	}
	return nil
}

func (in RunOutputCandidate) claims() ([]output.ClaimRecord, output.HandoffRecord, error) {
	tx, err := in.Finish.Start.DB.Conn.Begin()
	if err != nil {
		return nil, output.HandoffRecord{}, err
	}
	defer db.Rollback(tx)
	repository := in.Finish.repository()
	r, err := repository.LoadHandoffRecord(context.Background(), tx, in.Record.HandoffID)
	if err != nil {
		return nil, r, err
	}
	claims, err := repository.ReadClaims(context.Background(), tx, in.Record.Ref)
	return claims, r, err
}

// The model's fixed output is the only substituted content. All admission,
// control, publication, verification, release and storage operations are real.
// envtest supplies Pod identity; the fixture supplies kubelet termination status.
func publishRunCandidate(in RunOutputRuntime, rec *brine.Recorder, publish ...func(string) error) (RunOutputCandidate, error) {
	return publishRunCandidateStarted(in, rec, func(directory string, _ executioncontrol.Acknowledgement) error {
		if len(publish) > 0 {
			return publish[0](directory)
		}
		return os.WriteFile(filepath.Join(directory, "findings.json"), []byte("{\"findings\":[]}\n"), 0600)
	})
}

func publishRunCandidateStarted(in RunOutputRuntime, rec *brine.Recorder, publish func(string, executioncontrol.Acknowledgement) error) (RunOutputCandidate, error) {
	out := RunOutputCandidate{Runtime: in}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var err error
	in.Control, err = in.prepare()
	if err != nil {
		return out, err
	}
	if err := checkRuntimeGrant(in, rec); err != nil {
		return out, err
	}
	r, err := in.readSource()
	if err != nil {
		return out, err
	}
	out.Finish = RunOutputFinish{Start: in.Start}
	out.Finish.Start.Record = r
	client := jetbridge.NewOutputControlClient(in.Start.Daemon.Output.URL, in.Start.Daemon.HTTP, in.Start.Daemon.Minter, r.ActivationEpoch)
	out.Finish.Hold, err = client.InspectHold(ctx, r.Execution, r.HandoffID)
	if err != nil {
		return out, err
	}
	tx, err := in.Start.DB.Conn.Begin()
	if err != nil {
		return out, err
	}
	err = out.Finish.repository().AcknowledgeSourceHold(ctx, tx, out.Finish.Hold)
	if err == nil {
		err = tx.Commit()
	}
	db.Rollback(tx)
	if err != nil {
		return out, err
	}
	start, err := client.RecordStart(ctx, r.Execution, out.Finish.Hold.PodUID, executioncontrol.ProcessIdentity(freshUUID()))
	if err != nil {
		return out, err
	}
	directory := filepath.Join(in.Start.Daemon.Output.Root, "steps", r.Source.Directory)
	err = publish(directory, start)
	if err != nil {
		return out, err
	}

	if _, err := client.RecordOutcome(ctx, r.Execution, executioncontrol.AcknowledgementFinish, executioncontrol.ExitOutcome{ExitCode: 0}); err != nil {
		return out, err
	}
	// A real API identity and current termination status, matching the drain
	// fixture's boundary. Live CI is responsible for kubelet-driven status.
	pods, err := in.Client.CoreV1().Pods("default").List(ctx, metav1.ListOptions{})
	if err != nil {
		return out, err
	}
	found := false
	for _, pod := range pods.Items {
		if string(pod.UID) != string(out.Finish.Hold.PodUID) {
			continue
		}
		found = true
		pod.Status.Phase = corev1.PodSucceeded
		pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "main", Image: "busybox", ImageID: "brine-image", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0, Reason: "Completed"}}}}
		if _, err := in.Client.CoreV1().Pods("default").UpdateStatus(ctx, &pod, metav1.UpdateOptions{}); err != nil {
			return out, err
		}
	}
	if !found {
		return out, fmt.Errorf("source hold names no real producing Pod")
	}
	keys := hangaroutput.ReceiptKeyRing{ActiveKeyID: hangarReceiptKeyID, ActivationEpoch: r.ActivationEpoch, Keys: []hangaroutput.ReceiptKeyEntry{{ID: hangarReceiptKeyID, Epoch: r.ActivationEpoch, PublicKey: base64.StdEncoding.EncodeToString(in.Start.Daemon.ReceiptPublic)}}}
	verifier, err := keys.SignatureVerifier(output.ClockFunc(func() time.Time { return time.Now().UTC() }))
	if err != nil {
		return out, err
	}
	controls := jetbridge.NewOutputControls(in.Config, jetbridge.NewNodeIPResolver(in.Client), in.Start.Daemon.Minter, r.ActivationEpoch)
	repository := out.Finish.repository()
	coordinator := &hangaroutput.Coordinator{
		Transactor: brineTransactor{conn: in.Start.DB.Conn}, Repository: repository,
		Dialer:   hangaroutput.SourceDialerFunc(func(node string) (hangaroutput.SourceControl, error) { return controls.ForNode(ctx, node) }),
		Drain:    &jetbridge.OutputDrain{Client: in.Client, Controls: controls, Namespace: "default"},
		Verifier: verifier, HoldVerifier: hangaroutput.ControlKeyRing{ActivationEpoch: r.ActivationEpoch, Keys: []hangaroutput.ControlKeyEntry{{Epoch: r.ActivationEpoch, PublicKey: base64.StdEncoding.EncodeToString(in.Start.Daemon.ControlPublic)}}},
		Announcer: hangaroutput.AnnouncerFunc(repository.RecordAnnouncement), OwnerID: freshUUID(), ReceiptKeyID: hangarReceiptKeyID,
	}
	for i := 0; i < 15; i++ {
		if _, err := coordinator.Advance(ctx, r.HandoffID); err != nil {
			return out, err
		}
		out.Record, err = in.readSource()
		if err != nil {
			return out, err
		}
		if out.Record.State == output.CaptureStateRegistered && out.Record.Receipt != nil {
			return out, nil
		}
	}
	return out, fmt.Errorf("Run capture did not reach a verified receipt")
}
