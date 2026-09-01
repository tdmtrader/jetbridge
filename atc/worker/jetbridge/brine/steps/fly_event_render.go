package steps

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
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
	Output string
	Exit   int
}

var flyRenderColorMu sync.Mutex

func FlyEventRenderDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[brine.Empty, FlyRenderOutcome](
			"fly renders the production SSE profile {string}",
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder) (FlyRenderOutcome, error) {
				profile, ok := p.GetString(0)
				if !ok {
					return FlyRenderOutcome{}, fmt.Errorf("expected Fly render profile")
				}
				return renderFlyEventProfile(profile)
			},
		),
		brine.DefineCheck[FlyRenderOutcome](
			"fly terminal bytes match expectation {string}",
			func(in FlyRenderOutcome, p brine.Params, _ *brine.Recorder) error {
				name, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected Fly terminal expectation name")
				}
				expected, timestamped, err := expectedFlyTerminal(name)
				if err != nil {
					return err
				}
				if timestamped {
					pattern := `^\d{2}:\d{2}:\d{2}  ` + regexp.QuoteMeta(expected) + `$`
					if !regexp.MustCompile(pattern).MatchString(in.Output) {
						return fmt.Errorf("Fly terminal bytes %q do not match %q", in.Output, pattern)
					}
					return nil
				}
				if in.Output != expected {
					return fmt.Errorf("Fly terminal bytes %q, expected %q", in.Output, expected)
				}
				return nil
			},
		),
		CheckInt[FlyRenderOutcome]("fly exits with status {int}", "Fly exit status",
			func(in FlyRenderOutcome) (int, error) { return in.Exit, nil }),
	}
}

func renderFlyEventProfile(profile string) (FlyRenderOutcome, error) {
	flyRenderColorMu.Lock()
	defer flyRenderColorMu.Unlock()
	previousNoColor := color.NoColor
	color.NoColor = false
	ui.SucceededColor.EnableColor()
	ui.FailedColor.EnableColor()
	ui.ErroredColor.EnableColor()
	ui.AbortedColor.EnableColor()
	defer func() {
		ui.SucceededColor.DisableColor()
		ui.FailedColor.DisableColor()
		ui.ErroredColor.DisableColor()
		ui.AbortedColor.DisableColor()
		color.NoColor = previousNoColor
	}()

	now := time.Unix(1710000000, 0).Unix()
	showTimestamp := strings.HasSuffix(profile, "-time")
	ignoreErrors := strings.HasSuffix(profile, "-ignore")
	base := profile
	for _, suffix := range []string{"-output", "-exit", "-time", "-ignore"} {
		base = strings.TrimSuffix(base, suffix)
	}
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
			err = writeEvent(event.Status{Status: atc.StatusSucceeded})
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
	case "sidecar-attached", "sidecar-log", "sidecar-main":
		planBytes, marshalErr := json.Marshal(atc.Plan{ID: "abc123/sidecar/log-emitter", Sidecar: &atc.SidecarPlan{Name: "log-emitter", Image: "alpine:latest"}})
		if marshalErr != nil {
			return FlyRenderOutcome{}, marshalErr
		}
		plan := json.RawMessage(planBytes)
		err = writeEvent(event.Sidecar{Time: now, Origin: event.Origin{ID: "abc123"}, PublicPlan: &plan})
		if err == nil && base == "sidecar-log" {
			err = writeEvent(event.Log{Time: now, Origin: event.Origin{ID: "abc123/sidecar/log-emitter"}, Payload: "hello from sidecar\n"})
		}
		if err == nil && base == "sidecar-main" {
			err = writeEvent(event.Log{Time: now, Origin: event.Origin{ID: "abc123"}, Payload: "hello from main\n"})
		}
	case "unknown":
		writeRaw(`{"data":{},"event":"some-event","version":"1.0"}`)
		writeRaw(`{"data":{},"event":"status","version":"9.0"}`)
	case "missing":
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
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return FlyRenderOutcome{}, err
	}
	handlerDone := make(chan error, 1)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/events" {
			w.WriteHeader(http.StatusNotFound)
			handlerDone <- fmt.Errorf("unexpected SSE request %s %s", request.Method, request.URL.Path)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		_, writeErr := w.Write(body.Bytes())
		handlerDone <- writeErr
	})}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	stopServer := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shutdownErr := server.Shutdown(ctx)
		serveErr := <-serveDone
		if shutdownErr != nil {
			return shutdownErr
		}
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			return serveErr
		}
		return nil
	}

	transport := &http.Transport{Proxy: nil}
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	request, err := http.NewRequest(http.MethodGet, "http://"+listener.Addr().String()+"/events", nil)
	if err != nil {
		_ = stopServer()
		return FlyRenderOutcome{}, err
	}
	source, err := sse.Connect(client, time.Second, func() *http.Request {
		return request.Clone(context.Background())
	})
	if err != nil {
		_ = stopServer()
		return FlyRenderOutcome{}, err
	}
	stream := clientstream.NewSSEEventStream(source)
	var output bytes.Buffer
	exit := flyeventstream.Render(&output, stream, flyeventstream.RenderOptions{ShowTimestamp: showTimestamp, IgnoreEventParsingErrors: ignoreErrors})
	writeErr := <-handlerDone
	closeErr := stream.Close()
	transport.CloseIdleConnections()
	serverErr := stopServer()
	if writeErr != nil {
		return FlyRenderOutcome{}, writeErr
	}
	if closeErr != nil {
		return FlyRenderOutcome{}, closeErr
	}
	if serverErr != nil {
		return FlyRenderOutcome{}, serverErr
	}
	return FlyRenderOutcome{Output: output.String(), Exit: exit}, nil
}

func expectedFlyTerminal(name string) (string, bool, error) {
	const (
		bold      = "\x1b[1m"
		green     = "\x1b[32m"
		red       = "\x1b[31m"
		boldRed   = "\x1b[31;1m"
		magenta   = "\x1b[35m"
		reset     = "\x1b[0m"
		boldReset = "\x1b[0;22m"
		blank     = "          "
		attached  = bold + "sidecar 'log-emitter' attached" + reset + "\n"
	)
	switch name {
	case "log-output":
		return "hello", false, nil
	case "log-time":
		return "hello", true, nil
	case "error-output":
		return boldRed + "oh no!" + boldReset + "\n", false, nil
	case "error-time":
		return blank + boldRed + "oh no!" + boldReset + "\n", false, nil
	case "initialize-output":
		return bold + "initializing" + reset + "\n", false, nil
	case "initialize-time":
		return bold + "initializing" + reset + "\n", true, nil
	case "start-output":
		return bold + "running /some/script arg1 arg2" + reset + "\n", false, nil
	case "start-time":
		return bold + "running /some/script arg1 arg2" + reset + "\n", true, nil
	case "finish-status-output":
		return green + "succeeded" + reset + "\n", false, nil
	case "finish-status-time":
		return blank + green + "succeeded" + reset + "\n", false, nil
	case "status-succeeded-output":
		return green + "succeeded" + reset + "\n", false, nil
	case "status-succeeded-time":
		return green + "succeeded" + reset + "\n", true, nil
	case "status-failed-output":
		return red + "failed" + reset + "\n", false, nil
	case "status-failed-time":
		return red + "failed" + reset + "\n", true, nil
	case "status-errored-output":
		return boldRed + "errored" + boldReset + "\n", false, nil
	case "status-errored-time":
		return boldRed + "errored" + boldReset + "\n", true, nil
	case "status-aborted-output":
		return magenta + "aborted" + reset + "\n", false, nil
	case "status-aborted-time":
		return magenta + "aborted" + reset + "\n", true, nil
	case "waiting-output":
		return bold + "no suitable workers found, waiting for worker..." + reset + "\n", false, nil
	case "waiting-time":
		return bold + "no suitable workers found, waiting for worker..." + reset + "\n", true, nil
	case "selected-output":
		return bold + "selected worker:" + reset + " some-worker\n", false, nil
	case "selected-time":
		return bold + "selected worker:" + reset + " some-worker\n", true, nil
	case "sidecar-attached-output":
		return attached, false, nil
	case "sidecar-log-output":
		return attached + "[log-emitter] hello from sidecar\n", false, nil
	case "sidecar-main-output":
		return attached + "hello from main\n", false, nil
	case "unknown-output":
		return "failed to parse next event: " + boldRed + "unknown event type: some-event" + boldReset + "\n", false, nil
	case "missing-output":
		return "failed to parse next event: " + boldRed + "missing event data: some-event version 1.0" + boldReset + "\n", false, nil
	default:
		return "", false, fmt.Errorf("unknown Fly terminal expectation %q", name)
	}
}
