package integration_test

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/concourse/concourse/atc"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const staticPublishTokenForPrincipalTest = "integration-static-token"

var _ = Describe("Agent Principals API", func() {
	BeforeEach(func() {
		cmd.AgentReviewPublishToken = staticPublishTokenForPrincipalTest
	})

	reviewBodyFor := func(buildID int, commit string) []byte {
		return []byte(`{
			"build_id": ` + strconv.Itoa(buildID) + `,
			"review": {
				"schema_version": "1.0.0",
				"metadata": {"repo": "itest", "commit": "` + commit + `"},
				"score": {"value": 8, "max": 10, "pass": true},
				"proven_issues": [],
				"observations": [],
				"summary": "clean"
			}
		}`)
	}

	mintPrincipal := func(httpClient *http.Client, body string) (*http.Response, map[string]any) {
		req, err := http.NewRequest("POST", atcURL+"/api/v1/agent/principals", strings.NewReader(body))
		Expect(err).NotTo(HaveOccurred())
		req.Header.Set("Content-Type", "application/json")
		resp, err := httpClient.Do(req)
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		var decoded map[string]any
		json.NewDecoder(resp.Body).Decode(&decoded)
		return resp, decoded
	}

	It("mints, uses, attributes, and revokes scoped principals", func() {
		client := login(atcURL, "test", "test")
		httpClient := client.HTTPClient()

		By("minting a reviews:write principal as admin")
		resp, created := mintPrincipal(httpClient,
			`{"name": "itest-reviewer", "description": "integration", "scopes": ["reviews:write"]}`)
		Expect(resp.StatusCode).To(Equal(http.StatusCreated))
		token, _ := created["token"].(string)
		Expect(token).To(HavePrefix("cap1."))
		Expect(created["token_prefix"]).To(Equal(token[:12]))

		By("publishing a review with the principal token")
		build, err := client.Team("main").CreateBuild(atc.Plan{})
		Expect(err).NotTo(HaveOccurred())
		pub := postAgentReview(atcURL, token, reviewBodyFor(build.ID, "cafe0001"))
		Expect(pub.StatusCode).To(Equal(http.StatusCreated))

		By("recording the writing principal on the review row")
		req, err := http.NewRequest("GET", atcURL+"/api/v1/builds/"+strconv.Itoa(build.ID)+"/agent-reviews", nil)
		Expect(err).NotTo(HaveOccurred())
		getResp, err := httpClient.Do(req)
		Expect(err).NotTo(HaveOccurred())
		defer getResp.Body.Close()
		var reviews []map[string]any
		Expect(json.NewDecoder(getResp.Body).Decode(&reviews)).To(Succeed())
		Expect(reviews).To(HaveLen(1))
		Expect(reviews[0]["submitted_by"]).To(Equal("itest-reviewer"))

		By("still accepting the static token during the dual-accept window, attributed to legacy-publish")
		build2, err := client.Team("main").CreateBuild(atc.Plan{})
		Expect(err).NotTo(HaveOccurred())
		pub = postAgentReview(atcURL, staticPublishTokenForPrincipalTest, reviewBodyFor(build2.ID, "cafe0002"))
		Expect(pub.StatusCode).To(Equal(http.StatusCreated))

		req, err = http.NewRequest("GET", atcURL+"/api/v1/builds/"+strconv.Itoa(build2.ID)+"/agent-reviews", nil)
		Expect(err).NotTo(HaveOccurred())
		getResp2, err := httpClient.Do(req)
		Expect(err).NotTo(HaveOccurred())
		defer getResp2.Body.Close()
		reviews = nil
		Expect(json.NewDecoder(getResp2.Body).Decode(&reviews)).To(Succeed())
		Expect(reviews).To(HaveLen(1))
		Expect(reviews[0]["submitted_by"]).To(Equal("legacy-publish"))

		By("rejecting a wrong-scope principal with 401")
		resp, wrongScope := mintPrincipal(httpClient,
			`{"name": "itest-ticketer", "scopes": ["tickets:read"]}`)
		Expect(resp.StatusCode).To(Equal(http.StatusCreated))
		pub = postAgentReview(atcURL, wrongScope["token"].(string), reviewBodyFor(build.ID, "cafe0003"))
		Expect(pub.StatusCode).To(Equal(http.StatusUnauthorized))

		By("rejecting a principal revoked before first use")
		resp, doomed := mintPrincipal(httpClient,
			`{"name": "itest-doomed", "scopes": ["reviews:write"]}`)
		Expect(resp.StatusCode).To(Equal(http.StatusCreated))
		doomedID := strconv.Itoa(int(doomed["id"].(float64)))

		req, err = http.NewRequest("DELETE", atcURL+"/api/v1/agent/principals/"+doomedID, nil)
		Expect(err).NotTo(HaveOccurred())
		delResp, err := httpClient.Do(req)
		Expect(err).NotTo(HaveOccurred())
		delResp.Body.Close()
		Expect(delResp.StatusCode).To(Equal(http.StatusNoContent))

		pub = postAgentReview(atcURL, doomed["token"].(string), reviewBodyFor(build.ID, "cafe0004"))
		Expect(pub.StatusCode).To(Equal(http.StatusUnauthorized))

		By("listing principals with revocation state and without token material")
		req, err = http.NewRequest("GET", atcURL+"/api/v1/agent/principals", nil)
		Expect(err).NotTo(HaveOccurred())
		listResp, err := httpClient.Do(req)
		Expect(err).NotTo(HaveOccurred())
		defer listResp.Body.Close()
		var list []map[string]any
		Expect(json.NewDecoder(listResp.Body).Decode(&list)).To(Succeed())

		byName := map[string]map[string]any{}
		for _, p := range list {
			byName[p["name"].(string)] = p
		}
		Expect(byName).To(HaveKey("legacy-publish"))
		Expect(byName["itest-doomed"]["revoked_at"]).NotTo(BeNil())
		Expect(byName["itest-reviewer"]["last_used_at"]).NotTo(BeNil())
		Expect(byName["itest-reviewer"]).NotTo(HaveKey("token"))
	})

	It("rejects principal minting by non-admins", func() {
		setupTeam(atcURL, atc.Team{
			Name: "some-team",
			Auth: atc.TeamAuth{
				"viewer": map[string][]string{
					"users":  []string{"local:v-user"},
					"groups": []string{},
				},
			},
		})
		nonAdmin := login(atcURL, "v-user", "v-user")

		req, err := http.NewRequest("POST", atcURL+"/api/v1/agent/principals",
			strings.NewReader(`{"name": "sneaky", "scopes": ["reviews:write"]}`))
		Expect(err).NotTo(HaveOccurred())
		req.Header.Set("Content-Type", "application/json")
		resp, err := nonAdmin.HTTPClient().Do(req)
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(http.StatusForbidden))
	})
})
