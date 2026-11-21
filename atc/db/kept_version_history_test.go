package db_test

import (
	"context"
	"database/sql"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"
	gocache "github.com/patrickmn/go-cache"

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

// A resource that left a shared history (its source was edited, or a
// credential behind it rotated) lists that history and may copy it into its
// current one when it is that history's only active user.
var _ = Describe("Copying back a version history the resource left", func() {
	var (
		scenario      *dbtest.Scenario
		upstreamBuild db.Build
		oldScopeID    int
		newScope      db.ResourceConfigScope

		oldSource = atc.Source{"credential": "before-rotation"}
		newSource = atc.Source{"credential": "after-rotation"}
	)

	pipelineConfig := atc.Config{
		ResourceTypes: atc.ResourceTypes{
			{Name: "some-type", Type: dbtest.BaseResourceType, Source: newSource},
		},
		Resources: atc.ResourceConfigs{
			{Name: "some-resource", Type: dbtest.BaseResourceType, Source: oldSource},
			{Name: "teammate", Type: dbtest.BaseResourceType, Source: atc.Source{"teammate": "own"}},
		},
		Jobs: atc.JobConfigs{
			{
				Name:         "upstream-job",
				PlanSequence: []atc.Step{{Config: &atc.GetStep{Name: "some-resource"}}},
			},
		},
	}

	resource := func() db.Resource { return scenario.Resource("some-resource") }

	moveOnto := func(r db.Resource, source atc.Source) db.ResourceConfigScope {
		GinkgoHelper()
		config, err := resourceConfigFactory.FindOrCreateResourceConfig(dbtest.BaseResourceType, source, nil)
		Expect(err).ToNot(HaveOccurred())
		scope, err := config.FindOrCreateScope(r.TeamID(), intptr(r.ID()))
		Expect(err).ToNot(HaveOccurred())
		Expect(r.SetResourceConfigScope(scope)).To(Succeed())
		return scope
	}

	refs := func(scopeID int) []string {
		GinkgoHelper()
		rows, err := dbConn.Query(`
			SELECT version->>'ref' FROM resource_config_versions
			WHERE resource_config_scope_id = $1 ORDER BY check_order
		`, scopeID)
		Expect(err).ToNot(HaveOccurred())
		defer rows.Close()
		out := []string{}
		for rows.Next() {
			var ref string
			Expect(rows.Scan(&ref)).To(Succeed())
			out = append(out, ref)
		}
		return out
	}

	versionRows := func(scopeID int) []int {
		GinkgoHelper()
		rows, err := dbConn.Query(`SELECT id FROM resource_config_versions WHERE resource_config_scope_id = $1 ORDER BY id`, scopeID)
		Expect(err).ToNot(HaveOccurred())
		defer rows.Close()
		ids := []int{}
		for rows.Next() {
			var id int
			Expect(rows.Scan(&id)).To(Succeed())
			ids = append(ids, id)
		}
		return ids
	}

	listedIDs := func() []int {
		GinkgoHelper()
		listed, err := resource().DeprecatedScopes()
		Expect(err).ToNot(HaveOccurred())
		ids := []int{}
		for _, s := range listed {
			ids = append(ids, s.ID)
		}
		return ids
	}

	BeforeEach(func() {
		scenario = dbtest.Setup(
			builder.WithPipeline(pipelineConfig),
			builder.WithResourceVersions("some-resource", atc.Version{"ref": "v1"}, atc.Version{"ref": "v2"}, atc.Version{"ref": "v3"}),
			builder.WithJobBuild(&upstreamBuild, "upstream-job", dbtest.JobInputs{
				{Name: "some-resource", Version: atc.Version{"ref": "v1"}},
			}, dbtest.JobOutputs{}),
		)
		oldScopeID = resource().ResourceConfigScopeID()

		// The credential rotates: the next check finds only the latest
		// version, in a new, empty history.
		newScope = moveOnto(resource(), newSource)
		Expect(newScope.ID()).ToNot(Equal(oldScopeID))
		Expect(newScope.SaveVersions(db.SpanContext{}, []atc.Version{{"ref": "v3"}})).To(Succeed())
	})

	It("lists the history it left with its version count and last check, and not its current one", func() {
		listed, err := resource().DeprecatedScopes()
		Expect(err).ToNot(HaveOccurred())
		Expect(listed).To(HaveLen(1))
		Expect(listed[0].ID).To(Equal(oldScopeID))
		Expect(listed[0].Versions).To(Equal(3))
		Expect(listed[0].DeprecatedAt).To(BeZero())
		Expect(listed[0].LastCheckEnd).To(BeTemporally("~", time.Now(), time.Minute))
	})

	It("does not list a history it left that holds no versions", func() {
		_, err := dbConn.Exec(`DELETE FROM resource_config_versions WHERE resource_config_scope_id = $1`, newScope.ID())
		Expect(err).ToNot(HaveOccurred())
		moveOnto(resource(), atc.Source{"credential": "third"})
		Expect(listedIDs()).To(ConsistOf(oldScopeID))
	})

	It("lists both earlier histories after two rotations", func() {
		third := moveOnto(resource(), atc.Source{"credential": "third"})
		Expect(third.SaveVersions(db.SpanContext{}, []atc.Version{{"ref": "v4"}})).To(Succeed())
		Expect(listedIDs()).To(ConsistOf(oldScopeID, newScope.ID()))
	})

	It("copies the history beneath the version both hold, and pins, disabled versions and passed constraints resolve again", func() {
		// A pin of v1 and v2 disabled, made while the resource was on the old
		// history: both name the resource and a version digest.
		_, err := dbConn.Exec(`INSERT INTO resource_pins (resource_id, version, comment_text, config) VALUES ($1, '{"ref":"v1"}', '', false)`, resource().ID())
		Expect(err).ToNot(HaveOccurred())
		_, err = dbConn.Exec(`
			INSERT INTO resource_disabled_versions (resource_id, version_digest)
			SELECT $1, COALESCE(version_md5, version_sha256) FROM resource_config_versions
			WHERE resource_config_scope_id = $2 AND version->>'ref' = 'v2'
		`, resource().ID(), oldScopeID)
		Expect(err).ToNot(HaveOccurred())

		_, found, err := resource().FindVersion(resource().APIPinnedVersion())
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeFalse(), "the pinned version resolves before the copy")

		versionsDB := db.NewVersionsDB(dbConn, 100, gocache.New(10*time.Second, 10*time.Second))
		outputs, err := versionsDB.SuccessfulBuildOutputs(context.TODO(), upstreamBuild.ID())
		Expect(err).ToNot(HaveOccurred())
		Expect(outputs).To(HaveLen(1))
		exists, err := versionsDB.VersionExists(context.TODO(), resource().ID(), outputs[0].Version)
		Expect(err).ToNot(HaveOccurred())
		Expect(exists).To(BeFalse(), "the old build's input resolves before the copy")

		copied, err := resource().CopyVersionsFromScope(oldScopeID)
		Expect(err).ToNot(HaveOccurred())
		Expect(copied).To(Equal(2))
		Expect(refs(newScope.ID())).To(Equal([]string{"v1", "v2", "v3"}))

		latest, found, err := newScope.LatestVersion()
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(latest.Version()).To(Equal(db.Version{"ref": "v3"}))

		_, found, err = resource().FindVersion(resource().APIPinnedVersion())
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue(), "the pinned version does not resolve")

		versions, _, found, err := resource().Versions(db.Page{Limit: 10}, nil)
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		enabled := map[string]bool{}
		for _, v := range versions {
			enabled[v.Version["ref"]] = v.Enabled
		}
		Expect(enabled).To(Equal(map[string]bool{"v1": true, "v2": false, "v3": true}))

		exists, err = versionsDB.VersionExists(context.TODO(), resource().ID(), outputs[0].Version)
		Expect(err).ToNot(HaveOccurred())
		Expect(exists).To(BeTrue(), "the old build's input does not resolve")

		Expect(refs(oldScopeID)).To(Equal([]string{"v1", "v2", "v3"}), "the source keeps its rows")
	})

	It("refuses while another active resource of the team uses the current history", func() {
		Expect(moveOnto(scenario.Resource("teammate"), newSource).ID()).To(Equal(newScope.ID()))

		_, err := resource().CopyVersionsFromScope(oldScopeID)
		Expect(err).To(MatchError(db.ErrCopyIntoSharedScope))
		Expect(refs(newScope.ID())).To(Equal([]string{"v3"}))
	})

	It("refuses while an active resource type of the team is on the current history's config", func() {
		Expect(scenario.ResourceType("some-type").SetResourceConfigScope(newScope)).To(Succeed())

		_, err := resource().CopyVersionsFromScope(oldScopeID)
		Expect(err).To(MatchError(db.ErrCopyIntoSharedScope))
		Expect(refs(newScope.ID())).To(Equal([]string{"v3"}))
	})

	It("is not blocked by an inactive resource still pointing at the current history", func() {
		Expect(moveOnto(scenario.Resource("teammate"), newSource).ID()).To(Equal(newScope.ID()))
		teammateID := scenario.Resource("teammate").ID()
		config := pipelineConfig
		config.Resources = atc.ResourceConfigs{pipelineConfig.Resources[0]}
		scenario.Run(builder.WithPipeline(config))
		var active bool
		var scopeID sql.NullInt64
		Expect(dbConn.QueryRow(`SELECT active, resource_config_scope_id FROM resources WHERE id = $1`, teammateID).Scan(&active, &scopeID)).To(Succeed())
		Expect(active).To(BeFalse())
		Expect(int(scopeID.Int64)).To(Equal(newScope.ID()))

		copied, err := resource().CopyVersionsFromScope(oldScopeID)
		Expect(err).ToNot(HaveOccurred())
		Expect(copied).To(Equal(2))
	})

	It("neither lists nor copies another team's history, whatever its pointer says", func() {
		other := dbtest.Setup(builder.WithPipeline(pipelineConfig), builder.WithResourceVersions("some-resource", atc.Version{"ref": "theirs"}))
		theirScopeID := other.Resource("some-resource").ResourceConfigScopeID()
		Expect(theirScopeID).ToNot(Equal(oldScopeID))
		moveOnto(other.Resource("some-resource"), atc.Source{"credential": "theirs-moved"})
		_, err := dbConn.Exec(`UPDATE resource_config_scopes SET deprecated_from_resource_id = $2 WHERE id = $1`, theirScopeID, resource().ID())
		Expect(err).ToNot(HaveOccurred())

		Expect(listedIDs()).ToNot(ContainElement(theirScopeID))
		_, err = resource().CopyVersionsFromScope(theirScopeID)
		Expect(err).To(MatchError(db.ErrVersionHistoryNotFound))
		Expect(refs(newScope.ID())).To(Equal([]string{"v3"}))
	})

	It("answers not found when the history it left is collected before the copy", func() {
		Expect(listedIDs()).To(ConsistOf(oldScopeID))
		_, err := dbConn.Exec(`DELETE FROM resource_config_scopes WHERE id = $1`, oldScopeID)
		Expect(err).ToNot(HaveOccurred())

		_, err = resource().CopyVersionsFromScope(oldScopeID)
		Expect(err).To(MatchError(db.ErrVersionHistoryNotFound))
	})

	It("leaves another team's identical histories alone", func() {
		other := dbtest.Setup(builder.WithPipeline(pipelineConfig), builder.WithResourceVersions("some-resource", atc.Version{"ref": "v1"}, atc.Version{"ref": "v2"}, atc.Version{"ref": "v3"}))
		theirOld := other.Resource("some-resource").ResourceConfigScopeID()
		Expect(theirOld).ToNot(Equal(oldScopeID))
		theirNew := moveOnto(other.Resource("some-resource"), newSource)
		Expect(theirNew.SaveVersions(db.SpanContext{}, []atc.Version{{"ref": "v3"}})).To(Succeed())
		Expect(theirNew.ID()).ToNot(Equal(newScope.ID()))
		beforeOld, beforeNew := versionRows(theirOld), versionRows(theirNew.ID())

		_, err := resource().CopyVersionsFromScope(oldScopeID)
		Expect(err).ToNot(HaveOccurred())

		Expect(versionRows(theirOld)).To(Equal(beforeOld))
		Expect(versionRows(theirNew.ID())).To(Equal(beforeNew))
		Expect(refs(theirNew.ID())).To(Equal([]string{"v3"}))
	})
})
