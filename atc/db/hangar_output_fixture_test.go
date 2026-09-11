package db_test

// The Hangar output capture fixture, at package level.
//
// It lives here rather than inside one Describe because a second file's specs
// need the same capture and a second spelling of a capture is a second set of
// facts. The guards under test are exactly about facts agreeing, so the
// fixture drives the repository rather than writing the rows itself: a
// fixture that inserted its own would be testing the schema twice and the
// repository not at all.

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// One activation epoch and one fresh, safe policy attestation: without
// both, nothing in the plane admits anything, which is the held state the
// migration leaves behind.
func hangarActivateEpoch(ctx context.Context, repository *db.HangarOutputRepository) {
	GinkgoHelper()
	_, err := dbConn.Exec(`
		INSERT INTO hangar_output_activation_epochs
			(epoch_id, base_state, output_state, base_attestation, output_attestation,
			 receipt_public_key_id, receipt_key_valid_from, receipt_key_valid_until,
			 materialization_key_id, bucket_fingerprint, derived_namespace)
		VALUES (1, 'enabled', 'enabled', '{}', '{}', 'receipt-key-1',
			now() - interval '1 day', now() + interval '30 days',
			'materialize-key-1', 'gs://output-bucket', 'deployment/ns')`)
	Expect(err).NotTo(HaveOccurred())

	tx, err := dbConn.Begin()
	Expect(err).NotTo(HaveOccurred())
	defer db.Rollback(tx)
	Expect(repository.RecordPolicySnapshot(ctx, tx, output.PolicySnapshot{
		ProtocolVersion:      output.ProtocolVersion,
		ActivationEpoch:      1,
		BucketFingerprint:    "gs://output-bucket",
		Metageneration:       3,
		PolicyHash:           "policy-hash-1",
		LifecycleDeleteRules: 0,
		State:                output.PolicySafe,
		ObservedAt:           output.NewTimestamp(time.Now()),
	})).To(Succeed())
	Expect(tx.Commit()).To(Succeed())
}

func hangarIdentity() executioncontrol.Identity {
	return executioncontrol.Identity{
		ExecutionID: executioncontrol.ExecutionID(uuid.NewString()),
		Fence:       1,
	}
}

func hangarHoldFor(handoff output.HandoffID, lease output.SourceLeaseID, execution executioncontrol.Identity, name output.OutputName) output.CaptureAcknowledgement {
	return output.CaptureAcknowledgement{
		ProtocolVersion: output.ProtocolVersion,
		Kind:            output.CaptureHoldAcknowledged,
		Execution:       execution,
		ActivationEpoch: 1,
		LedgerSequence:  1,
		NodeUID:         "node-uid",
		HandoffID:       handoff,
		SourceLeaseID:   lease,
		Incarnation: output.SourceIncarnation{
			ExecutionID:      execution.ExecutionID,
			NodeUID:          "node-uid",
			HandleGeneration: 1,
			Output:           name,
		},
		ObservedAt: output.NewTimestamp(time.Now()),
		Signature:  "signature",
	}
}

// A hold binds to an incarnation the daemon issued FIRST, and the schema
// now says so: the reservation exists before the producing Pod does, and a
// hold over no reservation is the Phase 4 seam -- a producer writing into a
// directory nothing protects. Every hold below reserves first, because
// every hold in production does.
func hangarReserveFor(handoff output.HandoffID, lease output.SourceLeaseID, execution executioncontrol.Identity, name output.OutputName) output.ReservedIncarnation {
	return output.ReservedIncarnation{
		ProtocolVersion: output.ProtocolVersion,
		Execution:       execution,
		ActivationEpoch: 1,
		HandoffID:       handoff,
		SourceLeaseID:   lease,
		NodeUID:         "node-uid",
		Incarnation: output.SourceIncarnation{
			ExecutionID:      execution.ExecutionID,
			NodeUID:          "node-uid",
			HandleGeneration: 1,
			Output:           name,
		},
		Directory:      string(execution.ExecutionID) + ".1/" + string(name),
		LedgerSequence: 1,
		ObservedAt:     output.NewTimestamp(time.Now()),
	}
}

func hangarFinishFor(execution executioncontrol.Identity) executioncontrol.Acknowledgement {
	return executioncontrol.Acknowledgement{
		ProtocolVersion: executioncontrol.ProtocolVersion,
		Kind:            executioncontrol.AcknowledgementFinish,
		Identity:        execution,
		ActivationEpoch: 1,
		LedgerSequence:  2,
		NodeUID:         "node-uid",
		ProcessIdentity: "process-1",
		ObservedAt:      output.NewTimestamp(time.Now()),
		Outcome:         &executioncontrol.ExitOutcome{ExitCode: 0},
		Signature:       "signature",
	}
}

// The one-use stat challenge the daemon would have been issued, for one
// exact ref. Written as SQL because minting it is not the repository's job.
func hangarIssueChallenge(handoff output.HandoffID, reservation output.ReservationID, ref hangar.TreeRef) (string, time.Time) {
	GinkgoHelper()

	nonce := "nonce-" + uuid.NewString()
	var issuedAt time.Time
	err := dbConn.QueryRow(`
		INSERT INTO hangar_receipt_stat_challenges
			(nonce, handoff_id, reservation_id, activation_epoch, receipt_public_key_id,
			 scope, digest, generation, capture_fence, not_after)
		VALUES ($1, $2, $3, 1, 'receipt-key-1', $4, $5, $6, 1, now() + interval '5 minutes')
		RETURNING issued_at`,
		nonce, string(handoff), string(reservation), string(ref.Scope), string(ref.Digest),
		ref.Generation).Scan(&issuedAt)
	Expect(err).NotTo(HaveOccurred())

	return nonce, issuedAt
}

// One receipt admission, in one place, because a second spelling of it is
// a second set of facts and the guards under test are exactly about facts
// agreeing.
func hangarAdmissionFor(handoff output.HandoffID, execution executioncontrol.Identity, reservation output.ReservationID, ref hangar.TreeRef, nonce string, issuedAt time.Time) output.ReceiptAdmission {
	name := output.OutputName("result")

	return output.ReceiptAdmission{
		ProtocolVersion: output.ProtocolVersion,
		Receipt: output.Receipt{
			Claims: output.ReceiptClaims{
				ProtocolVersion:      output.ProtocolVersion,
				ReceiptVersion:       output.ReceiptDomain,
				Execution:            execution,
				ActivationEpoch:      1,
				HandoffID:            handoff,
				ProducerCheckpointID: output.OpaqueID("checkpoint-" + string(handoff)),
				ReservationID:        reservation,
				ChallengeNonce:       nonce,
				ChallengeIssuedAt:    output.NewTimestamp(issuedAt),
				Incarnation: output.SourceIncarnation{
					ExecutionID:      execution.ExecutionID,
					NodeUID:          "node-uid",
					HandleGeneration: 1,
					Output:           name,
				},
				Output:        name,
				CaptureFence:  1,
				WriterFence:   1,
				Ref:           ref,
				Attributes:    output.AttributesFromFoundation(hangar.TreeAttributes{Ref: ref, StoredBytes: 2048, LogicalBytes: 4096, CreatedAt: time.Now()}),
				MarkerVersion: output.MarkerVersion,
				SignedAt:      output.NewTimestamp(time.Now()),
			},
			KeyID:     "receipt-key-1",
			Algorithm: output.ReceiptAlgorithm,
			Signature: "signature",
		},
		ChallengeNonce: nonce,
		Metageneration: 1,
		AdmittedAt:     output.NewTimestamp(time.Now()),
	}
}

// publish drives one whole capture, from predeclaration to a registered
// receipt, through the repository rather than around it: what is under test
// is the seam, and a fixture that wrote the rows itself would be testing
// the schema twice and the repository not at all.
// HangarCapture is one whole capture's identities, so a spec that needs to
// settle it, release its source or correlate an orphan against it does not have
// to go looking for them in the rows.
type HangarCapture struct {
	HandoffID     output.HandoffID
	SourceLeaseID output.SourceLeaseID
	Execution     executioncontrol.Identity
	Output        output.OutputName
	ReservationID output.ReservationID
	Ref           hangar.TreeRef
	Deadline      output.Timestamp
}

// hangarPublish drives one whole capture to a registered receipt, with the
// default 24-hour deadline.
func hangarPublish(ctx context.Context, repository *db.HangarOutputRepository, digest hangar.Digest, generation int64) (output.ReservationID, hangar.TreeRef) {
	GinkgoHelper()
	capture := hangarPublishAt(ctx, repository, digest, generation,
		output.NewTimestamp(time.Now().Add(output.DefaultCaptureDeadline)))

	return capture.ReservationID, capture.Ref
}

// hangarReserve drives a capture as far as its resolved logical reservation and
// stops there, which is the AC 14 gap: the logical (scope, digest) is reserved
// and no generation is registered for it.
func hangarReserve(ctx context.Context, repository *db.HangarOutputRepository, digest hangar.Digest, deadline output.Timestamp) HangarCapture {
	GinkgoHelper()

	capture := HangarCapture{
		HandoffID:     output.HandoffID(uuid.NewString()),
		SourceLeaseID: output.SourceLeaseID(uuid.NewString()),
		Execution:     hangarIdentity(),
		Output:        output.OutputName("result"),
		Deadline:      deadline,
	}

	tx, err := dbConn.Begin()
	Expect(err).NotTo(HaveOccurred())
	defer db.Rollback(tx)

	Expect(repository.PredeclareHandoff(ctx, tx, output.CaptureAdmission{
		ProtocolVersion: output.ProtocolVersion,
		Execution:       capture.Execution,
		ActivationEpoch: 1,
		HandoffID:       capture.HandoffID,
		SourceLeaseID:   capture.SourceLeaseID,
		Output:          capture.Output,
		CaptureDeadline: capture.Deadline,
	})).To(Succeed())
	Expect(repository.RecordSourceReservation(ctx, tx,
		hangarReserveFor(capture.HandoffID, capture.SourceLeaseID, capture.Execution,
			capture.Output), "node-a")).To(Succeed())
	Expect(repository.AcknowledgeSourceHold(ctx, tx,
		hangarHoldFor(capture.HandoffID, capture.SourceLeaseID, capture.Execution,
			capture.Output))).To(Succeed())

	capture.ReservationID, err = repository.CommitCaptureReservation(ctx, tx,
		output.SuccessfulFinishDisposition{
			ProtocolVersion:       output.ProtocolVersion,
			Disposition:           output.DispositionCapture,
			Execution:             capture.Execution,
			ActivationEpoch:       1,
			HandoffID:             capture.HandoffID,
			SourceLeaseID:         capture.SourceLeaseID,
			ProducerCheckpointID:  output.OpaqueID("checkpoint-" + string(capture.HandoffID)),
			Output:                capture.Output,
			CaptureFence:          1,
			CaptureDeadline:       capture.Deadline,
			FinishAcknowledgement: hangarFinishFor(capture.Execution),
		})
	Expect(err).NotTo(HaveOccurred())

	_, err = repository.AcquireCaptureLease(ctx, tx, capture.ReservationID, uuid.NewString(),
		output.MinLeaseTerm)
	Expect(err).NotTo(HaveOccurred())

	Expect(repository.ResolveLogicalReservation(ctx, tx, output.LogicalResolution{
		ProtocolVersion: output.ProtocolVersion,
		Execution:       capture.Execution,
		ActivationEpoch: 1,
		HandoffID:       capture.HandoffID,
		ReservationID:   capture.ReservationID,
		CaptureFence:    1,
		Scope:           "team-a",
		Digest:          digest,
		LogicalBytes:    4096,
		ResolvedAt:      output.NewTimestamp(time.Now()),
	})).To(Succeed())
	Expect(repository.RecordFirstObjectCreate(ctx, tx, capture.ReservationID, 1)).To(Succeed())
	Expect(tx.Commit()).To(Succeed())

	return capture
}

// hangarPublishAt drives a whole capture to a registered receipt under a named
// capture deadline, which is how a spec reaches the state adoption waits for
// without moving a row's deadline behind the production code's back.
func hangarPublishAt(ctx context.Context, repository *db.HangarOutputRepository, digest hangar.Digest, generation int64, deadline output.Timestamp) HangarCapture {
	GinkgoHelper()

	capture := hangarReserve(ctx, repository, digest, deadline)
	capture.Ref = hangar.TreeRef{Scope: "team-a", Digest: digest, Generation: generation}
	nonce, issuedAt := hangarIssueChallenge(capture.HandoffID, capture.ReservationID, capture.Ref)

	tx, err := dbConn.Begin()
	Expect(err).NotTo(HaveOccurred())
	defer db.Rollback(tx)
	Expect(repository.RegisterReceipt(ctx, tx, hangarAdmissionFor(capture.HandoffID,
		capture.Execution, capture.ReservationID, capture.Ref, nonce, issuedAt))).To(Succeed())
	Expect(tx.Commit()).To(Succeed())

	return capture
}

// hangarReleaseSource acknowledges the fenced release a registered capture still
// owes, which is what settles it. Req 40 wants the source released and not only
// the decision taken, so a spec that needs a settled capture says so here rather
// than stamping a column.
func hangarReleaseSource(ctx context.Context, repository *db.HangarOutputRepository, capture HangarCapture) {
	GinkgoHelper()

	var intent string
	Expect(dbConn.QueryRow(`
		SELECT release_intent_id FROM hangar_capture_reservations WHERE reservation_id = $1`,
		string(capture.ReservationID)).Scan(&intent)).To(Succeed())

	tx, err := dbConn.Begin()
	Expect(err).NotTo(HaveOccurred())
	defer db.Rollback(tx)
	Expect(repository.AcknowledgeCaptureRelease(ctx, tx, output.ReleaseAcknowledgement{
		ProtocolVersion: output.ProtocolVersion,
		Disposition:     output.DispositionCapture,
		Execution:       capture.Execution,
		ActivationEpoch: 1,
		HandoffID:       capture.HandoffID,
		SourceLeaseID:   capture.SourceLeaseID,
		ReleaseIntentID: output.ReleaseIntentID(intent),
		Incarnation: output.SourceIncarnation{
			ExecutionID:      capture.Execution.ExecutionID,
			NodeUID:          "node-uid",
			HandleGeneration: 1,
			Output:           capture.Output,
		},
		LedgerSequence: 3,
		ObservedAt:     output.NewTimestamp(time.Now()),
		Signature:      "signature",
	})).To(Succeed())
	Expect(tx.Commit()).To(Succeed())
}

// hangarAgeCapture moves one capture's deadline into the past.
//
// It suspends two guards to do it, and that is the honest shape rather than a
// convenience. The capture deadline is IMMUTABLE by design: the predeclaration
// refuses any change to it, and the Stage 2 constraint trigger refuses a
// reservation whose deadline does not match the predeclaration's. Between them
// they are Req 40's last sentence -- no reservation is extended past its
// original deadline merely to avoid collection -- and
// `hangar_output_adoption_test.go` proves both halves through production
// statements, with these guards ON.
//
// So the only ways to reach "the capture deadline has passed" are to wait an
// hour (the schema's own minimum) or to say plainly that the fixture is moving
// a clock. This does the second. Nothing in production writes this column at
// all after the predeclaration.
func hangarAgeCapture(capture HangarCapture, by time.Duration) {
	GinkgoHelper()

	interval := fmt.Sprintf("%d seconds", int64(by.Seconds()))

	for _, statement := range []string{
		`ALTER TABLE hangar_handoff_predeclarations DISABLE TRIGGER hangar_predeclaration_immutability_guard`,
		`ALTER TABLE hangar_capture_reservations DISABLE TRIGGER hangar_stage_two_matches_predeclaration`,
	} {
		_, err := dbConn.Exec(statement)
		Expect(err).NotTo(HaveOccurred())
	}
	defer func() {
		for _, statement := range []string{
			`ALTER TABLE hangar_handoff_predeclarations ENABLE TRIGGER hangar_predeclaration_immutability_guard`,
			`ALTER TABLE hangar_capture_reservations ENABLE TRIGGER hangar_stage_two_matches_predeclaration`,
		} {
			_, err := dbConn.Exec(statement)
			Expect(err).NotTo(HaveOccurred())
		}
	}()

	_, err := dbConn.Exec(`
		UPDATE hangar_handoff_predeclarations
		   SET created_at = created_at - $2::interval,
		       capture_deadline_at = capture_deadline_at - $2::interval
		 WHERE handoff_id = $1`, string(capture.HandoffID), interval)
	Expect(err).NotTo(HaveOccurred())

	result, err := dbConn.Exec(`
		UPDATE hangar_capture_reservations
		   SET capture_deadline_at = capture_deadline_at - $2::interval
		 WHERE reservation_id = $1`, string(capture.ReservationID), interval)
	Expect(err).NotTo(HaveOccurred())
	Expect(result.RowsAffected()).To(BeEquivalentTo(1))
}

// hangarAdoptionFor is a well-formed adoption request for one found object: a
// verified marker that describes it, the frozen grace and margin, and a
// creation time the caller chooses so a spec can be on either side of grace.
func hangarAdoptionFor(ref hangar.TreeRef, createdAt time.Time) output.AdoptionRequest {
	return output.AdoptionRequest{
		ProtocolVersion: output.ProtocolVersion,
		ActivationEpoch: 1,
		Ref:             ref,
		Metageneration:  1,
		Marker: output.ObjectMarker{
			Version:         output.MarkerVersion,
			Scope:           ref.Scope,
			Digest:          ref.Digest,
			ReservationID:   output.ReservationID("77777777-7777-4777-8777-777777777777"),
			ActivationEpoch: 1,
			CreatedAt:       output.NewTimestamp(createdAt),
		},
		CreatedAt:    output.NewTimestamp(createdAt),
		Grace:        output.DefaultPublicationGrace,
		SafetyMargin: output.PublicationGraceMargin,
	}
}
