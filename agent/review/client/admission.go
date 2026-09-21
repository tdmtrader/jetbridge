package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/concourse/concourse/atc"
)

// HTTPError classifies a refusal without including response bodies, which may
// contain submitted content. It also lets submission distinguish a missing
// grant from an uncertain network outcome without parsing error messages.
type HTTPError struct{ StatusCode int }

func (err HTTPError) Error() string { return fmt.Sprintf("JetBridge returned HTTP %d", err.StatusCode) }

func (c *Client) UploadInput(ctx context.Context, team, template, name string, archive io.Reader) (atc.RunInputSource, error) {
	var source atc.RunInputSource
	endpoint, err := c.templateEndpoint("v1", team, template)
	if err != nil {
		return source, err
	}
	if name == "" || archive == nil {
		return source, errors.New("input name and archive are required")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/run-inputs/"+url.PathEscape(name), archive)
	if err != nil {
		return source, err
	}
	request.Header.Set("Content-Type", "application/x-tar")
	if err := c.sendAdmission(request, &source, 16<<10, http.StatusCreated); err != nil {
		return source, err
	}
	if source.Validate() != nil || source.SourceID == "" || source.Bearer == "" {
		return atc.RunInputSource{}, errors.New("JetBridge returned an invalid input grant")
	}
	return source, nil
}

// CreateRun submits the caller's persisted invocation key. Callers may replay
// with a source ID alone: only a new admission requires its transient bearer.
func (c *Client) CreateRun(ctx context.Context, team, template string, intent atc.CreatePipelineRunV2Request) (atc.PipelineRun, error) {
	var run atc.PipelineRun
	endpoint, err := c.templateEndpoint("v2", team, template)
	if err != nil {
		return run, err
	}
	if intent.InvocationKey == "" {
		return run, errors.New("a persisted invocation key is required")
	}
	body, err := json.Marshal(intent)
	if err != nil {
		return run, err
	}
	if len(body) > 1<<20 {
		return run, errors.New("Run invocation exceeds 1 MiB")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/runs", bytes.NewReader(body))
	if err != nil {
		return run, err
	}
	request.Header.Set("Content-Type", "application/json")
	if err := c.sendAdmission(request, &run, 1<<20, http.StatusCreated, http.StatusOK); err != nil {
		return run, err
	}
	if run.ID <= 0 || run.Number <= 0 || run.ContractVersion != atc.RunContractV2 || run.ActivationEpoch <= 0 {
		return atc.PipelineRun{}, errors.New("JetBridge returned an invalid versioned Run identity")
	}
	return run, nil
}

func (c *Client) templateEndpoint(version, team, template string) (string, error) {
	if team == "" || template == "" {
		return "", errors.New("team and template are required")
	}
	return c.base + "/api/" + version + "/teams/" + url.PathEscape(team) + "/pipelines/" + url.PathEscape(template), nil
}

func (c *Client) sendAdmission(request *http.Request, result any, limit int64, accepted ...int) error {
	request.Header.Set("Accept", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	ok := false
	for _, status := range accepted {
		ok = ok || response.StatusCode == status
	}
	if !ok {
		return HTTPError{StatusCode: response.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return err
	}
	if int64(len(body)) > limit {
		return errors.New("JetBridge admission response exceeds its size limit")
	}
	if err := json.Unmarshal(body, result); err != nil {
		return errors.New("JetBridge returned an invalid admission response")
	}
	return nil
}
