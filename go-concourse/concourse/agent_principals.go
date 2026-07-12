package concourse

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/go-concourse/concourse/internal"
	"github.com/tedsuo/rata"
)

// ErrAgentPrincipalNotFound is returned by RevokeAgentPrincipal when no
// principal has the given id (server responds 404).
var ErrAgentPrincipalNotFound = errors.New("no such agent principal")

func (client *client) ListAgentPrincipals() ([]atc.AgentPrincipal, error) {
	var principals []atc.AgentPrincipal
	err := client.connection.Send(internal.Request{
		RequestName: atc.ListAgentPrincipals,
	}, &internal.Response{Result: &principals})
	return principals, err
}

func (client *client) CreateAgentPrincipal(spec atc.AgentPrincipalCreateSpec) (atc.AgentPrincipalCreated, error) {
	buffer := &bytes.Buffer{}
	if err := json.NewEncoder(buffer).Encode(spec); err != nil {
		return atc.AgentPrincipalCreated{}, err
	}

	var created atc.AgentPrincipalCreated
	err := client.connection.Send(internal.Request{
		RequestName: atc.CreateAgentPrincipal,
		Body:        buffer,
		Header: http.Header{
			"Content-Type": {"application/json"},
		},
	}, &internal.Response{Result: &created})
	return created, err
}

func (client *client) RevokeAgentPrincipal(id int) error {
	err := client.connection.Send(internal.Request{
		RequestName: atc.RevokeAgentPrincipal,
		Params:      rata.Params{"principal_id": strconv.Itoa(id)},
	}, &internal.Response{})
	if _, ok := err.(internal.ResourceNotFoundError); ok {
		return ErrAgentPrincipalNotFound
	}
	return err
}
