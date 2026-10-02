package db_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The schema half of TestNoRunCodeBranchesOnTheContractClass: every earlier
// branch on a Run's contract class was a SQL predicate in a function or
// constraint, which no Go scan sees. After every migration, only the two birth
// objects may name the column: the constraint that admits only v2, and the
// trigger function that keeps the field immutable after birth.
var _ = Describe("the migrated schema and the Run contract class", func() {
	It("names run_contract_version only in the birth constraint and its immutability trigger", func() {
		rows, err := dbConn.Query(`
			SELECT 'function ' || p.proname
			  FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
			 WHERE n.nspname = 'public' AND p.prokind IN ('f', 'p')
			   AND p.proname <> 'immutable_run_birth_contract'
			   AND pg_get_functiondef(p.oid) LIKE '%run_contract_version%'
			UNION ALL
			SELECT 'constraint ' || c.conname
			  FROM pg_constraint c JOIN pg_namespace n ON n.oid = c.connamespace
			 WHERE n.nspname = 'public' AND c.conname <> 'pipeline_run_birth_contract'
			   AND pg_get_constraintdef(c.oid) LIKE '%run_contract_version%'
			UNION ALL
			SELECT 'view ' || viewname FROM pg_views
			 WHERE schemaname = 'public' AND definition LIKE '%run_contract_version%'
			UNION ALL
			SELECT 'index ' || indexname FROM pg_indexes
			 WHERE schemaname = 'public' AND indexdef LIKE '%run_contract_version%'`)
		Expect(err).NotTo(HaveOccurred())
		defer rows.Close()
		var found []string
		for rows.Next() {
			var name string
			Expect(rows.Scan(&name)).To(Succeed())
			found = append(found, name)
		}
		Expect(rows.Err()).NotTo(HaveOccurred())
		Expect(found).To(BeEmpty(), "every Run is v2; no schema object may behave by class")

		var births int
		Expect(dbConn.QueryRow(`
			SELECT (SELECT count(*) FROM pg_constraint
			         WHERE conname = 'pipeline_run_birth_contract'
			           AND pg_get_constraintdef(oid) LIKE '%run_contract_version%')
			     + (SELECT count(*) FROM pg_proc
			         WHERE proname = 'immutable_run_birth_contract'
			           AND pg_get_functiondef(oid) LIKE '%run_contract_version%')`).Scan(&births)).To(Succeed())
		Expect(births).To(Equal(2), "the scan would pass vacuously without the birth objects it exempts")
	})
})
