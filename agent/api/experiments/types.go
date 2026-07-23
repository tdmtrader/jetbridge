// Package experiments exposes team-scoped management of durable workflow
// experiments. Execution remains in agent/experiment; this package is only
// the HTTP/storage contract.
package experiments

import "github.com/concourse/concourse/agent/experiment"

var (
	ErrNotFound             = experiment.ErrNotFound
	ErrRevisionConflict     = experiment.ErrRevisionConflict
	ErrImmutable            = experiment.ErrImmutable
	ErrInvalidDefinition    = experiment.ErrInvalidDefinition
	ErrScorecardUnavailable = experiment.ErrScorecardUnavailable
)

type StoredExperiment = experiment.StoredExperiment
type StoredCell = experiment.StoredCell
type Store = experiment.Store

type ValidationResult struct {
	Valid         bool  `json:"valid"`
	Revision      int64 `json:"revision"`
	ExpectedCells int   `json:"expected_cells"`
}
