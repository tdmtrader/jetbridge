package runner

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/concourse/concourse/agent/provider"
)

// signalBoundaryControl owns the runner-side signal contract used by the
// existing supervisor. Signals merely coalesce/cancel a server request; only
// an adapter or truthful terminal path can call AtSafeBoundary.
type signalBoundaryControl struct {
	mu      sync.Mutex
	pending bool
	stop    func() error
	signals chan os.Signal
	done    chan struct{}
	close   sync.Once
}

func newSignalBoundaryControl(stop func() error) *signalBoundaryControl {
	if stop == nil {
		stop = func() error { return syscall.Kill(os.Getpid(), syscall.SIGSTOP) }
	}
	control := &signalBoundaryControl{
		stop:    stop,
		signals: make(chan os.Signal, 2),
		done:    make(chan struct{}),
	}
	signal.Notify(control.signals, syscall.SIGUSR1, syscall.SIGUSR2)
	go control.listen()
	return control
}

func (control *signalBoundaryControl) listen() {
	for {
		select {
		case signalValue := <-control.signals:
			control.mu.Lock()
			switch signalValue {
			case syscall.SIGUSR1:
				control.pending = true
			case syscall.SIGUSR2:
				control.pending = false
			}
			control.mu.Unlock()
		case <-control.done:
			return
		}
	}
}

func (control *signalBoundaryControl) Close() {
	if control == nil {
		return
	}
	control.close.Do(func() {
		signal.Stop(control.signals)
		close(control.done)
	})
}

func (control *signalBoundaryControl) AtSafeBoundary(ctx context.Context, boundary provider.Boundary) error {
	if control == nil {
		return errors.New("boundary controller is unavailable")
	}
	if err := boundary.Validate(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	control.mu.Lock()
	pending := control.pending
	if pending {
		control.pending = false
	}
	control.mu.Unlock()
	if !pending {
		return nil
	}
	return control.stop()
}
