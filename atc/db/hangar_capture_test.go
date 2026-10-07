package db_test

import (
	"context"
	"time"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

var _ = Describe("Hangar capture rows", func() {
	var (
		ctx        context.Context
		repository *db.HangarOutputRepository
		key        output.CaptureKey
	)

	digestOf := func(fill string) hangar.Digest {
		return hangar.Digest("sha256:" + fill + fill + fill + fill + fill + fill + fill + fill)
	}
	const scope = hangar.Scope("deployment-ns")

	// in runs one statement group in its own committed transaction, the way the
	// coordinator does: no lock is held between two of them.
	in := func(body func(tx db.Tx) error) error {
		tx, err := dbConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		defer db.Rollback(tx)
		if err := body(tx); err != nil {
			return err
		}
		return db.HangarOutputTx{Tx: tx}.Commit()
	}

	insert := func(k output.CaptureKey, term time.Duration) output.Capture {
		var capture output.Capture
		Expect(in(func(tx db.Tx) (err error) {
			capture, err = repository.InsertPending(ctx, tx, output.PendingCapture{
				Execution: executioncontrol.Identity{ExecutionID: k.ExecutionID, Fence: 1}, Output: k.Output,
				Node: "node-a", NodeUID: "node-uid-a", Term: term,
			})
			return err
		})).To(Succeed())
		return capture
	}

	get := func(k output.CaptureKey) output.Capture {
		var capture output.Capture
		Expect(in(func(tx db.Tx) (err error) {
			capture, err = repository.GetCapture(ctx, tx, k)
			return err
		})).To(Succeed())
		return capture
	}

	BeforeEach(func() {
		ctx = context.Background()
		repository = db.NewHangarOutputRepository(db.HangarConsumerPrefixForComponent())
		key = output.CaptureKey{ExecutionID: executioncontrol.ExecutionID(uuid.NewString()), Output: "result"}
		hangarActivateEpoch(ctx, repository)
	})

	Describe("the tree lock between a capture's move to publishing and a decision to delete", func() {
		BeforeEach(func() {
			dbConn.SetMaxOpenConns(4)
			DeferCleanup(func() { dbConn.SetMaxOpenConns(1) })
		})

		casToPublishing := func(digest hangar.Digest) <-chan error {
			done := make(chan error, 1)
			go func() {
				defer GinkgoRecover()
				done <- in(func(tx db.Tx) error {
					_, err := repository.CASPendingToPublishing(ctx, tx, key, "pod-1", scope, digest)
					return err
				})
			}()
			return done
		}

		It("makes the move to publishing wait for an orphan verdict on the same tree", func() {
			digest := digestOf("bbbbbbbb")
			insert(key, time.Hour)

			judging, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(judging)
			verdict, err := repository.JudgeOrphan(ctx, judging, hangar.TreeRef{Scope: scope, Digest: digest, Generation: 7})
			Expect(err).NotTo(HaveOccurred())
			Expect(verdict).To(Equal(db.HangarOrphan))

			done := casToPublishing(digest)
			Consistently(done, 500*time.Millisecond).ShouldNot(Receive(),
				"a capture moved onto a tree while the sweep was deciding to delete it")

			Expect(judging.Commit()).To(Succeed())
			Eventually(done, 10*time.Second).Should(Receive(BeNil()))
		})

		It("makes an orphan verdict wait for a move to publishing in flight, and then protects the tree", func() {
			digest := digestOf("cccccccc")
			insert(key, time.Hour)

			moving, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(moving)
			_, err = repository.CASPendingToPublishing(ctx, moving, key, "pod-1", scope, digest)
			Expect(err).NotTo(HaveOccurred())

			verdicts := make(chan db.HangarOrphanVerdict, 1)
			go func() {
				defer GinkgoRecover()
				Expect(in(func(tx db.Tx) error {
					verdict, err := repository.JudgeOrphan(ctx, tx, hangar.TreeRef{Scope: scope, Digest: digest, Generation: 7})
					verdicts <- verdict
					return err
				})).To(Succeed())
			}()
			Consistently(verdicts, 500*time.Millisecond).ShouldNot(Receive(),
				"the sweep decided about a tree a capture was moving onto")

			Expect(moving.Commit()).To(Succeed())
			Eventually(verdicts, 10*time.Second).Should(Receive(Equal(db.HangarOrphanProtected)))
		})
	})

	It("records a release no node acknowledged, and counts it apart from the residue", func() {
		insert(key, time.Hour)
		Expect(in(func(tx db.Tx) error {
			_, err := repository.CASPendingToDiscarded(ctx, tx, key, "no output")
			return err
		})).To(Succeed())
		Expect(in(func(tx db.Tx) error {
			_, err := repository.SetReleasedWithoutAcknowledgement(ctx, tx, key)
			return err
		})).To(Succeed())
		Expect(get(key).Released()).To(BeTrue())

		var counts output.PlaneCounts
		Expect(in(func(tx db.Tx) (err error) {
			counts, err = repository.CountOutputPlaneState(ctx, tx)
			return err
		})).To(Succeed())
		Expect(counts.UnacknowledgedReleases).To(Equal(1))
		Expect(counts.UnreleasedCaptures).To(BeZero())
		Expect(counts.Residue()).To(BeZero())
	})

	It("admits no new capture out of service, and still replays one admitted before", func() {
		insert(key, time.Hour)
		_, err := db.SetHangarEnabled(ctx, dbConn, false)
		Expect(err).NotTo(HaveOccurred())

		again := insert(key, time.Hour)
		Expect(again.State).To(Equal(output.CapturePending), "a replay of an admitted insert is not new admission")

		other := output.CaptureKey{ExecutionID: executioncontrol.ExecutionID(uuid.NewString()), Output: "result"}
		err = in(func(tx db.Tx) error {
			_, err := repository.InsertPending(ctx, tx, output.PendingCapture{
				Execution: executioncontrol.Identity{ExecutionID: other.ExecutionID, Fence: 1}, Output: other.Output,
				Node: "node-a", NodeUID: "node-uid-a", Term: time.Hour,
			})
			return err
		})
		Expect(err).To(MatchError(output.ErrCaptureDisabled))
	})

	It("inserts a pending row once and replays the same facts", func() {
		first := insert(key, time.Hour)
		Expect(first.State).To(Equal(output.CapturePending))
		Expect(first.Node).To(Equal("node-a"))
		Expect(first.CaptureDeadline).To(BeTemporally("~", time.Now().Add(time.Hour), time.Minute))

		again := insert(key, time.Hour)
		Expect(again.CaptureDeadline).To(Equal(first.CaptureDeadline))

		err := in(func(tx db.Tx) error {
			_, err := repository.InsertPending(ctx, tx, output.PendingCapture{
				Execution: executioncontrol.Identity{ExecutionID: key.ExecutionID, Fence: 1}, Output: key.Output,
				Node: "node-b", NodeUID: "node-uid-b", Term: time.Hour,
			})
			return err
		})
		Expect(err).To(MatchError(output.ErrConflict))
	})

	It("walks pending, publishing, published and takes the capture's claim", func() {
		hangarActivateEpoch(ctx, repository)
		insert(key, time.Hour)

		Expect(in(func(tx db.Tx) error {
			_, err := repository.CASPendingToPublishing(ctx, tx, key, "pod-1", scope, digestOf("aaaaaaaa"))
			return err
		})).To(Succeed())
		By("replaying the same CAS after a lost answer")
		Expect(in(func(tx db.Tx) error {
			_, err := repository.CASPendingToPublishing(ctx, tx, key, "pod-1", scope, digestOf("aaaaaaaa"))
			return err
		})).To(Succeed())
		By("refusing a different digest")
		Expect(in(func(tx db.Tx) error {
			_, err := repository.CASPendingToPublishing(ctx, tx, key, "pod-1", scope, digestOf("bbbbbbbb"))
			return err
		})).To(MatchError(output.ErrConflict))

		publishing := get(key)
		Expect(publishing.State).To(Equal(output.CapturePublishing))
		Expect(publishing.PodUID).To(Equal(executioncontrol.PodUID("pod-1")))

		published := output.PublishedCapture{Key: key, Generation: 7, Metageneration: 1, ActivationEpoch: 1}
		Expect(in(func(tx db.Tx) error {
			_, err := repository.CASPublishingToPublished(ctx, tx, published)
			return err
		})).To(Succeed())
		Expect(in(func(tx db.Tx) error {
			_, err := repository.CASPublishingToPublished(ctx, tx, published)
			return err
		})).To(Succeed())

		row := get(key)
		Expect(row.State).To(Equal(output.CapturePublished))
		ref, err := row.Ref()
		Expect(err).NotTo(HaveOccurred())
		Expect(ref).To(Equal(hangar.TreeRef{Scope: scope, Digest: digestOf("aaaaaaaa"), Generation: 7}))
		Expect(row.FinishedAt).NotTo(BeNil())

		var lifecycles, claims int
		Expect(dbConn.QueryRow(`SELECT count(*) FROM hangar_exact_lifecycles WHERE scope=$1 AND digest=$2 AND generation=7`,
			string(scope), string(ref.Digest)).Scan(&lifecycles)).To(Succeed())
		Expect(lifecycles).To(Equal(1))
		Expect(dbConn.QueryRow(`SELECT count(*) FROM hangar_claims WHERE claim_id=$1 AND released_at IS NULL`,
			string(key.ClaimID())).Scan(&claims)).To(Succeed())
		Expect(claims).To(Equal(1))
	})

	It("discards a pending capture and never publishes it afterwards", func() {
		insert(key, time.Hour)
		Expect(in(func(tx db.Tx) error {
			_, err := repository.CASPendingToDiscarded(ctx, tx, key, output.DiscardRunCancelled)
			return err
		})).To(Succeed())
		Expect(get(key).Error).To(Equal(output.DiscardRunCancelled))

		Expect(in(func(tx db.Tx) error {
			_, err := repository.CASPendingToPublishing(ctx, tx, key, "pod-1", scope, digestOf("cccccccc"))
			return err
		})).To(MatchError(output.ErrConflict))
	})

	It("never cancels a publishing capture", func() {
		insert(key, time.Hour)
		Expect(in(func(tx db.Tx) error {
			_, err := repository.CASPendingToPublishing(ctx, tx, key, "pod-1", scope, digestOf("dddddddd"))
			return err
		})).To(Succeed())
		Expect(in(func(tx db.Tx) error {
			_, err := repository.CASPendingToDiscarded(ctx, tx, key, output.DiscardRunCancelled)
			return err
		})).To(MatchError(output.ErrConflict))
	})

	It("refuses a transition the schema does not have, whoever writes it", func() {
		insert(key, time.Hour)
		_, err := dbConn.Exec(`UPDATE hangar_captures SET state='published', scope=$3, digest=$4, generation=1, finished_at=now()
			WHERE execution_id=$1 AND output_name=$2`, string(key.ExecutionID), string(key.Output), string(scope), string(digestOf("eeeeeeee")))
		Expect(err).To(MatchError(ContainSubstring("cannot move from pending to published")))
		_, err = dbConn.Exec(`DELETE FROM hangar_captures WHERE execution_id=$1`, string(key.ExecutionID))
		Expect(err).To(MatchError(ContainSubstring("deleted only by its team's purge")))
	})

	It("lists recovery, deadline and release work and releases once", func() {
		expired := output.CaptureKey{ExecutionID: executioncontrol.ExecutionID(uuid.NewString()), Output: "result"}
		insert(expired, time.Second)
		insert(key, time.Hour)
		Expect(in(func(tx db.Tx) error {
			_, err := repository.CASPendingToPublishing(ctx, tx, key, "pod-1", scope, digestOf("ffffffff"))
			return err
		})).To(Succeed())

		Eventually(func() []output.CaptureKey {
			var keys []output.CaptureKey
			Expect(in(func(tx db.Tx) error {
				rows, err := repository.ListPendingPastDeadline(ctx, tx, 100)
				for _, row := range rows {
					keys = append(keys, row.Key)
				}
				return err
			})).To(Succeed())
			return keys
		}, 5*time.Second, 100*time.Millisecond).Should(ContainElement(expired))

		var publishing []output.Capture
		Expect(in(func(tx db.Tx) (err error) {
			publishing, err = repository.ListPublishingForRecovery(ctx, tx, 100)
			return err
		})).To(Succeed())
		Expect(publishing).To(ContainElement(HaveField("Key", key)))

		Expect(in(func(tx db.Tx) error {
			_, err := repository.MarkFailed(ctx, tx, expired, "capture deadline passed")
			return err
		})).To(Succeed())

		var unreleased []output.Capture
		Expect(in(func(tx db.Tx) (err error) {
			unreleased, err = repository.ListUnreleased(ctx, tx, 100)
			return err
		})).To(Succeed())
		Expect(unreleased).To(ContainElement(HaveField("Key", expired)))
		Expect(unreleased).NotTo(ContainElement(HaveField("Key", key)))

		By("refusing to release a capture that is still publishing")
		Expect(in(func(tx db.Tx) error {
			_, err := repository.SetReleased(ctx, tx, key)
			return err
		})).To(MatchError(output.ErrConflict))

		Expect(in(func(tx db.Tx) error {
			_, err := repository.SetReleased(ctx, tx, expired)
			return err
		})).To(Succeed())
		Expect(in(func(tx db.Tx) error {
			_, err := repository.SetReleased(ctx, tx, expired)
			return err
		})).To(Succeed())
		Expect(get(expired).Released()).To(BeTrue())
	})
})
