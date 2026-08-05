package db_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"code.cloudfoundry.org/lager/v3/lagertest"

	"github.com/concourse/concourse/agent/publisher"
	schema "github.com/concourse/concourse/agent/schema"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/agent/workflow/graph"
	"github.com/concourse/concourse/agent/workflowrun"
	"github.com/concourse/concourse/agent/workflowrun/occurrence"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/builds"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/event"
)

// seedManifest loads one of the real seed workflows. Synthetic fixtures have
// missed bugs at every phase of this work; the seeds are what the rest of the
// system actually accepts.
func seedManifest(name string) workflow.Manifest {
	manifest, err := workflow.ManifestFromDir(
		filepath.Join("..", "..", "agent", "workflow", "seeds", name),
	)
	Expect(err).ToNot(HaveOccurred())
	return manifest
}

// planSeedDefinition renders a stored definition and runs the real build
// planner over it, returning the frozen actual plan exactly as a run stores it.
// The plan IDs it produces are the ones the projection joins evidence on, so
// nothing short of the real planner proves the join.
func planSeedDefinition(definition workflow.Definition) []byte {
	target, err := workflow.FullFunctionTarget(definition)
	Expect(err).ToNot(HaveOccurred())
	rendered, err := workflow.RenderFunction(target)
	Expect(err).ToNot(HaveOccurred())

	params := map[string]any{}
	for index, param := range rendered.Config.Params {
		params[param.Name] = strconv.Itoa(9000001 + index)
	}
	validated, err := atc.ValidateRunParams(rendered.Config.Params, params)
	Expect(err).ToNot(HaveOccurred())
	concrete, err := atc.MaterializeRunConfig(rendered.Config, 1, 7, validated)
	Expect(err).ToNot(HaveOccurred())
	Expect(concrete.Jobs).To(HaveLen(1))

	plan, err := builds.NewPlanner(atc.NewPlanFactory(0)).Create(
		concrete.Jobs[0].StepConfig(), nil, concrete.ResourceTypes, concrete.Prototypes, nil, false,
	)
	Expect(err).ToNot(HaveOccurred())
	raw, err := json.Marshal(plan)
	Expect(err).ToNot(HaveOccurred())
	return raw
}

// planIDForNode locates the single plan node carrying one workflow-local
// identity.
func planIDForNode(actualPlan []byte, nodeID string) string {
	nodes, err := occurrence.PlanNodes(actualPlan)
	Expect(err).ToNot(HaveOccurred())
	var found []string
	for _, node := range nodes {
		if node.NodeID == nodeID {
			found = append(found, node.PlanID)
		}
	}
	Expect(found).To(HaveLen(1), "expected exactly one plan node %q", nodeID)
	return found[0]
}

func executionNodeIDs(definition workflow.Definition) []string {
	built, err := graph.Build(definition.Compiled.Function)
	Expect(err).ToNot(HaveOccurred())
	ids := []string{}
	for id := range occurrence.ExecutionNodesOf(built) {
		ids = append(ids, id)
	}
	return ids
}

var _ = Describe("AgentWorkflowRunEvidenceFactory NodeOccurrence sources", func() {
	var (
		factory db.AgentWorkflowRunEvidenceFactory
		ctx     context.Context
	)

	BeforeEach(func() {
		factory = db.NewAgentWorkflowRunEvidenceFactory(dbConn)
		ctx = context.Background()
	})

	// A run bound to a real build, so the evidence reader has a real partition
	// of build_events to find.
	newRun := func(build db.Build) db.AgentWorkflowRun {
		name := fmt.Sprintf("evidence-%d", time.Now().UnixNano())
		runID := snapshot.WorkflowRunID(createWorkflowRun(name, 3, build.ID()))
		buildID := int64(build.ID())
		return db.AgentWorkflowRun{
			ID: runID, TeamID: defaultTeam.ID(), WorkflowName: name,
			WorkflowVersion: 3, PlannedBuildID: &buildID,
		}
	}

	Describe("deterministic task steps", func() {
		// The whole projection exists for this path. A task step has no
		// attempt metric, no wait, and no publication: its terminal state
		// lives only in build events, which Concourse GC reclaims. A reader
		// that is wrong here loses exactly the data the projection was built
		// to preserve, while every other node kind still populates.
		It("reads a finished task's terminal status out of build events, keyed by plan ID", func() {
			build, err := defaultTeam.CreateOneOffBuild()
			Expect(err).ToNot(HaveOccurred())
			Expect(build.SaveEvent(event.FinishTask{
				ExitStatus: 0, Time: 100,
				Origin: event.Origin{ID: event.OriginID("1/3")},
			})).To(Succeed())
			Expect(build.SaveEvent(event.FinishTask{
				ExitStatus: 2, Time: 101,
				Origin: event.Origin{ID: event.OriginID("1/4")},
			})).To(Succeed())
			Expect(build.Finish(db.BuildStatusFailed)).To(Succeed())

			evidence, err := factory.EvidenceForRun(ctx, newRun(build))
			Expect(err).ToNot(HaveOccurred())
			Expect(evidence.BuildStepStatus).To(Equal(map[string]string{
				"1/3": db.AgentNodeBuildStepSucceeded,
				"1/4": db.AgentNodeBuildStepFailed,
			}))
		})

		It("reads the generic step finish event, which non-task steps write instead", func() {
			build, err := defaultTeam.CreateOneOffBuild()
			Expect(err).ToNot(HaveOccurred())
			Expect(build.SaveEvent(event.Finish{
				Succeeded: true, Time: 100,
				Origin: event.Origin{ID: event.OriginID("1/5")},
			})).To(Succeed())
			Expect(build.SaveEvent(event.Finish{
				Succeeded: false, Time: 101,
				Origin: event.Origin{ID: event.OriginID("1/6")},
			})).To(Succeed())

			evidence, err := factory.EvidenceForRun(ctx, newRun(build))
			Expect(err).ToNot(HaveOccurred())
			Expect(evidence.BuildStepStatus).To(Equal(map[string]string{
				"1/5": db.AgentNodeBuildStepSucceeded,
				"1/6": db.AgentNodeBuildStepFailed,
			}))
		})

		It("records a step that errored", func() {
			build, err := defaultTeam.CreateOneOffBuild()
			Expect(err).ToNot(HaveOccurred())
			Expect(build.SaveEvent(event.Error{
				Message: "worker disappeared", Time: 100,
				Origin: event.Origin{ID: event.OriginID("1/7")},
			})).To(Succeed())

			evidence, err := factory.EvidenceForRun(ctx, newRun(build))
			Expect(err).ToNot(HaveOccurred())
			Expect(evidence.BuildStepStatus).To(HaveKeyWithValue("1/7", db.AgentNodeBuildStepErrored))
		})

		// A step that reported a result and then errored is errored: the last
		// thing the engine recorded is what actually happened to it.
		It("lets the last recorded event win for one step", func() {
			build, err := defaultTeam.CreateOneOffBuild()
			Expect(err).ToNot(HaveOccurred())
			Expect(build.SaveEvent(event.FinishTask{
				ExitStatus: 0, Time: 100, Origin: event.Origin{ID: event.OriginID("1/8")},
			})).To(Succeed())
			Expect(build.SaveEvent(event.Error{
				Message: "interrupted", Time: 101, Origin: event.Origin{ID: event.OriginID("1/8")},
			})).To(Succeed())

			evidence, err := factory.EvidenceForRun(ctx, newRun(build))
			Expect(err).ToNot(HaveOccurred())
			Expect(evidence.BuildStepStatus).To(HaveKeyWithValue("1/8", db.AgentNodeBuildStepErrored))
		})

		// Build-level events belong to no step. Admitting one under an empty
		// key would put a row with no node identity into the projection.
		It("ignores events that carry no step origin", func() {
			build, err := defaultTeam.CreateOneOffBuild()
			Expect(err).ToNot(HaveOccurred())
			Expect(build.SaveEvent(event.Error{Message: "build blew up", Time: 100})).To(Succeed())
			Expect(build.SaveEvent(event.Status{
				Status: atc.StatusSucceeded, Time: 101,
			})).To(Succeed())

			evidence, err := factory.EvidenceForRun(ctx, newRun(build))
			Expect(err).ToNot(HaveOccurred())
			Expect(evidence.BuildStepStatus).To(BeEmpty())
		})

		// Non-terminal events are not evidence of an outcome. Treating a start
		// as a conclusion would freeze a running step as finished.
		It("ignores events that are not terminal", func() {
			build, err := defaultTeam.CreateOneOffBuild()
			Expect(err).ToNot(HaveOccurred())
			Expect(build.SaveEvent(event.StartTask{
				Time: 100, Origin: event.Origin{ID: event.OriginID("1/9")},
			})).To(Succeed())
			Expect(build.SaveEvent(event.Log{
				Payload: "hello", Time: 101, Origin: event.Origin{ID: event.OriginID("1/9")},
			})).To(Succeed())

			evidence, err := factory.EvidenceForRun(ctx, newRun(build))
			Expect(err).ToNot(HaveOccurred())
			Expect(evidence.BuildStepStatus).To(BeEmpty())
		})

		// Build events live in a per-pipeline partition, and reading the wrong
		// one silently returns nothing at all.
		It("reads a pipeline build's events from its own partition", func() {
			build, err := defaultJob.CreateBuild(defaultBuildCreatedBy)
			Expect(err).ToNot(HaveOccurred())
			Expect(build.SaveEvent(event.FinishTask{
				ExitStatus: 0, Time: 100, Origin: event.Origin{ID: event.OriginID("2/1")},
			})).To(Succeed())

			evidence, err := factory.EvidenceForRun(ctx, newRun(build))
			Expect(err).ToNot(HaveOccurred())
			Expect(evidence.BuildStepStatus).To(HaveKeyWithValue("2/1", db.AgentNodeBuildStepSucceeded))
		})

		It("returns no build-scoped evidence for a run that never planned a build", func() {
			name := fmt.Sprintf("evidence-unplanned-%d", time.Now().UnixNano())
			runID := snapshot.WorkflowRunID(createWorkflowRun(name, 3, createBuildWithStatus(db.BuildStatusSucceeded)))

			evidence, err := factory.EvidenceForRun(ctx, db.AgentWorkflowRun{
				ID: runID, TeamID: defaultTeam.ID(), WorkflowName: name, WorkflowVersion: 3,
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(evidence.BuildStepStatus).To(BeEmpty())
			Expect(evidence.AttemptMetrics).To(BeEmpty())
		})

		It("treats a reclaimed build as missing evidence rather than a fault", func() {
			build, err := defaultTeam.CreateOneOffBuild()
			Expect(err).ToNot(HaveOccurred())
			run := newRun(build)
			gone := int64(build.ID()) + 100000
			run.PlannedBuildID = &gone

			evidence, err := factory.EvidenceForRun(ctx, run)
			Expect(err).ToNot(HaveOccurred())
			Expect(evidence.BuildStepStatus).To(BeEmpty())
		})
	})

	Describe("agent step attempt metrics", func() {
		// Attempt metrics are keyed by BUILD, not by run, and both live on the
		// same evidence struct. Reading them for the wrong identifier returns
		// an empty set that is indistinguishable from a run whose agent steps
		// never ran.
		It("reads only the run's own build, not every build that ran an agent", func() {
			build, err := defaultTeam.CreateOneOffBuild()
			Expect(err).ToNot(HaveOccurred())
			other, err := defaultTeam.CreateOneOffBuild()
			Expect(err).ToNot(HaveOccurred())
			run := newRun(build)

			metricsFactory := db.NewAgentRunMetricsFactory(dbConn)
			for _, seed := range []struct {
				buildID int
				planID  string
				cost    float64
			}{
				{build.ID(), "1/2", 0.25},
				{build.ID(), "1/3", 0.75},
				{other.ID(), "1/2", 9.99},
			} {
				_, _, err := metricsFactory.UpsertReturningInserted(&schema.RunMetrics{
					BuildID: seed.buildID, PlanID: seed.planID, StepName: "implement",
					Status: "ok", CostUSD: seed.cost, WallTimeSeconds: 30,
				})
				Expect(err).ToNot(HaveOccurred())
			}

			evidence, err := factory.EvidenceForRun(ctx, run)
			Expect(err).ToNot(HaveOccurred())
			Expect(evidence.AttemptMetrics).To(HaveLen(2))
			Expect(evidence.AttemptMetrics[0].PlanID).To(Equal("1/2"))
			Expect(evidence.AttemptMetrics[0].CostUSD).To(BeNumerically("~", 0.25, 1e-9))
			Expect(evidence.AttemptMetrics[1].PlanID).To(Equal("1/3"))
			Expect(evidence.AttemptMetrics[1].CostUSD).To(BeNumerically("~", 0.75, 1e-9))
		})

		// The production composition has never enabled checkpoints, so no
		// attempt row is ever written and agent_run_attempt_metrics is empty.
		// A reader that only knows that table reports every agent node as
		// having cost nothing, and the freeze makes that permanent.
		It("reads an agent step whose only record is the aggregate production actually writes", func() {
			build, err := defaultTeam.CreateOneOffBuild()
			Expect(err).ToNot(HaveOccurred())
			run := newRun(build)

			_, _, err = db.NewAgentRunMetricsFactory(dbConn).UpsertReturningInserted(&schema.RunMetrics{
				BuildID: build.ID(), PlanID: "1/2", StepName: "implement",
				Status: "ok", CostUSD: 1.70, WallTimeSeconds: 392,
			})
			Expect(err).ToNot(HaveOccurred())

			evidence, err := factory.EvidenceForRun(ctx, run)
			Expect(err).ToNot(HaveOccurred())
			Expect(evidence.AttemptMetrics).To(HaveLen(1))
			Expect(evidence.AttemptMetrics[0].PlanID).To(Equal("1/2"))
			Expect(evidence.AttemptMetrics[0].Status).To(Equal("ok"))
			Expect(evidence.AttemptMetrics[0].CostUSD).To(BeNumerically("~", 1.70, 1e-9))
			Expect(evidence.AttemptMetrics[0].ExecutionAttempt).To(Equal(1))
		})

		// agent_run_metrics has no updated_at, and its created_at defaults to
		// now() on a row written after the agent process exits — so it stamps
		// COMPLETION, not start. Reading it as the start and adding wall time
		// would place the whole node one full run duration into the future.
		It("derives the start backwards from wall time, because the metrics row is stamped at completion", func() {
			build, err := defaultTeam.CreateOneOffBuild()
			Expect(err).ToNot(HaveOccurred())
			run := newRun(build)

			_, _, err = db.NewAgentRunMetricsFactory(dbConn).UpsertReturningInserted(&schema.RunMetrics{
				BuildID: build.ID(), PlanID: "1/2", StepName: "implement",
				Status: "ok", CostUSD: 1.70, WallTimeSeconds: 392,
			})
			Expect(err).ToNot(HaveOccurred())

			var completedAt time.Time
			Expect(dbConn.QueryRow(
				`SELECT created_at FROM agent_run_metrics WHERE build_id = $1 AND plan_id = '1/2'`,
				build.ID()).Scan(&completedAt)).To(Succeed())

			evidence, err := factory.EvidenceForRun(ctx, run)
			Expect(err).ToNot(HaveOccurred())
			Expect(evidence.AttemptMetrics).To(HaveLen(1))
			Expect(evidence.AttemptMetrics[0].UpdatedAt).To(BeTemporally("==", completedAt))
			Expect(evidence.AttemptMetrics[0].UpdatedAt.Sub(evidence.AttemptMetrics[0].CreatedAt)).
				To(Equal(392 * time.Second))
		})

		// Degraded ingestion writes wall_time_seconds = 0. Collapsing to a
		// point reads as "unknown duration"; the alternative — inventing a
		// span — would be a confidently wrong number on the permanent record.
		It("collapses the span to a point when wall time was never recorded", func() {
			build, err := defaultTeam.CreateOneOffBuild()
			Expect(err).ToNot(HaveOccurred())
			run := newRun(build)

			_, _, err = db.NewAgentRunMetricsFactory(dbConn).UpsertReturningInserted(&schema.RunMetrics{
				BuildID: build.ID(), PlanID: "1/2", StepName: "implement",
				Status: "ok", CostUSD: 1.70, WallTimeSeconds: 0,
			})
			Expect(err).ToNot(HaveOccurred())

			evidence, err := factory.EvidenceForRun(ctx, run)
			Expect(err).ToNot(HaveOccurred())
			Expect(evidence.AttemptMetrics).To(HaveLen(1))
			Expect(evidence.AttemptMetrics[0].CreatedAt).
				To(BeTemporally("==", evidence.AttemptMetrics[0].UpdatedAt))
		})
	})

	Describe("waits and publications", func() {
		It("reads the run's waits with the timeout policy the derivation needs", func() {
			build, err := defaultTeam.CreateOneOffBuild()
			Expect(err).ToNot(HaveOccurred())
			run := newRun(build)
			questionID := createEvidenceSnapshot("question")
			var waitID int64
			Expect(dbConn.QueryRow(`
				INSERT INTO agent_workflow_waits
					(team_id, workflow_run_id, build_id, build_id_evidence, plan_id, attempt,
					 output_name, question_name, question_snapshot_id, expected_type_name,
					 expected_type_version, deadline, timeout_policy, status)
				VALUES ($1, $2, $3, $3, '1/2', '1', 'approval', 'approve?', $4,
				        'human-answer', 1, now() + interval '1 hour', 'fail', 'waiting')
				RETURNING id
			`, defaultTeam.ID(), int64(run.ID), build.ID(), questionID).Scan(&waitID)).To(Succeed())

			evidence, err := factory.EvidenceForRun(ctx, run)
			Expect(err).ToNot(HaveOccurred())
			Expect(evidence.Waits).To(HaveLen(1))
			Expect(evidence.Waits[0].ID).To(Equal(waitID))
			Expect(evidence.Waits[0].PlanID).To(Equal("1/2"))
			Expect(evidence.Waits[0].Status).To(Equal("waiting"))
			Expect(evidence.Waits[0].TimeoutPolicy).To(Equal("fail"))
			Expect(evidence.Waits[0].ResolvedAt).To(BeNil())
		})

		// A publication recorded before the projection existed carries no plan
		// identity, so it can be joined to no node. Returning it would let it
		// attach to whichever publish node happened to be looked up first.
		It("skips publications that carry no plan identity", func() {
			build, err := defaultTeam.CreateOneOffBuild()
			Expect(err).ToNot(HaveOccurred())
			run := newRun(build)
			publicationID := createEvidencePublication(int64(run.ID), int64(build.ID()), "")
			withPlan := createEvidencePublication(int64(run.ID), int64(build.ID()), "1/9")

			evidence, err := factory.EvidenceForRun(ctx, run)
			Expect(err).ToNot(HaveOccurred())
			Expect(evidence.Publications).To(HaveLen(1))
			Expect(evidence.Publications[0].ID).To(Equal(withPlan))
			Expect(evidence.Publications[0].PlanID).To(Equal("1/9"))
			Expect(evidence.Publications[0].ID).ToNot(Equal(publicationID))
		})
	})
})

func createEvidenceSnapshot(typeName string) int64 {
	var id int64
	digest := fmt.Sprintf("sha256:%064d", time.Now().UnixNano()%1e9)
	Expect(dbConn.QueryRow(`
		INSERT INTO agent_snapshots
			(team_id, type_name, type_version, digest, byte_size, file_count,
			 representation, content_state)
		VALUES ($1, $2, 1, $3, 1, 1, 'application/x-tar', 'available')
		RETURNING id
	`, defaultTeam.ID(), typeName, digest).Scan(&id)).To(Succeed())
	return id
}

// createEvidencePublication records a publication through the REAL writer, so
// this fixture cannot drift away from what production actually stores. Each
// call publishes to a distinct destination, which is a distinct operation and
// therefore a distinct occurrence.
func createEvidencePublication(workflowRunID, buildID int64, planID string) int64 {
	inputID := createEvidenceSnapshot("repository-change")
	_, err := dbConn.Exec(`
		INSERT INTO agent_workflow_run_snapshots
			(workflow_run_id, direction, port_name, snapshot_id, promoted_at)
		VALUES ($1, 'output', $2, $3, now())
	`, workflowRunID, fmt.Sprintf("change-%d", inputID), inputID)
	Expect(err).ToNot(HaveOccurred())

	var digest string
	Expect(dbConn.QueryRow(
		`SELECT digest FROM agent_snapshots WHERE id = $1`, inputID,
	).Scan(&digest)).To(Succeed())

	// The store authorizes a publication against the build's own creator, so
	// the fixture reads it rather than asserting one.
	var createdBy sql.NullString
	Expect(dbConn.QueryRow(
		`SELECT created_by FROM builds WHERE id = $1`, buildID,
	).Scan(&createdBy)).To(Succeed())
	actor := strings.TrimSpace(createdBy.String)
	if actor == "" {
		actor = "concourse"
	}

	publication, execute, err := db.NewAgentPublicationsFactory(dbConn).Acquire(
		context.Background(),
		publisher.Request{
			Publisher: publisher.GitPublisher,
			Input: snapshot.SnapshotRef{
				ID: snapshot.SnapshotID(inputID), Type: "repository-change/v1",
				Digest: snapshot.Digest(digest),
			},
			Destination: fmt.Sprintf("github.example/team/repo-%d", inputID),
			Mode:        publisher.ModeBranch,
			Parameters: map[string]string{
				"source_branch": "agent/change", "target_branch": "main",
			},
			ApprovalPolicyVersion: "engineering/v2",
			Authority: publisher.Authority{
				TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(),
				BuildID: buildID, PlanID: planID, Actor: actor,
			},
		},
		time.Minute,
	)
	Expect(err).ToNot(HaveOccurred())
	Expect(execute).To(BeTrue())

	var occurrenceID int64
	Expect(dbConn.QueryRow(
		`SELECT id FROM agent_publication_occurrences WHERE publication_id = $1`,
		publication.ID,
	).Scan(&occurrenceID)).To(Succeed())
	return occurrenceID
}

// The Phase B exit: a real workflow, a real plan, a real build with real
// events, and a real reconciler finalizing a real run — producing the durable
// projection that outlives every one of those sources.
var _ = Describe("NodeOccurrence freeze end to end", func() {
	var (
		workflows  db.AgentWorkflowsFactory
		definition *workflow.Definition
		actualPlan []byte
		name       string
	)

	BeforeEach(func() {
		workflows = db.NewAgentWorkflowsFactory(dbConn)
		manifest := seedManifest("small-fix-v3")
		// The store refuses an import whose name disagrees with the compiled
		// definition, so the workflow keeps its authored identity.
		compiled, err := workflow.CompileDefinition(manifest)
		Expect(err).ToNot(HaveOccurred())
		name = compiled.Name

		definition, err = workflows.ImportManifest(name, manifest, "alice")
		Expect(err).ToNot(HaveOccurred())
		Expect(definition.Version).To(Equal(1))

		// A SECOND, later revision of the same workflow. Nothing about it may
		// reach the first revision's projection: a run's history describes the
		// revision that actually executed, not whatever is newest when it is
		// inspected.
		later := seedManifest("small-fix-v3")
		for file, contents := range later {
			if strings.HasSuffix(file, "workflow.yaml") {
				later[file] = contents + "\n# a later revision\n"
			}
		}
		newer, err := workflows.ImportManifest(name, later, "alice")
		Expect(err).ToNot(HaveOccurred())
		Expect(newer.Version).To(Equal(2), "the second import must be a distinct revision")

		actualPlan = planSeedDefinition(*definition)
	})

	It("projects one row per execution node, including the deterministic task no other record survives", func() {
		build, err := defaultTeam.CreateOneOffBuild()
		Expect(err).ToNot(HaveOccurred())

		taskPlanID := planIDForNode(actualPlan, "dev-validation-repository-gates")
		Expect(build.SaveEvent(event.FinishTask{
			ExitStatus: 0, Time: time.Now().Unix(),
			Origin: event.Origin{ID: event.OriginID(taskPlanID)},
		})).To(Succeed())
		Expect(build.Finish(db.BuildStatusFailed)).To(Succeed())

		runID := insertReconcilableRun(name, definition.ID, build.ID(), actualPlan)

		// A durable wait, so the projection carries evidence from more than
		// one source in the same freeze.
		questionID := createEvidenceSnapshot("question")
		awaitPlanID := planIDForNode(actualPlan, "approval")
		Expect(dbConn.Exec(`
			INSERT INTO agent_workflow_waits
				(team_id, workflow_run_id, build_id, build_id_evidence, plan_id, attempt,
				 output_name, question_name, question_snapshot_id, expected_type_name,
				 expected_type_version, deadline, timeout_policy, status)
			VALUES ($1, $2, $3, $3, $4, '1', 'approval', 'approve?', $5,
			        'human-answer', 1, now() + interval '1 hour', 'fail', 'waiting')
		`, defaultTeam.ID(), int64(runID), build.ID(), awaitPlanID, questionID)).ToNot(BeNil())

		occurrences := db.NewAgentWorkflowRunNodeOccurrencesFactory(dbConn)
		freezer, err := occurrence.NewFreezer(
			db.NewAgentWorkflowRunEvidenceFactory(dbConn), workflows,
			db.NewAgentNodesFactory(dbConn), occurrences,
		)
		Expect(err).ToNot(HaveOccurred())

		reconciler, err := workflowrun.NewReconciler(
			db.NewAgentWorkflowRunsFactory(dbConn),
			lagertest.NewTestLogger("freeze-e2e"),
			15*time.Minute, time.Minute,
			workflowrun.WithNodeOccurrenceFreezer(freezer),
			// The wait canceler is part of the finalization, not an optional
			// extra: the freeze reads wait rows, and a wait cancelled AFTER the
			// freeze leaves 'waiting' in immutable history forever.
			workflowrun.WithWaitCanceler(db.NewAgentWorkflowWaitsFactory(dbConn)),
		)
		Expect(err).ToNot(HaveOccurred())
		Expect(reconciler.Run(context.Background())).To(Succeed())

		// The run really terminalized.
		var status string
		Expect(dbConn.QueryRow(
			`SELECT status FROM agent_workflow_runs WHERE id = $1`, int64(runID),
		).Scan(&status)).To(Succeed())
		Expect(status).To(Equal("failed"))

		stored, err := occurrences.ForRun(context.Background(), int64(runID))
		Expect(err).ToNot(HaveOccurred())

		byNode := map[string]db.AgentWorkflowRunNodeOccurrence{}
		for _, row := range stored {
			Expect(byNode).ToNot(HaveKey(row.NodeID), "one row per execution node")
			byNode[row.NodeID] = row
		}
		Expect(byNode).To(HaveLen(len(executionNodeIDs(*definition))))
		for _, nodeID := range executionNodeIDs(*definition) {
			Expect(byNode).To(HaveKey(nodeID))
		}

		// THE case that exists for no other reason: after build log retention
		// and template retirement reclaim this build, this row is the only
		// record that the task ran at all.
		task := byNode["dev-validation-repository-gates"]
		Expect(task.NodeKind).To(Equal("task"))
		Expect(task.PlanID).To(Equal(taskPlanID))
		Expect(task.Status).To(Equal("succeeded"))

		// The wait outlived the build that would have answered it. The
		// finalization cancels it BEFORE the freeze, so the durable row and the
		// immutable projection agree that it ended rather than one of them
		// claiming work is still in flight on a finished run.
		var waitStatus string
		Expect(dbConn.QueryRow(
			`SELECT status FROM agent_workflow_waits WHERE workflow_run_id = $1`, int64(runID),
		).Scan(&waitStatus)).To(Succeed())
		Expect(waitStatus).To(Equal("cancelled"))

		await := byNode["approval"]
		Expect(await.NodeKind).To(Equal("await"))
		Expect(await.Status).To(Equal("aborted"))
		Expect(await.WaitID).ToNot(BeNil())

		// Every row describes the revision that executed, not the live one.
		for nodeID, row := range byNode {
			Expect(row.WorkflowVersion).To(Equal(1), "node %s", nodeID)
			Expect(row.WorkflowName).To(Equal(name))
			Expect(row.TeamID).To(Equal(defaultTeam.ID()))
			Expect(row.FrozenAt).ToNot(BeZero())
		}
	})
})

// insertReconcilableRun writes a running workflow run with a copied terminal
// execution outcome and complete plan provenance — the exact state the
// reconciler finalizes on its next pass.
func insertReconcilableRun(name string, definitionID, buildID int, actualPlan []byte) snapshot.WorkflowRunID {
	unique := time.Now().UnixNano()
	var runID int64
	Expect(dbConn.QueryRow(`
		INSERT INTO agent_workflow_runs
			(team_id, team_name, workflow_definition_id, workflow_name, workflow_version,
			 schema_version, signature_version, definition_content_hash, idempotency_key,
			 parameterized_config, parameterized_config_hash, origin_kind, origin_reference,
			 created_by, status, execution_status, planned_build_id,
			 actual_plan, actual_plan_hash, resolved_dependencies, reconcile_after)
		VALUES ($1, $2, $3, $4, 1, 3, 1, $5, $6, '{}', $7, 'manual', '', 'alice',
		        'running', 'failed', $8, $9, $10, '{"nodes":[]}'::jsonb, now() - interval '1 hour')
		RETURNING id
	`, defaultTeam.ID(), defaultTeam.Name(), definitionID, name,
		strings.Repeat("c", 64), fmt.Sprintf("freeze-e2e-%d", unique), strings.Repeat("f", 64),
		buildID, actualPlan, strings.Repeat("9", 64),
	).Scan(&runID)).To(Succeed())
	return snapshot.WorkflowRunID(runID)
}
