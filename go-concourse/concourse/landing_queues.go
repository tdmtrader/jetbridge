package concourse

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/go-concourse/concourse/internal"
	"github.com/tedsuo/rata"
)

// ErrLandingEntryConflict is the server's 409: the entry id is already queued
// or settled for another commit.
var ErrLandingEntryConflict = errors.New("landing entry is already queued or settled for another commit")

// SetLandingQueue sets the named queue from its YAML. It reports whether the
// queue was created rather than replaced.
func (team *team) SetLandingQueue(queueName string, config []byte) (bool, error) {
	params := rata.Params{"team_name": team.Name(), "queue_name": queueName}
	var response internal.Response
	err := team.connection.Send(internal.Request{
		RequestName: atc.SetLandingQueue, Params: params, Body: bytes.NewReader(config),
		Header: http.Header{"Content-Type": {"application/x-yaml"}},
	}, &response)
	if err != nil {
		return false, err
	}
	return response.Created, nil
}

// SubmitLanding queues an entry. It reports whether the entry was created;
// false means the same commit was already queued.
func (team *team) SubmitLanding(queueName string, submission atc.LandingSubmission) (bool, error) {
	body, err := json.Marshal(submission)
	if err != nil {
		return false, err
	}
	params := rata.Params{"team_name": team.Name(), "queue_name": queueName}
	var response internal.Response
	err = team.connection.Send(internal.Request{
		RequestName: atc.SubmitLanding, Params: params, Body: bytes.NewReader(body),
		Header: http.Header{"Content-Type": {"application/json"}},
	}, &response)
	var unexpected internal.UnexpectedResponseError
	if errors.As(err, &unexpected) && unexpected.StatusCode == http.StatusConflict {
		return false, fmt.Errorf("%w: %s", ErrLandingEntryConflict, unexpected.Body)
	}
	if err != nil {
		return false, err
	}
	return response.Created, nil
}

// LandingQueue reads the queue's status. False means no such queue.
func (team *team) LandingQueue(queueName string) (atc.LandingQueueStatus, bool, error) {
	var status atc.LandingQueueStatus
	params := rata.Params{"team_name": team.Name(), "queue_name": queueName}
	err := team.connection.Send(internal.Request{RequestName: atc.GetLandingQueue, Params: params}, &internal.Response{Result: &status})
	switch err.(type) {
	case nil:
		return status, true, nil
	case internal.ResourceNotFoundError:
		return status, false, nil
	default:
		return status, false, err
	}
}
