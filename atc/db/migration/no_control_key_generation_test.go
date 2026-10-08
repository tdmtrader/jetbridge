package migration_test

import (
	"database/sql"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const noControlKeyGenerationVersion = 1789793157

var _ = Describe("Hangar no_control_key_generation migration", func() {
	var database *sql.DB

	const (
		generationDigest = "sha256:4444444444444444444444444444444444444444444444444444444444444444"
		resultClaim      = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		uploadClaim      = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
		reservation      = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
		reservationNonce = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
		execution        = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
		principalDigest  = "5555555555555555555555555555555555555555555555555555555555555555"
	)

	// The six tables the migration strips, with the Run epoch's own table
	// deliberately absent: pipeline_runs.activation_epoch is a different
	// epoch and stays.
	tables := []string{
		"hangar_claims", "hangar_exact_lifecycles", "hangar_input_publications",
		"pipeline_run_executions", "pipeline_run_input_uploads", "pipeline_run_inputs",
	}
	seeded := map[string]int{
		"hangar_claims": 2, "hangar_exact_lifecycles": 1, "hangar_input_publications": 1,
		"pipeline_run_executions": 1, "pipeline_run_input_uploads": 1, "pipeline_run_inputs": 1,
	}

	columnShape := func(table string) (exists bool, nullable, dflt string) {
		GinkgoHelper()
		var isNullable, columnDefault sql.NullString
		err := database.QueryRow(`SELECT is_nullable, column_default FROM information_schema.columns
			WHERE table_name = $1 AND column_name = 'activation_epoch'`, table).Scan(&isNullable, &columnDefault)
		if err == sql.ErrNoRows {
			return false, "", ""
		}
		Expect(err).NotTo(HaveOccurred(), table)
		return true, isNullable.String, columnDefault.String
	}

	functionsNamingTheEpoch := func() []string {
		GinkgoHelper()
		rows, err := database.Query(`SELECT proname FROM pg_proc WHERE prosrc LIKE '%activation_epoch%' ORDER BY proname`)
		Expect(err).NotTo(HaveOccurred())
		defer rows.Close()
		var names []string
		for rows.Next() {
			var name string
			Expect(rows.Scan(&name)).To(Succeed())
			names = append(names, name)
		}
		Expect(rows.Err()).NotTo(HaveOccurred())
		return names
	}

	rowCount := func(table string) int {
		GinkgoHelper()
		var count int
		Expect(database.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count)).To(Succeed())
		return count
	}

	BeforeEach(func() {
		database = postgresRunner.OpenDBAtVersion(noControlKeyGenerationVersion - 1)
		DeferCleanup(func() { Expect(database.Close()).To(Succeed()) })

		// Written with triggers off: the old shape's deferred checks want a
		// whole Run, a whole build and a whole admitted upload around each
		// row, and what is being arranged is one row per table that the
		// migration must carry across and give back.
		setup, err := database.Begin()
		Expect(err).NotTo(HaveOccurred())
		_, err = setup.Exec(`SET LOCAL session_replication_role = replica`)
		Expect(err).NotTo(HaveOccurred())
		_, err = setup.Exec(`INSERT INTO hangar_exact_lifecycles (scope, digest, generation, activation_epoch)
			VALUES ('scope-a', $1, 1, 3)`, generationDigest)
		Expect(err).NotTo(HaveOccurred())
		_, err = setup.Exec(`INSERT INTO hangar_claims (claim_id, lifecycle_id, activation_epoch, consumer_binding_id)
			SELECT $1, id, 3, 'result:binding' FROM hangar_exact_lifecycles`, resultClaim)
		Expect(err).NotTo(HaveOccurred())
		_, err = setup.Exec(`INSERT INTO hangar_claims (claim_id, lifecycle_id, activation_epoch, consumer_binding_id)
			SELECT $1::uuid, id, 3, $1::text FROM hangar_exact_lifecycles`, uploadClaim)
		Expect(err).NotTo(HaveOccurred())
		_, err = setup.Exec(`INSERT INTO hangar_input_publications
			(reservation_id, scope, digest, activation_epoch, stage, nonce, expires_at)
			VALUES ($1, 'scope-a', $2, 3, '{"version":"hangar-input/v1","activation_epoch":3}', $3, now() + interval '1 minute')`,
			reservation, generationDigest, reservationNonce)
		Expect(err).NotTo(HaveOccurred())
		_, err = setup.Exec(`INSERT INTO pipeline_run_input_uploads
			(reservation_id, template_pipeline_id, team_id, principal_digest, input_name, activation_epoch, claim_id, expires_at)
			VALUES ($1, 1, 1, $2, 'input-0', 3, $3, clock_timestamp() + interval '10 minutes')`,
			reservation, principalDigest, uploadClaim)
		Expect(err).NotTo(HaveOccurred())
		_, err = setup.Exec(`INSERT INTO pipeline_run_executions
			(run_id, build_id, plan_id, kind, activation_epoch, node_name, node_uid, execution_id, execution_fence)
			VALUES (1, 1, 'plan', 'task', 3, 'node', 'node-uid', $1, 1)`, execution)
		Expect(err).NotTo(HaveOccurred())
		_, err = setup.Exec(`INSERT INTO pipeline_run_inputs
			(run_id, name, source_id, scope, digest, generation, claim_id, activation_epoch)
			VALUES (1, 'input-0', 'input-v1-' || $1, 'scope-a', $2, 1, $3, 3)`,
			principalDigest, generationDigest, resultClaim)
		Expect(err).NotTo(HaveOccurred())
		Expect(setup.Commit()).To(Succeed())
		Expect(functionsNamingTheEpoch()).To(HaveLen(7), "the fixture is not the shape the migration starts from")
		Expect(database.Close()).To(Succeed())
	})

	It("drops the generation from every Hangar row and its guards, keeps the Run epoch, and comes back lossily as generation 1", func() {
		database = postgresRunner.OpenDBAtVersion(noControlKeyGenerationVersion)

		for _, table := range tables {
			exists, _, _ := columnShape(table)
			Expect(exists).To(BeFalse(), table+".activation_epoch survived")
			Expect(rowCount(table)).To(Equal(seeded[table]), table+" lost rows")
		}
		exists, nullable, _ := columnShape("pipeline_runs")
		Expect(exists).To(BeTrue(), "the Run contract's own epoch was touched")
		Expect(nullable).To(Equal("YES"))

		// Only the Run epoch's two guards still spell the column.
		Expect(functionsNamingTheEpoch()).To(Equal([]string{"immutable_run_birth_contract", "monotonic_run_activation"}))

		// The five rewritten guards are still attached and still guard.
		var triggers int
		Expect(database.QueryRow(`SELECT count(*) FROM pg_trigger WHERE tgname IN
			('hangar_input_publication_immutable', 'hangar_input_publication_matches',
			 'pipeline_run_input_upload_claim', 'pipeline_run_input_upload_immutable')`).Scan(&triggers)).To(Succeed())
		Expect(triggers).To(Equal(4))
		_, err := database.Exec(`UPDATE hangar_input_publications SET digest = $1 WHERE reservation_id = $2`,
			"sha256:0000000000000000000000000000000000000000000000000000000000000000", reservation)
		Expect(err).To(MatchError(ContainSubstring("immutable")), "the rewritten immutability guard no longer guards")
		_, err = database.Exec(`UPDATE pipeline_run_input_uploads SET input_name = 'other' WHERE reservation_id = $1`, reservation)
		Expect(err).To(MatchError(ContainSubstring("immutable")), "the rewritten immutability guard no longer guards")
		Expect(database.Close()).To(Succeed())

		// Down: the generation is not reconstructible, so every row comes
		// back as generation 1 under a NOT NULL DEFAULT 1 column, and the
		// five guards get their epoch predicates back.
		database = postgresRunner.OpenDBAtVersion(noControlKeyGenerationVersion - 1)
		for _, table := range tables {
			exists, nullable, dflt := columnShape(table)
			Expect(exists).To(BeTrue(), table)
			Expect(nullable).To(Equal("NO"), table)
			Expect(dflt).To(Equal("1"), table)
			var generationOne, total int
			Expect(database.QueryRow(`SELECT count(*) FILTER (WHERE activation_epoch = 1), count(*) FROM `+table).
				Scan(&generationOne, &total)).To(Succeed())
			Expect(total).NotTo(BeZero(), table)
			Expect(generationOne).To(Equal(total), table)
		}
		var check string
		Expect(database.QueryRow(`SELECT pg_get_constraintdef(oid) FROM pg_constraint
			WHERE conrelid = 'pipeline_run_executions'::regclass AND conname = 'pipeline_run_executions_activation_epoch_check'`).
			Scan(&check)).To(Succeed())
		Expect(check).To(Equal("CHECK ((activation_epoch > 0))"))
		Expect(functionsNamingTheEpoch()).To(HaveLen(7))
	})
})
