package db

// NotifySignal is a coalescing wake-up signal. Multiple calls to Signal()
// between reads collapse into a single wake-up. Signal() never blocks and
// never drops.
//
// The key contract: consumers MUST do a full scan of pending work on each
// wake-up. The notification means "something changed" — not "process this
// specific item." This makes coalescing safe: N signals between reads are
// equivalent to 1, because the consumer finds all pending work regardless.
//
// This replaces the pattern of chan Notification with non-blocking sends
// that silently dropped notifications, combined with payload-targeted
// processing that made drops unsafe.
type NotifySignal struct {
	ch chan struct{}
}

// NewNotifySignal creates a new coalescing signal.
func NewNotifySignal() *NotifySignal {
	return &NotifySignal{
		ch: make(chan struct{}, 1),
	}
}

// Signal wakes the reader. If a signal is already pending (reader hasn't
// consumed it yet), this is a no-op — which is safe because the reader
// will do a full scan when it wakes. Never blocks.
func (s *NotifySignal) Signal() {
	select {
	case s.ch <- struct{}{}:
	default:
	}
}

// C returns the channel to select on for wake-ups.
func (s *NotifySignal) C() <-chan struct{} {
	return s.ch
}
