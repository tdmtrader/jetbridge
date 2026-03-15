package db

import (
	"database/sql"
	"sync"

	"github.com/jackc/pgx/v5/pgconn"
)

//counterfeiter:generate . Listener

type Listener interface {
	Close() error
	Listen(channel string) error
	Unlisten(channel string) error
	NotificationChannel() <-chan *pgconn.Notification
}

//counterfeiter:generate . Executor
type Executor interface {
	Exec(statement string, args ...any) (sql.Result, error)
}

type NotificationsBus interface {
	Notify(channel string) error
	ListenSignal(channel string) (*NotifySignal, error)
	UnlistenSignal(channel string, signal *NotifySignal) error
	Close() error
}

type notificationsBus struct {
	sync.Mutex

	listener Listener
	executor Executor

	signals *signalsMap

	wg *sync.WaitGroup
}

func NewNotificationsBus(listener Listener, executor Executor) *notificationsBus {
	bus := &notificationsBus{
		listener: listener,
		executor: executor,
		signals:  newSignalsMap(),

		wg: new(sync.WaitGroup),
	}

	// DO NOT use bus.wg to wait for bus.wait().
	go bus.wait()

	return bus
}

func (bus *notificationsBus) Close() error {
	return bus.listener.Close()
}

func (bus *notificationsBus) Notify(channel string) error {
	_, err := bus.executor.Exec("NOTIFY " + channel)
	return err
}

func (bus *notificationsBus) wait() {
	for {
		notification, ok := <-bus.listener.NotificationChannel()
		if !ok {
			break
		}

		if notification != nil {
			bus.handleNotification(notification)
		} else {
			bus.handleReconnect()
		}
	}
}

func (bus *notificationsBus) ListenSignal(channel string) (*NotifySignal, error) {
	bus.Lock()
	defer bus.Unlock()

	if bus.signals.empty(channel) {
		err := bus.listener.Listen(channel)
		if err != nil {
			return nil, err
		}
	}

	signal := NewNotifySignal()
	bus.signals.register(channel, signal)
	return signal, nil
}

func (bus *notificationsBus) UnlistenSignal(channel string, signal *NotifySignal) error {
	bus.Lock()
	defer bus.Unlock()

	bus.signals.unregister(channel, signal)

	if bus.signals.empty(channel) {
		return bus.listener.Unlisten(channel)
	}

	return nil
}

func (bus *notificationsBus) handleNotification(notification *pgconn.Notification) {
	// wake any signal listeners (coalescing)
	bus.signals.eachForChannel(notification.Channel, func(signal *NotifySignal) {
		signal.Signal()
	})
}

func (bus *notificationsBus) handleReconnect() {
	// wake all signal listeners — they'll do a full scan and discover
	// anything missed during the reconnect
	bus.signals.each(func(signal *NotifySignal) {
		signal.Signal()
	})
}

func newSignalsMap() *signalsMap {
	return &signalsMap{
		signals: make(map[string]map[*NotifySignal]struct{}),
	}
}

type signalsMap struct {
	sync.RWMutex

	signals map[string]map[*NotifySignal]struct{}
}

func (m *signalsMap) empty(channel string) bool {
	m.RLock()
	defer m.RUnlock()

	return len(m.signals[channel]) == 0
}

func (m *signalsMap) register(channel string, signal *NotifySignal) {
	m.Lock()
	defer m.Unlock()

	sinks, found := m.signals[channel]
	if !found {
		sinks = make(map[*NotifySignal]struct{})
		m.signals[channel] = sinks
	}

	sinks[signal] = struct{}{}
}

func (m *signalsMap) unregister(channel string, signal *NotifySignal) {
	m.Lock()
	defer m.Unlock()

	_, ok := m.signals[channel]
	if !ok {
		return
	}
	delete(m.signals[channel], signal)

	if len(m.signals[channel]) == 0 {
		delete(m.signals, channel)
	}
}

func (m *signalsMap) each(f func(*NotifySignal)) {
	m.RLock()
	defer m.RUnlock()

	for _, sinks := range m.signals {
		for signal := range sinks {
			f(signal)
		}
	}
}

func (m *signalsMap) eachForChannel(channel string, f func(*NotifySignal)) {
	m.RLock()
	defer m.RUnlock()

	for signal := range m.signals[channel] {
		f(signal)
	}
}
