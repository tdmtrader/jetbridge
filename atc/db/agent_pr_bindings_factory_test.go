package db_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/pullrequest"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflowwait"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("AgentPRBindingsFactory", func() {
	var (
		ctx            context.Context
		factory        pullrequest.BindingStore
		definitionID   int
		definitionName string
		createRequest  pullrequest.CreateBinding
		binding        pullrequest.Binding
	)

	BeforeEach(func() {
		ctx = context.Background()
		factory = db.NewAgentPRBindingsFactory(dbConn)
		suffix := time.Now().UnixNano()
		definitionName = fmt.Sprintf("pr-monitor-%d", suffix)
		Expect(dbConn.QueryRow(`
			INSERT INTO agent_workflow_definitions
				(definition_kind, name, version, content_hash, definition,
				 created_by, schema_version, signature_version)
			VALUES ('workflow', $1, 3, $2, 'schema_version: 3', 'test', 3, 1)
			RETURNING id
		`, definitionName, strings.Repeat("a", 64)).Scan(&definitionID)).To(Succeed())
		target := agentPRBindingPublicationTarget{
			Locator: pullrequest.Locator{
				Provider: pullrequest.ProviderGitHub, Repository: "example/repository",
				ExternalID: "118",
			},
			URL:                   "https://github.example/example/repository/pull/118",
			SourceRef:             "refs/heads/feature/pr",
			SourceSHA:             strings.Repeat("b", 40),
			TargetRef:             "refs/heads/main",
			TargetSHA:             strings.Repeat("c", 40),
			Destination:           "github.example/example/repository",
			ApprovalPolicyVersion: "engineering/v3",
		}
		originRunID, acceptedOccurrenceID, creationOccurrenceID, observationID :=
			insertAgentPRBindingEvidence(
				defaultTeam.ID(), defaultTeam.Name(), definitionID, fmt.Sprint(suffix),
				target,
			)
		reconciledAt := time.Now().UTC().Truncate(time.Microsecond)
		createRequest = pullrequest.CreateBinding{
			TeamID:                           defaultTeam.ID(),
			Locator:                          target.Locator,
			URL:                              target.URL,
			SourceRef:                        target.SourceRef,
			TargetRef:                        target.TargetRef,
			Destination:                      target.Destination,
			ApprovalPolicyVersion:            target.ApprovalPolicyVersion,
			OriginatingWorkflowRunID:         snapshot.WorkflowRunID(originRunID),
			OriginatingPublicationOccurrence: acceptedOccurrenceID,
			CreationPublicationOccurrenceID:  creationOccurrenceID,
			MonitorWorkflowDefinitionID:      definitionID,
			MonitorWorkflowVersion:           3,
			AcknowledgedCursor:               pullrequest.Cursor(" \t{\"opaque\":[1,\"雪\"]}\n"),
			LastObservationSnapshotID:        snapshot.SnapshotID(observationID),
			LastReconciledSourceSHA:          target.SourceSHA,
			LastReconciledTargetSHA:          target.TargetSHA,
			LastReconciledAt:                 reconciledAt,
		}
		var created bool
		var err error
		binding, created, err = factory.Create(ctx, createRequest)
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())
	})

	It("rejects a merely same-run publication occurrence as binding authority", func() {
		request := createRequest
		request.Locator.ExternalID = "same-run-is-not-authority"
		request.URL = "https://github.example/example/repository/pull/same-run-is-not-authority"

		_, _, err := factory.Create(ctx, request)
		Expect(errors.Is(err, pullrequest.ErrBindingConflict)).To(BeTrue())
	})

	It("does not reinterpret creation proof as the original accepted-review authority", func() {
		request := createRequest
		request.Locator.ExternalID = "creation-is-not-review-authority"
		request.URL = "https://github.example/example/repository/pull/creation-is-not-review-authority"
		request.OriginatingPublicationOccurrence =
			request.CreationPublicationOccurrenceID

		_, _, err := factory.Create(ctx, request)
		Expect(errors.Is(err, pullrequest.ErrBindingConflict)).To(BeTrue())
	})

	It("requires creation proof from the exact originating workflow run", func() {
		request := createRequest
		request.Locator.ExternalID = "cross-run-creation"
		request.URL = "https://github.example/example/repository/pull/cross-run-creation"
		_, _, crossRunCreationID, _ := insertAgentPRBindingEvidence(
			request.TeamID, defaultTeam.Name(), definitionID,
			"cross-run-creation",
			agentPRBindingPublicationTarget{
				Locator: request.Locator, URL: request.URL,
				SourceRef: request.SourceRef, SourceSHA: request.LastReconciledSourceSHA,
				TargetRef: request.TargetRef, TargetSHA: request.LastReconciledTargetSHA,
				Destination:           request.Destination,
				ApprovalPolicyVersion: request.ApprovalPolicyVersion,
			},
		)
		request.CreationPublicationOccurrenceID = crossRunCreationID

		_, _, err := factory.Create(ctx, request)
		Expect(errors.Is(err, pullrequest.ErrBindingConflict)).To(BeTrue())
	})

	It("is team scoped, idempotent, locator unique, and preserves opaque cursor bytes", func() {
		Expect(binding.AcknowledgedCursor).To(Equal(createRequest.AcknowledgedCursor))
		Expect([]byte(binding.AcknowledgedCursor)).To(Equal([]byte(createRequest.AcknowledgedCursor)))
		Expect(binding.Destination).To(Equal(createRequest.Destination))
		Expect(binding.ApprovalPolicyVersion).To(Equal(createRequest.ApprovalPolicyVersion))
		Expect(binding.CreationPublicationOccurrenceID).NotTo(BeNil())
		Expect(*binding.CreationPublicationOccurrenceID).To(Equal(
			createRequest.CreationPublicationOccurrenceID,
		))
		Expect(*binding.CreationPublicationOccurrenceID).NotTo(Equal(
			*binding.OriginatingPublicationOccurrence,
		))
		var baselineRepositoryID, baselineValidationID int64
		Expect(dbConn.QueryRow(`
			SELECT candidate_snapshot_id, validation_snapshot_id
			FROM agent_publication_approval_evidence
			WHERE publication_id=$1
			  AND evidence_kind='accepted_review'
		`, createRequest.OriginatingPublicationOccurrence).Scan(
			&baselineRepositoryID, &baselineValidationID,
		)).To(Succeed())
		Expect(binding.ApprovedBaselineRepositorySnapshotID).To(Equal(
			snapshot.SnapshotID(baselineRepositoryID),
		))
		Expect(binding.ApprovedBaselineValidationSnapshotID).To(Equal(
			snapshot.SnapshotID(baselineValidationID),
		))
		Expect(binding.ApprovedBaselinePublicationOccurrenceID).To(Equal(
			createRequest.OriginatingPublicationOccurrence,
		))

		replayed, created, err := factory.Create(ctx, createRequest)
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeFalse())
		Expect(replayed.ID).To(Equal(binding.ID))

		other, err := teamFactory.CreateTeam(atc.Team{Name: fmt.Sprintf("pr-binding-other-%d", time.Now().UnixNano())})
		Expect(err).NotTo(HaveOccurred())
		_, found, err := factory.Get(ctx, other.ID(), binding.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())

		crossTeam := createRequest
		crossTeam.TeamID = other.ID()
		crossTeam.Locator.ExternalID = "cross-team"
		_, _, err = factory.Create(ctx, crossTeam)
		Expect(errors.Is(err, pullrequest.ErrBindingConflict)).To(BeTrue())

		wrongVersion := createRequest
		wrongVersion.Locator.ExternalID = "wrong-monitor-version"
		wrongVersion.MonitorWorkflowVersion = 2
		_, _, err = factory.Create(ctx, wrongVersion)
		Expect(errors.Is(err, pullrequest.ErrBindingConflict)).To(BeTrue())

		var wrongSnapshotID int64
		Expect(dbConn.QueryRow(`
			INSERT INTO agent_snapshots
				(team_id, type_name, type_version, digest, byte_size, file_count, representation)
			VALUES ($1, 'repository', 1, $2, 1, 1,
			        'application/vnd.jetbridge.snapshot.tar.v1')
			RETURNING id
		`, defaultTeam.ID(), "sha256:"+strings.Repeat("8", 64)).Scan(&wrongSnapshotID)).To(Succeed())
		wrongObservation := createRequest
		wrongObservation.Locator.ExternalID = "wrong-observation-type"
		wrongObservation.LastObservationSnapshotID = snapshot.SnapshotID(wrongSnapshotID)
		_, _, err = factory.Create(ctx, wrongObservation)
		Expect(errors.Is(err, pullrequest.ErrBindingConflict)).To(BeTrue())

		conflict := createRequest
		conflict.TargetRef = "refs/heads/release"
		_, _, err = factory.Create(ctx, conflict)
		Expect(errors.Is(err, pullrequest.ErrBindingConflict)).To(BeTrue())

		_, err = dbConn.Exec(`DELETE FROM agent_workflow_runs WHERE id=$1`, int64(createRequest.OriginatingWorkflowRunID))
		Expect(err).To(HaveOccurred(), "originating workflow evidence must be immutable")
		_, err = dbConn.Exec(`DELETE FROM agent_snapshots WHERE id=$1`, int64(createRequest.LastObservationSnapshotID))
		Expect(err).To(HaveOccurred(), "sealed observation evidence must be immutable")
	})

	It("canonicalizes caller timestamps and reservation expiry to PostgreSQL precision", func() {
		nonCanonical := time.Date(
			2026, time.July, 29, 18, 17, 16, 987654321,
			time.FixedZone("test-offset", -7*60*60),
		)
		canonical := nonCanonical.UTC().Truncate(time.Microsecond)
		request := createRequest
		request.Locator.ExternalID = "timestamp-precision"
		request.LastReconciledAt = nonCanonical
		replaceAgentPRBindingOrigin(
			&request, defaultTeam.Name(), definitionID, "timestamp-precision",
		)

		createdBinding, created, err := factory.Create(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())
		Expect(createdBinding.LastReconciledAt).To(Equal(canonical))
		Expect(createdBinding.LastReconciledAt.Location()).To(Equal(time.UTC))

		replayedBinding, created, err := factory.Create(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeFalse())
		Expect(replayedBinding.ID).To(Equal(createdBinding.ID))
		Expect(replayedBinding.LastReconciledAt).To(Equal(canonical))

		reservationRequest := pullrequest.ReserveLaunch{
			TeamID: createdBinding.TeamID, BindingID: createdBinding.ID,
			ExpectedRevision:      createdBinding.Revision,
			ActionDigest:          "sha256:" + strings.Repeat("7", 64),
			ObservationSnapshotID: request.LastObservationSnapshotID,
			Cursor:                pullrequest.Cursor(`{"batch":"precision"}`),
			SourceSHA:             strings.Repeat("7", 40),
			TargetSHA:             strings.Repeat("8", 40),
			ExpiresIn:             5*time.Minute + 321*time.Nanosecond,
		}
		reservation, reserved, err := factory.ReserveLaunch(ctx, reservationRequest)
		Expect(err).NotTo(HaveOccurred())
		Expect(reserved).To(BeTrue())
		Expect(reservation.ExpiresAt.Location()).To(Equal(time.UTC))
		Expect(reservation.ExpiresAt.Nanosecond() % int(time.Microsecond)).To(BeZero())

		stored, found, err := factory.Get(ctx, createdBinding.TeamID, createdBinding.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(stored.Active).NotTo(BeNil())
		Expect(stored.Active.ExpiresAt).To(Equal(reservation.ExpiresAt))

		replayed, reserved, err := factory.ReserveLaunch(ctx, reservationRequest)
		Expect(err).NotTo(HaveOccurred())
		Expect(reserved).To(BeTrue())
		Expect(replayed.ExpiresAt).To(Equal(reservation.ExpiresAt))
	})

	It("serializes reservations, replays exactly, recovers expiry, and rejects stale revisions", func() {
		base := pullrequest.ReserveLaunch{
			TeamID: binding.TeamID, BindingID: binding.ID,
			ExpectedRevision:      binding.Revision,
			ObservationSnapshotID: createRequest.LastObservationSnapshotID,
			Cursor:                pullrequest.Cursor(`{"batch":"next"}`),
			SourceSHA:             strings.Repeat("d", 40),
			TargetSHA:             strings.Repeat("e", 40),
			ExpiresIn:             5 * time.Minute,
		}
		first := base
		first.ActionDigest = "sha256:" + strings.Repeat("1", 64)
		second := base
		second.ActionDigest = "sha256:" + strings.Repeat("2", 64)
		type result struct {
			request     pullrequest.ReserveLaunch
			reservation pullrequest.LaunchReservation
			reserved    bool
			err         error
		}
		results := make(chan result, 2)
		for _, request := range []pullrequest.ReserveLaunch{first, second} {
			request := request
			go func() {
				reservation, reserved, err := factory.ReserveLaunch(ctx, request)
				results <- result{request, reservation, reserved, err}
			}()
		}
		claims := []result{<-results, <-results}
		var winner result
		reservedCount := 0
		for _, claim := range claims {
			Expect(claim.err).NotTo(HaveOccurred())
			if claim.reserved {
				reservedCount++
				winner = claim
			}
		}
		Expect(reservedCount).To(Equal(1))

		replayed, reserved, err := factory.ReserveLaunch(ctx, winner.request)
		Expect(err).NotTo(HaveOccurred())
		Expect(reserved).To(BeTrue())
		Expect(replayed.Token).To(Equal(winner.reservation.Token))

		_, err = dbConn.Exec(`
			UPDATE agent_pr_bindings
			SET active_reservation_expires_at=clock_timestamp()-interval '1 second'
			WHERE id=$1
		`, binding.ID)
		Expect(err).NotTo(HaveOccurred())
		reclaimed, reserved, err := factory.ReserveLaunch(ctx, winner.request)
		Expect(err).NotTo(HaveOccurred())
		Expect(reserved).To(BeTrue())
		Expect(reclaimed.Token).NotTo(Equal(winner.reservation.Token))
		Expect(reclaimed.BaseRevision).To(Equal(winner.reservation.BaseRevision))
		Expect(reclaimed.BindingRevision).To(Equal(
			winner.reservation.BindingRevision,
		))

		_, err = dbConn.Exec(`
			UPDATE agent_pr_bindings
			SET active_reservation_expires_at=clock_timestamp()-interval '1 second'
			WHERE id=$1
		`, binding.ID)
		Expect(err).NotTo(HaveOccurred())
		current, found, err := factory.Get(ctx, binding.TeamID, binding.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		recovery := base
		recovery.ExpectedRevision = current.Revision
		recovery.ActionDigest = "sha256:" + strings.Repeat("3", 64)
		recovered, reserved, err := factory.ReserveLaunch(ctx, recovery)
		Expect(err).NotTo(HaveOccurred())
		Expect(reserved).To(BeTrue())
		Expect(recovered.Token).NotTo(Equal(winner.reservation.Token))

		_, err = factory.Pause(ctx, pullrequest.OperatorRequest{
			TeamID: binding.TeamID, BindingID: binding.ID,
			ExpectedRevision: current.Revision,
		})
		Expect(errors.Is(err, pullrequest.ErrStaleBindingRevision)).To(BeTrue())
	})

	It("releases only the exact unattached reservation and permits a fresh recovery", func() {
		reservationRequest := pullrequest.ReserveLaunch{
			TeamID: binding.TeamID, BindingID: binding.ID,
			ExpectedRevision:      binding.Revision,
			ActionDigest:          "sha256:" + strings.Repeat("5", 64),
			ObservationSnapshotID: createRequest.LastObservationSnapshotID,
			Cursor:                pullrequest.Cursor(`{"batch":"unattached-release"}`),
			SourceSHA:             strings.Repeat("5", 40),
			TargetSHA:             strings.Repeat("6", 40),
			ExpiresIn:             5 * time.Minute,
		}
		reservation, reserved, err := factory.ReserveLaunch(ctx, reservationRequest)
		Expect(err).NotTo(HaveOccurred())
		Expect(reserved).To(BeTrue())

		release := pullrequest.ReleaseLaunch{
			TeamID: binding.TeamID, BindingID: binding.ID,
			ExpectedRevision: reservation.BindingRevision,
			ActionDigest:     reservation.ActionDigest,
			ReservationToken: reservation.Token,
		}
		wrongToken := release
		wrongToken.ReservationToken = "wrong-token"
		_, err = factory.ReleaseLaunch(ctx, wrongToken)
		Expect(errors.Is(err, pullrequest.ErrReservationMismatch)).To(BeTrue())

		wrongRunID := snapshot.WorkflowRunID(99118)
		wrongRun := release
		wrongRun.WorkflowRunID = &wrongRunID
		_, err = factory.ReleaseLaunch(ctx, wrongRun)
		Expect(errors.Is(err, pullrequest.ErrReservationMismatch)).To(BeTrue())

		stale := release
		stale.ExpectedRevision = reservation.BaseRevision
		_, err = factory.ReleaseLaunch(ctx, stale)
		Expect(errors.Is(err, pullrequest.ErrStaleBindingRevision)).To(BeTrue())

		released, err := factory.ReleaseLaunch(ctx, release)
		Expect(err).NotTo(HaveOccurred())
		Expect(released.Active).To(BeNil())
		Expect(released.Revision).To(Equal(reservation.BindingRevision + 1))

		_, err = factory.ReleaseLaunch(ctx, release)
		Expect(errors.Is(err, pullrequest.ErrStaleBindingRevision)).To(BeTrue())
		current, found, err := factory.Get(ctx, binding.TeamID, binding.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(current.Active).To(BeNil())

		recoveryRequest := reservationRequest
		recoveryRequest.ExpectedRevision = released.Revision
		recoveryRequest.ActionDigest = "sha256:" + strings.Repeat("6", 64)
		recovered, reserved, err := factory.ReserveLaunch(ctx, recoveryRequest)
		Expect(err).NotTo(HaveOccurred())
		Expect(reserved).To(BeTrue())
		Expect(recovered.Token).NotTo(Equal(reservation.Token))
	})

	It("releases any exact terminal attached run without erasing audit history", func() {
		for index, status := range []string{"failed", "errored", "aborted", "succeeded"} {
			testBinding := binding
			if index > 0 {
				request := createRequest
				request.Locator.ExternalID = "release-" + status
				replaceAgentPRBindingOrigin(
					&request, defaultTeam.Name(), definitionID,
					"release-"+status,
				)
				var created bool
				var err error
				testBinding, created, err = factory.Create(ctx, request)
				Expect(err).NotTo(HaveOccurred())
				Expect(created).To(BeTrue())
			}
			reservation := reserveAgentPRBinding(
				factory, testBinding, *testBinding.LastObservationSnapshotID,
			)
			runID := insertAgentPRMonitorRun(
				defaultTeam.ID(), defaultTeam.Name(), definitionID,
				testBinding.ID, "admitting", "release-"+status,
			)
			attached, err := factory.AttachRun(ctx, pullrequest.AttachRun{
				TeamID: testBinding.TeamID, BindingID: testBinding.ID,
				ExpectedRevision: reservation.BindingRevision,
				ActionDigest:     reservation.ActionDigest,
				ReservationToken: reservation.Token,
				WorkflowRunID:    snapshot.WorkflowRunID(runID),
			})
			Expect(err).NotTo(HaveOccurred())
			_, err = dbConn.Exec(`
				UPDATE agent_workflow_runs
				SET status=$2, completed_at=clock_timestamp(), updated_at=clock_timestamp()
				WHERE id=$1
			`, runID, status)
			Expect(err).NotTo(HaveOccurred())

			run := snapshot.WorkflowRunID(runID)
			release := pullrequest.ReleaseLaunch{
				TeamID: testBinding.TeamID, BindingID: testBinding.ID,
				ExpectedRevision: attached.Revision,
				ActionDigest:     reservation.ActionDigest,
				ReservationToken: reservation.Token,
				WorkflowRunID:    &run,
			}
			if index == 0 {
				wrongToken := release
				wrongToken.ReservationToken = "wrong-attached-token"
				_, err = factory.ReleaseLaunch(ctx, wrongToken)
				Expect(errors.Is(err, pullrequest.ErrReservationMismatch)).To(BeTrue())

				wrongRunID := snapshot.WorkflowRunID(insertAgentPRMonitorRun(
					defaultTeam.ID(), defaultTeam.Name(), definitionID,
					testBinding.ID, "failed", "wrong-release-run",
				))
				wrongRun := release
				wrongRun.WorkflowRunID = &wrongRunID
				_, err = factory.ReleaseLaunch(ctx, wrongRun)
				Expect(errors.Is(err, pullrequest.ErrReservationMismatch)).To(BeTrue())
			}

			released, err := factory.ReleaseLaunch(ctx, release)
			Expect(err).NotTo(HaveOccurred())
			Expect(released.Active).To(BeNil())

			audit, err := factory.ListAudit(
				ctx, testBinding.TeamID, testBinding.ID, pullrequest.AuditFilter{},
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(audit).To(ContainElement(SatisfyAll(
				HaveField("WorkflowRunID", run),
				HaveField("Status", status),
			)))
		}
	})

	It("replays exact attached reservations but refuses their live run release", func() {
		for index, status := range []string{"admitting", "running", "canceling"} {
			request := createRequest
			request.Locator.ExternalID = "refuse-release-" + status
			replaceAgentPRBindingOrigin(
				&request, defaultTeam.Name(), definitionID,
				"refuse-release-"+status,
			)
			testBinding, created, err := factory.Create(ctx, request)
			Expect(err).NotTo(HaveOccurred())
			Expect(created).To(BeTrue())

			reservationRequest := pullrequest.ReserveLaunch{
				TeamID: testBinding.TeamID, BindingID: testBinding.ID,
				ExpectedRevision:      testBinding.Revision,
				ActionDigest:          fmt.Sprintf("sha256:%064x", index+11),
				ObservationSnapshotID: request.LastObservationSnapshotID,
				Cursor:                pullrequest.Cursor(`{"batch":"refuse-release"}`),
				SourceSHA:             strings.Repeat("7", 40),
				TargetSHA:             strings.Repeat("8", 40),
				ExpiresIn:             5 * time.Minute,
			}
			reservation, reserved, err := factory.ReserveLaunch(ctx, reservationRequest)
			Expect(err).NotTo(HaveOccurred())
			Expect(reserved).To(BeTrue())
			runID := insertAgentPRMonitorRun(
				defaultTeam.ID(), defaultTeam.Name(), definitionID,
				testBinding.ID, "admitting", "refuse-"+status,
			)
			attached, err := factory.AttachRun(ctx, pullrequest.AttachRun{
				TeamID: testBinding.TeamID, BindingID: testBinding.ID,
				ExpectedRevision: reservation.BindingRevision,
				ActionDigest:     reservation.ActionDigest,
				ReservationToken: reservation.Token,
				WorkflowRunID:    snapshot.WorkflowRunID(runID),
			})
			Expect(err).NotTo(HaveOccurred())
			if status != "admitting" {
				_, err = dbConn.Exec(`
					UPDATE agent_workflow_runs
					SET status=$2,
					    completed_at=CASE WHEN $2 IN ('failed','errored','aborted','succeeded')
					                      THEN clock_timestamp() ELSE NULL END,
					    updated_at=clock_timestamp()
					WHERE id=$1
				`, runID, status)
				Expect(err).NotTo(HaveOccurred())
			}

			replayed, reserved, err := factory.ReserveLaunch(
				ctx, reservationRequest,
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(reserved).To(BeTrue())
			Expect(replayed.Token).To(Equal(reservation.Token))
			Expect(replayed.BindingRevision).To(Equal(
				reservation.BindingRevision,
			))
			Expect(replayed.WorkflowRunID).NotTo(BeNil())
			Expect(*replayed.WorkflowRunID).To(Equal(
				snapshot.WorkflowRunID(runID),
			))

			run := snapshot.WorkflowRunID(runID)
			_, err = factory.ReleaseLaunch(ctx, pullrequest.ReleaseLaunch{
				TeamID: testBinding.TeamID, BindingID: testBinding.ID,
				ExpectedRevision: attached.Revision,
				ActionDigest:     reservation.ActionDigest,
				ReservationToken: reservation.Token,
				WorkflowRunID:    &run,
			})
			Expect(errors.Is(err, pullrequest.ErrReservationMismatch)).To(BeTrue())

			stored, found, err := factory.Get(ctx, testBinding.TeamID, testBinding.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(stored.Revision).To(Equal(attached.Revision))
			Expect(stored.Active).NotTo(BeNil())
			Expect(stored.Active.WorkflowRunID).To(Equal(&run))
		}
	})

	It("attaches and acknowledges only the exact same-team monitor run and action", func() {
		reservation := reserveAgentPRBinding(factory, binding, createRequest.LastObservationSnapshotID)
		wrongRunID := insertAgentPRMonitorRun(
			defaultTeam.ID(), defaultTeam.Name(), definitionID,
			binding.ID+1, "admitting", "wrong-origin",
		)
		_, err := factory.AttachRun(ctx, pullrequest.AttachRun{
			TeamID: binding.TeamID, BindingID: binding.ID,
			ExpectedRevision: reservation.BindingRevision,
			ActionDigest:     reservation.ActionDigest, ReservationToken: reservation.Token,
			WorkflowRunID: snapshot.WorkflowRunID(wrongRunID),
		})
		Expect(errors.Is(err, pullrequest.ErrReservationMismatch)).To(BeTrue())

		runID := insertAgentPRMonitorRun(
			defaultTeam.ID(), defaultTeam.Name(), definitionID,
			binding.ID, "admitting", "exact",
		)
		attached, err := factory.AttachRun(ctx, pullrequest.AttachRun{
			TeamID: binding.TeamID, BindingID: binding.ID,
			ExpectedRevision: reservation.BindingRevision,
			ActionDigest:     reservation.ActionDigest, ReservationToken: reservation.Token,
			WorkflowRunID: snapshot.WorkflowRunID(runID),
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(attached.Active.WorkflowRunID).To(Equal(pointerToWorkflowRunID(runID)))

		_, err = dbConn.Exec(`
			UPDATE agent_workflow_runs
			SET status='succeeded', completed_at=clock_timestamp(), updated_at=clock_timestamp()
			WHERE id=$1
		`, runID)
		Expect(err).NotTo(HaveOccurred())
		ack := pullrequest.AcknowledgeAction{
			TeamID: binding.TeamID, BindingID: binding.ID,
			ExpectedRevision: attached.Revision,
			ActionDigest:     reservation.ActionDigest, ReservationToken: reservation.Token,
			WorkflowRunID:         snapshot.WorkflowRunID(runID),
			ObservationSnapshotID: reservation.ObservationSnapshotID,
			Cursor:                reservation.Cursor, SourceSHA: reservation.SourceSHA,
			TargetSHA: reservation.TargetSHA,
		}
		wrong := ack
		wrong.ActionDigest = "sha256:" + strings.Repeat("9", 64)
		_, err = factory.AcknowledgeAction(ctx, wrong)
		Expect(errors.Is(err, pullrequest.ErrReservationMismatch)).To(BeTrue())

		acknowledged, err := factory.AcknowledgeAction(ctx, ack)
		Expect(err).NotTo(HaveOccurred())
		Expect(acknowledged.Active).To(BeNil())
		Expect(acknowledged.AcknowledgedCursor).To(Equal(reservation.Cursor))
		Expect(acknowledged.LastAcknowledgedWorkflowRunID).To(Equal(pointerToWorkflowRunID(runID)))
		replayed, err := factory.AcknowledgeAction(ctx, ack)
		Expect(err).NotTo(HaveOccurred())
		Expect(replayed.Revision).To(Equal(acknowledged.Revision))

		replayedCreate, created, err := factory.Create(ctx, createRequest)
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeFalse())
		Expect(replayedCreate.ID).To(Equal(binding.ID))
		Expect(replayedCreate.Revision).To(Equal(acknowledged.Revision))
		Expect(replayedCreate.AcknowledgedCursor).To(Equal(
			acknowledged.AcknowledgedCursor,
		))
		Expect(replayedCreate.LastAcknowledgedWorkflowRunID).To(Equal(
			acknowledged.LastAcknowledgedWorkflowRunID,
		))
	})

	It("records terminal provider evidence after operator termination without manufacturing or erasing it", func() {
		reservation := reserveAgentPRBinding(factory, binding, createRequest.LastObservationSnapshotID)
		runID := insertAgentPRMonitorRun(
			defaultTeam.ID(), defaultTeam.Name(), definitionID,
			binding.ID, "admitting", "terminal",
		)
		attached, err := factory.AttachRun(ctx, pullrequest.AttachRun{
			TeamID: binding.TeamID, BindingID: binding.ID,
			ExpectedRevision: reservation.BindingRevision,
			ActionDigest:     reservation.ActionDigest, ReservationToken: reservation.Token,
			WorkflowRunID: snapshot.WorkflowRunID(runID),
		})
		Expect(err).NotTo(HaveOccurred())
		_, err = dbConn.Exec(`
			UPDATE agent_workflow_runs
			SET status='succeeded', completed_at=clock_timestamp(), updated_at=clock_timestamp()
			WHERE id=$1
		`, runID)
		Expect(err).NotTo(HaveOccurred())
		terminated, err := factory.Terminate(ctx, pullrequest.OperatorRequest{
			TeamID: binding.TeamID, BindingID: binding.ID,
			ExpectedRevision: attached.Revision,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(terminated.State).To(Equal(pullrequest.BindingActive))
		Expect(terminated.OperatorTerminated).To(BeTrue())
		Expect(terminated.TerminalObservationSnapshotID).To(BeNil())

		terminalRequest := pullrequest.TerminalBinding{
			TeamID: binding.TeamID, BindingID: binding.ID,
			ExpectedRevision: terminated.Revision, State: pullrequest.BindingCompleted,
			ActionDigest: reservation.ActionDigest, ReservationToken: reservation.Token,
			WorkflowRunID:         snapshot.WorkflowRunID(runID),
			ObservationSnapshotID: reservation.ObservationSnapshotID,
			Cursor:                reservation.Cursor, SourceSHA: reservation.SourceSHA,
			TargetSHA: reservation.TargetSHA,
		}
		completed, err := factory.MarkTerminal(ctx, terminalRequest)
		Expect(err).NotTo(HaveOccurred())
		Expect(completed.State).To(Equal(pullrequest.BindingCompleted))
		Expect(completed.OperatorTerminated).To(BeTrue())
		Expect(completed.TerminalObservationSnapshotID).To(Equal(
			pointerToSnapshotID(int64(reservation.ObservationSnapshotID)),
		))

		mismatch := terminalRequest
		mismatch.State = pullrequest.BindingAbandoned
		_, err = factory.MarkTerminal(ctx, mismatch)
		Expect(errors.Is(err, pullrequest.ErrBindingImmutable)).To(BeTrue())
		_, err = factory.Terminate(ctx, pullrequest.OperatorRequest{
			TeamID: binding.TeamID, BindingID: binding.ID,
			ExpectedRevision: completed.Revision,
		})
		Expect(errors.Is(err, pullrequest.ErrBindingImmutable)).To(BeTrue())
	})
})

func reserveAgentPRBinding(
	factory pullrequest.BindingStore,
	binding pullrequest.Binding,
	observationID snapshot.SnapshotID,
) pullrequest.LaunchReservation {
	reservation, reserved, err := factory.ReserveLaunch(context.Background(), pullrequest.ReserveLaunch{
		TeamID: binding.TeamID, BindingID: binding.ID, ExpectedRevision: binding.Revision,
		ActionDigest:          "sha256:" + strings.Repeat("4", 64),
		ObservationSnapshotID: observationID,
		Cursor:                pullrequest.Cursor(`{"batch":"action"}`),
		SourceSHA:             strings.Repeat("d", 40), TargetSHA: strings.Repeat("e", 40),
		ExpiresIn: 5 * time.Minute,
	})
	Expect(err).NotTo(HaveOccurred())
	Expect(reserved).To(BeTrue())
	return reservation
}

func insertAgentPRMonitorRun(
	teamID int,
	teamName string,
	definitionID int,
	bindingID int64,
	status string,
	suffix string,
) int64 {
	var runID int64
	Expect(dbConn.QueryRow(`
		INSERT INTO agent_workflow_runs
			(definition_kind, team_id, team_name, workflow_definition_id,
			 workflow_name, workflow_version, schema_version, signature_version,
			 definition_content_hash, idempotency_key, parameterized_config,
			 parameterized_config_hash, origin_kind, origin_reference, created_by, status)
		SELECT 'workflow', $1, $2, definition.id, definition.name,
		       definition.version, definition.schema_version, definition.signature_version,
		       definition.content_hash, $3, '{}'::jsonb, $4,
		       'pr-monitor', $5, 'test', $6
		FROM agent_workflow_definitions definition WHERE definition.id=$7
		RETURNING id
	`, teamID, teamName, fmt.Sprintf("pr-monitor-%s-%d", suffix, time.Now().UnixNano()),
		strings.Repeat("f", 64), fmt.Sprint(bindingID), status, definitionID,
	).Scan(&runID)).To(Succeed())
	return runID
}

type agentPRBindingPublicationTarget struct {
	Locator               pullrequest.Locator
	URL                   string
	SourceRef             string
	SourceSHA             string
	TargetRef             string
	TargetSHA             string
	Destination           string
	ApprovalPolicyVersion string
}

func replaceAgentPRBindingOrigin(
	request *pullrequest.CreateBinding,
	teamName string,
	definitionID int,
	suffix string,
) {
	workflowRunID, acceptedOccurrenceID, creationOccurrenceID, observationID :=
		insertAgentPRBindingEvidence(
			request.TeamID, teamName, definitionID, suffix,
			agentPRBindingPublicationTarget{
				Locator: request.Locator, URL: request.URL,
				SourceRef: request.SourceRef, SourceSHA: request.LastReconciledSourceSHA,
				TargetRef: request.TargetRef, TargetSHA: request.LastReconciledTargetSHA,
				Destination:           request.Destination,
				ApprovalPolicyVersion: request.ApprovalPolicyVersion,
			},
		)
	request.OriginatingWorkflowRunID = snapshot.WorkflowRunID(workflowRunID)
	request.OriginatingPublicationOccurrence = acceptedOccurrenceID
	request.CreationPublicationOccurrenceID = creationOccurrenceID
	request.LastObservationSnapshotID = snapshot.SnapshotID(observationID)
}

func insertAgentPRBindingEvidence(
	teamID int,
	teamName string,
	definitionID int,
	suffix string,
	target agentPRBindingPublicationTarget,
) (int64, int64, int64, int64) {
	var pipelineID int
	Expect(dbConn.QueryRow(`
		INSERT INTO pipelines (name, team_id, secondary_ordering)
		VALUES ($1, $2, 1) RETURNING id
	`, "pr-binding-origin-"+suffix, teamID).Scan(&pipelineID)).To(Succeed())
	var buildID int64
	Expect(dbConn.QueryRow(`
		INSERT INTO builds (name, status, team_id, pipeline_id, completed, end_time)
		VALUES ($1, 'succeeded', $2, $3, true, clock_timestamp()) RETURNING id
	`, "pr-binding-origin-"+suffix, teamID, pipelineID).Scan(&buildID)).To(Succeed())
	var workflowRunID int64
	Expect(dbConn.QueryRow(`
		INSERT INTO agent_workflow_runs
			(definition_kind, team_id, team_name, workflow_definition_id,
			 workflow_name, workflow_version, schema_version, signature_version,
			 definition_content_hash, idempotency_key, parameterized_config,
			 parameterized_config_hash, origin_kind, origin_reference, created_by,
			 status, planned_build_id, completed_at)
		SELECT 'workflow', $1, $2, definition.id, definition.name,
		       definition.version, definition.schema_version, definition.signature_version,
		       definition.content_hash, $3, '{}'::jsonb, $4,
		       'initial-publish', $5, 'test', 'succeeded', $6, clock_timestamp()
		FROM agent_workflow_definitions definition WHERE definition.id=$7
		RETURNING id
	`, teamID, teamName, "pr-binding-origin-"+suffix, strings.Repeat("e", 64),
		suffix, buildID, definitionID).Scan(&workflowRunID)).To(Succeed())
	insertSnapshot := func(typeName, portName string, offset int64) snapshot.SnapshotRef {
		var snapshotID int64
		digest := "sha256:" + fmt.Sprintf("%064x", workflowRunID+offset)
		Expect(dbConn.QueryRow(`
			INSERT INTO agent_snapshots
				(team_id, type_name, type_version, digest, byte_size, file_count,
				 representation, content_state)
			VALUES ($1, $2, 1, $3, 1, 1, 'application/x-tar', 'available')
			RETURNING id
		`, teamID, typeName, digest).Scan(&snapshotID)).To(Succeed())
		_, err := dbConn.Exec(`
			INSERT INTO agent_workflow_run_snapshots
				(workflow_run_id, direction, port_name, snapshot_id, promoted_at)
			VALUES ($1, 'output', $2, $3, clock_timestamp())
		`, workflowRunID, portName, snapshotID)
		Expect(err).NotTo(HaveOccurred())
		return snapshot.SnapshotRef{
			ID:   snapshot.SnapshotID(snapshotID),
			Type: snapshot.TypeRef(typeName + "/v1"), Digest: snapshot.Digest(digest),
		}
	}
	observation := insertSnapshot("pull-request", "initial-observation", 100)
	candidate := insertSnapshot("repository-change", "initial-candidate", 101)
	validation := insertSnapshot("validation", "initial-validation", 102)
	impact := insertSnapshot("publish-impact", "initial-impact", 103)
	question := insertSnapshot("question", "initial-question", 104)
	answer := insertSnapshot("human-answer", "initial-answer", 105)
	acceptedCandidate := insertSnapshot("repository", "accepted-candidate", 106)
	review := insertSnapshot("review", "accepted-review", 107)
	resolvedAt := time.Now().UTC().Truncate(time.Microsecond)
	var reviewWorkflowRunID int64
	Expect(dbConn.QueryRow(`
		INSERT INTO agent_workflow_runs
			(definition_kind, team_id, team_name, workflow_definition_id,
			 workflow_name, workflow_version, schema_version, signature_version,
			 definition_content_hash, idempotency_key, parameterized_config,
			 parameterized_config_hash, origin_kind, origin_reference, created_by,
			 status, planned_build_id, started_at, completed_at)
		SELECT 'workflow', $1, $2, definition.id, 'code-review',
		       definition.version, 3, definition.signature_version,
		       definition.content_hash, $3, '{}'::jsonb, $4,
		       'manual', '', 'reviewer', 'succeeded', $5,
		       clock_timestamp(), clock_timestamp()
		FROM agent_workflow_definitions definition WHERE definition.id=$6
		RETURNING id
	`, teamID, teamName, "pr-binding-review-"+suffix, strings.Repeat("f", 64),
		buildID, definitionID).Scan(&reviewWorkflowRunID)).To(Succeed())
	_, err := dbConn.Exec(`
		INSERT INTO agent_workflow_run_snapshots
			(workflow_run_id, direction, port_name, snapshot_id, promoted_at)
		VALUES ($1, 'input', 'after', $2, $4),
		       ($1, 'output', 'review', $3, $4)
	`, reviewWorkflowRunID, int64(acceptedCandidate.ID), int64(review.ID), resolvedAt)
	Expect(err).NotTo(HaveOccurred())
	_, err = dbConn.Exec(`
		INSERT INTO agent_workflow_outcomes
			(team_id, workflow_run_id, output_snapshot_id, disposition,
			 publication_state, human_modified, intervention_count, labels,
			 actor, revision, audited_at)
		VALUES ($1, $2, $3, 'accepted', 'not_requested',
		        false, 0, '[]'::jsonb, 'reviewer', 1, $4)
	`, teamID, reviewWorkflowRunID, int64(review.ID), resolvedAt)
	Expect(err).NotTo(HaveOccurred())
	var waitID int64
	Expect(dbConn.QueryRow(`
		INSERT INTO agent_workflow_waits
			(team_id, workflow_run_id, build_id, build_id_evidence,
			 plan_id, attempt, output_name, question_name, question_snapshot_id,
			 expected_type_name, expected_type_version, deadline, timeout_policy,
			 status, answer_snapshot_id, resolved_by, resolved_by_display_name,
			 resolution_source, resolved_at, resolution_intent_answer,
			 resolution_intent_actor, resolution_intent_display_name,
			 resolution_intent_at)
		VALUES ($1, $2, $3, $3, $4, '1', 'approval', 'PR approval', $5,
		        'human-answer', 1, $6, 'fail', 'resolved', $7, 'reviewer',
		        'reviewer', 'human', $8, 'approve', 'reviewer', 'reviewer', $8)
		RETURNING id
	`, teamID, workflowRunID, buildID, "pr-binding-approval-"+suffix,
		int64(question.ID), resolvedAt.Add(time.Hour), int64(answer.ID), resolvedAt,
	).Scan(&waitID)).To(Succeed())

	action := publisher.PRAction{
		Kind: publisher.OperationCreatePR,
		PullRequest: &publisher.PullRequestPublicationRequest{
			Authority: publisher.Authority{
				TeamID: teamID, TeamName: teamName, BuildID: buildID,
				WorkflowRunID: snapshot.WorkflowRunID(workflowRunID), Actor: "concourse",
			},
			Observation: observation, Candidate: candidate,
			Validation: validation, Impact: impact,
			Evidence: publisher.PublicationEvidence{
				Kind: publisher.EvidenceHumanWait,
				HumanWait: &publisher.ApprovalEvidence{
					WaitID: workflowwait.ID(waitID), Question: question, Answer: answer,
					ResolvedBy: "reviewer", ResolvedAt: resolvedAt,
				},
			},
			Destination: target.Destination, ApprovalPolicyVersion: target.ApprovalPolicyVersion,
			Locator: publisher.PRLocator{
				Provider:   publisher.PRProvider(target.Locator.Provider),
				Repository: target.Locator.Repository,
			},
			SourceRef: target.SourceRef, SourceSHA: target.SourceSHA,
			TargetRef: target.TargetRef, TargetSHA: target.TargetSHA,
			Title: "Validated change", Body: "Ready for provider review.",
		},
	}
	operationKey, err := action.OperationKey()
	Expect(err).NotTo(HaveOccurred())
	payloadAction := action.Clone()
	payloadAction.PullRequest.Authority = publisher.Authority{TeamID: teamID}
	payload, err := json.Marshal(payloadAction)
	Expect(err).NotTo(HaveOccurred())
	result, err := json.Marshal(publisher.Result{
		Status: publisher.StatusSucceeded, ExternalID: target.Locator.ExternalID,
		URL: target.URL, HeadSHA: target.SourceSHA, BaseSHA: target.TargetSHA,
	})
	Expect(err).NotTo(HaveOccurred())

	tx, err := dbConn.Begin()
	Expect(err).NotTo(HaveOccurred())
	defer func() { _ = tx.Rollback() }()
	var acceptedPublicationID, acceptedOccurrenceID int64
	var creationPublicationID, creationOccurrenceID int64
	Expect(tx.QueryRow(`SELECT nextval('agent_publications_id_seq')`).Scan(
		&acceptedPublicationID,
	)).To(Succeed())
	Expect(tx.QueryRow(`SELECT nextval('agent_publication_occurrences_id_seq')`).Scan(
		&acceptedOccurrenceID,
	)).To(Succeed())
	Expect(tx.QueryRow(`SELECT nextval('agent_publications_id_seq')`).Scan(
		&creationPublicationID,
	)).To(Succeed())
	Expect(tx.QueryRow(`SELECT nextval('agent_publication_occurrences_id_seq')`).Scan(
		&creationOccurrenceID,
	)).To(Succeed())
	_, err = tx.Exec(`SET CONSTRAINTS agent_publications_lease_owner_occurrence_fkey DEFERRED`)
	Expect(err).NotTo(HaveOccurred())
	_, err = tx.Exec(`
		INSERT INTO agent_publications
			(id, operation_key, team_id, team_name, workflow_run_id, build_id,
			 actor, input_snapshot_id, publisher, destination, mode, parameters,
			 approval_policy_version, status, result, lease_owner_occurrence_id)
		VALUES ($1, $2, $3, $4, $5, $6, 'concourse', $7,
		        'accepted-review-authority/v1', $8, 'pull-request', '{}', $9,
		        'succeeded', '{"status":"succeeded"}', $10)
	`, acceptedPublicationID,
		"sha256:"+fmt.Sprintf("%064x", acceptedPublicationID+2000),
		teamID, teamName, workflowRunID, buildID, int64(acceptedCandidate.ID),
		target.Destination, target.ApprovalPolicyVersion, acceptedOccurrenceID)
	Expect(err).NotTo(HaveOccurred())
	_, err = tx.Exec(`
		INSERT INTO agent_publication_occurrences
			(id, publication_id, team_id, team_name, workflow_run_id, build_id,
			 actor, input_snapshot_id, status)
		VALUES ($1, $2, $3, $4, $5, $6, 'concourse', $7, 'succeeded')
	`, acceptedOccurrenceID, acceptedPublicationID, teamID, teamName,
		workflowRunID, buildID, int64(acceptedCandidate.ID))
	Expect(err).NotTo(HaveOccurred())
	_, err = tx.Exec(`
		INSERT INTO agent_publication_approval_evidence
			(publication_id, team_id, evidence_kind,
			 review_snapshot_id, candidate_snapshot_id, validation_snapshot_id,
			 review_workflow_run_id, outcome_revision, accepted_by, accepted_at)
		VALUES ($1, $2, 'accepted_review', $3, $4, $5, $6, 1, 'reviewer', $7)
	`, acceptedOccurrenceID, teamID, int64(review.ID), int64(acceptedCandidate.ID),
		int64(validation.ID), reviewWorkflowRunID, resolvedAt)
	Expect(err).NotTo(HaveOccurred())
	_, err = tx.Exec(`
		INSERT INTO agent_publications
			(id, operation_key, operation_kind, operation_payload,
			 team_id, team_name, workflow_run_id, build_id,
			 actor, input_snapshot_id, publisher, destination, mode, parameters,
			 approval_policy_version, status, result, lease_owner_occurrence_id)
		VALUES ($1, $2, 'create_pr', $3, $4, $5, $6, $7, 'concourse', $8,
		        'provider-native-pr/v1', $9, 'pull-request', '{}', $10,
		        'succeeded', $11, $12)
	`, creationPublicationID, operationKey, payload, teamID, teamName, workflowRunID,
		buildID, int64(candidate.ID), target.Destination,
		target.ApprovalPolicyVersion, result, creationOccurrenceID)
	Expect(err).NotTo(HaveOccurred())
	_, err = tx.Exec(`
		INSERT INTO agent_publication_occurrences
			(id, publication_id, team_id, team_name, workflow_run_id, build_id,
			 actor, input_snapshot_id, status)
		VALUES ($1, $2, $3, $4, $5, $6, 'concourse', $7, 'succeeded')
	`, creationOccurrenceID, creationPublicationID, teamID, teamName,
		workflowRunID, buildID,
		int64(candidate.ID))
	Expect(err).NotTo(HaveOccurred())
	for role, reference := range map[string]snapshot.SnapshotRef{
		"observation": observation, "validation": validation, "impact": impact,
	} {
		_, err = tx.Exec(`
			INSERT INTO agent_publication_inputs
				(publication_id, team_id, role, snapshot_id)
			VALUES ($1, $2, $3, $4)
		`, creationOccurrenceID, teamID, role, int64(reference.ID))
		Expect(err).NotTo(HaveOccurred())
	}
	_, err = tx.Exec(`
		INSERT INTO agent_publication_approval_evidence
			(publication_id, team_id, evidence_kind, human_wait_id,
			 question_snapshot_id, answer_snapshot_id, resolved_by, resolved_at)
		VALUES ($1, $2, 'human_wait', $3, $4, $5, 'reviewer', $6)
	`, creationOccurrenceID, teamID, waitID, int64(question.ID),
		int64(answer.ID), resolvedAt)
	Expect(err).NotTo(HaveOccurred())
	Expect(tx.Commit()).To(Succeed())
	return workflowRunID, acceptedOccurrenceID, creationOccurrenceID,
		int64(observation.ID)
}

func pointerToWorkflowRunID(value int64) *snapshot.WorkflowRunID {
	converted := snapshot.WorkflowRunID(value)
	return &converted
}

func pointerToSnapshotID(value int64) *snapshot.SnapshotID {
	converted := snapshot.SnapshotID(value)
	return &converted
}
