package metric_test

import (
	"slices"
	"sync"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/metric"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// recordingEmitter keeps every event handed to it. Every emitter that ships is
// a write-only sink that rewrites what it is given -- emitter.LagerEmitter
// renames attributes and drops Host, Time and TraceID -- so a spec that wants
// to see what a Monitor emitted has to supply the sink itself.
type recordingEmitter struct {
	mu     sync.Mutex
	events []metric.Event
}

func (e *recordingEmitter) Emit(_ lager.Logger, event metric.Event) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.events = append(e.events, event)
}

// Events copies, because a Monitor emits from its own goroutine.
func (e *recordingEmitter) Events() []metric.Event {
	e.mu.Lock()
	defer e.mu.Unlock()

	return slices.Clone(e.events)
}

func (e *recordingEmitter) EventCount() int {
	return len(e.Events())
}

type recordingEmitterFactory struct {
	emitter *recordingEmitter
}

func (f recordingEmitterFactory) Description() string { return "recording" }

func (f recordingEmitterFactory) IsConfigured() bool { return true }

func (f recordingEmitterFactory) NewEmitter(map[string]string) (metric.Emitter, error) {
	return f.emitter, nil
}

// monitorWithRecorder returns a running Monitor and the sink it emits to.
func monitorWithRecorder() (*metric.Monitor, *recordingEmitter) {
	GinkgoHelper()

	emitter := &recordingEmitter{}

	monitor := metric.NewMonitor()
	monitor.RegisterEmitter(recordingEmitterFactory{emitter: emitter})
	Expect(monitor.Initialize(testLogger, "test", map[string]string{}, 1000)).To(Succeed())

	return monitor, emitter
}
