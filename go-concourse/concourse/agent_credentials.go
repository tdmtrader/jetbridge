package concourse

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/concourse/concourse/agent/credentials"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/go-concourse/concourse/internal"
	"github.com/tedsuo/rata"
)

func (client *client) SetAgentUserCredential(req credentials.PutRequest) error {
	buffer := &bytes.Buffer{}
	if err := json.NewEncoder(buffer).Encode(req); err != nil {
		return err
	}
	return client.connection.Send(internal.Request{
		RequestName: atc.SetAgentUserCredential,
		Body:        buffer,
		Header: http.Header{
			"Content-Type": {"application/json"},
		},
	}, &internal.Response{})
}

func (client *client) AgentUserCredentialStatus() ([]credentials.Credential, error) {
	var creds []credentials.Credential
	err := client.connection.Send(internal.Request{
		RequestName: atc.GetAgentUserCredentialStatus,
	}, &internal.Response{Result: &creds})
	return creds, err
}

func (client *client) DeleteAgentUserCredential(kind string, platform bool) error {
	req := internal.Request{
		RequestName: atc.DeleteAgentUserCredential,
		Params:      rata.Params{"kind": kind},
	}
	if platform {
		req.Query = url.Values{"user": {credentials.PlatformUserName}}
	}
	return client.connection.Send(req, &internal.Response{})
}
