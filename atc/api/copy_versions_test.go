package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

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
		scope, err := config.FindOrCreateScope(resource.TeamID(), db.NewIntPtr(resource.ID()))
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
				{Name: "teammate", Type: dbtest.BaseResourceType, Source: resourceSource},
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

			// Another of the team's resources on the base type shares this scope.
			baseConfig, err := configFactory.FindOrCreateResourceConfig(dbtest.BaseResourceType, resourceSource, nil)
			Expect(err).NotTo(HaveOccurred())
			sharedScope, err = baseConfig.FindOrCreateScope(fixture.team.ID(), nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(sharedScope.SaveVersions(db.SpanContext{}, []atc.Version{{"ref": "b1"}, {"ref": "b2"}, {"ref": "b3"}})).To(Succeed())

			// The resource changes to the base type and joins it.
			Expect(moveTo(baseConfig).ID()).To(Equal(sharedScope.ID()))
			teammate := fixture.resource("teammate")
			Expect(teammate.SetResourceConfigScope(sharedScope)).To(Succeed())

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

	Context("when the resource left a shared history for another config", func() {
		var (
			oldScopeID int
			newScope   db.ResourceConfigScope
		)

		onSource := func(source atc.Source) db.ResourceConfig {
			config, err := fixture.database.Deps.resourceConfigFactory.FindOrCreateResourceConfig(dbtest.BaseResourceType, source, nil)
			Expect(err).NotTo(HaveOccurred())
			return config
		}

		requestList := func() []map[string]any {
			if fixture.server == nil {
				fixture.server = fixture.database.Serve()
			}
			path := fmt.Sprintf(
				"/api/v1/teams/%s/pipelines/%s/resources/some-resource/deprecated-scopes",
				fixture.team.Name(), fixture.pipeline.Name(),
			)
			response, err := client.Get(fixture.server.URL + path)
			Expect(err).NotTo(HaveOccurred())
			defer response.Body.Close()
			Expect(response.StatusCode).To(Equal(http.StatusOK))
			var listed []map[string]any
			Expect(json.NewDecoder(response.Body).Decode(&listed)).To(Succeed())
			return listed
		}

		BeforeEach(func() {
			old := moveTo(onSource(atc.Source{"credential": "before"}))
			oldScopeID = old.ID()
			Expect(old.SaveVersions(db.SpanContext{}, []atc.Version{{"ref": "v1"}, {"ref": "v2"}})).To(Succeed())
			_, err := fixture.database.Conn.Exec(`UPDATE resource_config_scopes SET last_check_end_time = '2026-11-01T10:00:00Z' WHERE id = $1`, oldScopeID)
			Expect(err).NotTo(HaveOccurred())

			newScope = moveTo(onSource(atc.Source{"credential": "after"}))
			Expect(newScope.SaveVersions(db.SpanContext{}, []atc.Version{{"ref": "v2"}})).To(Succeed())
		})

		It("lists the history it left, with its version count and last check", func() {
			listed := requestList()
			Expect(listed).To(HaveLen(1))
			Expect(listed[0]).To(HaveKeyWithValue("id", BeNumerically("==", oldScopeID)))
			Expect(listed[0]).To(HaveKeyWithValue("versions", BeNumerically("==", 2)))
			Expect(listed[0]).To(HaveKeyWithValue("last_checked", "2026-11-01T10:00:00Z"))
			Expect(listed[0]).To(HaveKeyWithValue("deprecated_at", ""))
		})

		It("copies it into the history the resource alone uses", func() {
			response := requestCopy(oldScopeID)
			Expect(response.StatusCode).To(Equal(http.StatusOK))

			var copied atc.CopyVersionsResponse
			Expect(json.NewDecoder(response.Body).Decode(&copied)).To(Succeed())
			Expect(copied.VersionsCopied).To(Equal(1))
			Expect(refs(newScope.ID())).To(Equal([]string{"v1", "v2"}))
		})

		It("refuses with Conflict while a teammate uses the current history", func() {
			Expect(fixture.resource("teammate").SetResourceConfigScope(newScope)).To(Succeed())

			response := requestCopy(oldScopeID)
			Expect(response.StatusCode).To(Equal(http.StatusConflict))
			body, err := io.ReadAll(response.Body)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(body)).To(ContainSubstring("shared with other resources"))
			Expect(refs(newScope.ID())).To(Equal([]string{"v2"}))
		})

		It("answers Not Found when the history is collected between the listing and the copy", func() {
			// The collector, on its own connection, deletes the source scope
			// and holds it until the copy is waiting for it.
			collectorConn := postgresRunner.OpenConn()
			DeferCleanup(collectorConn.Close)
			collector, err := collectorConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() { _ = collector.Rollback() })
			_, err = collector.Exec(`DELETE FROM resource_config_scopes WHERE id = $1`, oldScopeID)
			Expect(err).NotTo(HaveOccurred())

			if fixture.server == nil {
				fixture.server = fixture.database.Serve()
			}
			done := make(chan int, 1)
			go func() {
				defer GinkgoRecover()
				done <- requestCopy(oldScopeID).StatusCode
			}()
			Consistently(done, time.Second).ShouldNot(Receive())
			Expect(collector.Commit()).To(Succeed())

			var status int
			Eventually(done, 10*time.Second).Should(Receive(&status))
			Expect(status).To(Equal(http.StatusNotFound))
			Expect(refs(newScope.ID())).To(Equal([]string{"v2"}))
		})
	})

	Context("when another team's identical resource is on the resource's current digest", func() {
		var (
			oldScopeID   int
			currentScope db.ResourceConfigScope
			theirScope   db.ResourceConfigScope
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

			// Another team's resource, on the same digest, keeps a history of
			// its own: it cannot join this team's.
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
			Expect(otherResource.SetResourceConfigScope(currentScope)).To(MatchError(ContainSubstring("of another team")))
			theirScope, err = currentScope.ResourceConfig().FindOrCreateScope(otherResource.TeamID(), db.NewIntPtr(otherResource.ID()))
			Expect(err).NotTo(HaveOccurred())
			Expect(theirScope.ID()).NotTo(Equal(currentScope.ID()))
			Expect(otherResource.SetResourceConfigScope(theirScope)).To(Succeed())

			listed, err := fixture.resource("some-resource").DeprecatedScopes()
			Expect(err).NotTo(HaveOccurred())
			Expect(listed).NotTo(BeEmpty())
			Expect(listed[0].ID).To(Equal(oldScopeID))
		})

		It("copies into the team's own history, and leaves the other team's alone", func() {
			response := requestCopy(oldScopeID)
			Expect(response.StatusCode).To(Equal(http.StatusOK))

			var copied atc.CopyVersionsResponse
			Expect(json.NewDecoder(response.Body).Decode(&copied)).To(Succeed())
			Expect(copied.VersionsCopied).To(Equal(1))

			Expect(refs(currentScope.ID())).To(Equal([]string{"v1", "v2", "EVIL"}))
			Expect(refs(theirScope.ID())).To(BeEmpty())
		})
	})
})
