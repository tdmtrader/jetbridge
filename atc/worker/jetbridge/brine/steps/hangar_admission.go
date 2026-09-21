package steps

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

type CaptureAdmissionAttempt struct {
	Before, After output.HandoffRecord
	Err           error
}

func HangarAdmissionDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[HeldSource, CaptureAdmissionAttempt]("the controller recovers the source using {string}",
			[]string{"jetbridge-db"}, func(in HeldSource, p brine.Params, _ *brine.Recorder, res brine.Resources) (CaptureAdmissionAttempt, error) {
				keys, _ := p.GetString(0)
				return attemptCaptureAdmission(in, res, "recover", keys)
			}),
		CheckThat[CaptureAdmissionAttempt]("unverifiable hold recovery is refused without changing durable state", func(in CaptureAdmissionAttempt) error {
			if !errors.Is(in.Err, output.ErrUnsigned) && !errors.Is(in.Err, output.ErrIncomplete) && !errors.Is(in.Err, output.ErrCorrupt) {
				return fmt.Errorf("expected a verification refusal, got %v", in.Err)
			}
			if !reflect.DeepEqual(in.Before, in.After) {
				return fmt.Errorf("unverifiable hold changed durable admission")
			}
			return nil
		}),
		brine.DefineMapUsing[HeldSource, CaptureAdmissionAttempt]("the controller reconnects before the source hold was recorded",
			[]string{"jetbridge-db"}, func(in HeldSource, _ brine.Params, _ *brine.Recorder, res brine.Resources) (CaptureAdmissionAttempt, error) {
				return attemptCaptureAdmission(in, res, "recover", "")
			}),
		CheckThat[CaptureAdmissionAttempt]("the held source is recovered without authorizing capture", func(in CaptureAdmissionAttempt) error {
			if in.Err != nil {
				return in.Err
			}
			if in.Before.HoldAcknowledged || !in.After.HoldAcknowledged || in.After.Disposition != nil {
				return fmt.Errorf("controller did not recover only the daemon's non-authorizing hold")
			}
			return nil
		}),
		brine.DefineMapUsing[HeldSource, CaptureAdmissionAttempt]("the controller retries the predeclaration with a different deadline",
			[]string{"jetbridge-db"}, func(in HeldSource, _ brine.Params, _ *brine.Recorder, res brine.Resources) (CaptureAdmissionAttempt, error) {
				return attemptCaptureAdmission(in, res, "deadline", "")
			}),
		brine.DefineMapUsing[HeldSource, CaptureAdmissionAttempt]("the controller records a reservation with a different {string}",
			[]string{"jetbridge-db"}, func(in HeldSource, p brine.Params, _ *brine.Recorder, res brine.Resources) (CaptureAdmissionAttempt, error) {
				field, _ := p.GetString(0)
				return attemptCaptureAdmission(in, res, "reservation", field)
			}),
		brine.DefineMapUsing[HeldSource, CaptureAdmissionAttempt]("the controller records a hold with a different {string}",
			[]string{"jetbridge-db"}, func(in HeldSource, p brine.Params, _ *brine.Recorder, res brine.Resources) (CaptureAdmissionAttempt, error) {
				field, _ := p.GetString(0)
				return attemptCaptureAdmission(in, res, "hold", field)
			}),
		brine.DefineMapUsing[HeldSource, CaptureAdmissionAttempt]("the controller repeats the exact admission after reconnecting",
			[]string{"jetbridge-db"}, func(in HeldSource, _ brine.Params, _ *brine.Recorder, res brine.Resources) (CaptureAdmissionAttempt, error) {
				return attemptCaptureAdmission(in, res, "replay", "")
			}),
		CheckThat[CaptureAdmissionAttempt]("the mismatched admission is refused without changing durable state", func(in CaptureAdmissionAttempt) error {
			if !errors.Is(in.Err, output.ErrInvalidIdentity) && !errors.Is(in.Err, output.ErrConflict) {
				return fmt.Errorf("expected an identity refusal, got %v", in.Err)
			}
			if !reflect.DeepEqual(in.Before, in.After) {
				return fmt.Errorf("refused admission changed the durable handoff")
			}
			return nil
		}),
		CheckThat[CaptureAdmissionAttempt]("the original source reservation and hold remain acknowledged", func(in CaptureAdmissionAttempt) error {
			if in.Err != nil {
				return in.Err
			}
			if !in.After.Source.Reserved() || !in.After.HoldAcknowledged || !reflect.DeepEqual(in.Before, in.After) {
				return fmt.Errorf("identical replay did not preserve the acknowledged reservation")
			}
			return nil
		}),
	}
}

// The daemon supplies a real reservation and hold; PostgreSQL decides whether
// each response belongs to the current admission. Mutations model a stale or
// misrouted response, not a replacement implementation of either dependency.
func attemptCaptureAdmission(source HeldSource, res brine.Resources, operation, field string) (CaptureAdmissionAttempt, error) {
	plane, err := newSettlementPlane(source, res)
	if err != nil {
		return CaptureAdmissionAttempt{}, err
	}
	ctx := context.Background()
	tx, err := plane.DB.Conn.Begin()
	if err != nil {
		return CaptureAdmissionAttempt{}, err
	}
	defer db.Rollback(tx)
	admitted := source.Admission
	if operation == "reservation" && field == "output" {
		admitted.Output = "other"
	}
	if err := plane.Repository.PredeclareHandoff(ctx, tx, admitted); err != nil {
		return CaptureAdmissionAttempt{}, err
	}
	if operation != "reservation" {
		if err := plane.Repository.RecordSourceReservation(ctx, tx, source.Reserved, "brine-node"); err != nil {
			return CaptureAdmissionAttempt{}, err
		}
	}
	if operation == "replay" {
		if err := plane.Repository.AcknowledgeSourceHold(ctx, tx, source.Acknowledgement); err != nil {
			return CaptureAdmissionAttempt{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return CaptureAdmissionAttempt{}, err
	}

	read := func() (output.HandoffRecord, error) {
		tx, err := plane.DB.Conn.Begin()
		if err != nil {
			return output.HandoffRecord{}, err
		}
		defer db.Rollback(tx)
		return plane.Repository.LoadHandoffRecord(ctx, tx, source.Admission.HandoffID)
	}
	before, err := read()
	if err != nil {
		return CaptureAdmissionAttempt{}, err
	}
	if operation == "recover" {
		keys, advanceErr := loadAdmissionControlKeys(source, field)
		if advanceErr == nil {
			plane.Coordinator.HoldVerifier = keys
			_, advanceErr = plane.Coordinator.Advance(ctx, source.Admission.HandoffID)
		}
		after, readErr := read()
		return CaptureAdmissionAttempt{Before: before, After: after, Err: advanceErr}, readErr
	}
	tx, err = plane.DB.Conn.Begin()
	if err != nil {
		return CaptureAdmissionAttempt{}, err
	}
	defer db.Rollback(tx)
	switch operation {
	case "deadline":
		admission := source.Admission
		admission.CaptureDeadline.Time = admission.CaptureDeadline.Add(time.Hour)
		err = plane.Repository.PredeclareHandoff(ctx, tx, admission)
	case "reservation":
		reserved := source.Reserved
		switch field {
		case "fence":
			reserved.Execution.Fence++
		case "epoch":
			reserved.ActivationEpoch++
		case "output": // The predeclaration above selected a different output.
		default:
			return CaptureAdmissionAttempt{}, fmt.Errorf("unknown reservation mutation %q", field)
		}
		err = plane.Repository.RecordSourceReservation(ctx, tx, reserved, "brine-node")
	case "hold":
		ack := source.Acknowledgement
		switch field {
		case "fence":
			ack.Execution.Fence++
		case "epoch":
			ack.ActivationEpoch++
		case "output":
			ack.Incarnation.Output = "other"
		case "generation":
			ack.Incarnation.HandleGeneration++
		case "node":
			ack.NodeUID = executioncontrol.NodeUID(freshUUID())
			ack.Incarnation.NodeUID = ack.NodeUID
		default:
			return CaptureAdmissionAttempt{}, fmt.Errorf("unknown hold mutation %q", field)
		}
		err = plane.Repository.AcknowledgeSourceHold(ctx, tx, ack)
	case "replay":
		err = plane.Repository.PredeclareHandoff(ctx, tx, source.Admission)
		if err == nil {
			err = plane.Repository.RecordSourceReservation(ctx, tx, source.Reserved, "brine-node")
		}
		if err == nil {
			err = plane.Repository.AcknowledgeSourceHold(ctx, tx, source.Acknowledgement)
		}
	default:
		return CaptureAdmissionAttempt{}, fmt.Errorf("unknown admission operation %q", operation)
	}
	if err == nil {
		err = tx.Commit()
	} else {
		_ = tx.Rollback()
	}
	after, readErr := read()
	return CaptureAdmissionAttempt{Before: before, After: after, Err: err}, readErr
}

func loadAdmissionControlKeys(source HeldSource, variant string) (hangaroutput.ControlKeyRing, error) {
	ring := hangaroutput.ControlKeyRing{
		ActivationEpoch: executioncontrol.ActivationEpoch(hangarEpoch),
		Keys:            []hangaroutput.ControlKeyEntry{{Epoch: executioncontrol.ActivationEpoch(hangarEpoch), PublicKey: base64.StdEncoding.EncodeToString(source.Draft.Daemon.ControlPublic)}},
	}
	switch variant {
	case "":
	case "the receipt key":
		ring.Keys[0].PublicKey = base64.StdEncoding.EncodeToString(source.Draft.Daemon.ReceiptPublic)
	case "an unknown epoch":
		ring.ActivationEpoch++
		ring.Keys[0].Epoch++
	case "no keys":
		ring.Keys = nil
	case "duplicate epochs":
		ring.Keys = append(ring.Keys, ring.Keys[0])
	case "a malformed public key":
		ring.Keys[0].PublicKey = "not-a-public-key"
	case "a retained epoch":
		ring.ActivationEpoch++
		ring.Keys = append(ring.Keys, hangaroutput.ControlKeyEntry{Epoch: ring.ActivationEpoch, PublicKey: base64.StdEncoding.EncodeToString(source.Draft.Daemon.ReceiptPublic)})
	default:
		return hangaroutput.ControlKeyRing{}, fmt.Errorf("unknown control key setup %q", variant)
	}
	file, err := AttributedTempFile("brine-control-public-keys-*.json")
	if err != nil {
		return hangaroutput.ControlKeyRing{}, err
	}
	defer os.Remove(file.Name())
	if err := json.NewEncoder(file).Encode(ring); err != nil {
		file.Close()
		return hangaroutput.ControlKeyRing{}, err
	}
	if err := file.Close(); err != nil {
		return hangaroutput.ControlKeyRing{}, err
	}
	return hangaroutput.LoadControlKeyRing(file.Name())
}
