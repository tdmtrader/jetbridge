package steps

import (
	"encoding/json"
	"io"

	"github.com/brine-dev/brine-go/pkg/brine"
)

// WithDisposalVerdict includes suite cleanup failures in the engine protocol.
func WithDisposalVerdict(out io.Writer) io.Writer { return disposalVerdictWriter{out: out} }

// disposalVerdictWriter bridges the pinned runner's report-only suite disposal
// to the engine verdict. pipeline.go:510 drains ScopeSuite before :514 emits
// run_end, but discards DisposeScope's errors. JsonlEmitter writes one complete
// event per Write. Preserve that boundary and add a run-level cleanup failure
// (not a fictional scenario) to failed. Diagnostics remain for the exit report.
type disposalVerdictWriter struct{ out io.Writer }

func (w disposalVerdictWriter) Write(data []byte) (int, error) {
	var event struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &event); err != nil {
		return 0, err
	}
	if event.Type != "run_end" || DisposalFailureCount() == 0 {
		return w.out.Write(data)
	}
	var end brine.RunEnd
	if err := json.Unmarshal(data, &end); err != nil {
		return 0, err
	}
	end.Failed++
	encoded, err := json.Marshal(end)
	if err != nil {
		return 0, err
	}
	encoded = append(encoded, '\n')
	n, err := w.out.Write(encoded)
	if err == nil && n != len(encoded) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return 0, err
	}
	return len(data), nil
}
