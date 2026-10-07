package migration_test

import (
	"database/sql"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const noActivationVersion = 1789793154

var _ = Describe("Hangar no_activation_no_inventory migration", func() {
	var database *sql.DB

	tableExists := func(name string) bool {
		GinkgoHelper()
		var exists bool
		Expect(database.QueryRow(`SELECT to_regclass($1) IS NOT NULL`, name).Scan(&exists)).To(Succeed())
		return exists
	}

	BeforeEach(func() {
		database = postgresRunner.OpenDBAtVersion(noActivationVersion - 1)
		DeferCleanup(func() { Expect(database.Close()).To(Succeed()) })
	})

	It("drops the epochs, the operation leases and the inventory, keeps the in-service row and the findings, and comes back empty", func() {
		// An epoch and a lifecycle under it, and an open finding: what a
		// deployment that had activated carries into the migration. Written
		// with triggers off, as the activation role's guard would refuse the
		// test's own role.
		setup, err := database.Begin()
		Expect(err).NotTo(HaveOccurred())
		_, err = setup.Exec(`SET LOCAL session_replication_role = replica`)
		Expect(err).NotTo(HaveOccurred())
		_, err = setup.Exec(`INSERT INTO hangar_output_activation_epochs(epoch_id) VALUES (3)`)
		Expect(err).NotTo(HaveOccurred())
		_, err = setup.Exec(`INSERT INTO hangar_exact_lifecycles
			(scope, digest, generation, metageneration, activation_epoch, marker_version, origin, state)
			VALUES ('scope-a', 'sha256:` + "1111111111111111111111111111111111111111111111111111111111111111" + `', 7, 1, 3,
				'hangar-output-v1', 'registered', 'registered')`)
		Expect(err).NotTo(HaveOccurred())
		_, err = setup.Exec(`INSERT INTO hangar_integrity_findings (violation, subject) VALUES ('out_of_band_absence', 'scope-a/x/7')`)
		Expect(err).NotTo(HaveOccurred())
		_, err = setup.Exec(`UPDATE hangar_enabled SET enabled = true`)
		Expect(err).NotTo(HaveOccurred())
		Expect(setup.Commit()).To(Succeed())
		Expect(database.Close()).To(Succeed())

		database = postgresRunner.OpenDBAtVersion(noActivationVersion)
		for _, gone := range []string{"hangar_output_activation_epochs", "hangar_operation_leases",
			"hangar_inventory_cursors", "hangar_inventory_debt", "hangar_output_node_keys"} {
			Expect(tableExists(gone)).To(BeFalse(), gone)
		}
		var roleName int
		Expect(database.QueryRow(`SELECT count(*) FROM pg_proc WHERE proname IN
			('hangar_activation_role_name', 'hangar_activation_grants', 'hangar_activation_role_guard')`).Scan(&roleName)).To(Succeed())
		Expect(roleName).To(BeZero(), "the activation role's functions survived")

		// The lifecycle keeps its recorded number with nothing to reference.
		var epoch int64
		Expect(database.QueryRow(`SELECT activation_epoch FROM hangar_exact_lifecycles`).Scan(&epoch)).To(Succeed())
		Expect(epoch).To(BeEquivalentTo(3))
		check, err := database.Begin()
		Expect(err).NotTo(HaveOccurred())
		_, err = check.Exec(`INSERT INTO hangar_exact_lifecycles
			(scope, digest, generation, metageneration, activation_epoch, marker_version, origin, state)
			VALUES ('scope-a', 'sha256:` + "1111111111111111111111111111111111111111111111111111111111111111" + `', 8, 1, 99,
				'hangar-output-v1', 'registered', 'registered')`)
		Expect(err).NotTo(HaveOccurred(), "a lifecycle no longer needs an epoch row")
		_, err = check.Exec(`SET CONSTRAINTS ALL IMMEDIATE`)
		Expect(err).NotTo(HaveOccurred(), "a lifecycle no longer needs an epoch row")
		Expect(check.Commit()).To(Succeed())

		var enabled bool
		Expect(database.QueryRow(`SELECT enabled FROM hangar_enabled`).Scan(&enabled)).To(Succeed())
		Expect(enabled).To(BeTrue(), "the in-service row is kept as it was")
		var findings int
		Expect(database.QueryRow(`SELECT count(*) FROM hangar_integrity_findings WHERE resolved_at IS NULL`).Scan(&findings)).To(Succeed())
		Expect(findings).To(Equal(1))
		Expect(database.Close()).To(Succeed())

		// Down: the shapes come back EMPTY -- epochs are not reconstructible --
		// and the rows recorded since keep their numbers.
		database = postgresRunner.OpenDBAtVersion(noActivationVersion - 1)
		for _, back := range []string{"hangar_output_activation_epochs", "hangar_operation_leases",
			"hangar_inventory_cursors", "hangar_inventory_debt"} {
			Expect(tableExists(back)).To(BeTrue(), back)
		}
		var epochs, lifecycles int
		Expect(database.QueryRow(`SELECT count(*) FROM hangar_output_activation_epochs`).Scan(&epochs)).To(Succeed())
		Expect(epochs).To(BeZero())
		Expect(database.QueryRow(`SELECT count(*) FROM hangar_exact_lifecycles`).Scan(&lifecycles)).To(Succeed())
		Expect(lifecycles).To(Equal(2))
	})
})
