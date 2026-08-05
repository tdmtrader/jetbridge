package main

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/agent/hangar"
)

const (
	// DefaultHangarInventoryInterval is deliberately coarse. The daemon is a
	// DaemonSet, so this listing runs once per node against one shared bucket:
	// the real cost is the interval divided by the node count, not the interval
	// itself. Residency is a slow-moving quantity and the alerts that consume
	// it look at hours, so a five-minute picture is far finer than anything
	// that reads it needs.
	DefaultHangarInventoryInterval = 5 * time.Minute

	// DefaultHangarInventoryTimeout bounds one pass so a wedged bucket listing
	// cannot occupy the refresher indefinitely and stall every later pass.
	DefaultHangarInventoryTimeout = 2 * time.Minute

	// MinHangarInventoryInterval keeps a misconfiguration from turning a
	// per-node full-bucket listing into a hot loop against object storage.
	MinHangarInventoryInterval = 30 * time.Second
)

// hangarInventoryKinds is the set swept for residency. It is exhaustive over
// hangar.Kind on purpose: a kind omitted here would hold bytes that the
// capacity alert never counts, which is precisely the blind spot this metric
// exists to remove.
// KindCheckpoint stays in the inventory even though nothing writes it any
// more. The checkpoint subsystem was removed, but a deployment that ran it
// may still hold objects under that prefix and nothing sweeps them now --
// dropping the kind here would make those bytes invisible to the residency
// gauges and to the alerts that fire before the store fills.
var hangarInventoryKinds = []hangar.Kind{hangar.KindSnapshot, hangar.KindCheckpoint}

// hangarResidency is one kind's aggregate occupancy.
type hangarResidency struct {
	objects int64
	bytes   int64
}

// RefreshHangarInventory enumerates the durable store and publishes how much it
// currently holds.
//
// This is the only thing that can answer that question. The daemon's snapshot
// counters measure bytes in motion — they rise on GET and DELETE as readily as
// on PUT — so no combination of them expresses occupancy, and the database
// cannot answer it either, since an object whose row was never written is
// invisible to every metadata query by construction. Storage is the authority
// on what storage holds.
//
// The pass is all-or-nothing. Totals are accumulated for every kind and
// published only once all of them enumerated cleanly, because a partially
// listed store yields a number that is smaller than the truth while looking
// exactly like a legitimate one. Retaining the previous totals and declining to
// advance the freshness timestamp is the honest outcome; a half-total published
// as a total would read as a store that had just shed bytes, which is the one
// conclusion an operator must never draw from this metric.
func (s *Server) RefreshHangarInventory(ctx context.Context) error {
	store := s.hangar
	if store == nil {
		// Hangar is optional, and a daemon without it holds no durable store to
		// describe. Publishing zeros here would assert an empty store rather
		// than an absent one.
		return nil
	}

	observed := make(map[hangar.Kind]hangarResidency, len(hangarInventoryKinds))
	for _, kind := range hangarInventoryKinds {
		var residency hangarResidency
		err := store.List(ctx, kind, func(attributes hangar.Attributes) error {
			residency.objects++
			// CompressedBytes is what occupies the store. UncompressedBytes
			// describes the content, not the space it consumes, and List
			// reports it as zero for objects whose metadata is absent or
			// malformed — which are exactly the objects most likely to be
			// wasting room.
			residency.bytes += attributes.CompressedBytes
			return nil
		})
		if err != nil {
			s.metrics.recordHangarInventoryRefresh("error", time.Time{})
			s.logger.Error("hangar-inventory-refresh-failed", err, lager.Data{"kind": string(kind)})
			return fmt.Errorf("hangar inventory: list %s: %w", kind, err)
		}
		observed[kind] = residency
	}

	var totalObjects, totalBytes int64
	for kind, residency := range observed {
		s.metrics.recordHangarResidency(string(kind), residency.objects, residency.bytes)
		totalObjects += residency.objects
		totalBytes += residency.bytes
	}
	s.metrics.recordHangarInventoryRefresh("ok", time.Now())
	s.logger.Debug("hangar-inventory-refreshed", lager.Data{
		"objects": totalObjects,
		"bytes":   totalBytes,
	})
	return nil
}

// StartHangarInventory runs the residency sweep until ctx is cancelled. It
// returns immediately; a zero or negative interval disables the sweep, and a
// daemon with no durable store never starts one.
//
// The first pass is delayed by a random fraction of the interval. Every daemon
// in the DaemonSet starts within seconds of a rollout and lists the same
// bucket, so an unjittered loop would synchronize the whole fleet into one
// simultaneous burst of listings every interval, forever.
func (s *Server) StartHangarInventory(ctx context.Context, interval, timeout time.Duration) {
	if s.hangar == nil || interval <= 0 {
		return
	}
	if interval < MinHangarInventoryInterval {
		interval = MinHangarInventoryInterval
	}
	if timeout <= 0 {
		timeout = DefaultHangarInventoryTimeout
	}

	go func() {
		jitter := time.Duration(rand.Int63n(int64(interval)))
		select {
		case <-ctx.Done():
			return
		case <-time.After(jitter):
		}

		refresh := func() {
			passCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			// The error is already counted and logged inside the refresh; a
			// failed pass must not stop the loop, since the next one is what
			// clears the staleness alert.
			_ = s.RefreshHangarInventory(passCtx)
		}

		refresh()

		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				refresh()
			}
		}
	}()
}
