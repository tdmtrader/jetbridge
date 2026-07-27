package budget_test

import (
	"testing"
	"time"

	"github.com/concourse/concourse/agent/budget"
	"github.com/concourse/concourse/agent/budget/budgettest"
)

func newChecker(t *testing.T, dailyCap float64, entries []budget.LedgerEntry) budget.Checker {
	t.Helper()
	ledger := budgettest.NewMemoryLedger()
	for _, e := range entries {
		if err := ledger.Insert(e); err != nil {
			t.Fatal(err)
		}
	}
	fixedNow := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	return budget.NewChecker(ledger, budget.Config{
		GlobalDailyCapUSD: dailyCap,
		Location:          time.UTC,
		Now:               func() time.Time { return fixedNow },
	})
}

func runEntry(runID int64, cost float64, at time.Time) budget.LedgerEntry {
	id := runID
	return budget.LedgerEntry{
		WorkflowRunID: &id, FunctionID: "implement",
		Source: budget.SourceAgentStep, CostUSD: cost, OccurredAt: at,
	}
}

func TestGlobalDailyRemaining(t *testing.T) {
	today := time.Date(2026, 7, 8, 9, 0, 0, 0, time.UTC)
	yesterday := time.Date(2026, 7, 7, 23, 0, 0, 0, time.UTC)
	c := newChecker(t, 50, []budget.LedgerEntry{
		{Source: budget.SourceCIAgent, CostUSD: 12.5, OccurredAt: today},
		{Source: budget.SourceCIAgent, CostUSD: 100, OccurredAt: yesterday}, // before local midnight
	})
	r, err := c.GlobalDailyRemaining()
	if err != nil {
		t.Fatal(err)
	}
	if r.LimitUSD != 50 || r.SpentUSD != 12.5 || r.RemainingUSD != 37.5 || r.Exhausted {
		t.Fatalf("got %+v", r)
	}

	uncapped := newChecker(t, 0, []budget.LedgerEntry{{Source: budget.SourceCIAgent, CostUSD: 12.5, OccurredAt: today}})
	r, err = uncapped.GlobalDailyRemaining()
	if err != nil {
		t.Fatal(err)
	}
	if r.LimitUSD != 0 || r.Exhausted || r.SpentUSD != 12.5 {
		t.Fatalf("cap 0 must be uncapped, got %+v", r)
	}
}

func TestGlobalDailyRemainingCountsEverySource(t *testing.T) {
	// The daily cap is the only budget this package owns, and it is a
	// platform-wide cap: every source counts toward it.
	at := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	c := newChecker(t, 10, []budget.LedgerEntry{
		runEntry(7, 4.0, at),
		{Source: budget.SourceCIAgent, CostUSD: 2.0, OccurredAt: at},
	})
	r, err := c.GlobalDailyRemaining()
	if err != nil {
		t.Fatal(err)
	}
	if r.SpentUSD != 6.0 || r.RemainingUSD != 4.0 || r.Exhausted {
		t.Fatalf("got %+v", r)
	}
}

func TestGlobalDailyExhausted(t *testing.T) {
	at := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	c := newChecker(t, 3, []budget.LedgerEntry{runEntry(7, 3.5, at)})
	r, err := c.GlobalDailyRemaining()
	if err != nil {
		t.Fatal(err)
	}
	if !r.Exhausted || r.RemainingUSD != -0.5 {
		t.Fatalf("got %+v", r)
	}
}

func TestRecordValidates(t *testing.T) {
	c := newChecker(t, 0, nil)
	if err := c.Record(budget.LedgerEntry{Source: "made-up", CostUSD: 1}); err == nil {
		t.Fatal("invalid source accepted")
	}
	// the v2 sources (gateway/harvest_judge/retrospective/probe) named
	// subsystems that no longer exist and are no longer writable
	for _, retired := range []string{"gateway", "harvest_judge", "retrospective", "probe"} {
		if err := c.Record(budget.LedgerEntry{Source: retired, CostUSD: 1}); err == nil {
			t.Fatalf("retired source %q accepted", retired)
		}
	}
	if err := c.Record(budget.LedgerEntry{Source: budget.SourceAgentStep, CostUSD: -1}); err == nil {
		t.Fatal("negative cost accepted")
	}
	if err := c.Record(budget.LedgerEntry{Source: budget.SourceAgentStep, CostUSD: 0.25}); err != nil {
		t.Fatalf("valid entry rejected: %v", err)
	}
}

func TestValidGroupByAcceptsModelAndStep(t *testing.T) {
	for _, g := range []string{budget.GroupByModel, budget.GroupByStep} {
		if !budget.ValidGroupBy(g) {
			t.Fatalf("ValidGroupBy(%q) = false, want true", g)
		}
	}
	if budget.GroupByModel != "model" || budget.GroupByStep != "step" {
		t.Fatalf("group_by tokens drifted: model=%q step=%q", budget.GroupByModel, budget.GroupByStep)
	}
	if budget.ValidGroupBy("ticket") {
		t.Fatal("group_by=ticket must no longer be accepted")
	}
}

func TestMemoryLedgerRollupByModelAndStep(t *testing.T) {
	m := &budgettest.MemoryLedger{}
	base := time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(m.Insert(budget.LedgerEntry{OccurredAt: base, Source: budget.SourceAgentStep, Model: "opus", StepName: "implement", CostUSD: 1.0}))
	must(m.Insert(budget.LedgerEntry{OccurredAt: base, Source: budget.SourceAgentStep, Model: "opus", StepName: "gates", CostUSD: 2.0}))
	must(m.Insert(budget.LedgerEntry{OccurredAt: base, Source: budget.SourceAgentStep, Model: "sonnet", StepName: "implement", CostUSD: 4.0}))

	byModel, err := m.Rollup(budget.GroupByModel, base.Add(-time.Hour), time.Time{})
	must(err)
	got := map[string]float64{}
	for _, r := range byModel {
		got[r.Key] = r.CostUSD
	}
	if got["opus"] != 3.0 || got["sonnet"] != 4.0 {
		t.Fatalf("by model = %+v, want opus=3 sonnet=4", got)
	}

	byStep, err := m.Rollup(budget.GroupByStep, base.Add(-time.Hour), time.Time{})
	must(err)
	got = map[string]float64{}
	for _, r := range byStep {
		got[r.Key] = r.CostUSD
	}
	if got["implement"] != 5.0 || got["gates"] != 2.0 {
		t.Fatalf("by step = %+v, want implement=5 gates=2", got)
	}
}

func TestMemoryLedgerRollup(t *testing.T) {
	ledger := budgettest.NewMemoryLedger()
	day1 := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	entries := []budget.LedgerEntry{
		{Source: budget.SourceCIAgent, UserName: "alice", CostUSD: 1, Turns: 2, OccurredAt: day1},
		{Source: budget.SourceCIAgent, UserName: "alice", CostUSD: 2, Turns: 3, OccurredAt: day2},
		runEntry(91, 4, day2),
	}
	for _, e := range entries {
		if err := ledger.Insert(e); err != nil {
			t.Fatal(err)
		}
	}

	rows, err := ledger.Rollup(budget.GroupByDay, day1.Add(-time.Hour), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Key != "2026-07-07" || rows[1].CostUSD != 6 {
		t.Fatalf("day rollup: %+v", rows)
	}

	rows, _ = ledger.Rollup(budget.GroupByUser, day1.Add(-time.Hour), time.Time{})
	if len(rows) != 2 {
		t.Fatalf("user rollup: %+v", rows)
	}

	rows, _ = ledger.Rollup(budget.GroupByWorkflow, day1.Add(-time.Hour), time.Time{})
	found := false
	for _, r := range rows {
		if r.Key == "91" && r.CostUSD == 4 {
			found = true
		}
	}
	if !found {
		t.Fatalf("workflow rollup missing the run-attributed spend: %+v", rows)
	}
}
