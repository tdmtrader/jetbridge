package jetbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/output"
)

// ManagedReadTimeout is the operation budget used to admit the lease that
// protects this node read. It is configured with the daemon at deployment.
func (client *OutputControlClient) ManagedReadTimeout() time.Duration {
	return client.readTimeout
}

// StatExactObject implements managed-read admission's metadata port over the
// existing node transport. Bucket access stays on the output daemon; no
// producer Pod or source directory must survive for a retained result to read.
func (client *OutputControlClient) StatExactObject(ctx context.Context, ref hangar.TreeRef) (output.PublishedObject, error) {
	var object output.PublishedObject
	if err := ref.Validate(); err != nil {
		return object, err
	}
	response, err := client.postRead(ctx, "/read/v1/stat", struct {
		Ref hangar.TreeRef `json:"ref"`
	}{ref})
	if err != nil {
		return object, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return object, &OutputControlRefusal{Operation: "read-stat", Status: response.StatusCode}
	}
	const maximum = 64 << 10
	data, err := io.ReadAll(io.LimitReader(response.Body, maximum+1))
	if err != nil || len(data) > maximum || json.Unmarshal(data, &object) != nil {
		return output.PublishedObject{}, fmt.Errorf("%w: invalid remote stat response", output.ErrCorrupt)
	}
	if err := object.Validate(); err != nil || object.Attributes.Ref != ref {
		return output.PublishedObject{}, fmt.Errorf("%w: remote stat changed the exact identity", output.ErrCorrupt)
	}
	return object, nil
}

// OpenManagedOutput returns the daemon's verified canonical archive. The node
// has staged it under a live read lease before sending headers. The caller must
// still verify the archive digest before publishing it to an external client.
func (client *OutputControlClient) OpenManagedOutput(ctx context.Context, input output.ManagedReadRequest, maxBytes int64) (io.ReadCloser, hangar.TreeAttributes, error) {
	var attributes hangar.TreeAttributes
	if err := input.Validate(); err != nil {
		return nil, attributes, err
	}
	if maxBytes <= 0 {
		return nil, attributes, output.ErrIncomplete
	}
	response, err := client.postRead(ctx, "/read/v1/archive", input)
	if err != nil {
		return nil, attributes, err
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, attributes, &OutputControlRefusal{Operation: "read-archive", Status: response.StatusCode}
	}
	data := response.Header.Get(output.ReadAttributesHeader)
	var wire output.TreeAttributes
	if len(data) > 64<<10 || json.Unmarshal([]byte(data), &wire) != nil || wire.Validate() != nil || wire.Ref != input.Ref || response.ContentLength < 0 || wire.LogicalBytes != response.ContentLength {
		response.Body.Close()
		return nil, hangar.TreeAttributes{}, fmt.Errorf("%w: invalid archive metadata", output.ErrCorrupt)
	}
	if response.ContentLength > maxBytes {
		response.Body.Close()
		return nil, hangar.TreeAttributes{}, output.ErrLimitExceeded
	}
	return response.Body, wire.Foundation(), nil
}

func (client *OutputControlClient) postRead(ctx context.Context, path string, input any) (*http.Response, error) {
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.endpoint+path, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	transport := *client.http
	transport.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := transport.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%w: remote output read", output.ErrInfrastructure)
	}
	return response, nil
}

// MaterializeManagedOutput installs the exact leased tree in the selected node's
// managed steps directory. It completes before the task may mount that input.
func (client *OutputControlClient) MaterializeManagedOutput(ctx context.Context, input output.ManagedReadRequest) error {
	if err := input.Validate(); err != nil {
		return err
	}
	response, err := client.postRead(ctx, "/read/v1/materialize", input)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return &OutputControlRefusal{Operation: "read-materialize", Status: response.StatusCode}
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 1))
	if err != nil || len(data) != 0 {
		return fmt.Errorf("%w: invalid materialization response", output.ErrCorrupt)
	}
	return nil
}
