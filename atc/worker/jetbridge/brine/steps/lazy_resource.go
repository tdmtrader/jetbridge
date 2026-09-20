package steps

import (
	"fmt"
	"sync"
)

// lazyResource keeps ownership shared when a resource value is copied out of
// brine.Resources. Acquisition itself must not allocate external resources.
// Starters clean up partial failures; close only disposes a successful start.
// Closing an unused resource never starts it, and prevents a later start.
type lazyResource[T any] struct {
	mu       sync.Mutex
	once     sync.Once
	start    func() (T, error)
	value    T
	err      error
	started  bool
	closed   bool
	closeErr error
}

func (r *lazyResource[T]) get() (T, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		var zero T
		return zero, fmt.Errorf("resource already disposed")
	}
	r.once.Do(func() {
		r.value, r.err = r.start()
		r.started = r.err == nil
	})
	return r.value, r.err
}

func (r *lazyResource[T]) close(dispose func(T) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.closed {
		r.closed = true
		if r.started {
			r.closeErr = dispose(r.value)
		}
	}
	return r.closeErr
}
