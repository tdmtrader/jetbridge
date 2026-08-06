package db_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/concourse/concourse/agent/pagination"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("AgentSnapshotsFactory", func() {
	var (
		ctx     context.Context
		factory db.AgentSnapshotsFactory
		lease   snapshot.DigestLease
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
				TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(), CreatedBy: "alice",
				Build:      &snapshot.BuildOccurrence{BuildID: buildID, PlanID: "plan-1", Attempt: attempt, StepKind: "task", StepName: "produce"},
				Inputs:     inputs,
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

	importWorkflowDefinition := func(name string) (int, string) {
		definition, err := db.NewAgentWorkflowsFactory(dbConn).Import(name, []byte(fmt.Sprintf(`
schema_version: 3
name: %s
signature_version: 1
inputs:
  - name: source
    type: repository/v1
outputs: []
plan:
  - agent: work
    function_id: work
    prompt: Exercise snapshot provenance.
    inputs: [source]
    input_types:
      source: {type: repository/v1}
`, name)), "alice")
		Expect(err).NotTo(HaveOccurred())
		return definition.ID, definition.ContentHash
	}

	It("keyset-pages equal-timestamp authorized history and composes with created_after", func() {
		typeName := fmt.Sprintf("pagination-%d", time.Now().UnixNano())
		createdAt := time.Date(2026, time.July, 22, 12, 34, 56, 123456000, time.UTC)
		createdAfter := createdAt.Add(-time.Minute)
		var want []snapshot.SnapshotID
		for index, character := range []string{"a", "c", "d", "e"} {
			rowCreatedAt := createdAt
			if index == 3 {
				rowCreatedAt = createdAfter
			}
			var id int64
			Expect(dbConn.QueryRow(`
				INSERT INTO agent_snapshots
					(team_id, type_name, type_version, digest, byte_size, file_count, representation, created_at)
				VALUES ($1, $2, 1, $3, 1, 1, 'application/x-tar', $4)
				RETURNING id
			`, defaultTeam.ID(), typeName, "sha256:"+strings.Repeat(character, 64), rowCreatedAt).Scan(&id)).To(Succeed())
			if index < 3 {
				want = append(want, snapshot.SnapshotID(id))
			}
		}
		sort.Slice(want, func(i, j int) bool { return want[i] > want[j] })

		filter := snapshot.SnapshotListFilter{
			Type: snapshot.TypeRef(typeName + "/v1"), CreatedAfter: &createdAfter, Limit: 2,
		}
		var got []snapshot.SnapshotID
		for {
			page, err := factory.ListAuthorized(ctx, defaultTeam.ID(), filter)
			Expect(err).NotTo(HaveOccurred())
			if len(page) == 0 {
				break
			}
			for _, value := range page {
				Expect(value.CreatedAt).To(BeTemporally("==", createdAt))
				got = append(got, value.ID)
			}
			last := page[len(page)-1]
			filter.Before = &pagination.Cursor{CreatedAt: last.CreatedAt, ID: int64(last.ID)}
		}
		Expect(got).To(Equal(want))
	})

	type workflowSnapshotFixture struct {
		definitionID int
		runID        snapshot.WorkflowRunID
		otherRunID   snapshot.WorkflowRunID
		buildID      int
		input        snapshot.SnapshotRef
	}

	setupWorkflowSnapshotFixture := func(withOtherRun bool) workflowSnapshotFixture {
		inputDigest := digest("1")
		inputStage := stage(inputDigest, defaultTeam.ID(), "workflow-input")
		input := seal(newBuild(defaultTeam.ID()), "workflow-input", nil, nil, []snapshot.SealCommitOutput{
			output("input", "source", "repository/v1", inputDigest, inputStage),
		})["input"].Snapshot

		// Workflow admission takes the input digest advisory lock while it creates
		// the durable binding, so release the producer lease first.
		Expect(lease.Close()).To(Succeed())

		suffix := time.Now().UnixNano()
		definitionName := fmt.Sprintf("snapshot-input-provenance-%d", suffix)
		definitionID, definitionHash := importWorkflowDefinition(definitionName)

		runFactory := db.NewAgentWorkflowRunsFactory(dbConn)
		createRun := func(label string) db.AgentWorkflowRun {
			run, created, err := runFactory.CreateWithInputs(ctx, db.AgentWorkflowRunCreateRequest{
				TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(),
				WorkflowDefinitionID: definitionID, WorkflowName: definitionName, WorkflowVersion: 1,
				SchemaVersion: 3, SignatureVersion: 1, DefinitionContentHash: definitionHash,
				IdempotencyKey:          fmt.Sprintf("snapshot-input-provenance-%s-%d", label, suffix),
				ParameterizedConfig:     json.RawMessage(`{"jobs":[]}`),
				ParameterizedConfigHash: strings.Repeat("b", 64), OriginKind: "test",
				OriginReference: fmt.Sprintf("input-provenance-%s-%d", label, suffix),
				CreatedBy:       "alice", Status: db.AgentWorkflowRunStatusAdmitting,
				Inputs: map[string]snapshot.SnapshotRef{"source": input},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(created).To(BeTrue())
			return run
		}

		run := createRun("primary")
		var otherRunID snapshot.WorkflowRunID
		if withOtherRun {
			otherRunID = createRun("other").ID
		}

		var templateID, instanceID, pipelineRunID int
		Expect(dbConn.QueryRow(`
			INSERT INTO pipelines (name, team_id, secondary_ordering)
			VALUES ($1, $2, 1)
			RETURNING id
		`, fmt.Sprintf("snapshot-input-template-%d", suffix), defaultTeam.ID()).Scan(&templateID)).To(Succeed())
		Expect(dbConn.QueryRow(`
			INSERT INTO pipelines (name, team_id, secondary_ordering)
			VALUES ($1, $2, 1)
			RETURNING id
		`, fmt.Sprintf("snapshot-input-instance-%d", suffix), defaultTeam.ID()).Scan(&instanceID)).To(Succeed())
		Expect(dbConn.QueryRow(`
			INSERT INTO pipeline_runs (template_pipeline_id, instance_pipeline_id, number)
			VALUES ($1, $2, 1)
			RETURNING id
		`, templateID, instanceID).Scan(&pipelineRunID)).To(Succeed())
		Expect(runFactory.LinkExecution(ctx, run.ID, db.AgentWorkflowRunExecutionLink{
			PipelineRunID: pipelineRunID, TemplatePipelineID: templateID, InstancePipelineID: instanceID,
			ConcreteConfig: json.RawMessage(`{"instance":true}`), ConcreteConfigHash: strings.Repeat("c", 64),
		})).To(Succeed())

		var buildID int
		Expect(dbConn.QueryRow(`
			INSERT INTO builds (name, status, team_id, pipeline_id)
			VALUES ($1, 'started', $2, $3)
			RETURNING id
		`, fmt.Sprintf("snapshot-input-build-%d", suffix), defaultTeam.ID(), instanceID).Scan(&buildID)).To(Succeed())
		_, err := dbConn.Exec(`UPDATE agent_workflow_runs SET planned_build_id = $2 WHERE id = $1`, int64(run.ID), buildID)
		Expect(err).NotTo(HaveOccurred())
		Expect(runFactory.RecordPlan(ctx, run.ID, db.AgentWorkflowRunPlan{
			BuildID: int64(buildID), ActualPlan: json.RawMessage(`{"task":"input-provenance"}`),
			ActualPlanHash: strings.Repeat("d", 64), ResolvedDependencies: json.RawMessage(`{}`),
		})).To(Succeed())

		allDigests := make([]snapshot.Digest, 0, 16)
		for _, hexDigit := range "0123456789abcdef" {
			allDigests = append(allDigests, digest(string(hexDigit)))
		}
		lease, err = db.NewAgentSnapshotDigestLocker(dbConn).AcquireMany(ctx, allDigests)
		Expect(err).NotTo(HaveOccurred())
		workflowLease := lease
		DeferCleanup(func() { Expect(workflowLease.Close()).To(Succeed()) })

		return workflowSnapshotFixture{
			definitionID: definitionID,
			runID:        run.ID,
			otherRunID:   otherRunID,
			buildID:      buildID,
			input:        input,
		}
	}

	BeforeEach(func() {
		ctx = context.Background()
		// The lease itself occupies one pool slot. Keep one independent slot for
		// assertions that inspect intermediate state while the lease remains held.
		dbConn.SetMaxOpenConns(2)
		factory = db.NewAgentSnapshotsFactory(dbConn)
		allDigests := make([]snapshot.Digest, 0, 16)
		for _, hexDigit := range "0123456789abcdef" {
			allDigests = append(allDigests, digest(string(hexDigit)))
		}
		var err error
		lease, err = db.NewAgentSnapshotDigestLocker(dbConn).AcquireMany(ctx, allDigests)
		Expect(err).NotTo(HaveOccurred())
		baseLease := lease
		DeferCleanup(func() { Expect(baseLease.Close()).To(Succeed()) })
	})

	It("installs the complete durable snapshot schema", func() {
		tables := []string{
			"agent_snapshots",
			"agent_snapshot_locations",
			"agent_snapshot_staged_uploads",
			"agent_workflow_runs",
			"agent_snapshot_productions",
			"agent_snapshot_lineage",
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
		Expect(lease.Close()).To(Succeed())
		uncovered, err := db.NewAgentSnapshotDigestLocker(dbConn).AcquireMany(ctx, []snapshot.Digest{digest("2")})
		Expect(err).NotTo(HaveOccurred())
		_, err = factory.StageUpload(ctx, uncovered, snapshot.StageUploadRequest{
			Digest: value, TeamID: defaultTeam.ID(), Attempt: "attempt-1",
			LeaseExpiresAt: time.Now().Add(time.Hour),
		})
		Expect(err).To(MatchError(ContainSubstring("does not cover")))
		Expect(uncovered.Close()).To(Succeed())

		lease, err = db.NewAgentSnapshotDigestLocker(dbConn).AcquireMany(ctx, []snapshot.Digest{value})
		Expect(err).NotTo(HaveOccurred())
		expiry := time.Now().Add(time.Hour).Round(time.Microsecond)
		staged, err := factory.StageUpload(ctx, lease, snapshot.StageUploadRequest{
			Digest: value, TeamID: defaultTeam.ID(), Attempt: "attempt-1",
			LeaseExpiresAt: expiry,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(staged.ID).To(BeNumerically(">", 0))
		Expect(staged.LeaseExpiresAt).To(BeTemporally("==", expiry))
		Expect(staged.CreatedAt).ToNot(BeZero())
		Expect(lease.Close()).To(Succeed())
	})

	It("rejects a forged digest lease even when it claims coverage", func() {
		_, err := factory.StageUpload(ctx, forgedDigestLease{}, snapshot.StageUploadRequest{
			Digest: digest("1"), TeamID: defaultTeam.ID(), Attempt: "forged",
			LeaseExpiresAt: time.Now().Add(time.Hour),
		})
		Expect(err).To(MatchError(ContainSubstring("was not issued by the database lock manager")))
	})

	It("rejects a real digest lease owned by another database connection", func() {
		// A second real connection, not a stand-in for one: the rejection is
		// about connection identity, so the other connection has to be a genuine
		// one for the check to mean anything.
		otherConn := postgresRunner.OpenConn()
		defer func() { Expect(otherConn.Close()).To(Succeed()) }()

		otherFactory := db.NewAgentSnapshotsFactory(otherConn)
		_, err := otherFactory.DigestState(ctx, lease, digest("1"), time.Now())
		Expect(err).To(MatchError(ContainSubstring("belongs to another database connection")))
	})

	It("executes leased stage, commit, and state reads with one database connection", func() {
		Expect(lease.Close()).To(Succeed())
		dbConn.SetMaxOpenConns(1)
		value := digest("f")
		buildID := newBuild(defaultTeam.ID())
		manager := db.NewAgentSnapshotDigestLocker(dbConn)
		held, err := manager.AcquireMany(ctx, []snapshot.Digest{value})
		Expect(err).NotTo(HaveOccurred())
		defer func() { Expect(held.Close()).To(Succeed()) }()

		operationCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		staged, err := factory.StageUpload(operationCtx, held, snapshot.StageUploadRequest{
			Digest: value, TeamID: defaultTeam.ID(), Attempt: "single-connection",
			LeaseExpiresAt: time.Now().Add(time.Hour),
		})
		Expect(err).NotTo(HaveOccurred())

		candidate := output("single-connection", "result", "opaque/v1", value, staged)
		_, err = factory.CommitSealBatch(operationCtx, held, snapshot.SealCommit{
			Context: snapshot.SealCommitContext{
				TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(), CreatedBy: "alice",
				Build: &snapshot.BuildOccurrence{
					BuildID: buildID, PlanID: "plan-1", Attempt: "single-connection", StepKind: "task", StepName: "produce",
				},
				ExpectedOutputs: []snapshot.Port{candidate.Port},
			},
			Outputs: []snapshot.SealCommitOutput{candidate},
		})
		Expect(err).NotTo(HaveOccurred())

		state, err := factory.DigestState(operationCtx, held, value, time.Now())
		Expect(err).NotTo(HaveOccurred())
		Expect(state.Snapshots).To(HaveLen(1))
		Expect(state.Stages).To(BeEmpty())
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
				TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(), CreatedBy: "alice",
				Build:           &snapshot.BuildOccurrence{BuildID: newBuild(defaultTeam.ID()), PlanID: "plan-1", Attempt: "expired", StepKind: "task", StepName: "produce"},
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

	It("atomically seals team-owned manifests, occurrences, locations, claims, and ordered lineage", func() {
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

	It("finds only the exact authorized resource-capture output for one succeeded pipeline run", func() {
		operationKey := strings.Repeat("a", 64)
		createCaptureOutput := func(templateName, valueSeed string) (int64, snapshot.SnapshotRef) {
			var templateID, instanceID, pipelineRunID, buildID int
			Expect(dbConn.QueryRow(`
				INSERT INTO pipelines (name, team_id, secondary_ordering, template)
				VALUES ($1, $2, 1, true) RETURNING id
			`, templateName, defaultTeam.ID()).Scan(&templateID)).To(Succeed())
			_, err := dbConn.Exec(`INSERT INTO agent_workflow_run_templates (pipeline_id) VALUES ($1)`, templateID)
			Expect(err).NotTo(HaveOccurred())
			Expect(dbConn.QueryRow(`
				INSERT INTO pipelines (name, team_id, secondary_ordering, template, instance_vars)
				VALUES ($1, $2, 1, true, '{"run":1}') RETURNING id
			`, templateName, defaultTeam.ID()).Scan(&instanceID)).To(Succeed())
			Expect(dbConn.QueryRow(`
				INSERT INTO pipeline_runs (template_pipeline_id, instance_pipeline_id, number, status)
				VALUES ($1, $2, 1, 'succeeded') RETURNING id
			`, templateID, instanceID).Scan(&pipelineRunID)).To(Succeed())
			Expect(dbConn.QueryRow(`
				INSERT INTO builds (name, status, team_id, pipeline_id)
				VALUES ($1, 'succeeded', $2, $3) RETURNING id
			`, fmt.Sprintf("resource-capture-%s-%d", valueSeed, time.Now().UnixNano()), defaultTeam.ID(), instanceID).Scan(&buildID)).To(Succeed())

			value := digest(valueSeed)
			staged := stage(value, defaultTeam.ID(), "1")
			candidate := output("snapshot", "snapshot", "repository/v1", value, staged)
			candidate.SourceMetadata = json.RawMessage(fmt.Sprintf(`{"adapter":"resource-version","operation_key":%q,"snapshot_type":"repository/v1"}`, operationKey))
			sealed, err := factory.CommitSealBatch(ctx, lease, snapshot.SealCommit{
				Context: snapshot.SealCommitContext{
					TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(), CreatedBy: "alice",
					Build:  &snapshot.BuildOccurrence{BuildID: buildID, PlanID: "capture", Attempt: "1", StepKind: "task", StepName: "seal-snapshot"},
					Inputs: map[string]snapshot.SnapshotRef{}, InputOrder: []string{}, ExpectedOutputs: []snapshot.Port{candidate.Port},
				},
				Outputs: []snapshot.SealCommitOutput{candidate},
			})
			Expect(err).NotTo(HaveOccurred())
			return int64(pipelineRunID), sealed["snapshot"].Snapshot
		}
		validName := "agent-resource-capture-" + operationKey[:24] + "-" + strings.Repeat("a", 12)
		pipelineRunID, sealed := createCaptureOutput(validName, "e")
		missingSuffixRunID, _ := createCaptureOutput("agent-resource-capture-"+operationKey[:24], "f")
		uppercaseSuffixRunID, _ := createCaptureOutput("agent-resource-capture-"+operationKey[:24]+"-"+strings.Repeat("A", 12), "b")
		shortSuffixRunID, _ := createCaptureOutput("agent-resource-capture-"+operationKey[:24]+"-"+strings.Repeat("a", 11), "c")
		wrongOperationRunID, _ := createCaptureOutput("agent-resource-capture-"+strings.Repeat("b", 24)+"-"+strings.Repeat("a", 12), "d")
		invalidRuns := []int64{
			missingSuffixRunID,
			uppercaseSuffixRunID,
			shortSuffixRunID,
			wrongOperationRunID,
		}
		finder, ok := factory.(interface {
			FindResourceCaptureOutput(context.Context, int, int64, string, string, snapshot.TypeRef) (snapshot.Snapshot, bool, error)
		})
		Expect(ok).To(BeTrue())
		found, exists, err := finder.FindResourceCaptureOutput(ctx, defaultTeam.ID(), int64(pipelineRunID), operationKey, "snapshot", "repository/v1")
		Expect(err).NotTo(HaveOccurred())
		Expect(exists).To(BeTrue())
		Expect(snapshot.SnapshotRef{ID: found.ID, Type: found.Type, Digest: found.Digest}).To(Equal(sealed))
		_, exists, err = finder.FindResourceCaptureOutput(ctx, defaultTeam.ID(), int64(pipelineRunID), strings.Repeat("b", 64), "snapshot", "repository/v1")
		Expect(err).NotTo(HaveOccurred())
		Expect(exists).To(BeFalse())
		for _, invalidRunID := range invalidRuns {
			_, exists, err = finder.FindResourceCaptureOutput(ctx, defaultTeam.ID(), invalidRunID, operationKey, "snapshot", "repository/v1")
			Expect(err).NotTo(HaveOccurred())
			Expect(exists).To(BeFalse())
		}

		pendingFinder, ok := factory.(interface {
			ListPendingResourceCaptureOutputs(context.Context, string, int) ([]db.ResourceCaptureOutput, error)
		})
		Expect(ok).To(BeTrue())
		pending, err := pendingFinder.ListPendingResourceCaptureOutputs(ctx, "system:resource-capture", 100)
		Expect(err).NotTo(HaveOccurred())
		Expect(pending).To(ConsistOf(db.ResourceCaptureOutput{
			TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(), PipelineRunID: pipelineRunID,
			OperationKey: operationKey, OutputPort: "snapshot", ExpectedType: "repository/v1",
		}))
		_, err = factory.Pin(
			ctx, lease, defaultTeam.ID(), "system:resource-capture", sealed,
			"resource capture "+operationKey,
		)
		Expect(err).NotTo(HaveOccurred())
		pending, err = pendingFinder.ListPendingResourceCaptureOutputs(ctx, "system:resource-capture", 100)
		Expect(err).NotTo(HaveOccurred())
		Expect(pending).To(BeEmpty())
	})

	It("persists exposure and materialization lineage outside the sealed value", func() {
		typeRef := snapshot.TypeRef(fmt.Sprintf("exposure-lineage-%d/v1", time.Now().UnixNano()))
		sourceBuildID := newBuild(defaultTeam.ID())
		diffValue, baseValue := digest("7"), digest("8")
		diffRef := seal(sourceBuildID, "produce-diff", nil, nil, []snapshot.SealCommitOutput{
			output("diff", "diff", typeRef, diffValue, stage(diffValue, defaultTeam.ID(), "produce-diff")),
		})["diff"].Snapshot
		baseRef := seal(sourceBuildID, "produce-base", nil, nil, []snapshot.SealCommitOutput{
			output("base", "base", typeRef, baseValue, stage(baseValue, defaultTeam.ID(), "produce-base")),
		})["base"].Snapshot

		judgeBuildID := newBuild(defaultTeam.ID())
		reviewValue := digest("b")
		review := output("review", "review", typeRef, reviewValue, stage(reviewValue, defaultTeam.ID(), "judge"))
		commit := snapshot.SealCommit{
			Context: snapshot.SealCommitContext{
				TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(), CreatedBy: "alice",
				Build: &snapshot.BuildOccurrence{
					BuildID: judgeBuildID, PlanID: "plan-judge", Attempt: "judge",
					StepKind: "agent", StepName: "judge",
				},
				InputOrder: []string{"diff", "base"},
				Inputs:     map[string]snapshot.SnapshotRef{"diff": diffRef, "base": baseRef},
				InputExposures: map[string]snapshot.InputExposure{
					"diff": snapshot.FullTreeExposure("/tmp/build/plan/diff", diffRef.Digest),
					"base": snapshot.FullTreeExposure("/tmp/build/plan/base", baseRef.Digest),
				},
				ExpectedOutputs: []snapshot.Port{review.Port},
			},
			Outputs: []snapshot.SealCommitOutput{review},
		}
		sealed, err := factory.CommitSealBatch(ctx, lease, commit)
		Expect(err).NotTo(HaveOccurred())

		var productionID int64
		Expect(dbConn.QueryRow(`
			SELECT id FROM agent_snapshot_productions
			WHERE build_id = $1 AND plan_id = 'plan-judge' AND attempt = 'judge' AND output_port = 'review'
		`, judgeBuildID).Scan(&productionID)).To(Succeed())

		type exposureRow struct {
			Mode      string
			Tree      string
			MountPath sql.NullString
		}
		readExposure := func(port string) exposureRow {
			var row exposureRow
			Expect(dbConn.QueryRow(`
				SELECT materialization_mode, tree_digest, mount_path
				FROM agent_snapshot_exposures WHERE production_id = $1 AND input_port = $2
			`, productionID, port).Scan(&row.Mode, &row.Tree, &row.MountPath)).To(Succeed())
			return row
		}
		Expect(readExposure("diff")).To(Equal(exposureRow{
			Mode: "full", Tree: diffRef.Digest.String(),
			MountPath: sql.NullString{String: "/tmp/build/plan/diff", Valid: true},
		}))
		Expect(readExposure("base")).To(Equal(exposureRow{
			Mode: "full", Tree: baseRef.Digest.String(),
			MountPath: sql.NullString{String: "/tmp/build/plan/base", Valid: true},
		}))

		By("keeping exposure lineage out of the deduplicated content identity")
		var intrinsic sql.NullString
		Expect(dbConn.QueryRow(`
			SELECT intrinsic_metadata::text FROM agent_snapshots WHERE id = $1
		`, int64(sealed["review"].Snapshot.ID)).Scan(&intrinsic)).To(Succeed())
		Expect(intrinsic.String).NotTo(ContainSubstring("mount"))
		Expect(intrinsic.String).NotTo(ContainSubstring("materializ"))

		recommit := func(context snapshot.SealCommitContext) error {
			retried := review.Clone()
			retried.StagedUploadID = stage(reviewValue, defaultTeam.ID(), "judge").ID
			_, err := factory.CommitSealBatch(ctx, lease, snapshot.SealCommit{
				Context: context, Outputs: []snapshot.SealCommitOutput{retried},
			})
			return err
		}

		By("re-committing the identical occurrence idempotently")
		Expect(recommit(commit.Context.Clone())).To(Succeed())

		By("refusing an occurrence that reports a different exposure than it recorded")
		conflicting := commit.Context.Clone()
		conflicting.InputExposures["diff"] = snapshot.FullTreeExposure("/tmp/build/plan/elsewhere", diffRef.Digest)
		Expect(recommit(conflicting)).To(MatchError(ContainSubstring("exposure")))

		By("defaulting an undeclared exposure to the whole tree")
		plainBuildID := newBuild(defaultTeam.ID())
		plainValue := digest("c")
		plain := output("plain", "plain", typeRef, plainValue, stage(plainValue, defaultTeam.ID(), "plain"))
		_, err = factory.CommitSealBatch(ctx, lease, snapshot.SealCommit{
			Context: snapshot.SealCommitContext{
				TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(), CreatedBy: "alice",
				Build: &snapshot.BuildOccurrence{
					BuildID: plainBuildID, PlanID: "plan-plain", Attempt: "plain",
					StepKind: "task", StepName: "plain",
				},
				InputOrder:      []string{"diff"},
				Inputs:          map[string]snapshot.SnapshotRef{"diff": diffRef},
				ExpectedOutputs: []snapshot.Port{plain.Port},
			},
			Outputs: []snapshot.SealCommitOutput{plain},
		})
		Expect(err).NotTo(HaveOccurred())
		var defaulted exposureRow
		Expect(dbConn.QueryRow(`
			SELECT e.materialization_mode, e.tree_digest, e.mount_path
			FROM agent_snapshot_exposures e
			JOIN agent_snapshot_productions p ON p.id = e.production_id
			WHERE p.build_id = $1 AND e.input_port = 'diff'
		`, plainBuildID).Scan(&defaulted.Mode, &defaulted.Tree, &defaulted.MountPath)).To(Succeed())
		Expect(defaulted).To(Equal(exposureRow{Mode: "full", Tree: diffRef.Digest.String()}))
	})

	It("records honest idempotent upload occurrences without synthetic builds", func() {
		value := digest("0")
		staged := stage(value, defaultTeam.ID(), "upload:manual-1")
		candidate := output("upload", "snapshot", "opaque/v1", value, staged)
		candidate.Retention = []snapshot.RetentionSpec{{
			Class: snapshot.RetentionClassPin, Actor: "github:subject-1", Reason: "manual upload",
		}}
		candidate.SourceMetadata = json.RawMessage(`{"adapter":"upload","uploader":"Alice"}`)
		commit := func(candidate snapshot.SealCommitOutput, createdBy string) (map[string]snapshot.SealedOutput, error) {
			return factory.CommitSealBatch(ctx, lease, snapshot.SealCommit{
				Context: snapshot.SealCommitContext{
					TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(), CreatedBy: createdBy,
					Upload: &snapshot.UploadOccurrence{IdempotencyKey: "manual-1"},
					Inputs: map[string]snapshot.SnapshotRef{}, InputOrder: []string{},
					ExpectedOutputs: []snapshot.Port{candidate.Port},
				},
				Outputs: []snapshot.SealCommitOutput{candidate},
			})
		}

		sealed, err := commit(candidate, "Alice")
		Expect(err).NotTo(HaveOccurred())
		manifest := sealed["upload"].Snapshot
		var kind string
		var buildID sql.NullInt64
		var key string
		Expect(dbConn.QueryRow(`
			SELECT occurrence_kind, build_id, upload_idempotency_key
			FROM agent_snapshot_productions
			WHERE snapshot_id = $1
		`, int64(manifest.ID)).Scan(&kind, &buildID, &key)).To(Succeed())
		Expect(kind).To(Equal("upload"))
		Expect(buildID.Valid).To(BeFalse())
		Expect(key).To(Equal("manual-1"))

		var owner, pins int
		Expect(dbConn.QueryRow(`SELECT team_id FROM agent_snapshots WHERE id = $1`, int64(manifest.ID)).Scan(&owner)).To(Succeed())
		Expect(dbConn.QueryRow(`SELECT count(*) FROM agent_snapshot_retention_claims WHERE snapshot_id = $1 AND team_id = $2 AND class = 'pin' AND actor = 'github:subject-1'`, int64(manifest.ID), defaultTeam.ID()).Scan(&pins)).To(Succeed())
		Expect(owner).To(Equal(defaultTeam.ID()))
		Expect(pins).To(Equal(1))

		retryStage := stage(value, defaultTeam.ID(), "upload:manual-1")
		retry := candidate.Clone()
		retry.StagedUploadID = retryStage.ID
		retried, err := commit(retry, "Alice")
		Expect(err).NotTo(HaveOccurred())
		Expect(retried["upload"].Snapshot).To(Equal(manifest))
		var productions int
		Expect(dbConn.QueryRow(`SELECT count(*) FROM agent_snapshot_productions WHERE team_id = $1 AND upload_idempotency_key = 'manual-1'`, defaultTeam.ID()).Scan(&productions)).To(Succeed())
		Expect(productions).To(Equal(1))

		renameStage := stage(value, defaultTeam.ID(), "upload:manual-1")
		renameRetry := candidate.Clone()
		renameRetry.StagedUploadID = renameStage.ID
		renameRetry.Retention[0].Actor = "github:another-subject"
		renameRetry.SourceMetadata = json.RawMessage(`{"adapter":"upload","uploader":"Alice Renamed"}`)
		renamed, err := commit(renameRetry, "Alice Renamed")
		Expect(err).NotTo(HaveOccurred())
		Expect(renamed["upload"].Snapshot).To(Equal(manifest))
		var renamedPins int
		Expect(dbConn.QueryRow(`
			SELECT count(*) FROM agent_snapshot_retention_claims
			WHERE snapshot_id = $1 AND team_id = $2 AND class = 'pin' AND actor = 'github:another-subject'
		`, int64(manifest.ID), defaultTeam.ID()).Scan(&renamedPins)).To(Succeed())
		Expect(renamedPins).To(Equal(0))

		conflictDigest := digest("1")
		conflictStage := stage(conflictDigest, defaultTeam.ID(), "upload:manual-1")
		conflict := output("upload", "snapshot", "opaque/v1", conflictDigest, conflictStage)
		conflict.Retention = candidate.Retention
		conflict.SourceMetadata = candidate.SourceMetadata
		_, err = commit(conflict, "Alice")
		Expect(errors.Is(err, snapshot.ErrConflict)).To(BeTrue(), "error: %v", err)
		var conflictManifests, retainedStages int
		Expect(dbConn.QueryRow(`SELECT count(*) FROM agent_snapshots WHERE digest = $1`, conflictDigest).Scan(&conflictManifests)).To(Succeed())
		Expect(dbConn.QueryRow(`SELECT count(*) FROM agent_snapshot_staged_uploads WHERE id = $1`, conflictStage.ID).Scan(&retainedStages)).To(Succeed())
		Expect(conflictManifests).To(Equal(0))
		Expect(retainedStages).To(Equal(1))
	})

	It("treats recomputed binding expiry as idempotent production policy", func() {
		value := digest("8")
		buildID := newBuild(defaultTeam.ID())
		firstExpiry := time.Now().Add(7 * 24 * time.Hour).Round(time.Microsecond)
		commit := func(staged snapshot.StagedUpload, expiresAt time.Time) (map[string]snapshot.SealedOutput, error) {
			candidate := output("result", "result", "opaque/v1", value, staged)
			candidate.Retention = []snapshot.RetentionSpec{{
				Class: snapshot.RetentionClassBinding, Actor: "build:retry", Reason: "build output", ExpiresAt: &expiresAt,
			}}
			return factory.CommitSealBatch(ctx, lease, snapshot.SealCommit{
				Context: snapshot.SealCommitContext{
					TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(), CreatedBy: "alice",
					Build:  &snapshot.BuildOccurrence{BuildID: buildID, PlanID: "plan-1", Attempt: "retry", StepKind: "task", StepName: "produce"},
					Inputs: map[string]snapshot.SnapshotRef{}, InputOrder: []string{}, ExpectedOutputs: []snapshot.Port{candidate.Port},
				},
				Outputs: []snapshot.SealCommitOutput{candidate},
			})
		}

		first, err := commit(stage(value, defaultTeam.ID(), "retry"), firstExpiry)
		Expect(err).NotTo(HaveOccurred())
		second, err := commit(stage(value, defaultTeam.ID(), "retry"), firstExpiry.Add(time.Minute))
		Expect(err).NotTo(HaveOccurred())
		Expect(second["result"].Snapshot).To(Equal(first["result"].Snapshot))

		var stored time.Time
		Expect(dbConn.QueryRow(`
			SELECT expires_at FROM agent_snapshot_retention_claims
			WHERE snapshot_id = $1 AND team_id = $2 AND class = 'binding' AND actor = 'build:retry'
		`, int64(first["result"].Snapshot.ID), defaultTeam.ID()).Scan(&stored)).To(Succeed())
		Expect(stored).To(BeTemporally("==", firstExpiry))
	})

	It("accepts workflow admission bindings and exact same-build typed productions", func() {
		fixture := setupWorkflowSnapshotFixture(false)
		definitionID, runID := fixture.definitionID, fixture.runID
		cases := []struct {
			stepKind      string
			producedDigit string
			consumedDigit string
		}{
			{stepKind: "task", producedDigit: "2", consumedDigit: "3"},
			{stepKind: "agent", producedDigit: "4", consumedDigit: "5"},
			{stepKind: "await_snapshot", producedDigit: "6", consumedDigit: "7"},
		}

		for _, test := range cases {
			By("accepting an exact " + test.stepKind + " production")
			producedAttempt := "produce-" + test.stepKind
			producedPort := "artifact-" + test.stepKind
			producedStage := stage(digest(test.producedDigit), defaultTeam.ID(), producedAttempt)
			producedOutput := output(
				"produced-"+test.stepKind,
				producedPort,
				"opaque/v1",
				digest(test.producedDigit),
				producedStage,
			)
			runIDCopy := runID
			producedOutput.Retention = append(producedOutput.Retention, snapshot.RetentionSpec{
				Class: snapshot.RetentionClassRun, WorkflowRunID: &runIDCopy,
				Actor:  fmt.Sprintf("workflow-run:%d:test-output:%s", int64(runID), producedPort),
				Reason: "active workflow-run internal output",
			})
			produced, err := factory.CommitSealBatch(ctx, lease, snapshot.SealCommit{
				Context: snapshot.SealCommitContext{
					TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(), CreatedBy: "alice",
					Build: &snapshot.BuildOccurrence{
						BuildID: fixture.buildID, PlanID: "plan-" + producedAttempt, Attempt: producedAttempt,
						StepKind: test.stepKind, StepName: "produce-" + test.stepKind,
						WorkflowDefinitionID: &definitionID, WorkflowRunID: &runID,
					},
					Inputs: map[string]snapshot.SnapshotRef{
						"source": fixture.input,
					},
					InputOrder: []string{"source"}, ExpectedOutputs: []snapshot.Port{producedOutput.Port},
				},
				Outputs: []snapshot.SealCommitOutput{producedOutput},
			})
			Expect(err).NotTo(HaveOccurred(), "the original workflow input binding must remain valid")
			var runClaims int
			Expect(dbConn.QueryRow(`
				SELECT count(*) FROM agent_snapshot_retention_claims
				WHERE snapshot_id = $1 AND team_id = $2 AND class = 'run'
				  AND workflow_run_id = $3 AND expires_at IS NULL
			`, int64(produced["produced-"+test.stepKind].Snapshot.ID), defaultTeam.ID(),
				int64(runID)).Scan(&runClaims)).To(Succeed())
			Expect(runClaims).To(Equal(1))

			consumedAttempt := "consume-" + test.stepKind
			consumedStage := stage(digest(test.consumedDigit), defaultTeam.ID(), consumedAttempt)
			consumedOutput := output(
				"consumed-"+test.stepKind,
				"consumed-"+test.stepKind,
				"opaque/v1",
				digest(test.consumedDigit),
				consumedStage,
			)
			_, err = factory.CommitSealBatch(ctx, lease, snapshot.SealCommit{
				Context: snapshot.SealCommitContext{
					TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(), CreatedBy: "alice",
					Build: &snapshot.BuildOccurrence{
						BuildID: fixture.buildID, PlanID: "plan-" + consumedAttempt, Attempt: consumedAttempt,
						StepKind: "agent", StepName: "consume-" + test.stepKind,
						WorkflowDefinitionID: &definitionID, WorkflowRunID: &runID,
					},
					Inputs: map[string]snapshot.SnapshotRef{
						producedPort: produced["produced-"+test.stepKind].Snapshot,
					},
					InputOrder: []string{producedPort}, ExpectedOutputs: []snapshot.Port{consumedOutput.Port},
				},
				Outputs: []snapshot.SealCommitOutput{consumedOutput},
			})
			Expect(err).NotTo(HaveOccurred())
		}
	})

	It("rejects workflow inputs without exact run, build, team, port, and snapshot provenance", func() {
		fixture := setupWorkflowSnapshotFixture(true)
		definitionID, runID := fixture.definitionID, fixture.runID

		exactStage := stage(digest("2"), defaultTeam.ID(), "produce-exact")
		exactOutput := output("exact", "artifact", "opaque/v1", digest("2"), exactStage)
		exact, err := factory.CommitSealBatch(ctx, lease, snapshot.SealCommit{
			Context: snapshot.SealCommitContext{
				TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(), CreatedBy: "alice",
				Build: &snapshot.BuildOccurrence{
					BuildID: fixture.buildID, PlanID: "plan-produce-exact", Attempt: "produce-exact",
					StepKind: "agent", StepName: "produce-exact",
					WorkflowDefinitionID: &definitionID, WorkflowRunID: &runID,
				},
				Inputs: map[string]snapshot.SnapshotRef{"source": fixture.input}, InputOrder: []string{"source"},
				ExpectedOutputs: []snapshot.Port{exactOutput.Port},
			},
			Outputs: []snapshot.SealCommitOutput{exactOutput},
		})
		Expect(err).NotTo(HaveOccurred())
		exactRef := exact["exact"].Snapshot

		seedBuildProduction := func(
			hexDigit string,
			buildID int,
			port string,
			attempt string,
		) snapshot.SnapshotRef {
			staged := stage(digest(hexDigit), defaultTeam.ID(), attempt)
			candidate := output(attempt, port, "opaque/v1", digest(hexDigit), staged)
			sealed, err := factory.CommitSealBatch(ctx, lease, snapshot.SealCommit{
				Context: snapshot.SealCommitContext{
					TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(), CreatedBy: "alice",
					Build: &snapshot.BuildOccurrence{
						BuildID: buildID, PlanID: "plan-" + attempt, Attempt: attempt,
						StepKind: "task", StepName: attempt,
					},
					ExpectedOutputs: []snapshot.Port{candidate.Port},
				},
				Outputs: []snapshot.SealCommitOutput{candidate},
			})
			Expect(err).NotTo(HaveOccurred())
			return sealed[attempt].Snapshot
		}

		associateProduction := func(
			ref snapshot.SnapshotRef,
			buildID int,
			port string,
			associatedRunID snapshot.WorkflowRunID,
			teamID int,
			teamName string,
		) {
			result, err := dbConn.Exec(`
				UPDATE agent_snapshot_productions
				SET workflow_definition_id = $1, workflow_run_id = $2,
				    team_id = $3, team_name = $4
				WHERE snapshot_id = $5 AND build_id = $6 AND output_port = $7
			`, definitionID, int64(associatedRunID), teamID, teamName, int64(ref.ID), buildID, port)
			Expect(err).NotTo(HaveOccurred())
			rows, err := result.RowsAffected()
			Expect(err).NotTo(HaveOccurred())
			Expect(rows).To(Equal(int64(1)))
		}

		crossRun := seedBuildProduction("3", fixture.buildID, "cross-run", "seed-cross-run")
		associateProduction(
			crossRun,
			fixture.buildID,
			"cross-run",
			fixture.otherRunID,
			defaultTeam.ID(),
			defaultTeam.Name(),
		)

		otherBuildID := newBuild(defaultTeam.ID())
		crossBuild := seedBuildProduction("4", otherBuildID, "cross-build", "seed-cross-build")
		associateProduction(
			crossBuild,
			otherBuildID,
			"cross-build",
			runID,
			defaultTeam.ID(),
			defaultTeam.Name(),
		)

		otherTeam, err := teamFactory.CreateTeam(structTeam(fmt.Sprintf("snapshot-input-other-%d", time.Now().UnixNano())))
		Expect(err).NotTo(HaveOccurred())
		crossTeam := seedBuildProduction("5", fixture.buildID, "cross-team", "seed-cross-team")
		associateProduction(crossTeam, fixture.buildID, "cross-team", runID, otherTeam.ID(), otherTeam.Name())

		unauthorizedStage := stage(digest("6"), otherTeam.ID(), "unauthorized")
		unauthorizedOutput := output("unauthorized", "unauthorized", "opaque/v1", digest("6"), unauthorizedStage)
		unauthorizedBuildID := newBuild(otherTeam.ID())
		unauthorized, err := factory.CommitSealBatch(ctx, lease, snapshot.SealCommit{
			Context: snapshot.SealCommitContext{
				TeamID: otherTeam.ID(), TeamName: otherTeam.Name(), CreatedBy: "bob",
				Build: &snapshot.BuildOccurrence{
					BuildID: unauthorizedBuildID, PlanID: "plan-unauthorized", Attempt: "unauthorized",
					StepKind: "task", StepName: "unauthorized",
				},
				ExpectedOutputs: []snapshot.Port{unauthorizedOutput.Port},
			},
			Outputs: []snapshot.SealCommitOutput{unauthorizedOutput},
		})
		Expect(err).NotTo(HaveOccurred())

		rejectedStage := stage(digest("7"), defaultTeam.ID(), "reject-input")
		rejectedOutput := output("rejected", "rejected", "opaque/v1", digest("7"), rejectedStage)
		cases := []struct {
			name        string
			port        string
			ref         snapshot.SnapshotRef
			errContains string
		}{
			{name: "wrong port", port: "not-artifact", ref: exactRef, errContains: "workflow-run binding"},
			{name: "wrong snapshot", port: "artifact", ref: fixture.input, errContains: "workflow-run binding"},
			{name: "cross run", port: "cross-run", ref: crossRun, errContains: "workflow-run binding"},
			{name: "cross build", port: "cross-build", ref: crossBuild, errContains: "workflow-run binding"},
			{name: "cross team", port: "cross-team", ref: crossTeam, errContains: "workflow-run binding"},
			{
				name: "unauthorized", port: "unauthorized", ref: unauthorized["unauthorized"].Snapshot,
				errContains: "absent, unavailable, or unauthorized",
			},
		}

		for _, test := range cases {
			By("rejecting a " + test.name + " workflow input")
			_, err := factory.CommitSealBatch(ctx, lease, snapshot.SealCommit{
				Context: snapshot.SealCommitContext{
					TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(), CreatedBy: "alice",
					Build: &snapshot.BuildOccurrence{
						BuildID: fixture.buildID, PlanID: "plan-reject-" + strings.ReplaceAll(test.name, " ", "-"),
						Attempt: "reject-input", StepKind: "agent", StepName: "reject-input",
						WorkflowDefinitionID: &definitionID, WorkflowRunID: &runID,
					},
					Inputs: map[string]snapshot.SnapshotRef{test.port: test.ref}, InputOrder: []string{test.port},
					ExpectedOutputs: []snapshot.Port{rejectedOutput.Port},
				},
				Outputs: []snapshot.SealCommitOutput{rejectedOutput},
			})
			Expect(err).To(MatchError(ContainSubstring(test.errContains)))
		}
	})

	It("atomically stages workflow outputs and preserves production history after build deletion", func() {
		inputDigest := digest("1")
		inputStage := stage(inputDigest, defaultTeam.ID(), "input")
		input := seal(newBuild(defaultTeam.ID()), "input", nil, nil, []snapshot.SealCommitOutput{
			output("input", "source", "repository/v1", inputDigest, inputStage),
		})["input"].Snapshot
		// Workflow admission takes the same digest advisory lock while it creates
		// the durable input claim. Release the producer lease before admission.
		Expect(lease.Close()).To(Succeed())

		definitionName := fmt.Sprintf("snapshot-output-binding-%d", time.Now().UnixNano())
		definitionID, definitionHash := importWorkflowDefinition(definitionName)
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
		lease, err = db.NewAgentSnapshotDigestLocker(dbConn).AcquireMany(ctx, []snapshot.Digest{
			digest("2"), digest("3"),
		})
		Expect(err).NotTo(HaveOccurred())
		defer func() { Expect(lease.Close()).To(Succeed()) }()

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
		Expect(dbConn.QueryRow(`INSERT INTO builds (name, status, team_id, pipeline_id) VALUES ($1, 'started', $2, $3) RETURNING id`, fmt.Sprintf("snapshot-output-%d", pipelineSuffix), defaultTeam.ID(), instanceID).Scan(&producerBuildID)).To(Succeed())
		_, err = dbConn.Exec(`UPDATE agent_workflow_runs SET planned_build_id = $2 WHERE id = $1`, int64(run.ID), producerBuildID)
		Expect(err).NotTo(HaveOccurred())

		resultDigest := digest("2")
		resultStage := stage(resultDigest, defaultTeam.ID(), "workflow-output")
		result := output("result", "result", "review/v1", resultDigest, resultStage)
		result.WorkflowPort = "public-review"
		result.Retention = append(result.Retention, snapshot.RetentionSpec{
			Class: snapshot.RetentionClassWorkflow, Actor: "workflow-output", Reason: "durable workflow output",
		})
		runID := run.ID
		_, err = factory.CommitSealBatch(ctx, lease, snapshot.SealCommit{
			Context: snapshot.SealCommitContext{
				TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(), CreatedBy: "alice",
				Build: &snapshot.BuildOccurrence{BuildID: producerBuildID, PlanID: "plan-output", Attempt: "workflow-output",
					StepKind: "agent", StepName: "review", WorkflowDefinitionID: &definitionID, WorkflowRunID: &runID},
				InputOrder: []string{"source"}, Inputs: map[string]snapshot.SnapshotRef{"source": input},
				ExpectedOutputs: []snapshot.Port{result.Port},
			},
			Outputs: []snapshot.SealCommitOutput{result},
		})
		Expect(err).To(MatchError(ContainSubstring("actual plan")))
		var retainedStage int
		Expect(dbConn.QueryRow(`SELECT count(*) FROM agent_snapshot_staged_uploads WHERE id = $1`, resultStage.ID).Scan(&retainedStage)).To(Succeed())
		Expect(retainedStage).To(Equal(1), "missing-provenance rejection must roll the seal transaction back")

		Expect(runFactory.RecordPlan(ctx, run.ID, db.AgentWorkflowRunPlan{
			BuildID: int64(producerBuildID), ActualPlan: json.RawMessage(`{"task":"review"}`),
			ActualPlanHash: strings.Repeat("d", 64), ResolvedDependencies: json.RawMessage(`{}`),
		})).To(Succeed())
		_, err = dbConn.Exec(`UPDATE agent_workflow_runs SET status = 'succeeded', completed_at = now() WHERE id = $1`, int64(run.ID))
		Expect(err).NotTo(HaveOccurred())
		_, err = factory.CommitSealBatch(ctx, lease, snapshot.SealCommit{
			Context: snapshot.SealCommitContext{
				TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(), CreatedBy: "alice",
				Build: &snapshot.BuildOccurrence{BuildID: producerBuildID, PlanID: "plan-output", Attempt: "workflow-output",
					StepKind: "agent", StepName: "review", WorkflowDefinitionID: &definitionID, WorkflowRunID: &runID},
				InputOrder: []string{"source"}, Inputs: map[string]snapshot.SnapshotRef{"source": input},
				ExpectedOutputs: []snapshot.Port{result.Port},
			},
			Outputs: []snapshot.SealCommitOutput{result},
		})
		Expect(err).To(MatchError(ContainSubstring("active")))
		Expect(dbConn.QueryRow(`SELECT count(*) FROM agent_snapshot_staged_uploads WHERE id = $1`, resultStage.ID).Scan(&retainedStage)).To(Succeed())
		Expect(retainedStage).To(Equal(1), "terminal-run rejection must roll the seal transaction back")
		_, err = dbConn.Exec(`UPDATE agent_workflow_runs SET status = 'admitting', completed_at = NULL WHERE id = $1`, int64(run.ID))
		Expect(err).NotTo(HaveOccurred())

		wrongBuildID := newBuild(defaultTeam.ID())
		_, err = factory.CommitSealBatch(ctx, lease, snapshot.SealCommit{
			Context: snapshot.SealCommitContext{
				TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(), CreatedBy: "alice",
				Build: &snapshot.BuildOccurrence{BuildID: wrongBuildID, PlanID: "plan-output", Attempt: "workflow-output",
					StepKind: "agent", StepName: "review", WorkflowDefinitionID: &definitionID, WorkflowRunID: &runID},
				InputOrder: []string{"source"}, Inputs: map[string]snapshot.SnapshotRef{"source": input},
				ExpectedOutputs: []snapshot.Port{result.Port},
			},
			Outputs: []snapshot.SealCommitOutput{result},
		})
		Expect(err).To(MatchError(ContainSubstring("planned build")))
		Expect(dbConn.QueryRow(`SELECT count(*) FROM agent_snapshot_staged_uploads WHERE id = $1`, resultStage.ID).Scan(&retainedStage)).To(Succeed())
		Expect(retainedStage).To(Equal(1), "wrong-build rejection must roll the seal transaction back")

		duplicate := result.Clone()
		duplicate.ClientKey = "duplicate"
		duplicate.Port.Name = "duplicate"
		_, err = factory.CommitSealBatch(ctx, lease, snapshot.SealCommit{
			Context: snapshot.SealCommitContext{
				TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(), CreatedBy: "alice",
				Build: &snapshot.BuildOccurrence{BuildID: producerBuildID, PlanID: "plan-output", Attempt: "workflow-output",
					StepKind: "agent", StepName: "review", WorkflowDefinitionID: &definitionID, WorkflowRunID: &runID},
				InputOrder: []string{"source"}, Inputs: map[string]snapshot.SnapshotRef{"source": input},
				ExpectedOutputs: []snapshot.Port{result.Port, duplicate.Port},
			},
			Outputs: []snapshot.SealCommitOutput{result, duplicate},
		})
		Expect(err).To(MatchError(ContainSubstring("duplicate workflow port")))
		var manifests int
		Expect(dbConn.QueryRow(`SELECT count(*) FROM agent_snapshots WHERE digest = $1`, resultDigest).Scan(&manifests)).To(Succeed())
		Expect(manifests).To(Equal(0))
		Expect(dbConn.QueryRow(`SELECT count(*) FROM agent_snapshot_staged_uploads WHERE id = $1`, resultStage.ID).Scan(&retainedStage)).To(Succeed())
		Expect(retainedStage).To(Equal(1), "duplicate workflow-port rejection must not consume its stage")

		bindingDigest := digest("3")
		bindingStage := stage(bindingDigest, defaultTeam.ID(), "workflow-output")
		binding := output("audit", "audit", "log-bundle/v1", bindingDigest, bindingStage)

		sealed, err := factory.CommitSealBatch(ctx, lease, snapshot.SealCommit{
			Context: snapshot.SealCommitContext{
				TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(), CreatedBy: "alice",
				Build: &snapshot.BuildOccurrence{BuildID: producerBuildID, PlanID: "plan-output", Attempt: "workflow-output",
					StepKind: "agent", StepName: "review", WorkflowDefinitionID: &definitionID, WorkflowRunID: &runID},
				InputOrder: []string{"source"}, Inputs: map[string]snapshot.SnapshotRef{"source": input},
				ExpectedOutputs: []snapshot.Port{result.Port, binding.Port},
			},
			Outputs: []snapshot.SealCommitOutput{result, binding},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(sealed).To(HaveKey("audit"))

		bindings, err := runFactory.Snapshots(ctx, run.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(bindings).To(ConsistOf(db.AgentWorkflowRunSnapshotBinding{
			WorkflowRunID: run.ID, Direction: db.AgentWorkflowRunSnapshotInput,
			PortName: "source", Snapshot: input,
		}))
		var stagedSnapshotID int64
		var promotedAt sql.NullTime
		Expect(dbConn.QueryRow(`
			SELECT snapshot_id, promoted_at
			FROM agent_workflow_run_snapshots
			WHERE workflow_run_id = $1 AND direction = 'output' AND port_name = 'public-review'
		`, int64(run.ID)).Scan(&stagedSnapshotID, &promotedAt)).To(Succeed())
		Expect(stagedSnapshotID).To(Equal(int64(sealed["result"].Snapshot.ID)))
		Expect(promotedAt.Valid).To(BeFalse(), "active-run output must remain an invisible candidate")

		_, err = dbConn.Exec(`DELETE FROM builds WHERE id = $1`, producerBuildID)
		Expect(err).NotTo(HaveOccurred())
		var productions int
		Expect(dbConn.QueryRow(`
			SELECT count(*) FROM agent_snapshot_productions
			WHERE build_id = $1 AND workflow_run_id = $2
		`, producerBuildID, int64(run.ID)).Scan(&productions)).To(Succeed())
		Expect(productions).To(Equal(2))

		detail, found, err := factory.GetAuthorizedDetail(ctx, defaultTeam.ID(), sealed["result"].Snapshot.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(detail.Productions).To(HaveLen(1))
		Expect(detail.Productions[0].Build).NotTo(BeNil())
		Expect(detail.Productions[0].Build.WorkflowName).To(Equal(definitionName))
		Expect(detail.Productions[0].Build.WorkflowRunID).NotTo(BeNil())
		Expect(*detail.Productions[0].Build.WorkflowRunID).To(Equal(run.ID))
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
				TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(), CreatedBy: "alice",
				Build:           &snapshot.BuildOccurrence{BuildID: newBuild(defaultTeam.ID()), PlanID: "plan-1", Attempt: "two", StepKind: "task", StepName: "produce"},
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
				TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(), CreatedBy: "alice",
				Build: &snapshot.BuildOccurrence{BuildID: newBuild(defaultTeam.ID()), PlanID: "plan-1", Attempt: "batch", StepKind: "task", StepName: "produce"},
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
				TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(), CreatedBy: "alice",
				Build:           &snapshot.BuildOccurrence{BuildID: buildID, PlanID: "plan-1", Attempt: "batch", StepKind: "task", StepName: "produce"},
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
						TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(), CreatedBy: "alice",
						Build:           &snapshot.BuildOccurrence{BuildID: builds[index], PlanID: "plan-1", Attempt: fmt.Sprintf("concurrent-%d", index+1), StepKind: "task", StepName: "produce"},
						ExpectedOutputs: []snapshot.Port{candidate.Port},
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
				TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(), CreatedBy: "alice",
				Build:           &snapshot.BuildOccurrence{BuildID: buildID, PlanID: "plan-1", Attempt: "retry", StepKind: "task", StepName: "produce"},
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
				TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(), CreatedBy: "alice",
				Build:           &snapshot.BuildOccurrence{BuildID: buildID, PlanID: "plan-1", Attempt: "lineage", StepKind: "task", StepName: "produce"},
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

		var manifestCount, expiredCount, occurrenceCount, ownerCount int
		Expect(dbConn.QueryRow(`SELECT count(*), count(*) FILTER (WHERE content_state = 'expired') FROM agent_snapshots WHERE digest = $1`, value).Scan(&manifestCount, &expiredCount)).To(Succeed())
		Expect(dbConn.QueryRow(`SELECT count(*) FROM agent_snapshot_productions p JOIN agent_snapshots s ON s.id = p.snapshot_id WHERE s.digest = $1`, value).Scan(&occurrenceCount)).To(Succeed())
		Expect(dbConn.QueryRow(`SELECT count(*) FROM agent_snapshots WHERE digest = $1 AND team_id = $2`, value, defaultTeam.ID()).Scan(&ownerCount)).To(Succeed())
		Expect(manifestCount).To(Equal(2))
		Expect(expiredCount).To(Equal(2))
		Expect(occurrenceCount).To(Equal(2))
		Expect(ownerCount).To(Equal(2))

		reupload := stage(value, defaultTeam.ID(), "revive")
		seal(newBuild(defaultTeam.ID()), "revive", nil, nil, []snapshot.SealCommitOutput{
			output("revive", "result", "opaque/v1", value, reupload),
		})
		Expect(dbConn.QueryRow(`SELECT count(*) FILTER (WHERE content_state = 'available') FROM agent_snapshots WHERE digest = $1`, value).Scan(&manifestCount)).To(Succeed())
		Expect(manifestCount).To(Equal(2))
	})

	It("enforces direct team ownership while allowing independently owned equal digests", func() {
		value := digest("a")
		staged := stage(value, defaultTeam.ID(), "grant")
		ref := seal(newBuild(defaultTeam.ID()), "grant", nil, nil, []snapshot.SealCommitOutput{
			output("value", "value", "opaque/v1", value, staged),
		})["value"].Snapshot

		other, err := teamFactory.CreateTeam(structTeam("other-snapshot-team"))
		Expect(err).NotTo(HaveOccurred())
		otherStage := stage(value, other.ID(), "other-owner")
		otherOutput := output("other", "value", "opaque/v1", value, otherStage)
		otherSealed, err := factory.CommitSealBatch(ctx, lease, snapshot.SealCommit{
			Context: snapshot.SealCommitContext{
				TeamID: other.ID(), TeamName: other.Name(), CreatedBy: "alice",
				Build: &snapshot.BuildOccurrence{
					BuildID: newBuild(other.ID()), PlanID: "plan-other", Attempt: "other-owner",
					StepKind: "task", StepName: "produce",
				},
				Inputs: map[string]snapshot.SnapshotRef{}, InputOrder: []string{},
				ExpectedOutputs: []snapshot.Port{otherOutput.Port},
			},
			Outputs: []snapshot.SealCommitOutput{otherOutput},
		})
		Expect(err).NotTo(HaveOccurred())
		otherRef := otherSealed["other"].Snapshot
		Expect(otherRef.ID).NotTo(Equal(ref.ID))
		Expect(otherRef.Digest).To(Equal(ref.Digest))

		_, found, err := factory.GetAuthorized(ctx, other.ID(), ref.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())
		_, found, err = factory.GetAuthorized(ctx, other.ID(), otherRef.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())

		alice, err := factory.Pin(ctx, lease, other.ID(), "alice", otherRef, "investigation")
		Expect(err).NotTo(HaveOccurred())
		bob, err := factory.Pin(ctx, lease, other.ID(), "bob", otherRef, "audit")
		Expect(err).NotTo(HaveOccurred())
		Expect(alice.ID).NotTo(Equal(bob.ID))
		retriedBob, err := factory.Pin(ctx, lease, other.ID(), "bob", otherRef, "audit")
		Expect(err).NotTo(HaveOccurred())
		Expect(retriedBob.ID).To(Equal(bob.ID))
		_, err = factory.Pin(ctx, lease, other.ID(), "bob", otherRef, "different immutable reason")
		Expect(err).To(MatchError(ContainSubstring("pin conflicts")))
		defaultAlice, err := factory.Pin(ctx, lease, defaultTeam.ID(), "alice", ref, "local investigation")
		Expect(err).NotTo(HaveOccurred())
		Expect(defaultAlice.ID).NotTo(Equal(alice.ID))

		Expect(factory.Unpin(ctx, lease, other.ID(), "alice", otherRef)).To(Succeed())
		var claims int
		Expect(dbConn.QueryRow(`
			SELECT count(*) FROM agent_snapshot_retention_claims
			WHERE snapshot_id = $1 AND team_id = $2 AND class = 'pin'
		`, int64(otherRef.ID), other.ID()).Scan(&claims)).To(Succeed())
		Expect(claims).To(Equal(1))
		Expect(dbConn.QueryRow(`
			SELECT count(*) FROM agent_snapshot_retention_claims
			WHERE snapshot_id = $1 AND class = 'pin' AND actor = 'alice'
		`, int64(ref.ID)).Scan(&claims)).To(Succeed())
		Expect(claims).To(Equal(1), "unpinning the other team must preserve the default team's same-actor claim")
	})

	It("does not revive another team’s expired equal-digest snapshot", func() {
		value := digest("d")
		staged := stage(value, defaultTeam.ID(), "default-owner")
		ref := seal(newBuild(defaultTeam.ID()), "default-owner", nil, nil, []snapshot.SealCommitOutput{
			output("default", "value", "opaque/v1", value, staged),
		})["default"].Snapshot

		other, err := teamFactory.CreateTeam(structTeam("other-revival-team"))
		Expect(err).NotTo(HaveOccurred())
		otherStage := stage(value, other.ID(), "other-owner")
		otherOutput := output("other", "value", "opaque/v1", value, otherStage)
		otherSealed, err := factory.CommitSealBatch(ctx, lease, snapshot.SealCommit{
			Context: snapshot.SealCommitContext{
				TeamID: other.ID(), TeamName: other.Name(), CreatedBy: "alice",
				Build: &snapshot.BuildOccurrence{
					BuildID: newBuild(other.ID()), PlanID: "plan-other", Attempt: "other-owner",
					StepKind: "task", StepName: "produce",
				},
				Inputs: map[string]snapshot.SnapshotRef{}, InputOrder: []string{},
				ExpectedOutputs: []snapshot.Port{otherOutput.Port},
			},
			Outputs: []snapshot.SealCommitOutput{otherOutput},
		})
		Expect(err).NotTo(HaveOccurred())
		otherRef := otherSealed["other"].Snapshot

		_, err = dbConn.Exec(`UPDATE agent_snapshots SET content_state = 'expired' WHERE id IN ($1, $2)`, int64(ref.ID), int64(otherRef.ID))
		Expect(err).NotTo(HaveOccurred())
		reupload := stage(value, defaultTeam.ID(), "default-revive")
		seal(newBuild(defaultTeam.ID()), "default-revive", nil, nil, []snapshot.SealCommitOutput{
			output("revive", "value", "opaque/v1", value, reupload),
		})

		var defaultState, otherState string
		Expect(dbConn.QueryRow(`SELECT content_state FROM agent_snapshots WHERE id = $1`, int64(ref.ID)).Scan(&defaultState)).To(Succeed())
		Expect(dbConn.QueryRow(`SELECT content_state FROM agent_snapshots WHERE id = $1`, int64(otherRef.ID)).Scan(&otherState)).To(Succeed())
		Expect(defaultState).To(Equal("available"))
		Expect(otherState).To(Equal("expired"))
		persisted, found, err := factory.GetAuthorized(ctx, other.ID(), otherRef.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(persisted.ContentState).To(Equal(snapshot.ContentStateExpired))
	})

	It("allows an actor to release its pin after expiry while rejecting a new expired pin", func() {
		value := digest("c")
		staged := stage(value, defaultTeam.ID(), "expired-pin")
		ref := seal(newBuild(defaultTeam.ID()), "expired-pin", nil, nil, []snapshot.SealCommitOutput{
			output("value", "value", "opaque/v1", value, staged),
		})["value"].Snapshot
		_, err := factory.Pin(ctx, lease, defaultTeam.ID(), "alice", ref, "release after inspection")
		Expect(err).NotTo(HaveOccurred())
		_, err = dbConn.Exec(`UPDATE agent_snapshots SET content_state = 'expired' WHERE id = $1`, int64(ref.ID))
		Expect(err).NotTo(HaveOccurred())

		_, err = factory.Pin(ctx, lease, defaultTeam.ID(), "bob", ref, "too late")
		Expect(errors.Is(err, snapshot.ErrExpired)).To(BeTrue(), "error: %v", err)
		Expect(factory.Unpin(ctx, lease, defaultTeam.ID(), "alice", ref)).To(Succeed())
		var pins int
		Expect(dbConn.QueryRow(`
			SELECT count(*) FROM agent_snapshot_retention_claims
			WHERE snapshot_id = $1 AND team_id = $2 AND class = 'pin' AND actor = 'alice'
		`, int64(ref.ID), defaultTeam.ID()).Scan(&pins)).To(Succeed())
		Expect(pins).To(Equal(0))
	})

	It("returns authorization-filtered detail without leaking another team's provenance or claims", func() {
		value := digest("b")
		staged := stage(value, defaultTeam.ID(), "detail")
		ref := seal(newBuild(defaultTeam.ID()), "detail", nil, nil, []snapshot.SealCommitOutput{
			output("value", "value", "opaque/v1", value, staged),
		})["value"].Snapshot

		other, err := teamFactory.CreateTeam(structTeam(fmt.Sprintf("other-snapshot-detail-%d", time.Now().UnixNano())))
		Expect(err).NotTo(HaveOccurred())
		var otherID int64
		Expect(dbConn.QueryRow(`
			INSERT INTO agent_snapshots
				(team_id, type_name, type_version, digest, byte_size, file_count, representation, intrinsic_metadata)
			VALUES ($1, 'opaque', 1, $2, 128, 2, 'application/vnd.jetbridge.snapshot.tar.v1', '{"tree":"abc"}')
			RETURNING id
		`, other.ID(), value.String()).Scan(&otherID)).To(Succeed())
		_, err = dbConn.Exec(`
			INSERT INTO agent_snapshot_retention_claims
				(snapshot_id, team_id, class, actor, reason)
			VALUES ($1, $2, 'pin', 'other-secret-actor', 'other secret reason')
		`, otherID, other.ID())
		Expect(err).NotTo(HaveOccurred())
		_, err = dbConn.Exec(`
			INSERT INTO agent_snapshot_productions
				(snapshot_id, occurrence_kind, team_id, team_name, created_by,
				 upload_idempotency_key, source_metadata)
			VALUES ($1, 'upload', $2, $3, 'bob', 'other-secret-key', '{"adapter":"other-secret"}')
		`, otherID, other.ID(), other.Name())
		Expect(err).NotTo(HaveOccurred())

		detail, found, err := factory.GetAuthorizedDetail(ctx, defaultTeam.ID(), ref.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(detail.Manifest.ID).To(Equal(ref.ID))
		Expect(detail.ReplicaCount).To(Equal(1))
		Expect(detail.RetentionClaims).To(HaveLen(1))
		Expect(detail.RetentionClaims[0].Actor).NotTo(Equal("other-secret-actor"))
		Expect(detail.Productions).To(HaveLen(1))
		Expect(detail.Productions[0].Kind).To(Equal(snapshot.ProductionKindBuild))
		Expect(detail.Productions[0].CreatedBy).To(Equal("alice"))
		Expect(detail.Downstream).To(BeEmpty())

		_, found, err = factory.GetAuthorizedDetail(ctx, other.ID(), ref.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())
		otherDetail, found, err := factory.GetAuthorizedDetail(ctx, other.ID(), snapshot.SnapshotID(otherID))
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(otherDetail.RetentionClaims).To(HaveLen(1))
		Expect(otherDetail.RetentionClaims[0].Actor).To(Equal("other-secret-actor"))
		Expect(otherDetail.Productions).To(HaveLen(1))
		Expect(otherDetail.Productions[0].Kind).To(Equal(snapshot.ProductionKindUpload))
		Expect(otherDetail.Productions[0].Build).To(BeNil())
		Expect(otherDetail.Productions[0].OutputPort).To(BeEmpty())
		Expect(string(otherDetail.Productions[0].SourceMetadata)).To(MatchJSON(`{"adapter":"other-secret"}`))

		_, found, err = factory.GetAuthorizedDetail(ctx, other.ID()+99999, ref.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())
	})

	It("discovers lifecycle candidates with a stable cursor and mutates locations idempotently", func() {
		orphanDigest := digest("3")
		orphanStage := stage(orphanDigest, defaultTeam.ID(), "orphan")
		_, err := dbConn.Exec(`
			UPDATE agent_snapshot_staged_uploads
			SET created_at = now() - interval '2 hours',
			    lease_expires_at = now() - interval '1 hour'
			WHERE id = $1
		`, orphanStage.ID)
		Expect(err).NotTo(HaveOccurred())

		committedWithStaleStageDigest := digest("5")
		committedStage := stage(committedWithStaleStageDigest, defaultTeam.ID(), "committed")
		seal(newBuild(defaultTeam.ID()), "committed", nil, nil, []snapshot.SealCommitOutput{
			output("committed", "result", "opaque/v1", committedWithStaleStageDigest, committedStage),
		})
		staleCommittedRow := stage(committedWithStaleStageDigest, defaultTeam.ID(), "post-commit-stale-row")
		_, err = dbConn.Exec(`
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

		expiryDigest := digest("7")
		expiryStage := stage(expiryDigest, defaultTeam.ID(), "expiry")
		expiry := seal(newBuild(defaultTeam.ID()), "expiry", nil, nil, []snapshot.SealCommitOutput{
			output("expiry", "result", "opaque/v1", expiryDigest, expiryStage),
		})["expiry"].Snapshot
		_, err = dbConn.Exec(`
			UPDATE agent_snapshot_retention_claims
			SET expires_at = now() - interval '1 hour'
			WHERE snapshot_id = $1
		`, int64(expiry.ID))
		Expect(err).NotTo(HaveOccurred())

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
			snapshot.LifecycleCandidate{Digest: committedWithStaleStageDigest, Kind: snapshot.LifecycleCandidateRepair},
			snapshot.LifecycleCandidate{Digest: failedRecaptureDigest, Kind: snapshot.LifecycleCandidateOrphan},
			snapshot.LifecycleCandidate{Digest: repairDigest, Kind: snapshot.LifecycleCandidateRepair},
			snapshot.LifecycleCandidate{Digest: expiryDigest, Kind: snapshot.LifecycleCandidateExpiry},
		))

		Expect(factory.AddLocation(ctx, lease, location)).To(Succeed())
		Expect(factory.AddLocation(ctx, lease, location)).To(Succeed())
		locations, err := factory.LocationsForDigest(ctx, repairDigest)
		Expect(err).NotTo(HaveOccurred())
		Expect(locations).To(ConsistOf(location))
	})

	Describe("real PostgreSQL digest-lock barriers", func() {
		BeforeEach(func() {
			Expect(lease.Close()).To(Succeed())
			lease = nil
			dbConn.SetMaxOpenConns(8)
		})

		makeUnretained := func(value snapshot.Digest, attempt string) (snapshot.SnapshotRef, snapshot.Location) {
			setupLease, err := db.NewAgentSnapshotDigestLocker(dbConn).AcquireMany(ctx, []snapshot.Digest{value})
			Expect(err).NotTo(HaveOccurred())
			lease = setupLease
			defer func() {
				Expect(setupLease.Close()).To(Succeed())
				lease = nil
			}()
			staged := stage(value, defaultTeam.ID(), attempt)
			ref := seal(newBuild(defaultTeam.ID()), attempt, nil, nil, []snapshot.SealCommitOutput{
				output(attempt, "result", "opaque/v1", value, staged),
			})[attempt].Snapshot
			_, err = dbConn.Exec(`
				UPDATE agent_snapshot_retention_claims
				SET expires_at = now() - interval '1 hour'
				WHERE snapshot_id = $1
			`, int64(ref.ID))
			Expect(err).NotTo(HaveOccurred())
			locations, err := factory.LocationsForDigest(ctx, value)
			Expect(err).NotTo(HaveOccurred())
			Expect(locations).To(HaveLen(1))
			Expect(factory.RemoveLocation(ctx, lease, locations[0])).To(Succeed())
			return ref, locations[0]
		}

		It("orders orphan GC before a new seal for the same digest", func() {
			value := digest("1")
			setupLease, err := db.NewAgentSnapshotDigestLocker(dbConn).AcquireMany(ctx, []snapshot.Digest{value})
			Expect(err).NotTo(HaveOccurred())
			lease = setupLease
			stale := stage(value, defaultTeam.ID(), "abandoned")
			Expect(setupLease.Close()).To(Succeed())
			lease = nil
			_, err = dbConn.Exec(`
				UPDATE agent_snapshot_staged_uploads
				SET created_at = now() - interval '2 hours',
				    lease_expires_at = now() - interval '1 hour'
				WHERE id = $1
			`, stale.ID)
			Expect(err).NotTo(HaveOccurred())

			manager := db.NewAgentSnapshotDigestLocker(dbConn)
			gcLease, err := manager.AcquireMany(ctx, []snapshot.Digest{value})
			Expect(err).NotTo(HaveOccurred())
			defer gcLease.Close()

			sealerPID, sealerResult := acquireObservedDigestLease(ctx, dbConn, value)
			waitForAdvisoryLockWaiter(dbConn, receiveBackendPID(sealerPID))

			var stageCount int
			Expect(dbConn.QueryRow(`SELECT count(*) FROM agent_snapshot_staged_uploads WHERE digest = $1`, value).Scan(&stageCount)).To(Succeed())
			Expect(stageCount).To(Equal(1), "the waiting sealer must not stage before GC releases the digest")
			Expect(factory.RemoveStagedUploads(ctx, gcLease, value, []int64{stale.ID})).To(Succeed())
			Expect(gcLease.Close()).To(Succeed())

			sealerLease := receiveDigestLease(sealerResult)
			defer sealerLease.Close()
			fresh, err := factory.StageUpload(ctx, sealerLease, snapshot.StageUploadRequest{
				Digest: value, TeamID: defaultTeam.ID(), Attempt: "after-gc",
				LeaseExpiresAt: time.Now().Add(time.Hour),
			})
			Expect(err).NotTo(HaveOccurred())
			candidate := output("after-gc", "result", "opaque/v1", value, fresh)
			_, err = factory.CommitSealBatch(ctx, sealerLease, snapshot.SealCommit{
				Context: snapshot.SealCommitContext{
					TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(), CreatedBy: "alice",
					Build: &snapshot.BuildOccurrence{
						BuildID: newBuild(defaultTeam.ID()), PlanID: "plan-1", Attempt: "after-gc", StepKind: "task", StepName: "produce",
					},
					ExpectedOutputs: []snapshot.Port{candidate.Port},
				},
				Outputs: []snapshot.SealCommitOutput{candidate},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(dbConn.QueryRow(`SELECT count(*) FROM agent_snapshot_staged_uploads WHERE digest = $1`, value).Scan(&stageCount)).To(Succeed())
			Expect(stageCount).To(Equal(0))
		})

		It("holds the seal lock from staging through commit before lifecycle recheck", func() {
			value := digest("2")
			manager := db.NewAgentSnapshotDigestLocker(dbConn)
			sealerLease, err := manager.AcquireMany(ctx, []snapshot.Digest{value})
			Expect(err).NotTo(HaveOccurred())
			defer sealerLease.Close()

			staged, err := factory.StageUpload(ctx, sealerLease, snapshot.StageUploadRequest{
				Digest: value, TeamID: defaultTeam.ID(), Attempt: "seal-before-gc",
				LeaseExpiresAt: time.Now().Add(time.Hour),
			})
			Expect(err).NotTo(HaveOccurred())

			gcPID, gcResult := acquireObservedDigestLease(ctx, dbConn, value)
			waitForAdvisoryLockWaiter(dbConn, receiveBackendPID(gcPID))

			candidate := output("seal-before-gc", "result", "opaque/v1", value, staged)
			_, err = factory.CommitSealBatch(ctx, sealerLease, snapshot.SealCommit{
				Context: snapshot.SealCommitContext{
					TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(), CreatedBy: "alice",
					Build: &snapshot.BuildOccurrence{
						BuildID: newBuild(defaultTeam.ID()), PlanID: "plan-1", Attempt: "seal-before-gc", StepKind: "task", StepName: "produce",
					},
					ExpectedOutputs: []snapshot.Port{candidate.Port},
				},
				Outputs: []snapshot.SealCommitOutput{candidate},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(sealerLease.Close()).To(Succeed())

			gcLease := receiveDigestLease(gcResult)
			defer gcLease.Close()
			state, err := factory.DigestState(ctx, gcLease, value, time.Now())
			Expect(err).NotTo(HaveOccurred())
			Expect(state.Snapshots).To(HaveLen(1))
			Expect(state.Stages).To(BeEmpty())
			Expect(state.HasActiveRetention).To(BeTrue())
			expired, err := factory.MarkDigestExpired(ctx, gcLease, value, time.Now())
			Expect(err).NotTo(HaveOccurred())
			Expect(expired).To(BeFalse(), "GC must preserve the claim committed before the sealer released the digest")
		})

		It("serializes two sealers and lets the second reuse committed digest state", func() {
			value := digest("3")
			manager := db.NewAgentSnapshotDigestLocker(dbConn)
			firstLease, err := manager.AcquireMany(ctx, []snapshot.Digest{value})
			Expect(err).NotTo(HaveOccurred())
			defer firstLease.Close()

			firstStage, err := factory.StageUpload(ctx, firstLease, snapshot.StageUploadRequest{
				Digest: value, TeamID: defaultTeam.ID(), Attempt: "first-sealer",
				LeaseExpiresAt: time.Now().Add(time.Hour),
			})
			Expect(err).NotTo(HaveOccurred())
			secondPID, secondResult := acquireObservedDigestLease(ctx, dbConn, value)
			waitForAdvisoryLockWaiter(dbConn, receiveBackendPID(secondPID))

			firstOutput := output("first-sealer", "result", "opaque/v1", value, firstStage)
			_, err = factory.CommitSealBatch(ctx, firstLease, snapshot.SealCommit{
				Context: snapshot.SealCommitContext{
					TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(), CreatedBy: "alice",
					Build: &snapshot.BuildOccurrence{
						BuildID: newBuild(defaultTeam.ID()), PlanID: "plan-1", Attempt: "first-sealer", StepKind: "task", StepName: "produce",
					},
					ExpectedOutputs: []snapshot.Port{firstOutput.Port},
				},
				Outputs: []snapshot.SealCommitOutput{firstOutput},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(firstLease.Close()).To(Succeed())

			secondLease := receiveDigestLease(secondResult)
			defer secondLease.Close()
			committed, err := factory.DigestState(ctx, secondLease, value, time.Now())
			Expect(err).NotTo(HaveOccurred())
			Expect(committed.Snapshots).To(HaveLen(1))
			Expect(committed.Locations).To(HaveLen(1))

			secondStage, err := factory.StageUpload(ctx, secondLease, snapshot.StageUploadRequest{
				Digest: value, TeamID: defaultTeam.ID(), Attempt: "second-sealer",
				LeaseExpiresAt: time.Now().Add(time.Hour),
			})
			Expect(err).NotTo(HaveOccurred())
			secondOutput := output("second-sealer", "result", "opaque/v1", value, secondStage)
			_, err = factory.CommitSealBatch(ctx, secondLease, snapshot.SealCommit{
				Context: snapshot.SealCommitContext{
					TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(), CreatedBy: "bob",
					Build: &snapshot.BuildOccurrence{
						BuildID: newBuild(defaultTeam.ID()), PlanID: "plan-1", Attempt: "second-sealer", StepKind: "task", StepName: "produce",
					},
					ExpectedOutputs: []snapshot.Port{secondOutput.Port},
				},
				Outputs: []snapshot.SealCommitOutput{secondOutput},
			})
			Expect(err).NotTo(HaveOccurred())

			var manifests, productions, locations int
			Expect(dbConn.QueryRow(`SELECT count(*) FROM agent_snapshots WHERE digest = $1`, value).Scan(&manifests)).To(Succeed())
			Expect(dbConn.QueryRow(`SELECT count(*) FROM agent_snapshot_productions p JOIN agent_snapshots s ON s.id = p.snapshot_id WHERE s.digest = $1`, value).Scan(&productions)).To(Succeed())
			Expect(dbConn.QueryRow(`SELECT count(*) FROM agent_snapshot_locations WHERE digest = $1`, value).Scan(&locations)).To(Succeed())
			Expect(manifests).To(Equal(1))
			Expect(productions).To(Equal(2))
			Expect(locations).To(Equal(1))
		})

		It("orders a pin before GC and preserves the newly retained digest", func() {
			value := digest("4")
			ref, _ := makeUnretained(value, "pin-before-gc")
			manager := db.NewAgentSnapshotDigestLocker(dbConn)
			pinLease, err := manager.AcquireMany(ctx, []snapshot.Digest{value})
			Expect(err).NotTo(HaveOccurred())
			defer pinLease.Close()
			_, err = factory.Pin(ctx, pinLease, defaultTeam.ID(), "release-manager", ref, "hold for release")
			Expect(err).NotTo(HaveOccurred())

			gcPID, gcResult := acquireObservedDigestLease(ctx, dbConn, value)
			waitForAdvisoryLockWaiter(dbConn, receiveBackendPID(gcPID))
			Expect(pinLease.Close()).To(Succeed())

			gcLease := receiveDigestLease(gcResult)
			defer gcLease.Close()
			state, err := factory.DigestState(ctx, gcLease, value, time.Now())
			Expect(err).NotTo(HaveOccurred())
			Expect(state.HasActiveRetention).To(BeTrue())
			expired, err := factory.MarkDigestExpired(ctx, gcLease, value, time.Now())
			Expect(err).NotTo(HaveOccurred())
			Expect(expired).To(BeFalse())
		})

		It("orders GC before a pin and rejects the pin after expiry", func() {
			value := digest("5")
			ref, _ := makeUnretained(value, "gc-before-pin")
			manager := db.NewAgentSnapshotDigestLocker(dbConn)
			gcLease, err := manager.AcquireMany(ctx, []snapshot.Digest{value})
			Expect(err).NotTo(HaveOccurred())
			defer gcLease.Close()

			pinPID, pinResult := acquireObservedDigestLease(ctx, dbConn, value)
			waitForAdvisoryLockWaiter(dbConn, receiveBackendPID(pinPID))
			expired, err := factory.MarkDigestExpired(ctx, gcLease, value, time.Now())
			Expect(err).NotTo(HaveOccurred())
			Expect(expired).To(BeTrue())
			Expect(gcLease.Close()).To(Succeed())

			pinLease := receiveDigestLease(pinResult)
			defer pinLease.Close()
			_, err = factory.Pin(ctx, pinLease, defaultTeam.ID(), "release-manager", ref, "too late")
			Expect(errors.Is(err, snapshot.ErrExpired)).To(BeTrue(), "error: %v", err)
		})

		It("prevents repair from adding a location during the final expiry transition", func() {
			value := digest("6")
			_, location := makeUnretained(value, "expiry-before-repair")
			manager := db.NewAgentSnapshotDigestLocker(dbConn)
			expiryLease, err := manager.AcquireMany(ctx, []snapshot.Digest{value})
			Expect(err).NotTo(HaveOccurred())
			defer expiryLease.Close()

			repairPID, repairResult := acquireObservedDigestLease(ctx, dbConn, value)
			waitForAdvisoryLockWaiter(dbConn, receiveBackendPID(repairPID))
			expired, err := factory.MarkDigestExpired(ctx, expiryLease, value, time.Now())
			Expect(err).NotTo(HaveOccurred())
			Expect(expired).To(BeTrue())
			Expect(expiryLease.Close()).To(Succeed())

			repairLease := receiveDigestLease(repairResult)
			defer repairLease.Close()
			err = factory.AddLocation(ctx, repairLease, location)
			Expect(err).To(MatchError(ContainSubstring("without an available snapshot manifest")))
			locations, err := factory.LocationsForDigest(ctx, value)
			Expect(err).NotTo(HaveOccurred())
			Expect(locations).To(BeEmpty())
		})

		It("releases an earlier digest lock when a later acquisition is cancelled", func() {
			freeDigest, blockedDigest := digest("7"), digest("8")
			manager := db.NewAgentSnapshotDigestLocker(dbConn)
			blocker, err := manager.AcquireMany(ctx, []snapshot.Digest{blockedDigest})
			Expect(err).NotTo(HaveOccurred())
			defer blocker.Close()

			blockedCtx, cancel := context.WithCancel(ctx)
			waiterPID, waiterResult := acquireObservedDigestLease(blockedCtx, dbConn, blockedDigest, freeDigest)
			waitForAdvisoryLockWaiter(dbConn, receiveBackendPID(waiterPID))
			cancel()

			var partial observedDigestLeaseResult
			Eventually(waiterResult).WithTimeout(5 * time.Second).Should(Receive(&partial))
			Expect(partial.err).To(HaveOccurred())
			Expect(partial.lease).NotTo(BeNil())
			Expect(partial.lease.Covers(freeDigest)).To(BeTrue())
			Expect(partial.lease.Covers(blockedDigest)).To(BeFalse())
			Expect(partial.lease.Close()).To(Succeed())
			Expect(blocker.Close()).To(Succeed())

			probe, err := manager.AcquireMany(ctx, []snapshot.Digest{blockedDigest, freeDigest})
			Expect(err).NotTo(HaveOccurred())
			Expect(probe.Close()).To(Succeed())
		})
	})
})

func structTeam(name string) atc.Team { return atc.Team{Name: name} }

type observedDigestConn struct {
	db.DbConn
	backendPID chan<- int
}

func (conn observedDigestConn) Conn(ctx context.Context) (*sql.Conn, error) {
	dedicated, err := conn.DbConn.Conn(ctx)
	if err != nil {
		return nil, err
	}
	var pid int
	if err := dedicated.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		dedicated.Close()
		return nil, err
	}
	conn.backendPID <- pid
	return dedicated, nil
}

type observedDigestLeaseResult struct {
	lease snapshot.DigestLease
	err   error
}

func acquireObservedDigestLease(
	ctx context.Context,
	conn db.DbConn,
	digests ...snapshot.Digest,
) (<-chan int, <-chan observedDigestLeaseResult) {
	backendPID := make(chan int, 1)
	result := make(chan observedDigestLeaseResult, 1)
	manager := db.NewAgentSnapshotDigestLocker(observedDigestConn{
		DbConn: conn, backendPID: backendPID,
	})
	go func() {
		lease, err := manager.AcquireMany(ctx, digests)
		result <- observedDigestLeaseResult{lease: lease, err: err}
	}()
	return backendPID, result
}

func receiveBackendPID(pids <-chan int) int {
	var pid int
	Eventually(pids).WithTimeout(5 * time.Second).Should(Receive(&pid))
	return pid
}

func waitForAdvisoryLockWaiter(conn db.DbConn, pid int) {
	Eventually(func() (bool, error) {
		var waiting bool
		err := conn.QueryRow(`
			SELECT EXISTS (
				SELECT 1 FROM pg_locks
				WHERE pid = $1 AND locktype = 'advisory' AND NOT granted
			)
		`, pid).Scan(&waiting)
		return waiting, err
	}).WithTimeout(5 * time.Second).Should(BeTrue())
}

func receiveDigestLease(results <-chan observedDigestLeaseResult) snapshot.DigestLease {
	var result observedDigestLeaseResult
	Eventually(results).WithTimeout(5 * time.Second).Should(Receive(&result))
	Expect(result.err).NotTo(HaveOccurred())
	Expect(result.lease).NotTo(BeNil())
	return result.lease
}

// forgedDigestLease is a lease the database lock manager never issued. It
// claims to cover every digest, which is the point: coverage it asserts about
// itself must not be enough to stage an upload.
type forgedDigestLease struct{}

func (forgedDigestLease) Covers(snapshot.Digest) bool { return true }
func (forgedDigestLease) Close() error                { return nil }
