package db_test

import (
	"errors"
	"fmt"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"
	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5/pgconn"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("IsForeignKeyViolation", func() {
	Describe("error-shape unit cases", func() {
		It("returns false for nil", func() {
			Expect(db.IsForeignKeyViolation(nil)).To(BeFalse())
		})

		It("returns false for a non-postgres error", func() {
			Expect(db.IsForeignKeyViolation(errors.New("boom"))).To(BeFalse())
		})

		It("returns true for a bare *pgconn.PgError with the FK code", func() {
			Expect(db.IsForeignKeyViolation(&pgconn.PgError{Code: pgerrcode.ForeignKeyViolation})).To(BeTrue())
		})

		It("returns true for a wrapped *pgconn.PgError (fmt.Errorf %w)", func() {
			wrapped := fmt.Errorf("save versions: %w", &pgconn.PgError{Code: pgerrcode.ForeignKeyViolation})
			Expect(db.IsForeignKeyViolation(wrapped)).To(BeTrue())
		})

		It("returns true via the string fallback when only the SQLSTATE text is present", func() {
			// Some wrapping paths flatten the typed error to a string; the
			// SQLSTATE marker must still be detected.
			Expect(db.IsForeignKeyViolation(errors.New(`ERROR: violates foreign key constraint (SQLSTATE 23503)`))).To(BeTrue())
		})

		It("returns false for a non-FK postgres error", func() {
			Expect(db.IsForeignKeyViolation(&pgconn.PgError{Code: pgerrcode.UniqueViolation})).To(BeFalse())
		})
	})

	// Regression test for the resource_config_scope GC race: GC deletes the
	// scope mid-check, so the subsequent INSERT into resource_config_versions
	// fails its foreign key. This drives the REAL pgx/database-sql error path
	// (not a synthetic *pgconn.PgError) to guard against wrapping changes that
	// would silently break detection — the gap that let the leak ship.
	Describe("with a real foreign-key violation from the database (GC race)", func() {
		var resourceScope db.ResourceConfigScope

		BeforeEach(func() {
			scenario := dbtest.Setup(
				builder.WithPipeline(atc.Config{
					Resources: atc.ResourceConfigs{
						{
							Name:   "some-resource",
							Type:   "some-base-resource-type",
							Source: atc.Source{"some": "source"},
						},
					},
				}),
				builder.WithResourceVersions("some-resource"),
			)

			rc, found, err := resourceConfigFactory.FindResourceConfigByID(scenario.Resource("some-resource").ResourceConfigID())
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())

			resourceScope, err = rc.FindOrCreateScope(intptr(scenario.Resource("some-resource").ID()))
			Expect(err).ToNot(HaveOccurred())
		})

		It("is detected by IsForeignKeyViolation when SaveVersions races a scope deletion", func() {
			// Simulate GC deleting the resource_config_scope mid-check.
			_, err := dbConn.Exec(`DELETE FROM resource_config_scopes WHERE id = $1`, resourceScope.ID())
			Expect(err).ToNot(HaveOccurred())

			err = resourceScope.SaveVersions(nil, []atc.Version{{"ref": "after-delete"}})
			Expect(err).To(HaveOccurred(), "expected SaveVersions to fail after the scope was deleted")
			Expect(db.IsForeignKeyViolation(err)).To(BeTrue(),
				"expected the real FK violation to be detected; got %T: %v", err, err)
		})
	})
})
