package db_test

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/dispatch"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/agent/workflowrun"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/builds"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("agent workflow run vertical slice", func() {
	It("imports, binds, executes, seals, reconciles, and preserves exact history after execution deletion", func() {
		fixture := newAgentDispatchFixture()
		ticketID := fixture.queueTicket()
		dispatched, err := dispatch.DispatchOne(context.Background(), fixture.deps, ticketID, "admin")
		Expect(err).NotTo(HaveOccurred())
		Expect(dispatched.WorkflowRunID).NotTo(BeNil())

		result, found, err := fixture.workflowRuns.Get(
			context.Background(), defaultTeam.ID(), *dispatched.WorkflowRunID,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(result.Status).To(Equal(db.AgentWorkflowRunStatusRunning))
		Expect(result.OriginKind).To(Equal("ticket"))
		Expect(result.OriginReference).To(Equal(fmt.Sprintf("%d", ticketID)))
		Expect(result.WorkflowDefinitionID).To(Equal(fixture.definition.ID))
		Expect(result.WorkflowVersion).To(Equal(fixture.definition.Version))
		Expect(result.DefinitionContentHash).To(Equal(fixture.definition.ContentHash))
		Expect(result.PlannedBuildID).NotTo(BeNil())
		Expect(result.TemplatePipelineID).NotTo(BeNil())
		Expect(result.InstancePipelineID).NotTo(BeNil())
		Expect(result.PipelineRunID).NotTo(BeNil())
		ticket, found, err := fixture.tickets.Get(ticketID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(ticket.WorkflowRunID).To(Equal(dispatched.WorkflowRunID))
		Expect(ticket.WorkflowDefinitionID).To(Equal(&fixture.definition.ID))
		Expect(ticket.WorkflowVersion).To(Equal(&fixture.definition.Version))

		var concrete atc.Config
		Expect(atc.UnmarshalConfig(result.ConcreteConfig, &concrete)).To(Succeed())
		Expect(concrete.Jobs).To(HaveLen(1))
		plan, err := builds.NewPlanner(atc.NewPlanFactory(0)).Create(
			concrete.Jobs[0].StepConfig(), nil, concrete.ResourceTypes, concrete.Prototypes, nil, false,
		)
		Expect(err).NotTo(HaveOccurred())
		producer := findAgentPlan(plan, "implement")
		Expect(producer).NotTo(BeNil())
		Expect(producer.Agent.SnapshotOutputs["report"].WorkflowRunID).To(Equal(result.ID.String()))

		build, found, err := buildFactory.Build(int(*result.PlannedBuildID))
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		started, err := build.Start(plan)
		Expect(err).NotTo(HaveOccurred())
		Expect(started).To(BeTrue())

		registry, err := contracts.NewRegistry()
		Expect(err).NotTo(HaveOccurred())
		content := &workflowRunMemoryContent{objects: map[snapshot.Digest][]byte{}}
		sealer, err := snapshot.NewBatchSealer(
			snapshot.Canonicalizer{TempDir: GinkgoT().TempDir()},
			registry,
			db.NewAgentSnapshotsFactory(dbConn),
			content,
			db.NewAgentSnapshotDigestLocker(dbConn),
		)
		Expect(err).NotTo(HaveOccurred())
		reportArchive := workflowRunTar(map[string][]byte{"report.txt": []byte("complete")})
		definitionID := fixture.definition.ID
		workflowRunID := result.ID
		sealed, err := sealer.Seal(context.Background(), snapshot.SealRequest{
			BuildID: build.ID(), TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(), CreatedBy: "alice",
			PlanID: producer.ID.String(), Attempt: "1", StepKind: "agent", StepName: "implement",
			WorkflowDefinitionID: &definitionID, WorkflowRunID: &workflowRunID,
			InputOrder: []string{"work-item", "repository"},
			Inputs: map[string]snapshot.SnapshotRef{
				"work-item": {
					ID: fixture.workItemSnapshot.ID, Type: fixture.workItemSnapshot.Type,
					Digest: fixture.workItemSnapshot.Digest,
				},
				"repository": {
					ID: fixture.repositorySnapshot.ID, Type: fixture.repositorySnapshot.Type,
					Digest: fixture.repositorySnapshot.Digest,
				},
			},
			OutputDeclarations: []snapshot.Port{{Name: "report", Type: "opaque/v1"}},
			Outputs: []snapshot.OutputSource{{
				ClientKey: "report", Port: snapshot.Port{Name: "report", Type: "opaque/v1"},
				OpenTar: func(context.Context) (io.ReadCloser, error) {
					return io.NopCloser(bytes.NewReader(reportArchive)), nil
				},
				Retention: snapshot.RetentionClassWorkflow, WorkflowPort: "report",
			}},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(sealed).To(HaveKey("report"))
		output := sealed["report"].Snapshot
		Expect(output.Type).To(Equal(snapshot.TypeRef("opaque/v1")))

		Expect(build.Finish(db.BuildStatusSucceeded)).To(Succeed())
		now := time.Now().Add(time.Hour)
		reconciler, err := workflowrun.NewReconciler(
			fixture.workflowRuns, logger, 10*time.Minute, time.Minute,
			workflowrun.WithReconcilerClock(func() time.Time { return now }),
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(reconciler.Run(context.Background())).To(Succeed())

		completed, found, err := fixture.workflowRuns.Get(context.Background(), defaultTeam.ID(), result.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(completed.Status).To(Equal(db.AgentWorkflowRunStatusSucceeded))
		Expect(completed.ExecutionStatus).NotTo(BeNil())
		Expect(*completed.ExecutionStatus).To(Equal(db.AgentWorkflowRunExecutionStatusSucceeded))
		Expect(completed.ActualPlan).NotTo(BeEmpty())
		Expect(completed.ActualPlanHash).NotTo(BeNil())
		Expect(completed.ResolvedDependencies).To(ContainSubstring(dispatchRuntimeImage))

		bindings, err := fixture.workflowRuns.Snapshots(context.Background(), completed.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(bindings).To(ConsistOf(
			db.AgentWorkflowRunSnapshotBinding{
				WorkflowRunID: completed.ID, Direction: db.AgentWorkflowRunSnapshotInput,
				PortName: "work-item", Snapshot: snapshot.SnapshotRef{
					ID: fixture.workItemSnapshot.ID, Type: "work-item/v1",
					Digest: fixture.workItemSnapshot.Digest,
				},
			},
			db.AgentWorkflowRunSnapshotBinding{
				WorkflowRunID: completed.ID, Direction: db.AgentWorkflowRunSnapshotInput,
				PortName: "repository", Snapshot: snapshot.SnapshotRef{
					ID: fixture.repositorySnapshot.ID, Type: "repository/v1",
					Digest: fixture.repositorySnapshot.Digest,
				},
			},
			db.AgentWorkflowRunSnapshotBinding{
				WorkflowRunID: completed.ID, Direction: db.AgentWorkflowRunSnapshotOutput,
				PortName: "report", Snapshot: output,
			},
		))
		var durableOutputClaims int
		Expect(dbConn.QueryRow(`
			SELECT count(*) FROM agent_snapshot_retention_claims
			WHERE snapshot_id = $1 AND team_id = $2 AND class = 'workflow'
			  AND expires_at IS NULL AND actor = $3
		`, int64(output.ID), defaultTeam.ID(), fmt.Sprintf("workflow-run:%d:output:report", int64(completed.ID))).Scan(&durableOutputClaims)).To(Succeed())
		Expect(durableOutputClaims).To(Equal(1))

		pipelineRunID := *completed.PipelineRunID
		templatePipelineID := *completed.TemplatePipelineID
		instancePipelineID := *completed.InstancePipelineID
		plannedBuildID := *completed.PlannedBuildID
		Expect(dbConn.QueryRow(`DELETE FROM builds WHERE id = $1 RETURNING id`, plannedBuildID).Scan(new(int64))).To(Succeed())
		_, err = dbConn.Exec(`DELETE FROM pipeline_runs WHERE id = $1`, pipelineRunID)
		Expect(err).NotTo(HaveOccurred())
		_, err = dbConn.Exec(`DELETE FROM pipelines WHERE id IN ($1, $2)`, instancePipelineID, templatePipelineID)
		Expect(err).NotTo(HaveOccurred())

		history, found, err := fixture.workflowRuns.Get(context.Background(), defaultTeam.ID(), completed.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(history.Status).To(Equal(db.AgentWorkflowRunStatusSucceeded))
		Expect(history.PipelineRunID).To(Equal(&pipelineRunID))
		Expect(history.TemplatePipelineID).To(Equal(&templatePipelineID))
		Expect(history.InstancePipelineID).To(Equal(&instancePipelineID))
		Expect(history.PlannedBuildID).To(Equal(&plannedBuildID))
		Expect(history.ActualPlanHash).To(Equal(completed.ActualPlanHash))
		Expect(history.ActualPlan).To(MatchJSON(completed.ActualPlan))
		historyBindings, err := fixture.workflowRuns.Snapshots(context.Background(), completed.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(historyBindings).To(ConsistOf(bindings))

		durableTicket, found, err := fixture.tickets.Get(ticketID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(durableTicket.WorkflowRunID).To(Equal(&completed.ID))
		Expect(durableTicket.WorkflowDefinitionID).To(Equal(&completed.WorkflowDefinitionID))
		Expect(durableTicket.WorkflowVersion).To(Equal(&completed.WorkflowVersion))
		_, legacyFound, err := defaultTeam.Pipeline(atc.PipelineRef{
			Name: fmt.Sprintf("agent-ticket-%d", ticketID),
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(legacyFound).To(BeFalse())

		Expect(fixture.tickets.Transition(
			ticketID, tickets.StateRunning, tickets.StateQueued, tickets.TransitionMeta{},
		)).To(Succeed())
		requeued, err := dispatch.DispatchOne(context.Background(), fixture.deps, ticketID, "admin")
		Expect(err).NotTo(HaveOccurred())
		Expect(requeued.WorkflowRunID).NotTo(BeNil())
		Expect(*requeued.WorkflowRunID).NotTo(Equal(completed.ID))
	})

	It("promotes a prompt-only compatible version while keeping prior runs grouped and exact", func() {
		fixture := newWorkflowRunVerticalSlice("compatible")
		first := fixture.importAndPromote(1, "Review the subject.", "review", "review/v1")
		input := fixture.upload("subject.txt", []byte("versioned subject"))
		firstRun := fixture.bind(first.Version, input, "compatible-v1", nil).Run

		second := fixture.importAndPromote(1, "Review the subject more carefully.", "review", "review/v1")
		Expect(second.Version).To(Equal(2))
		secondRun := fixture.bind(second.Version, input, "compatible-v2", nil).Run

		storedFirst, found, err := fixture.runs.Get(context.Background(), defaultTeam.ID(), firstRun.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		storedSecond, found, err := fixture.runs.Get(context.Background(), defaultTeam.ID(), secondRun.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(storedFirst.WorkflowVersion).To(Equal(1))
		Expect(storedFirst.WorkflowDefinitionID).To(Equal(first.ID))
		Expect(storedFirst.DefinitionContentHash).To(Equal(first.ContentHash))
		Expect(storedSecond.WorkflowVersion).To(Equal(2))
		Expect(storedSecond.WorkflowDefinitionID).To(Equal(second.ID))
		Expect(storedSecond.DefinitionContentHash).To(Equal(second.ContentHash))
		Expect(storedSecond.ParameterizedConfigHash).NotTo(Equal(storedFirst.ParameterizedConfigHash))

		group, err := fixture.runs.List(context.Background(), db.AgentWorkflowRunListFilter{
			TeamID: defaultTeam.ID(), WorkflowName: fixture.name, Limit: 10,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(group).To(ConsistOf(
			HaveField("ID", firstRun.ID),
			HaveField("ID", secondRun.ID),
		))
	})

	It("rejects retry substitution across incompatible signature versions before durable allocation", func() {
		fixture := newWorkflowRunVerticalSlice("retry-signature")
		first := fixture.importAndPromote(1, "Review the subject.", "review", "review/v1")
		input := fixture.upload("subject.txt", []byte("retry subject"))
		firstRun := fixture.bind(first.Version, input, "retry-original", nil).Run

		incompatible := fixture.importAndPromote(2, "Summarize the subject.", "report", "opaque/v1")
		_, err := fixture.binder.BindAndCreate(context.Background(), fixture.admission(), workflowrun.BindRequest{
			WorkflowName: fixture.name, Version: &incompatible.Version,
			Inputs:         map[string]snapshot.SnapshotID{"subject": input.ID},
			IdempotencyKey: "retry-incompatible", RetryOf: &firstRun.ID,
		})
		Expect(err).To(MatchError(And(
			ContainSubstring("retry"),
			ContainSubstring("incompatible"),
		)))
		_, found, findErr := fixture.runs.FindByIdempotencyKey(context.Background(), defaultTeam.ID(), "retry-incompatible")
		Expect(findErr).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())
	})
})

type workflowRunVerticalSlice struct {
	name      string
	workflows db.AgentWorkflowsFactory
	snapshots db.AgentSnapshotsFactory
	runs      db.AgentWorkflowRunsFactory
	sealer    *snapshot.BatchSealer
	binder    *workflowrun.Binder
}

func newWorkflowRunVerticalSlice(suffix string) *workflowRunVerticalSlice {
	renderer := workflowrun.WorkflowTargetRenderer{
		RuntimeImage: "registry.example/agent-runner@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	workflows := db.NewAgentWorkflowsFactory(dbConn, renderer)
	snapshots := db.NewAgentSnapshotsFactory(dbConn)
	runs := db.NewAgentWorkflowRunsFactory(dbConn)
	registry, err := contracts.NewRegistry()
	Expect(err).NotTo(HaveOccurred())
	content := &workflowRunMemoryContent{objects: map[snapshot.Digest][]byte{}}
	sealer, err := snapshot.NewBatchSealer(
		snapshot.Canonicalizer{TempDir: GinkgoT().TempDir()}, registry, snapshots, content,
		db.NewAgentSnapshotDigestLocker(dbConn),
	)
	Expect(err).NotTo(HaveOccurred())
	templates, err := workflowrun.NewTemplateSaver(
		teamFactory,
		db.NewWorkflowRunTemplateFactory(dbConn, lockFactory),
	)
	Expect(err).NotTo(HaveOccurred())
	binder, err := workflowrun.NewBinder(
		workflowrun.WorkflowDefinitionStoreResolver{Store: workflows},
		renderer, snapshots, runs,
		workflowrun.AllowAllBudgetAdmitter{}, templates,
		db.NewPipelineRunFactory(logger, dbConn, lockFactory, checkFactory),
		workflowRunAllowAllCredentialAdmitter{},
	)
	Expect(err).NotTo(HaveOccurred())
	return &workflowRunVerticalSlice{
		name:      fmt.Sprintf("vertical-%s-%d", suffix, time.Now().UnixNano()),
		workflows: workflows, snapshots: snapshots, runs: runs, sealer: sealer, binder: binder,
	}
}

func (fixture *workflowRunVerticalSlice) importAndPromote(
	signatureVersion int,
	prompt string,
	outputName string,
	outputType snapshot.TypeRef,
) workflow.Definition {
	manifest := workflow.Manifest{
		"workflow.yml": fmt.Sprintf(`
schema_version: 3
name: %s
signature_version: %d
inputs:
  - name: subject
    type: opaque/v1
outputs:
  - name: %s
    type: %s
    from: %s
plan:
  - agent: review
    function_id: review
    prompt_file: prompts/review.md
    inputs: [subject]
    outputs: [%s]
    input_types:
      subject: {type: opaque/v1}
    output_types:
      %s: %s
`, fixture.name, signatureVersion, outputName, outputType, outputName, outputName, outputName, outputType),
		"prompts/review.md": prompt,
	}
	definition, err := fixture.workflows.ImportManifest(fixture.name, manifest, "alice")
	Expect(err).NotTo(HaveOccurred())
	_, err = fixture.workflows.Promote(fixture.name, definition.Version, "alice")
	Expect(err).NotTo(HaveOccurred())
	return *definition
}

func (fixture *workflowRunVerticalSlice) upload(name string, body []byte) snapshot.Snapshot {
	archive := workflowRunTar(map[string][]byte{name: body})
	value, err := fixture.sealer.Upload(context.Background(), snapshot.UploadRequest{
		TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(), UploadedBy: "alice",
		Actor: "vertical-slice", IdempotencyKey: fmt.Sprintf("upload-%d", time.Now().UnixNano()),
		Type: "opaque/v1",
		OpenTar: func(context.Context) (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(archive)), nil
		},
		SourceMetadata: json.RawMessage(`{"adapter":"vertical-slice"}`),
	})
	Expect(err).NotTo(HaveOccurred())
	return value
}

func (fixture *workflowRunVerticalSlice) admission() workflowrun.AdmissionContext {
	return workflowrun.AdmissionContext{
		TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(), CreatedBy: "alice",
		Origin: workflowrun.Origin{Kind: "manual"},
	}
}

func (fixture *workflowRunVerticalSlice) bind(
	version int,
	input snapshot.Snapshot,
	key string,
	retryOf *snapshot.WorkflowRunID,
) workflowrun.BindResult {
	result, err := fixture.binder.BindAndCreate(context.Background(), fixture.admission(), workflowrun.BindRequest{
		WorkflowName: fixture.name, Version: &version,
		Inputs:         map[string]snapshot.SnapshotID{"subject": input.ID},
		IdempotencyKey: key, RetryOf: retryOf,
	})
	Expect(err).NotTo(HaveOccurred())
	return result
}

// Agent pods read the platform secret themselves, so binding needs only the
// presence check; this double always admits.
type workflowRunAllowAllCredentialAdmitter struct{}

func (workflowRunAllowAllCredentialAdmitter) AdmitModelCredential(context.Context) error { return nil }

type workflowRunMemoryContent struct {
	mutex   sync.Mutex
	objects map[snapshot.Digest][]byte
}

func (store *workflowRunMemoryContent) Put(
	_ context.Context,
	digest snapshot.Digest,
	reader io.Reader,
) ([]snapshot.Location, error) {
	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	if workflowRunDigest(body) != digest {
		return nil, fmt.Errorf("vertical-slice content digest mismatch")
	}
	store.mutex.Lock()
	store.objects[digest] = append([]byte(nil), body...)
	store.mutex.Unlock()
	return []snapshot.Location{{Digest: digest, Driver: "vertical-memory", Key: digest.String()}}, nil
}

func (store *workflowRunMemoryContent) Open(_ context.Context, value snapshot.Snapshot) (io.ReadCloser, error) {
	store.mutex.Lock()
	body, found := store.objects[value.Digest]
	store.mutex.Unlock()
	if !found {
		return nil, fmt.Errorf("vertical-slice content %s not found", value.Digest)
	}
	return io.NopCloser(bytes.NewReader(append([]byte(nil), body...))), nil
}

func (store *workflowRunMemoryContent) Exists(_ context.Context, location snapshot.Location) (bool, error) {
	store.mutex.Lock()
	body, found := store.objects[location.Digest]
	store.mutex.Unlock()
	return found && workflowRunDigest(body) == location.Digest, nil
}

func (store *workflowRunMemoryContent) DeleteLocation(_ context.Context, location snapshot.Location) error {
	store.mutex.Lock()
	delete(store.objects, location.Digest)
	store.mutex.Unlock()
	return nil
}

func (store *workflowRunMemoryContent) DeleteAll(_ context.Context, digest snapshot.Digest) error {
	store.mutex.Lock()
	delete(store.objects, digest)
	store.mutex.Unlock()
	return nil
}

func workflowRunDigest(body []byte) snapshot.Digest {
	sum := sha256.Sum256(body)
	return snapshot.Digest("sha256:" + hex.EncodeToString(sum[:]))
}

func workflowRunTar(files map[string][]byte) []byte {
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		body := files[name]
		Expect(writer.WriteHeader(&tar.Header{
			Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg,
		})).To(Succeed())
		_, err := writer.Write(body)
		Expect(err).NotTo(HaveOccurred())
	}
	Expect(writer.Close()).To(Succeed())
	return buffer.Bytes()
}

func findAgentPlan(plan atc.Plan, name string) *atc.Plan {
	var found *atc.Plan
	plan.Each(func(candidate *atc.Plan) {
		if found == nil && candidate.Agent != nil && candidate.Agent.Name == name {
			copy := *candidate
			found = &copy
		}
	})
	return found
}

var _ snapshot.ContentStore = (*workflowRunMemoryContent)(nil)
