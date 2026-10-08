package migration_test

import (
	"database/sql"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const claimsOnlyReclaimVersion = 1789793156

var _ = Describe("Hangar claims_only_reclaim migration", func() {
	var database *sql.DB

	const (
		reclaimDigest = "sha256:3333333333333333333333333333333333333333333333333333333333333333"
		consumerClaim = "66666666-6666-4666-8666-666666666666"
		readerClaim   = "77777777-7777-4777-8777-777777777777"
		readLease     = "88888888-8888-4888-8888-888888888888"
		reclaimOwner  = "99999999-9999-4999-8999-999999999999"
	)

	tableExists := func(name string) bool {
		GinkgoHelper()
		var exists bool
		Expect(database.QueryRow(`SELECT to_regclass($1) IS NOT NULL`, name).Scan(&exists)).To(Succeed())
		return exists
	}

	columnExists := func(table, column string) bool {
		GinkgoHelper()
		var exists bool
		Expect(database.QueryRow(`SELECT EXISTS (
			SELECT 1 FROM information_schema.columns WHERE table_name = $1 AND column_name = $2)`,
			table, column).Scan(&exists)).To(Succeed())
		return exists
	}

	functionsPresent := func() int {
		GinkgoHelper()
		var count int
		Expect(database.QueryRow(`SELECT count(*) FROM pg_proc WHERE proname IN
			('hangar_check_read_lease_claim', 'hangar_read_lease_guard', 'hangar_check_reclaim_admission',
			 'hangar_check_reclaim_evidence', 'hangar_check_reclaim_exclusion', 'hangar_lifecycle_transition')`).
			Scan(&count)).To(Succeed())
		return count
	}

	// The states a deployment carries into the migration, one lifecycle each,
	// keyed by generation.
	states := map[int64]string{
		1: "registered",
		2: "reclaiming",
		3: "reclaimed_confirmed",
		4: "reclaimed_inferred",
		5: "conflicted",
		6: "missing_out_of_band",
	}

	BeforeEach(func() {
		database = postgresRunner.OpenDBAtVersion(claimsOnlyReclaimVersion - 1)
		DeferCleanup(func() { Expect(database.Close()).To(Succeed()) })

		// Written with triggers off: the old shape's transition guard
		// refuses a lifecycle born in any state but registered, and what is
		// being arranged is a deployment that reached these states honestly.
		setup, err := database.Begin()
		Expect(err).NotTo(HaveOccurred())
		_, err = setup.Exec(`SET LOCAL session_replication_role = replica`)
		Expect(err).NotTo(HaveOccurred())
		for generation, state := range states {
			_, err = setup.Exec(`INSERT INTO hangar_exact_lifecycles
				(scope, digest, generation, metageneration, activation_epoch, marker_version, origin, state)
				VALUES ('scope-a', $1, $2, 1, 1, 'hangar-output-v1', 'registered', $3)`,
				reclaimDigest, generation, state)
			Expect(err).NotTo(HaveOccurred(), state)
		}
		// A consumer's claim and a reader's claim beside its read lease, on
		// the registered generation; a reclaim job and one attempt on the
		// reclaiming one.
		_, err = setup.Exec(`INSERT INTO hangar_claims (claim_id, lifecycle_id, activation_epoch, consumer_binding_id)
			SELECT $1, id, 1, 'result:binding' FROM hangar_exact_lifecycles WHERE generation = 1`, consumerClaim)
		Expect(err).NotTo(HaveOccurred())
		_, err = setup.Exec(`INSERT INTO hangar_claims (claim_id, lifecycle_id, activation_epoch, consumer_binding_id)
			SELECT $1, id, 1, 'input-read:handle/input-0' FROM hangar_exact_lifecycles WHERE generation = 1`, readerClaim)
		Expect(err).NotTo(HaveOccurred())
		_, err = setup.Exec(`INSERT INTO hangar_read_leases
			(read_lease_id, claim_id, lifecycle_id, activation_epoch, lease_fence, expires_at,
			 grant_nonce, destination_handle, destination_volume,
			 stat_metageneration, stat_marker_version, stat_observed_at, lease_term_seconds)
			SELECT $1, $2, id, 1, 1, now() + interval '20 minutes', 'AAAAAAAAAAAAAAAAAAAAAA', 'handle', 'input-0',
			       1, 'hangar-output-v1', now(), 900
			  FROM hangar_exact_lifecycles WHERE generation = 1`, readLease, readerClaim)
		Expect(err).NotTo(HaveOccurred())
		_, err = setup.Exec(`INSERT INTO hangar_reclaim_jobs
			(lifecycle_id, activation_epoch, owner_id, lease_fence, generation, metageneration, expires_at)
			SELECT id, 1, $1, 1, 2, 1, now() + interval '20 minutes'
			  FROM hangar_exact_lifecycles WHERE generation = 2`, reclaimOwner)
		Expect(err).NotTo(HaveOccurred())
		_, err = setup.Exec(`INSERT INTO hangar_reclaim_attempts (job_id, lease_fence)
			SELECT id, 1 FROM hangar_reclaim_jobs`)
		Expect(err).NotTo(HaveOccurred())
		Expect(setup.Commit()).To(Succeed())
		Expect(functionsPresent()).To(Equal(6), "the fixture is not the shape the migration starts from")
		Expect(database.Close()).To(Succeed())
	})

	It("folds the lifecycle states into one stamp, gives a claim its expiry, drops the read lease and the reclaim job, and comes back lossily", func() {
		database = postgresRunner.OpenDBAtVersion(claimsOnlyReclaimVersion)

		for _, gone := range []string{"hangar_read_leases", "hangar_reclaim_jobs", "hangar_reclaim_attempts"} {
			Expect(tableExists(gone)).To(BeFalse(), gone)
		}
		Expect(functionsPresent()).To(BeZero(), "a function that existed only for the dropped tables survived")
		var triggers int
		Expect(database.QueryRow(`SELECT count(*) FROM pg_trigger WHERE tgname IN
			('hangar_lifecycle_transition_guard', 'hangar_reclaim_exclusion') OR
			(tgname = 'hangar_policy_admits_new_protection' AND tgrelid = 'hangar_exact_lifecycles'::regclass)`).
			Scan(&triggers)).To(Succeed())
		Expect(triggers).To(BeZero())
		Expect(database.QueryRow(`SELECT count(*) FROM pg_trigger WHERE tgname = 'hangar_policy_admits_new_protection'`).
			Scan(&triggers)).To(Succeed())
		Expect(triggers).To(Equal(3), "the admission gate stays on captures, claims and input publications")

		for _, dropped := range []string{"metageneration", "marker_version", "origin", "lifetime_audited_at", "state"} {
			Expect(columnExists("hangar_exact_lifecycles", dropped)).To(BeFalse(), dropped)
		}
		Expect(columnExists("hangar_exact_lifecycles", "reclaimed_at")).To(BeTrue())
		Expect(columnExists("hangar_claims", "expires_at")).To(BeTrue())

		// Confirmed and inferred reclamations are reclaimed, and so is a
		// conflicted generation: its exact generation is gone. One caught
		// mid-reclaim is registered again for the next pass, and one found
		// missing out of band stays registered beside its finding.
		stamped := map[int64]bool{}
		rows, err := database.Query(`SELECT generation, reclaimed_at IS NOT NULL FROM hangar_exact_lifecycles`)
		Expect(err).NotTo(HaveOccurred())
		for rows.Next() {
			var generation int64
			var reclaimed bool
			Expect(rows.Scan(&generation, &reclaimed)).To(Succeed())
			stamped[generation] = reclaimed
		}
		Expect(rows.Err()).NotTo(HaveOccurred())
		Expect(rows.Close()).To(Succeed())
		Expect(stamped).To(Equal(map[int64]bool{1: false, 2: false, 3: true, 4: true, 5: true, 6: false}))

		var reclaimable int
		Expect(database.QueryRow(`SELECT count(*) FROM hangar_exact_lifecycles WHERE reclaimed_at IS NULL`).
			Scan(&reclaimable)).To(Succeed())
		Expect(reclaimable).To(Equal(3))

		// A registered row is the only shape; a reclaimed one never resurrects,
		// and the row's own key is what the pass selects by.
		var index bool
		Expect(database.QueryRow(`SELECT indexdef LIKE '%WHERE (reclaimed_at IS NULL)%'
			FROM pg_indexes WHERE indexname = 'hangar_exact_lifecycles_reclaimable_idx'`).Scan(&index)).To(Succeed())
		Expect(index).To(BeTrue())

		// Every claim carried over has no expiry: the old shape had no spelling
		// for a reader's hold, so the reader's claim is a consumer's hold here
		// until it is released.
		var expiring int
		Expect(database.QueryRow(`SELECT count(*) FROM hangar_claims WHERE expires_at IS NOT NULL`).
			Scan(&expiring)).To(Succeed())
		Expect(expiring).To(BeZero())
		_, err = database.Exec(`UPDATE hangar_claims SET expires_at = now() + interval '20 minutes' WHERE claim_id = $1`,
			readerClaim)
		Expect(err).NotTo(HaveOccurred(), "a reader's claim cannot be given its term")
		Expect(database.Close()).To(Succeed())

		// Down: the dropped shapes come back EMPTY -- which reads were in
		// flight and what the store answered are not reconstructible -- a
		// lifecycle gets a state back from its stamp, and the reader's
		// expiring claim is released rather than left as a hold nothing will
		// give back.
		database = postgresRunner.OpenDBAtVersion(claimsOnlyReclaimVersion - 1)
		for _, back := range []string{"hangar_read_leases", "hangar_reclaim_jobs", "hangar_reclaim_attempts"} {
			Expect(tableExists(back)).To(BeTrue(), back)
			var count int
			Expect(database.QueryRow(`SELECT count(*) FROM ` + back).Scan(&count)).To(Succeed())
			Expect(count).To(BeZero(), back)
		}
		Expect(functionsPresent()).To(Equal(6))
		Expect(columnExists("hangar_exact_lifecycles", "reclaimed_at")).To(BeFalse())
		Expect(columnExists("hangar_claims", "expires_at")).To(BeFalse())

		restored := map[int64]string{}
		rows, err = database.Query(`SELECT generation, state FROM hangar_exact_lifecycles`)
		Expect(err).NotTo(HaveOccurred())
		for rows.Next() {
			var generation int64
			var state string
			Expect(rows.Scan(&generation, &state)).To(Succeed())
			restored[generation] = state
		}
		Expect(rows.Err()).NotTo(HaveOccurred())
		Expect(rows.Close()).To(Succeed())
		Expect(restored).To(Equal(map[int64]string{
			1: "registered", 2: "registered", 3: "reclaimed_confirmed",
			4: "reclaimed_confirmed", 5: "reclaimed_confirmed", 6: "registered",
		}))
		var origins int
		Expect(database.QueryRow(`SELECT count(*) FROM hangar_exact_lifecycles
			WHERE metageneration = 1 AND marker_version = 'hangar-output-v1' AND origin = 'registered'`).
			Scan(&origins)).To(Succeed())
		Expect(origins).To(Equal(6))

		var consumerReleased, readerReleased bool
		Expect(database.QueryRow(`SELECT released_at IS NOT NULL FROM hangar_claims WHERE claim_id = $1`,
			consumerClaim).Scan(&consumerReleased)).To(Succeed())
		Expect(database.QueryRow(`SELECT released_at IS NOT NULL FROM hangar_claims WHERE claim_id = $1`,
			readerClaim).Scan(&readerReleased)).To(Succeed())
		Expect(consumerReleased).To(BeFalse(), "a consumer's hold was released on the way down")
		Expect(readerReleased).To(BeTrue(), "a reader's expiring hold was left as a consumer's hold")
	})
})
