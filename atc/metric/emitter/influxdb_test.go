package emitter_test

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/atc/metric/emitter"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/ghttp"
)

type influxWrite struct {
	database string
	username string
	password string
	points   []string
}

type influxWriteRecorder struct {
	mutex        sync.Mutex
	writes       []influxWrite
	status       int
	responseBody string
}

func (recorder *influxWriteRecorder) Handle(res http.ResponseWriter, req *http.Request) {
	body, err := io.ReadAll(req.Body)

	recorder.mutex.Lock()
	defer recorder.mutex.Unlock()

	if err == nil {
		username, password, _ := req.BasicAuth()

		var points []string
		for _, line := range strings.Split(string(body), "\n") {
			if line != "" {
				points = append(points, line)
			}
		}

		recorder.writes = append(recorder.writes, influxWrite{
			database: req.URL.Query().Get("db"),
			username: username,
			password: password,
			points:   points,
		})
	}

	res.WriteHeader(recorder.status)
	res.Write([]byte(recorder.responseBody))
}

func (recorder *influxWriteRecorder) Count() int {
	recorder.mutex.Lock()
	defer recorder.mutex.Unlock()

	return len(recorder.writes)
}

func (recorder *influxWriteRecorder) Write(index int) influxWrite {
	recorder.mutex.Lock()
	defer recorder.mutex.Unlock()

	return recorder.writes[index]
}

func (recorder *influxWriteRecorder) RespondWith(status int, body string) {
	recorder.mutex.Lock()
	defer recorder.mutex.Unlock()

	recorder.status = status
	recorder.responseBody = body
}

func loggedErrors(logger *lagertest.TestLogger) []string {
	var errors []string
	for _, log := range logger.Logs() {
		if err, found := log.Data["error"]; found {
			errors = append(errors, fmt.Sprintf("%s: %v", log.Message, err))
		}
	}
	return errors
}

var _ = Describe("InfluxDBEmitter", func() {
	var (
		server          *ghttp.Server
		recorder        *influxWriteRecorder
		influxDBEmitter *emitter.InfluxDBEmitter
		testLogger      *lagertest.TestLogger
	)

	newEmitter := func(config emitter.InfluxDBConfig) *emitter.InfluxDBEmitter {
		config.URL = server.URL()

		created, err := config.NewEmitter(nil)
		Expect(err).ToNot(HaveOccurred())

		return created.(*emitter.InfluxDBEmitter)
	}

	event := func(name string, value float64) metric.Event {
		return metric.Event{
			Name:  name,
			Value: value,
			Host:  "localhost",
		}
	}

	BeforeEach(func() {
		testLogger = lagertest.NewTestLogger("test")

		recorder = &influxWriteRecorder{status: http.StatusNoContent}

		server = ghttp.NewServer()
		server.RouteToHandler("POST", "/write", recorder.Handle)
	})

	AfterEach(func() {
		server.Close()
	})

	Context("with batch size 2", func() {
		BeforeEach(func() {
			influxDBEmitter = newEmitter(emitter.InfluxDBConfig{
				Database:      "concourse",
				Username:      "admin",
				Password:      "s3cret",
				BatchSize:     2,
				BatchDuration: time.Hour,
			})
		})

		It("writes nothing while the batch is not full", func() {
			influxDBEmitter.Emit(testLogger, event("build started", 1))

			Consistently(recorder.Count).Should(BeZero())
		})

		It("writes one batch once the batch is full", func() {
			for i := 0; i < 3; i++ {
				influxDBEmitter.Emit(testLogger, event("build started", float64(i)))
			}

			Eventually(recorder.Count).Should(Equal(1))
			Consistently(recorder.Count).Should(Equal(1))
		})

		It("writes two batches once the batch has filled twice", func() {
			for i := 0; i < 4; i++ {
				influxDBEmitter.Emit(testLogger, event("build started", float64(i)))
			}

			Eventually(recorder.Count).Should(Equal(2))
		})

		It("writes the points to the configured database as the configured user", func() {
			for i := 0; i < 2; i++ {
				influxDBEmitter.Emit(testLogger, event("build started", float64(i)))
			}

			Eventually(recorder.Count).Should(Equal(1))
			Expect(recorder.Write(0).database).To(Equal("concourse"))
			Expect(recorder.Write(0).username).To(Equal("admin"))
			Expect(recorder.Write(0).password).To(Equal("s3cret"))
		})

		It("writes a partial batch when the batch is submitted explicitly", func() {
			influxDBEmitter.Emit(testLogger, event("build started", 1))
			influxDBEmitter.SubmitBatch(testLogger)

			Eventually(recorder.Count).Should(Equal(1))
			Expect(recorder.Write(0).points).To(Equal([]string{
				`build\ started,host=localhost value=1`,
			}))
		})

		It("writes the points as line protocol", func() {
			influxDBEmitter.Emit(testLogger, metric.Event{
				Name:  "build started",
				Value: 123,
				Attributes: map[string]string{
					"pipeline":   "test1",
					"job":        "job1",
					"build_name": "build1",
					"build_id":   "123",
					"team_name":  "team1",
				},
				Host: "localhost",
			})
			influxDBEmitter.Emit(testLogger, metric.Event{
				Name:  "build finished",
				Value: 100,
				Attributes: map[string]string{
					"pipeline":     "test2",
					"job":          "job2",
					"build_name":   "build2",
					"build_id":     "456",
					"build_status": "succeeded",
					"team_name":    "team2",
				},
				Host: "localhost",
			})

			Eventually(recorder.Count).Should(Equal(1))
			Expect(recorder.Write(0).points).To(Equal([]string{
				`build\ started,build_id=123,build_name=build1,host=localhost,job=job1,pipeline=test1,team_name=team1 value=123`,
				`build\ finished,build_id=456,build_name=build2,build_status=succeeded,host=localhost,job=job2,pipeline=test2,team_name=team2 value=100`,
			}))
		})

		It("batches independently of other emitters", func() {
			otherEmitter := newEmitter(emitter.InfluxDBConfig{
				Database:      "concourse",
				BatchSize:     2,
				BatchDuration: time.Hour,
			})

			influxDBEmitter.Emit(testLogger, event("first", 1))
			otherEmitter.Emit(testLogger, event("second", 2))

			Consistently(recorder.Count).Should(BeZero())

			influxDBEmitter.Emit(testLogger, event("third", 3))

			Eventually(recorder.Count).Should(Equal(1))
			Expect(recorder.Write(0).points).To(Equal([]string{
				`first,host=localhost value=1`,
				`third,host=localhost value=3`,
			}))
		})

		Context("when InfluxDB rejects the write", func() {
			BeforeEach(func() {
				recorder.RespondWith(http.StatusInternalServerError, "partial write: field type conflict")
			})

			It("logs the failure as a failure to emit", func() {
				for i := 0; i < 2; i++ {
					influxDBEmitter.Emit(testLogger, event("build started", float64(i)))
				}

				Eventually(func() []string {
					return loggedErrors(testLogger)
				}).Should(ContainElement(ContainSubstring(
					"failed-to-send-points: failed to emit metric: partial write: field type conflict",
				)))
			})
		})

		Context("when InfluxDB accepts the write", func() {
			It("logs no failure", func() {
				for i := 0; i < 2; i++ {
					influxDBEmitter.Emit(testLogger, event("build started", float64(i)))
				}

				Eventually(recorder.Count).Should(Equal(1))
				Consistently(func() []string {
					return loggedErrors(testLogger)
				}).Should(BeEmpty())
			})
		})
	})

	Context("with batch duration 50 milliseconds", func() {
		BeforeEach(func() {
			influxDBEmitter = newEmitter(emitter.InfluxDBConfig{
				Database:      "concourse",
				BatchSize:     5000,
				BatchDuration: 50 * time.Millisecond,
			})
		})

		It("writes the batch on the first event after the duration has elapsed", func() {
			influxDBEmitter.Emit(testLogger, event("build started", 1))
			time.Sleep(60 * time.Millisecond)
			influxDBEmitter.Emit(testLogger, event("build finished", 2))

			Eventually(recorder.Count).Should(Equal(1))
			Expect(recorder.Write(0).points).To(Equal([]string{
				`build\ started,host=localhost value=1`,
				`build\ finished,host=localhost value=2`,
			}))
		})

		It("restarts the duration when it writes the batch", func() {
			influxDBEmitter.Emit(testLogger, event("first", 1))
			time.Sleep(60 * time.Millisecond)
			influxDBEmitter.Emit(testLogger, event("second", 2))

			Eventually(recorder.Count).Should(Equal(1))

			influxDBEmitter.Emit(testLogger, event("third", 3))

			Consistently(recorder.Count).Should(Equal(1))

			time.Sleep(60 * time.Millisecond)
			influxDBEmitter.Emit(testLogger, event("fourth", 4))

			Eventually(recorder.Count).Should(Equal(2))
			Expect(recorder.Write(1).points).To(Equal([]string{
				`third,host=localhost value=3`,
				`fourth,host=localhost value=4`,
			}))
		})
	})
})
