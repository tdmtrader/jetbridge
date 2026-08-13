package main

import (
	"context"
	"math/rand"
	"time"

	"code.cloudfoundry.org/lager/v3"

	"github.com/concourse/concourse/cmd/artifact-daemon/durable"
)

// defaultResidencyInterval is how often a daemon enumerates the store.
//
// Deliberately slow. A List is billed per page and every daemon runs its own,
// so the cost is (objects/1000) x nodes x frequency. Residency changes on the
// scale of a lifecycle rule, not a build, so minutes of staleness cost nothing.
const defaultResidencyInterval = 15 * time.Minute

// ResidencyReporter periodically measures what the durable store actually holds.
//
// # Why a gauge, and why measured rather than counted
//
// The daemon already counts operations, and a counter cannot answer "how big is
// the store". More importantly, reclaim is an object lifecycle rule the operator
// writes against a key prefix: nothing in this process performs it, so nothing
// in this process knows whether it is working. A rule with the wrong prefix
// matches nothing, deletes nothing, and reports no error anywhere. Measuring the
// store is the only way that failure becomes visible.
//
// # This is also the shape a JetBridge-managed reclaim would take
//
// Enumerate the store, decide something per object, act. This does the first
// two and its action is "add to a total". A reclaim would keep the walk and
// change the action to Delete for objects older than a policy. That is why the
// walk is factored as it is, and why Attributes carries Updated.
type ResidencyReporter struct {
	logger   lager.Logger
	store    durable.Store
	metrics  *metrics
	interval time.Duration
}

func NewResidencyReporter(logger lager.Logger, store durable.Store, m *metrics, interval time.Duration) *ResidencyReporter {
	if interval <= 0 {
		interval = defaultResidencyInterval
	}

	return &ResidencyReporter{
		logger:   logger.Session("durable-residency"),
		store:    store,
		metrics:  m,
		interval: interval,
	}
}

// Run measures once at startup and then on the interval, until ctx is done.
//
// The first measurement is jittered. Every daemon in the DaemonSet starts within
// seconds of the others on a rolling update, and without jitter they would all
// enumerate the bucket simultaneously, forever, in lockstep.
func (r *ResidencyReporter) Run(ctx context.Context) {
	if r == nil || r.store == nil {
		return
	}

	select {
	case <-time.After(time.Duration(rand.Int63n(int64(r.interval)))):
	case <-ctx.Done():
		return
	}

	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		r.measure(ctx)

		select {
		case <-ticker.C:
		case <-ctx.Done():
			return
		}
	}
}

// measure walks the store and publishes what it found.
//
// A failure leaves the previous gauge values in place rather than zeroing them.
// A zero would be indistinguishable from an empty store, and "the bucket went to
// zero" is exactly the shape of an alert nobody wants to receive at 3am because
// a credential expired.
func (r *ResidencyReporter) measure(ctx context.Context) {
	start := time.Now()

	var objects, bytes int64
	var oldest time.Time

	err := r.store.List(ctx, func(a durable.Attributes) error {
		objects++
		bytes += a.Size
		if !a.Updated.IsZero() && (oldest.IsZero() || a.Updated.Before(oldest)) {
			oldest = a.Updated
		}

		return nil
	})
	if err != nil {
		r.logger.Error("list-failed", err)
		r.metrics.recordDurable("list", "error")

		return
	}

	age := time.Duration(0)
	if !oldest.IsZero() {
		age = time.Since(oldest)
	}

	r.metrics.recordResidency(objects, bytes, age)
	r.metrics.recordDurable("list", "ok")

	r.logger.Debug("measured", lager.Data{
		"objects":    objects,
		"bytes":      bytes,
		"oldest_age": age.String(),
		"duration":   time.Since(start).String(),
	})
}
