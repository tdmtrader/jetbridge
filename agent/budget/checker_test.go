package budget_test

import (
	"testing"
	"time"

	"github.com/concourse/concourse/agent/budget"
)

type fixedBudgets struct {
	budgets map[int]float64
}

func (f fixedBudgets) BudgetUSD(ticketID int) (float64, bool, error) {
	b, ok := f.budgets[ticketID]
	return b, ok, nil
}

func newChecker(t *testing.T, dailyCap float64, budgets map[int]float64, entries []budget.LedgerEntry) budget.Checker {
	t.Helper()
	ledger := budget.NewMemoryLedger()
	for _, e := range entries {
		if err := ledger.Insert(e); err != nil {
			t.Fatal(err)
		}
	}
	fixedNow := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	return budget.NewChecker(ledger, fixedBudgets{budgets}, budget.Config{
		GlobalDailyCapUSD: dailyCap,
		Location:          time.UTC,
		Now:               func() time.Time { return fixedNow },
	})
}

func ticketEntry(ticketID int, cost float64, at time.Time) budget.LedgerEntry {
	tid := ticketID
	return budget.LedgerEntry{TicketID: &tid, Source: budget.SourceAgentStep, CostUSD: cost, OccurredAt: at}
}

func TestTicketRemaining(t *testing.T) {
	at := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	c := newChecker(t, 0, map[int]float64{7: 10.0}, []budget.LedgerEntry{
		ticketEntry(7, 4.0, at),
		ticketEntry(8, 99.0, at),
	})

	r, err := c.TicketRemaining(7)
	if err != nil {
		t.Fatal(err)
	}
	if r.LimitUSD != 10.0 || r.SpentUSD != 4.0 || r.RemainingUSD != 6.0 || r.Exhausted {
		t.Fatalf("got %+v", r)
	}

	r, err = c.TicketRemaining(99) // unknown ticket -> uncapped
	if err != nil {
		t.Fatal(err)
	}
	if r.LimitUSD != 0 || r.Exhausted {
		t.Fatalf("unknown ticket must be uncapped, got %+v", r)
	}
}

func TestTicketExhausted(t *testing.T) {
	at := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	c := newChecker(t, 0, map[int]float64{7: 3.0}, []budget.LedgerEntry{ticketEntry(7, 3.5, at)})
	r, err := c.TicketRemaining(7)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Exhausted || r.RemainingUSD != -0.5 {
		t.Fatalf("got %+v", r)
	}
}

func TestTicketRemainingExcludesHarvestJudgeSpend(t *testing.T) {
	// §1.13: judge spend is capped separately (workflow judge_usd) and must
	// never deplete the ticket budget — the judge runs precisely when the
	// agent may have burned everything.
	at := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	tid := 7
	c := newChecker(t, 0, map[int]float64{7: 10.0}, []budget.LedgerEntry{
		ticketEntry(7, 4.0, at),
		{TicketID: &tid, Source: budget.SourceHarvestJudge, CostUSD: 3.0, OccurredAt: at},
	})
	r, err := c.TicketRemaining(7)
	if err != nil {
		t.Fatal(err)
	}
	if r.SpentUSD != 4.0 || r.RemainingUSD != 6.0 {
		t.Fatalf("judge spend leaked into the ticket budget: %+v", r)
	}

	// The daily window still counts ALL sources, judge included.
	capped := newChecker(t, 50, nil, []budget.LedgerEntry{
		{TicketID: &tid, Source: budget.SourceHarvestJudge, CostUSD: 3.0, OccurredAt: at},
		{Source: budget.SourceCIAgent, CostUSD: 2.0, OccurredAt: at},
	})
	daily, err := capped.GlobalDailyRemaining()
	if err != nil {
		t.Fatal(err)
	}
	if daily.SpentUSD != 5.0 {
		t.Fatalf("daily cap must include judge spend: %+v", daily)
	}
}

func TestGlobalDailyRemaining(t *testing.T) {
	today := time.Date(2026, 7, 8, 9, 0, 0, 0, time.UTC)
	yesterday := time.Date(2026, 7, 7, 23, 0, 0, 0, time.UTC)
	c := newChecker(t, 50, nil, []budget.LedgerEntry{
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

	uncapped := newChecker(t, 0, nil, []budget.LedgerEntry{{Source: budget.SourceCIAgent, CostUSD: 12.5, OccurredAt: today}})
	r, err = uncapped.GlobalDailyRemaining()
	if err != nil {
		t.Fatal(err)
	}
	if r.LimitUSD != 0 || r.Exhausted || r.SpentUSD != 12.5 {
		t.Fatalf("cap 0 must be uncapped, got %+v", r)
	}
}

func TestStepSlice(t *testing.T) {
	at := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	c := newChecker(t, 0, map[int]float64{7: 10.0}, []budget.LedgerEntry{ticketEntry(7, 8.0, at)})

	// slice smaller than ticket remaining -> slice wins
	r, err := c.StepSlice(7, 1.0)
	if err != nil {
		t.Fatal(err)
	}
	if r.RemainingUSD != 1.0 || r.Exhausted {
		t.Fatalf("got %+v", r)
	}

	// slice larger than ticket remaining -> ticket remaining wins
	r, _ = c.StepSlice(7, 5.0)
	if r.RemainingUSD != 2.0 || r.Exhausted {
		t.Fatalf("got %+v", r)
	}

	// zero slice -> inherit ticket remaining
	r, _ = c.StepSlice(7, 0)
	if r.LimitUSD != 10.0 || r.RemainingUSD != 2.0 {
		t.Fatalf("got %+v", r)
	}

	// uncapped ticket + explicit slice -> slice is the cap
	r, _ = c.StepSlice(42, 2.5)
	if r.LimitUSD != 2.5 || r.RemainingUSD != 2.5 || r.Exhausted {
		t.Fatalf("got %+v", r)
	}
}

func TestStepSliceExhaustedTicket(t *testing.T) {
	at := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	c := newChecker(t, 0, map[int]float64{7: 3.0}, []budget.LedgerEntry{ticketEntry(7, 3.0, at)})
	r, err := c.StepSlice(7, 1.0)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Exhausted {
		t.Fatalf("expected exhausted slice, got %+v", r)
	}
}

func TestRecordValidates(t *testing.T) {
	c := newChecker(t, 0, nil, nil)
	if err := c.Record(budget.LedgerEntry{Source: "made-up", CostUSD: 1}); err == nil {
		t.Fatal("invalid source accepted")
	}
	if err := c.Record(budget.LedgerEntry{Source: budget.SourceProbe, CostUSD: -1}); err == nil {
		t.Fatal("negative cost accepted")
	}
	if err := c.Record(budget.LedgerEntry{Source: budget.SourceProbe, CostUSD: 0.25}); err != nil {
		t.Fatalf("valid entry rejected: %v", err)
	}
}

func TestMemoryLedgerRollup(t *testing.T) {
	ledger := budget.NewMemoryLedger()
	day1 := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	entries := []budget.LedgerEntry{
		{Source: budget.SourceCIAgent, UserName: "alice", CostUSD: 1, Turns: 2, OccurredAt: day1},
		{Source: budget.SourceCIAgent, UserName: "alice", CostUSD: 2, Turns: 3, OccurredAt: day2},
		{Source: budget.SourceCIAgent, UserName: "bob", CostUSD: 4, Turns: 1, OccurredAt: day2,
			Metadata: []byte(`{"workflow":"review@1"}`)},
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
		if r.Key == "review@1" && r.CostUSD == 4 {
			found = true
		}
	}
	if !found {
		t.Fatalf("workflow rollup missing metadata key: %+v", rows)
	}
}
