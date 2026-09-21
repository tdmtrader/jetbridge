package atc

// RunCaptureProgress is authorized execution progress, retained independently
// of the disposable payload. It grants no access to a source or stored object.
type RunCaptureProgress struct {
	TaskID  string            `json:"task_id"`
	BuildID int               `json:"build_id"`
	Result  string            `json:"result"`
	Events  []RunCaptureEvent `json:"events"`
}

type RunCaptureEvent struct {
	Kind        string `json:"kind"`
	Disposition string `json:"disposition,omitempty"`
	Reason      string `json:"reason,omitempty"`
}
