package artifactwire

// RegisterRequest is the body of POST /register: a node-local alias for bytes
// already on this daemon's disk.
type RegisterRequest struct {
	Key       string `json:"key"`
	LocalPath string `json:"local_path"`

	// ReadOnly says this alias is a name for READING bytes another authority
	// owns, and it is the one kind the capture guard admits onto a held
	// incarnation.
	//
	// The guard refuses an alias onto a capture-held location because a second
	// write-capable name for bytes a capture is about to seal reaches them
	// under a name the capture never heard of. What is forbidden is that
	// mount, not a read, and a capture-selected task's output is still an
	// ordinary output that downstream steps must be able to fetch. So the
	// caller declares which of the two it is asking for, and refusing an
	// undeclared one is what keeps "read-only" from being a default nobody
	// chose.
	ReadOnly bool `json:"read_only,omitempty"`
}

// MirrorRequest is the body of POST /mirror.
type MirrorRequest struct {
	Key string `json:"key"`
}

// ResolveRequest is one item of a resolve batch: copy
// the bytes under Key to Dest on this node.
type ResolveRequest struct {
	Key  string `json:"key"`
	Dest string `json:"dest"`

	// Capability is a short-lived token bound to this exact Key and Dest,
	// signed by the ATC with a key both sides share. Required: the daemon
	// refuses a resolve without one.
	Capability string `json:"capability,omitempty"`
}

// ResolveResponse is one result in a resolve batch.
//
// Key is echoed on every result. A batch answers N items in one body, and
// without it a caller reading a failed batch knows only an INDEX into a list
// it has to have kept, which the one caller that matters, an init container
// shell script, has not. Naming the artifact in its own result is what lets a
// failure be read out of a build log.
type ResolveResponse struct {
	Key      string `json:"key,omitempty"`
	Status   string `json:"status"`
	Source   string `json:"source"`
	Method   string `json:"method"`
	Duration string `json:"duration,omitempty"`
	Error    string `json:"error,omitempty"`
}

// BatchResolveRequest is the body of POST /resolve-batch.
type BatchResolveRequest struct {
	Items []ResolveRequest `json:"items"`
}

// BatchResolveResponse is the body returned by POST /resolve-batch.
//
// Error summarises the failing items in one line. Results already carries the
// detail, but a caller that can only log one string, again the init
// container, gets nothing from a list it cannot index.
type BatchResolveResponse struct {
	Status  string            `json:"status"`
	Results []ResolveResponse `json:"results"`
	Error   string            `json:"error,omitempty"`
}

// CaptureClassResponse is the body of GET /capture-held/steps/{handle}: what
// the output ledger says about one step directory.
type CaptureClassResponse struct {
	Class  string `json:"class"`
	Handle string `json:"handle"`
	Reason string `json:"reason,omitempty"`
}
