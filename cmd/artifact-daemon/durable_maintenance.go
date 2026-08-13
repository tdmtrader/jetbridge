package main

import (
	"context"
	"math/rand"
	"time"

	"code.cloudfoundry.org/lager/v3"

	"github.com/concourse/concourse/cmd/artifact-daemon/durable"
)

// defaultMaintenanceInterval is how often a daemon walks the durable store.
//
// Deliberately slow. A List is billed per page and every daemon runs its own, so
// the cost is (objects/1000) x nodes x frequency. Neither residency nor
// expiry changes on the scale of a build.
const defaultMaintenanceInterval = 15 * time.Minute

// maxDeletesPerPass bounds how much one pass will reclaim.
//
// The first pass after a policy is set — or after a long outage — can find an
// enormous backlog, and deleting it all at once is a self-inflicted API storm
// against the same bucket builds are reading from. The backlog drains over
// several passes instead. Whenever the cap bites, the pass says so: a reclaim
// that silently stops early reads exactly like a reclaim that finished.
const maxDeletesPerPass = 1000

// StoreMaintainer walks the durable store on an interval, reclaiming what has
// expired and reporting what remains.
//
// # One walk, two jobs
//
// Both need the same enumeration, and enumeration is the expensive part, so they
// share it. The gauges then describe the store as the pass leaves it rather than
// as it found it, which is the state anyone reading them cares about.
//
// # Why JetBridge reclaims rather than the bucket
//
// An object lifecycle rule can do this, and is the obvious answer, but it means
// the retention period lives as a string an operator types into a cloud console
// that must match a prefix this code composes. Nothing can check the two agree —
// a rule with the wrong prefix matches nothing, deletes nothing, and reports no
// error. It also does not exist at all for the filesystem backend, where a
// shared NFS or RWX volume would simply grow forever.
//
// So policy lives in one place, next to everything else that configures this
// daemon. A bucket rule remains a perfectly good backstop for when JetBridge is
// not running, and the two do not conflict: both only ever delete what is
// already past its age.
//
// # Nothing here may fail a build
//
// Reclaim is best-effort like every other durable path. A failed enumeration or
// a failed delete is logged and the pass moves on; the artifact it could not
// remove is simply removed next time.
type StoreMaintainer struct {
	logger   lager.Logger
	tier     *DurableTier
	metrics  *metrics
	interval time.Duration
	policy   RetentionPolicy
}

func NewStoreMaintainer(logger lager.Logger, tier *DurableTier, m *metrics, interval time.Duration, policy RetentionPolicy) *StoreMaintainer {
	if interval <= 0 {
		interval = defaultMaintenanceInterval
	}

	return &StoreMaintainer{
		logger:   logger.Session("durable-maintenance"),
		tier:     tier,
		metrics:  m,
		interval: interval,
		policy:   policy,
	}
}

// Run walks once at startup and then on the interval, until ctx is done.
//
// The first walk is jittered. Every daemon in the DaemonSet starts within
// seconds of the others on a rolling update, and without jitter they would all
// enumerate the bucket simultaneously, forever, in lockstep.
//
// No leader election, deliberately. Deleting an absent key is not an error by
// the Store contract, so several daemons reclaiming at once is correct — merely
// wasteful, which is what the jitter and the interval are for.
func (s *StoreMaintainer) Run(ctx context.Context) {
	if s == nil || s.tier.ObjectStore() == nil {
		return
	}

	select {
	case <-time.After(time.Duration(rand.Int63n(int64(s.interval)))):
	case <-ctx.Done():
		return
	}

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		s.sweep(ctx)

		select {
		case <-ticker.C:
		case <-ctx.Done():
			return
		}
	}
}

// sweep enumerates the store, deletes what has expired, and publishes what is
// left.
//
// A failed enumeration leaves the previous gauge values in place rather than
// zeroing them. A zero is indistinguishable from an empty store, and "the bucket
// went to zero" is the worst false alert this could produce.
func (s *StoreMaintainer) sweep(ctx context.Context) {
	start := time.Now()
	now := start

	var objects, bytes, deleted, deletedBytes, failed int64
	var oldest time.Time
	capped := false

	err := s.tier.ObjectStore().List(ctx, func(a durable.Attributes) error {
		if s.policy.expired(a, now) {
			if deleted >= maxDeletesPerPass {
				capped = true
			} else if s.tier.Delete(ctx, a.Key) {
				deleted++
				deletedBytes += a.Size

				return nil
			} else {
				// Still there. Count it as residency, because it is.
				failed++
			}
		}

		objects++
		bytes += a.Size
		if !a.Updated.IsZero() && (oldest.IsZero() || a.Updated.Before(oldest)) {
			oldest = a.Updated
		}

		return nil
	})
	if err != nil {
		s.logger.Error("list-failed", err)
		s.metrics.recordDurable("list", "error")

		return
	}

	age := time.Duration(0)
	if !oldest.IsZero() {
		age = time.Since(oldest)
	}

	s.metrics.recordResidency(objects, bytes, age)
	s.metrics.recordReclaimed(deleted, deletedBytes)
	s.metrics.recordDurable("list", "ok")

	data := lager.Data{
		"objects":       objects,
		"bytes":         bytes,
		"oldest_age":    age.String(),
		"deleted":       deleted,
		"deleted_bytes": deletedBytes,
		"duration":      time.Since(start).String(),
	}
	if failed > 0 {
		data["delete_failures"] = failed
	}

	if capped {
		// Never let a truncated pass look like a complete one.
		s.logger.Info("reclaim-capped", lager.Data{
			"deleted": deleted,
			"cap":     maxDeletesPerPass,
			"note":    "more objects are expired than one pass will remove; the backlog drains over subsequent passes",
		})
	}
	s.logger.Debug("swept", data)
}
