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
	"crypto/rand"
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

// hangarActivateEpoch puts the output plane in service, as the web's startup
// write does.
func hangarActivateEpoch(ctx context.Context, repository *db.HangarOutputRepository) {
	GinkgoHelper()
	_, err := db.SetHangarEnabled(ctx, dbConn, true)
	Expect(err).NotTo(HaveOccurred())
}

func hangarIdentity() executioncontrol.Identity {
	return executioncontrol.Identity{
		ExecutionID: executioncontrol.ExecutionID(uuid.NewString()),
		Fence:       1,
	}
}

// HangarCapture is one whole capture's identities, so a spec that needs to
// settle it, release it or correlate an orphan against it does not have to go
// looking for them in the rows.
type HangarCapture struct {
	Key       output.CaptureKey
	Execution executioncontrol.Identity
	Ref       hangar.TreeRef
	Deadline  time.Duration
}

// hangarPublish drives one whole capture to published, with the default
// 24-hour deadline, and gives the capture's own claim back, as a consumer
// that did not select it would: what the specs that use it want is a
// published generation nothing protects.
func hangarPublish(ctx context.Context, repository *db.HangarOutputRepository, digest hangar.Digest, generation int64) (output.CaptureKey, hangar.TreeRef) {
	GinkgoHelper()
	capture := hangarPublishAt(ctx, repository, digest, generation, output.DefaultCaptureDeadline)
	hangarReleaseCaptureClaim(ctx, repository, capture)

	return capture.Key, capture.Ref
}

// hangarCaptureTx runs one statement group in its own committed transaction.
func hangarCaptureTx(body func(tx db.Tx)) {
	GinkgoHelper()
	tx, err := dbConn.Begin()
	Expect(err).NotTo(HaveOccurred())
	defer db.Rollback(tx)
	body(tx)
	Expect(db.HangarOutputTx{Tx: tx}.Commit()).To(Succeed())
}

// hangarPending inserts a pending capture under a named deadline term.
func hangarPending(ctx context.Context, repository *db.HangarOutputRepository, deadline time.Duration) HangarCapture {
	GinkgoHelper()

	capture := HangarCapture{Execution: hangarIdentity(), Deadline: deadline}
	capture.Key = output.CaptureKey{ExecutionID: capture.Execution.ExecutionID, Output: "result"}
	hangarCaptureTx(func(tx db.Tx) {
		_, err := repository.InsertPending(ctx, tx, output.PendingCapture{
			Execution: capture.Execution, Output: capture.Key.Output,
			Node: "node-a", NodeUID: "node-uid", Term: deadline,
		})
		Expect(err).NotTo(HaveOccurred())
	})

	return capture
}

// hangarReserve drives a capture as far as publishing and stops there: the
// digest is written and no generation is known for it, which is the state
// reclaim admission and adoption must treat as protecting its correlation.
func hangarReserve(ctx context.Context, repository *db.HangarOutputRepository, digest hangar.Digest, deadline time.Duration) HangarCapture {
	GinkgoHelper()

	capture := hangarPending(ctx, repository, deadline)
	hangarCaptureTx(func(tx db.Tx) {
		_, err := repository.CASPendingToPublishing(ctx, tx, capture.Key, "pod-a", "team-a", digest)
		Expect(err).NotTo(HaveOccurred())
	})
	capture.Ref = hangar.TreeRef{Scope: "team-a", Digest: digest}

	return capture
}

// hangarPublishAt drives a whole capture to published under a named capture
// deadline. The capture keeps its own claim.
func hangarPublishAt(ctx context.Context, repository *db.HangarOutputRepository, digest hangar.Digest, generation int64, deadline time.Duration) HangarCapture {
	GinkgoHelper()

	capture := hangarReserve(ctx, repository, digest, deadline)
	capture.Ref.Generation = generation
	hangarCaptureTx(func(tx db.Tx) {
		_, err := repository.CASPublishingToPublished(ctx, tx, output.PublishedCapture{
			Key: capture.Key, Generation: generation, Metageneration: 1, ActivationEpoch: 1,
		})
		Expect(err).NotTo(HaveOccurred())
	})

	return capture
}

// hangarReleaseCaptureClaim gives back the claim a capture's publication took.
func hangarReleaseCaptureClaim(ctx context.Context, repository *db.HangarOutputRepository, capture HangarCapture) {
	GinkgoHelper()
	hangarCaptureTx(func(tx db.Tx) {
		Expect(repository.ReleaseClaim(ctx, tx, output.ClaimRelease{
			ProtocolVersion: output.ProtocolVersion, ClaimID: capture.Key.ClaimID(), Ref: capture.Ref,
			RequestedAt: output.NewTimestamp(time.Now()),
		})).To(Succeed())
	})
}

// hangarReleaseSource is step 6: the node cleared the marker.
func hangarReleaseSource(ctx context.Context, repository *db.HangarOutputRepository, capture HangarCapture) {
	GinkgoHelper()
	hangarCaptureTx(func(tx db.Tx) {
		_, err := repository.SetReleased(ctx, tx, capture.Key)
		Expect(err).NotTo(HaveOccurred())
	})
}

// readLeaseRequest is a well-formed managed-read admission: an exact stat
// taken a moment ago, a destination that is a handle and a volume, and a
// nonce minted once for this lease. Every refusal spec below starts from
// this and changes exactly one thing, so a red row names the check rather
// than "a lease was refused".
func hangarReadLeaseRequest(id output.ReadLeaseID, claimID output.ClaimID, ref hangar.TreeRef) output.ReadLeaseRequest {
	GinkgoHelper()
	nonce, err := output.NewReadWarrantNonce(rand.Reader)
	Expect(err).NotTo(HaveOccurred())

	// The marker on a real stat carries the capture that created the object.
	// The fixture reads it back rather than inventing one, so a well-formed
	// stat proof here is the shape production actually observes.
	var execution, outputName string
	Expect(dbConn.QueryRow(`
		SELECT execution_id, output_name FROM hangar_captures WHERE scope = $1 AND digest = $2
		ORDER BY created_at LIMIT 1`,
		string(ref.Scope), string(ref.Digest)).Scan(&execution, &outputName)).To(Succeed())
	reservation := output.CaptureKey{ExecutionID: executioncontrol.ExecutionID(execution),
		Output: output.OutputName(outputName)}.MarkerID()

	return output.ReadLeaseRequest{
		ReadLeaseID:            id,
		ClaimID:                claimID,
		Ref:                    ref,
		ActivationEpoch:        1,
		RequestedAt:            output.NewTimestamp(time.Now()),
		MaterializationTimeout: 10 * time.Minute,
		Destination:            output.ReadDestination{Handle: "task-handle", Volume: "input-0"},
		WarrantNonce:           nonce,
		StatProof: output.PublishedObject{
			Attributes: hangar.TreeAttributes{
				Ref: ref, StoredBytes: 1024, LogicalBytes: 4096,
				CreatedAt: time.Now().Add(-time.Minute),
			},
			Metageneration: 1,
			Marker: output.ObjectMarker{
				Version:         output.MarkerVersion,
				Scope:           ref.Scope,
				Digest:          ref.Digest,
				ReservationID:   reservation,
				ActivationEpoch: 1,
				CreatedAt:       output.NewTimestamp(time.Now().Add(-time.Minute)),
			},
		},
		StatObservedAt: output.NewTimestamp(time.Now()),
	}
}

// hangarAgeCapture moves one capture's creation and deadline into the past.
//
// It suspends the capture row's transition guard to do it, and that is the
// honest shape: the deadline is immutable by design, so the only ways to reach
// "the capture deadline has passed" are to wait or to say plainly that the
// fixture is moving a clock.
func hangarAgeCapture(capture HangarCapture, by time.Duration) {
	GinkgoHelper()

	interval := fmt.Sprintf("%d seconds", int64(by.Seconds()))
	_, err := dbConn.Exec(`ALTER TABLE hangar_captures DISABLE TRIGGER hangar_capture_transition_guard`)
	Expect(err).NotTo(HaveOccurred())
	defer func() {
		_, err := dbConn.Exec(`ALTER TABLE hangar_captures ENABLE TRIGGER hangar_capture_transition_guard`)
		Expect(err).NotTo(HaveOccurred())
	}()

	result, err := dbConn.Exec(`
		UPDATE hangar_captures
		   SET created_at = created_at - $3::interval,
		       capture_deadline_at = capture_deadline_at - $3::interval
		 WHERE execution_id = $1 AND output_name = $2`,
		string(capture.Key.ExecutionID), string(capture.Key.Output), interval)
	Expect(err).NotTo(HaveOccurred())
	Expect(result.RowsAffected()).To(BeEquivalentTo(1))
}

// hangarAgePublication moves a lifecycle row into the past on the database
// clock.
//
// Elapsed publication grace is one of reclaim admission's seven preconditions
// and the default is eight days, so every spec that admits a reclamation has to
// arrange it. Moving the row is the honest form: what is being arranged is time
// passing, and the comparison the repository makes is still the database's own
// against the row's own registered_at.
func hangarAgePublication(ref hangar.TreeRef, by time.Duration) {
	GinkgoHelper()

	_, err := dbConn.Exec(`
		UPDATE hangar_exact_lifecycles SET registered_at = registered_at - $4::interval
		 WHERE scope = $1 AND digest = $2 AND generation = $3`,
		string(ref.Scope), string(ref.Digest), ref.Generation, by.Round(time.Second).String())
	Expect(err).NotTo(HaveOccurred())
}

// hangarGraceElapsed is the aging every admission spec applies: the default
// publication grace plus an hour, so the row is outside grace by a margin no
// clock skew can close.
const hangarGraceElapsed = output.DefaultPublicationGrace + time.Hour
