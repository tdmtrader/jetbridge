package pipelinerunserver

import "github.com/concourse/concourse/atc/runs"

// Services are configured at startup. A missing admission service keeps Run
// creation unavailable; a missing Hangar epoch keeps input intake and
// credential delivery unavailable. Runs are admitted under the separate Run
// activation epoch (atc.PipelineRunActivationEpoch).
type Services struct {
	Results  ResultReader
	Admitter runs.Admitter
	// Epoch is the Hangar output epoch this control plane speaks for.
	Epoch int64
	// ResultReadConcurrency bounds result reads in flight. Each spools the
	// whole archive, twice, into web scratch until its response is written,
	// so readers past the bound are refused rather than queued. Zero or less
	// is DefaultResultReadConcurrency, never unbounded.
	ResultReadConcurrency int
}

// DefaultResultReadConcurrency is the result-read bound when none is
// configured.
const DefaultResultReadConcurrency = 2

func (s *Server) SetServices(services Services) {
	s.services = services
	limit := services.ResultReadConcurrency
	if limit <= 0 {
		limit = DefaultResultReadConcurrency
	}
	s.resultSlots = make(chan struct{}, limit)
}
