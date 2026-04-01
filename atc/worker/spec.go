package worker

// Spec identifies which worker should run a step. TeamID controls
// team-scoped vs global worker selection. Platform, when non-empty,
// restricts selection to workers matching that OS (e.g. "linux", "darwin").
// An empty Platform matches any worker (preserving K8s-only behavior).
type Spec struct {
	TeamID   int
	Platform string
}
