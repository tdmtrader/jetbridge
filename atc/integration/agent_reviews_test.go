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

const agentReviewPublishTokenForTest = "integration-token"

var _ = Describe("Agent Reviews API", func() {
	BeforeEach(func() {
		cmd.AgentReviewPublishToken = agentReviewPublishTokenForTest
	})

	It("accepts a published review, rejects a bad token, and serves it back by build and by team", func() {
		client := login(atcURL, "test", "test")

		build, err := client.Team("main").CreateBuild(atc.Plan{})
		Expect(err).NotTo(HaveOccurred())
		Expect(build.ID).To(BeNumerically(">", 0))

		reviewBody := []byte(`{
			"build_id": ` + strconv.Itoa(build.ID) + `,
			"review": {
				"schema_version": "1.0.0",
				"metadata": {"repo": "itest", "commit": "deadbeef"},
				"score": {"value": 8, "max": 10, "pass": true},
				"proven_issues": [],
				"observations": [],
				"summary": "clean"
			}
		}`)

		By("rejecting a submission with the wrong bearer token")
		{
			resp := postAgentReview(atcURL, "wrong-token", reviewBody)
			Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))
		}

		By("accepting a submission with the correct bearer token")
		{
			resp := postAgentReview(atcURL, agentReviewPublishTokenForTest, reviewBody)
			Expect(resp.StatusCode).To(Equal(http.StatusCreated))
		}

		By("returning the review from the per-build endpoint")
		{
			httpClient := client.HTTPClient()
			req, err := http.NewRequest("GET", atcURL+"/api/v1/builds/"+strconv.Itoa(build.ID)+"/agent-reviews", nil)
			Expect(err).NotTo(HaveOccurred())

			resp, err := httpClient.Do(req)
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			var reviews []map[string]interface{}
			Expect(json.NewDecoder(resp.Body).Decode(&reviews)).To(Succeed())
			Expect(reviews).To(HaveLen(1))
			Expect(reviews[0]["repo"]).To(Equal("itest"))
			Expect(reviews[0]["commit_sha"]).To(Equal("deadbeef"))
			Expect(reviews[0]["score"]).To(Equal(8.0))
		}

		By("returning the review from the per-team endpoint")
		{
			httpClient := client.HTTPClient()
			req, err := http.NewRequest("GET", atcURL+"/api/v1/teams/main/agent-reviews", nil)
			Expect(err).NotTo(HaveOccurred())

			resp, err := httpClient.Do(req)
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			var reviews []map[string]interface{}
			Expect(json.NewDecoder(resp.Body).Decode(&reviews)).To(Succeed())

			found := false
			for _, r := range reviews {
				if r["repo"] == "itest" && r["commit_sha"] == "deadbeef" {
					found = true
					Expect(r["score"]).To(Equal(8.0))
				}
			}
			Expect(found).To(BeTrue(), "expected to find the itest/deadbeef review in team listing")
		}
	})
})

// plainHTTPClient is a dedicated client for hitting the ATC directly.
// http.DefaultClient must not be used here: the ATC boot process
// (skyHttpClient in atc/atccmd/command.go) mutates http.DefaultClient's
// Transport in place to install a host-rewriting round tripper, so reusing
// it from test code sends requests down an unrelated (and here, broken)
// path.
var plainHTTPClient = &http.Client{}

func postAgentReview(atcURL, token string, body []byte) *http.Response {
	req, err := http.NewRequest("POST", atcURL+"/api/v1/agent/reviews", bytes.NewReader(body))
	Expect(err).NotTo(HaveOccurred())
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := plainHTTPClient.Do(req)
	Expect(err).NotTo(HaveOccurred())
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp
}
