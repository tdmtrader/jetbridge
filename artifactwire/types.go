package artifactwire

// DurableTierHeader is set by a daemon that has a durable tier configured. Its
// presence on any response, at any status, is how the ATC learns the cluster
// can serve a warm at all. Capability rides an existing response rather than
// being probed for, so an ATC talking to daemons that predate the tier makes
// zero requests to a route they do not have.
const DurableTierHeader = "X-Durable-Tier"

// ArtifactTierHeader says where a restore's bytes came from: "durable" if the
// call fetched them, "local" if they were already there.
const ArtifactTierHeader = "X-Artifact-Tier"

// RegisterRequest is the body of POST /register: a node-local alias for bytes
// already on this daemon's disk.
//
// One request, not two. DurableKey and ReadOnly are independent facts about
// the alias and the daemon reads both; a caller may legitimately name a
// read-only alias that is also kept for the long term.
type RegisterRequest struct {
	Key       string `json:"key"`
	LocalPath string `json:"local_path"`

	// DurableKey is the name to store this artifact under for the long term,
	// or empty for "do not keep it".
	//
	// Its presence is the entire eligibility protocol: whether an artifact is
	// re-derivable and how long to keep it are questions only the ATC can
	// answer, so the daemon neither parses this nor derives it from Key.
	//
	// Deliberately a separate field: Key names a node-local alias and stays a
	// single segment; DurableKey names a bucket object and carries a
	// retention-class prefix that a lifecycle rule acts on.
	DurableKey string `json:"durable_key,omitempty"`

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

// ResolveRequest is the body of POST /resolve and one item of a batch: copy
// the bytes under Key to Dest on this node.
type ResolveRequest struct {
	Key  string `json:"key"`
	Dest string `json:"dest"`

	// Capability is a short-lived token bound to this exact Key and Dest,
	// signed by the ATC with a key both sides share. Required whenever the
	// daemon was started with --resolve-capability-key.
	Capability string `json:"capability,omitempty"`
}

// ResolveResponse is the body returned by POST /resolve.
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

// DurableRestoreRequest is the body of POST /durable/restore.
//
// The key travels in the body rather than the path deliberately. As a path
// segment it would have to be un-escaped and joined onto the storage root,
// where "%2e%2e%2f%2e%2e%2fpwned" decodes to "../../pwned" and escapes the
// directory entirely. It also keeps this route consistent with /register,
// /resolve and /mirror.
//
// The two names are different namespaces and must not be conflated. Key is the
// node-local alias, and becomes a direct child of steps/, which is the only
// thing the sweeper reclaims, so it must be a single path segment. DurableKey
// names an object in a bucket and carries a retention-class prefix that an
// object lifecycle rule acts on.
type DurableRestoreRequest struct {
	Key        string `json:"key"`
	DurableKey string `json:"durable_key"`
}

// DurableRestoreResponse is the body returned by POST /durable/restore.
type DurableRestoreResponse struct {
	Restored bool   `json:"restored"`
	Node     string `json:"node,omitempty"`
	Path     string `json:"path,omitempty"`
	Duration string `json:"duration,omitempty"`
}

// CaptureClassResponse is the body of GET /capture-held/steps/{handle}: what
// the output ledger says about one step directory.
type CaptureClassResponse struct {
	Class  string `json:"class"`
	Handle string `json:"handle"`
	Reason string `json:"reason,omitempty"`
}

// TreeRef names an exact immutable Hangar tree. It is the wire shape of
// hangar.TreeRef, field for field and tag for tag, and deliberately not that
// type: this package is linked by the daemon's durable handlers, which must
// not import hangar. Each end converts at its own edge.
type TreeRef struct {
	Scope      string `json:"scope"`
	Digest     string `json:"digest"`
	Generation int64  `json:"generation"`
}

// MaterializationItem is one tree to materialize into one volume, with the
// warrant that authorizes exactly that.
type MaterializationItem struct {
	Ref     TreeRef `json:"ref"`
	Handle  string  `json:"handle"`
	Volume  string  `json:"volume"`
	Warrant string  `json:"warrant"`
}

// MaterializationRequest is the body of POST /hangar/v1/materializations.
type MaterializationRequest struct {
	Items []MaterializationItem `json:"items"`
}
