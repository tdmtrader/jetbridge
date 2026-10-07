package atc

// HangarStatus is the Hangar output plane's operator status: whether it is in
// service, the residue a drain waits on, and the open integrity findings.
//
// A drain is: take the plane out of service (hangarOutput.webEnabled off),
// wait until Drained, then remove the node daemons' output plane.
type HangarStatus struct {
	Enabled bool `json:"enabled"`
	AtRisk  bool `json:"at_risk"`
	Drained bool `json:"drained"`

	Residue         HangarResidue `json:"residue"`
	LiveGenerations int           `json:"live_generations"`

	Findings []HangarFinding `json:"findings"`
}

// HangarResidue counts what still needs the output plane to finish. Total is
// their sum; zero with the plane out of service is drained. Open integrity
// findings are not residue: they block admission, not removal, and are listed
// in HangarStatus.Findings.
type HangarResidue struct {
	PendingCaptures        int `json:"pending_captures"`
	PublishingCaptures     int `json:"publishing_captures"`
	UnreleasedCaptures     int `json:"unreleased_captures"`
	OpenClaims             int `json:"open_claims"`
	LiveReadLeases         int `json:"live_read_leases"`
	UnfinalizedReclaimJobs int `json:"unfinalized_reclaim_jobs"`
	Total                  int `json:"total"`

	// UnacknowledgedReleases are not in Total: captures released because
	// their node was gone or re-registered, whose step marker no node
	// acknowledged clearing.
	UnacknowledgedReleases int `json:"unacknowledged_releases"`
}

// HangarFinding is one open integrity finding. BlocksAdmission is true for the
// runtime classes that refuse new work until an operator resolves them by ID.
type HangarFinding struct {
	ID              int64  `json:"id"`
	Violation       string `json:"violation"`
	Subject         string `json:"subject"`
	Detail          string `json:"detail,omitempty"`
	ObservedAt      int64  `json:"observed_at"`
	BlocksAdmission bool   `json:"blocks_admission"`
}
