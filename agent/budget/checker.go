package budget

import (
	"errors"
	"fmt"
	"time"
)

// ErrInvalidEntry marks Record validation failures so HTTP handlers can
// map them to 400 rather than 500.
var ErrInvalidEntry = errors.New("invalid ledger entry")

func NewChecker(ledger Ledger, cfg Config) Checker {
	if cfg.Location == nil {
		cfg.Location = time.Local
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &checker{ledger: ledger, cfg: cfg}
}

type checker struct {
	ledger Ledger
	cfg    Config
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

func (c *checker) Record(entry LedgerEntry) error {
	if !ValidSource(entry.Source) {
		return fmt.Errorf("source %q: %w", entry.Source, ErrInvalidEntry)
	}
	if entry.CostUSD < 0 {
		return fmt.Errorf("negative cost_usd: %w", ErrInvalidEntry)
	}
	return c.ledger.Insert(entry)
}
