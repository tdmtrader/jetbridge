package atc

// RunCredentialSession retains delivery facts only. Claimed means delivery may
// have happened; it is never permission to send credentials again.
type RunCredentialSession struct {
	RunID  int    `json:"run_id"`
	Result string `json:"result"`
	Status string `json:"status"`
}
