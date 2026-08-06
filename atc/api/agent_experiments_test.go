package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("agent experiment routes", func() {
	// server shadows the package-level one for this Describe: the routes are
	// served by handlers built over a real database rather than over
	// FakeAgentExperimentsFactory. The list below was previously empty because
	// an unstubbed fake returns a zero value; it is empty now because the table
	// is.
	var (
		realdb *realDB
		server *httptest.Server
	)

	BeforeEach(func() {
		fakeAccess.IsAuthenticatedReturns(true)
		fakeAccess.IsAuthorizedReturns(true)

		realdb = useRealDB()
		server = realdb.Serve()
	})

	It("serves the main-team experiment list through the production router", func() {
		request, err := http.NewRequest(http.MethodGet, server.URL+"/api/v1/agent/experiments", nil)
		Expect(err).NotTo(HaveOccurred())
		response, err := client.Do(request)
		Expect(err).NotTo(HaveOccurred())
		defer response.Body.Close()
		Expect(response.StatusCode).To(Equal(http.StatusOK))
		var listed []any
		Expect(json.NewDecoder(response.Body).Decode(&listed)).To(Succeed())
		Expect(listed).To(BeEmpty())
	})
})
