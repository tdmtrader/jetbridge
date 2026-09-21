package jetbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// StageInput uploads only to the selected node's authenticated control plane.
// The returned identity must be reserved durably before PublishInput is called.
func (c *OutputControlClient) StageInput(ctx context.Context, node executioncontrol.NodeUID, archive io.Reader) (output.InputStage, error) {
	var stage output.InputStage
	if node == "" || archive == nil {
		return stage, output.ErrIncomplete
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/input/v1/stage", archive)
	if err != nil {
		return stage, err
	}
	r.Header.Set("Content-Type", "application/x-tar")
	client := *c.http
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(r)
	if err != nil {
		return stage, output.ErrInfrastructure
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return stage, &OutputControlRefusal{Operation: "input-stage", Status: response.StatusCode}
	}
	if err := decodeInputResponse(response.Body, &stage); err != nil {
		return stage, err
	}
	if stage.Validate() != nil || stage.NodeUID != node || stage.ActivationEpoch != c.epoch {
		return output.InputStage{}, output.ErrCorrupt
	}
	return stage, nil
}

// PublishInput returns signed publication evidence, not a Run binding or claim.
// Consumer registration must recheck its retained nonce and ownership atomically.
func (c *OutputControlClient) PublishInput(ctx context.Context, stage output.InputStage, nonce string, verifier *output.ReceiptSignatureVerifier) (output.InputPublication, error) {
	var publication output.InputPublication
	request := output.InputPublishRequest{Version: output.InputPublicationVersion, ReservationID: stage.ReservationID, Nonce: nonce}
	if verifier == nil || stage.Validate() != nil || stage.ActivationEpoch != c.epoch || request.Validate() != nil {
		return publication, output.ErrIncomplete
	}
	response, err := c.postRead(ctx, "/input/v1/publish", request)
	if err != nil {
		return publication, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return publication, &OutputControlRefusal{Operation: "input-publish", Status: response.StatusCode}
	}
	if err := decodeInputResponse(response.Body, &publication); err != nil {
		return publication, err
	}
	if err := verifier.VerifyInputPublication(publication, stage, nonce); err != nil {
		return output.InputPublication{}, err
	}
	return publication, nil
}

func decodeInputResponse(body io.Reader, result any) error {
	data, err := io.ReadAll(io.LimitReader(body, (64<<10)+1))
	if err != nil || len(data) > 64<<10 {
		return output.ErrCorrupt
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(result) != nil || decoder.Decode(new(any)) != io.EOF {
		return fmt.Errorf("%w: invalid input publication response", output.ErrCorrupt)
	}
	return nil
}
