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
	LandsPerHour       float64     `json:"lands_per_hour"`      // landing events (batches), not rows
	MedianWalkSeconds  float64     `json:"median_walk_seconds"` // earliest admit in a landing to its land
	P90WalkSeconds     float64     `json:"p90_walk_seconds"`
}

// Stats folds the settle records and the queue into a Summary of the window
// ending at now; older records are ignored. Admitted counts each admission once.
func Stats(s Snapshot, now time.Time, window time.Duration) Summary {
	out := Summary{Queued: len(s.Queued), InFlight: len(s.InFlight), Paused: s.Paused, PausedWhy: Redact(s.Why)}
	in := func(t time.Time) bool { return !t.IsZero() && !t.Before(now.Add(-window)) && !t.After(now) }
	admitted, waits := map[string]bool{}, []float64{}
	type landing struct{ at, first time.Time }
	landings := map[string]landing{} // keyed by run, or by land time when a record has no run
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
			k := r.Run
			if k == "" {
				k = r.At.String()
			}
			l, ok := landings[k]
			if !ok || r.AdmittedAt.Before(l.first) {
				l = landing{r.At, r.AdmittedAt}
			}
			landings[k] = l
		case r.Kind == FlakeEvent:
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
	out.MedianQueueSeconds, out.P90QueueSeconds = medianP90(waits)
	walks := []float64{}
	for _, l := range landings {
		walks = append(walks, l.at.Sub(l.first).Seconds())
	}
	out.MedianWalkSeconds, out.P90WalkSeconds = medianP90(walks)
	if window > 0 {
		out.LandedPerHour = float64(out.Landed) / window.Hours()
		out.LandsPerHour = float64(len(landings)) / window.Hours()
	}
	return out
}

func medianP90(xs []float64) (median, p90 float64) {
	slices.Sort(xs)
	if n := len(xs); n > 0 {
		median, p90 = (xs[(n-1)/2]+xs[n/2])/2, xs[(n*9+9)/10-1]
	}
	return
}
