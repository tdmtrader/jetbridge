package eventstream

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/event"
	"github.com/concourse/concourse/fly/ui"
	"github.com/concourse/concourse/go-concourse/concourse/eventstream"
	"github.com/fatih/color"
)

type RenderOptions struct {
	ShowTimestamp            bool
	IgnoreEventParsingErrors bool
}

// sidecarColor is used for sidecar prefixes and headers — dim cyan to be
// visible but not overwhelming alongside main container output.
var sidecarColor = color.New(color.FgCyan)

func Render(dst io.Writer, src eventstream.EventStream, options RenderOptions) int {
	dstImpl := NewTimestampedWriter(dst, options.ShowTimestamp)

	exitStatus := 0

	// Track sidecar origin ID prefixes → sidecar names so we can prefix
	// their log lines. The origin ID format is "<parent>/sidecar/<name>".
	sidecarNames := map[string]string{}

	for {
		ev, err := src.NextEvent()
		if err != nil {
			if err == io.EOF {
				return exitStatus
			} else if options.IgnoreEventParsingErrors && isEventParseError(err) {
				continue
			} else {
				dstImpl.SetTimestamp(0)
				fmt.Fprintf(dstImpl, "failed to parse next event: %s\n", ui.ErroredColor.Sprint(err))
				return 255
			}
		}

		switch e := ev.(type) {
		case event.Log:
			dstImpl.SetTimestamp(e.Time)
			if name, ok := sidecarNames[string(e.Origin.ID)]; ok {
				prefix := sidecarColor.Sprintf("[%s] ", name)
				fmt.Fprintf(dstImpl, "%s%s", prefix, e.Payload)
			} else {
				fmt.Fprintf(dstImpl, "%s", e.Payload)
			}

		case event.WaitingForWorker:
			dstImpl.SetTimestamp(e.Time)
			fmt.Fprintf(dstImpl, "\x1b[1mno suitable workers found, waiting for worker...\x1b[0m\n")

		case event.SelectedWorker:
			dstImpl.SetTimestamp(e.Time)
			fmt.Fprintf(dstImpl, "\x1b[1mselected worker:\x1b[0m %s\n", e.WorkerName)

		case event.InitializeCheck:
			dstImpl.SetTimestamp(e.Time)
			fmt.Fprintf(dstImpl, "\x1b[1minitializing check:\x1b[0m %s\n", e.Name)

		case event.InitializeTask:
			dstImpl.SetTimestamp(e.Time)
			fmt.Fprintf(dstImpl, "\x1b[1minitializing\x1b[0m\n")

		case event.StartTask:
			buildConfig := e.TaskConfig

			argv := strings.Join(append([]string{buildConfig.Run.Path}, buildConfig.Run.Args...), " ")
			dstImpl.SetTimestamp(e.Time)
			fmt.Fprintf(dstImpl, "\x1b[1mrunning %s\x1b[0m\n", argv)

		case event.Sidecar:
			dstImpl.SetTimestamp(e.Time)
			if e.PublicPlan != nil {
				var plan atc.Plan
				if err := json.Unmarshal(*e.PublicPlan, &plan); err == nil && plan.Sidecar != nil {
					name := plan.Sidecar.Name
					if plan.ID != "" {
						sidecarNames[string(plan.ID)] = name
					}
					fmt.Fprintf(dstImpl, "\x1b[1msidecar '%s' attached\x1b[0m\n", name)
				}
			}

		case event.FinishTask:
			exitStatus = e.ExitStatus

		case event.Error:
			errCol := ui.ErroredColor.SprintFunc()
			dstImpl.SetTimestamp(0)
			fmt.Fprintf(dstImpl, "%s\n", errCol(e.Message))

		case event.Status:
			dstImpl.SetTimestamp(e.Time)
			var printColor *color.Color

			switch e.Status {
			case "started":
				continue
			case "succeeded":
				printColor = ui.SucceededColor
			case "failed":
				printColor = ui.FailedColor

				if exitStatus == 0 {
					exitStatus = 1
				}
			case "errored":
				printColor = ui.ErroredColor

				if exitStatus == 0 {
					exitStatus = 2
				}
			case "aborted":
				printColor = ui.AbortedColor

				if exitStatus == 0 {
					exitStatus = 3
				}
			default:
				fmt.Fprintf(dstImpl, "unknown status: %s", e.Status)
				return 255
			}

			printColorFunc := printColor.SprintFunc()
			fmt.Fprintf(dstImpl, "%s\n", printColorFunc(e.Status))

			return exitStatus
		}
	}
}

func isEventParseError(err error) bool {
	if _, ok := err.(event.UnknownEventTypeError); ok {
		return true
	} else if _, ok := err.(event.UnknownEventVersionError); ok {
		return true
	}
	return false
}

