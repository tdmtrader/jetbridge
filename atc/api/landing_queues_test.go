package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/concourse/concourse/atc"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Landing queues API", func() {
	var realdb *realDB
	var base string
	const config = "repository: https://example.test/repo.git\ntrunk: core\ncompose: landing-compose\nland: landing-land\ngates: []\n"
	shaA := strings.Repeat("a", 40)
	shaB := strings.Repeat("b", 40)

	BeforeEach(func() {
		realdb = useRealDB()
		server = realdb.Serve()
		_, err := realdb.Deps.teamFactory.CreateTeam(atc.Team{Name: "a-team", Auth: atc.TeamAuth{"owner": map[string][]string{"groups": {}, "users": {"local:username"}}}})
		Expect(err).NotTo(HaveOccurred())
		fakeAccess.IsAuthenticatedReturns(true)
		fakeAccess.IsAuthorizedReturns(true)
		base = server.URL + "/api/v1/teams/a-team/landing-queues/trunk"
	})

	do := func(method, url, body string) *http.Response {
		GinkgoHelper()
		request, err := http.NewRequest(method, url, bytes.NewBufferString(body))
		Expect(err).NotTo(HaveOccurred())
		response, err := client.Do(request)
		Expect(err).NotTo(HaveOccurred())
		return response
	}
	text := func(response *http.Response) string {
		GinkgoHelper()
		body, err := io.ReadAll(response.Body)
		Expect(err).NotTo(HaveOccurred())
		return string(body)
	}

	It("A landing queue is created with 201, replaced with 200, and refused naming an unknown key", func() {
		Expect(do(http.MethodPut, base, config).StatusCode).To(Equal(http.StatusCreated))
		Expect(do(http.MethodPut, base, config).StatusCode).To(Equal(http.StatusOK))
		response := do(http.MethodPut, base, config+"branch: core\n")
		Expect(response.StatusCode).To(Equal(http.StatusBadRequest))
		Expect(text(response)).To(ContainSubstring("branch"))
		response = do(http.MethodPut, base, "repository: r\ncompose: c\nland: l\n")
		Expect(response.StatusCode).To(Equal(http.StatusBadRequest))
		Expect(text(response)).To(ContainSubstring("trunk"))
	})

	It("A submit queues an entry with 201, repeats with 200, and conflicts with 409 naming the id", func() {
		Expect(do(http.MethodPut, base, config).StatusCode).To(Equal(http.StatusCreated))
		submit := func(id, sha string) *http.Response {
			body, _ := json.Marshal(atc.LandingSubmission{ID: id, Commit: sha})
			return do(http.MethodPost, base+"/entries", string(body))
		}
		Expect(submit("fix-1", shaA).StatusCode).To(Equal(http.StatusCreated))
		Expect(submit("fix-1", shaA).StatusCode).To(Equal(http.StatusOK))
		response := submit("fix-1", shaB)
		Expect(response.StatusCode).To(Equal(http.StatusConflict))
		Expect(text(response)).To(ContainSubstring("fix-1"))
		Expect(submit("../x", shaA).StatusCode).To(Equal(http.StatusBadRequest))
		Expect(submit("fix-2", "abc").StatusCode).To(Equal(http.StatusBadRequest))

		response = do(http.MethodGet, base, "")
		Expect(response.StatusCode).To(Equal(http.StatusOK))
		var status atc.LandingQueueStatus
		Expect(json.NewDecoder(response.Body).Decode(&status)).To(Succeed())
		Expect(status.Name).To(Equal("trunk"))
		Expect(status.Config.Trunk).To(Equal("core"))
		Expect(status.Entries).To(HaveLen(1))
		Expect(status.Entries[0].ID).To(Equal("fix-1"))
		Expect(status.Entries[0].State).To(Equal(atc.LandingEntryQueued))
	})

	It("A queue that is not set answers 404 to a submit and a status read", func() {
		Expect(do(http.MethodGet, base, "").StatusCode).To(Equal(http.StatusNotFound))
		Expect(do(http.MethodPost, base+"/entries", `{"id":"x","commit":"`+shaA+`"}`).StatusCode).To(Equal(http.StatusNotFound))
	})

	It("A member of another team is refused", func() {
		fakeAccess.IsAuthorizedReturns(false)
		Expect(do(http.MethodPut, base, config).StatusCode).To(Equal(http.StatusForbidden))
		Expect(do(http.MethodGet, base, "").StatusCode).To(Equal(http.StatusForbidden))
	})
})
