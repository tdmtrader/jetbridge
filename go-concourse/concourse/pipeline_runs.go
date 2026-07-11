package concourse

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/go-concourse/concourse/internal"
	"github.com/tedsuo/rata"
)

func (team *team) CreatePipelineRun(pipelineRef atc.PipelineRef, params map[string]any) (atc.PipelineRun, error) {
	payload, err := json.Marshal(atc.CreatePipelineRunRequest{Params: params})
	if err != nil {
		return atc.PipelineRun{}, err
	}

	var run atc.PipelineRun
	err = team.connection.Send(internal.Request{
		RequestName: atc.CreatePipelineRun,
		Params: rata.Params{
			"team_name":     team.Name(),
			"pipeline_name": pipelineRef.Name,
		},
		Body:   bytes.NewBuffer(payload),
		Header: http.Header{"Content-Type": []string{"application/json"}},
	}, &internal.Response{
		Result: &run,
	})
	return run, err
}

func (team *team) ListPipelineRuns(pipelineRef atc.PipelineRef) ([]atc.PipelineRun, error) {
	var runs []atc.PipelineRun
	err := team.connection.Send(internal.Request{
		RequestName: atc.ListPipelineRuns,
		Params: rata.Params{
			"team_name":     team.Name(),
			"pipeline_name": pipelineRef.Name,
		},
	}, &internal.Response{
		Result: &runs,
	})
	return runs, err
}

func (team *team) GetPipelineRun(pipelineRef atc.PipelineRef, number int) (atc.PipelineRun, bool, error) {
	var run atc.PipelineRun
	err := team.connection.Send(internal.Request{
		RequestName: atc.GetPipelineRun,
		Params: rata.Params{
			"team_name":     team.Name(),
			"pipeline_name": pipelineRef.Name,
			"run_number":    strconv.Itoa(number),
		},
	}, &internal.Response{
		Result: &run,
	})

	switch err.(type) {
	case nil:
		return run, true, nil
	case internal.ResourceNotFoundError:
		return atc.PipelineRun{}, false, nil
	default:
		return atc.PipelineRun{}, false, err
	}
}
