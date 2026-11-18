package db_test

import (
	"errors"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"
	"github.com/jackc/pgx/v5/pgconn"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// A resource config scope belongs to one team: two teams with identical
// resources or resource types each keep their own version history, and
// nothing one team does reaches the other's.
var _ = Describe("A version history belongs to one team", func() {
	const repository = "registry.example/team-scoped-type"

	var (
		resourceSource = atc.Source{"some": "source"}

		pipelineConfig = atc.Config{
			ResourceTypes: atc.ResourceTypes{
				{
					Name:   "custom-type",
					Type:   "registry-image",
					Source: atc.Source{"repository": repository},
				},
				{
					// A type on the custom type with the resource's source,
					// so it has the resource's config.
					Name:   "nested-type",
					Type:   "custom-type",
					Source: resourceSource,
				},
			},
			Resources: atc.ResourceConfigs{
				{
					Name:   "some-resource",
					Type:   "custom-type",
					Source: resourceSource,
				},
				{
					Name:   "git-resource",
					Type:   dbtest.BaseResourceType,
					Source: resourceSource,
				},
			},
			Jobs: atc.JobConfigs{
				{
					Name: "some-job",
					PlanSequence: []atc.Step{
						{Config: &atc.GetStep{Name: "some-resource"}},
						{Config: &atc.GetStep{Name: "git-resource"}},
					},
				},
			},
		}

		teamA, teamB *dbtest.Scenario
		cacheUser    db.Build
	)

	scopeFor := func(config db.ResourceConfig, team db.Team, resourceID *int) db.ResourceConfigScope {
		GinkgoHelper()
		scope, err := config.FindOrCreateScope(team.ID(), resourceID)
		Expect(err).ToNot(HaveOccurred())
		return scope
	}

	pinnedImage := func(digest string) db.ResourceCache {
		GinkgoHelper()
		resourceType, source, version := db.PinnedImage(repository + "@sha256:" + digest)
		cache, err := resourceCacheFactory.FindOrCreateResourceCache(
			db.ForBuild(cacheUser.ID()), resourceType, version, source, nil, nil,
		)
		Expect(err).ToNot(HaveOccurred())
		return cache
	}

	configOnDigest := func(digest string) db.ResourceConfig {
		GinkgoHelper()
		config, err := resourceConfigFactory.FindOrCreateResourceConfig("custom-type", resourceSource, pinnedImage(digest))
		Expect(err).ToNot(HaveOccurred())
		return config
	}

	gitConfig := func() db.ResourceConfig {
		GinkgoHelper()
		config, err := resourceConfigFactory.FindOrCreateResourceConfig(dbtest.BaseResourceType, resourceSource, nil)
		Expect(err).ToNot(HaveOccurred())
		return config
	}

	attach := func(scenario *dbtest.Scenario, name string, config db.ResourceConfig) db.ResourceConfigScope {
		GinkgoHelper()
		resource := scenario.Resource(name)
		scope := scopeFor(config, scenario.Team, intptr(resource.ID()))
		Expect(resource.SetResourceConfigScope(scope)).To(Succeed())
		return scope
	}

	versionsOf := func(scopeID int) []string {
		GinkgoHelper()
		rows, err := dbConn.Query(`
			SELECT version->>'ref' FROM resource_config_versions
			WHERE resource_config_scope_id = $1
			ORDER BY check_order
		`, scopeID)
		Expect(err).ToNot(HaveOccurred())
		defer rows.Close()

		refs := []string{}
		for rows.Next() {
			var ref string
			Expect(rows.Scan(&ref)).To(Succeed())
			refs = append(refs, ref)
		}
		return refs
	}

	BeforeEach(func() {
		teamA = dbtest.Setup(
			builder.WithPipeline(pipelineConfig),
			builder.WithBaseWorker(),
		)
		teamB = dbtest.Setup(builder.WithPipeline(pipelineConfig))
		Expect(teamB.Team.ID()).ToNot(Equal(teamA.Team.ID()))

		var err error
		cacheUser, err = teamA.Team.CreateOneOffBuild()
		Expect(err).ToNot(HaveOccurred())
	})

	Context("when one team's history carried to a new type digest", func() {
		var carried db.ResourceConfigScope

		BeforeEach(func() {
			old := attach(teamA, "some-resource", configOnDigest("d0"))
			Expect(old.SaveVersions(db.SpanContext{}, []atc.Version{{"ref": "v1"}, {"ref": "v2"}, {"ref": "v3"}})).To(Succeed())

			carried = attach(teamA, "some-resource", configOnDigest("d1"))
			Expect(versionsOf(carried.ID())).To(Equal([]string{"v1", "v2", "v3"}))
		})

		It("gives another team's resource that attaches afterwards a history of its own, without the carried versions", func() {
			theirs := attach(teamB, "some-resource", configOnDigest("d1"))

			Expect(theirs.ID()).ToNot(Equal(carried.ID()))
			Expect(teamB.Resource("some-resource").ResourceConfigScopeID()).To(Equal(theirs.ID()))
			Expect(versionsOf(theirs.ID())).To(BeEmpty())

			_, found, err := teamB.Resource("some-resource").FindVersion(atc.Version{"ref": "v3"})
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeFalse())
		})

		It("gives another team's resource type that attaches afterwards a history of its own", func() {
			nested := teamB.ResourceType("nested-type")
			Expect(nested.SetResourceConfigScope(scopeFor(configOnDigest("d1"), teamB.Team, nil))).To(Succeed())

			nested = teamB.ResourceType("nested-type")
			Expect(nested.ResourceConfigScopeID()).ToNot(BeZero())
			Expect(nested.ResourceConfigScopeID()).ToNot(Equal(carried.ID()))
			Expect(versionsOf(nested.ResourceConfigScopeID())).To(BeEmpty())
		})
	})

	It("gives two teams' concurrent puts of one config a scope each, holding only its own version", func() {
		buildA, err := teamA.Job("some-job").CreateBuild("someone")
		Expect(err).ToNot(HaveOccurred())
		buildB, err := teamB.Job("some-job").CreateBuild("someone")
		Expect(err).ToNot(HaveOccurred())

		put := func(build db.Build, ref string) chan error {
			done := make(chan error, 1)
			go func() {
				defer GinkgoRecover()
				done <- build.SaveOutput(dbtest.BaseResourceType, nil, resourceSource, atc.Version{"ref": ref}, nil, "out", "git-resource")
			}()
			return done
		}

		doneA := put(buildA, "put-a")
		doneB := put(buildB, "put-b")
		Eventually(doneA, 10).Should(Receive(BeNil()))
		Eventually(doneB, 10).Should(Receive(BeNil()))

		scopeA := teamA.Resource("git-resource").ResourceConfigScopeID()
		scopeB := teamB.Resource("git-resource").ResourceConfigScopeID()
		Expect(scopeA).ToNot(Equal(scopeB))
		Expect(versionsOf(scopeA)).To(Equal([]string{"put-a"}))
		Expect(versionsOf(scopeB)).To(Equal([]string{"put-b"}))
	})

	It("refuses pointing a resource at another team's scope", func() {
		theirs := attach(teamA, "git-resource", gitConfig())

		_, err := dbConn.Exec(`UPDATE resources SET resource_config_scope_id = $1 WHERE id = $2`,
			theirs.ID(), teamB.Resource("git-resource").ID())
		Expect(err).To(HaveOccurred())
		var pgErr *pgconn.PgError
		Expect(errors.As(err, &pgErr)).To(BeTrue(), "%v", err)
		Expect(pgErr.Code).To(Equal("23514"))
	})

	It("leaves another team's history when one team clears its versions", func() {
		mine := attach(teamA, "git-resource", gitConfig())
		Expect(mine.SaveVersions(db.SpanContext{}, []atc.Version{{"ref": "a1"}})).To(Succeed())
		theirs := attach(teamB, "git-resource", gitConfig())
		Expect(theirs.SaveVersions(db.SpanContext{}, []atc.Version{{"ref": "b1"}})).To(Succeed())

		cleared, err := teamA.Resource("git-resource").ClearVersions()
		Expect(err).ToNot(HaveOccurred())
		Expect(cleared).To(BeEquivalentTo(1))

		Expect(versionsOf(teamB.Resource("git-resource").ResourceConfigScopeID())).To(Equal([]string{"b1"}))
	})

	Context("given the row id of a version in another team's history", func() {
		var theirVersionID int

		BeforeEach(func() {
			attach(teamA, "git-resource", gitConfig())
			attach(teamB, "git-resource", gitConfig())
			theirs := teamB.Resource("git-resource").ResourceConfigScopeID()
			Expect(dbConn.QueryRow(`
				INSERT INTO resource_config_versions (resource_config_scope_id, version, version_sha256, metadata, check_order)
				VALUES ($1, '{"ref":"secret"}', encode(digest('{"ref":"secret"}', 'sha256'), 'hex'), '[{"name":"m","value":"secret"}]', 1)
				RETURNING id
			`, theirs).Scan(&theirVersionID)).To(Succeed())
		})

		It("does not read it through the team's pipeline", func() {
			_, found, err := teamA.Pipeline.ResourceVersion(teamA.Resource("git-resource").ID(), theirVersionID)
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeFalse())
		})

		It("does not pin to it", func() {
			found, err := teamA.Resource("git-resource").PinVersion(theirVersionID)
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeFalse())
			Expect(teamA.Resource("git-resource").CurrentPinnedVersion()).To(BeNil())
		})

		It("does not disable or enable by it", func() {
			Expect(teamA.Resource("git-resource").DisableVersion(theirVersionID)).To(MatchError(db.ErrResourceVersionNotFound))
			Expect(teamA.Resource("git-resource").EnableVersion(theirVersionID)).To(MatchError(db.ErrResourceVersionNotFound))

			var disabled int
			Expect(dbConn.QueryRow(`SELECT count(*) FROM resource_disabled_versions`).Scan(&disabled)).To(Succeed())
			Expect(disabled).To(BeZero())
		})

		It("lists no builds through it", func() {
			builds, err := teamA.Pipeline.GetBuildsWithVersionAsInput(teamA.Resource("git-resource").ID(), theirVersionID)
			Expect(err).ToNot(HaveOccurred())
			Expect(builds).To(BeEmpty())
		})
	})

	It("names only the team's own resources and types as sharing a history", func() {
		attach(teamA, "git-resource", gitConfig())
		attach(teamB, "git-resource", gitConfig())

		shared, err := teamA.Resource("git-resource").SharedResourcesAndTypes()
		Expect(err).ToNot(HaveOccurred())
		for _, r := range shared.Resources {
			Expect(r.TeamName).To(Equal(teamA.Team.Name()))
		}
		Expect(shared.Resources).To(HaveLen(1))
	})
})
