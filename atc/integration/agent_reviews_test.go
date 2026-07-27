package integration_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"github.com/concourse/concourse/atc"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The v1 publishing route (POST /api/v1/agent/reviews) is gone: reviews reach
// the API only as sealed review/v1 snapshot projections written in-process.
// What is still reachable over HTTP is the read side, and its authorization is
// what these specs pin.
var _ = Describe("Agent Reviews API", func() {
	It("serves an empty per-build review list to an authorized team member", func() {
		client := login(atcURL, "test", "test")

		build, err := client.Team("main").CreateBuild(atc.Plan{})
		Expect(err).NotTo(HaveOccurred())
		Expect(build.ID).To(BeNumerically(">", 0))

		req, err := http.NewRequest("GET", atcURL+"/api/v1/builds/"+strconv.Itoa(build.ID)+"/agent-reviews", nil)
		Expect(err).NotTo(HaveOccurred())
		resp, err := client.HTTPClient().Do(req)
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(http.StatusOK))

		var reviews []map[string]any
		Expect(json.NewDecoder(resp.Body).Decode(&reviews)).To(Succeed())
		Expect(reviews).To(BeEmpty())
	})

	It("refuses to publish a review over HTTP", func() {
		client := login(atcURL, "test", "test")
		build, err := client.Team("main").CreateBuild(atc.Plan{})
		Expect(err).NotTo(HaveOccurred())

		req, err := http.NewRequest("POST", atcURL+"/api/v1/agent/reviews", bytes.NewBufferString(
			`{"build_id": `+strconv.Itoa(build.ID)+`, "review": {"schema_version":"1.0.0"}}`))
		Expect(err).NotTo(HaveOccurred())
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.HTTPClient().Do(req)
		Expect(err).NotTo(HaveOccurred())
		defer func() {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}()
		Expect(resp.StatusCode).To(Equal(http.StatusNotFound))
	})

	It("rejects a user with no access to the build's team", func() {
		client := login(atcURL, "test", "test")

		build, err := client.Team("main").CreateBuild(atc.Plan{})
		Expect(err).NotTo(HaveOccurred())
		Expect(build.ID).To(BeNumerically(">", 0))

		// v-user only has a role on "some-team", not "main", so it must
		// be rejected when reading a main-team build's agent reviews.
		setupTeam(atcURL, atc.Team{
			Name: "some-team",
			Auth: atc.TeamAuth{
				"viewer": map[string][]string{
					"users":  []string{"local:v-user"},
					"groups": []string{},
				},
			},
		})

		otherTeamClient := login(atcURL, "v-user", "v-user")
		httpClient := otherTeamClient.HTTPClient()
		req, err := http.NewRequest("GET", atcURL+"/api/v1/builds/"+strconv.Itoa(build.ID)+"/agent-reviews", nil)
		Expect(err).NotTo(HaveOccurred())

		resp, err := httpClient.Do(req)
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(http.StatusForbidden))
	})
})

// plainHTTPClient is a dedicated client for hitting the ATC directly.
// http.DefaultClient must not be used here: the ATC boot process
// (skyHttpClient in atc/atccmd/command.go) mutates http.DefaultClient's
// Transport in place to install a host-rewriting round tripper, so reusing
// it from test code sends requests down an unrelated (and here, broken)
// path.
var plainHTTPClient = &http.Client{}
