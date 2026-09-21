package pipelinerunserver

import "github.com/concourse/concourse/atc/runs"

// Services are configured at startup. A missing admission service or epoch
// keeps public input intake unavailable.
type Services struct {
	Results  ResultReader
	Admitter runs.Admitter
	Epoch    int64
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
