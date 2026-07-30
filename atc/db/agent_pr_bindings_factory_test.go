package db_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/pullrequest"
	"github.com/concourse/concourse/agent/snapshot"
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
		originRunID, occurrenceID, observationID := insertAgentPRBindingEvidence(
			defaultTeam.ID(), defaultTeam.Name(), definitionID, fmt.Sprint(suffix),
		)
		reconciledAt := time.Now().UTC().Truncate(time.Microsecond)
		createRequest = pullrequest.CreateBinding{
			TeamID: defaultTeam.ID(),
			Locator: pullrequest.Locator{
				Provider: pullrequest.ProviderGitHub, Repository: "example/repository",
				ExternalID: "118",
			},
			URL:       "https://github.example/example/repository/pull/118",
			SourceRef: "refs/heads/feature/pr", TargetRef: "refs/heads/main",
			OriginatingWorkflowRunID:         snapshot.WorkflowRunID(originRunID),
			OriginatingPublicationOccurrence: occurrenceID,
			MonitorWorkflowDefinitionID:      definitionID,
			MonitorWorkflowVersion:           3,
			AcknowledgedCursor:               pullrequest.Cursor(" \t{\"opaque\":[1,\"雪\"]}\n"),
			LastObservationSnapshotID:        snapshot.SnapshotID(observationID),
			LastReconciledSourceSHA:          strings.Repeat("b", 40),
			LastReconciledTargetSHA:          strings.Repeat("c", 40),
			LastReconciledAt:                 reconciledAt,
		}
		var created bool
		var err error
		binding, created, err = factory.Create(ctx, createRequest)
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())
	})

	It("is team scoped, idempotent, locator unique, and preserves opaque cursor bytes", func() {
		Expect(binding.AcknowledgedCursor).To(Equal(createRequest.AcknowledgedCursor))
		Expect([]byte(binding.AcknowledgedCursor)).To(Equal([]byte(createRequest.AcknowledgedCursor)))

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

	It("releases failed, errored, and aborted attached runs without erasing audit history", func() {
		for index, status := range []string{"failed", "errored", "aborted"} {
			testBinding := binding
			if index > 0 {
				request := createRequest
				request.Locator.ExternalID = "release-" + status
				var created bool
				var err error
				testBinding, created, err = factory.Create(ctx, request)
				Expect(err).NotTo(HaveOccurred())
				Expect(created).To(BeTrue())
			}
			reservation := reserveAgentPRBinding(
				factory, testBinding, createRequest.LastObservationSnapshotID,
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

	It("refuses wrong, live, canceling, and succeeded attached runs and stale projected reservations", func() {
		for index, status := range []string{"admitting", "running", "canceling", "succeeded"} {
			request := createRequest
			request.Locator.ExternalID = "refuse-release-" + status
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

			_, reserved, err = factory.ReserveLaunch(ctx, reservationRequest)
			Expect(reserved).To(BeFalse())
			Expect(errors.Is(err, pullrequest.ErrStaleBindingRevision)).To(BeTrue())

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

func insertAgentPRBindingEvidence(
	teamID int,
	teamName string,
	definitionID int,
	suffix string,
) (int64, int64, int64) {
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
	var snapshotID int64
	Expect(dbConn.QueryRow(`
		INSERT INTO agent_snapshots
			(team_id, type_name, type_version, digest, byte_size, file_count, representation)
		VALUES ($1, 'pull-request', 1, $2, 1, 1,
		        'application/vnd.jetbridge.snapshot.tar.v1')
		RETURNING id
	`, teamID, "sha256:"+fmt.Sprintf("%064x", workflowRunID+100)).Scan(&snapshotID)).To(Succeed())

	tx, err := dbConn.Begin()
	Expect(err).NotTo(HaveOccurred())
	defer func() { _ = tx.Rollback() }()
	var publicationID, occurrenceID int64
	Expect(tx.QueryRow(`SELECT nextval('agent_publications_id_seq')`).Scan(&publicationID)).To(Succeed())
	Expect(tx.QueryRow(`SELECT nextval('agent_publication_occurrences_id_seq')`).Scan(&occurrenceID)).To(Succeed())
	_, err = tx.Exec(`SET CONSTRAINTS agent_publications_lease_owner_occurrence_fkey DEFERRED`)
	Expect(err).NotTo(HaveOccurred())
	_, err = tx.Exec(`
		INSERT INTO agent_publications
			(id, operation_key, team_id, team_name, workflow_run_id, build_id,
			 actor, input_snapshot_id, publisher, destination, mode, parameters,
			 approval_policy_version, status, result, lease_owner_occurrence_id)
		VALUES ($1, $2, $3, $4, $5, $6, 'test', $7,
		        'forge-pr', 'origin', 'pull-request', '{}', 'v1',
		        'succeeded', '{"external_id":"created"}', $8)
	`, publicationID, "sha256:"+fmt.Sprintf("%064x", publicationID+1000),
		teamID, teamName, workflowRunID, buildID, snapshotID, occurrenceID)
	Expect(err).NotTo(HaveOccurred())
	_, err = tx.Exec(`
		INSERT INTO agent_publication_occurrences
			(id, publication_id, team_id, team_name, workflow_run_id, build_id,
			 actor, input_snapshot_id, status)
		VALUES ($1, $2, $3, $4, $5, $6, 'test', $7, 'succeeded')
	`, occurrenceID, publicationID, teamID, teamName, workflowRunID, buildID, snapshotID)
	Expect(err).NotTo(HaveOccurred())
	Expect(tx.Commit()).To(Succeed())
	return workflowRunID, occurrenceID, snapshotID
}

func pointerToWorkflowRunID(value int64) *snapshot.WorkflowRunID {
	converted := snapshot.WorkflowRunID(value)
	return &converted
}

func pointerToSnapshotID(value int64) *snapshot.SnapshotID {
	converted := snapshot.SnapshotID(value)
	return &converted
}
