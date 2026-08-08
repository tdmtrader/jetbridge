package api_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc/api/transcriptserver"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const agentWorkflowTranscriptRunID = snapshot.WorkflowRunID(9007199254740993)

type agentWorkflowTranscriptStoreCall struct {
	workflowName string
	runID        snapshot.WorkflowRunID
}

type agentWorkflowTranscriptStore struct {
	transcriptserver.Store

	mu      sync.Mutex
	listErr error
	calls   []agentWorkflowTranscriptStoreCall
}

func (store *agentWorkflowTranscriptStore) ListByWorkflowRun(
	workflowName string,
	runID snapshot.WorkflowRunID,
) ([]db.AgentRunTranscript, error) {
	store.mu.Lock()
	store.calls = append(store.calls, agentWorkflowTranscriptStoreCall{
		workflowName: workflowName,
		runID:        runID,
	})
	err := store.listErr
	delegate := store.Store
	store.mu.Unlock()

	if err != nil {
		return nil, err
	}
	return delegate.ListByWorkflowRun(workflowName, runID)
}

func (store *agentWorkflowTranscriptStore) setListError(err error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.listErr = err
}

func (store *agentWorkflowTranscriptStore) listCalls() []agentWorkflowTranscriptStoreCall {
	store.mu.Lock()
	defer store.mu.Unlock()
	return append([]agentWorkflowTranscriptStoreCall(nil), store.calls...)
}

type agentWorkflowTranscriptFixture struct {
	runID       snapshot.WorkflowRunID
	decoyRunID  snapshot.WorkflowRunID
	firstBuild  db.Build
	secondBuild db.Build
	transcripts db.AgentRunTranscriptFactory
}

var _ = Describe("agent workflow transcript routes", func() {
	var (
		realdb  *realDB
		deps    apiDBDeps
		fixture agentWorkflowTranscriptFixture
		store   *agentWorkflowTranscriptStore
		server  *httptest.Server
	)

	BeforeEach(func() {
		fakeAccess.IsAuthenticatedReturns(true)
		fakeAccess.IsAuthorizedReturns(true)

		realdb = useRealDB()
		deps = realdb.Deps
		fixture = seedAgentWorkflowTranscriptFixture(realdb)
		store = &agentWorkflowTranscriptStore{Store: fixture.transcripts}
		deps.transcripts = store
	})

	JustBeforeEach(func() {
		realdb.Deps = deps
		server = realdb.Serve()
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
			unprojected := get("/api/v1/agent/workflows/review/runs/9007199254740993/transcripts")
			body, err := io.ReadAll(unprojected.Body)
			Expect(err).NotTo(HaveOccurred())
			Expect(unprojected.Body.Close()).To(Succeed())
			Expect(unprojected.StatusCode).To(Equal(http.StatusOK))
			Expect(string(body)).To(Equal("[]\n"))

			firstNDJSON := `{"type":"system"}` + "\n"
			secondNDJSON := `{"type":"result"}` + "\n"
			Expect(fixture.transcripts.Upsert(db.AgentRunTranscript{
				BuildID: fixture.firstBuild.ID(), PlanID: "plan-1", WorkflowRunID: &fixture.runID,
				FunctionID: "implement", StepName: "implement",
				NDJSON: firstNDJSON, ByteLen: len(firstNDJSON),
			})).To(Succeed())
			Expect(fixture.transcripts.Upsert(db.AgentRunTranscript{
				BuildID: fixture.secondBuild.ID(), PlanID: "plan-2", WorkflowRunID: &fixture.runID,
				FunctionID: "review", StepName: "review",
				NDJSON: secondNDJSON, ByteLen: len(secondNDJSON), Truncated: true,
			})).To(Succeed())

			persisted, err := fixture.transcripts.ListByWorkflowRun("review", fixture.runID)
			Expect(err).NotTo(HaveOccurred())
			Expect(persisted).To(HaveLen(2))
			for _, row := range persisted {
				Expect(row.WorkflowRunID).NotTo(BeNil())
				Expect(*row.WorkflowRunID).To(Equal(fixture.runID))
			}

			response := get("/api/v1/agent/workflows/review/runs/9007199254740993/transcripts")
			defer response.Body.Close()
			Expect(response.StatusCode).To(Equal(http.StatusOK))
			Expect(response.Header.Get("Content-Type")).To(Equal("application/json"))
			body, err = io.ReadAll(response.Body)
			Expect(err).NotTo(HaveOccurred())
			expected, err := json.Marshal([]transcriptserver.Ref{
				{
					PlanID: "plan-1", FunctionID: "implement", StepName: "implement",
					BuildID: fixture.firstBuild.ID(), WorkflowRunID: "9007199254740993",
					ByteLen: len(firstNDJSON),
				},
				{
					PlanID: "plan-2", FunctionID: "review", StepName: "review",
					BuildID: fixture.secondBuild.ID(), WorkflowRunID: "9007199254740993",
					ByteLen: len(secondNDJSON), Truncated: true,
				},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(body).To(MatchJSON(expected))

			By("requiring the workflow name as part of the durable run scope")
			wrongWorkflow := get("/api/v1/agent/workflows/other-workflow/runs/9007199254740993/transcripts")
			defer wrongWorkflow.Body.Close()
			Expect(wrongWorkflow.StatusCode).To(Equal(http.StatusOK))
			wrongBody, err := io.ReadAll(wrongWorkflow.Body)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(wrongBody)).To(Equal("[]\n"))
		})

		It("returns an empty array, never null, for a run with no transcripts", func() {
			decoy, err := fixture.transcripts.ListByWorkflowRun("review", fixture.decoyRunID)
			Expect(err).NotTo(HaveOccurred())
			Expect(decoy).To(HaveLen(1))
			Expect(decoy[0].PlanID).To(Equal("other-plan"))

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
			Expect(store.listCalls()).To(BeEmpty())
		})

		It("500s when the store fails", func() {
			store.setListError(errors.New("nope"))

			response := get("/api/v1/agent/workflows/review/runs/9007199254740993/transcripts")
			defer response.Body.Close()
			Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
			Expect(store.listCalls()).To(Equal([]agentWorkflowTranscriptStoreCall{{
				workflowName: "review",
				runID:        fixture.runID,
			}}))
		})
	})

	Describe("reading one transcript body", func() {
		It("serves the ndjson of the addressed plan", func() {
			missing := get("/api/v1/agent/workflows/review/runs/9007199254740993/transcripts/plan-1")
			_, _ = io.Copy(io.Discard, missing.Body)
			Expect(missing.Body.Close()).To(Succeed())
			Expect(missing.StatusCode).To(Equal(http.StatusNotFound))

			ndjson := `{"type":"system"}` + "\n"
			Expect(fixture.transcripts.Upsert(db.AgentRunTranscript{
				BuildID: fixture.firstBuild.ID(), PlanID: "plan-1", WorkflowRunID: &fixture.runID,
				FunctionID: "implement", StepName: "implement",
				NDJSON: ndjson, ByteLen: len(ndjson),
			})).To(Succeed())

			response := get("/api/v1/agent/workflows/review/runs/9007199254740993/transcripts/plan-1")
			defer response.Body.Close()
			Expect(response.StatusCode).To(Equal(http.StatusOK))
			Expect(response.Header.Get("Content-Type")).To(Equal("application/x-ndjson"))
			body, err := io.ReadAll(response.Body)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(body)).To(Equal(ndjson))
		})

		It("404s for a plan the run never captured", func() {
			targetNDJSON := `{"type":"system"}` + "\n"
			Expect(fixture.transcripts.Upsert(db.AgentRunTranscript{
				BuildID: fixture.firstBuild.ID(), PlanID: "plan-1", WorkflowRunID: &fixture.runID,
				FunctionID: "implement", StepName: "implement",
				NDJSON: targetNDJSON, ByteLen: len(targetNDJSON),
			})).To(Succeed())
			target, err := fixture.transcripts.ListByWorkflowRun("review", fixture.runID)
			Expect(err).NotTo(HaveOccurred())
			Expect(target).To(HaveLen(1))
			Expect(target[0].PlanID).To(Equal("plan-1"))
			Expect(target[0].WorkflowRunID).NotTo(BeNil())
			Expect(*target[0].WorkflowRunID).To(Equal(fixture.runID))

			decoy, err := fixture.transcripts.ListByWorkflowRun("review", fixture.decoyRunID)
			Expect(err).NotTo(HaveOccurred())
			Expect(decoy).To(HaveLen(1))
			Expect(decoy[0].PlanID).To(Equal("other-plan"))

			response := get("/api/v1/agent/workflows/review/runs/9007199254740993/transcripts/other-plan")
			defer response.Body.Close()
			Expect(response.StatusCode).To(Equal(http.StatusNotFound))
		})
	})
})

func seedAgentWorkflowTranscriptFixture(realdb *realDB) agentWorkflowTranscriptFixture {
	GinkgoHelper()

	firstBuild, err := realdb.Main.CreateOneOffBuild()
	Expect(err).NotTo(HaveOccurred())
	secondBuild, err := realdb.Main.CreateOneOffBuild()
	Expect(err).NotTo(HaveOccurred())
	decoyBuild, err := realdb.Main.CreateOneOffBuild()
	Expect(err).NotTo(HaveOccurred())

	definitionContentHash := strings.Repeat("a", 64)
	var definitionID int
	Expect(realdb.Conn.QueryRow(`
		INSERT INTO agent_workflow_definitions
			(definition_kind, name, version, content_hash, definition, created_by,
			 schema_version, signature_version)
		VALUES ('workflow', 'review', 1, $1, $2, 'transcript-fixture', 3, 1)
		RETURNING id
	`, definitionContentHash, `schema_version: 3
name: review
signature_version: 1
inputs: []
outputs: []
plan: []
`).Scan(&definitionID)).To(Succeed())

	insertAgentWorkflowTranscriptRun(
		realdb,
		agentWorkflowTranscriptRunID,
		definitionID,
		definitionContentHash,
		"transcript-target-run",
		strings.Repeat("b", 64),
		firstBuild.ID(),
	)
	decoyRunID := agentWorkflowTranscriptRunID + 1
	insertAgentWorkflowTranscriptRun(
		realdb,
		decoyRunID,
		definitionID,
		definitionContentHash,
		"transcript-decoy-run",
		strings.Repeat("c", 64),
		decoyBuild.ID(),
	)

	transcripts := db.NewAgentRunTranscriptFactory(realdb.Conn)
	decoyNDJSON := `{"type":"decoy"}` + "\n"
	Expect(transcripts.Upsert(db.AgentRunTranscript{
		BuildID: decoyBuild.ID(), PlanID: "other-plan", WorkflowRunID: &decoyRunID,
		FunctionID: "decoy", StepName: "decoy",
		NDJSON: decoyNDJSON, ByteLen: len(decoyNDJSON),
	})).To(Succeed())

	return agentWorkflowTranscriptFixture{
		runID:       agentWorkflowTranscriptRunID,
		decoyRunID:  decoyRunID,
		firstBuild:  firstBuild,
		secondBuild: secondBuild,
		transcripts: transcripts,
	}
}

func insertAgentWorkflowTranscriptRun(
	realdb *realDB,
	runID snapshot.WorkflowRunID,
	definitionID int,
	definitionContentHash string,
	idempotencyKey string,
	parameterizedConfigHash string,
	plannedBuildID int,
) {
	GinkgoHelper()

	_, err := realdb.Conn.Exec(`
		INSERT INTO agent_workflow_runs
			(id, team_id, team_name, workflow_definition_id, definition_kind,
			 workflow_name, workflow_version, schema_version, signature_version,
			 definition_content_hash, idempotency_key, parameterized_config,
			 parameterized_config_hash, origin_kind, origin_reference, created_by,
			 status, planned_build_id)
		VALUES ($1, $2, $3, $4, 'workflow', 'review', 1, 3, 1,
		        $5, $6, '{}', $7, 'manual', '', 'transcript-fixture',
		        'running', $8)
	`, int64(runID), realdb.Main.ID(), realdb.Main.Name(), definitionID,
		definitionContentHash, idempotencyKey, parameterizedConfigHash, plannedBuildID)
	Expect(err).NotTo(HaveOccurred())
}
