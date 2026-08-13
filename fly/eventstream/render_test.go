package eventstream_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"time"

	"github.com/fatih/color"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/onsi/gomega/gbytes"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/event"
	"github.com/concourse/concourse/fly/eventstream"
	"github.com/concourse/concourse/fly/ui"
	clientstream "github.com/concourse/concourse/go-concourse/concourse/eventstream"
	"github.com/vito/go-sse/sse"
)

var _ = Describe("V1.0 Renderer", func() {
	var (
		out     *gbytes.Buffer
		options eventstream.RenderOptions

		frames []sse.Event
		server *httptest.Server
		stream *clientstream.SSEEventStream

		exitStatus int
	)

	writeEvent := func(ev atc.Event) {
		payload, err := json.Marshal(event.Message{Event: ev})
		Expect(err).NotTo(HaveOccurred())

		frames = append(frames, sse.Event{
			ID:   strconv.Itoa(len(frames)),
			Name: "event",
			Data: payload,
		})
	}

	writeRawEvent := func(payload string) {
		frames = append(frames, sse.Event{
			ID:   strconv.Itoa(len(frames)),
			Name: "event",
			Data: []byte(payload),
		})
	}

	BeforeEach(func() {
		color.NoColor = false
		out = gbytes.NewBuffer()
		options = eventstream.RenderOptions{}
		frames = nil
	})

	JustBeforeEach(func() {
		var body bytes.Buffer
		for _, frame := range frames {
			Expect(frame.Write(&body)).To(Succeed())
		}
		Expect(sse.Event{ID: strconv.Itoa(len(frames)), Name: "end"}.Write(&body)).To(Succeed())

		server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			w.Write(body.Bytes())
		}))

		source, err := sse.Connect(server.Client(), time.Second, func() *http.Request {
			request, err := http.NewRequest("GET", server.URL, nil)
			Expect(err).NotTo(HaveOccurred())
			return request
		})
		Expect(err).NotTo(HaveOccurred())

		stream = clientstream.NewSSEEventStream(source)

		exitStatus = eventstream.Render(out, stream, options)
	})

	AfterEach(func() {
		Expect(stream.Close()).To(Succeed())
		server.Close()
	})

	Context("when a Log event is received", func() {
		BeforeEach(func() {
			writeEvent(event.Log{
				Payload: "hello",
				Time:    time.Now().Unix(),
			})
		})

		It("prints its payload", func() {
			Expect(out).To(gbytes.Say("hello"))
		})

		Context("and time configuration is enabled", func() {
			BeforeEach(func() {
				options.ShowTimestamp = true
			})

			It("prints its payload with a timestamp", func() {
				Expect(out).To(gbytes.Say(`\d{2}\:\d{2}\:\d{2}\s{2}hello`))
			})

		})
	})

	Context("when an Error event is received", func() {
		BeforeEach(func() {
			writeEvent(event.Error{
				Message: "oh no!",
			})
		})

		It("prints its message in bold red, followed by a linebreak", func() {
			Expect(out.Contents()).To(ContainSubstring(ui.ErroredColor.SprintFunc()("oh no!") + "\n"))
		})

		Context("and time configuration is enabled", func() {
			BeforeEach(func() {
				options.ShowTimestamp = true
			})

			It("empty space is prefixed", func() {
				Expect(out).To(gbytes.Say(`\s{10}\w*`))
			})
		})
	})

	Context("when an InitializeTask event is received", func() {
		BeforeEach(func() {
			writeEvent(event.InitializeTask{
				Time: time.Now().Unix(),
			})
		})

		It("prints initializing", func() {
			Expect(out.Contents()).To(ContainSubstring("\x1b[1minitializing\x1b[0m\n"))
		})

		Context("and time configuration is enabled", func() {
			BeforeEach(func() {
				options.ShowTimestamp = true
			})

			It("timestamp is prefixed", func() {
				Expect(out).To(gbytes.Say(`\d{2}\:\d{2}\:\d{2}\s{2}\w*`))
			})
		})
	})

	Context("and a StartTask event is received", func() {
		BeforeEach(func() {
			writeEvent(event.StartTask{
				Time: time.Now().Unix(),
				TaskConfig: event.TaskConfig{
					Image: "some-image",
					Run: event.TaskRunConfig{
						Path: "/some/script",
						Args: []string{"arg1", "arg2"},
					},
				},
			})
		})

		It("prints the build's run script", func() {
			Expect(out.Contents()).To(ContainSubstring("\x1b[1mrunning /some/script arg1 arg2\x1b[0m\n"))
		})

		Context("and time configuration enabled", func() {
			BeforeEach(func() {
				options.ShowTimestamp = true
			})

			It("timestamp is prefixed", func() {
				Expect(out).To(gbytes.Say(`\d{2}\:\d{2}\:\d{2}\s{2}\w*`))
			})
		})
	})

	Context("when a FinishTask event is received", func() {
		BeforeEach(func() {
			writeEvent(event.FinishTask{
				ExitStatus: 42,
			})
		})

		It("returns its exit status", func() {
			Expect(exitStatus).To(Equal(42))
		})

		Context("and a Status event is received", func() {
			BeforeEach(func() {
				writeEvent(event.Status{
					Status: atc.StatusSucceeded,
				})
			})

			It("still processes it", func() {
				Expect(out.Contents()).To(ContainSubstring("succeeded"))
			})

			It("exits with the status from the FinishTask event", func() {
				Expect(exitStatus).To(Equal(42))
			})

			Context("and time configuration is enabled", func() {
				BeforeEach(func() {
					options.ShowTimestamp = true
				})

				It("empty string is prefixed", func() {
					Expect(out).To(gbytes.Say(`\s{10}\w*`))
				})
			})
		})
	})

	Describe("receiving a Status event", func() {
		Context("with status 'succeeded'", func() {
			BeforeEach(func() {
				writeEvent(event.Status{
					Status: atc.StatusSucceeded,
					Time:   time.Now().Unix(),
				})
			})

			It("prints it in green", func() {
				Expect(out.Contents()).To(ContainSubstring(ui.SucceededColor.SprintFunc()("succeeded") + "\n"))
			})

			It("exits 0", func() {
				Expect(exitStatus).To(Equal(0))
			})

			Context("and time configuration is enabled", func() {
				BeforeEach(func() {
					options.ShowTimestamp = true
				})

				It("timestamp is prefixed", func() {
					Expect(out).To(gbytes.Say(`\d{2}\:\d{2}\:\d{2}\s{2}\w*`))
				})
			})
		})

		Context("with status 'failed'", func() {
			BeforeEach(func() {
				writeEvent(event.Status{
					Status: atc.StatusFailed,
					Time:   time.Now().Unix(),
				})
			})

			It("prints it in red", func() {
				Expect(out.Contents()).To(ContainSubstring(ui.FailedColor.SprintFunc()("failed") + "\n"))
			})

			It("exits 1", func() {
				Expect(exitStatus).To(Equal(1))
			})

			Context("and time configuration is enabled", func() {
				BeforeEach(func() {
					options.ShowTimestamp = true
				})

				It("timestamp is prefixed", func() {
					Expect(out).To(gbytes.Say(`\d{2}\:\d{2}\:\d{2}\s{2}\w*`))
				})
			})
		})

		Context("with status 'errored'", func() {
			BeforeEach(func() {
				writeEvent(event.Status{
					Status: atc.StatusErrored,
					Time:   time.Now().Unix(),
				})
			})

			It("prints it in bold red", func() {
				Expect(out.Contents()).To(ContainSubstring(ui.ErroredColor.SprintFunc()("errored") + "\n"))
			})

			It("exits 2", func() {
				Expect(exitStatus).To(Equal(2))
			})

			Context("and time configuration is enabled", func() {
				BeforeEach(func() {
					options.ShowTimestamp = true
				})

				It("timestamp is prefixed", func() {
					Expect(out).To(gbytes.Say(`\d{2}\:\d{2}\:\d{2}\s{2}\w*`))
				})
			})
		})

		Context("with status 'aborted'", func() {
			BeforeEach(func() {
				writeEvent(event.Status{
					Status: atc.StatusAborted,
					Time:   time.Now().Unix(),
				})
			})

			It("prints it in yellow", func() {
				Expect(out.Contents()).To(ContainSubstring(ui.AbortedColor.SprintFunc()("aborted") + "\n"))
			})

			It("exits 3", func() {
				Expect(exitStatus).To(Equal(3))
			})

			Context("and time configuration is enabled", func() {
				BeforeEach(func() {
					options.ShowTimestamp = true
				})

				It("timestamp is prefixed", func() {
					Expect(out).To(gbytes.Say(`\d{2}\:\d{2}\:\d{2}\s{2}\w*`))
				})
			})
		})
	})

	Context("when a WaitingForWorker event is received", func() {
		BeforeEach(func() {
			writeEvent(event.WaitingForWorker{
				Time: time.Now().Unix(),
			})
		})

		It("prints the build's run script", func() {
			Expect(out.Contents()).To(ContainSubstring("\x1b[1mno suitable workers found, waiting for worker...\x1b[0m\n"))
		})

		Context("and time configuration enabled", func() {
			BeforeEach(func() {
				options.ShowTimestamp = true
			})

			It("timestamp is prefixed", func() {
				Expect(out).To(gbytes.Say(`\d{2}\:\d{2}\:\d{2}\s{2}\w*`))
			})
		})
	})

	Context("when a SelectedWorker event is received", func() {
		BeforeEach(func() {
			writeEvent(event.SelectedWorker{
				Time:       time.Now().Unix(),
				WorkerName: "some-worker",
			})
		})

		It("prints the build's run script", func() {
			Expect(out.Contents()).To(ContainSubstring("\x1b[1mselected worker:\u001B[0m some-worker\n"))
		})

		Context("and time configuration enabled", func() {
			BeforeEach(func() {
				options.ShowTimestamp = true
			})

			It("timestamp is prefixed", func() {
				Expect(out).To(gbytes.Say(`\d{2}\:\d{2}\:\d{2}\s{2}\w*`))
			})
		})
	})

	Context("when a Sidecar event is received", func() {
		var sidecarPlan json.RawMessage

		BeforeEach(func() {
			plan := atc.Plan{
				ID: "abc123/sidecar/log-emitter",
				Sidecar: &atc.SidecarPlan{
					Name:  "log-emitter",
					Image: "alpine:latest",
				},
			}
			planBytes, err := json.Marshal(plan)
			Expect(err).ToNot(HaveOccurred())
			sidecarPlan = json.RawMessage(planBytes)

			writeEvent(event.Sidecar{
				Time:       time.Now().Unix(),
				Origin:     event.Origin{ID: "abc123"},
				PublicPlan: &sidecarPlan,
			})
		})

		It("prints a sidecar attached header", func() {
			Expect(out.Contents()).To(ContainSubstring("sidecar 'log-emitter' attached"))
		})

		Context("and a Log event is received from the sidecar", func() {
			BeforeEach(func() {
				writeEvent(event.Log{
					Time:    time.Now().Unix(),
					Origin:  event.Origin{ID: "abc123/sidecar/log-emitter"},
					Payload: "hello from sidecar\n",
				})
			})

			It("prefixes the log line with the sidecar name", func() {
				Expect(out.Contents()).To(ContainSubstring("[log-emitter]"))
				Expect(out.Contents()).To(ContainSubstring("hello from sidecar"))
			})
		})

		Context("and a Log event is received from the main container", func() {
			BeforeEach(func() {
				writeEvent(event.Log{
					Time:    time.Now().Unix(),
					Origin:  event.Origin{ID: "abc123"},
					Payload: "hello from main\n",
				})
			})

			It("does not prefix the log line with a sidecar name", func() {
				Expect(out.Contents()).To(ContainSubstring("hello from main"))
				Expect(out.Contents()).ToNot(ContainSubstring("[log-emitter]"))
			})
		})
	})

	Context("when an UnknownEventTypeError or UnknownEventVersionError is received", func() {

		BeforeEach(func() {
			writeRawEvent(`{"data":{},"event":"some-event","version":"1.0"}`)
			writeRawEvent(`{"data":{},"event":"status","version":"9.0"}`)
		})

		It("prints the build's run script", func() {
			Expect(out.Contents()).To(ContainSubstring("failed to parse next event"))
		})

		It("exits with 255 exit code", func() {
			Expect(exitStatus).To(Equal(255))
		})

		Context("when IgnoreEventParsingErrors is configured", func() {
			BeforeEach(func() {
				options.IgnoreEventParsingErrors = true
			})
			It("exits with 0 exit code", func() {
				Expect(exitStatus).To(Equal(0))
			})

		})

	})

	Context("when an event with no data is received", func() {

		BeforeEach(func() {
			writeRawEvent(`{"event":"some-event","version":"1.0"}`)
			writeRawEvent(`{"event":"log","version":"5.1"}`)
		})

		It("reports it as a parse failure", func() {
			Expect(out.Contents()).To(ContainSubstring("failed to parse next event"))
			Expect(out.Contents()).To(ContainSubstring("missing event data: some-event version 1.0"))
		})

		It("exits with 255 exit code", func() {
			Expect(exitStatus).To(Equal(255))
		})

		Context("when IgnoreEventParsingErrors is configured", func() {
			BeforeEach(func() {
				options.IgnoreEventParsingErrors = true
			})

			It("exits with 0 exit code", func() {
				Expect(exitStatus).To(Equal(0))
			})

		})

	})

})
