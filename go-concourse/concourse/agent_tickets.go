package concourse

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/concourse/concourse/agent/api/outcomes"
	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/gitcheck"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/go-concourse/concourse/internal"
	"github.com/tedsuo/rata"
)

func (client *client) ListAgentTickets(filter tickets.ListFilter) ([]tickets.Ticket, error) {
	query := url.Values{}
	if filter.State != "" {
		query.Set("state", string(filter.State))
	}
	if filter.Repo != "" {
		query.Set("repo", filter.Repo)
	}
	if filter.Origin != "" {
		query.Set("origin", filter.Origin)
	}
	if filter.Limit > 0 {
		query.Set("limit", strconv.Itoa(filter.Limit))
	}

	var result []tickets.Ticket
	err := client.connection.Send(internal.Request{
		RequestName: atc.ListAgentTickets,
		Query:       query,
	}, &internal.Response{
		Result: &result,
	})
	return result, err
}

func (client *client) CreateAgentTicket(req tickets.CreateRequest) (tickets.Ticket, error) {
	buffer := &bytes.Buffer{}
	if err := json.NewEncoder(buffer).Encode(req); err != nil {
		return tickets.Ticket{}, err
	}

	var created tickets.Ticket
	err := client.connection.Send(internal.Request{
		RequestName: atc.CreateAgentTicket,
		Body:        buffer,
		Header:      http.Header{"Content-Type": []string{"application/json"}},
	}, &internal.Response{
		Result: &created,
	})
	return created, err
}

func (client *client) TransitionAgentTicket(id int, req tickets.TransitionRequest) (tickets.Ticket, error) {
	buffer := &bytes.Buffer{}
	if err := json.NewEncoder(buffer).Encode(req); err != nil {
		return tickets.Ticket{}, err
	}

	var updated tickets.Ticket
	err := client.connection.Send(internal.Request{
		RequestName: atc.TransitionAgentTicket,
		Params:      rata.Params{"ticket_id": strconv.Itoa(id)},
		Body:        buffer,
		Header:      http.Header{"Content-Type": []string{"application/json"}},
	}, &internal.Response{
		Result: &updated,
	})
	return updated, err
}

func (client *client) UpdateAgentTicket(id int, req tickets.UpdateRequest) (tickets.Ticket, error) {
	buffer := &bytes.Buffer{}
	if err := json.NewEncoder(buffer).Encode(req); err != nil {
		return tickets.Ticket{}, err
	}

	var updated tickets.Ticket
	err := client.connection.Send(internal.Request{
		RequestName: atc.UpdateAgentTicket,
		Params:      rata.Params{"ticket_id": strconv.Itoa(id)},
		Body:        buffer,
		Header:      http.Header{"Content-Type": []string{"application/json"}},
	}, &internal.Response{
		Result: &updated,
	})
	return updated, err
}

func (client *client) SetAgentTicketDisposition(id int, req outcomes.DispositionRequest) (outcomes.Outcome, error) {
	buffer := &bytes.Buffer{}
	if err := json.NewEncoder(buffer).Encode(req); err != nil {
		return outcomes.Outcome{}, err
	}

	var outcome outcomes.Outcome
	err := client.connection.Send(internal.Request{
		RequestName: atc.SetAgentTicketDisposition,
		Params:      rata.Params{"ticket_id": strconv.Itoa(id)},
		Body:        buffer,
		Header:      http.Header{"Content-Type": []string{"application/json"}},
	}, &internal.Response{
		Result: &outcome,
	})
	return outcome, err
}

func (client *client) GetAgentTicketOutcome(id int) (outcomes.Outcome, bool, error) {
	var outcome outcomes.Outcome
	err := client.connection.Send(internal.Request{
		RequestName: atc.GetAgentTicketOutcome,
		Params:      rata.Params{"ticket_id": strconv.Itoa(id)},
	}, &internal.Response{
		Result: &outcome,
	})
	switch err.(type) {
	case nil:
		return outcome, true, nil
	case internal.ResourceNotFoundError:
		return outcome, false, nil
	default:
		return outcome, false, err
	}
}

func (client *client) GetAgentTicketDiff(id int, offset, limit int) (gitcheck.DiffPage, bool, error) {
	query := url.Values{}
	query.Set("offset", strconv.Itoa(offset))
	query.Set("limit", strconv.Itoa(limit))

	var page gitcheck.DiffPage
	err := client.connection.Send(internal.Request{
		RequestName: atc.GetAgentTicketDiff,
		Params:      rata.Params{"ticket_id": strconv.Itoa(id)},
		Query:       query,
	}, &internal.Response{
		Result: &page,
	})
	switch err.(type) {
	case nil:
		return page, true, nil
	case internal.ResourceNotFoundError:
		return page, false, nil
	default:
		return page, false, err
	}
}

// AgentRunTranscript fetches the raw ndjson transcript for a ticket's run
// (build) and returns it verbatim. A missing transcript surfaces as the
// connection's ResourceNotFoundError.
func (client *client) AgentRunTranscript(ticketID, buildID int) ([]byte, error) {
	response := internal.Response{}
	err := client.connection.Send(internal.Request{
		RequestName: atc.GetAgentTicketRunTranscript,
		Params: rata.Params{
			"ticket_id": strconv.Itoa(ticketID),
			"build_id":  strconv.Itoa(buildID),
		},
		ReturnResponseBody: true,
	}, &response)
	if err != nil {
		return nil, err
	}

	body := response.Result.(io.ReadCloser)
	defer body.Close()
	return io.ReadAll(body)
}

func (client *client) DispatchAgentTicket(id int) (tickets.DispatchResponse, error) {
	var res tickets.DispatchResponse
	err := client.connection.Send(internal.Request{
		RequestName: atc.DispatchAgentTicket,
		Params:      rata.Params{"ticket_id": strconv.Itoa(id)},
	}, &internal.Response{
		Result: &res,
	})
	return res, err
}

func (client *client) GetAgentTicket(id int) (tickets.TicketDetail, bool, error) {
	var detail tickets.TicketDetail
	err := client.connection.Send(internal.Request{
		RequestName: atc.GetAgentTicket,
		Params:      rata.Params{"ticket_id": strconv.Itoa(id)},
	}, &internal.Response{
		Result: &detail,
	})
	switch err.(type) {
	case nil:
		return detail, true, nil
	case internal.ResourceNotFoundError:
		return detail, false, nil
	default:
		return detail, false, err
	}
}
