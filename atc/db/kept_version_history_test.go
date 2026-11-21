package db_test

import (
	"database/sql"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// A version history no resource uses any more is kept for the history grace
// after its last check or after its last resource left it. A resource leaves
// a history when a check or a put moves it to another config, or when a
// pipeline save detaches it (its source or type changed, or its pipeline was
// archived and set again). Leaving stamps the config's last_referenced, the
// retention clock, and names the resource on the scope it left.
var _ = Describe("A version history its last resource left", func() {
	const (
		shortGrace   = 65 * time.Minute
		historyGrace = 30 * 24 * time.Hour
	)

	var (
		scenario    *dbtest.Scenario
		resource    db.Resource
		oldConfigID int
		oldScopeID  int

		oldSource = atc.Source{"some": "old-source"}
		newSource = atc.Source{"some": "new-source"}
	)

	pipelineWith := func(source atc.Source) atc.Config {
		return atc.Config{
			Resources: atc.ResourceConfigs{
				{
					Name:   "some-resource",
					Type:   dbtest.BaseResourceType,
					Source: source,
				},
			},
			Jobs: atc.JobConfigs{
				{
					Name: "some-job",
					PlanSequence: []atc.Step{
						{Config: &atc.PutStep{Name: "some-resource"}},
					},
				},
			},
		}
	}

	collect := func() {
		GinkgoHelper()
		Expect(resourceConfigFactory.CleanUnreferencedConfigs(shortGrace, historyGrace)).To(Succeed())
	}

	configPresent := func(configID int) bool {
		GinkgoHelper()
		_, found, err := resourceConfigFactory.FindResourceConfigByID(configID)
		Expect(err).ToNot(HaveOccurred())
		return found
	}

	versionCount := func(scopeID int) int {
		GinkgoHelper()
		var n int
		Expect(dbConn.QueryRow(`SELECT count(*) FROM resource_config_versions WHERE resource_config_scope_id = $1`, scopeID).Scan(&n)).To(Succeed())
		return n
	}

	// leftBy is the scope's deprecated_from_resource_id and whether the scope
	// is deprecated.
	leftBy := func(scopeID int) (sql.NullInt64, bool) {
		GinkgoHelper()
		var from sql.NullInt64
		var deprecated bool
		Expect(dbConn.QueryRow(`
			SELECT deprecated_from_resource_id, deprecated_at IS NOT NULL FROM resource_config_scopes WHERE id = $1
		`, scopeID).Scan(&from, &deprecated)).To(Succeed())
		return from, deprecated
	}

	// The history was last checked long ago: a resource that is only put to,
	// or whose checks stopped, has a last_referenced as old as its last check.
	ageOldConfig := func() {
		GinkgoHelper()
		_, err := dbConn.Exec(`UPDATE resource_configs SET last_referenced = now() - interval '31 days' WHERE id = $1`, oldConfigID)
		Expect(err).ToNot(HaveOccurred())
	}

	checkOnto := func(source atc.Source) db.ResourceConfigScope {
		GinkgoHelper()
		config, err := resourceConfigFactory.FindOrCreateResourceConfig(dbtest.BaseResourceType, source, nil)
		Expect(err).ToNot(HaveOccurred())
		scope, err := config.FindOrCreateScope(resource.TeamID(), intptr(resource.ID()))
		Expect(err).ToNot(HaveOccurred())
		Expect(resource.SetResourceConfigScope(scope)).To(Succeed())
		return scope
	}

	expectKeptAndNamed := func() {
		GinkgoHelper()
		collect()
		Expect(configPresent(oldConfigID)).To(BeTrue(), "the old config was collected")
		Expect(versionCount(oldScopeID)).To(Equal(2))

		from, deprecated := leftBy(oldScopeID)
		Expect(from.Valid).To(BeTrue(), "the old scope does not name the resource that left it")
		Expect(int(from.Int64)).To(Equal(resource.ID()))
		Expect(deprecated).To(BeFalse(), "a shared scope left behind is kept live, not deprecated")
	}

	BeforeEach(func() {
		scenario = dbtest.Setup(
			builder.WithPipeline(pipelineWith(oldSource)),
			builder.WithResourceVersions("some-resource", atc.Version{"ref": "v1"}, atc.Version{"ref": "v2"}),
		)
		resource = scenario.Resource("some-resource")
		oldConfigID = resource.ResourceConfigID()
		oldScopeID = resource.ResourceConfigScopeID()
		ageOldConfig()
	})

	It("is kept when a pipeline save detaches its resource", func() {
		scenario.Run(builder.WithPipeline(pipelineWith(newSource)))
		Expect(scenario.Resource("some-resource").ResourceConfigScopeID()).To(BeZero())

		expectKeptAndNamed()
	})

	It("is kept when a check moves its resource to another config", func() {
		checkOnto(newSource)

		expectKeptAndNamed()
	})

	It("is kept when a put moves its resource to another config", func() {
		var build db.Build
		scenario.Run(builder.WithPendingJobBuild(&build, "some-job"))
		Expect(build.SaveOutput(dbtest.BaseResourceType, nil, newSource, atc.Version{"ref": "put"}, nil, "some-resource", "some-resource")).To(Succeed())
		Expect(scenario.Resource("some-resource").ResourceConfigID()).ToNot(Equal(oldConfigID))

		expectKeptAndNamed()
	})

	It("is found again on its old scope after an archived pipeline is set again", func() {
		Expect(scenario.Pipeline.Archive()).To(Succeed())
		ageOldConfig()

		_, _, err := scenario.Team.SavePipeline(atc.PipelineRef{Name: "some-pipeline"}, pipelineWith(oldSource), db.ConfigVersion(0), false)
		Expect(err).ToNot(HaveOccurred())
		Expect(scenario.Resource("some-resource").ResourceConfigScopeID()).To(BeZero(), "re-setting an archived pipeline no longer detaches its resources")

		collect()
		Expect(configPresent(oldConfigID)).To(BeTrue(), "the old config was collected")

		resource = scenario.Resource("some-resource")
		scope := checkOnto(oldSource)
		Expect(scope.ID()).To(Equal(oldScopeID))
		Expect(versionCount(scope.ID())).To(Equal(2))
	})

	It("keeps the short grace for a history whose last resource stays", func() {
		// Nothing left: the config is still referenced and its clock untouched.
		scenario.Run(builder.WithPipeline(pipelineWith(oldSource)))
		var lastReferenced time.Time
		Expect(dbConn.QueryRow(`SELECT last_referenced FROM resource_configs WHERE id = $1`, oldConfigID).Scan(&lastReferenced)).To(Succeed())
		Expect(lastReferenced).To(BeTemporally("<", time.Now().Add(-29*24*time.Hour)))
		from, _ := leftBy(oldScopeID)
		Expect(from.Valid).To(BeFalse())
	})

	Context("when another transaction holds the old scope", func() {
		// A pipeline save holds its pipeline's job rows, and a mover holds
		// scopes while it schedules those jobs, so a save must never wait on
		// a scope or a config row.
		var holder db.Tx

		hold := func(statement string, id int) {
			GinkgoHelper()
			conn := openRunLifecycleConn()
			var err error
			holder, err = conn.Begin()
			Expect(err).ToNot(HaveOccurred())
			DeferCleanup(func() { _ = holder.Rollback() })
			_, err = holder.Exec(statement, id)
			Expect(err).ToNot(HaveOccurred())
		}

		saveWithoutWaiting := func() {
			GinkgoHelper()
			done := make(chan error, 1)
			go func() {
				defer GinkgoRecover()
				_, _, err := scenario.Team.SavePipeline(atc.PipelineRef{Name: "some-pipeline"}, pipelineWith(newSource), scenario.Pipeline.ConfigVersion(), false)
				done <- err
			}()
			var err error
			Eventually(done).WithTimeout(lockOrderWait).Should(Receive(&err), "the pipeline save waited on a row another transaction holds")
			Expect(err).ToNot(HaveOccurred())
		}

		It("commits the save without waiting while the scope is held FOR UPDATE, keeping the history", func() {
			hold(`SELECT 1 FROM resource_config_scopes WHERE id = $1 FOR UPDATE`, oldScopeID)

			saveWithoutWaiting()
			Expect(holder.Rollback()).To(Succeed())

			collect()
			Expect(configPresent(oldConfigID)).To(BeTrue(), "the config's clock was not stamped")
			Expect(versionCount(oldScopeID)).To(Equal(2))
			// The known limit: the held scope is kept but does not name the
			// resource.
			from, _ := leftBy(oldScopeID)
			Expect(from.Valid).To(BeFalse())
		})

		It("commits the save without waiting while the config is held FOR UPDATE", func() {
			hold(`SELECT 1 FROM resource_configs WHERE id = $1 FOR UPDATE`, oldConfigID)

			saveWithoutWaiting()
			Expect(holder.Rollback()).To(Succeed())

			from, _ := leftBy(oldScopeID)
			Expect(from.Valid).To(BeTrue(), "the scope stamp was skipped with the config stamp")
		})

		It("still stamps a scope a version writer holds FOR KEY SHARE", func() {
			hold(`SELECT 1 FROM resource_config_scopes WHERE id = $1 FOR KEY SHARE`, oldScopeID)

			saveWithoutWaiting()
			Expect(holder.Rollback()).To(Succeed())

			expectKeptAndNamed()
		})
	})

	Context("when the resource has a unique version history", func() {
		BeforeEach(func() {
			config := pipelineWith(oldSource)
			config.Resources[0].Type = dbtest.UniqueBaseResourceType
			scenario = dbtest.Setup(
				builder.WithPipeline(config),
				builder.WithResourceVersions("some-resource", atc.Version{"ref": "v1"}),
			)
			resource = scenario.Resource("some-resource")
			oldConfigID = resource.ResourceConfigID()
			oldScopeID = resource.ResourceConfigScopeID()
		})

		It("still deprecates the old scope on the next check, naming the resource", func() {
			config := pipelineWith(newSource)
			config.Resources[0].Type = dbtest.UniqueBaseResourceType
			scenario.Run(builder.WithPipeline(config))

			from, deprecated := leftBy(oldScopeID)
			Expect(deprecated).To(BeFalse())
			Expect(from.Valid).To(BeFalse(), "a save names the resource only on a shared scope it left")

			resource = scenario.Resource("some-resource")
			newConfig, err := resourceConfigFactory.FindOrCreateResourceConfig(dbtest.UniqueBaseResourceType, newSource, nil)
			Expect(err).ToNot(HaveOccurred())
			scope, err := newConfig.FindOrCreateScope(resource.TeamID(), intptr(resource.ID()))
			Expect(err).ToNot(HaveOccurred())
			Expect(resource.SetResourceConfigScope(scope)).To(Succeed())

			from, deprecated = leftBy(oldScopeID)
			Expect(deprecated).To(BeTrue())
			Expect(int(from.Int64)).To(Equal(resource.ID()))
		})
	})
})
