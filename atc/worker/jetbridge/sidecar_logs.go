package jetbridge

import (
	"fmt"
	"io"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/runtime"
)

// sidecarLogWriters pairs every declared sidecar with the writer the engine
// handed the step for it, in ProcessIO.SidecarWriters.
//
// The engine makes one per declared sidecar (atc/exec TaskStep.buildProcessIO),
// and each emits Log events under the sidecar's own plan ID, which is how fly
// watch and the web tell whose lines they are. There is no second place for a
// sidecar's output to go: the "[name] "-prefixed fallback into the step's
// stdout was a second mechanism for the one job, and a step that declares a
// sidecar it has no writer for is a caller bug this names rather than hides.
func sidecarLogWriters(sidecars []atc.SidecarConfig, pio runtime.ProcessIO) (map[string]io.Writer, error) {
	if len(sidecars) == 0 {
		return nil, nil
	}
	writers := make(map[string]io.Writer, len(sidecars))
	for _, sc := range sidecars {
		w := pio.SidecarWriters[sc.Name]
		if w == nil {
			return nil, fmt.Errorf("sidecar %q has no log writer: the step declared it without one", sc.Name)
		}
		writers[sc.Name] = w
	}
	return writers, nil
}

// closeSidecarWriter closes a sidecar's writer, if it can be closed, once its
// stream has ended. Only the streaming goroutine knows when that is, and it
// is the writer's only writer, so the close cannot race a write. The engine's
// writer redacts whole lines and holds an unterminated one back until it is
// closed; without this a sidecar's last line would never reach the build log.
func closeSidecarWriter(w io.Writer) {
	if c, ok := w.(io.Closer); ok {
		_ = c.Close()
	}
}
