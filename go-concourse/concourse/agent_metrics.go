package concourse

import (
	"net/url"
	"strconv"

	agentschema "github.com/concourse/concourse/agent/schema"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/go-concourse/concourse/internal"
)

// AgentRunMetrics returns the most-recent agent run metrics across all
// workflow runs/builds (newest first). A non-positive limit uses the server default.
func (client *client) AgentRunMetrics(limit int) ([]agentschema.RunMetrics, error) {
	query := url.Values{}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	var resp []agentschema.RunMetrics
	err := client.connection.Send(internal.Request{
		RequestName: atc.ListRecentAgentRunMetrics,
		Query:       query,
	}, &internal.Response{Result: &resp})
	return resp, err
}
