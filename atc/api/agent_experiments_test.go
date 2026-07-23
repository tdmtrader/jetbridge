package api_test

import (
	"encoding/json"
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("agent experiment routes", func() {
	BeforeEach(func() {
		fakeAccess.IsAuthenticatedReturns(true)
		fakeAccess.IsAuthorizedReturns(true)
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
