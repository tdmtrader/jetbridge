package budget

import (
	"errors"
	"fmt"
	"math"
	"time"
)

// ErrInvalidEntry marks Record validation failures so HTTP handlers can
// map them to 400 rather than 500.
var ErrInvalidEntry = errors.New("invalid ledger entry")

func NewChecker(ledger Ledger, budgets TicketBudgets, cfg Config) Checker {
	if cfg.Location == nil {
		cfg.Location = time.Local
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &checker{ledger: ledger, budgets: budgets, cfg: cfg}
}

type checker struct {
	ledger  Ledger
	budgets TicketBudgets
	cfg     Config
}

func (c *checker) TicketRemaining(ticketID int) (Remaining, error) {
	spent, err := c.ledger.SpentForTicket(ticketID)
	if err != nil {
		return Remaining{}, err
	}
	limit, found, err := c.budgets.BudgetUSD(ticketID)
	if err != nil {
		return Remaining{}, err
	}
	if !found || limit <= 0 {
		return Remaining{SpentUSD: spent}, nil // uncapped
	}
	remaining := limit - spent
	return Remaining{
		LimitUSD:     limit,
		SpentUSD:     spent,
		RemainingUSD: remaining,
		Exhausted:    remaining <= 0,
	}, nil
}

func (c *checker) GlobalDailyRemaining() (Remaining, error) {
	now := c.cfg.Now().In(c.cfg.Location)
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, c.cfg.Location)
	spent, err := c.ledger.SpentSince(midnight)
	if err != nil {
		return Remaining{}, err
	}
	if c.cfg.GlobalDailyCapUSD <= 0 {
		return Remaining{SpentUSD: spent}, nil // uncapped
	}
	remaining := c.cfg.GlobalDailyCapUSD - spent
	return Remaining{
		LimitUSD:     c.cfg.GlobalDailyCapUSD,
		SpentUSD:     spent,
		RemainingUSD: remaining,
		Exhausted:    remaining <= 0,
	}, nil
}

func (c *checker) StepSlice(ticketID int, sliceUSD float64) (Remaining, error) {
	ticket, err := c.TicketRemaining(ticketID)
	if err != nil {
		return Remaining{}, err
	}
	if sliceUSD <= 0 {
		// No per-step slice declared: the step inherits whatever the
		// ticket has left (possibly uncapped).
		return ticket, nil
	}
	if ticket.LimitUSD == 0 {
		// Uncapped ticket: the slice itself is the only cap.
		return Remaining{LimitUSD: sliceUSD, RemainingUSD: sliceUSD}, nil
	}
	allowed := math.Min(sliceUSD, ticket.RemainingUSD)
	return Remaining{
		LimitUSD:     sliceUSD,
		SpentUSD:     ticket.SpentUSD,
		RemainingUSD: allowed,
		Exhausted:    allowed <= 0,
	}, nil
}

func (c *checker) Record(entry LedgerEntry) error {
	if !ValidSource(entry.Source) {
		return fmt.Errorf("source %q: %w", entry.Source, ErrInvalidEntry)
	}
	if entry.CostUSD < 0 {
		return fmt.Errorf("negative cost_usd: %w", ErrInvalidEntry)
	}
	return c.ledger.Insert(entry)
}
