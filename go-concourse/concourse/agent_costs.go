package concourse

import (
	"net/url"

	"github.com/concourse/concourse/agent/api/costs"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/go-concourse/concourse/internal"
)

func (client *client) AgentCostRollup(groupBy, since, until string) (costs.RollupResponse, error) {
	query := url.Values{}
	if groupBy != "" {
		query.Set("group_by", groupBy)
	}
	if since != "" {
		query.Set("since", since)
	}
	if until != "" {
		query.Set("until", until)
	}
	var resp costs.RollupResponse
	err := client.connection.Send(internal.Request{
		RequestName: atc.GetAgentCostRollup,
		Query:       query,
	}, &internal.Response{Result: &resp})
	return resp, err
}
