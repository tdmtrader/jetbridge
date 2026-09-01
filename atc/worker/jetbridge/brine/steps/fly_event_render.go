package steps

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/event"
	flyeventstream "github.com/concourse/concourse/fly/eventstream"
	"github.com/concourse/concourse/fly/ui"
	clientstream "github.com/concourse/concourse/go-concourse/concourse/eventstream"
	"github.com/fatih/color"
	"github.com/vito/go-sse/sse"
)

type FlyRenderOutcome struct {
	Output       string
	Exit         int
	HasTimestamp bool
}

func FlyEventRenderDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[brine.Empty, FlyRenderOutcome](
			"fly renders the real SSE event profile {string}",
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder) (FlyRenderOutcome, error) {
				profile, ok := p.GetString(0)
				if !ok {
					return FlyRenderOutcome{}, fmt.Errorf("expected Fly render profile")
				}
				return renderFlyEventProfile(profile)
			},
		),
		CheckContains[FlyRenderOutcome]("fly output contains {string}", "Fly output",
			func(in FlyRenderOutcome) (string, error) { return in.Output, nil }),
		brine.DefineCheck[FlyRenderOutcome](
			"fly output does not contain {string}",
			func(in FlyRenderOutcome, p brine.Params, _ *brine.Recorder) error {
				unexpected, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected excluded Fly output")
				}
				if strings.Contains(in.Output, unexpected) {
					return fmt.Errorf("Fly output %q contains %q", in.Output, unexpected)
				}
				return nil
			},
		),
		CheckInt[FlyRenderOutcome]("fly exits with status {int}", "Fly exit status",
			func(in FlyRenderOutcome) (int, error) { return in.Exit, nil }),
		brine.DefineCheck[FlyRenderOutcome](
			"fly output has a timestamp prefix",
			func(in FlyRenderOutcome, _ brine.Params, _ *brine.Recorder) error {
				if !in.HasTimestamp {
					return fmt.Errorf("Fly output has no timestamp prefix: %q", in.Output)
				}
				return nil
			},
		),
		brine.DefineCheck[FlyRenderOutcome](
			"fly output has a blank timestamp prefix",
			func(in FlyRenderOutcome, _ brine.Params, _ *brine.Recorder) error {
				if !regexp.MustCompile(`(?m)^\s{10}\S`).MatchString(in.Output) {
					return fmt.Errorf("Fly output has no blank timestamp prefix: %q", in.Output)
				}
				return nil
			},
		),
		brine.DefineCheck[FlyRenderOutcome](
			"fly renders {string} with status color {string}",
			func(in FlyRenderOutcome, p brine.Params, _ *brine.Recorder) error {
				text, colorName, err := twoParams("fly renders {string} with status color {string}", p)
				if err != nil {
					return err
				}
				var colored string
				switch colorName {
				case "green":
					colored = ui.SucceededColor.Sprint(text)
				case "red":
					colored = ui.FailedColor.Sprint(text)
				case "bold-red":
					colored = ui.ErroredColor.Sprint(text)
				case "magenta":
					colored = ui.AbortedColor.Sprint(text)
				default:
					return fmt.Errorf("unknown status color %q", colorName)
				}
				if !strings.Contains(in.Output, colored) {
					return fmt.Errorf("Fly output %q does not contain %q", in.Output, colored)
				}
				return nil
			},
		),
	}
}

func renderFlyEventProfile(profile string) (FlyRenderOutcome, error) {
	color.NoColor = false
	now := time.Unix(1710000000, 0).Unix()
	showTimestamp := strings.HasSuffix(profile, "/time")
	ignoreErrors := strings.HasSuffix(profile, "/ignore")
	base := strings.TrimSuffix(strings.TrimSuffix(profile, "/time"), "/ignore")
	frames := []sse.Event{}
	writeEvent := func(ev atc.Event) error {
		payload, err := json.Marshal(event.Message{Event: ev})
		if err != nil {
			return err
		}
		frames = append(frames, sse.Event{ID: strconv.Itoa(len(frames)), Name: "event", Data: payload})
		return nil
	}
	writeRaw := func(payload string) {
		frames = append(frames, sse.Event{ID: strconv.Itoa(len(frames)), Name: "event", Data: []byte(payload)})
	}

	var err error
	switch base {
	case "log":
		err = writeEvent(event.Log{Payload: "hello", Time: now})
	case "error":
		err = writeEvent(event.Error{Message: "oh no!"})
	case "initialize":
		err = writeEvent(event.InitializeTask{Time: now})
	case "start":
		err = writeEvent(event.StartTask{
			Time: now,
			TaskConfig: event.TaskConfig{
				Image: "some-image",
				Run:   event.TaskRunConfig{Path: "/some/script", Args: []string{"arg1", "arg2"}},
			},
		})
	case "finish":
		err = writeEvent(event.FinishTask{ExitStatus: 42})
	case "finish-status":
		if err = writeEvent(event.FinishTask{ExitStatus: 42}); err == nil {
			err = writeEvent(event.Status{Status: atc.StatusSucceeded, Time: now})
		}
	case "status-succeeded":
		err = writeEvent(event.Status{Status: atc.StatusSucceeded, Time: now})
	case "status-failed":
		err = writeEvent(event.Status{Status: atc.StatusFailed, Time: now})
	case "status-errored":
		err = writeEvent(event.Status{Status: atc.StatusErrored, Time: now})
	case "status-aborted":
		err = writeEvent(event.Status{Status: atc.StatusAborted, Time: now})
	case "waiting":
		err = writeEvent(event.WaitingForWorker{Time: now})
	case "selected":
		err = writeEvent(event.SelectedWorker{Time: now, WorkerName: "some-worker"})
	case "sidecar-attached", "sidecar-log", "sidecar-main-log":
		planBytes, marshalErr := json.Marshal(atc.Plan{ID: "abc123/sidecar/log-emitter", Sidecar: &atc.SidecarPlan{Name: "log-emitter", Image: "alpine:latest"}})
		if marshalErr != nil {
			return FlyRenderOutcome{}, marshalErr
		}
		plan := json.RawMessage(planBytes)
		err = writeEvent(event.Sidecar{Time: now, Origin: event.Origin{ID: "abc123"}, PublicPlan: &plan})
		if err == nil && base == "sidecar-log" {
			err = writeEvent(event.Log{Time: now, Origin: event.Origin{ID: "abc123/sidecar/log-emitter"}, Payload: "hello from sidecar\n"})
		}
		if err == nil && base == "sidecar-main-log" {
			err = writeEvent(event.Log{Time: now, Origin: event.Origin{ID: "abc123"}, Payload: "hello from main\n"})
		}
	case "unknown-event":
		writeRaw(`{"data":{},"event":"some-event","version":"1.0"}`)
		writeRaw(`{"data":{},"event":"status","version":"9.0"}`)
	case "missing-data":
		writeRaw(`{"event":"some-event","version":"1.0"}`)
		writeRaw(`{"event":"log","version":"5.1"}`)
	default:
		return FlyRenderOutcome{}, fmt.Errorf("unknown Fly render profile %q", profile)
	}
	if err != nil {
		return FlyRenderOutcome{}, err
	}

	var body bytes.Buffer
	for _, frame := range frames {
		if err := frame.Write(&body); err != nil {
			return FlyRenderOutcome{}, err
		}
	}
	if err := (sse.Event{ID: strconv.Itoa(len(frames)), Name: "end"}).Write(&body); err != nil {
		return FlyRenderOutcome{}, err
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		_, _ = w.Write(body.Bytes())
	}))
	defer server.Close()
	source, err := sse.Connect(server.Client(), time.Second, func() *http.Request {
		req, _ := http.NewRequest(http.MethodGet, server.URL, nil)
		return req
	})
	if err != nil {
		return FlyRenderOutcome{}, err
	}
	stream := clientstream.NewSSEEventStream(source)
	defer stream.Close()
	var output bytes.Buffer
	exit := flyeventstream.Render(&output, stream, flyeventstream.RenderOptions{ShowTimestamp: showTimestamp, IgnoreEventParsingErrors: ignoreErrors})
	text := output.String()
	return FlyRenderOutcome{Output: text, Exit: exit, HasTimestamp: regexp.MustCompile(`\d{2}:\d{2}:\d{2}\s{2}`).MatchString(text)}, nil
}
