package steps

import (
	"context"
	"encoding/base64"
	"fmt"
	"sync"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

type RunOutputFinish struct {
	Reserved            output.ReservedIncarnation
	Release             output.ReleaseAcknowledgement
	Start               RunOutputStart
	Hold                output.CaptureAcknowledgement
	Finish              executioncontrol.Acknowledgement
	Disposition         output.SuccessfulFinishDisposition
	NoCapture           output.NoCaptureDisposition
	Reservation, Replay output.ReservationID
	Err                 error
}

func RunOutputFinishDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[RunOutputFinish, RunOutputFinish]("capture recovery advances the Run producer", func(in RunOutputFinish, _ brine.Params, _ *brine.Recorder) (RunOutputFinish, error) {
			client := jetbridge.NewOutputControlClient(in.Start.Daemon.Output.URL, in.Start.Daemon.HTTP, in.Start.Daemon.Minter, executioncontrol.ActivationEpoch(hangarEpoch))
			coordinator := hangaroutput.Coordinator{Transactor: brineTransactor{conn: in.Start.DB.Conn}, Repository: in.repository(), Dialer: hangaroutput.SourceDialerFunc(func(node string) (hangaroutput.SourceControl, error) {
				if node != "brine-node" {
					return nil, fmt.Errorf("unknown reserved node %s", node)
				}
				return client, nil
			}), OwnerID: "brine-run-recovery"}
			if _, err := coordinator.Advance(context.Background(), in.Start.Record.HandoffID); err != nil {
				return in, err
			}
			tx, err := in.Start.DB.Conn.Begin()
			if err != nil {
				return in, err
			}
			defer db.Rollback(tx)
			record, err := in.repository().LoadHandoffRecord(context.Background(), tx, in.Start.Record.HandoffID)
			if err != nil {
				return in, err
			}
			in.Reservation = record.ReservationID
			in.Disposition.ProducerCheckpointID = record.ProducerCheckpointID
			return in, nil
		}),
		brine.DefineMap[RunOutputFinish, RunOutputFinish]("its Run controller records producer cancellation", func(in RunOutputFinish, _ brine.Params, _ *brine.Recorder) (RunOutputFinish, error) {
			if err := in.Start.Creation.EntryBuilds[0].MarkAsAborted(); err != nil {
				return in, err
			}
			if err := retainCancellationEvidence(in); err != nil {
				return in, err
			}
			tx, err := in.Start.DB.Conn.Begin()
			if err != nil {
				return in, err
			}
			defer db.Rollback(tx)
			_, in.Err = in.repository().CancelOrSettle(context.Background(), tx, in.Start.Record.HandoffID)
			if in.Err != nil {
				return in, in.Err
			}
			return in, tx.Commit()
		}),
		CheckThat[RunOutputFinish]("a direct terminal update cannot bypass its open handoff", func(in RunOutputFinish) error {
			_, err := in.Start.DB.Conn.Exec(`UPDATE pipeline_runs SET status='failed',completed_at=now(),result_manifest='{}',terminal_observation_version='invalid-direct-publication' WHERE id=$1`, in.Start.Creation.Run.ID())
			if err == nil {
				return fmt.Errorf("open producer handoff was bypassed by terminal publication")
			}
			var status string
			if err := in.Start.DB.Conn.QueryRow(`SELECT status FROM pipeline_runs WHERE id=$1`, in.Start.Creation.Run.ID()).Scan(&status); err != nil {
				return err
			}
			if status != "running" {
				return fmt.Errorf("refused terminal write changed Run state")
			}
			return nil
		}),

		CheckThat[RunOutputFinish]("Run and Hangar retain pre-reservation cancellation without a success checkpoint", func(in RunOutputFinish) error {
			var count int
			err := in.Start.DB.Conn.QueryRow(`SELECT count(*) FROM pipeline_run_output_finishes f JOIN hangar_handoff_dispositions d USING(handoff_id) WHERE f.handoff_id=$1 AND f.disposition='pre_reservation_cancel' AND d.disposition=f.disposition AND f.producer_checkpoint_id IS NULL AND f.reservation_id IS NULL`, string(in.Start.Record.HandoffID)).Scan(&count)
			if err != nil {
				return err
			}
			if count != 1 {
				return fmt.Errorf("cancellation has no owning Run decision")
			}
			return nil
		}),
		brine.DefineMap[RunOutputFinish, RunOutputFinish]("its producer build is aborted", func(in RunOutputFinish, _ brine.Params, _ *brine.Recorder) (RunOutputFinish, error) {
			return in, in.Start.Creation.EntryBuilds[0].MarkAsAborted()
		}),
		CheckThat[RunOutputFinish]("Run capture recovery observes the cancellation request", func(in RunOutputFinish) error {
			tx, err := in.Start.DB.Conn.Begin()
			if err != nil {
				return err
			}
			defer db.Rollback(tx)
			record, err := in.repository().LoadHandoffRecord(context.Background(), tx, in.Start.Record.HandoffID)
			if err != nil {
				return err
			}
			if !record.CancellationRequested {
				return fmt.Errorf("Run recovery missed a committed build abort")
			}
			return nil
		}),
		brine.DefineMap[RunOutputFinish, RunOutputFinish]("a generic capture caller bypasses the Run checkpoint", func(in RunOutputFinish, _ brine.Params, _ *brine.Recorder) (RunOutputFinish, error) {
			tx, err := in.Start.DB.Conn.Begin()
			if err != nil {
				return in, err
			}
			defer db.Rollback(tx)
			_, in.Err = in.repository().HangarOutputRepository.CommitCaptureReservation(context.Background(), tx, in.Disposition)
			if in.Err == nil {
				in.Err = tx.Commit()
			}
			return in, nil
		}),
		brine.DefineMap[RunOutputFinish, RunOutputFinish]("two Run controllers concurrently commit the finish", func(in RunOutputFinish, _ brine.Params, _ *brine.Recorder) (RunOutputFinish, error) {
			in.Start.DB.Conn.SetMaxOpenConns(4)
			var reservations [2]output.ReservationID
			var errs [2]error
			var group sync.WaitGroup
			gate := make(chan struct{})
			for i := range reservations {
				group.Add(1)
				go func(i int) { defer group.Done(); <-gate; reservations[i], errs[i] = in.capture(in.Disposition, false) }(i)
			}
			close(gate)
			group.Wait()
			for _, err := range errs {
				if err != nil {
					return in, err
				}
			}
			in.Reservation, in.Replay = reservations[0], reservations[1]
			return in, nil
		}),
		brine.DefineMapUsing[brine.Empty, RunOutputFinish]("a Run producer with an authoritative {string} finish", []string{"jetbridge-db"}, func(_ brine.Empty, p brine.Params, rec *brine.Recorder, res brine.Resources) (RunOutputFinish, error) {
			kind, _ := p.GetString(0)
			return runFinishFixture(rec, res, kind)
		}),
		brine.DefineMap[RunOutputFinish, RunOutputFinish]("its Run controller commits capture selection", func(in RunOutputFinish, _ brine.Params, _ *brine.Recorder) (RunOutputFinish, error) {
			in.Reservation, in.Err = in.capture(in.Disposition, false)
			return in, nil
		}),
		brine.DefineMap[RunOutputFinish, RunOutputFinish]("a new Run controller repeats capture selection", func(in RunOutputFinish, _ brine.Params, _ *brine.Recorder) (RunOutputFinish, error) {
			if in.Err != nil {
				return in, in.Err
			}
			in.Replay, in.Err = in.capture(in.Disposition, false)
			return in, in.Err
		}),
		brine.DefineMap[RunOutputFinish, RunOutputFinish]("its Run capture selection transaction rolls back", func(in RunOutputFinish, _ brine.Params, _ *brine.Recorder) (RunOutputFinish, error) {
			in.Reservation, in.Err = in.capture(in.Disposition, true)
			return in, in.Err
		}),
		brine.DefineMap[RunOutputFinish, RunOutputFinish]("its Run controller offers a different {string} for capture selection", func(in RunOutputFinish, p brine.Params, _ *brine.Recorder) (RunOutputFinish, error) {
			fact, _ := p.GetString(0)
			d := in.Disposition
			var err error
			switch fact {
			case "signature":
				d.FinishAcknowledgement.Signature = base64.StdEncoding.EncodeToString(make([]byte, 64))
			case "pod":
				d.FinishAcknowledgement.PodUID = "replacement-pod"
			case "epoch":
				d.ActivationEpoch++
			case "source hold":
				d.SourceHoldID = output.SourceHoldID(freshUUID())
			case "selected output":
				d.Output = "other"
			case "deadline":
				d.CaptureDeadline = output.NewTimestamp(d.CaptureDeadline.Add(time.Minute))
			case "checkpoint":
				d.ProducerCheckpointID = output.OpaqueID(freshUUID())
			case "aborted build":
				_, err = in.Start.DB.Conn.Exec(`UPDATE builds SET aborted=true WHERE id=$1`, in.Start.Creation.EntryBuilds[0].ID())
			default:
				return in, fmt.Errorf("unknown finish mutation %s", fact)
			}
			if err != nil {
				return in, err
			}
			_, in.Err = in.capture(d, false)
			return in, nil
		}),
		CheckThat[RunOutputFinish]("one Run checkpoint names the exact retained capture reservation", checkRunCheckpoint),
		CheckThat[RunOutputFinish]("neither a Run checkpoint nor a capture reservation exists", checkNoRunCheckpoint),
		CheckThat[RunOutputFinish]("Run capture selection is refused without a checkpoint or reservation", func(in RunOutputFinish) error {
			if in.Err == nil {
				return fmt.Errorf("unadmitted finish selected a capture")
			}
			return checkNoRunCheckpoint(in)
		}),
		CheckThat[RunOutputFinish]("the first Run checkpoint and capture reservation remain unchanged", func(in RunOutputFinish) error {
			if in.Err == nil {
				return fmt.Errorf("checkpoint identity changed on replay")
			}
			in.Err = nil
			return checkRunCheckpoint(in)
		}),
		brine.DefineMap[RunOutputFinish, RunOutputFinish]("its Run controller commits the no-capture decision", func(in RunOutputFinish, _ brine.Params, _ *brine.Recorder) (RunOutputFinish, error) {
			tx, err := in.Start.DB.Conn.Begin()
			if err != nil {
				return in, err
			}
			defer db.Rollback(tx)
			in.Err = in.repository().RecordNoCaptureIntent(context.Background(), tx, in.NoCapture)
			if in.Err != nil {
				return in, in.Err
			}
			return in, tx.Commit()
		}),
		CheckThat[RunOutputFinish]("the Run retains that exact no-capture decision and no success checkpoint", func(in RunOutputFinish) error {
			var branch string
			var checkpoint, reservation *string
			if err := in.Start.DB.Conn.QueryRow(`SELECT disposition,producer_checkpoint_id,reservation_id FROM pipeline_run_output_finishes WHERE handoff_id=$1`, string(in.Start.Record.HandoffID)).Scan(&branch, &checkpoint, &reservation); err != nil {
				return err
			}
			if branch != "no_capture" || checkpoint != nil || reservation != nil {
				return fmt.Errorf("non-success created a success checkpoint: %s %v %v", branch, checkpoint, reservation)
			}
			return nil
		}),
		brine.DefineMap[RunOutputFinish, RunOutputFinish]("a new controller asks to admit that producer again", func(in RunOutputFinish, _ brine.Params, _ *brine.Recorder) (RunOutputFinish, error) {
			if in.Err != nil {
				return in, in.Err
			}
			_, in.Err = in.Start.start(in.Start.Plan, int64(hangarEpoch), "brine-node", hangarNodeUID, false)
			return in, nil
		}),
		CheckThat[RunOutputFinish]("producer start admission is closed by its finish decision", func(in RunOutputFinish) error {
			if in.Err == nil {
				return fmt.Errorf("start admission reopened after finish disposition")
			}
			return nil
		}),
	}
}

func (in RunOutputFinish) repository() *db.RunOutputRepository {
	keys := hangaroutput.ControlKeyRing{ActivationEpoch: executioncontrol.ActivationEpoch(hangarEpoch), Keys: []hangaroutput.ControlKeyEntry{{Epoch: executioncontrol.ActivationEpoch(hangarEpoch), PublicKey: base64.StdEncoding.EncodeToString(in.Start.Daemon.ControlPublic)}}}
	ring := hangaroutput.ReceiptKeyRing{ActiveKeyID: hangarReceiptKeyID, ActivationEpoch: executioncontrol.ActivationEpoch(hangarEpoch), Keys: []hangaroutput.ReceiptKeyEntry{{ID: hangarReceiptKeyID, Epoch: executioncontrol.ActivationEpoch(hangarEpoch), PublicKey: base64.StdEncoding.EncodeToString(in.Start.Daemon.ReceiptPublic)}}}
	verifier, err := ring.SignatureVerifier(output.ClockFunc(func() time.Time { return time.Now().UTC() }))
	if err != nil {
		panic(err)
	}
	return db.NewRunOutputRepository(db.NewHangarOutputRepository(db.HangarConsumerPrefixForComponent()), keys, verifier)
}

func (in RunOutputFinish) capture(d output.SuccessfulFinishDisposition, rollback bool) (output.ReservationID, error) {
	tx, err := in.Start.DB.Conn.Begin()
	if err != nil {
		return "", err
	}
	defer db.Rollback(tx)
	reservation, err := in.repository().CommitCaptureReservation(context.Background(), tx, d)
	if err != nil || rollback {
		return reservation, err
	}
	return reservation, tx.Commit()
}

func runFinishFixture(rec *brine.Recorder, res brine.Resources, kind string) (RunOutputFinish, error) {
	start, err := runOutputFixture(rec, res, "current")
	in := RunOutputFinish{Start: start}
	if err != nil {
		return in, err
	}
	if start.Err != nil {
		return in, start.Err
	}
	start.Record, err = start.start(start.Plan, int64(hangarEpoch), "brine-node", hangarNodeUID, false)
	if err != nil {
		return in, err
	}
	if kind == "not reserved" {
		in.Start = start
		return in, nil
	}
	start, err = reserveRunSource(start, false)
	if err != nil {
		return in, err
	}
	if start.Err != nil {
		return in, start.Err
	}
	in.Start = start
	r := start.Replay
	admission := output.CaptureAdmission{ProtocolVersion: output.ProtocolVersion, Execution: r.Execution, ActivationEpoch: r.ActivationEpoch, HandoffID: r.HandoffID, SourceHoldID: r.SourceHoldID, Output: r.Output, CaptureDeadline: r.CaptureDeadline}
	pod := executioncontrol.PodUID("run-producer-" + freshUUID())
	answer := start.Daemon.capture("hold", "/capture/v1/hold", r.Execution, holdBody(admission, r.Source.Incarnation, pod))
	in.Hold, err = decodeControl[output.CaptureAcknowledgement](answer)
	if err != nil {
		return in, err
	}
	tx, err := start.DB.Conn.Begin()
	if err != nil {
		return in, err
	}
	defer db.Rollback(tx)
	if err := in.repository().AcknowledgeSourceHold(context.Background(), tx, in.Hold); err != nil {
		return in, err
	}
	if err := tx.Commit(); err != nil {
		return in, err
	}
	if kind == "never started" {
		return in, nil
	}
	if kind == "start only" {
		started := start.Daemon.base("start", "/execution/v1/start", r.Execution, map[string]any{"execution": r.Execution, "pod_uid": pod, "process_identity": "brine-supervisor-" + freshUUID()})
		in.Finish, err = decodeControl[executioncontrol.Acknowledgement](started)
	} else {
		exit := 0
		if kind == "failure" {
			exit = 1
		}
		held := heldFrom(CaptureDraft{Daemon: start.Daemon, Admission: admission, PodUID: pod}, in.Hold, answer)
		witnessed, witnessErr := held.witness(executioncontrol.AcknowledgementFinish, executioncontrol.ExitOutcome{ExitCode: exit})
		in.Finish, err = witnessed.Reported, witnessErr
	}
	if err != nil {
		return in, err
	}
	in.Disposition = output.SuccessfulFinishDisposition{ProtocolVersion: output.ProtocolVersion, Disposition: output.DispositionCapture, Execution: r.Execution, ActivationEpoch: r.ActivationEpoch, HandoffID: r.HandoffID, SourceHoldID: r.SourceHoldID, ProducerCheckpointID: output.OpaqueID(freshUUID()), Output: r.Output, CaptureFence: 1, CaptureDeadline: r.CaptureDeadline, FinishAcknowledgement: in.Finish}
	in.NoCapture = output.NoCaptureDisposition{ProtocolVersion: output.ProtocolVersion, Disposition: output.DispositionNoCapture, Execution: r.Execution, ActivationEpoch: r.ActivationEpoch, HandoffID: r.HandoffID, SourceHoldID: r.SourceHoldID, Reason: output.NoCaptureAuthoritativeNonSuccess, ReleaseIntentID: output.ReleaseIntentID(freshUUID()), FinishAcknowledgement: &in.Finish}
	return in, nil
}

func checkRunCheckpoint(in RunOutputFinish) error {
	if in.Err != nil {
		return in.Err
	}
	if in.Replay != "" && in.Reservation != in.Replay {
		return fmt.Errorf("capture replay replaced reservation")
	}
	var count int
	err := in.Start.DB.Conn.QueryRow(`SELECT count(*) FROM pipeline_run_output_finishes f JOIN pipeline_run_output_starts s USING(handoff_id) JOIN hangar_capture_reservations c ON c.reservation_id=f.reservation_id WHERE s.run_id=$1 AND s.build_id=$2 AND s.task_id=$3 AND f.producer_checkpoint_id=$4 AND f.reservation_id=$5 AND f.disposition='capture' AND c.producer_checkpoint_id=f.producer_checkpoint_id AND c.handoff_id=f.handoff_id`, in.Start.Creation.Run.ID(), in.Start.Creation.EntryBuilds[0].ID(), in.Start.Plan.TaskID, string(in.Disposition.ProducerCheckpointID), string(in.Reservation)).Scan(&count)
	if err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("capture has no matching Run producer checkpoint")
	}
	return nil
}

func checkNoRunCheckpoint(in RunOutputFinish) error {
	var finishes, captures int
	err := in.Start.DB.Conn.QueryRow(`SELECT (SELECT count(*) FROM pipeline_run_output_finishes WHERE handoff_id=$1),(SELECT count(*) FROM hangar_capture_reservations WHERE handoff_id=$1)`, string(in.Start.Record.HandoffID)).Scan(&finishes, &captures)
	if err != nil {
		return err
	}
	if finishes != 0 || captures != 0 {
		return fmt.Errorf("uncommitted finish retained %d checkpoints and %d captures", finishes, captures)
	}
	return nil
}
