package gc_test

import (
	"context"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db/dbtest"
	"github.com/concourse/concourse/atc/gc"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func intptr(i int) *int {
	return &i
}

var _ = Describe("DeprecatedScopeCollector", func() {
	var (
		collector  GcCollector
		oldScopeID int
	)

	BeforeEach(func() {
		scenario := dbtest.Setup(
			builder.WithPipeline(atc.Config{
				Resources: atc.ResourceConfigs{
					{
						Name: "some-resource",
						Type: "some-base-type",
						Source: atc.Source{
							"some": "source",
						},
					},
				},
				Jobs: atc.JobConfigs{
					{
						Name: "some-job",
						PlanSequence: []atc.Step{
							{
								Config: &atc.GetStep{
									Name: "some-resource",
								},
							},
						},
					},
				},
			}),
			builder.WithResourceVersions("some-resource"),
		)

		resource := scenario.Resource("some-resource")
		oldScopeID = resource.ResourceConfigScopeID()

		// Change config to deprecate the old scope
		newRC, err := resourceConfigFactory.FindOrCreateResourceConfig(
			"some-base-type",
			atc.Source{"some": "changed-source"},
			nil,
		)
		Expect(err).ToNot(HaveOccurred())

		_, err = newRC.FindOrCreateScope(intptr(resource.ID()))
		Expect(err).ToNot(HaveOccurred())

		// Verify the old scope was deprecated
		var deprecatedAt *time.Time
		err = dbConn.QueryRow(
			`SELECT deprecated_at FROM resource_config_scopes WHERE id = $1`,
			oldScopeID,
		).Scan(&deprecatedAt)
		Expect(err).ToNot(HaveOccurred())
		Expect(deprecatedAt).ToNot(BeNil())
	})

	It("does NOT collect scopes within the grace period", func() {
		collector = gc.NewDeprecatedScopeCollector(dbConn, 30*24*time.Hour)

		err := collector.Run(context.Background())
		Expect(err).ToNot(HaveOccurred())

		// Scope should still exist
		var count int
		err = dbConn.QueryRow(
			`SELECT COUNT(*) FROM resource_config_scopes WHERE id = $1`,
			oldScopeID,
		).Scan(&count)
		Expect(err).ToNot(HaveOccurred())
		Expect(count).To(Equal(1))
	})

	It("collects scopes past the grace period", func() {
		// Backdate deprecated_at to 31 days ago
		_, err := dbConn.Exec(
			`UPDATE resource_config_scopes SET deprecated_at = now() - interval '31 days' WHERE id = $1`,
			oldScopeID,
		)
		Expect(err).ToNot(HaveOccurred())

		collector = gc.NewDeprecatedScopeCollector(dbConn, 30*24*time.Hour)

		err = collector.Run(context.Background())
		Expect(err).ToNot(HaveOccurred())

		// Scope should be deleted
		var count int
		err = dbConn.QueryRow(
			`SELECT COUNT(*) FROM resource_config_scopes WHERE id = $1`,
			oldScopeID,
		).Scan(&count)
		Expect(err).ToNot(HaveOccurred())
		Expect(count).To(Equal(0))
	})
})
