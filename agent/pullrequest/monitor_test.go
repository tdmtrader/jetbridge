package pullrequest

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
	"github.com/concourse/concourse/atc"
)

func TestMonitorCoordinatorSerializesOneExactActionAndLaunchesOneDurableRun(t *testing.T) {
	binding := monitorTestBinding()
	store := newMonitorMemoryStore(binding)
	launcher := &monitorMemoryLauncher{store: store}
	results := &monitorMemoryResults{}
	coordinator, err := NewMonitorCoordinator(
		store, launcher, results, &monitorDirectObservationInspector{},
		monitorTestAcceptedResolver(binding),
		10*time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	source := monitorTestSourceBuild(binding)

	type launchResult struct {
		run      snapshot.WorkflowRunID
		launched bool
		err      error
	}
	start := make(chan struct{})
	outcomes := make(chan launchResult, 2)
	for range 2 {
		go func() {
			<-start
			run, launched, err := coordinator.ReserveAndLaunch(
				context.Background(), source,
			)
			outcomes <- launchResult{run: run, launched: launched, err: err}
		}()
	}
	close(start)
	first, second := <-outcomes, <-outcomes
	for _, result := range []launchResult{first, second} {
		if result.err != nil {
			t.Fatalf("reserve and launch: %v", result.err)
		}
		if !result.launched || result.run <= 0 {
			t.Fatalf("launch result = %#v, want the one durable run", result)
		}
	}
	if first.run != second.run {
		t.Fatalf("concurrent runs = %d and %d, want one durable identity", first.run, second.run)
	}
	if launcher.uniqueRuns() != 1 {
		t.Fatalf("durable launches = %d, want one", launcher.uniqueRuns())
	}
	attached := store.bindingValue()
	if attached.Active == nil || attached.Active.WorkflowRunID == nil ||
		*attached.Active.WorkflowRunID != first.run ||
		attached.Active.ActionDigest != source.Version.ActionDigest ||
		attached.Active.Cursor != Cursor(source.Version.Cursor) {
		t.Fatalf("attached binding = %#v, want exact reserved action and run", attached)
	}
	replayedRun, replayed, err := coordinator.ReserveAndLaunch(
		context.Background(), source,
	)
	if err != nil || !replayed || replayedRun != first.run {
		t.Fatalf(
			"attached launch replay = (%d, %t, %v), want run %d",
			replayedRun, replayed, err, first.run,
		)
	}
	if launcher.uniqueRuns() != 1 {
		t.Fatalf(
			"attached replay allocated %d durable runs, want one",
			launcher.uniqueRuns(),
		)
	}

	next := monitorTestSourceBuildWith(
		attached, 302, 42, "cursor-2", monitorDigest("e"),
	)
	if run, launched, err := coordinator.ReserveAndLaunch(
		context.Background(), next,
	); err != nil || launched || run != 0 {
		t.Fatalf("busy second action = (%d, %t, %v), want deferred", run, launched, err)
	}

	result := monitorResultFor(attached, MonitorRunSucceeded, MonitorOutcomePublished)
	acknowledged, err := coordinator.Acknowledge(context.Background(), result)
	if err != nil {
		t.Fatalf("acknowledge first action: %v", err)
	}
	if acknowledged.Active != nil ||
		acknowledged.AcknowledgedCursor != Cursor(source.Version.Cursor) {
		t.Fatalf("acknowledged binding = %#v", acknowledged)
	}

	next = monitorTestSourceBuildWith(
		acknowledged, 302, 42, "cursor-2", monitorDigest("e"),
	)
	run, launched, err := coordinator.ReserveAndLaunch(context.Background(), next)
	if err != nil || !launched || run == 0 || run == first.run {
		t.Fatalf("next action after acknowledgement = (%d, %t, %v)", run, launched, err)
	}
}

func TestMonitorPublicationTargetProtectsExactBindingAuthority(t *testing.T) {
	target, err := NewMonitorPublicationTarget(MonitorPublicationTargetSpec{
		Destination:           "github.example/acme/widget",
		ApprovalPolicyVersion: "engineering/v3",
		SourceRef:             "refs/heads/change",
		TargetRef:             "refs/heads/main",
	})
	if err != nil {
		t.Fatal(err)
	}
	protected, err := target.Protected()
	if err != nil {
		t.Fatal(err)
	}
	if protected.Destination != "github.example/acme/widget" ||
		protected.ApprovalPolicyVersion != "engineering/v3" ||
		protected.SourceRef != "refs/heads/change" ||
		protected.TargetRef != "refs/heads/main" {
		t.Fatalf("protected target = %#v", protected)
	}

	target.TargetRef = "refs/heads/other"
	if _, err := target.Protected(); err == nil {
		t.Fatal("monitor publication target allowed caller mutation")
	}

	for name, spec := range map[string]MonitorPublicationTargetSpec{
		"missing destination": {
			ApprovalPolicyVersion: "engineering/v3",
			SourceRef:             "refs/heads/change",
			TargetRef:             "refs/heads/main",
		},
		"missing policy": {
			Destination: "github.example/acme/widget",
			SourceRef:   "refs/heads/change",
			TargetRef:   "refs/heads/main",
		},
		"unsafe source": {
			Destination:           "github.example/acme/widget",
			ApprovalPolicyVersion: "engineering/v3",
			SourceRef:             "change",
			TargetRef:             "refs/heads/main",
		},
		"same refs": {
			Destination:           "github.example/acme/widget",
			ApprovalPolicyVersion: "engineering/v3",
			SourceRef:             "refs/heads/main",
			TargetRef:             "refs/heads/main",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewMonitorPublicationTarget(spec); err == nil {
				t.Fatal("invalid monitor publication target succeeded")
			}
		})
	}
}

func TestMonitorCoordinatorLaunchFailureReleasesOnlyTheExactUnattachedReservation(t *testing.T) {
	for _, test := range []struct {
		name            string
		attachBeforeErr bool
		wantActive      bool
	}{
		{name: "unattached", wantActive: false},
		{name: "attached", attachBeforeErr: true, wantActive: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			binding := monitorTestBinding()
			store := newMonitorMemoryStore(binding)
			launchErr := errors.New("launch failed")
			launcher := &monitorMemoryLauncher{
				store: store, fail: launchErr,
				attachBeforeFailure: test.attachBeforeErr,
			}
			coordinator, err := NewMonitorCoordinator(
				store, launcher, &monitorMemoryResults{},
				&monitorDirectObservationInspector{},
				monitorTestAcceptedResolver(binding), 10*time.Minute,
			)
			if err != nil {
				t.Fatal(err)
			}

			run, launched, err := coordinator.ReserveAndLaunch(
				context.Background(), monitorTestSourceBuild(binding),
			)
			if !errors.Is(err, launchErr) || launched {
				t.Fatalf("launch failure = (%d, %t, %v)", run, launched, err)
			}
			got := store.bindingValue()
			if (got.Active != nil) != test.wantActive {
				t.Fatalf("active reservation = %#v, want active %t", got.Active, test.wantActive)
			}
			if test.wantActive && (got.Active.WorkflowRunID == nil || run != *got.Active.WorkflowRunID) {
				t.Fatalf("attached launch failure = run %d binding %#v", run, got.Active)
			}
		})
	}
}

func TestMonitorCoordinatorAcknowledgesOnlySafeSucceededOutcomes(t *testing.T) {
	for _, test := range []struct {
		name       string
		runStatus  MonitorRunStatus
		outcome    MonitorOutcome
		wantCursor bool
		wantState  BindingState
		wantPaused bool
	}{
		{name: "published", runStatus: MonitorRunSucceeded, outcome: MonitorOutcomePublished, wantCursor: true, wantState: BindingActive},
		{name: "validated no-op", runStatus: MonitorRunSucceeded, outcome: MonitorOutcomeValidatedNoop, wantCursor: true, wantState: BindingActive},
		{name: "failed", runStatus: MonitorRunFailed, wantState: BindingActive},
		{name: "errored", runStatus: MonitorRunErrored, wantState: BindingActive},
		{name: "aborted", runStatus: MonitorRunAborted, wantState: BindingActive},
		{name: "stale", runStatus: MonitorRunSucceeded, outcome: MonitorOutcomeStale, wantState: BindingActive},
		{name: "ambiguous", runStatus: MonitorRunSucceeded, outcome: MonitorOutcomeAmbiguous, wantState: BindingAttentionRequired, wantPaused: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			binding := monitorTestBinding()
			store := newMonitorMemoryStore(binding)
			launcher := &monitorMemoryLauncher{store: store}
			coordinator, err := NewMonitorCoordinator(
				store, launcher, &monitorMemoryResults{},
				&monitorDirectObservationInspector{},
				monitorTestAcceptedResolver(binding), 10*time.Minute,
			)
			if err != nil {
				t.Fatal(err)
			}
			run, launched, err := coordinator.ReserveAndLaunch(
				context.Background(), monitorTestSourceBuild(binding),
			)
			if err != nil || !launched || run <= 0 {
				t.Fatalf("launch = (%d, %t, %v)", run, launched, err)
			}
			attached := store.bindingValue()
			result := monitorResultFor(attached, test.runStatus, test.outcome)
			if test.outcome == MonitorOutcomeAmbiguous {
				result.AttentionReason = "provider response could not be recovered exactly"
			}
			got, err := coordinator.Acknowledge(context.Background(), result)
			if err != nil {
				t.Fatalf("acknowledge: %v", err)
			}
			if (got.AcknowledgedCursor == Cursor("cursor-1")) != test.wantCursor {
				t.Fatalf("acknowledged cursor = %q, want advanced %t", got.AcknowledgedCursor, test.wantCursor)
			}
			if got.State != test.wantState || got.Paused != test.wantPaused {
				t.Fatalf("binding state = %q paused=%t, want %q paused=%t", got.State, got.Paused, test.wantState, test.wantPaused)
			}
			if !test.wantCursor && got.LastAcknowledgedWorkflowRunID != nil {
				t.Fatalf("unsafe outcome acknowledged run %d", *got.LastAcknowledgedWorkflowRunID)
			}
			if got.Active != nil {
				t.Fatalf("terminal run reservation was not released: %#v", got.Active)
			}
			audit := store.auditValue()
			if len(audit) != 1 || audit[0].WorkflowRunID != run {
				t.Fatalf("run history = %#v, want retained run %d", audit, run)
			}
		})
	}
}

func TestMonitorCoordinatorAcknowledgesPublishedHeadWhileMatchingObservedLease(
	t *testing.T,
) {
	binding := monitorTestBinding()
	store := newMonitorMemoryStore(binding)
	coordinator, err := NewMonitorCoordinator(
		store, &monitorMemoryLauncher{store: store}, &monitorMemoryResults{},
		&monitorDirectObservationInspector{},
		monitorTestAcceptedResolver(binding), 10*time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := coordinator.ReserveAndLaunch(
		context.Background(), monitorTestSourceBuild(binding),
	); err != nil {
		t.Fatal(err)
	}
	attached := store.bindingValue()
	result := monitorResultFor(
		attached, MonitorRunSucceeded, MonitorOutcomePublished,
	)
	result.ReconciledSourceSHA = strings.Repeat("5", 40)

	wrongLease := result
	wrongLease.SourceSHA = result.ReconciledSourceSHA
	if _, err := coordinator.Acknowledge(
		context.Background(), wrongLease,
	); !errors.Is(err, ErrReservationMismatch) {
		t.Fatalf("published head used as lease identity: %v", err)
	}

	acknowledged, err := coordinator.Acknowledge(context.Background(), result)
	if err != nil {
		t.Fatal(err)
	}
	if acknowledged.LastReconciledSourceSHA != strings.Repeat("5", 40) {
		t.Fatalf(
			"last reconciled source sha = %q, want published head",
			acknowledged.LastReconciledSourceSHA,
		)
	}
	replayed, err := coordinator.Acknowledge(context.Background(), result)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Revision != acknowledged.Revision {
		t.Fatalf(
			"idempotent replay revision = %d, want %d",
			replayed.Revision, acknowledged.Revision,
		)
	}
}

func TestAcknowledgeActionRejectsInvalidReconciledSourceSHA(t *testing.T) {
	request := AcknowledgeAction{
		TeamID:                7,
		BindingID:             9,
		ExpectedRevision:      8,
		ActionDigest:          monitorDigest("d"),
		ReservationToken:      "reservation-1",
		WorkflowRunID:         91,
		ObservationSnapshotID: 501,
		Cursor:                "cursor-1",
		SourceSHA:             strings.Repeat("3", 40),
		ReconciledSourceSHA:   "not-an-object-id",
		TargetSHA:             strings.Repeat("4", 40),
	}

	if err := request.Validate(); err == nil {
		t.Fatal("invalid reconciled source sha was accepted")
	}
}

func TestMonitorCoordinatorMarksExactTerminalProviderObservation(t *testing.T) {
	for _, test := range []struct {
		outcome MonitorOutcome
		state   BindingState
	}{
		{outcome: MonitorOutcomeCompleted, state: BindingCompleted},
		{outcome: MonitorOutcomeAbandoned, state: BindingAbandoned},
	} {
		t.Run(string(test.outcome), func(t *testing.T) {
			binding := monitorTestBinding()
			store := newMonitorMemoryStore(binding)
			launcher := &monitorMemoryLauncher{store: store}
			coordinator, err := NewMonitorCoordinator(
				store, launcher, &monitorMemoryResults{},
				&monitorDirectObservationInspector{},
				monitorTestAcceptedResolver(binding), 10*time.Minute,
			)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := coordinator.ReserveAndLaunch(
				context.Background(), monitorTestSourceBuild(binding),
			); err != nil {
				t.Fatal(err)
			}
			attached := store.bindingValue()
			got, err := coordinator.Acknowledge(
				context.Background(),
				monitorResultFor(attached, MonitorRunSucceeded, test.outcome),
			)
			if err != nil {
				t.Fatal(err)
			}
			if got.State != test.state || got.TerminalObservationSnapshotID == nil ||
				*got.TerminalObservationSnapshotID != 501 || got.Active != nil {
				t.Fatalf("terminal binding = %#v", got)
			}
		})
	}
}

func TestMonitorCoordinatorReconcilesOnlyTheExactAttachedTerminalRun(t *testing.T) {
	binding := monitorTestBinding()
	store := newMonitorMemoryStore(binding)
	launcher := &monitorMemoryLauncher{store: store}
	results := &monitorMemoryResults{byRun: map[snapshot.WorkflowRunID]MonitorRunResult{}}
	coordinator, err := NewMonitorCoordinator(
		store, launcher, results, &monitorDirectObservationInspector{},
		monitorTestAcceptedResolver(binding),
		10*time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := coordinator.ReserveAndLaunch(
		context.Background(), monitorTestSourceBuild(binding),
	)
	if err != nil {
		t.Fatal(err)
	}
	attached := store.bindingValue()
	results.byRun[run] = monitorResultFor(
		attached, MonitorRunSucceeded, MonitorOutcomeValidatedNoop,
	)

	got, err := coordinator.ReconcileTerminal(context.Background(), 7, binding.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.AcknowledgedCursor != "cursor-1" || got.Active != nil {
		t.Fatalf("reconciled binding = %#v", got)
	}
}

func TestMonitorCoordinatorReconcilesExactDirectTerminalObservationWithoutRunEvidence(
	t *testing.T,
) {
	for _, test := range []struct {
		kind  ActionKind
		state BindingState
	}{
		{kind: ActionCompleted, state: BindingCompleted},
		{kind: ActionAbandoned, state: BindingAbandoned},
	} {
		t.Run(string(test.kind), func(t *testing.T) {
			binding := monitorTestBinding()
			store := newMonitorMemoryStore(binding)
			inspector := &monitorDirectObservationInspector{
				body: monitorDirectTerminalBody(binding, test.kind),
			}
			launcher := &monitorMemoryLauncher{store: store}
			coordinator, err := NewMonitorCoordinator(
				store, launcher, &monitorMemoryResults{}, inspector,
				monitorTestAcceptedResolver(binding), 10*time.Minute,
			)
			if err != nil {
				t.Fatal(err)
			}
			source := monitorTestSourceBuildWithKind(
				binding, 301, 41, "cursor-terminal",
				monitorDirectTerminalDigest(test.kind), test.kind,
			)

			reconciled, err := coordinator.ReconcileDirectTerminal(
				context.Background(), source,
			)
			if err != nil {
				t.Fatal(err)
			}
			if reconciled.State != test.state ||
				reconciled.TerminalObservationSnapshotID == nil ||
				*reconciled.TerminalObservationSnapshotID != source.Observation.ID ||
				reconciled.LastObservationSnapshotID == nil ||
				*reconciled.LastObservationSnapshotID != source.Observation.ID ||
				reconciled.AcknowledgedCursor != Cursor(source.Version.Cursor) ||
				reconciled.LastReconciledSourceSHA != source.Version.SourceSHA ||
				reconciled.LastReconciledTargetSHA != source.Version.TargetSHA ||
				reconciled.Active != nil {
				t.Fatalf("direct terminal binding = %#v", reconciled)
			}
			if reconciled.LastAcknowledgedWorkflowRunID != nil ||
				reconciled.LastAcknowledgedActionDigest != "" {
				t.Fatalf(
					"direct terminal transition fabricated run acknowledgement: %#v",
					reconciled,
				)
			}
			if launcher.uniqueRuns() != 0 {
				t.Fatalf("direct terminal launches = %d, want none", launcher.uniqueRuns())
			}

			replayed, err := coordinator.ReconcileDirectTerminal(
				context.Background(), source,
			)
			if err != nil {
				t.Fatal(err)
			}
			if replayed.Revision != reconciled.Revision {
				t.Fatalf(
					"direct terminal replay revision = %d, want %d",
					replayed.Revision, reconciled.Revision,
				)
			}

			different := monitorTestSourceBuildWithKindAndObservation(
				binding, 301, 41, "cursor-terminal",
				monitorDirectTerminalDigest(test.kind), test.kind, 502,
			)
			_, err = coordinator.ReconcileDirectTerminal(
				context.Background(), different,
			)
			if !errors.Is(err, ErrBindingImmutable) {
				t.Fatalf("different terminal evidence error = %v", err)
			}
			if got := store.bindingValue(); got.Revision != reconciled.Revision {
				t.Fatalf(
					"different terminal evidence changed binding = %#v",
					got,
				)
			}
		})
	}
}

func TestMonitorCoordinatorRejectsAlteredDirectTerminalEvidence(t *testing.T) {
	for _, test := range []struct {
		name       string
		mutateBody func(*contracts.PullRequestBody)
		digest     string
	}{
		{
			name: "provider",
			mutateBody: func(body *contracts.PullRequestBody) {
				body.Provider = "other-forge"
			},
		},
		{
			name: "external id",
			mutateBody: func(body *contracts.PullRequestBody) {
				body.ExternalID = "43"
			},
		},
		{
			name: "url",
			mutateBody: func(body *contracts.PullRequestBody) {
				body.URL = "https://github.example/acme/widget/pull/43"
			},
		},
		{
			name: "source ref",
			mutateBody: func(body *contracts.PullRequestBody) {
				body.SourceRef = "refs/heads/other"
			},
		},
		{
			name: "terminal state",
			mutateBody: func(body *contracts.PullRequestBody) {
				body.State = contracts.PullRequestAbandoned
			},
		},
		{
			name: "terminal trigger",
			mutateBody: func(body *contracts.PullRequestBody) {
				body.Trigger = contracts.PullRequestAbandonedTrigger
			},
		},
		{
			name: "source head",
			mutateBody: func(body *contracts.PullRequestBody) {
				body.SourceSHA = strings.Repeat("5", 40)
			},
		},
		{
			name:   "action digest",
			digest: monitorDigest("f"),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			binding := monitorTestBinding()
			body := monitorDirectTerminalBody(binding, ActionCompleted)
			if test.mutateBody != nil {
				test.mutateBody(&body)
			}
			digest := test.digest
			if digest == "" {
				digest = monitorDirectTerminalDigest(ActionCompleted)
			}
			store := newMonitorMemoryStore(binding)
			coordinator, err := NewMonitorCoordinator(
				store, &monitorMemoryLauncher{store: store},
				&monitorMemoryResults{},
				&monitorDirectObservationInspector{body: body},
				monitorTestAcceptedResolver(binding), 10*time.Minute,
			)
			if err != nil {
				t.Fatal(err)
			}
			source := monitorTestSourceBuildWithKind(
				binding, 301, 41, "cursor-terminal", digest,
				ActionCompleted,
			)

			_, err = coordinator.ReconcileDirectTerminal(
				context.Background(), source,
			)
			if !errors.Is(err, ErrStaleMonitorSourceVersion) {
				t.Fatalf("altered direct terminal evidence error = %v", err)
			}
			if got := store.bindingValue(); got.State != BindingActive ||
				got.TerminalObservationSnapshotID != nil ||
				got.Revision != binding.Revision {
				t.Fatalf("altered terminal evidence mutated binding = %#v", got)
			}
		})
	}
}

func TestMonitorCoordinatorDefersDirectTerminalBehindActiveWorkAndClassifiesStaleVersion(
	t *testing.T,
) {
	binding := monitorTestBinding()
	source := monitorTestSourceBuildWithKind(
		binding, 301, 41, "cursor-terminal",
		monitorDirectTerminalDigest(ActionCompleted), ActionCompleted,
	)
	body := monitorDirectTerminalBody(binding, ActionCompleted)

	t.Run("active work", func(t *testing.T) {
		busy := binding
		busy.Revision++
		busy.Active = &LaunchReservation{
			BindingID: busy.ID, BaseRevision: binding.Revision,
			BindingRevision:       busy.Revision,
			ActionDigest:          monitorDigest("d"),
			ObservationSnapshotID: 401,
			Cursor:                "cursor-active",
			SourceSHA:             strings.Repeat("1", 40),
			TargetSHA:             strings.Repeat("2", 40),
			Token:                 "active-token",
			ExpiresAt:             time.Now().Add(time.Minute),
		}
		store := newMonitorMemoryStore(busy)
		coordinator, err := NewMonitorCoordinator(
			store, &monitorMemoryLauncher{store: store},
			&monitorMemoryResults{},
			&monitorDirectObservationInspector{body: body},
			monitorTestAcceptedResolver(binding), 10*time.Minute,
		)
		if err != nil {
			t.Fatal(err)
		}

		_, err = coordinator.ReconcileDirectTerminal(
			context.Background(), source,
		)
		if !errors.Is(err, ErrBindingBusy) {
			t.Fatalf("busy direct terminal error = %v", err)
		}
		if got := store.bindingValue(); got.State != BindingActive ||
			got.Active == nil || got.Revision != busy.Revision {
			t.Fatalf("busy direct terminal mutated binding = %#v", got)
		}
	})

	t.Run("stale projected revision", func(t *testing.T) {
		stale := binding
		stale.Revision++
		store := newMonitorMemoryStore(stale)
		coordinator, err := NewMonitorCoordinator(
			store, &monitorMemoryLauncher{store: store},
			&monitorMemoryResults{},
			&monitorDirectObservationInspector{body: body},
			monitorTestAcceptedResolver(binding), 10*time.Minute,
		)
		if err != nil {
			t.Fatal(err)
		}

		_, err = coordinator.ReconcileDirectTerminal(
			context.Background(), source,
		)
		if !errors.Is(err, ErrStaleMonitorSourceVersion) {
			t.Fatalf("stale direct terminal error = %v", err)
		}
		if got := store.bindingValue(); got.State != BindingActive ||
			got.TerminalObservationSnapshotID != nil ||
			got.Revision != stale.Revision {
			t.Fatalf("stale direct terminal mutated binding = %#v", got)
		}
	})
}

func TestMonitorCoordinatorRefusesAStaleOrAlteredSourceVersionBeforeLaunch(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*MonitorSourceBuild)
	}{
		{name: "binding revision", mutate: func(source *MonitorSourceBuild) { source.Version.BindingRevision = "6" }},
		{name: "action digest", mutate: func(source *MonitorSourceBuild) { source.Version.ActionDigest = monitorDigest("f") }},
		{name: "cursor", mutate: func(source *MonitorSourceBuild) { source.Version.Cursor = "" }},
		{name: "source head", mutate: func(source *MonitorSourceBuild) { source.Version.SourceSHA = strings.Repeat("f", 40) }},
		{name: "binding", mutate: func(source *MonitorSourceBuild) { source.BindingID++ }},
	} {
		t.Run(test.name, func(t *testing.T) {
			binding := monitorTestBinding()
			store := newMonitorMemoryStore(binding)
			launcher := &monitorMemoryLauncher{store: store}
			coordinator, err := NewMonitorCoordinator(
				store, launcher, &monitorMemoryResults{},
				&monitorDirectObservationInspector{},
				monitorTestAcceptedResolver(binding), 10*time.Minute,
			)
			if err != nil {
				t.Fatal(err)
			}
			source := monitorTestSourceBuild(binding)
			test.mutate(&source)

			run, launched, err := coordinator.ReserveAndLaunch(
				context.Background(), source,
			)
			if !errors.Is(err, ErrStaleMonitorSourceVersion) ||
				launched || run != 0 {
				t.Fatalf("stale launch = (%d, %t, %v)", run, launched, err)
			}
			if launcher.uniqueRuns() != 0 || store.bindingValue().Active != nil {
				t.Fatal("stale version reached reservation or launch")
			}
		})
	}
}

func TestMonitorCoordinatorRefusesTerminalActionBeforeMutationLaunch(t *testing.T) {
	for _, kind := range []ActionKind{ActionCompleted, ActionAbandoned} {
		t.Run(string(kind), func(t *testing.T) {
			binding := monitorTestBinding()
			store := newMonitorMemoryStore(binding)
			launcher := &monitorMemoryLauncher{store: store}
			coordinator, err := NewMonitorCoordinator(
				store, launcher, &monitorMemoryResults{},
				&monitorDirectObservationInspector{},
				monitorTestAcceptedResolver(binding), 10*time.Minute,
			)
			if err != nil {
				t.Fatal(err)
			}
			source := monitorTestSourceBuildWithKind(
				binding, 301, 41, "cursor-terminal",
				monitorDigest("f"), kind,
			)

			run, launched, err := coordinator.ReserveAndLaunch(
				context.Background(), source,
			)
			if !errors.Is(err, ErrTerminalMonitorAction) ||
				run != 0 || launched {
				t.Fatalf(
					"terminal launch = (%d, %t, %v), want fail-closed reconciliation",
					run, launched, err,
				)
			}
			if store.bindingValue().Active != nil ||
				launcher.uniqueRuns() != 0 {
				t.Fatal("terminal observation reached mutation reservation or launcher")
			}
		})
	}
}

func TestMonitorCoordinatorRejectsAlteredAcceptedReviewAuthorityBeforeReservation(t *testing.T) {
	binding := monitorTestBinding()
	store := newMonitorMemoryStore(binding)
	launcher := &monitorMemoryLauncher{store: store}
	resolver := monitorTestAcceptedResolver(binding)
	resolver.authority.Review.ID++
	coordinator, err := NewMonitorCoordinator(
		store, launcher, &monitorMemoryResults{},
		&monitorDirectObservationInspector{}, resolver, 10*time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}

	run, launched, err := coordinator.ReserveAndLaunch(
		context.Background(), monitorTestSourceBuild(binding),
	)
	if err == nil || run != 0 || launched {
		t.Fatalf("altered accepted authority = (%d, %t, %v)", run, launched, err)
	}
	if store.bindingValue().Active != nil || launcher.uniqueRuns() != 0 {
		t.Fatal("altered accepted authority reached reservation or launch")
	}
}

func monitorTestBinding() Binding {
	pipelineID := 13
	occurrenceID := int64(81)
	return Binding{
		ID: 9, TeamID: 7,
		Locator: Locator{
			Provider: ProviderGitHub, Repository: "acme/widget", ExternalID: "42",
		},
		URL:                         "https://github.example/acme/widget/pull/42",
		SourceRef:                   "refs/heads/change",
		TargetRef:                   "refs/heads/main",
		Destination:                 "github.example/acme/widget",
		ApprovalPolicyVersion:       "engineering/v3",
		MonitorWorkflowDefinitionID: 91, MonitorWorkflowVersion: 3,
		OriginatingPublicationOccurrence: &occurrenceID,
		PipelineID:                       &pipelineID, AcknowledgedCursor: "cursor-0",
		LastObservationSnapshotID: monitorSnapshotID(401),
		LastReconciledSourceSHA:   strings.Repeat("1", 40),
		LastReconciledTargetSHA:   strings.Repeat("2", 40),
		LastReconciledAt:          time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC),
		State:                     BindingActive, Revision: 7,
	}
}

func monitorTestAcceptedResolver(
	binding Binding,
) *monitorMemoryAcceptedReviewResolver {
	authority, err := NewAcceptedReviewAuthority(
		AcceptedReviewAuthoritySpec{
			TeamID:                  binding.TeamID,
			PublicationOccurrenceID: *binding.OriginatingPublicationOccurrence,
			Review: snapshot.SnapshotRef{
				ID: 601, Type: "review/v1",
				Digest: snapshot.Digest("sha256:" + strings.Repeat("6", 64)),
			},
			Candidate: snapshot.SnapshotRef{
				ID: 602, Type: "repository/v1",
				Digest: snapshot.Digest("sha256:" + strings.Repeat("7", 64)),
			},
			Validation: snapshot.SnapshotRef{
				ID: 603, Type: "validation/v1",
				Digest: snapshot.Digest("sha256:" + strings.Repeat("8", 64)),
			},
			ReviewWorkflowRunID: 71,
			OutcomeRevision:     3,
		},
	)
	if err != nil {
		panic(err)
	}
	return &monitorMemoryAcceptedReviewResolver{authority: authority}
}

func monitorDirectTerminalDigest(kind ActionKind) string {
	switch kind {
	case ActionCompleted:
		return "sha256:39dfdd969033d493329ab8e0e09d0049ee8b5f4998dd683ff5e00c36e94f7aa7"
	case ActionAbandoned:
		return "sha256:58defc235d662ef6747e16e1bbe39528cb82511301fc77ab8035f884a85e048f"
	default:
		panic("direct terminal digest requires a terminal action")
	}
}

func monitorDirectTerminalBody(
	binding Binding,
	kind ActionKind,
) contracts.PullRequestBody {
	state := contracts.PullRequestCompleted
	trigger := contracts.PullRequestCompletedTrigger
	if kind == ActionAbandoned {
		state = contracts.PullRequestAbandoned
		trigger = contracts.PullRequestAbandonedTrigger
	}
	return contracts.PullRequestBody{
		Provider: string(binding.Locator.Provider), Repository: binding.Locator.Repository,
		ExternalID: binding.Locator.ExternalID, URL: binding.URL,
		State: state, Mergeability: contracts.PullRequestMergeable,
		SourceRef: binding.SourceRef, SourceSHA: strings.Repeat("3", 40),
		TargetRef: binding.TargetRef, TargetSHA: strings.Repeat("4", 40),
		Iteration: "iteration-terminal", Trigger: trigger,
	}
}

type monitorDirectObservationInspector struct {
	body  contracts.PullRequestBody
	err   error
	calls int
}

func (inspector *monitorDirectObservationInspector) InspectMonitorObservation(
	_ context.Context,
	_ int,
	_ snapshot.SnapshotRef,
) (contracts.PullRequestBody, error) {
	inspector.calls++
	return inspector.body, inspector.err
}

func monitorTestSourceBuild(binding Binding) MonitorSourceBuild {
	return monitorTestSourceBuildWith(
		binding, 301, 41, "cursor-1", monitorDigest("d"),
	)
}

func monitorTestSourceBuildWith(
	binding Binding,
	buildID int,
	admissionID int64,
	cursor string,
	actionDigest string,
) MonitorSourceBuild {
	return monitorTestSourceBuildWithKind(
		binding, buildID, admissionID, cursor, actionDigest,
		ActionReviewBatch,
	)
}

func monitorTestSourceBuildWithKind(
	binding Binding,
	buildID int,
	admissionID int64,
	cursor string,
	actionDigest string,
	actionKind ActionKind,
) MonitorSourceBuild {
	return monitorTestSourceBuildWithKindAndObservation(
		binding, buildID, admissionID, cursor, actionDigest,
		actionKind, 501,
	)
}

func monitorTestSourceBuildWithKindAndObservation(
	binding Binding,
	buildID int,
	admissionID int64,
	cursor string,
	actionDigest string,
	actionKind ActionKind,
	observationID snapshot.SnapshotID,
) MonitorSourceBuild {
	source, err := NewMonitorSourceBuild(MonitorSourceBuildSpec{
		TeamID: binding.TeamID, TeamName: "main", BindingID: binding.ID,
		PipelineID: *binding.PipelineID, BuildID: buildID,
		AdmissionID:          admissionID,
		WorkflowDefinitionID: binding.MonitorWorkflowDefinitionID,
		WorkflowName:         "pr-monitor-v3",
		WorkflowVersion:      binding.MonitorWorkflowVersion,
		Observation: snapshot.SnapshotRef{
			ID: observationID, Type: snapshot.TypeRef("pull-request/v1"),
			Digest: snapshot.Digest("sha256:" + strings.Repeat("a", 64)),
		},
		SelectedVersion: atc.Version{
			"provider": string(binding.Locator.Provider), "external_id": binding.Locator.ExternalID,
			"source_sha": strings.Repeat("3", 40), "target_sha": strings.Repeat("4", 40),
			"action_kind": string(actionKind), "action_digest": actionDigest,
			"cursor": cursor, "binding_revision": strconv.FormatInt(binding.Revision, 10),
		},
	})
	if err != nil {
		panic(err)
	}
	return source
}

func monitorResultFor(
	binding Binding,
	status MonitorRunStatus,
	outcome MonitorOutcome,
) MonitorRunResult {
	if binding.Active == nil || binding.Active.WorkflowRunID == nil {
		panic("monitor result requires attached run")
	}
	return MonitorRunResult{
		TeamID: binding.TeamID, BindingID: binding.ID,
		BindingRevision:       binding.Revision,
		WorkflowRunID:         *binding.Active.WorkflowRunID,
		ActionDigest:          binding.Active.ActionDigest,
		ReservationToken:      binding.Active.Token,
		ObservationSnapshotID: binding.Active.ObservationSnapshotID,
		Cursor:                binding.Active.Cursor,
		SourceSHA:             binding.Active.SourceSHA,
		ReconciledSourceSHA:   binding.Active.SourceSHA,
		TargetSHA:             binding.Active.TargetSHA,
		RunStatus:             status,
		Outcome:               outcome,
	}
}

func monitorDigest(character string) string {
	return "sha256:" + strings.Repeat(character, 64)
}

func monitorSnapshotID(value snapshot.SnapshotID) *snapshot.SnapshotID {
	return &value
}

type monitorMemoryStore struct {
	mu      sync.Mutex
	binding Binding
	audit   []AuditEntry
	token   int
}

func newMonitorMemoryStore(binding Binding) *monitorMemoryStore {
	return &monitorMemoryStore{binding: binding}
}

func (store *monitorMemoryStore) Create(context.Context, CreateBinding) (Binding, bool, error) {
	panic("unexpected Create")
}

func (store *monitorMemoryStore) Get(
	_ context.Context,
	teamID int,
	bindingID int64,
) (Binding, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.binding.TeamID != teamID || store.binding.ID != bindingID {
		return Binding{}, false, nil
	}
	return cloneMonitorMemoryBinding(store.binding), true, nil
}

func (store *monitorMemoryStore) GetByExternal(
	context.Context,
	int,
	Locator,
) (Binding, bool, error) {
	panic("unexpected GetByExternal")
}

func (store *monitorMemoryStore) ReserveLaunch(
	_ context.Context,
	request ReserveLaunch,
) (LaunchReservation, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if request.TeamID != store.binding.TeamID || request.BindingID != store.binding.ID {
		return LaunchReservation{}, false, ErrBindingNotFound
	}
	if store.binding.Active != nil {
		active := store.binding.Active
		if active.BaseRevision == request.ExpectedRevision &&
			active.ActionDigest == request.ActionDigest &&
			active.ObservationSnapshotID == request.ObservationSnapshotID &&
			active.Cursor == request.Cursor &&
			active.SourceSHA == request.SourceSHA &&
			active.TargetSHA == request.TargetSHA {
			return *cloneMonitorReservation(active), true, nil
		}
		return LaunchReservation{}, false, nil
	}
	if store.binding.Revision != request.ExpectedRevision {
		return LaunchReservation{}, false, ErrStaleBindingRevision
	}
	store.token++
	store.binding.Revision++
	store.binding.Active = &LaunchReservation{
		BindingID: request.BindingID, BindingRevision: store.binding.Revision,
		BaseRevision: request.ExpectedRevision, ActionDigest: request.ActionDigest,
		ObservationSnapshotID: request.ObservationSnapshotID, Cursor: request.Cursor,
		SourceSHA: request.SourceSHA, TargetSHA: request.TargetSHA,
		Token:     fmt.Sprintf("reservation-%d", store.token),
		ExpiresAt: time.Now().UTC().Add(request.ExpiresIn),
	}
	return *cloneMonitorReservation(store.binding.Active), true, nil
}

func (store *monitorMemoryStore) AttachRun(
	context.Context,
	AttachRun,
) (Binding, error) {
	panic("monitor launcher must attach atomically")
}

func (store *monitorMemoryStore) attachAtomic(
	request MonitorLaunch,
	runID snapshot.WorkflowRunID,
) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	active := store.binding.Active
	if active == nil || active.Token != request.Reservation.Token ||
		active.ActionDigest != request.Reservation.ActionDigest {
		return ErrReservationMismatch
	}
	if active.WorkflowRunID != nil {
		if *active.WorkflowRunID != runID {
			return ErrReservationMismatch
		}
		return nil
	}
	if store.binding.Revision != request.Reservation.BindingRevision {
		return ErrReservationMismatch
	}
	active.WorkflowRunID = &runID
	store.binding.Revision++
	store.audit = append(store.audit, AuditEntry{
		WorkflowRunID: runID, OriginKind: "pr-monitor",
		OriginReference: strconv.FormatInt(store.binding.ID, 10),
		Status:          "running",
	})
	return nil
}

func (store *monitorMemoryStore) ReleaseLaunch(
	_ context.Context,
	request ReleaseLaunch,
) (Binding, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.binding.Revision != request.ExpectedRevision ||
		store.binding.Active == nil ||
		store.binding.Active.ActionDigest != request.ActionDigest ||
		store.binding.Active.Token != request.ReservationToken {
		return Binding{}, ErrReservationMismatch
	}
	activeRun := store.binding.Active.WorkflowRunID
	if (request.WorkflowRunID == nil) != (activeRun == nil) ||
		request.WorkflowRunID != nil && *request.WorkflowRunID != *activeRun {
		return Binding{}, ErrReservationMismatch
	}
	store.binding.Active = nil
	store.binding.Revision++
	return cloneMonitorMemoryBinding(store.binding), nil
}

func (store *monitorMemoryStore) AcknowledgeAction(
	_ context.Context,
	request AcknowledgeAction,
) (Binding, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.exactActive(request.ExpectedRevision, request.WorkflowRunID, request.ActionDigest, request.ReservationToken); err != nil {
		return Binding{}, err
	}
	store.binding.AcknowledgedCursor = request.Cursor
	store.binding.LastObservationSnapshotID = &request.ObservationSnapshotID
	store.binding.LastAcknowledgedActionDigest = request.ActionDigest
	store.binding.LastAcknowledgedWorkflowRunID = &request.WorkflowRunID
	store.binding.LastReconciledSourceSHA = request.ReconciledSourceSHA
	store.binding.LastReconciledTargetSHA = request.TargetSHA
	store.binding.Active = nil
	store.binding.Revision++
	return cloneMonitorMemoryBinding(store.binding), nil
}

func (store *monitorMemoryStore) MarkAttention(
	_ context.Context,
	teamID int,
	bindingID int64,
	reason string,
) (Binding, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if teamID != store.binding.TeamID || bindingID != store.binding.ID ||
		strings.TrimSpace(reason) == "" {
		return Binding{}, ErrBindingConflict
	}
	store.binding.State = BindingAttentionRequired
	store.binding.AttentionReason = reason
	store.binding.Paused = true
	store.binding.Revision++
	return cloneMonitorMemoryBinding(store.binding), nil
}

func (store *monitorMemoryStore) MarkTerminal(
	_ context.Context,
	request TerminalBinding,
) (Binding, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.exactActive(request.ExpectedRevision, request.WorkflowRunID, request.ActionDigest, request.ReservationToken); err != nil {
		return Binding{}, err
	}
	store.binding.AcknowledgedCursor = request.Cursor
	store.binding.LastObservationSnapshotID = &request.ObservationSnapshotID
	store.binding.LastAcknowledgedActionDigest = request.ActionDigest
	store.binding.LastAcknowledgedWorkflowRunID = &request.WorkflowRunID
	store.binding.LastReconciledSourceSHA = request.SourceSHA
	store.binding.LastReconciledTargetSHA = request.TargetSHA
	store.binding.State = request.State
	store.binding.TerminalObservationSnapshotID = &request.ObservationSnapshotID
	store.binding.Active = nil
	store.binding.Revision++
	return cloneMonitorMemoryBinding(store.binding), nil
}

func (store *monitorMemoryStore) MarkDirectTerminal(
	_ context.Context,
	request DirectTerminalBinding,
) (Binding, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.binding.State.Terminal() {
		if store.binding.State == request.State &&
			store.binding.TerminalObservationSnapshotID != nil &&
			*store.binding.TerminalObservationSnapshotID ==
				request.ObservationSnapshotID &&
			store.binding.LastObservationSnapshotID != nil &&
			*store.binding.LastObservationSnapshotID ==
				request.ObservationSnapshotID &&
			store.binding.AcknowledgedCursor == request.Cursor &&
			store.binding.LastReconciledSourceSHA == request.SourceSHA &&
			store.binding.LastReconciledTargetSHA == request.TargetSHA {
			return cloneMonitorMemoryBinding(store.binding), nil
		}
		return Binding{}, ErrBindingImmutable
	}
	if store.binding.Active != nil {
		return Binding{}, ErrBindingBusy
	}
	if store.binding.Revision != request.ExpectedRevision {
		return Binding{}, ErrStaleBindingRevision
	}
	store.binding.AcknowledgedCursor = request.Cursor
	store.binding.LastObservationSnapshotID = &request.ObservationSnapshotID
	store.binding.LastReconciledSourceSHA = request.SourceSHA
	store.binding.LastReconciledTargetSHA = request.TargetSHA
	store.binding.LastReconciledAt = time.Now().UTC()
	store.binding.State = request.State
	store.binding.TerminalObservationSnapshotID = &request.ObservationSnapshotID
	terminalAt := store.binding.LastReconciledAt
	store.binding.TerminalAt = &terminalAt
	store.binding.Revision++
	return cloneMonitorMemoryBinding(store.binding), nil
}

func (store *monitorMemoryStore) RequestObservation(
	context.Context,
	OperatorRequest,
) (Binding, error) {
	panic("unexpected RequestObservation")
}

func (store *monitorMemoryStore) Pause(context.Context, OperatorRequest) (Binding, error) {
	panic("unexpected Pause")
}

func (store *monitorMemoryStore) Resume(context.Context, OperatorRequest) (Binding, error) {
	panic("unexpected Resume")
}

func (store *monitorMemoryStore) Terminate(context.Context, OperatorRequest) (Binding, error) {
	panic("unexpected Terminate")
}

func (store *monitorMemoryStore) ListAudit(
	context.Context,
	int,
	int64,
	AuditFilter,
) ([]AuditEntry, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return append([]AuditEntry(nil), store.audit...), nil
}

func (store *monitorMemoryStore) ListActive(context.Context, int) ([]Binding, error) {
	panic("unexpected ListActive")
}

func (store *monitorMemoryStore) exactActive(
	revision int64,
	runID snapshot.WorkflowRunID,
	actionDigest string,
	token string,
) error {
	if store.binding.Revision != revision || store.binding.Active == nil ||
		store.binding.Active.WorkflowRunID == nil ||
		*store.binding.Active.WorkflowRunID != runID ||
		store.binding.Active.ActionDigest != actionDigest ||
		store.binding.Active.Token != token {
		return ErrReservationMismatch
	}
	return nil
}

func (store *monitorMemoryStore) bindingValue() Binding {
	store.mu.Lock()
	defer store.mu.Unlock()
	return cloneMonitorMemoryBinding(store.binding)
}

func (store *monitorMemoryStore) auditValue() []AuditEntry {
	store.mu.Lock()
	defer store.mu.Unlock()
	return append([]AuditEntry(nil), store.audit...)
}

func cloneMonitorMemoryBinding(binding Binding) Binding {
	cloned := binding
	cloned.PipelineID = cloneMonitorInt(binding.PipelineID)
	cloned.LastObservationSnapshotID = cloneMonitorSnapshotID(binding.LastObservationSnapshotID)
	cloned.LastAcknowledgedWorkflowRunID = cloneMonitorRunID(binding.LastAcknowledgedWorkflowRunID)
	cloned.TerminalObservationSnapshotID = cloneMonitorSnapshotID(binding.TerminalObservationSnapshotID)
	cloned.Active = cloneMonitorReservation(binding.Active)
	return cloned
}

func cloneMonitorReservation(value *LaunchReservation) *LaunchReservation {
	if value == nil {
		return nil
	}
	cloned := *value
	cloned.WorkflowRunID = cloneMonitorRunID(value.WorkflowRunID)
	return &cloned
}

func cloneMonitorInt(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneMonitorSnapshotID(value *snapshot.SnapshotID) *snapshot.SnapshotID {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneMonitorRunID(value *snapshot.WorkflowRunID) *snapshot.WorkflowRunID {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

type monitorMemoryLauncher struct {
	mu                  sync.Mutex
	store               *monitorMemoryStore
	byToken             map[string]snapshot.WorkflowRunID
	nextRun             snapshot.WorkflowRunID
	fail                error
	attachBeforeFailure bool
}

func (launcher *monitorMemoryLauncher) LaunchMonitor(
	_ context.Context,
	request MonitorLaunch,
) (snapshot.WorkflowRunID, error) {
	launcher.mu.Lock()
	if launcher.byToken == nil {
		launcher.byToken = map[string]snapshot.WorkflowRunID{}
	}
	run, found := launcher.byToken[request.Reservation.Token]
	if !found {
		launcher.nextRun++
		run = 700 + launcher.nextRun
		launcher.byToken[request.Reservation.Token] = run
	}
	fail := launcher.fail
	attach := fail == nil || launcher.attachBeforeFailure
	launcher.mu.Unlock()
	if attach {
		if err := launcher.store.attachAtomic(request, run); err != nil {
			return 0, err
		}
	}
	if fail != nil {
		if attach {
			return run, fail
		}
		return 0, fail
	}
	return run, nil
}

func (launcher *monitorMemoryLauncher) uniqueRuns() int {
	launcher.mu.Lock()
	defer launcher.mu.Unlock()
	return len(launcher.byToken)
}

type monitorMemoryResults struct {
	byRun map[snapshot.WorkflowRunID]MonitorRunResult
}

type monitorMemoryAcceptedReviewResolver struct {
	authority AcceptedReviewAuthority
	err       error
}

func (resolver *monitorMemoryAcceptedReviewResolver) ResolveAcceptedReviewAuthority(
	_ context.Context,
	teamID int,
	publicationOccurrenceID int64,
) (AcceptedReviewAuthority, bool, error) {
	if resolver.err != nil {
		return AcceptedReviewAuthority{}, false, resolver.err
	}
	protected, err := resolver.authority.Protected()
	if err != nil {
		return resolver.authority, true, nil
	}
	if protected.TeamID != teamID ||
		protected.PublicationOccurrenceID != publicationOccurrenceID {
		return AcceptedReviewAuthority{}, false, nil
	}
	return resolver.authority, true, nil
}

func (results *monitorMemoryResults) InspectMonitorRun(
	_ context.Context,
	teamID int,
	runID snapshot.WorkflowRunID,
) (MonitorRunResult, bool, error) {
	result, found := results.byRun[runID]
	if !found {
		return MonitorRunResult{}, false, nil
	}
	if result.TeamID != teamID {
		return MonitorRunResult{}, false, errors.New("team drift")
	}
	return result, true, nil
}

var _ BindingStore = (*monitorMemoryStore)(nil)
var _ MonitorRunLauncher = (*monitorMemoryLauncher)(nil)
var _ MonitorRunInspector = (*monitorMemoryResults)(nil)
