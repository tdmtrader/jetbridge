package api_test

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/concourse/concourse/agent/api/workflowoutcomes"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("agent workflow outcome routes", func() {
	BeforeEach(func() {
		fakeAccess.IsAuthenticatedReturns(true)
		fakeAccess.IsAuthorizedReturns(true)
	})

	It("serves the main-team run outcome list through the production router", func() {
		request, err := http.NewRequest(
			http.MethodGet,
			server.URL+"/api/v1/agent/workflows/review/runs/9007199254740993/outcomes",
			nil,
		)
		Expect(err).NotTo(HaveOccurred())
		response, err := client.Do(request)
		Expect(err).NotTo(HaveOccurred())
		defer response.Body.Close()
		Expect(response.StatusCode).To(Equal(http.StatusOK))
		var listed []workflowoutcomes.Outcome
		Expect(json.NewDecoder(response.Body).Decode(&listed)).To(Succeed())
		Expect(listed).To(BeEmpty())
	})

	It("records one exact output through the production router", func() {
		body := strings.NewReader(`{"disposition":"accepted","human_modified":false}`)
		request, err := http.NewRequest(
			http.MethodPut,
			server.URL+"/api/v1/agent/workflows/review/runs/9007199254740994/outputs/9007199254740995/outcome",
			body,
		)
		Expect(err).NotTo(HaveOccurred())
		request.Header.Set("Content-Type", "application/json")
		response, err := client.Do(request)
		Expect(err).NotTo(HaveOccurred())
		defer response.Body.Close()
		Expect(response.StatusCode).To(Equal(http.StatusCreated))
		var recorded workflowoutcomes.Outcome
		Expect(json.NewDecoder(response.Body).Decode(&recorded)).To(Succeed())
		Expect(recorded.WorkflowRunID.String()).To(Equal("9007199254740994"))
		Expect(recorded.OutputSnapshotID.String()).To(Equal("9007199254740995"))
		Expect(recorded.Actor).To(Equal("api-suite"))
	})
})
