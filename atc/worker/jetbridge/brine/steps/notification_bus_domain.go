package steps

import (
	"fmt"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db"
)

type NotificationBusObservation struct{ Value string }

func NotificationBusDomainDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, NotificationBusObservation](
			"the real PostgreSQL notification bus evaluates profile {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (NotificationBusObservation, error) {
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return NotificationBusObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				profile, _ := p.GetString(0)
				value, err := observeNotificationBus(database, profile)
				return NotificationBusObservation{Value: value}, err
			},
		),
		CheckString[NotificationBusObservation]("the notification bus result is {string}", "notification bus result", func(in NotificationBusObservation) (string, error) {
			return in.Value, nil
		}),
	}
}

func observeNotificationBus(database JetbridgeDB, profile string) (string, error) {
	bus := database.Conn.Bus()
	const firstChannel = "brine_notification_first"
	const secondChannel = "brine_notification_second"
	subscribe := func(channel string) (*db.NotifySignal, error) { return bus.ListenSignal(channel) }
	unsubscribe := func(channel string, signal *db.NotifySignal) { _ = bus.UnlistenSignal(channel, signal) }

	switch profile {
	case "round-trip":
		first, err := subscribe(firstChannel)
		if err != nil {
			return "", err
		}
		defer unsubscribe(firstChannel, first)
		if err := bus.Notify(firstChannel); err != nil {
			return "", err
		}
		gotFirst := notificationBusReceive(first, 2*time.Second)
		gotSecond := notificationBusReceive(first, 100*time.Millisecond)
		return fmt.Sprintf("first=%t;second=%t", gotFirst, gotSecond), nil
	case "wrong-channel":
		signal, err := subscribe(firstChannel)
		if err != nil {
			return "", err
		}
		defer unsubscribe(firstChannel, signal)
		if err := bus.Notify(secondChannel); err != nil {
			return "", err
		}
		return fmt.Sprintf("received=%t", notificationBusReceive(signal, 150*time.Millisecond)), nil
	case "same-channel":
		first, err := subscribe(firstChannel)
		if err != nil {
			return "", err
		}
		second, err := subscribe(firstChannel)
		if err != nil {
			return "", err
		}
		defer unsubscribe(firstChannel, second)
		if err := bus.Notify(firstChannel); err != nil {
			return "", err
		}
		firstGot := notificationBusReceive(first, 2*time.Second)
		secondGot := notificationBusReceive(second, 2*time.Second)
		if err := bus.UnlistenSignal(firstChannel, first); err != nil {
			return "", err
		}
		if err := bus.Notify(firstChannel); err != nil {
			return "", err
		}
		after := notificationBusReceive(second, 2*time.Second) && !notificationBusReceive(first, 100*time.Millisecond)
		return fmt.Sprintf("first=%t;second=%t;after-unlisten=%t", firstGot, secondGot, after), nil
	case "single-unlisten":
		signal, err := subscribe(firstChannel)
		if err != nil {
			return "", err
		}
		unlistenErr := bus.UnlistenSignal(firstChannel, signal)
		if err := bus.Notify(firstChannel); err != nil {
			return "", err
		}
		return fmt.Sprintf("error=%t;received=%t", unlistenErr != nil, notificationBusReceive(signal, 150*time.Millisecond)), nil
	case "different-channels":
		first, err := subscribe(firstChannel)
		if err != nil {
			return "", err
		}
		second, err := subscribe(secondChannel)
		if err != nil {
			return "", err
		}
		defer unsubscribe(firstChannel, first)
		defer unsubscribe(secondChannel, second)
		if err := bus.Notify(firstChannel); err != nil {
			return "", err
		}
		return fmt.Sprintf("first=%t;second=%t", notificationBusReceive(first, 2*time.Second), notificationBusReceive(second, 150*time.Millisecond)), nil
	case "coalescing":
		signal, err := subscribe(firstChannel)
		if err != nil {
			return "", err
		}
		defer unsubscribe(firstChannel, signal)
		for range 100 {
			if err := bus.Notify(firstChannel); err != nil {
				return "", err
			}
		}
		time.Sleep(100 * time.Millisecond)
		one := notificationBusReceive(signal, 2*time.Second)
		extra := notificationBusReceive(signal, 150*time.Millisecond)
		if err := bus.Notify(firstChannel); err != nil {
			return "", err
		}
		again := notificationBusReceive(signal, 2*time.Second)
		return fmt.Sprintf("one=%t;extra=%t;again=%t", one, extra, again), nil
	case "pressure":
		first, err := subscribe(firstChannel)
		if err != nil {
			return "", err
		}
		for range 100 {
			if err := bus.Notify(firstChannel); err != nil {
				return "", err
			}
		}
		second, listenErr := subscribe(secondChannel)
		unlistenErr := bus.UnlistenSignal(firstChannel, first)
		if second != nil {
			unsubscribe(secondChannel, second)
		}
		return fmt.Sprintf("listen=%t;unlisten=%t", listenErr == nil, unlistenErr == nil), nil
	default:
		return "", fmt.Errorf("unknown notification bus profile %q", profile)
	}
}

func notificationBusReceive(signal *db.NotifySignal, timeout time.Duration) bool {
	select {
	case <-signal.C():
		return true
	case <-time.After(timeout):
		return false
	}
}
