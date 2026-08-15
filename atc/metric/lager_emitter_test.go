package metric_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/metric"
	metricemitter "github.com/concourse/concourse/atc/metric/emitter"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// metricLogOutput captures the JSON protocol written by Lager's real metric
// emitter. Monitor emits asynchronously, so writes and snapshots must be
// synchronized even though each spec itself is serial.
type metricLogOutput struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (output *metricLogOutput) Write(p []byte) (int, error) {
	output.mu.Lock()
	defer output.mu.Unlock()

	return output.buf.Write(p)
}

func (output *metricLogOutput) Logs() []lager.LogFormat {
	GinkgoHelper()

	output.mu.Lock()
	snapshot := output.buf.String()
	output.mu.Unlock()

	decoder := json.NewDecoder(strings.NewReader(snapshot))
	var logs []lager.LogFormat
	for {
		var log lager.LogFormat
		err := decoder.Decode(&log)
		if errors.Is(err, io.EOF) {
			break
		}
		Expect(err).NotTo(HaveOccurred())
		logs = append(logs, log)
	}

	return logs
}

func (output *metricLogOutput) MetricEvents() []lager.Data {
	var events []lager.Data
	for _, log := range output.Logs() {
		if _, ok := log.Data["name"]; ok {
			events = append(events, log.Data)
		}
	}
	return events
}

func (output *metricLogOutput) EventCount() int {
	return len(output.MetricEvents())
}

func monitorWithLager() (*metric.Monitor, *metricLogOutput, lager.Logger) {
	GinkgoHelper()

	output := &metricLogOutput{}
	logger := lager.NewLogger("metric-test")
	logger.RegisterSink(lager.NewWriterSink(output, lager.DEBUG))

	monitor := metric.NewMonitor()
	monitor.RegisterEmitter(&metricemitter.LagerConfig{Enabled: true})
	Expect(monitor.Initialize(logger, "test", map[string]string{}, 1000)).To(Succeed())

	return monitor, output, logger
}
