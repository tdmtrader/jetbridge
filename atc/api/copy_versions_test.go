package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("PUT /api/v1/teams/:team_name/pipelines/:pipeline_name/resources/:resource_name/copy-versions", func() {
	var (
		fixture        *versionsAPIFixture
		resourceSource = atc.Source{"repo": "shared"}
		typeSource     = atc.Source{"image": "the-teams-own"}
	)

	refs := func(scopeID int) []string {
		rows, err := fixture.database.Conn.Query(`
			SELECT version->>'ref' FROM resource_config_versions
			WHERE resource_config_scope_id = $1 ORDER BY check_order
		`, scopeID)
		Expect(err).NotTo(HaveOccurred())
		defer db.Close(rows)

		out := []string{}
		for rows.Next() {
			var ref string
			Expect(rows.Scan(&ref)).To(Succeed())
			out = append(out, ref)
		}
		return out
	}

	moveTo := func(config db.ResourceConfig) db.ResourceConfigScope {
		resource := fixture.resource("some-resource")
		scope, err := config.FindOrCreateScope(db.NewIntPtr(resource.ID()))
		Expect(err).NotTo(HaveOccurred())
		Expect(resource.SetResourceConfigScope(scope)).To(Succeed())
		return scope
	}

	requestCopy := func(fromScopeID int) *http.Response {
		if fixture.server == nil {
			fixture.server = fixture.database.Serve()
		}
		path := fmt.Sprintf(
			"/api/v1/teams/%s/pipelines/%s/resources/some-resource/copy-versions",
			fixture.team.Name(), fixture.pipeline.Name(),
		)
		body, err := json.Marshal(map[string]int{"from_scope_id": fromScopeID})
		Expect(err).NotTo(HaveOccurred())
		request, err := http.NewRequest(http.MethodPut, fixture.server.URL+path, bytes.NewReader(body))
		Expect(err).NotTo(HaveOccurred())
		request.Header.Set("Content-Type", "application/json")
		response, err := client.Do(request)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(response.Body.Close()).To(Succeed()) })
		return response
	}

	BeforeEach(func() {
		scriptedAccess.authenticated = true
		scriptedAccess.authorized = true

		fixture = newVersionsAPIFixture(atc.PipelineRef{Name: "some-pipeline"}, atc.Config{
			ResourceTypes: atc.ResourceTypes{
				{Name: "some-custom-type", Type: dbtest.BaseResourceType, Source: typeSource},
			},
			Resources: atc.ResourceConfigs{
				{Name: "some-resource", Type: "some-custom-type", Source: resourceSource},
			},
		})
		fixture.scenario.Run(fixture.builder.WithBaseWorker())
	})

	Context("when the resource's current scope is shared and the source history is another type image's", func() {
		var (
			pollutedScopeID int
			sharedScope     db.ResourceConfigScope
		)

		BeforeEach(func() {
			build, err := fixture.team.CreateOneOffBuild()
			Expect(err).NotTo(HaveOccurred())
			cacheFactory := db.NewResourceCacheFactory(fixture.database.Conn, fixture.database.LockFactory)
			configFactory := fixture.database.Deps.resourceConfigFactory

			onImage := func(version string) db.ResourceConfig {
				cache, err := cacheFactory.FindOrCreateResourceCache(
					db.ForBuild(build.ID()), dbtest.BaseResourceType,
					atc.Version{"digest": version}, typeSource, nil, nil,
				)
				Expect(err).NotTo(HaveOccurred())
				config, err := configFactory.FindOrCreateResourceConfig("some-custom-type", resourceSource, cache)
				Expect(err).NotTo(HaveOccurred())
				return config
			}

			// The team's own type image finds whatever it likes.
			polluted := moveTo(onImage("1"))
			pollutedScopeID = polluted.ID()
			Expect(polluted.SaveVersions(db.SpanContext{}, []atc.Version{{"ref": "b3"}, {"ref": "EVIL"}})).To(Succeed())

			// A new image version deprecates that history, listed for the resource.
			moveTo(onImage("2"))

			// Another team's resource on the base type shares this scope.
			baseConfig, err := configFactory.FindOrCreateResourceConfig(dbtest.BaseResourceType, resourceSource, nil)
			Expect(err).NotTo(HaveOccurred())
			sharedScope, err = baseConfig.FindOrCreateScope(nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(sharedScope.SaveVersions(db.SpanContext{}, []atc.Version{{"ref": "b1"}, {"ref": "b2"}, {"ref": "b3"}})).To(Succeed())

			// The resource changes to the base type and joins it.
			Expect(moveTo(baseConfig).ID()).To(Equal(sharedScope.ID()))

			listed, err := fixture.resource("some-resource").DeprecatedScopes()
			Expect(err).NotTo(HaveOccurred())
			Expect(listed).NotTo(BeEmpty())
			Expect(listed[0].ID).To(Equal(pollutedScopeID))
		})

		It("refuses the copy with Conflict and a reason, and leaves the shared scope alone", func() {
			response := requestCopy(pollutedScopeID)
			Expect(response.StatusCode).To(Equal(http.StatusConflict))

			body, err := io.ReadAll(response.Body)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(body)).To(ContainSubstring("shared with other resources"))

			Expect(refs(sharedScope.ID())).To(Equal([]string{"b1", "b2", "b3"}))
		})
	})

	Context("when another team's resource uses the resource's current history", func() {
		var (
			oldScopeID   int
			currentScope db.ResourceConfigScope
		)

		BeforeEach(func() {
			build, err := fixture.team.CreateOneOffBuild()
			Expect(err).NotTo(HaveOccurred())
			cacheFactory := db.NewResourceCacheFactory(fixture.database.Conn, fixture.database.LockFactory)
			configFactory := fixture.database.Deps.resourceConfigFactory

			// A resolved type is keyed on its image's repository@digest.
			onDigest := func(digest string) db.ResourceConfig {
				resourceType, source, version := db.PinnedImage("registry.example/some-custom-type@sha256:" + digest)
				cache, err := cacheFactory.FindOrCreateResourceCache(db.ForBuild(build.ID()), resourceType, version, source, nil, nil)
				Expect(err).NotTo(HaveOccurred())
				config, err := configFactory.FindOrCreateResourceConfig("some-custom-type", resourceSource, cache)
				Expect(err).NotTo(HaveOccurred())
				return config
			}

			oldScope := moveTo(onDigest("d0"))
			oldScopeID = oldScope.ID()
			Expect(oldScope.SaveVersions(db.SpanContext{}, []atc.Version{{"ref": "v1"}, {"ref": "v2"}})).To(Succeed())

			// A new digest in the same repository carries the history and
			// deprecates the old one, listed for the resource.
			currentScope = moveTo(onDigest("d1"))
			Expect(refs(currentScope.ID())).To(Equal([]string{"v1", "v2"}))

			_, err = fixture.database.Conn.Exec(`
				INSERT INTO resource_config_versions (resource_config_scope_id, version, version_sha256, check_order)
				VALUES ($1, '{"ref":"EVIL"}', encode(digest('{"ref":"EVIL"}', 'sha256'), 'hex'), 3)
			`, oldScopeID)
			Expect(err).NotTo(HaveOccurred())

			// Another team's resource, on the same digest, joins the history.
			otherTeam, err := fixture.database.Deps.teamFactory.CreateTeam(atc.Team{Name: "other-team"})
			Expect(err).NotTo(HaveOccurred())
			otherPipeline, _, err := otherTeam.SavePipeline(atc.PipelineRef{Name: "other-pipeline"}, atc.Config{
				ResourceTypes: atc.ResourceTypes{
					{Name: "some-custom-type", Type: "registry-image", Source: atc.Source{"repository": "registry.example/some-custom-type"}},
				},
				Resources: atc.ResourceConfigs{
					{Name: "some-resource", Type: "some-custom-type", Source: resourceSource},
				},
			}, db.ConfigVersion(0), false)
			Expect(err).NotTo(HaveOccurred())
			otherResource, found, err := otherPipeline.Resource("some-resource")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(otherResource.SetResourceConfigScope(currentScope)).To(Succeed())

			listed, err := fixture.resource("some-resource").DeprecatedScopes()
			Expect(err).NotTo(HaveOccurred())
			Expect(listed).NotTo(BeEmpty())
			Expect(listed[0].ID).To(Equal(oldScopeID))
		})

		It("refuses the copy with Conflict and a reason, and leaves the history alone", func() {
			response := requestCopy(oldScopeID)
			Expect(response.StatusCode).To(Equal(http.StatusConflict))

			body, err := io.ReadAll(response.Body)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(body)).To(ContainSubstring("another team"))

			Expect(refs(currentScope.ID())).To(Equal([]string{"v1", "v2"}))
		})
	})
})
