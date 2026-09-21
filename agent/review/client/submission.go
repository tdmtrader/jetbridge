package client

// SubmitOptions names local artifacts. Credentials are selected by local client
// configuration and never become Run parameters or MCP tool arguments.
type SubmitOptions struct {
	Team, Template           string
	Input, Receipt, AuthFile string
}

type Submission struct {
	RunID   int    `json:"run_id"`
	Handle  Handle `json:"handle"`
	Ready   bool   `json:"ready"`
	State   string `json:"state"`
	Message string `json:"message,omitempty"`
}
