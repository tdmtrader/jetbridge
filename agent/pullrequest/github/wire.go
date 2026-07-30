package github

import "github.com/concourse/concourse/agent/pullrequest"

var _ pullrequest.Observer = (*Observer)(nil)
