package occurrence

import (
	"testing"

	"github.com/concourse/concourse/atc/db"
)

// An await or publish node can die — or finish — long before it writes its own
// durable row: await_snapshot returns on validation, authorization, snapshot
// availability and the reapproval-not-required fast path all ahead of
// waits.CreateOrGet, and publish_snapshot has many error returns (a rejected
// merge approval among them) ahead of the agent_publication_occurrences insert.
// The engine wraps both in exec.LogError, so the build step's own outcome IS
// recorded. Freezing 'pending' anyway — the projection's word for "never
// reached" — hid the node that killed the run, permanently, because the frozen
// row is immutable.
func TestDeriveAwaitAndPublishFallBackToBuildStepStatus(t *testing.T) {
	for name, expectation := range map[string]struct {
		build Status
		want  Status
	}{
		"errored before writing its row": {build: StatusErrored, want: StatusErrored},
		"failed before writing its row":  {build: StatusFailed, want: StatusFailed},
		"succeeded without a row":        {build: StatusSucceeded, want: StatusSucceeded},
	} {
		t.Run(name, func(t *testing.T) {
			sources := mergeDeliverySources(t)
			sources.BuildStepStatus = map[string]Status{
				planIDOf(t, sources.Run.ActualPlan, "merge-approval"): expectation.build,
				planIDOf(t, sources.Run.ActualPlan, "land-merge"):     expectation.build,
			}

			occurrences, err := Derive(sources)
			if err != nil {
				t.Fatalf("Derive returned an error: %v", err)
			}
			for _, nodeID := range []string{"merge-approval", "land-merge"} {
				got, found := findOccurrence(occurrences, nodeID)
				if !found {
					t.Fatalf("expected %q to be projected", nodeID)
				}
				if got.Status != expectation.want {
					t.Fatalf("%q: expected %q from the build step, got %q",
						nodeID, expectation.want, got.Status)
				}
				if got.WaitID != nil || got.PublicationID != nil {
					t.Fatalf("%q: there is no durable row to point at: %+v", nodeID, got)
				}
			}
		})
	}
}

// The wait or publication row still wins when it exists: build step state is
// the fallback, not an override.
func TestDeriveOwnEvidenceOutranksBuildStepStatus(t *testing.T) {
	sources := mergeDeliverySources(t)
	awaitPlanID := planIDOf(t, sources.Run.ActualPlan, "merge-approval")
	sources.Waits = []Wait{{ID: 7, PlanID: awaitPlanID, Status: "resolved"}}
	sources.BuildStepStatus = map[string]Status{awaitPlanID: StatusErrored}

	occurrences, err := Derive(sources)
	if err != nil {
		t.Fatalf("Derive returned an error: %v", err)
	}
	got, _ := findOccurrence(occurrences, "merge-approval")
	if got.Status != StatusSucceeded {
		t.Fatalf("expected the wait's own resolution to win, got %q", got.Status)
	}
}

// A wait left 'waiting' or a publication left 'pending' by a build that was
// aborted, errored or drained is not work in flight — the process that would
// have advanced it is gone. Freezing the live value pinned the finished run in
// the attention lens forever, because the frozen row is immutable and nothing
// supersedes a live occurrence.
func TestDeriveSettlesLiveEvidenceAgainstATerminalRun(t *testing.T) {
	for _, status := range []db.AgentWorkflowRunStatus{
		db.AgentWorkflowRunStatusAborted,
		db.AgentWorkflowRunStatusErrored,
		db.AgentWorkflowRunStatusFailed,
		db.AgentWorkflowRunStatusSucceeded,
	} {
		t.Run(string(status), func(t *testing.T) {
			sources := mergeDeliverySources(t)
			sources.Run.Status = status
			sources.Waits = []Wait{{
				ID: 7, PlanID: planIDOf(t, sources.Run.ActualPlan, "merge-approval"),
				Status: "waiting", TimeoutPolicy: "fail",
			}}
			sources.Publications = []Publication{{
				ID: 11, PlanID: planIDOf(t, sources.Run.ActualPlan, "land-merge"),
				Status: "pending",
			}}

			occurrences, err := Derive(sources)
			if err != nil {
				t.Fatalf("Derive returned an error: %v", err)
			}
			for _, nodeID := range []string{"merge-approval", "land-merge"} {
				got, _ := findOccurrence(occurrences, nodeID)
				if got.Status != StatusAborted {
					t.Fatalf("%q: a finished run cannot carry work in flight, got %q",
						nodeID, got.Status)
				}
				// We know it never completed; we do not know when it stopped,
				// so no completion time is invented for it.
				if got.CompletedAt != nil || got.DurationSeconds != 0 {
					t.Fatalf("%q: invented a completion: %+v", nodeID, got)
				}
			}
		})
	}
}

// The same evidence on a run that is still executing is exactly what the
// overview must show, so the settling is conditional on the run, not on the
// evidence.
func TestDeriveLeavesLiveEvidenceAloneOnAnActiveRun(t *testing.T) {
	for _, status := range []db.AgentWorkflowRunStatus{
		db.AgentWorkflowRunStatusAdmitting,
		db.AgentWorkflowRunStatusRunning,
		db.AgentWorkflowRunStatusCanceling,
	} {
		t.Run(string(status), func(t *testing.T) {
			sources := mergeDeliverySources(t)
			sources.Run.Status = status
			sources.Waits = []Wait{{
				ID: 7, PlanID: planIDOf(t, sources.Run.ActualPlan, "merge-approval"),
				Status: "waiting", TimeoutPolicy: "fail",
			}}
			sources.Publications = []Publication{{
				ID: 11, PlanID: planIDOf(t, sources.Run.ActualPlan, "land-merge"),
				Status: "pending",
			}}
			occurrences, err := Derive(sources)
			if err != nil {
				t.Fatalf("Derive returned an error: %v", err)
			}
			approval, _ := findOccurrence(occurrences, "merge-approval")
			if approval.Status != StatusWaiting {
				t.Fatalf("expected waiting on an active run, got %q", approval.Status)
			}
			ship, _ := findOccurrence(occurrences, "land-merge")
			if ship.Status != StatusRunning {
				t.Fatalf("expected running on an active run, got %q", ship.Status)
			}
		})
	}
}

// Pending means "never reached", which stays true on a finished run. Settling
// it too would turn every unreached node of every terminal run into an
// attention-worthy failure.
func TestDeriveLeavesPendingAloneOnATerminalRun(t *testing.T) {
	sources := mergeDeliverySources(t)
	sources.Run.Status = db.AgentWorkflowRunStatusErrored

	occurrences, err := Derive(sources)
	if err != nil {
		t.Fatalf("Derive returned an error: %v", err)
	}
	for _, got := range occurrences {
		if got.Status != StatusPending {
			t.Fatalf("a run with no evidence is entirely pending, got %+v", got)
		}
	}
}
