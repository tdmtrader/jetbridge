package component

import (
	"context"
	"os"
	"time"

	"code.cloudfoundry.org/clock"
	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"github.com/concourse/concourse/atc/db"
)

var Clock = clock.NewClock()

type contextKey string

const notifyPayloadKey contextKey = "notify-payload"

// NotifyPayload extracts the NOTIFY payload from the context, if present.
// Returns the payload string and true, or "" and false when absent.
func NotifyPayload(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(notifyPayloadKey).(string)
	return v, ok && v != ""
}

// WithNotifyPayload returns a new context carrying the given NOTIFY payload.
func WithNotifyPayload(ctx context.Context, payload string) context.Context {
	return context.WithValue(ctx, notifyPayloadKey, payload)
}

type NotificationsBus interface {
	Listen(string, int) (chan db.Notification, error)
	Unlisten(string, chan db.Notification) error
}

// Schedulable represents a workload that is executed normally on a periodic
// schedule, but can also be run immediately.
type Schedulable interface {
	RunPeriodically(context.Context)
	RunImmediately(context.Context)
}

// Runner runs a workload periodically, or immediately upon receiving a
// notification. When Interval is zero the Runner operates in
// notification-only mode — it never polls and relies entirely on NOTIFY
// to trigger execution.
type Runner struct {
	Logger lager.Logger

	Interval  time.Duration
	Component Component
	Bus       NotificationsBus

	Schedulable Schedulable
}

func (scheduler *Runner) Run(signals <-chan os.Signal, ready chan<- struct{}) error {
	scheduler.Logger.Debug("start")
	defer scheduler.Logger.Debug("done")

	notifier, err := scheduler.Bus.Listen(scheduler.Component.Name(), 1)
	if err != nil {
		return err
	}

	defer scheduler.Bus.Unlisten(scheduler.Component.Name(), notifier)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-signals
		cancel()
	}()

	close(ready)

	if scheduler.Interval == 0 {
		return scheduler.runNotifyOnly(ctx, notifier)
	}
	return scheduler.runWithPolling(ctx, notifier)
}

func (scheduler *Runner) runNotifyOnly(ctx context.Context, notifier chan db.Notification) error {
	for {
		select {
		case n := <-notifier:
			runCtx := lagerctx.NewContext(ctx, scheduler.Logger.Session("notify"))
			if n.Payload != "" {
				runCtx = context.WithValue(runCtx, notifyPayloadKey, n.Payload)
			}
			scheduler.Schedulable.RunImmediately(runCtx)

		case <-ctx.Done():
			return nil
		}
	}
}

func (scheduler *Runner) runWithPolling(ctx context.Context, notifier chan db.Notification) error {
	for {
		timer := Clock.NewTimer(scheduler.Interval)

		select {
		case n := <-notifier:
			timer.Stop()
			runCtx := lagerctx.NewContext(ctx, scheduler.Logger.Session("notify"))
			if n.Payload != "" {
				runCtx = context.WithValue(runCtx, notifyPayloadKey, n.Payload)
			}
			scheduler.Schedulable.RunImmediately(runCtx)

		case <-timer.C():
			runCtx := lagerctx.NewContext(ctx, scheduler.Logger.Session("tick"))
			scheduler.Schedulable.RunPeriodically(runCtx)

		case <-ctx.Done():
			timer.Stop()
			return nil
		}
	}
}
