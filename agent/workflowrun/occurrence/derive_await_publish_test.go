package occurrence

import (
	"testing"
	"time"

	"github.com/concourse/concourse/atc/db"
)

// mergeDeliverySources builds Sources over the real merge-delivery-v3 seed,
// which is the shipped workflow that carries both an await and a publish node.
func mergeDeliverySources(t *testing.T) Sources {
	t.Helper()
	compiled := compileSeed(t, "merge-delivery-v3")
	return Sources{
		Run: db.AgentWorkflowRun{
			ID:                   42,
			TeamID:               1,
			WorkflowName:         compiled.Name,
			WorkflowDefinitionID: 41,
			WorkflowVersion:      3,
			ActualPlan:           planSeed(t, compiled),
		},
	}
}

func TestDeriveAwaitAndPublishOccurrences(t *testing.T) {
	created := time.Date(2026, 7, 31, 10, 0, 0, 0, time.UTC)

	sources := mergeDeliverySources(t)
	awaitPlanID := planIDOf(t, sources.Run.ActualPlan, "merge-approval")
	publishPlanID := planIDOf(t, sources.Run.ActualPlan, "land-merge")

	sources.Waits = []Wait{{
		ID: 7, PlanID: awaitPlanID, OutputName: "merge-approval",
		Status: "waiting", TimeoutPolicy: "fail", CreatedAt: created,
	}}
	sources.Publications = []Publication{{
		ID: 11, PlanID: publishPlanID, Status: "succeeded",
		CreatedAt: created, UpdatedAt: created.Add(time.Minute),
	}}

	occurrences, err := Derive(sources)
	if err != nil {
		t.Fatalf("Derive returned an error: %v", err)
	}

	approval, found := findOccurrence(occurrences, "merge-approval")
	if !found {
		t.Fatalf("expected an await occurrence, got %+v", occurrences)
	}
	if approval.NodeKind != KindAwait {
		t.Fatalf("expected the await kind, got %+v", approval)
	}
	if approval.Status != StatusWaiting {
		t.Fatalf("expected the unresolved wait to be waiting, got %q", approval.Status)
	}
	if approval.WaitID == nil || *approval.WaitID != 7 {
		t.Fatalf("expected the wait detail pointer, got %+v", approval.WaitID)
	}
	if approval.CompletedAt != nil {
		t.Fatalf("an unresolved wait has not completed: %+v", approval)
	}

	ship, found := findOccurrence(occurrences, "land-merge")
	if !found {
		t.Fatalf("expected a publish occurrence, got %+v", occurrences)
	}
	if ship.NodeKind != KindPublish {
		t.Fatalf("expected the publish kind, got %+v", ship)
	}
	if ship.Status != StatusSucceeded {
		t.Fatalf("expected the publication to be succeeded, got %q", ship.Status)
	}
	if ship.PublicationID == nil || *ship.PublicationID != 11 {
		t.Fatalf("expected the publication detail pointer, got %+v", ship.PublicationID)
	}
	if ship.CompletedAt == nil || ship.DurationSeconds != 60 {
		t.Fatalf("expected a completed publication of 60 seconds, got %+v", ship)
	}
}

func TestDeriveMapsWaitResolutionStatuses(t *testing.T) {
	resolved := time.Date(2026, 7, 31, 11, 0, 0, 0, time.UTC)
	for waitStatus, want := range map[string]Status{
		"waiting":   StatusWaiting,
		"resolved":  StatusSucceeded,
		"timed_out": StatusFailed,
		"cancelled": StatusAborted,
	} {
		sources := mergeDeliverySources(t)
		sources.Waits = []Wait{{
			ID: 7, PlanID: planIDOf(t, sources.Run.ActualPlan, "merge-approval"),
			OutputName: "merge-approval", Status: waitStatus,
			TimeoutPolicy: "fail", ResolvedAt: &resolved,
		}}
		occurrences, err := Derive(sources)
		if err != nil {
			t.Fatalf("Derive returned an error: %v", err)
		}
		got, _ := findOccurrence(occurrences, "merge-approval")
		if got.Status != want {
			t.Fatalf("wait status %q: expected %q, got %q", waitStatus, want, got.Status)
		}
	}
}

// A wait that expires under timeout_policy 'default' resolves onto its
// declared default snapshot and the workflow proceeds normally, but
// atc/db/agent_workflow_waits_factory.go records status 'timed_out' for that
// case exactly as it does for policy 'fail'. Mapping on status alone would
// paint a by-design success red — the same false-alarm class the 'incomplete'
// mapping exists to avoid.
func TestDeriveTimedOutWaitUnderDefaultPolicyIsNotAFailure(t *testing.T) {
	resolved := time.Date(2026, 7, 31, 11, 0, 0, 0, time.UTC)
	created := resolved.Add(-30 * time.Minute)

	sources := mergeDeliverySources(t)
	sources.Waits = []Wait{{
		ID: 7, PlanID: planIDOf(t, sources.Run.ActualPlan, "merge-approval"),
		OutputName: "merge-approval", Status: "timed_out", TimeoutPolicy: "default",
		CreatedAt: created, ResolvedAt: &resolved,
	}}

	occurrences, err := Derive(sources)
	if err != nil {
		t.Fatalf("Derive returned an error: %v", err)
	}
	got, _ := findOccurrence(occurrences, "merge-approval")
	if got.Status != StatusSucceeded {
		t.Fatalf("a wait that defaulted on timeout is not a failure, got %q", got.Status)
	}
	if got.CompletedAt == nil || got.DurationSeconds != 1800 {
		t.Fatalf("expected a completed wait of 1800 seconds, got %+v", got)
	}
}

// 'stale_base' and 'rebase_required' are unresolved outbound effects that need
// human attention, so they surface as failures rather than as pending.
func TestDeriveMapsPublicationStatuses(t *testing.T) {
	for publicationStatus, want := range map[string]Status{
		"pending":         StatusRunning,
		"succeeded":       StatusSucceeded,
		"failed":          StatusFailed,
		"stale_base":      StatusFailed,
		"rebase_required": StatusFailed,
	} {
		sources := mergeDeliverySources(t)
		sources.Publications = []Publication{{
			ID: 11, PlanID: planIDOf(t, sources.Run.ActualPlan, "land-merge"),
			Status: publicationStatus,
		}}
		occurrences, err := Derive(sources)
		if err != nil {
			t.Fatalf("Derive returned an error: %v", err)
		}
		got, _ := findOccurrence(occurrences, "land-merge")
		if got.Status != want {
			t.Fatalf("publication status %q: expected %q, got %q", publicationStatus, want, got.Status)
		}
	}
}

// A publication still in flight has not completed, so it carries no completion
// time even though its updated_at has moved.
func TestDerivePendingPublicationIsNotCompleted(t *testing.T) {
	created := time.Date(2026, 7, 31, 10, 0, 0, 0, time.UTC)
	sources := mergeDeliverySources(t)
	sources.Publications = []Publication{{
		ID: 11, PlanID: planIDOf(t, sources.Run.ActualPlan, "land-merge"),
		Status: "pending", CreatedAt: created, UpdatedAt: created.Add(time.Minute),
	}}
	occurrences, err := Derive(sources)
	if err != nil {
		t.Fatalf("Derive returned an error: %v", err)
	}
	got, _ := findOccurrence(occurrences, "land-merge")
	if got.Status != StatusRunning {
		t.Fatalf("expected running, got %q", got.Status)
	}
	if got.CompletedAt != nil || got.DurationSeconds != 0 {
		t.Fatalf("an in-flight publication is not completed: %+v", got)
	}
}

// Await and publish nodes a run never reached are pending, like every other
// node with no evidence.
func TestDeriveAwaitAndPublishWithoutEvidenceArePending(t *testing.T) {
	occurrences, err := Derive(mergeDeliverySources(t))
	if err != nil {
		t.Fatalf("Derive returned an error: %v", err)
	}
	for _, nodeID := range []string{"merge-approval", "land-merge"} {
		got, found := findOccurrence(occurrences, nodeID)
		if !found {
			t.Fatalf("expected %q to be projected", nodeID)
		}
		if got.Status != StatusPending {
			t.Fatalf("%q: expected pending, got %q", nodeID, got.Status)
		}
		if got.WaitID != nil || got.PublicationID != nil {
			t.Fatalf("%q: a node with no evidence has no detail pointer: %+v", nodeID, got)
		}
	}
}

// Wait and publication evidence is matched by plan ID, so evidence belonging
// to another node's plan must not leak onto this one.
func TestDeriveIgnoresEvidenceForOtherPlanIDs(t *testing.T) {
	sources := mergeDeliverySources(t)
	sources.Waits = []Wait{{ID: 7, PlanID: "not-a-plan-id", Status: "resolved"}}
	sources.Publications = []Publication{{ID: 11, PlanID: "not-a-plan-id", Status: "succeeded"}}

	occurrences, err := Derive(sources)
	if err != nil {
		t.Fatalf("Derive returned an error: %v", err)
	}
	for _, nodeID := range []string{"merge-approval", "land-merge"} {
		got, _ := findOccurrence(occurrences, nodeID)
		if got.Status != StatusPending {
			t.Fatalf("%q: expected pending, got %q", nodeID, got.Status)
		}
	}
}

// A resolution timestamp alone does not make an occurrence complete. A wait
// whose status the projection cannot interpret is pending, and a pending
// occurrence must not be stamped as finished just because the row carries a
// resolved_at — that would report a completion the projection cannot vouch
// for.
func TestDeriveDoesNotCompleteANonTerminalWaitThatCarriesAResolutionTime(t *testing.T) {
	resolved := time.Date(2026, 7, 31, 11, 0, 0, 0, time.UTC)
	sources := mergeDeliverySources(t)
	sources.Waits = []Wait{{
		ID: 7, PlanID: planIDOf(t, sources.Run.ActualPlan, "merge-approval"),
		Status: "brand-new", CreatedAt: resolved.Add(-time.Hour), ResolvedAt: &resolved,
	}}

	occurrences, err := Derive(sources)
	if err != nil {
		t.Fatalf("Derive returned an error: %v", err)
	}
	got, _ := findOccurrence(occurrences, "merge-approval")
	if got.Status != StatusPending {
		t.Fatalf("expected pending, got %q", got.Status)
	}
	if got.CompletedAt != nil || got.DurationSeconds != 0 {
		t.Fatalf("a non-terminal wait must not be reported as completed: %+v", got)
	}
}

// An unknown status from either table must degrade to pending rather than be
// guessed at, so a schema addition cannot silently render as success.
func TestDeriveDegradesUnknownEvidenceStatusesToPending(t *testing.T) {
	sources := mergeDeliverySources(t)
	sources.Waits = []Wait{{
		ID: 7, PlanID: planIDOf(t, sources.Run.ActualPlan, "merge-approval"), Status: "brand-new",
	}}
	sources.Publications = []Publication{{
		ID: 11, PlanID: planIDOf(t, sources.Run.ActualPlan, "land-merge"), Status: "brand-new",
	}}
	occurrences, err := Derive(sources)
	if err != nil {
		t.Fatalf("Derive returned an error: %v", err)
	}
	for _, nodeID := range []string{"merge-approval", "land-merge"} {
		got, _ := findOccurrence(occurrences, nodeID)
		if got.Status != StatusPending {
			t.Fatalf("%q: expected pending for an unknown status, got %q", nodeID, got.Status)
		}
	}
}
