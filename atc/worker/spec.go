package worker

// Spec identifies which worker should run a step. In K8s-only deployments
// (JetBridge), the only meaningful field is TeamID — platform, tags, and
// resource type filtering have been removed since there is one worker per
// namespace and it supports all types.
type Spec struct {
	TeamID int
}
