package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db/dbtest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// A version id names one row of one version history. Through a resource's
// URL it reaches only that resource's own history: a version id of another
// history, the same team's or another team's, is a version the resource does
// not have.
var _ = Describe("Version ids of another history", func() {
	var (
		fixture     *versionsAPIFixture
		ownID       int
		foreignID   int
		otherTeamID int
	)

	versionID := func(scenario *dbtest.Scenario, resource string, version atc.Version) int {
		GinkgoHelper()
		return scenario.ResourceVersion(resource, version).ID()
	}

	BeforeEach(func() {
		scriptedAccess.authenticated = true
		scriptedAccess.authorized = true

		config := atc.Config{
			Resources: atc.ResourceConfigs{
				{Name: "resource-name", Type: dbtest.BaseResourceType, Source: atc.Source{"repository": "mine"}},
				{Name: "other-history", Type: dbtest.BaseResourceType, Source: atc.Source{"repository": "other"}},
			},
			Jobs: atc.JobConfigs{{
				Name: "admit",
				PlanSequence: []atc.Step{
					{Config: &atc.GetStep{Name: "resource-name"}},
					{Config: &atc.GetStep{Name: "other-history"}},
				},
			}},
		}
		fixture = newVersionsAPIFixture(atc.PipelineRef{Name: "a-pipeline"}, config)
		fixture.scenario.Run(
			fixture.builder.WithResourceVersions("resource-name", atc.Version{"ref": "mine"}),
			fixture.builder.WithResourceVersions("other-history", atc.Version{"ref": "secret"}),
		)
		ownID = versionID(fixture.scenario, "resource-name", atc.Version{"ref": "mine"})
		foreignID = versionID(fixture.scenario, "other-history", atc.Version{"ref": "secret"})

		otherTeam, err := fixture.database.Deps.teamFactory.CreateTeam(atc.Team{Name: "other-team"})
		Expect(err).NotTo(HaveOccurred())
		otherPipeline := fixture.database.SavePipeline(otherTeam, "other-pipeline", config)
		other := &dbtest.Scenario{Team: otherTeam, Pipeline: otherPipeline}
		other.Run(fixture.builder.WithResourceVersions("resource-name", atc.Version{"ref": "their-secret"}))
		otherTeamID = versionID(other, "resource-name", atc.Version{"ref": "their-secret"})
	})

	get := func(versionID int) *http.Response {
		GinkgoHelper()
		if fixture.server == nil {
			fixture.server = fixture.database.Serve()
		}
		path := fmt.Sprintf("/api/v1/teams/%s/pipelines/%s/resources/resource-name/versions/%d",
			fixture.team.Name(), fixture.pipeline.Name(), versionID)
		response, err := client.Get(fixture.server.URL + path)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(response.Body.Close()).To(Succeed()) })
		return response
	}

	disabledDigests := func(resourceName string) int {
		GinkgoHelper()
		var count int
		Expect(fixture.database.Conn.QueryRow(`
			SELECT count(*) FROM resource_disabled_versions WHERE resource_id = $1
		`, fixture.resource(resourceName).ID()).Scan(&count)).To(Succeed())
		return count
	}

	It("gets the resource's own version", func() {
		response := get(ownID)
		Expect(response.StatusCode).To(Equal(http.StatusOK))
		var version atc.ResourceVersion
		Expect(json.NewDecoder(response.Body).Decode(&version)).To(Succeed())
		Expect(version.Version).To(Equal(atc.Version{"ref": "mine"}))
	})

	DescribeTable("does not get a version of another history",
		func(id func() int) {
			Expect(get(id()).StatusCode).To(Equal(http.StatusNotFound))
		},
		Entry("the same team's", func() int { return foreignID }),
		Entry("another team's", func() int { return otherTeamID }),
	)

	DescribeTable("does not pin to a version of another history",
		func(id func() int) {
			response := fixture.requestVersionMutation("resource-name", id(), "pin")
			Expect(response.StatusCode).To(Equal(http.StatusNotFound))
			Expect(reloadVersionsAPIResource(fixture).CurrentPinnedVersion()).To(BeNil())
		},
		Entry("the same team's", func() int { return foreignID }),
		Entry("another team's", func() int { return otherTeamID }),
	)

	DescribeTable("does not disable or enable by a version of another history",
		func(id func() int) {
			response := fixture.requestVersionMutation("resource-name", id(), "disable")
			Expect(response.StatusCode).To(Equal(http.StatusNotFound))
			Expect(disabledDigests("resource-name")).To(BeZero())

			response = fixture.requestVersionMutation("resource-name", id(), "enable")
			Expect(response.StatusCode).To(Equal(http.StatusNotFound))
		},
		Entry("the same team's", func() int { return foreignID }),
		Entry("another team's", func() int { return otherTeamID }),
	)

	DescribeTable("lists no builds for a version of another history, as for a version that does not exist",
		func(relationship string) {
			response := fixture.requestVersionBuilds("resource-name", strconv.Itoa(foreignID), relationship)
			Expect(response.StatusCode).To(Equal(http.StatusOK))
			Expect(decodeVersionsAPIBuilds(response)).To(BeEmpty())
		},
		Entry("input to", "input_to"),
		Entry("output of", "output_of"),
	)

	It("pages from no version of another history", func() {
		for _, bound := range []string{"from", "to"} {
			response := fixture.requestVersions("resource-name", url.Values{bound: {strconv.Itoa(foreignID)}, "limit": {"10"}})
			Expect(response.StatusCode).To(Equal(http.StatusOK))
			var versions []atc.ResourceVersion
			Expect(json.NewDecoder(response.Body).Decode(&versions)).To(Succeed())
			Expect(versions).To(BeEmpty(), bound)
		}
	})

	It("still finds a version of the other resource through its own URL", func() {
		Expect(fixture.requestVersionMutation("other-history", foreignID, "disable").StatusCode).To(Equal(http.StatusOK))
		Expect(disabledDigests("other-history")).To(Equal(1))
	})

})
