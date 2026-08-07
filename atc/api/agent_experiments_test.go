package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/concourse/concourse/agent/experiment"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc/db"

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

	It("returns persisted experiment identity and state", func() {
		created := createAgentExperimentFixture(realdb)

		request, err := http.NewRequest(http.MethodGet, server.URL+"/api/v1/agent/experiments", nil)
		Expect(err).NotTo(HaveOccurred())
		response, err := client.Do(request)
		Expect(err).NotTo(HaveOccurred())
		defer response.Body.Close()
		Expect(response.StatusCode).To(Equal(http.StatusOK))

		var listed []experiment.StoredExperiment
		Expect(json.NewDecoder(response.Body).Decode(&listed)).To(Succeed())
		Expect(listed).To(HaveLen(1))
		Expect(listed[0].ID).To(Equal(created.ID))
		Expect(listed[0].ID).NotTo(BeZero())
		Expect(listed[0].Definition.Name).To(Equal("api-list-experiment"))
		Expect(listed[0].Definition.State).To(Equal(experiment.StateDraft))
	})
})

func createAgentExperimentFixture(realdb *realDB) experiment.StoredExperiment {
	GinkgoHelper()

	workflows := db.NewAgentWorkflowsFactory(realdb.Conn)
	candidate, err := workflows.Import("api-experiment-candidate", []byte(`
schema_version: 3
name: api-experiment-candidate
signature_version: 1
inputs:
  - name: repo
    type: repository/v1
outputs:
  - name: review
    type: review/v1
    from: review
plan:
  - agent: review
    function_id: review
    prompt: Review the repository.
    budget_slice_usd: 0.25
    inputs: [repo]
    outputs: [review]
    input_types:
      repo: {type: repository/v1}
    output_types:
      review: review/v1
`), "fixture-author")
	Expect(err).NotTo(HaveOccurred())
	evaluator, err := workflows.Import("api-experiment-evaluator", []byte(`
schema_version: 3
name: api-experiment-evaluator
signature_version: 1
inputs:
  - name: candidate
    type: review/v1
  - name: repo
    type: repository/v1
outputs:
  - name: measurements
    type: measurements/v1
    from: measurements
plan:
  - agent: evaluate
    function_id: evaluate
    prompt: Evaluate the candidate output.
    budget_slice_usd: 0.25
    inputs: [candidate, repo]
    outputs: [measurements]
    input_types:
      candidate: {type: review/v1}
      repo: {type: repository/v1}
    output_types:
      measurements: measurements/v1
`), "fixture-author")
	Expect(err).NotTo(HaveOccurred())

	var fixtureSnapshot snapshot.SnapshotID
	Expect(realdb.Conn.QueryRow(`
		INSERT INTO agent_snapshots
			(team_id, type_name, type_version, digest, byte_size, file_count, representation)
		VALUES ($1, 'repository', 1, $2, 1, 1, 'filesystem-tree-v1')
		RETURNING id
	`, realdb.Main.ID(), "sha256:"+strings.Repeat("a", 64)).Scan(&fixtureSnapshot)).To(Succeed())

	candidateSignature := workflow.PublicSignature{
		Inputs:  []workflow.SignaturePort{{Name: "repo", Type: "repository/v1"}},
		Outputs: []workflow.SignaturePort{{Name: "review", Type: "review/v1"}},
	}
	signatureHash, err := experiment.HashSignature(candidateSignature)
	Expect(err).NotTo(HaveOccurred())
	definition := experiment.Definition{
		Name: "api-list-experiment", State: experiment.StateDraft,
		Signature: candidateSignature,
		Variants: []experiment.Variant{
			{
				Label: "control", Control: true, SignatureHash: signatureHash,
				Target: experiment.Target{
					Kind: experiment.TargetWorkflow, WorkflowName: candidate.Name,
					DefinitionID: int64(candidate.ID), Version: candidate.Version,
				},
			},
			{
				Label: "candidate", SignatureHash: signatureHash,
				Target: experiment.Target{
					Kind: experiment.TargetFunction, WorkflowName: candidate.Name,
					DefinitionID: int64(candidate.ID), Version: candidate.Version, FunctionID: "review",
				},
			},
		},
		Fixtures: []experiment.Fixture{
			{Label: "repository", Role: experiment.FixtureNormal, Inputs: map[string]snapshot.SnapshotID{"repo": fixtureSnapshot}},
		},
		Evaluator: experiment.Evaluator{
			Target: experiment.Target{
				Kind: experiment.TargetWorkflow, WorkflowName: evaluator.Name,
				DefinitionID: int64(evaluator.ID), Version: evaluator.Version,
			},
			Signature: workflow.PublicSignature{
				Inputs: []workflow.SignaturePort{
					{Name: "candidate", Type: "review/v1"},
					{Name: "repo", Type: "repository/v1"},
				},
				Outputs: []workflow.SignaturePort{{Name: "measurements", Type: "measurements/v1"}},
			},
			Mappings: []experiment.EvaluatorMapping{
				{EvaluatorPort: "candidate", SourceDirection: experiment.SourceCandidateOutput, SourcePort: "review"},
				{EvaluatorPort: "repo", SourceDirection: experiment.SourceFixtureInput, SourcePort: "repo"},
			},
			MeasurementsPort: "measurements",
		},
		Repetitions: 1,
	}

	created, err := realdb.Deps.experiments.Create(
		context.Background(), realdb.Main.ID(), realdb.Main.Name(), "fixture-author", definition,
	)
	Expect(err).NotTo(HaveOccurred())
	return created
}
