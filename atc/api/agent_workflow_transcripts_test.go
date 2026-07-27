package api_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc/api/transcriptserver"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("agent workflow transcript routes", func() {
	BeforeEach(func() {
		fakeAccess.IsAuthenticatedReturns(true)
		fakeAccess.IsAuthorizedReturns(true)
	})

	get := func(path string) *http.Response {
		GinkgoHelper()
		request, err := http.NewRequest(http.MethodGet, server.URL+path, nil)
		Expect(err).NotTo(HaveOccurred())
		response, err := client.Do(request)
		Expect(err).NotTo(HaveOccurred())
		return response
	}

	Describe("listing the transcripts of one durable run", func() {
		It("indexes which plans of the run are inspectable", func() {
			runID := snapshot.WorkflowRunID(9007199254740993)
			fakeAgentRunTranscriptFactory.ListByWorkflowRunReturns([]db.AgentRunTranscript{
				{
					BuildID:       42,
					PlanID:        "plan-1",
					WorkflowRunID: &runID,
					FunctionID:    "implement",
					StepName:      "implement",
					NDJSON:        `{"type":"system"}` + "\n",
					ByteLen:       19,
				},
				{
					BuildID:    43,
					PlanID:     "plan-2",
					FunctionID: "review",
					StepName:   "review",
					NDJSON:     `{"type":"result"}` + "\n",
					ByteLen:    18,
					Truncated:  true,
				},
			}, nil)

			response := get("/api/v1/agent/workflows/review/runs/9007199254740993/transcripts")
			defer response.Body.Close()
			Expect(response.StatusCode).To(Equal(http.StatusOK))

			var listed []transcriptserver.Ref
			Expect(json.NewDecoder(response.Body).Decode(&listed)).To(Succeed())
			Expect(listed).To(Equal([]transcriptserver.Ref{
				{
					PlanID:        "plan-1",
					FunctionID:    "implement",
					StepName:      "implement",
					BuildID:       42,
					WorkflowRunID: "9007199254740993",
					ByteLen:       19,
				},
				{
					PlanID:     "plan-2",
					FunctionID: "review",
					StepName:   "review",
					BuildID:    43,
					ByteLen:    18,
					Truncated:  true,
				},
			}))

			By("scoping the store query by workflow name AND run id")
			name, listedRunID := fakeAgentRunTranscriptFactory.ListByWorkflowRunArgsForCall(
				fakeAgentRunTranscriptFactory.ListByWorkflowRunCallCount() - 1,
			)
			Expect(name).To(Equal("review"))
			Expect(listedRunID).To(Equal(runID))
		})

		It("returns an empty array, never null, for a run with no transcripts", func() {
			fakeAgentRunTranscriptFactory.ListByWorkflowRunReturns(nil, nil)

			response := get("/api/v1/agent/workflows/review/runs/9007199254740993/transcripts")
			defer response.Body.Close()
			Expect(response.StatusCode).To(Equal(http.StatusOK))
			body, err := io.ReadAll(response.Body)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(body)).To(Equal("[]\n"))
		})

		It("rejects a malformed run id", func() {
			response := get("/api/v1/agent/workflows/review/runs/not-a-number/transcripts")
			defer response.Body.Close()
			Expect(response.StatusCode).To(Equal(http.StatusBadRequest))
		})

		It("500s when the store fails", func() {
			fakeAgentRunTranscriptFactory.ListByWorkflowRunReturns(nil, errors.New("nope"))

			response := get("/api/v1/agent/workflows/review/runs/9007199254740993/transcripts")
			defer response.Body.Close()
			Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
		})
	})

	Describe("reading one transcript body", func() {
		It("serves the ndjson of the addressed plan", func() {
			fakeAgentRunTranscriptFactory.ListByWorkflowRunReturns([]db.AgentRunTranscript{
				{BuildID: 42, PlanID: "plan-1", NDJSON: `{"type":"system"}` + "\n"},
			}, nil)

			response := get("/api/v1/agent/workflows/review/runs/9007199254740993/transcripts/plan-1")
			defer response.Body.Close()
			Expect(response.StatusCode).To(Equal(http.StatusOK))
			Expect(response.Header.Get("Content-Type")).To(Equal("application/x-ndjson"))
			body, err := io.ReadAll(response.Body)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(body)).To(Equal(`{"type":"system"}` + "\n"))
		})

		It("404s for a plan the run never captured", func() {
			fakeAgentRunTranscriptFactory.ListByWorkflowRunReturns([]db.AgentRunTranscript{
				{BuildID: 42, PlanID: "plan-1", NDJSON: "{}\n"},
			}, nil)

			response := get("/api/v1/agent/workflows/review/runs/9007199254740993/transcripts/other-plan")
			defer response.Body.Close()
			Expect(response.StatusCode).To(Equal(http.StatusNotFound))
		})
	})
})
