package db_test

import (
	"context"
	"time"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/output"
)

// The at-risk matrix: the exact set of operations that stop from detection
// onward, and the exact set that does not.
//
// Req 52 names five admissions that stop and three things that continue, and
// the value of this file is that both halves are asserted. A suite that only
// proved the refusals would pass for a plane that stopped doing anything at all
// the moment a monitor blinked -- which is the failure mode an operator cannot
// diagnose, because diagnosis is one of the things that has to keep working.
var _ = Describe("the lifetime-policy admission gate", func() {
	var (
		ctx        context.Context
		repository *db.HangarOutputRepository
	)

	begin := func() db.HangarOutputTx {
		GinkgoHelper()
		tx, err := dbConn.Begin()
		Expect(err).NotTo(HaveOccurred())

		return db.HangarOutputTx{Tx: tx}
	}

	in := func(work func(tx db.HangarOutputTx)) {
		GinkgoHelper()
		tx := begin()
		defer db.Rollback(tx)
		work(tx)
		Expect(tx.Commit()).To(Succeed())
	}

	// attest records one snapshot through the production repository.
	attest := func(state output.PolicyState, rules int, observedAt time.Time) {
		GinkgoHelper()
		in(func(tx db.HangarOutputTx) {
			Expect(repository.RecordPolicySnapshot(ctx, tx, output.PolicySnapshot{
				ProtocolVersion:      output.ProtocolVersion,
				ActivationEpoch:      1,
				BucketFingerprint:    "gs://output-bucket",
				Metageneration:       9,
				PolicyHash:           "sha256:" + string(state),
				LifecycleDeleteRules: rules,
				State:                state,
				ObservedAt:           output.NewTimestamp(observedAt),
			})).To(Succeed())
		})
	}

	// admissionRefused runs one admission to COMMIT, because the gate is a
	// deferred constraint trigger: a refusal that arrived statement by
	// statement would be a different gate from the one production has.
	commitOf := func(work func(tx db.HangarOutputTx)) error {
		GinkgoHelper()
		tx := begin()
		if err := func() error {
			defer func() { _ = recover() }()
			work(tx)

			return nil
		}(); err != nil {
			db.Rollback(tx)

			return err
		}
		err := tx.Commit()
		db.Rollback(tx)

		return err
	}

	// forgetAttestations removes every snapshot for the epoch.
	//
	// The gate reads the FRESHEST observation, so "the monitor last spoke an
	// hour ago" cannot be arranged by adding an old row beside a new one -- the
	// new one is still the freshest and is still what admits. What has to be
	// arranged is a deployment whose most recent reading is old, which is this.
	forgetAttestations := func() {
		GinkgoHelper()
		_, err := dbConn.Exec(`DELETE FROM hangar_policy_snapshots WHERE activation_epoch = 1`)
		Expect(err).NotTo(HaveOccurred())
	}

	settled := func(digest hangar.Digest, generation int64) HangarCapture {
		GinkgoHelper()
		capture := hangarPublishAt(ctx, repository, digest, generation,
			output.NewTimestamp(time.Now().Add(output.DefaultCaptureDeadline)))
		hangarReleaseSource(ctx, repository, capture)

		return capture
	}

	BeforeEach(func() {
		ctx = context.Background()
		dbConn.SetMaxOpenConns(3)
		DeferCleanup(func() { dbConn.SetMaxOpenConns(1) })

		consumer, err := db.HangarConsumerPrefixHeld("policy-spec")
		Expect(err).NotTo(HaveOccurred())
		repository = db.NewHangarOutputRepository(consumer)

		hangarActivateEpoch(ctx, repository)
	})

	Describe("what stops from detection onward", func() {
		var (
			ref     hangar.TreeRef
			capture HangarCapture
		)

		BeforeEach(func() {
			// Everything the refusals will need is built while the policy is
			// still safe, so a red row names the gate rather than the fixture.
			capture = settled(hangarDigest(80), 1725830823000080)
			ref = capture.Ref
			attest(output.PolicyAtRisk, 1, time.Now())
		})

		It("refuses a new capture's predeclaration", func() {
			err := commitOf(func(tx db.HangarOutputTx) {
				Expect(repository.PredeclareHandoff(ctx, tx, output.CaptureAdmission{
					ProtocolVersion: output.ProtocolVersion,
					Execution:       hangarIdentity(),
					ActivationEpoch: 1,
					HandoffID:       output.HandoffID(uuid.NewString()),
					SourceHoldID:    output.SourceHoldID(uuid.NewString()),
					Output:          output.OutputName("result"),
					CaptureDeadline: output.NewTimestamp(
						time.Now().Add(output.DefaultCaptureDeadline)),
				})).To(Succeed())
			})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("at_risk"))
		})

		It("refuses a claim acquire", func() {
			err := commitOf(func(tx db.HangarOutputTx) {
				Expect(repository.AcquireClaim(ctx, tx, output.ClaimAcquisition{
					ProtocolVersion:   output.ProtocolVersion,
					ClaimID:           output.ClaimID(uuid.NewString()),
					Ref:               ref,
					ConsumerBindingID: "binding-at-risk",
					RequestedAt:       output.NewTimestamp(time.Now()),
				})).To(Succeed())
			})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("at_risk"))
		})

		It("refuses orphan adoption", func() {
			orphan := hangar.TreeRef{
				Scope: "team-a", Digest: hangarDigest(81), Generation: 1725830823000081,
			}
			err := commitOf(func(tx db.HangarOutputTx) {
				outcome, err := repository.AdoptManagedOrphan(ctx, tx,
					hangarAdoptionFor(orphan, time.Now().Add(-30*24*time.Hour)))
				Expect(err).NotTo(HaveOccurred())
				Expect(outcome).To(Equal(output.AdoptionAdopted))
			})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("at_risk"))
		})

		It("refuses reclaim admission", func() {
			hangarAgeCapture(capture, 48*time.Hour)
			hangarAgePublication(ref, hangarGraceElapsed)
			err := commitOf(func(tx db.HangarOutputTx) {
				Expect(repository.AdmitReclaim(ctx, tx, ref, uuid.NewString(), 1,
					output.MinLeaseTerm,
					output.DefaultPublicationGrace)).To(Succeed())
			})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("at_risk"))
		})
	})

	Describe("what keeps working", func() {
		It("lets a terminal capture release its source and settle", func() {
			capture := hangarPublishAt(ctx, repository, hangarDigest(82), 1725830823000082,
				output.NewTimestamp(time.Now().Add(output.DefaultCaptureDeadline)))
			attest(output.PolicyAtRisk, 1, time.Now())

			// Releases remain possible. A plane that stopped releasing while
			// at risk would pin every producer's incarnation on its node for as
			// long as the monitor stayed unhappy.
			hangarReleaseSource(ctx, repository, capture)

			var settledAt bool
			Expect(dbConn.QueryRow(`
				SELECT settled_at IS NOT NULL FROM hangar_capture_reservations
				WHERE reservation_id = $1`, string(capture.ReservationID)).Scan(&settledAt)).
				To(Succeed())
			Expect(settledAt).To(BeTrue())
		})

		It("keeps existing claims recorded and lets them be released", func() {
			capture := settled(hangarDigest(83), 1725830823000083)
			claimID := output.ClaimID(uuid.NewString())
			in(func(tx db.HangarOutputTx) {
				Expect(repository.AcquireClaim(ctx, tx, output.ClaimAcquisition{
					ProtocolVersion:   output.ProtocolVersion,
					ClaimID:           claimID,
					Ref:               capture.Ref,
					ConsumerBindingID: "binding-existing",
					RequestedAt:       output.NewTimestamp(time.Now()),
				})).To(Succeed())
			})

			attest(output.PolicyAtRisk, 1, time.Now())

			// Diagnosis: the claim is still readable, which is how an operator
			// finds out what is protected.
			in(func(tx db.HangarOutputTx) {
				claims, err := repository.ReadClaims(ctx, tx, capture.Ref)
				Expect(err).NotTo(HaveOccurred())
				Expect(claims).To(HaveLen(1))
			})

			// And it can be released. A release is not a new protection.
			in(func(tx db.HangarOutputTx) {
				Expect(repository.ReleaseClaim(ctx, tx, output.ClaimRelease{
					ProtocolVersion: output.ProtocolVersion,
					ClaimID:         claimID,
					Ref:             capture.Ref,
					RequestedAt:     output.NewTimestamp(time.Now()),
				})).To(Succeed())
			})
		})

		It("lets an attestor keep recording, which is how recovery happens at all", func() {
			attest(output.PolicyAtRisk, 1, time.Now())
			attest(output.PolicySafe, 0, time.Now())

			// A fresh safe attestation reopens admission. It does NOT erase an
			// unresolved violation -- that reconciliation is the operator's --
			// but the gate reads the latest snapshot, so the plane can work
			// again once the bucket is provably safe.
			capture := settled(hangarDigest(84), 1725830823000084)
			in(func(tx db.HangarOutputTx) {
				Expect(repository.AcquireClaim(ctx, tx, output.ClaimAcquisition{
					ProtocolVersion:   output.ProtocolVersion,
					ClaimID:           output.ClaimID(uuid.NewString()),
					Ref:               capture.Ref,
					ConsumerBindingID: "binding-recovered",
					RequestedAt:       output.NewTimestamp(time.Now()),
				})).To(Succeed())
			})
		})
	})

	Describe("whose clock an attestation is dated by", func() {
		// THE FRESHNESS BOUND COMPARED TWO CLOCKS. The admission gate asks
		// whether now() - observed_at is inside fifteen minutes; now() is
		// PostgreSQL's and observed_at was stamped by the attestor process. An
		// attestor running fast therefore made stale evidence look fresh for
		// as long as its clock was wrong, which is the whole of the bounded-
		// staleness promise defeated by NTP.
		record := func(observedAt time.Time) error {
			GinkgoHelper()
			tx := begin()
			defer db.Rollback(tx)

			if err := repository.RecordPolicyAttestation(ctx, tx, output.PolicySnapshot{
				ProtocolVersion:      output.ProtocolVersion,
				ActivationEpoch:      1,
				BucketFingerprint:    "gs://output-bucket",
				Metageneration:       9,
				PolicyHash:           "sha256:safe",
				LifecycleDeleteRules: 0,
				State:                output.PolicySafe,
				ObservedAt:           output.NewTimestamp(observedAt),
			}, nil); err != nil {
				return err
			}

			return tx.Commit()
		}
		observedAtOf := func() time.Time {
			GinkgoHelper()
			var observed time.Time
			Expect(dbConn.QueryRow(`SELECT observed_at FROM hangar_policy_snapshots
				ORDER BY id DESC LIMIT 1`).Scan(&observed)).To(Succeed())

			return observed
		}

		It("does not let an attestor's fast clock buy freshness it did not earn", func() {
			Expect(record(time.Now().Add(5 * time.Minute))).To(Succeed())

			var future bool
			Expect(dbConn.QueryRow(`SELECT observed_at > now() FROM hangar_policy_snapshots
				ORDER BY id DESC LIMIT 1`).Scan(&future)).To(Succeed())
			Expect(future).To(BeFalse(),
				"an observation is dated after the transaction that recorded it, so an attestor "+
					"whose clock runs fast makes evidence the gate reads as fresher than it is")
		})

		It("refuses a reading from a clock that is wrong by more than the bound", func() {
			err := record(time.Now().Add(2 * time.Hour))
			Expect(err).To(MatchError(output.ErrIncomplete))
			Expect(err.Error()).To(ContainSubstring("ahead of the database clock"),
				"a two-hour skew was silently corrected, so the attestor stays wrong about "+
					"every other instant it stamps and nobody is told")
		})

		It("keeps an observation that really was made earlier", func() {
			// The non-vacuity. Without this the two above would pass against a
			// writer that stamped now() and discarded the attestor's reading,
			// which would make a hung pass's hour-old evidence look current --
			// the same defect in the other direction.
			earlier := time.Now().Add(-9 * time.Minute)
			Expect(record(earlier)).To(Succeed())
			Expect(observedAtOf()).To(BeTemporally("~", earlier, time.Second),
				"the stored observation is not the one the attestor made; a reading taken nine "+
					"minutes ago was recorded as current")
		})
	})

	Describe("what an attestation records", func() {
		It("keeps the findings when the snapshot they came from is superseded", func() {
			finding := output.PolicyFinding{
				Violation: output.ViolationLifecycleDeleteRule,
				Subject:   "gs://output-bucket",
				Detail:    "lifecycle rule \"Delete\" with condition \"age>1\" can remove an object",
			}

			in(func(tx db.HangarOutputTx) {
				Expect(repository.RecordPolicyAttestation(ctx, tx, output.PolicySnapshot{
					ProtocolVersion:      output.ProtocolVersion,
					ActivationEpoch:      1,
					BucketFingerprint:    "gs://output-bucket",
					Metageneration:       9,
					PolicyHash:           "sha256:unsafe",
					LifecycleDeleteRules: 1,
					State:                output.PolicyAtRisk,
					ObservedAt:           output.NewTimestamp(time.Now()),
				}, []output.PolicyFinding{finding})).To(Succeed())
			})

			// A monitor running every fifteen minutes against a bucket nobody
			// has fixed leaves ONE row, not one per pass.
			for pass := 0; pass < 3; pass++ {
				in(func(tx db.HangarOutputTx) {
					Expect(repository.RecordPolicyAttestation(ctx, tx, output.PolicySnapshot{
						ProtocolVersion:      output.ProtocolVersion,
						ActivationEpoch:      1,
						BucketFingerprint:    "gs://output-bucket",
						Metageneration:       9,
						PolicyHash:           "sha256:unsafe",
						LifecycleDeleteRules: 1,
						State:                output.PolicyAtRisk,
						ObservedAt:           output.NewTimestamp(time.Now()),
					}, []output.PolicyFinding{finding})).To(Succeed())
				})
			}

			in(func(tx db.HangarOutputTx) {
				open, err := repository.OpenPolicyViolations(ctx, tx, 1)
				Expect(err).NotTo(HaveOccurred())
				Expect(open).To(HaveLen(1))
				Expect(open[0].Violation).To(Equal(output.ViolationLifecycleDeleteRule))
			})

			// A fresh SAFE attestation reopens admission and does NOT erase the
			// violation: recovery needs reconciliation as well, and re-attesting
			// alone is a bucket somebody may have fixed and may not have.
			attest(output.PolicySafe, 0, time.Now())
			in(func(tx db.HangarOutputTx) {
				open, err := repository.OpenPolicyViolations(ctx, tx, 1)
				Expect(err).NotTo(HaveOccurred())
				Expect(open).To(HaveLen(1),
					"a fresh safe attestation erased an unresolved violation on its own")
			})

			// Reconciliation closes it, once, and never reopens it.
			in(func(tx db.HangarOutputTx) {
				Expect(repository.ReconcilePolicyViolation(ctx, tx, 1,
					output.ViolationLifecycleDeleteRule, "gs://output-bucket")).To(Succeed())
			})
			in(func(tx db.HangarOutputTx) {
				open, err := repository.OpenPolicyViolations(ctx, tx, 1)
				Expect(err).NotTo(HaveOccurred())
				Expect(open).To(BeEmpty())
			})

			_, err := dbConn.Exec(`
				UPDATE hangar_policy_violations SET resolved_at = NULL
				 WHERE activation_epoch = 1`)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("cannot be reopened"))
		})
	})

	Describe("staleness", func() {
		It("refuses admission on evidence past the detection bound, however safe it said it was", func() {
			capture := settled(hangarDigest(85), 1725830823000085)

			// A SAFE snapshot, observed too long ago, and nothing fresher.
			// "We have not checked" and "the check failed" are the same amount
			// of evidence.
			forgetAttestations()
			attest(output.PolicySafe, 0, time.Now().Add(-output.MaxPolicyEvidenceAge-time.Minute))

			err := commitOf(func(tx db.HangarOutputTx) {
				Expect(repository.AcquireClaim(ctx, tx, output.ClaimAcquisition{
					ProtocolVersion:   output.ProtocolVersion,
					ClaimID:           output.ClaimID(uuid.NewString()),
					Ref:               capture.Ref,
					ConsumerBindingID: "binding-stale",
					RequestedAt:       output.NewTimestamp(time.Now()),
				})).To(Succeed())
			})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("detection bound"))

			// The control: a fresh reading of the SAME safe policy admits.
			attest(output.PolicySafe, 0, time.Now())
			in(func(tx db.HangarOutputTx) {
				Expect(repository.AcquireClaim(ctx, tx, output.ClaimAcquisition{
					ProtocolVersion:   output.ProtocolVersion,
					ClaimID:           output.ClaimID(uuid.NewString()),
					Ref:               capture.Ref,
					ConsumerBindingID: "binding-fresh",
					RequestedAt:       output.NewTimestamp(time.Now()),
				})).To(Succeed())
			})
		})

		It("refuses a safe snapshot that admits it saw a removal rule", func() {
			tx := begin()
			defer db.Rollback(tx)
			err := repository.RecordPolicySnapshot(ctx, tx, output.PolicySnapshot{
				ProtocolVersion:      output.ProtocolVersion,
				ActivationEpoch:      1,
				BucketFingerprint:    "gs://output-bucket",
				Metageneration:       9,
				PolicyHash:           "sha256:contradictory",
				LifecycleDeleteRules: 1,
				State:                output.PolicySafe,
				ObservedAt:           output.NewTimestamp(time.Now()),
			})
			Expect(err).To(HaveOccurred(),
				"a snapshot that is safe AND counts a removal rule was accepted; the two halves "+
					"of one reading contradict each other and the safe half is the one a gate "+
					"would believe")
		})
	})
})
