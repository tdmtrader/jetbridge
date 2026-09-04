package concourse

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/go-concourse/concourse/internal"
)

func (team *team) CreatePipelineRun(pipelineName string, vars map[string]any) (atc.PipelineRun, error) {
	var run atc.PipelineRun

	body, err := json.Marshal(atc.CreatePipelineRunRequest{Vars: vars})
	if err != nil {
		return run, fmt.Errorf("marshalling pipeline run variables: %w", err)
	}

	request, err := http.NewRequest(http.MethodPost, team.pipelineRunsURL(pipelineName), bytes.NewReader(body))
	if err != nil {
		return run, err
	}
	request.Header.Set("Content-Type", "application/json")

	err = team.connection.SendHTTPRequest(request, false, &internal.Response{Result: &run})

	return run, pipelineRunError(err)
}

func (team *team) PipelineRuns(pipelineName string, page Page) ([]atc.PipelineRun, Pagination, error) {
	var runs []atc.PipelineRun
	headers := http.Header{}

	request, err := http.NewRequest(http.MethodGet, team.pipelineRunsURL(pipelineName), nil)
	if err != nil {
		return nil, Pagination{}, err
	}
	request.URL.RawQuery = page.QueryParams().Encode()

	err = team.connection.SendHTTPRequest(request, false, &internal.Response{
		Result:  &runs,
		Headers: &headers,
	})
	if err != nil {
		return nil, Pagination{}, pipelineRunError(err)
	}

	pagination, err := paginationFromHeaders(headers)
	if err != nil {
		return nil, Pagination{}, err
	}

	return runs, pagination, nil
}

func (team *team) PipelineRun(pipelineName string, number int) (atc.PipelineRun, bool, error) {
	var run atc.PipelineRun
	request, err := http.NewRequest(http.MethodGet, team.pipelineRunURL(pipelineName, number), nil)
	if err != nil {
		return run, false, err
	}

	err = team.connection.SendHTTPRequest(request, false, &internal.Response{Result: &run})

	switch err.(type) {
	case nil:
		return run, true, nil
	case internal.ResourceNotFoundError:
		return run, false, nil
	default:
		return run, false, pipelineRunError(err)
	}
}

// pipelineRunError unwraps the run API's JSON error envelope, so a refusal the
// server took care to phrase reaches the user as its own message rather than as
// a raw body inside "Unexpected Response". The sibling config write path does
// the same (see CreateOrUpdatePipelineConfig); the connection this path still
// goes through has already consumed the response, so the envelope is
// recognised by decoding it rather than by the Content-Type it arrived with.
func pipelineRunError(err error) error {
	var unexpected internal.UnexpectedResponseError
	if !errors.As(err, &unexpected) {
		return err
	}
	if unexpected.StatusCode != http.StatusBadRequest && unexpected.StatusCode != http.StatusConflict {
		return err
	}

	var envelope atc.SaveConfigResponse
	if json.Unmarshal([]byte(unexpected.Body), &envelope) != nil || len(envelope.Errors) == 0 {
		return err
	}
	return InvalidPipelineRunError{Errors: envelope.Errors}
}

func (team *team) pipelineRunsURL(pipelineName string) string {
	return fmt.Sprintf("%s/api/v1/teams/%s/pipelines/%s/runs", team.connection.URL(), url.PathEscape(team.Name()), url.PathEscape(pipelineName))
}

func (team *team) pipelineRunURL(pipelineName string, number int) string {
	return team.pipelineRunsURL(pipelineName) + "/" + strconv.Itoa(number)
}
