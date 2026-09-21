package steps

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

func RunOutputReleaseDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		CheckThat[RunOutputFinish]("the unreserved producer is complete without release evidence", func(in RunOutputFinish) error {
			if in.Err != nil {
				return in.Err
			}
			var completed, reserved, acknowledged bool
			err := in.Start.DB.Conn.QueryRow(`SELECT b.completed,x.source_reserved,EXISTS(SELECT 1 FROM pipeline_run_output_releases WHERE handoff_id=x.handoff_id) FROM builds b JOIN pipeline_run_output_starts s ON s.build_id=b.id JOIN hangar_pre_reservation_cancel_dispositions x USING(handoff_id) WHERE b.id=$1`, in.Start.Creation.EntryBuilds[0].ID()).Scan(&completed, &reserved, &acknowledged)
			if err != nil {
				return err
			}
			if !completed || reserved || acknowledged {
				return fmt.Errorf("unreserved cancellation: completed=%t reserved=%t acknowledged=%t", completed, reserved, acknowledged)
			}
			return nil
		}),
		brine.DefineMap[RunOutputFinish, RunOutputFinish]("a generic source-release caller bypasses the Run acknowledgement", func(in RunOutputFinish, _ brine.Params, _ *brine.Recorder) (RunOutputFinish, error) {
			ack, err := in.daemonRelease()
			if err != nil {
				return in, err
			}
			tx, err := in.Start.DB.Conn.Begin()
			if err != nil {
				return in, err
			}
			defer db.Rollback(tx)
			in.Err = in.repository().HangarOutputRepository.AcknowledgeNoCaptureRelease(context.Background(), tx, ack)
			if in.Err == nil {
				in.Err = tx.Commit()
			}
			return in, nil
		}),
		brine.DefineMap[RunOutputFinish, RunOutputFinish]("its Run controller records the daemon source release", func(in RunOutputFinish, _ brine.Params, _ *brine.Recorder) (RunOutputFinish, error) {
			ack, err := in.daemonRelease()
			if err != nil {
				return in, err
			}
			in.Release = ack
			in.Err = in.recordRelease(ack, false)
			return in, in.Err
		}),
		brine.DefineMap[RunOutputFinish, RunOutputFinish]("its Run release transaction rolls back", func(in RunOutputFinish, _ brine.Params, _ *brine.Recorder) (RunOutputFinish, error) {
			ack, err := in.daemonRelease()
			if err != nil {
				return in, err
			}
			in.Release = ack
			in.Err = in.recordRelease(ack, true)
			return in, in.Err
		}),
		brine.DefineMap[RunOutputFinish, RunOutputFinish]("another controller records the same source release", func(in RunOutputFinish, _ brine.Params, _ *brine.Recorder) (RunOutputFinish, error) {
			in.Err = in.recordRelease(in.Release, false)
			return in, in.Err
		}),
		brine.DefineMap[RunOutputFinish, RunOutputFinish]("its Run controller offers a different {string} for source release", func(in RunOutputFinish, p brine.Params, _ *brine.Recorder) (RunOutputFinish, error) {
			fact, _ := p.GetString(0)
			ack, err := in.daemonRelease()
			if err != nil {
				return in, err
			}
			in.Release = ack
			switch fact {
			case "signature":
				ack.Signature = base64.StdEncoding.EncodeToString(make([]byte, 64))
			case "epoch":
				ack.ActivationEpoch++
			case "source hold":
				ack.SourceHoldID = output.SourceHoldID(freshUUID())
			case "incarnation":
				ack.Incarnation.NodeUID = "replacement-node"
			default:
				return in, fmt.Errorf("unknown release mutation %s", fact)
			}
			in.Err = in.recordRelease(ack, false)
			return in, nil
		}),
		brine.DefineMap[RunOutputFinish, RunOutputFinish]("its build finishes with the {string} outcome", func(in RunOutputFinish, p brine.Params, _ *brine.Recorder) (RunOutputFinish, error) {
			status, _ := p.GetString(0)
			in.Err = in.Start.Creation.EntryBuilds[0].Finish(db.BuildStatus(status))
			return in, nil
		}),
		CheckThat[RunOutputFinish]("the Run retains the release and the build is complete", func(in RunOutputFinish) error {
			if in.Err != nil {
				return in.Err
			}
			var retained, completed bool
			err := in.Start.DB.Conn.QueryRow(`SELECT EXISTS(SELECT 1 FROM pipeline_run_output_releases WHERE handoff_id=$1),completed FROM builds WHERE id=$2`, string(in.Start.Record.HandoffID), in.Start.Creation.EntryBuilds[0].ID()).Scan(&retained, &completed)
			if err != nil {
				return err
			}
			if !retained || !completed {
				return fmt.Errorf("release retained=%t, build complete=%t", retained, completed)
			}
			return nil
		}),
		CheckThat[RunOutputFinish]("the Run still owes release acknowledgement", checkRunReleasePending),
		CheckThat[RunOutputFinish]("Run source release is refused and its build remains pending", func(in RunOutputFinish) error {
			if in.Err == nil {
				return fmt.Errorf("a different source release was accepted")
			}
			return checkRunReleasePending(in)
		}),
		CheckThat[RunOutputFinish]("the build outcome is refused and remains pending", func(in RunOutputFinish) error {
			if in.Err == nil {
				return fmt.Errorf("a failed producer was reported as successful")
			}
			var completed bool
			if err := in.Start.DB.Conn.QueryRow(`SELECT completed FROM builds WHERE id=$1`, in.Start.Creation.EntryBuilds[0].ID()).Scan(&completed); err != nil {
				return err
			}
			if completed {
				return fmt.Errorf("refused outcome still completed the build")
			}
			return nil
		}),
	}
}

func (in RunOutputFinish) daemonRelease() (output.ReleaseAcknowledgement, error) {
	tx, err := in.Start.DB.Conn.Begin()
	if err != nil {
		return output.ReleaseAcknowledgement{}, err
	}
	record, err := in.repository().LoadHandoffRecord(context.Background(), tx, in.Start.Record.HandoffID)
	db.Rollback(tx)
	if err != nil {
		return output.ReleaseAcknowledgement{}, err
	}
	if record.Disposition == nil {
		return output.ReleaseAcknowledgement{}, fmt.Errorf("release has no disposition")
	}
	client := jetbridge.NewOutputControlClient(in.Start.Daemon.Output.URL, in.Start.Daemon.HTTP, in.Start.Daemon.Minter, executioncontrol.ActivationEpoch(hangarEpoch))
	return client.AcknowledgeRelease(context.Background(), output.ReleaseIntent{ProtocolVersion: output.ProtocolVersion, Disposition: *record.Disposition, Execution: record.Execution, ActivationEpoch: record.ActivationEpoch, HandoffID: record.HandoffID, SourceHoldID: record.SourceHoldID, ReleaseIntentID: record.ReleaseIntentID, Incarnation: record.Source.Incarnation})
}

func (in RunOutputFinish) recordRelease(ack output.ReleaseAcknowledgement, rollback bool) error {
	tx, err := in.Start.DB.Conn.Begin()
	if err != nil {
		return err
	}
	defer db.Rollback(tx)
	repository := in.repository()
	switch ack.Disposition {
	case output.DispositionNoCapture:
		err = repository.AcknowledgeNoCaptureRelease(context.Background(), tx, ack)
	case output.DispositionPreReservationCancel:
		err = repository.AcknowledgePreReservationCancelRelease(context.Background(), tx, ack)
	case output.DispositionCapture:
		err = repository.AcknowledgeCaptureRelease(context.Background(), tx, ack)
	default:
		return fmt.Errorf("no release method for %s", ack.Disposition)
	}
	if err != nil || rollback {
		return err
	}
	return tx.Commit()
}

func checkRunReleasePending(in RunOutputFinish) error {
	var release, completed bool
	err := in.Start.DB.Conn.QueryRow(`SELECT EXISTS(SELECT 1 FROM pipeline_run_output_releases WHERE handoff_id=$1),completed FROM builds WHERE id=$2`, string(in.Start.Record.HandoffID), in.Start.Creation.EntryBuilds[0].ID()).Scan(&release, &completed)
	if err != nil {
		return err
	}
	if release || completed {
		return fmt.Errorf("uncommitted release or external completion: release=%t complete=%t", release, completed)
	}
	return nil
}
