package db_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/api/workflowoutcomes"
	"github.com/concourse/concourse/agent/fixtureagent"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/agent/workflowrun"
	"github.com/concourse/concourse/agent/workflowwait"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/builds"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/exec/build"
	"github.com/concourse/concourse/atc/exec/execfakes"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/tracing"
	"github.com/concourse/concourse/vars"
	oteltrace "go.opentelemetry.io/otel/trace"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// chainedWorkflowManifest is the smallest function that exercises every link
// this test exists to prove: a public input materialized by the renderer's
// load_snapshot, one agent producing a typed output, an await_snapshot answered
// server-side, and a second agent consuming BOTH the first agent's output (by
// artifact name, the only intra-plan chaining a workflow permits — see
// agent/workflow/render.go:364) and the sealed human answer.
//
// final-review is declared as a bare type reference even though it backs the
// public verdict port: an output_types entry accepts only type and optional
// (agent/workflow/parse.go:482), because workflow retention and the port name
// are attached by the platform in AnnotatePublicOutputs
// (agent/workflow/typecheck.go:103-104) from the outputs: block's from: field.
// Authoring them here is rejected at import.
func chainedWorkflowManifest(name string) workflow.Manifest {
	return workflow.Manifest{
		"workflow.yml": fmt.Sprintf(`schema_version: 3
name: %s
signature_version: 1
disposition_output: verdict
inputs:
  - name: change
    type: opaque/v1
outputs:
  - name: verdict
    type: review/v1
    from: final-review
plan:
  - agent: review
    function_id: review
    prompt: Review the exposed change, write review/v1, and ask for approval.
    inputs: [change]
    outputs: [draft-review, approval-question]
    input_types:
      change: {type: opaque/v1}
    output_types:
      draft-review: review/v1
      approval-question: question/v1
  - await_snapshot: approval
    question: approval-question
    type: human-answer/v1
    on_timeout: fail
    timeout: 1h
  - agent: confirm
    function_id: confirm
    prompt: Re-issue the reviewed verdict once a human approved it.
    inputs: [change, draft-review, approval]
    outputs: [final-review]
    input_types:
      change: {type: opaque/v1}
      draft-review: {type: review/v1}
      approval: {type: human-answer/v1}
    output_types:
      final-review: review/v1
`, name),
	}
}

var _ = Describe("agent workflow run chained DAG", func() {
	It("chains a sealed typed output into the next step, answers a wait server-side, and records the outcome", func() {
		name := fmt.Sprintf("chained-%d", time.Now().UnixNano())
		renderer := workflowrun.WorkflowTargetRenderer{
			RuntimeImage: "registry.example/agent-runner@sha256:" + strings.Repeat("a", 64),
		}
		workflows := db.NewAgentWorkflowsFactory(dbConn, renderer)
		snapshots := db.NewAgentSnapshotsFactory(dbConn)
		runs := db.NewAgentWorkflowRunsFactory(dbConn)
		waits := db.NewAgentWorkflowWaitsFactory(dbConn)
		outcomes := db.NewAgentWorkflowOutcomesFactory(dbConn)

		registry, err := contracts.NewRegistry(
			contracts.WithCanonicalizer(snapshot.Canonicalizer{TempDir: GinkgoT().TempDir()}),
		)
		Expect(err).NotTo(HaveOccurred())
		content := &workflowRunMemoryContent{objects: map[snapshot.Digest][]byte{}}
		sealer, err := snapshot.NewBatchSealer(
			snapshot.Canonicalizer{TempDir: GinkgoT().TempDir()}, registry, snapshots, content,
			db.NewAgentSnapshotDigestLocker(dbConn),
		)
		Expect(err).NotTo(HaveOccurred())

		definition, err := workflows.ImportManifest(name, chainedWorkflowManifest(name), "alice")
		Expect(err).NotTo(HaveOccurred())
		_, err = workflows.Promote(name, definition.Version, "alice")
		Expect(err).NotTo(HaveOccurred())

		// The public input: an ordinary uploaded opaque/v1 value.
		changeArchive := workflowRunTar(map[string][]byte{"payload.txt": []byte("subject under review\n")})
		change, err := sealer.Upload(context.Background(), snapshot.UploadRequest{
			TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(), UploadedBy: "alice",
			Actor: "chained", IdempotencyKey: name + "-change", Type: "opaque/v1",
			OpenTar: func(context.Context) (io.ReadCloser, error) {
				return io.NopCloser(bytes.NewReader(changeArchive)), nil
			},
			SourceMetadata: json.RawMessage(`{"adapter":"chained"}`),
		})
		Expect(err).NotTo(HaveOccurred())

		templates, err := workflowrun.NewTemplateSaver(
			teamFactory, db.NewWorkflowRunTemplateFactory(dbConn, lockFactory),
		)
		Expect(err).NotTo(HaveOccurred())
		binder, err := workflowrun.NewBinder(
			workflowrun.WorkflowDefinitionStoreResolver{Store: workflows},
			renderer, snapshots, runs, workflowrun.AllowAllBudgetAdmitter{}, templates,
			db.NewPipelineRunFactory(logger, dbConn, lockFactory, checkFactory),
			workflowRunNoopSecretPreparer{},
		)
		Expect(err).NotTo(HaveOccurred())
		bound, err := binder.BindAndCreate(context.Background(), workflowrun.AdmissionContext{
			TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(), CreatedBy: "alice",
			Origin: workflowrun.Origin{Kind: "manual"},
		}, workflowrun.BindRequest{
			WorkflowName: name, Version: &definition.Version,
			Inputs:         map[string]snapshot.SnapshotID{"change": change.ID},
			IdempotencyKey: name + "-run",
		})
		Expect(err).NotTo(HaveOccurred())
		runID := bound.Run.ID

		stored, found, err := runs.Get(context.Background(), defaultTeam.ID(), runID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		var concrete atc.Config
		Expect(atc.UnmarshalConfig(stored.ConcreteConfig, &concrete)).To(Succeed())
		plan, err := builds.NewPlanner(atc.NewPlanFactory(0)).Create(
			concrete.Jobs[0].StepConfig(), nil, concrete.ResourceTypes, concrete.Prototypes, nil, false,
		)
		Expect(err).NotTo(HaveOccurred())
		reviewPlan := findAgentPlan(plan, "review")
		confirmPlan := findAgentPlan(plan, "confirm")
		Expect(reviewPlan).NotTo(BeNil())
		Expect(confirmPlan).NotTo(BeNil())

		// The chaining is the run's own plan, not the test's arrangement: the
		// rendered confirm step declares the review step's output as one of its
		// typed inputs, under the same artifact name.
		Expect(reviewPlan.Agent.Outputs).To(ContainElement("draft-review"))
		Expect(confirmPlan.Agent.Inputs).To(ContainElement("draft-review"))
		Expect(confirmPlan.Agent.SnapshotInputs).To(HaveKeyWithValue(
			"draft-review", atc.SnapshotInputConfig{Type: "review/v1"},
		))
		// And the public-port metadata the manifest is forbidden to author is
		// what AnnotatePublicOutputs attached to the producing step.
		Expect(confirmPlan.Agent.SnapshotOutputs["final-review"].Retention).
			To(Equal(snapshot.RetentionClassWorkflow))
		Expect(confirmPlan.Agent.SnapshotOutputs["final-review"].WorkflowPort).To(Equal("verdict"))

		buildRow, found, err := buildFactory.Build(int(*stored.PlannedBuildID))
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		started, err := buildRow.Start(plan)
		Expect(err).NotTo(HaveOccurred())
		Expect(started).To(BeTrue())

		// ---- Step 0: the renderer's load_snapshot materializes the public input.
		state := exec.NewRunState(noopChainedStepper, vars.StaticVariables{})
		repository := state.ArtifactRepository()
		delegates := chainedDelegates()
		loadPlan := findLoadSnapshotPlan(plan, "change")
		Expect(loadPlan).NotTo(BeNil())
		loadStep := exec.NewLoadSnapshotStep(
			loadPlan.ID, *loadPlan.LoadSnapshot,
			exec.StepMetadata{TeamID: defaultTeam.ID(), BuildID: buildRow.ID()},
			delegates, snapshots, content, runs,
		)
		ok, err := loadStep.Run(context.Background(), state)
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
		loaded, found := repository.ArtifactEntryFor("change")
		Expect(found).To(BeTrue())
		Expect(loaded.Snapshot.ID).To(Equal(change.ID))

		// ---- Step 1: the first agent's sealed typed outputs.
		reviewDigest, _ := contracts.SchemaDigestFor(snapshot.TypeRef("review/v1"))
		draftTar, err := fixtureagent.Tar(fixtureagent.CaseReviewAccept, fixtureagent.Authority{
			RecordType: "review/v1", RecordSchema: reviewDigest.String(),
			SubjectInput: "change", SubjectType: "opaque/v1", SubjectDigest: change.Digest.String(),
		})
		Expect(err).NotTo(HaveOccurred())
		// question/v1 decodes question.json with DisallowUnknownFields and
		// requires both prompt and context (contracts/interaction.go).
		questionTar := workflowRunTar(map[string][]byte{
			"question.json": []byte(`{"schema_version":"1.0.0","prompt":"Approve this exact review?","context":"chained DAG integration test"}`),
		})
		definitionID := definition.ID
		sealedFirst, err := sealer.Seal(context.Background(), snapshot.SealRequest{
			BuildID: buildRow.ID(), TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(), CreatedBy: "alice",
			PlanID: reviewPlan.ID.String(), Attempt: "1", StepKind: "agent", StepName: "review",
			WorkflowDefinitionID: &definitionID, WorkflowRunID: &runID,
			InputOrder: []string{"change"},
			Inputs:     map[string]snapshot.SnapshotRef{"change": *loaded.Snapshot},
			InputExposures: map[string]snapshot.InputExposure{
				"change": snapshot.FullTreeExposure("/tmp/build/change", change.Digest),
			},
			OutputDeclarations: []snapshot.Port{
				{Name: "approval-question", Type: "question/v1"},
				{Name: "draft-review", Type: "review/v1"},
			},
			Outputs: []snapshot.OutputSource{
				{
					ClientKey: "approval-question", Port: snapshot.Port{Name: "approval-question", Type: "question/v1"},
					OpenTar: func(context.Context) (io.ReadCloser, error) {
						return io.NopCloser(bytes.NewReader(questionTar)), nil
					},
				},
				{
					ClientKey: "draft-review", Port: snapshot.Port{Name: "draft-review", Type: "review/v1"},
					OpenTar: func(context.Context) (io.ReadCloser, error) {
						return io.NopCloser(bytes.NewReader(draftTar)), nil
					},
				},
			},
		})
		Expect(err).NotTo(HaveOccurred())
		draftRef := sealedFirst["draft-review"].Snapshot
		questionRef := sealedFirst["approval-question"].Snapshot

		draftArtifact, err := chainedArtifact(context.Background(), snapshots, defaultTeam.ID(), draftRef, content)
		Expect(err).NotTo(HaveOccurred())
		questionArtifact, err := chainedArtifact(context.Background(), snapshots, defaultTeam.ID(), questionRef, content)
		Expect(err).NotTo(HaveOccurred())
		Expect(repository.RegisterArtifacts(map[build.ArtifactName]build.ArtifactEntry{
			"draft-review":      {Artifact: draftArtifact, Snapshot: &draftRef},
			"approval-question": {Artifact: questionArtifact, Snapshot: &questionRef},
		})).To(Succeed())

		// ---- Step 2: the await, answered server-side while the step polls.
		awaitPlan := findAwaitSnapshotPlan(plan, "approval")
		Expect(awaitPlan).NotTo(BeNil())
		awaitStep := exec.NewAwaitSnapshotStep(
			awaitPlan.ID, nil, *awaitPlan.AwaitSnapshot,
			exec.StepMetadata{TeamID: defaultTeam.ID(), BuildID: buildRow.ID()},
			delegates, waits, sealer, snapshots, content, 10*time.Millisecond,
		)
		awaitCtx, cancelAwait := context.WithTimeout(context.Background(), time.Minute)
		defer cancelAwait()

		answered := make(chan error, 1)
		go func() {
			defer GinkgoRecover()
			answered <- answerChainedWait(awaitCtx, waits, sealer, runID)
		}()

		ok, err = awaitStep.Run(awaitCtx, state)
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
		Eventually(answered, time.Minute).Should(Receive(BeNil()))

		approval, found := repository.ArtifactEntryFor("approval")
		Expect(found).To(BeTrue())
		Expect(approval.Snapshot.Type).To(Equal(snapshot.TypeRef("human-answer/v1")))

		// ---- Step 3: the second agent consumes step 1's sealed output BY NAME
		// plus the sealed answer. This is the chaining claim: the subject the
		// final record binds is the digest step 1 committed, not a literal.
		finalTar, err := fixtureagent.Tar(fixtureagent.CaseReviewAccept, fixtureagent.Authority{
			RecordType: "review/v1", RecordSchema: reviewDigest.String(),
			SubjectInput: "draft-review", SubjectType: "review/v1", SubjectDigest: draftRef.Digest.String(),
		})
		Expect(err).NotTo(HaveOccurred())
		sealedSecond, err := sealer.Seal(context.Background(), snapshot.SealRequest{
			BuildID: buildRow.ID(), TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(), CreatedBy: "alice",
			PlanID: confirmPlan.ID.String(), Attempt: "1", StepKind: "agent", StepName: "confirm",
			WorkflowDefinitionID: &definitionID, WorkflowRunID: &runID,
			InputOrder: []string{"approval", "change", "draft-review"},
			Inputs: map[string]snapshot.SnapshotRef{
				"approval": *approval.Snapshot, "change": *loaded.Snapshot, "draft-review": draftRef,
			},
			InputExposures: map[string]snapshot.InputExposure{
				"approval":     snapshot.FullTreeExposure("/tmp/build/approval", approval.Snapshot.Digest),
				"change":       snapshot.FullTreeExposure("/tmp/build/change", change.Digest),
				"draft-review": snapshot.FullTreeExposure("/tmp/build/draft-review", draftRef.Digest),
			},
			OutputDeclarations: []snapshot.Port{{Name: "final-review", Type: "review/v1"}},
			Outputs: []snapshot.OutputSource{{
				ClientKey: "final-review", Port: snapshot.Port{Name: "final-review", Type: "review/v1"},
				Retention: snapshot.RetentionClassWorkflow, WorkflowPort: "verdict",
				OpenTar: func(context.Context) (io.ReadCloser, error) {
					return io.NopCloser(bytes.NewReader(finalTar)), nil
				},
			}},
		})
		Expect(err).NotTo(HaveOccurred())
		verdict := sealedSecond["final-review"].Snapshot

		// ---- Terminal disposition.
		Expect(buildRow.Finish(db.BuildStatusSucceeded)).To(Succeed())
		later := time.Now().Add(time.Hour)
		reconciler, err := workflowrun.NewReconciler(
			runs, logger, 10*time.Minute, time.Minute,
			workflowrun.WithReconcilerClock(func() time.Time { return later }),
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(reconciler.Run(context.Background())).To(Succeed())

		completed, found, err := runs.Get(context.Background(), defaultTeam.ID(), runID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(completed.Status).To(Equal(db.AgentWorkflowRunStatusSucceeded))

		// The lineage the chain produced: change is an input, verdict is the
		// public output, and the intermediate draft is bound to the run too.
		bindings, err := runs.Snapshots(context.Background(), runID)
		Expect(err).NotTo(HaveOccurred())
		Expect(bindings).To(ContainElement(db.AgentWorkflowRunSnapshotBinding{
			WorkflowRunID: runID, Direction: db.AgentWorkflowRunSnapshotInput,
			PortName: "change", Snapshot: snapshot.SnapshotRef{ID: change.ID, Type: "opaque/v1", Digest: change.Digest},
		}))
		Expect(bindings).To(ContainElement(db.AgentWorkflowRunSnapshotBinding{
			WorkflowRunID: runID, Direction: db.AgentWorkflowRunSnapshotOutput,
			PortName: "verdict", Snapshot: verdict,
		}))

		// The outcome row: recordable only because the reconciler promoted the
		// output binding, which is the platform's definition of "this run
		// really produced this value".
		outcome, created, err := outcomes.Record(context.Background(), defaultTeam.ID(), workflowoutcomes.RecordRequest{
			WorkflowRunID: runID, OutputSnapshotID: verdict.ID,
			Disposition:      workflowoutcomes.DispositionAccepted,
			PublicationState: workflowoutcomes.PublicationNotRequested,
			Actor:            "alice",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())
		Expect(outcome.Disposition).To(Equal(workflowoutcomes.DispositionAccepted))
		Expect(outcome.OutputSnapshotID).To(Equal(verdict.ID))
	})
})

// answerChainedWait performs the server-side two-phase answer: reserve the
// durable intent, materialize the deterministic human-answer/v1 snapshot, then
// CAS it onto the wait. There is no single "answer" call.
func answerChainedWait(
	ctx context.Context,
	waits db.AgentWorkflowWaitsFactory,
	creator workflowwait.AnswerSnapshotCreator,
	runID snapshot.WorkflowRunID,
) error {
	var pending workflowwait.Wait
	Eventually(func() bool {
		listed, err := waits.List(ctx, defaultTeam.ID(), runID)
		if err != nil || len(listed) == 0 {
			return false
		}
		pending = listed[0]
		return pending.Status == workflowwait.StatusWaiting
	}, 30*time.Second, 10*time.Millisecond).Should(BeTrue())

	wait, intent, _, err := waits.ReserveResolution(ctx, workflowwait.ReserveResolutionRequest{
		TeamID: defaultTeam.ID(), WorkflowRunID: runID, WaitID: pending.ID,
		AnswerValue: "approved", Actor: "alice", DisplayName: "Alice",
	})
	if err != nil {
		return err
	}
	answer, err := workflowwait.MaterializeAnswer(
		ctx, creator, defaultTeam.ID(), defaultTeam.Name(), runID, wait.ID, intent,
	)
	if err != nil {
		return err
	}
	_, _, err = waits.Resolve(ctx, workflowwait.ResolveRequest{
		TeamID: defaultTeam.ID(), WorkflowRunID: runID, WaitID: wait.ID,
		Answer:      snapshot.SnapshotRef{ID: answer.ID, Type: answer.Type, Digest: answer.Digest},
		AnswerValue: intent.AnswerValue, Actor: intent.Actor,
		DisplayName: intent.DisplayName, ReservedAt: intent.ReservedAt,
	})
	return err
}

func chainedArtifact(
	ctx context.Context,
	store db.AgentSnapshotsFactory,
	teamID int,
	ref snapshot.SnapshotRef,
	content snapshot.ContentStore,
) (runtime.Artifact, error) {
	manifest, found, err := store.GetAuthorized(ctx, teamID, ref.ID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("sealed snapshot %s is unavailable", ref.ID)
	}
	return runtime.NewSnapshotArtifact(manifest, content)
}

func chainedDelegates() *execfakes.FakeBuildStepDelegateFactory {
	delegate := new(execfakes.FakeBuildStepDelegate)
	delegate.StartSpanCalls(func(ctx context.Context, _ string, _ tracing.Attrs) (context.Context, oteltrace.Span) {
		return ctx, tracing.NoopSpan
	})
	delegates := new(execfakes.FakeBuildStepDelegateFactory)
	delegates.BuildStepDelegateReturns(delegate)
	return delegates
}

var noopChainedStepper exec.Stepper = func(atc.Plan) exec.Step { return nil }

func findLoadSnapshotPlan(plan atc.Plan, name string) *atc.Plan {
	var found *atc.Plan
	plan.Each(func(candidate *atc.Plan) {
		if found == nil && candidate.LoadSnapshot != nil && candidate.LoadSnapshot.Name == name {
			clone := *candidate
			found = &clone
		}
	})
	return found
}

func findAwaitSnapshotPlan(plan atc.Plan, name string) *atc.Plan {
	var found *atc.Plan
	plan.Each(func(candidate *atc.Plan) {
		if found == nil && candidate.AwaitSnapshot != nil && candidate.AwaitSnapshot.Name == name {
			clone := *candidate
			found = &clone
		}
	})
	return found
}
