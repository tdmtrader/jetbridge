package steps

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
	"github.com/google/uuid"
)

// A capability assertion makes the missing production operation a recorded
// behavioral failure. All dependencies below it are real node/DB operations.
type inputRegistrationPort interface {
	ReserveInputPublication(context.Context, output.Tx, output.InputStage, string) error
	RegisterInputPublication(context.Context, output.Tx, output.InputPublication, *output.ReceiptSignatureVerifier) error
}

func RunInputRegistrationDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{brine.DefineMapUsing[HangarDaemon, HangarDaemon]("its input registration encounters {string}", []string{"jetbridge-db"}, func(in HangarDaemon, p brine.Params, _ *brine.Recorder, res brine.Resources) (HangarDaemon, error) {
		mode, _ := p.GetString(0)
		jdb, err := jetbridgeDBFrom(res)
		if err != nil {
			return in, err
		}
		return in, exerciseInputRegistration(in, jdb, mode)
	})}
}

func exerciseInputRegistration(in HangarDaemon, jdb JetbridgeDB, mode string) error {
	prefix, err := db.HangarConsumerPrefixHeld("brine-input-registration")
	if err != nil {
		return err
	}
	repository := db.NewHangarOutputRepository(prefix)
	port, ok := any(repository).(inputRegistrationPort)
	if !ok {
		return fmt.Errorf("Hangar has no transactional input publication registration")
	}
	if err := openActivationEpoch(jdb); err != nil {
		return err
	}
	if mode == "expired reservation" || mode == "commit after deadline" || mode == "reservation late commit" {
		if err := in.Output.cmd.Process.Kill(); err != nil {
			return err
		}
		<-in.Output.done
		in.Output.cmd.Args = append(in.Output.cmd.Args, "--output-timeout", "1s")
		if err := in.Output.restart(in.Ctx, in.HTTP); err != nil {
			return err
		}
	}
	verifier, err := inputPublicationVerifier(in)
	if err != nil {
		return err
	}
	node := jetbridge.NewOutputControlClient(in.Output.URL, in.HTTP, in.Minter, executioncontrol.ActivationEpoch(hangarEpoch))
	archive, err := durableTarOfOneFile("manifest.json", "registered review bundle")
	if err != nil {
		return err
	}
	stage, err := node.StageInput(in.Ctx, executioncontrol.NodeUID(in.NodeUID), bytes.NewReader(archive))
	if err != nil {
		return err
	}
	nonce := uuid.NewString()
	transact := func(rollback bool, f func(db.Tx) error) error {
		tx, err := jdb.Conn.Begin()
		if err != nil {
			return err
		}
		defer db.Rollback(tx)
		if err := f(tx); err != nil {
			return err
		}
		if rollback {
			return nil
		}
		return db.HangarCommitError(tx.Commit())
	}
	reserve := func(tx db.Tx) error { return port.ReserveInputPublication(in.Ctx, tx, stage, nonce) }
	if mode == "policy at risk" {
		if err := transact(false, func(tx db.Tx) error {
			return repository.RecordPolicyAttestation(in.Ctx, tx, output.PolicySnapshot{ProtocolVersion: output.ProtocolVersion, ActivationEpoch: stage.ActivationEpoch, BucketFingerprint: "gs://brine-output", Metageneration: 4, PolicyHash: "unsafe-input-policy", LifecycleDeleteRules: 1, State: output.PolicyAtRisk, ObservedAt: output.NewTimestamp(time.Now())}, nil)
		}); err != nil {
			return err
		}
		if err := transact(false, reserve); !errors.Is(err, output.ErrAtRisk) {
			return fmt.Errorf("unsafe policy did not refuse upload intent: %v", err)
		}
		return assertInputObjectCount(in, 0)
	}
	if mode == "reservation late commit" {
		err := transact(false, func(tx db.Tx) error {
			if err := reserve(tx); err != nil {
				return err
			}
			<-time.After(time.Until(stage.ExpiresAt.Time) + 100*time.Millisecond)
			return nil
		})
		if !errors.Is(err, output.ErrConflict) {
			return fmt.Errorf("expired reservation commit was not refused: %v", err)
		}
		var count int
		if err := jdb.Conn.QueryRow(`SELECT count(*) FROM hangar_input_publications`).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("expired reservation commit retained a row")
		}
		return assertInputObjectCount(in, 0)
	}
	if mode != "missing reservation" {
		if err := transact(mode == "reservation rollback", reserve); err != nil {
			return fmt.Errorf("reserve upload: %w", err)
		}
	}
	if mode == "reservation replay" {
		if err := transact(false, reserve); err != nil {
			return fmt.Errorf("reserve replay: %w", err)
		}
	}
	if mode == "conflicting reservation" {
		if err := transact(false, func(tx db.Tx) error { return port.ReserveInputPublication(in.Ctx, tx, stage, uuid.NewString()) }); !errors.Is(err, output.ErrConflict) {
			return fmt.Errorf("reservation did not refuse a different nonce: %v", err)
		}
	}
	if mode == "database nonce mutation" {
		err := transact(false, func(tx db.Tx) error {
			_, err := tx.ExecContext(in.Ctx, `UPDATE hangar_input_publications SET nonce=$2 WHERE reservation_id=$1`, string(stage.ReservationID), uuid.NewString())
			return db.HangarCommitError(err)
		})
		if !errors.Is(err, output.ErrConflict) {
			return fmt.Errorf("database nonce mutation was not refused: %v", err)
		}
	}
	publication, err := node.PublishInput(in.Ctx, stage, nonce, verifier)
	if err != nil {
		return fmt.Errorf("publish real upload: %w", err)
	}
	if mode == "pending adoption shield" {
		marker, err := output.ParseObjectMarker(publication.Marker)
		if err != nil {
			return err
		}
		var outcome output.AdoptionOutcome
		err = transact(false, func(tx db.Tx) error {
			var adoptErr error
			outcome, adoptErr = repository.AdoptManagedOrphan(in.Ctx, tx, output.AdoptionRequest{ProtocolVersion: output.ProtocolVersion, ActivationEpoch: stage.ActivationEpoch, Ref: publication.Attributes.Ref, Metageneration: publication.Metageneration, Marker: marker, CreatedAt: publication.Attributes.CreatedAt, Grace: output.MaxCaptureDeadline + output.PublicationGraceMargin, SafetyMargin: output.PublicationGraceMargin})
			return adoptErr
		})
		if err == nil || outcome != output.AdoptionProtectedByReservation {
			return fmt.Errorf("pending upload did not protect adoption: %s, %v", outcome, err)
		}
	}
	register := func(tx db.Tx) error { return port.RegisterInputPublication(in.Ctx, tx, publication, verifier) }
	if mode == "pending reclaim shield" || mode == "reclaim first" {
		if err := transact(false, register); err != nil {
			return err
		}
		reclaim := func(tx db.Tx) error {
			return repository.AdmitReclaim(in.Ctx, tx, publication.Attributes.Ref, uuid.NewString(), publication.Metageneration, 20*time.Minute, time.Microsecond)
		}
		if mode == "reclaim first" {
			if err := transact(false, reclaim); err != nil {
				return fmt.Errorf("admit prior reclaim: %w", err)
			}
		}
		stage, err = node.StageInput(in.Ctx, executioncontrol.NodeUID(in.NodeUID), bytes.NewReader(archive))
		if err != nil {
			return err
		}
		nonce = uuid.NewString()
		if err := transact(false, reserve); err != nil {
			return err
		}
		if mode == "pending reclaim shield" {
			if err := transact(false, reclaim); !errors.Is(err, output.ErrConflict) {
				return fmt.Errorf("reclaim did not refuse pending input publication: %v", err)
			}
		}
		publication, err = node.PublishInput(in.Ctx, stage, nonce, verifier)
		if err != nil {
			return err
		}
	}
	switch mode {
	case "changed nonce":
		publication.Nonce = uuid.NewString()
	case "changed generation":
		publication.Attributes.Ref.Generation++
	case "changed node":
		publication.Stage.NodeUID = executioncontrol.NodeUID(uuid.NewString())
	case "changed signature":
		publication.Signature = "invalid"
	case "expired reservation":
		<-time.After(time.Until(stage.ExpiresAt.Time) + 100*time.Millisecond)
	}
	claim := output.ClaimAcquisition{ProtocolVersion: output.ProtocolVersion, ClaimID: output.ClaimID(uuid.NewString()), Ref: publication.Attributes.Ref, ConsumerBindingID: output.OpaqueID(uuid.NewString()), RequestedAt: output.NewTimestamp(time.Now())}
	registerAndClaim := func(tx db.Tx) error {
		if err := register(tx); err != nil {
			return err
		}
		if err := repository.AcquireClaim(in.Ctx, tx, claim); err != nil {
			return err
		}
		if mode == "commit after deadline" {
			<-time.After(time.Until(stage.ExpiresAt.Time) + 100*time.Millisecond)
		}
		return nil
	}
	registrationErr := transact(mode == "registration rollback", registerAndClaim)
	refused := mode == "missing reservation" || mode == "reservation rollback" || mode == "changed nonce" || mode == "changed generation" || mode == "changed node" || mode == "changed signature" || mode == "expired reservation" || mode == "reclaim first" || mode == "commit after deadline"
	if refused {
		if !errors.Is(registrationErr, output.ErrConflict) && !errors.Is(registrationErr, output.ErrUnauthorized) && !errors.Is(registrationErr, output.ErrNotFound) && !errors.Is(registrationErr, output.ErrCorrupt) {
			return fmt.Errorf("registration did not refuse %s: %v", mode, registrationErr)
		}
	} else if registrationErr != nil {
		return fmt.Errorf("registration refused %s: %w", mode, registrationErr)
	}
	var claims, registrations int
	counts := func() error {
		return jdb.Conn.QueryRow(`SELECT (SELECT count(*) FROM hangar_claims WHERE claim_id=$1), (SELECT count(*) FROM hangar_input_publications WHERE reservation_id=$2 AND lifecycle_id IS NOT NULL)`, string(claim.ClaimID), string(stage.ReservationID)).Scan(&claims, &registrations)
	}
	if err := counts(); err != nil {
		return err
	}
	if refused || mode == "registration rollback" {
		if claims != 0 || registrations != 0 {
			return fmt.Errorf("refused or rolled-back registration retained %d claims and %d registrations", claims, registrations)
		}
	} else if claims != 1 || registrations != 1 {
		return fmt.Errorf("registration lost ownership: %d claims, %d registrations", claims, registrations)
	}
	if mode == "registration rollback" || mode == "registration replay" {
		if err := transact(false, registerAndClaim); err != nil {
			return fmt.Errorf("registration retry: %w", err)
		}
		if err := counts(); err != nil {
			return err
		}
		if claims != 1 || registrations != 1 {
			return fmt.Errorf("retry duplicated or lost ownership")
		}
	}
	if mode == "database receipt mutation" {
		err := transact(false, func(tx db.Tx) error {
			_, err := tx.ExecContext(in.Ctx, `UPDATE hangar_input_publications SET publication=publication || '{"signature":"changed"}'::jsonb WHERE reservation_id=$1`, string(stage.ReservationID))
			return db.HangarCommitError(err)
		})
		if !errors.Is(err, output.ErrConflict) {
			return fmt.Errorf("database receipt mutation was not refused: %v", err)
		}
	}
	return assertInputObjectCount(in, 1)
}

func inputPublicationVerifier(in HangarDaemon) (*output.ReceiptSignatureVerifier, error) {
	ring, err := output.NewReceiptKeyRing(output.EpochKey{KeyID: hangarReceiptKeyID, Epoch: executioncontrol.ActivationEpoch(hangarEpoch), PublicKey: in.ReceiptPublic, ValidFrom: output.NewTimestamp(time.Now().Add(-time.Hour)), ValidUntil: output.NewTimestamp(time.Now().Add(time.Hour))})
	if err != nil {
		return nil, err
	}
	return output.NewReceiptSignatureVerifier(ring, output.ClockFunc(time.Now))
}
