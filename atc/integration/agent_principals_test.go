package integration_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/concourse/concourse/atc"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Agent Principals API", func() {
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

	// createTicket exercises the surviving principal-gated write: the ticket
	// tier. The v1 publishing scopes (reviews/metrics/costs:write) went with
	// their routes, so tickets:write is the scope the tier is proved through.
	createTicket := func(token, title string) (*http.Response, map[string]any) {
		req, err := http.NewRequest("POST", atcURL+"/api/v1/agent/tickets", strings.NewReader(
			`{"title":"`+title+`","repo":"itest","origin":"retrospective"}`))
		Expect(err).NotTo(HaveOccurred())
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := plainHTTPClient.Do(req)
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		var decoded map[string]any
		json.NewDecoder(resp.Body).Decode(&decoded)
		return resp, decoded
	}

	It("mints, uses, attributes, and revokes scoped principals", func() {
		client := login(atcURL, "test", "test")
		httpClient := client.HTTPClient()

		By("minting a tickets:write principal as admin")
		resp, created := mintPrincipal(httpClient,
			`{"name": "itest-ticket-writer", "description": "integration", "scopes": ["tickets:write"]}`)
		Expect(resp.StatusCode).To(Equal(http.StatusCreated))
		token, _ := created["token"].(string)
		Expect(token).To(HavePrefix("cap1."))
		Expect(created["token_prefix"]).To(Equal(token[:12]))

		By("creating a retrospective ticket with the principal token")
		ticketResp, ticket := createTicket(token, "itest retrospective")
		Expect(ticketResp.StatusCode).To(Equal(http.StatusCreated))

		By("recording the writing principal on the ticket row")
		Expect(ticket["created_by"]).To(Equal("itest-ticket-writer"))
		ticketID := strconv.Itoa(int(ticket["id"].(float64)))
		req, err := http.NewRequest("GET", atcURL+"/api/v1/agent/tickets/"+ticketID, nil)
		Expect(err).NotTo(HaveOccurred())
		getResp, err := httpClient.Do(req)
		Expect(err).NotTo(HaveOccurred())
		defer getResp.Body.Close()
		var detail map[string]any
		Expect(json.NewDecoder(getResp.Body).Decode(&detail)).To(Succeed())
		Expect(detail["ticket"].(map[string]any)["created_by"]).To(Equal("itest-ticket-writer"))

		By("rejecting a read-only principal on a write route")
		resp, wrongScope := mintPrincipal(httpClient,
			`{"name": "itest-ticket-reader", "scopes": ["tickets:read"]}`)
		Expect(resp.StatusCode).To(Equal(http.StatusCreated))
		wrongResp, _ := createTicket(wrongScope["token"].(string), "should not exist")
		Expect(wrongResp.StatusCode).ToNot(Equal(http.StatusCreated))

		By("rejecting a retired publishing scope at mint time")
		resp, _ = mintPrincipal(httpClient, `{"name": "itest-reviewer", "scopes": ["reviews:write"]}`)
		Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))

		By("rejecting a principal revoked before first use")
		resp, doomed := mintPrincipal(httpClient,
			`{"name": "itest-doomed", "scopes": ["tickets:write"]}`)
		Expect(resp.StatusCode).To(Equal(http.StatusCreated))
		doomedID := strconv.Itoa(int(doomed["id"].(float64)))

		req, err = http.NewRequest("DELETE", atcURL+"/api/v1/agent/principals/"+doomedID, nil)
		Expect(err).NotTo(HaveOccurred())
		delResp, err := httpClient.Do(req)
		Expect(err).NotTo(HaveOccurred())
		io.Copy(io.Discard, delResp.Body)
		delResp.Body.Close()
		Expect(delResp.StatusCode).To(Equal(http.StatusNoContent))

		revokedResp, _ := createTicket(doomed["token"].(string), "revoked writer")
		Expect(revokedResp.StatusCode).To(Equal(http.StatusUnauthorized))

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
		Expect(byName["itest-doomed"]["revoked_at"]).NotTo(BeNil())
		Expect(byName["itest-ticket-writer"]["last_used_at"]).NotTo(BeNil())
		Expect(byName["itest-ticket-writer"]).NotTo(HaveKey("token"))
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
			strings.NewReader(`{"name": "sneaky", "scopes": ["tickets:write"]}`))
		Expect(err).NotTo(HaveOccurred())
		req.Header.Set("Content-Type", "application/json")
		resp, err := nonAdmin.HTTPClient().Do(req)
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(http.StatusForbidden))
	})
})
