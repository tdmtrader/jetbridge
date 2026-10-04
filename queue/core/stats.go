package core

import (
	"slices"
	"time"
)

// EjectCauses splits the ejects of a window by why they happened.
type EjectCauses struct {
	Culprit       int `json:"culprit"`
	ParentEjected int `json:"parent_ejected"`
	Refused       int `json:"refused"`
	Other         int `json:"other"`
}

// Summary is the fixed set of queue stats, over the window except the queue now.
type Summary struct {
	Queued             int         `json:"queued"`
	InFlight           int         `json:"in_flight"`
	Paused             bool        `json:"paused"`
	PausedWhy          string      `json:"paused_why"`
	Landed             int         `json:"landed"`
	Admitted           int         `json:"admitted"`
	Ejected            EjectCauses `json:"ejected"`
	Flakes             int         `json:"flakes"` // flaky batches
	MedianQueueSeconds float64     `json:"median_queue_seconds"`
	P90QueueSeconds    float64     `json:"p90_queue_seconds"`
	LandedPerHour      float64     `json:"landed_per_hour"`
}

// Stats folds the settle records and the queue into a Summary of the window
// ending at now; older records are ignored. Admitted counts each admission once.
func Stats(s Snapshot, now time.Time, window time.Duration) Summary {
	out := Summary{Queued: len(s.Queued), InFlight: len(s.InFlight), Paused: s.Paused, PausedWhy: Redact(s.Why)}
	in := func(t time.Time) bool { return !t.IsZero() && !t.Before(now.Add(-window)) && !t.After(now) }
	admitted, waits := map[string]bool{}, []float64{}
	for _, e := range s.Queued {
		admitted[e.ID+e.AdmittedAt.String()] = in(e.AdmittedAt)
	}
	for _, r := range s.Settled {
		if !r.AdmittedAt.IsZero() {
			admitted[r.ID+r.AdmittedAt.String()] = in(r.AdmittedAt)
		}
		if !in(r.At) {
			continue
		}
		switch {
		case r.Kind == LandedEvent:
			out.Landed++
			waits = append(waits, r.At.Sub(r.AdmittedAt).Seconds())
		case r.Kind == "flaky":
			out.Flakes++
		case r.Kind == RefusedEvent:
			out.Ejected.Refused++
		case r.Kind == EjectedEvent && r.Cause == Culprit:
			out.Ejected.Culprit++
		case r.Kind == EjectedEvent && r.Cause == ParentEjected:
			out.Ejected.ParentEjected++
		case r.Kind == EjectedEvent:
			out.Ejected.Other++
		}
	}
	for _, ok := range admitted {
		if ok {
			out.Admitted++
		}
	}
	slices.Sort(waits)
	if n := len(waits); n > 0 {
		out.MedianQueueSeconds = (waits[(n-1)/2] + waits[n/2]) / 2
		out.P90QueueSeconds = waits[(n*9+9)/10-1]
	}
	if window > 0 {
		out.LandedPerHour = float64(out.Landed) / window.Hours()
	}
	return out
}
