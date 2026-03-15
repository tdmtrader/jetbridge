package component

import (
	"context"
	"os"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"github.com/concourse/concourse/atc/db"
)

type NotificationsBus interface {
	ListenSignal(string) (*db.NotifySignal, error)
	UnlistenSignal(string, *db.NotifySignal) error
}

// Schedulable represents a workload that can be run on demand.
type Schedulable interface {
	RunImmediately(context.Context)
}

// Runner runs a workload immediately upon receiving a notification signal.
// It also fires once on startup so components that need an initial run (e.g.
// worker registration) don't have to wait for an external notification.
//
// When Interval is set (> 0), the Runner also fires periodically as a
// fallback. This is required for components like the K8s worker registrar
// that must heartbeat on a schedule to maintain a TTL.
type Runner struct {
	Logger    lager.Logger
	Interval  time.Duration
	Component Component
	Bus       NotificationsBus

	Schedulable Schedulable
}

func (r *Runner) Run(signals <-chan os.Signal, ready chan<- struct{}) error {
	r.Logger.Debug("start")
	defer r.Logger.Debug("done")

	signal, err := r.Bus.ListenSignal(r.Component.Name())
	if err != nil {
		return err
	}

	defer r.Bus.UnlistenSignal(r.Component.Name(), signal)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-signals
		cancel()
	}()

	close(ready)

	// Fire once on startup so the component runs immediately.
	runCtx := lagerctx.NewContext(ctx, r.Logger.Session("startup"))
	r.Schedulable.RunImmediately(runCtx)

	var ticker *time.Ticker
	var tickCh <-chan time.Time
	if r.Interval > 0 {
		ticker = time.NewTicker(r.Interval)
		tickCh = ticker.C
		defer ticker.Stop()
	}

	for {
		select {
		case <-signal.C():
			runCtx := lagerctx.NewContext(ctx, r.Logger.Session("notify"))
			r.Schedulable.RunImmediately(runCtx)

		case <-tickCh:
			runCtx := lagerctx.NewContext(ctx, r.Logger.Session("tick"))
			r.Schedulable.RunImmediately(runCtx)

		case <-ctx.Done():
			return nil
		}
	}
}
