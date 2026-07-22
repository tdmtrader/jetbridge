package db_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/snapshotfakes"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("AgentSnapshotsFactory", func() {
	var (
		ctx     context.Context
		factory db.AgentSnapshotsFactory
		lease   *snapshotfakes.FakeDigestLease
	)

	digest := func(hexDigit string) snapshot.Digest {
		return snapshot.Digest("sha256:" + strings.Repeat(hexDigit, 64))
	}

	newBuild := func(teamID int) int {
		var id int
		err := dbConn.QueryRow(`
			INSERT INTO builds (name, status, team_id)
			VALUES ($1, 'pending', $2)
			RETURNING id
		`, fmt.Sprintf("snapshot-build-%d", time.Now().UnixNano()), teamID).Scan(&id)
		Expect(err).NotTo(HaveOccurred())
		return id
	}

	stage := func(value snapshot.Digest, teamID int, attempt string) snapshot.StagedUpload {
		staged, err := factory.StageUpload(ctx, lease, snapshot.StageUploadRequest{
			Digest: value, TeamID: teamID, Attempt: attempt,
			LeaseExpiresAt: time.Now().Add(time.Hour),
		})
		Expect(err).NotTo(HaveOccurred())
		return staged
	}

	seal := func(
		buildID int,
		attempt string,
		inputs map[string]snapshot.SnapshotRef,
		inputOrder []string,
		outputs []snapshot.SealCommitOutput,
	) map[string]snapshot.SealedOutput {
		ports := make([]snapshot.Port, len(outputs))
		for i := range outputs {
			ports[i] = outputs[i].Port
		}
		sealed, err := factory.CommitSealBatch(ctx, lease, snapshot.SealCommit{
			Context: snapshot.SealCommitContext{
				BuildID: buildID, TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(),
				CreatedBy: "alice", PlanID: "plan-1", Attempt: attempt,
				StepKind: "task", StepName: "produce", Inputs: inputs,
				InputOrder: inputOrder, ExpectedOutputs: ports,
			},
			Outputs: outputs,
		})
		Expect(err).NotTo(HaveOccurred())
		return sealed
	}

	output := func(
		clientKey string,
		port string,
		typeRef snapshot.TypeRef,
		value snapshot.Digest,
		staged snapshot.StagedUpload,
	) snapshot.SealCommitOutput {
		return snapshot.SealCommitOutput{
			ClientKey: clientKey,
			Port:      snapshot.Port{Name: port, Type: typeRef},
			Digest:    value, ByteSize: 128, FileCount: 2,
			Representation:    "application/vnd.jetbridge.snapshot.tar.v1",
			IntrinsicMetadata: json.RawMessage(`{"tree":"abc"}`),
			StagedUploadID:    staged.ID,
			Locations: []snapshot.Location{{
				Digest: value, Driver: "jetbridge-daemon-v1", Key: "snapshots/" + value.String(), Node: "worker-1",
			}},
			Retention: []snapshot.RetentionSpec{{
				Class: snapshot.RetentionClassBinding, Actor: "build", Reason: "build output",
			}},
			SourceMetadata: json.RawMessage(`{"adapter":"test"}`),
		}
	}

	BeforeEach(func() {
		ctx = context.Background()
		factory = db.NewAgentSnapshotsFactory(dbConn)
		lease = new(snapshotfakes.FakeDigestLease)
		lease.CoversReturns(true)
	})

	It("installs the complete durable snapshot schema", func() {
		tables := []string{
			"agent_snapshots",
			"agent_snapshot_locations",
			"agent_snapshot_staged_uploads",
			"agent_workflow_runs",
			"agent_snapshot_productions",
			"agent_snapshot_lineage",
			"agent_snapshot_grants",
			"agent_snapshot_retention_claims",
			"agent_workflow_run_snapshots",
		}

		for _, table := range tables {
			var name sql.NullString
			err := dbConn.QueryRow(`SELECT to_regclass($1)`, table).Scan(&name)
			Expect(err).NotTo(HaveOccurred())
			Expect(name.Valid).To(BeTrue(), "missing table %s", table)
			Expect(name.String).To(Equal(table))
		}
	})

	It("requires lease coverage before staging and records explicit expiry", func() {
		value := digest("1")
		lease.CoversReturns(false)
		_, err := factory.StageUpload(ctx, lease, snapshot.StageUploadRequest{
			Digest: value, TeamID: defaultTeam.ID(), Attempt: "attempt-1",
			LeaseExpiresAt: time.Now().Add(time.Hour),
		})
		Expect(err).To(MatchError(ContainSubstring("does not cover")))

		lease.CoversReturns(true)
		expiry := time.Now().Add(time.Hour).Round(time.Microsecond)
		staged, err := factory.StageUpload(ctx, lease, snapshot.StageUploadRequest{
			Digest: value, TeamID: defaultTeam.ID(), Attempt: "attempt-1",
			LeaseExpiresAt: expiry,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(staged.ID).To(BeNumerically(">", 0))
		Expect(staged.LeaseExpiresAt).To(BeTemporally("==", expiry))
		Expect(staged.CreatedAt).ToNot(BeZero())
	})

	It("rejects expired stages without consuming them and supports leased cleanup", func() {
		value := digest("1")
		staged := stage(value, defaultTeam.ID(), "expired")
		_, err := dbConn.Exec(`
			UPDATE agent_snapshot_staged_uploads
			SET created_at = now() - interval '2 hours',
			    lease_expires_at = now() - interval '1 hour'
			WHERE id = $1
		`, staged.ID)
		Expect(err).NotTo(HaveOccurred())

		candidate := output("expired", "result", "opaque/v1", value, staged)
		_, err = factory.CommitSealBatch(ctx, lease, snapshot.SealCommit{
			Context: snapshot.SealCommitContext{
				BuildID: newBuild(defaultTeam.ID()), TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(),
				CreatedBy: "alice", PlanID: "plan-1", Attempt: "expired", StepKind: "task", StepName: "produce",
				ExpectedOutputs: []snapshot.Port{candidate.Port},
			},
			Outputs: []snapshot.SealCommitOutput{candidate},
		})
		Expect(err).To(MatchError(ContainSubstring("expired")))

		var stages, manifests int
		Expect(dbConn.QueryRow(`SELECT count(*) FROM agent_snapshot_staged_uploads WHERE id = $1`, staged.ID).Scan(&stages)).To(Succeed())
		Expect(dbConn.QueryRow(`SELECT count(*) FROM agent_snapshots WHERE digest = $1`, value).Scan(&manifests)).To(Succeed())
		Expect(stages).To(Equal(1))
		Expect(manifests).To(Equal(0))

		Expect(factory.RemoveStagedUploads(ctx, lease, value, []int64{staged.ID})).To(Succeed())
		Expect(dbConn.QueryRow(`SELECT count(*) FROM agent_snapshot_staged_uploads WHERE id = $1`, staged.ID).Scan(&stages)).To(Succeed())
		Expect(stages).To(Equal(0))
	})

	It("atomically seals manifests, occurrences, locations, grants, claims, and ordered lineage", func() {
		baseDigest := digest("2")
		baseStage := stage(baseDigest, defaultTeam.ID(), "base")
		base := seal(newBuild(defaultTeam.ID()), "base", nil, nil, []snapshot.SealCommitOutput{
			output("base", "base", "repository/v1", baseDigest, baseStage),
		})["base"].Snapshot

		secondDigest := digest("3")
		secondStage := stage(secondDigest, defaultTeam.ID(), "second")
		second := seal(newBuild(defaultTeam.ID()), "second", nil, nil, []snapshot.SealCommitOutput{
			output("second", "second", "log-bundle/v1", secondDigest, secondStage),
		})["second"].Snapshot

		resultDigest := digest("4")
		resultStage := stage(resultDigest, defaultTeam.ID(), "result")
		resultOutput := output("review-result", "review", "review/v1", resultDigest, resultStage)
		resultOutput.Locations = append(resultOutput.Locations, snapshot.Location{
			Digest: resultDigest, Driver: "jetbridge-daemon-v1", Key: "replicas/" + resultDigest.String(), Node: "worker-2",
		})
		sealed := seal(
			newBuild(defaultTeam.ID()),
			"result",
			map[string]snapshot.SnapshotRef{"before": base, "logs": second},
			[]string{"logs", "before"},
			[]snapshot.SealCommitOutput{resultOutput},
		)

		result := sealed["review-result"]
		Expect(result.Snapshot.Type).To(Equal(snapshot.TypeRef("review/v1")))
		Expect(result.Snapshot.Digest).To(Equal(resultDigest))

		stored, found, err := factory.GetAuthorized(ctx, defaultTeam.ID(), result.Snapshot.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(stored.IntrinsicMetadata).To(MatchJSON(`{"tree":"abc"}`))

		locations, err := factory.LocationsForDigest(ctx, resultDigest)
		Expect(err).NotTo(HaveOccurred())
		Expect(locations).To(ConsistOf(resultOutput.Locations))

		var source []byte
		var ports string
		err = dbConn.QueryRow(`
			SELECT p.source_metadata,
			       array_agg(l.input_port ORDER BY l.position)::text
			FROM agent_snapshot_productions p
			JOIN agent_snapshot_lineage l ON l.production_id = p.id
			WHERE p.snapshot_id = $1
			GROUP BY p.id
		`, int64(result.Snapshot.ID)).Scan(&source, &ports)
		Expect(err).NotTo(HaveOccurred())
		Expect(source).To(MatchJSON(`{"adapter":"test"}`))
		Expect(ports).To(Equal("{logs,before}"))

		var stages int
		Expect(dbConn.QueryRow(`SELECT count(*) FROM agent_snapshot_staged_uploads WHERE id = $1`, resultStage.ID).Scan(&stages)).To(Succeed())
		Expect(stages).To(Equal(0))
	})

	It("atomically binds workflow outputs and preserves production history after build deletion", func() {
		inputDigest := digest("1")
		inputStage := stage(inputDigest, defaultTeam.ID(), "input")
		input := seal(newBuild(defaultTeam.ID()), "input", nil, nil, []snapshot.SealCommitOutput{
			output("input", "source", "repository/v1", inputDigest, inputStage),
		})["input"].Snapshot

		definitionName := fmt.Sprintf("snapshot-output-binding-%d", time.Now().UnixNano())
		definitionHash := strings.Repeat("a", 64)
		var definitionID int
		Expect(dbConn.QueryRow(`
			INSERT INTO agent_workflow_definitions
				(name, version, content_hash, definition, created_by)
			VALUES ($1, 1, $2, 'schema_version: 3', 'alice')
			RETURNING id
		`, definitionName, definitionHash).Scan(&definitionID)).To(Succeed())
		runFactory := db.NewAgentWorkflowRunsFactory(dbConn)
		run, created, err := runFactory.CreateWithInputs(ctx, db.AgentWorkflowRunCreateRequest{
			TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(),
			WorkflowDefinitionID: definitionID, WorkflowName: definitionName, WorkflowVersion: 1,
			SchemaVersion: 3, SignatureVersion: 1, DefinitionContentHash: definitionHash,
			IdempotencyKey: "snapshot-output-binding", ParameterizedConfig: json.RawMessage(`{"jobs":[]}`),
			ParameterizedConfigHash: strings.Repeat("b", 64), OriginKind: "test", OriginReference: "binding",
			CreatedBy: "alice", Status: db.AgentWorkflowRunStatusAdmitting,
			Inputs: map[string]snapshot.SnapshotRef{"source": input},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())

		var templateID, instanceID, pipelineRunID int
		pipelineSuffix := time.Now().UnixNano()
		Expect(dbConn.QueryRow(`INSERT INTO pipelines (name, team_id, secondary_ordering) VALUES ($1, $2, 1) RETURNING id`, fmt.Sprintf("snapshot-template-%d", pipelineSuffix), defaultTeam.ID()).Scan(&templateID)).To(Succeed())
		Expect(dbConn.QueryRow(`INSERT INTO pipelines (name, team_id, secondary_ordering) VALUES ($1, $2, 1) RETURNING id`, fmt.Sprintf("snapshot-instance-%d", pipelineSuffix), defaultTeam.ID()).Scan(&instanceID)).To(Succeed())
		Expect(dbConn.QueryRow(`INSERT INTO pipeline_runs (template_pipeline_id, instance_pipeline_id, number) VALUES ($1, $2, 1) RETURNING id`, templateID, instanceID).Scan(&pipelineRunID)).To(Succeed())
		Expect(runFactory.LinkExecution(ctx, run.ID, db.AgentWorkflowRunExecutionLink{
			PipelineRunID: pipelineRunID, TemplatePipelineID: templateID, InstancePipelineID: instanceID,
			ConcreteConfig: json.RawMessage(`{"instance":true}`), ConcreteConfigHash: strings.Repeat("c", 64),
		})).To(Succeed())

		var producerBuildID int
		Expect(dbConn.QueryRow(`INSERT INTO builds (name, status, team_id, pipeline_id) VALUES ($1, 'pending', $2, $3) RETURNING id`, fmt.Sprintf("snapshot-output-%d", pipelineSuffix), defaultTeam.ID(), instanceID).Scan(&producerBuildID)).To(Succeed())
		Expect(runFactory.RecordPlan(ctx, run.ID, db.AgentWorkflowRunPlan{
			BuildID: producerBuildID, ActualPlan: json.RawMessage(`{"task":"review"}`),
			ActualPlanHash: strings.Repeat("d", 64), ResolvedDependencies: json.RawMessage(`{}`),
		})).To(Succeed())

		resultDigest := digest("2")
		resultStage := stage(resultDigest, defaultTeam.ID(), "workflow-output")
		result := output("result", "result", "review/v1", resultDigest, resultStage)
		result.Retention = append(result.Retention, snapshot.RetentionSpec{
			Class: snapshot.RetentionClassWorkflow, Actor: "workflow-output", Reason: "durable workflow output",
		})
		runID := run.ID
		wrongBuildID := newBuild(defaultTeam.ID())
		_, err = factory.CommitSealBatch(ctx, lease, snapshot.SealCommit{
			Context: snapshot.SealCommitContext{
				BuildID: wrongBuildID, TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(),
				CreatedBy: "alice", PlanID: "plan-output", Attempt: "workflow-output",
				StepKind: "agent", StepName: "review", WorkflowDefinitionID: &definitionID, WorkflowRunID: &runID,
				InputOrder: []string{"source"}, Inputs: map[string]snapshot.SnapshotRef{"source": input},
				ExpectedOutputs: []snapshot.Port{result.Port},
			},
			Outputs: []snapshot.SealCommitOutput{result},
		})
		Expect(err).To(MatchError(ContainSubstring("planned build")))
		var retainedStage int
		Expect(dbConn.QueryRow(`SELECT count(*) FROM agent_snapshot_staged_uploads WHERE id = $1`, resultStage.ID).Scan(&retainedStage)).To(Succeed())
		Expect(retainedStage).To(Equal(1), "wrong-build rejection must roll the seal transaction back")

		sealed, err := factory.CommitSealBatch(ctx, lease, snapshot.SealCommit{
			Context: snapshot.SealCommitContext{
				BuildID: producerBuildID, TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(),
				CreatedBy: "alice", PlanID: "plan-output", Attempt: "workflow-output",
				StepKind: "agent", StepName: "review", WorkflowDefinitionID: &definitionID, WorkflowRunID: &runID,
				InputOrder: []string{"source"}, Inputs: map[string]snapshot.SnapshotRef{"source": input},
				ExpectedOutputs: []snapshot.Port{result.Port},
			},
			Outputs: []snapshot.SealCommitOutput{result},
		})
		Expect(err).NotTo(HaveOccurred())

		bindings, err := runFactory.Snapshots(ctx, run.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(bindings).To(ConsistOf(
			db.AgentWorkflowRunSnapshotBinding{
				WorkflowRunID: run.ID, Direction: db.AgentWorkflowRunSnapshotInput,
				PortName: "source", Snapshot: input,
			},
			db.AgentWorkflowRunSnapshotBinding{
				WorkflowRunID: run.ID, Direction: db.AgentWorkflowRunSnapshotOutput,
				PortName: "result", Snapshot: sealed["result"].Snapshot,
			},
		))

		_, err = dbConn.Exec(`DELETE FROM builds WHERE id = $1`, producerBuildID)
		Expect(err).NotTo(HaveOccurred())
		var productions int
		Expect(dbConn.QueryRow(`
			SELECT count(*) FROM agent_snapshot_productions
			WHERE build_id = $1 AND workflow_run_id = $2
		`, producerBuildID, int64(run.ID)).Scan(&productions)).To(Succeed())
		Expect(productions).To(Equal(1))
	})

	It("deduplicates semantic manifests while preserving distinct productions and semantic siblings", func() {
		value := digest("5")
		firstStage := stage(value, defaultTeam.ID(), "one")
		first := seal(newBuild(defaultTeam.ID()), "one", nil, nil, []snapshot.SealCommitOutput{
			output("one", "result", "opaque/v1", value, firstStage),
		})["one"].Snapshot

		secondStage := stage(value, defaultTeam.ID(), "two")
		second := seal(newBuild(defaultTeam.ID()), "two", nil, nil, []snapshot.SealCommitOutput{
			output("two", "result", "opaque/v1", value, secondStage),
		})["two"].Snapshot
		Expect(second.ID).To(Equal(first.ID))

		siblingStage := stage(value, defaultTeam.ID(), "three")
		sibling := seal(newBuild(defaultTeam.ID()), "three", nil, nil, []snapshot.SealCommitOutput{
			output("three", "result", "measurements/v1", value, siblingStage),
		})["three"].Snapshot
		Expect(sibling.ID).NotTo(Equal(first.ID))

		var manifests, productions, locations int
		Expect(dbConn.QueryRow(`SELECT count(*) FROM agent_snapshots WHERE digest = $1`, value).Scan(&manifests)).To(Succeed())
		Expect(dbConn.QueryRow(`SELECT count(*) FROM agent_snapshot_productions p JOIN agent_snapshots s ON s.id = p.snapshot_id WHERE s.digest = $1`, value).Scan(&productions)).To(Succeed())
		Expect(dbConn.QueryRow(`SELECT count(*) FROM agent_snapshot_locations WHERE digest = $1`, value).Scan(&locations)).To(Succeed())
		Expect(manifests).To(Equal(2))
		Expect(productions).To(Equal(3))
		Expect(locations).To(Equal(1))
	})

	It("rejects immutable physical and intrinsic conflicts without consuming stages", func() {
		value := digest("6")
		firstStage := stage(value, defaultTeam.ID(), "one")
		seal(newBuild(defaultTeam.ID()), "one", nil, nil, []snapshot.SealCommitOutput{
			output("one", "result", "opaque/v1", value, firstStage),
		})

		conflictStage := stage(value, defaultTeam.ID(), "two")
		conflict := output("two", "result", "opaque/v1", value, conflictStage)
		conflict.ByteSize++
		_, err := factory.CommitSealBatch(ctx, lease, snapshot.SealCommit{
			Context: snapshot.SealCommitContext{
				BuildID: newBuild(defaultTeam.ID()), TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(),
				CreatedBy: "alice", PlanID: "plan-1", Attempt: "two", StepKind: "task", StepName: "produce",
				ExpectedOutputs: []snapshot.Port{conflict.Port},
			},
			Outputs: []snapshot.SealCommitOutput{conflict},
		})
		Expect(err).To(MatchError(ContainSubstring("conflicts")))

		var stages int
		Expect(dbConn.QueryRow(`SELECT count(*) FROM agent_snapshot_staged_uploads WHERE id = $1`, conflictStage.ID).Scan(&stages)).To(Succeed())
		Expect(stages).To(Equal(1))
	})

	It("rolls back the whole output batch when any input reference is invalid", func() {
		firstDigest, secondDigest := digest("7"), digest("8")
		firstStage := stage(firstDigest, defaultTeam.ID(), "batch")
		secondStage := stage(secondDigest, defaultTeam.ID(), "batch")
		outputs := []snapshot.SealCommitOutput{
			output("first", "first", "opaque/v1", firstDigest, firstStage),
			output("second", "second", "opaque/v1", secondDigest, secondStage),
		}

		_, err := factory.CommitSealBatch(ctx, lease, snapshot.SealCommit{
			Context: snapshot.SealCommitContext{
				BuildID: newBuild(defaultTeam.ID()), TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(),
				CreatedBy: "alice", PlanID: "plan-1", Attempt: "batch", StepKind: "task", StepName: "produce",
				Inputs: map[string]snapshot.SnapshotRef{"missing": {
					ID: 999999, Type: "opaque/v1", Digest: digest("9"),
				}}, InputOrder: []string{"missing"},
				ExpectedOutputs: []snapshot.Port{outputs[0].Port, outputs[1].Port},
			},
			Outputs: outputs,
		})
		Expect(err).To(HaveOccurred())

		var manifests, stages int
		Expect(dbConn.QueryRow(`SELECT count(*) FROM agent_snapshots WHERE digest IN ($1, $2)`, firstDigest, secondDigest).Scan(&manifests)).To(Succeed())
		Expect(dbConn.QueryRow(`SELECT count(*) FROM agent_snapshot_staged_uploads WHERE id IN ($1, $2)`, firstStage.ID, secondStage.ID).Scan(&stages)).To(Succeed())
		Expect(manifests).To(Equal(0))
		Expect(stages).To(Equal(2))
	})

	It("rolls back earlier outputs when a later production invocation conflicts", func() {
		buildID := newBuild(defaultTeam.ID())
		seedDigest := digest("9")
		seedStage := stage(seedDigest, defaultTeam.ID(), "batch")
		seal(buildID, "batch", nil, nil, []snapshot.SealCommitOutput{
			output("seed", "second", "opaque/v1", seedDigest, seedStage),
		})

		firstDigest, conflictingDigest := digest("a"), digest("b")
		firstStage := stage(firstDigest, defaultTeam.ID(), "batch")
		conflictingStage := stage(conflictingDigest, defaultTeam.ID(), "batch")
		outputs := []snapshot.SealCommitOutput{
			output("first", "first", "opaque/v1", firstDigest, firstStage),
			output("second", "second", "opaque/v1", conflictingDigest, conflictingStage),
		}
		_, err := factory.CommitSealBatch(ctx, lease, snapshot.SealCommit{
			Context: snapshot.SealCommitContext{
				BuildID: buildID, TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(),
				CreatedBy: "alice", PlanID: "plan-1", Attempt: "batch", StepKind: "task", StepName: "produce",
				ExpectedOutputs: []snapshot.Port{outputs[0].Port, outputs[1].Port},
			},
			Outputs: outputs,
		})
		Expect(err).To(MatchError(ContainSubstring("production invocation conflicts")))

		var manifests, stages int
		Expect(dbConn.QueryRow(`SELECT count(*) FROM agent_snapshots WHERE digest IN ($1, $2)`, firstDigest, conflictingDigest).Scan(&manifests)).To(Succeed())
		Expect(dbConn.QueryRow(`SELECT count(*) FROM agent_snapshot_staged_uploads WHERE id IN ($1, $2)`, firstStage.ID, conflictingStage.ID).Scan(&stages)).To(Succeed())
		Expect(manifests).To(Equal(0))
		Expect(stages).To(Equal(2))
	})

	It("converges concurrent commits of one semantic digest and preserves both occurrences", func() {
		value := digest("c")
		stages := []snapshot.StagedUpload{
			stage(value, defaultTeam.ID(), "concurrent-1"),
			stage(value, defaultTeam.ID(), "concurrent-2"),
		}
		builds := []int{newBuild(defaultTeam.ID()), newBuild(defaultTeam.ID())}
		results := make(chan snapshot.SnapshotID, 2)
		errors := make(chan error, 2)
		var wg sync.WaitGroup
		for i := range 2 {
			wg.Add(1)
			go func(index int) {
				defer GinkgoRecover()
				defer wg.Done()
				candidate := output(fmt.Sprintf("result-%d", index), "result", "opaque/v1", value, stages[index])
				sealed, err := factory.CommitSealBatch(ctx, lease, snapshot.SealCommit{
					Context: snapshot.SealCommitContext{
						BuildID: builds[index], TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(),
						CreatedBy: "alice", PlanID: "plan-1", Attempt: fmt.Sprintf("concurrent-%d", index+1),
						StepKind: "task", StepName: "produce", ExpectedOutputs: []snapshot.Port{candidate.Port},
					},
					Outputs: []snapshot.SealCommitOutput{candidate},
				})
				if err != nil {
					errors <- err
					return
				}
				results <- sealed[candidate.ClientKey].Snapshot.ID
			}(i)
		}
		wg.Wait()
		close(errors)
		close(results)
		Expect(errors).To(BeEmpty())
		ids := []snapshot.SnapshotID{}
		for id := range results {
			ids = append(ids, id)
		}
		Expect(ids).To(HaveLen(2))
		Expect(ids[0]).To(Equal(ids[1]))

		var manifests, productions int
		Expect(dbConn.QueryRow(`SELECT count(*) FROM agent_snapshots WHERE digest = $1`, value).Scan(&manifests)).To(Succeed())
		Expect(dbConn.QueryRow(`SELECT count(*) FROM agent_snapshot_productions p JOIN agent_snapshots s ON s.id = p.snapshot_id WHERE s.digest = $1`, value).Scan(&productions)).To(Succeed())
		Expect(manifests).To(Equal(1))
		Expect(productions).To(Equal(2))
	})

	It("accepts exact production retries and rejects a different snapshot without consuming its stage", func() {
		buildID := newBuild(defaultTeam.ID())
		value := digest("d")
		firstStage := stage(value, defaultTeam.ID(), "retry")
		first := seal(buildID, "retry", nil, nil, []snapshot.SealCommitOutput{
			output("first", "result", "opaque/v1", value, firstStage),
		})["first"].Snapshot

		retryStage := stage(value, defaultTeam.ID(), "retry")
		retry := seal(buildID, "retry", nil, nil, []snapshot.SealCommitOutput{
			output("retry", "result", "opaque/v1", value, retryStage),
		})["retry"].Snapshot
		Expect(retry).To(Equal(first))

		otherDigest := digest("e")
		conflictStage := stage(otherDigest, defaultTeam.ID(), "retry")
		conflict := output("conflict", "result", "opaque/v1", otherDigest, conflictStage)
		_, err := factory.CommitSealBatch(ctx, lease, snapshot.SealCommit{
			Context: snapshot.SealCommitContext{
				BuildID: buildID, TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(),
				CreatedBy: "alice", PlanID: "plan-1", Attempt: "retry", StepKind: "task", StepName: "produce",
				ExpectedOutputs: []snapshot.Port{conflict.Port},
			},
			Outputs: []snapshot.SealCommitOutput{conflict},
		})
		Expect(err).To(MatchError(ContainSubstring("production invocation conflicts")))

		var stages, manifests, productions int
		Expect(dbConn.QueryRow(`SELECT count(*) FROM agent_snapshot_staged_uploads WHERE id = $1`, conflictStage.ID).Scan(&stages)).To(Succeed())
		Expect(dbConn.QueryRow(`SELECT count(*) FROM agent_snapshots WHERE digest = $1`, otherDigest).Scan(&manifests)).To(Succeed())
		Expect(dbConn.QueryRow(`SELECT count(*) FROM agent_snapshot_productions WHERE build_id = $1 AND plan_id = 'plan-1' AND attempt = 'retry' AND output_port = 'result'`, buildID).Scan(&productions)).To(Succeed())
		Expect(stages).To(Equal(1))
		Expect(manifests).To(Equal(0))
		Expect(productions).To(Equal(1))
	})

	It("rejects a production retry whose ordered input provenance changed", func() {
		inputDigest := digest("1")
		inputStage := stage(inputDigest, defaultTeam.ID(), "input")
		input := seal(newBuild(defaultTeam.ID()), "input", nil, nil, []snapshot.SealCommitOutput{
			output("input", "input", "opaque/v1", inputDigest, inputStage),
		})["input"].Snapshot

		buildID := newBuild(defaultTeam.ID())
		value := digest("2")
		firstStage := stage(value, defaultTeam.ID(), "lineage")
		seal(buildID, "lineage", map[string]snapshot.SnapshotRef{"source": input}, []string{"source"}, []snapshot.SealCommitOutput{
			output("first", "result", "opaque/v1", value, firstStage),
		})

		retryStage := stage(value, defaultTeam.ID(), "lineage")
		retry := output("retry", "result", "opaque/v1", value, retryStage)
		_, err := factory.CommitSealBatch(ctx, lease, snapshot.SealCommit{
			Context: snapshot.SealCommitContext{
				BuildID: buildID, TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(),
				CreatedBy: "alice", PlanID: "plan-1", Attempt: "lineage", StepKind: "task", StepName: "produce",
				ExpectedOutputs: []snapshot.Port{retry.Port},
			},
			Outputs: []snapshot.SealCommitOutput{retry},
		})
		Expect(err).To(MatchError(ContainSubstring("lineage conflicts")))

		var stages int
		Expect(dbConn.QueryRow(`SELECT count(*) FROM agent_snapshot_staged_uploads WHERE id = $1`, retryStage.ID).Scan(&stages)).To(Succeed())
		Expect(stages).To(Equal(1))
	})

	It("expires only after effective retention and locations are gone, preserves manifests, and revives every sibling", func() {
		value := digest("f")
		firstStage := stage(value, defaultTeam.ID(), "first")
		first := seal(newBuild(defaultTeam.ID()), "first", nil, nil, []snapshot.SealCommitOutput{
			output("first", "result", "opaque/v1", value, firstStage),
		})["first"].Snapshot
		secondStage := stage(value, defaultTeam.ID(), "second")
		second := seal(newBuild(defaultTeam.ID()), "second", nil, nil, []snapshot.SealCommitOutput{
			output("second", "result", "measurements/v1", value, secondStage),
		})["second"].Snapshot

		location := output("unused", "result", "opaque/v1", value, firstStage).Locations[0]
		state, err := factory.DigestState(ctx, lease, value, time.Now())
		Expect(err).NotTo(HaveOccurred())
		Expect(state.HasActiveRetention).To(BeTrue())
		Expect(state.Snapshots).To(HaveLen(2))
		expired, err := factory.MarkDigestExpired(ctx, lease, value, time.Now())
		Expect(err).NotTo(HaveOccurred())
		Expect(expired).To(BeFalse())

		_, err = factory.Pin(ctx, lease, defaultTeam.ID(), "release-manager", first, "hold for audit")
		Expect(err).NotTo(HaveOccurred())
		_, err = dbConn.Exec(`
			DELETE FROM agent_snapshot_retention_claims
			WHERE snapshot_id IN ($1, $2) AND class <> 'pin'
		`, int64(first.ID), int64(second.ID))
		Expect(err).NotTo(HaveOccurred())
		Expect(factory.RemoveLocation(ctx, lease, location)).To(Succeed())
		expired, err = factory.MarkDigestExpired(ctx, lease, value, time.Now())
		Expect(err).NotTo(HaveOccurred())
		Expect(expired).To(BeFalse(), "the independent pin must remain effective")

		Expect(factory.Unpin(ctx, lease, defaultTeam.ID(), "release-manager", first)).To(Succeed())
		expired, err = factory.MarkDigestExpired(ctx, lease, value, time.Now())
		Expect(err).NotTo(HaveOccurred())
		Expect(expired).To(BeTrue())

		var manifestCount, expiredCount, occurrenceCount, grantCount int
		Expect(dbConn.QueryRow(`SELECT count(*), count(*) FILTER (WHERE content_state = 'expired') FROM agent_snapshots WHERE digest = $1`, value).Scan(&manifestCount, &expiredCount)).To(Succeed())
		Expect(dbConn.QueryRow(`SELECT count(*) FROM agent_snapshot_productions p JOIN agent_snapshots s ON s.id = p.snapshot_id WHERE s.digest = $1`, value).Scan(&occurrenceCount)).To(Succeed())
		Expect(dbConn.QueryRow(`SELECT count(*) FROM agent_snapshot_grants g JOIN agent_snapshots s ON s.id = g.snapshot_id WHERE s.digest = $1`, value).Scan(&grantCount)).To(Succeed())
		Expect(manifestCount).To(Equal(2))
		Expect(expiredCount).To(Equal(2))
		Expect(occurrenceCount).To(Equal(2))
		Expect(grantCount).To(Equal(2))

		reupload := stage(value, defaultTeam.ID(), "revive")
		seal(newBuild(defaultTeam.ID()), "revive", nil, nil, []snapshot.SealCommitOutput{
			output("revive", "result", "opaque/v1", value, reupload),
		})
		Expect(dbConn.QueryRow(`SELECT count(*) FILTER (WHERE content_state = 'available') FROM agent_snapshots WHERE digest = $1`, value).Scan(&manifestCount)).To(Succeed())
		Expect(manifestCount).To(Equal(2))
	})

	It("enforces team grants and actor-scoped independent pins", func() {
		value := digest("a")
		staged := stage(value, defaultTeam.ID(), "grant")
		ref := seal(newBuild(defaultTeam.ID()), "grant", nil, nil, []snapshot.SealCommitOutput{
			output("value", "value", "opaque/v1", value, staged),
		})["value"].Snapshot

		other, err := teamFactory.CreateTeam(structTeam("other-snapshot-team"))
		Expect(err).NotTo(HaveOccurred())
		_, found, err := factory.GetAuthorized(ctx, other.ID(), ref.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())

		_, err = dbConn.Exec(`
			INSERT INTO agent_snapshot_grants (snapshot_id, team_id, granted_by, reason)
			VALUES ($1, $2, 'test', 'shared for test')
		`, int64(ref.ID), other.ID())
		Expect(err).NotTo(HaveOccurred())

		alice, err := factory.Pin(ctx, lease, other.ID(), "alice", ref, "investigation")
		Expect(err).NotTo(HaveOccurred())
		bob, err := factory.Pin(ctx, lease, other.ID(), "bob", ref, "audit")
		Expect(err).NotTo(HaveOccurred())
		Expect(alice.ID).NotTo(Equal(bob.ID))
		retriedBob, err := factory.Pin(ctx, lease, other.ID(), "bob", ref, "audit")
		Expect(err).NotTo(HaveOccurred())
		Expect(retriedBob.ID).To(Equal(bob.ID))
		_, err = factory.Pin(ctx, lease, other.ID(), "bob", ref, "different immutable reason")
		Expect(err).To(MatchError(ContainSubstring("pin conflicts")))
		defaultAlice, err := factory.Pin(ctx, lease, defaultTeam.ID(), "alice", ref, "local investigation")
		Expect(err).NotTo(HaveOccurred())
		Expect(defaultAlice.ID).NotTo(Equal(alice.ID))

		Expect(factory.Unpin(ctx, lease, other.ID(), "alice", ref)).To(Succeed())
		var claims int
		Expect(dbConn.QueryRow(`
			SELECT count(*) FROM agent_snapshot_retention_claims
			WHERE snapshot_id = $1 AND team_id = $2 AND class = 'pin'
		`, int64(ref.ID), other.ID()).Scan(&claims)).To(Succeed())
		Expect(claims).To(Equal(1))
		Expect(dbConn.QueryRow(`
			SELECT count(*) FROM agent_snapshot_retention_claims
			WHERE snapshot_id = $1 AND class = 'pin' AND actor = 'alice'
		`, int64(ref.ID)).Scan(&claims)).To(Succeed())
		Expect(claims).To(Equal(1), "unpinning the other team must preserve the default team's same-actor claim")
	})

	It("discovers lifecycle candidates with a stable cursor and mutates locations idempotently", func() {
		orphanDigest := digest("3")
		stage(orphanDigest, defaultTeam.ID(), "orphan")

		committedWithStaleStageDigest := digest("5")
		committedStage := stage(committedWithStaleStageDigest, defaultTeam.ID(), "committed")
		seal(newBuild(defaultTeam.ID()), "committed", nil, nil, []snapshot.SealCommitOutput{
			output("committed", "result", "opaque/v1", committedWithStaleStageDigest, committedStage),
		})
		staleCommittedRow := stage(committedWithStaleStageDigest, defaultTeam.ID(), "post-commit-stale-row")
		_, err := dbConn.Exec(`
			UPDATE agent_snapshot_staged_uploads
			SET created_at = now() - interval '2 hours',
			    lease_expires_at = now() - interval '1 hour'
			WHERE id = $1
		`, staleCommittedRow.ID)
		Expect(err).NotTo(HaveOccurred())

		failedRecaptureDigest := digest("6")
		originalStage := stage(failedRecaptureDigest, defaultTeam.ID(), "original")
		original := seal(newBuild(defaultTeam.ID()), "original", nil, nil, []snapshot.SealCommitOutput{
			output("original", "result", "opaque/v1", failedRecaptureDigest, originalStage),
		})["original"].Snapshot
		_, err = dbConn.Exec(`UPDATE agent_snapshots SET content_state = 'expired' WHERE id = $1`, int64(original.ID))
		Expect(err).NotTo(HaveOccurred())
		staleRecapture := stage(failedRecaptureDigest, defaultTeam.ID(), "failed-recapture")
		_, err = dbConn.Exec(`
			UPDATE agent_snapshot_staged_uploads
			SET created_at = now() - interval '2 hours',
			    lease_expires_at = now() - interval '1 hour'
			WHERE id = $1
		`, staleRecapture.ID)
		Expect(err).NotTo(HaveOccurred())

		repairDigest := digest("4")
		repairStage := stage(repairDigest, defaultTeam.ID(), "repair")
		seal(newBuild(defaultTeam.ID()), "repair", nil, nil, []snapshot.SealCommitOutput{
			output("repair", "result", "opaque/v1", repairDigest, repairStage),
		})
		location := output("unused", "result", "opaque/v1", repairDigest, repairStage).Locations[0]
		Expect(factory.RemoveLocation(ctx, lease, location)).To(Succeed())

		request := snapshot.LifecyclePageRequest{Limit: 1}
		var candidates []snapshot.LifecycleCandidate
		for {
			page, err := factory.DiscoverLifecycleCandidates(ctx, request)
			Expect(err).NotTo(HaveOccurred())
			candidates = append(candidates, page.Candidates...)
			if page.Terminal() {
				break
			}
			request.After = page.Next
		}
		Expect(candidates).To(ConsistOf(
			snapshot.LifecycleCandidate{Digest: orphanDigest, Kind: snapshot.LifecycleCandidateOrphan},
			snapshot.LifecycleCandidate{Digest: committedWithStaleStageDigest, Kind: snapshot.LifecycleCandidateOrphan},
			snapshot.LifecycleCandidate{Digest: failedRecaptureDigest, Kind: snapshot.LifecycleCandidateOrphan},
			snapshot.LifecycleCandidate{Digest: repairDigest, Kind: snapshot.LifecycleCandidateRepair},
		))

		Expect(factory.AddLocation(ctx, lease, location)).To(Succeed())
		Expect(factory.AddLocation(ctx, lease, location)).To(Succeed())
		locations, err := factory.LocationsForDigest(ctx, repairDigest)
		Expect(err).NotTo(HaveOccurred())
		Expect(locations).To(ConsistOf(location))
	})
})

func structTeam(name string) atc.Team { return atc.Team{Name: name} }
