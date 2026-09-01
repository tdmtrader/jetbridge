package steps

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/metric"
	"github.com/tedsuo/ifrit"
)

type RuntimeMetricObservation struct{ Value string }

type runtimeMetricEmitter struct {
	mu     sync.Mutex
	events []metric.Event
	wake   chan struct{}
}

func (e *runtimeMetricEmitter) Emit(_ lager.Logger, event metric.Event) {
	e.mu.Lock()
	e.events = append(e.events, event)
	e.mu.Unlock()
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

func (e *runtimeMetricEmitter) snapshot() []metric.Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]metric.Event(nil), e.events...)
}

type runtimeMetricEmitterFactory struct{ emitter *runtimeMetricEmitter }

func (f runtimeMetricEmitterFactory) Description() string { return "brine-observation" }
func (f runtimeMetricEmitterFactory) IsConfigured() bool  { return true }
func (f runtimeMetricEmitterFactory) NewEmitter(map[string]string) (metric.Emitter, error) {
	return f.emitter, nil
}

type namedMetricDB struct {
	db.DbConn
	name string
}

func (d namedMetricDB) Name() string { return d.name }

func RuntimeMetricDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, RuntimeMetricObservation](
			"the production runtime metric evaluates profile {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (RuntimeMetricObservation, error) {
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return RuntimeMetricObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				profile, _ := p.GetString(0)
				value, err := observeRuntimeMetric(database, profile)
				return RuntimeMetricObservation{Value: value}, err
			},
		),
		CheckString[RuntimeMetricObservation]("the runtime metric result is {string}", "runtime metric result", func(in RuntimeMetricObservation) (string, error) {
			return in.Value, nil
		}),
	}
}

func observeRuntimeMetric(database JetbridgeDB, profile string) (string, error) {
	monitor, emitter, logger, err := newRuntimeMetricMonitor()
	if err != nil {
		return "", err
	}

	switch profile {
	case "workers-empty":
		metric.WorkersState{WorkerStateByName: map[string]db.WorkerState{}}.Emit(logger, monitor)
		events, err := waitForRuntimeMetrics(emitter, func(events []metric.Event) bool {
			return countRuntimeMetrics(events, "worker state") == len(db.AllWorkerStates())
		})
		if err != nil {
			return "", err
		}
		allZero := true
		states := map[string]bool{}
		for _, event := range events {
			if event.Name == "worker state" {
				states[event.Attributes["state"]] = true
				allZero = allZero && event.Value == 0
			}
		}
		return fmt.Sprintf("all-states=%t;all-zero=%t", len(states) == len(db.AllWorkerStates()), allZero), nil
	case "workers-running":
		metric.WorkersState{WorkerStateByName: map[string]db.WorkerState{"worker": db.WorkerStateRunning}}.Emit(logger, monitor)
		event, err := waitForRuntimeMetric(emitter, "worker state", func(event metric.Event) bool {
			return event.Attributes["state"] == string(db.WorkerStateRunning)
		})
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("running=%g", event.Value), nil
	case "http-root", "http-success", "http-failure":
		return observeHTTPMetric(profile, monitor, emitter, logger)
	case "periodic-database", "periodic-concurrent", "periodic-waiting":
		return observePeriodicMetric(database, profile, monitor, emitter, logger)
	case "error-log", "recursive-error", "info-log":
		collector := metric.NewErrorSinkCollector(logger, monitor)
		format := lager.LogFormat{Message: "err-msg", LogLevel: lager.ERROR}
		if profile == "recursive-error" {
			format.Message = "recursive"
			format.Error = metric.ErrFailedToEmit
		}
		if profile == "info-log" {
			format.Message = "info"
			format.LogLevel = lager.INFO
		}
		collector.Log(format)
		if profile != "error-log" {
			time.Sleep(25 * time.Millisecond)
			return fmt.Sprintf("emitted=%d", len(emitter.snapshot())), nil
		}
		event, err := waitForRuntimeMetric(emitter, "error log", func(metric.Event) bool { return true })
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("emitted=1;message=%s", event.Attributes["message"]), nil
	default:
		return "", fmt.Errorf("unknown runtime metric profile %q", profile)
	}
}

func newRuntimeMetricMonitor() (*metric.Monitor, *runtimeMetricEmitter, lager.Logger, error) {
	emitter := &runtimeMetricEmitter{wake: make(chan struct{}, 1)}
	monitor := metric.NewMonitor()
	monitor.RegisterEmitter(runtimeMetricEmitterFactory{emitter: emitter})
	logger := lagertest.NewTestLogger("runtime-metric")
	if err := monitor.Initialize(logger, "brine-host", map[string]string{}, 512); err != nil {
		return nil, nil, nil, err
	}
	return monitor, emitter, logger, nil
}

func observeHTTPMetric(profile string, monitor *metric.Monitor, emitter *runtimeMetricEmitter, logger lager.Logger) (string, error) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/success":
			return
		case "/failure":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	server := httptest.NewServer(metric.WrapHandler(logger, monitor, "ApiEndpoint", handler))
	defer server.Close()
	path := "/"
	if profile == "http-success" {
		path = "/success"
	} else if profile == "http-failure" {
		path = "/failure"
	}
	response, err := http.Get(server.URL + path)
	if err != nil {
		return "", err
	}
	_ = response.Body.Close()
	event, err := waitForRuntimeMetric(emitter, "http response time", func(metric.Event) bool { return true })
	if err != nil {
		return "", err
	}
	if profile == "http-root" {
		return fmt.Sprintf("status=%s;method=%s;route=%s;path=%s", event.Attributes["status"], event.Attributes["method"], event.Attributes["route"], event.Attributes["path"]), nil
	}
	return fmt.Sprintf("status=%s;path=%s", event.Attributes["status"], event.Attributes["path"]), nil
}

func observePeriodicMetric(database JetbridgeDB, profile string, monitor *metric.Monitor, emitter *runtimeMetricEmitter, logger lager.Logger) (string, error) {
	switch profile {
	case "periodic-database":
		monitor.DatabaseQueries.IncDelta(4)
		monitor.Databases = []db.DbConn{namedMetricDB{DbConn: database.Conn, name: "A"}, namedMetricDB{DbConn: database.Conn, name: "B"}}
	case "periodic-concurrent":
		gauge := &metric.Gauge{}
		gauge.Set(123)
		counter := &metric.Counter{}
		counter.IncDelta(10)
		monitor.ConcurrentRequests["ListAllSomething"] = gauge
		monitor.ConcurrentRequestsLimitHit["ListAllSomething"] = counter
	case "periodic-waiting":
		gauge := &metric.Gauge{}
		gauge.Set(123)
		monitor.StepsWaiting[metric.StepsWaitingLabels{TeamId: "42", TeamName: "teamdev", Type: "task"}] = gauge
	}

	process := ifrit.Invoke(metric.PeriodicallyEmit(logger, monitor, 10*time.Millisecond))
	select {
	case <-process.Ready():
	case <-time.After(time.Second):
		return "", fmt.Errorf("periodic metric runner did not become ready")
	}
	defer func() {
		process.Signal(os.Interrupt)
		<-process.Wait()
	}()

	switch profile {
	case "periodic-database":
		events, err := waitForRuntimeMetrics(emitter, func(events []metric.Event) bool {
			return countRuntimeMetrics(events, "database queries") > 0 && countRuntimeMetrics(events, "database connections") >= 2
		})
		if err != nil {
			return "", err
		}
		queryValue := float64(0)
		nameSet := map[string]bool{}
		for _, event := range events {
			if event.Name == "database queries" && event.Value > 0 {
				queryValue = event.Value
			}
			if event.Name == "database connections" {
				nameSet[event.Attributes["ConnectionName"]] = true
			}
		}
		names := make([]string, 0, len(nameSet))
		for name := range nameSet {
			names = append(names, name)
		}
		sort.Strings(names)
		return fmt.Sprintf("queries=%g;connections=%s", queryValue, strings.Join(names, ",")), nil
	case "periodic-concurrent":
		events, err := waitForRuntimeMetrics(emitter, func(events []metric.Event) bool {
			return findRuntimeMetric(events, "concurrent requests") != nil && findRuntimeMetric(events, "concurrent requests limit hit") != nil
		})
		if err != nil {
			return "", err
		}
		requests := findRuntimeMetric(events, "concurrent requests")
		limit := findRuntimeMetric(events, "concurrent requests limit hit")
		return fmt.Sprintf("requests=%g;limit-hit=%g;action=%s", requests.Value, limit.Value, requests.Attributes["action"]), nil
	case "periodic-waiting":
		event, err := waitForRuntimeMetric(emitter, "steps waiting", func(metric.Event) bool { return true })
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("value=%g;teamId=%s;teamName=%s;type=%s", event.Value, event.Attributes["teamId"], event.Attributes["teamName"], event.Attributes["type"]), nil
	}
	return "", fmt.Errorf("unsupported periodic profile %q", profile)
}

func waitForRuntimeMetric(emitter *runtimeMetricEmitter, name string, accept func(metric.Event) bool) (metric.Event, error) {
	events, err := waitForRuntimeMetrics(emitter, func(events []metric.Event) bool {
		for _, event := range events {
			if event.Name == name && accept(event) {
				return true
			}
		}
		return false
	})
	if err != nil {
		return metric.Event{}, err
	}
	for _, event := range events {
		if event.Name == name && accept(event) {
			return event, nil
		}
	}
	return metric.Event{}, fmt.Errorf("metric %q disappeared", name)
}

func waitForRuntimeMetrics(emitter *runtimeMetricEmitter, ready func([]metric.Event) bool) ([]metric.Event, error) {
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		events := emitter.snapshot()
		if ready(events) {
			return events, nil
		}
		select {
		case <-emitter.wake:
		case <-deadline.C:
			return nil, fmt.Errorf("timed out waiting for runtime metric; got %d events", len(events))
		}
	}
}

func countRuntimeMetrics(events []metric.Event, name string) int {
	count := 0
	for _, event := range events {
		if event.Name == name {
			count++
		}
	}
	return count
}

func findRuntimeMetric(events []metric.Event, name string) *metric.Event {
	for i := range events {
		if events[i].Name == name {
			return &events[i]
		}
	}
	return nil
}
