package jetbridge

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	hangaroutput "github.com/concourse/concourse/hangar/output"
)

// postHold is the capture control init's one request, made from the spec
// because no container runs here. It is written out rather than reused from
// OutputControlClient on purpose: the init container is NOT the ATC, it
// presents its own one-shot warrant on the node-local route, and a fixture that
// used the ATC's client would be asserting the ATC twice. The body is spelled
// as the generated script spells it: the execution, the selected output, and
// the Pod UID the container reads off the Downward API.
func postHold(endpoint, warrant string, hold hangaroutput.CaptureHoldRequest) (hangaroutput.CaptureHoldAcknowledgement, error) {
	var acknowledged hangaroutput.CaptureHoldAcknowledgement
	body, err := json.Marshal(hold)
	if err != nil {
		return acknowledged, err
	}
	request, err := http.NewRequest(http.MethodPost, endpoint+"/capture/v1/hold", bytes.NewReader(body))
	if err != nil {
		return acknowledged, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(CapabilityHeaderName, warrant)

	// What the control init does: it dials its own node by IP, which no
	// certificate names, so it does not verify the server and presents no
	// client certificate -- the hold is the node-local route.
	node := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		MinVersion: tls.VersionTLS12, InsecureSkipVerify: true}}}
	response, err := node.Do(request)
	if err != nil {
		return acknowledged, err
	}
	defer response.Body.Close()

	answer, _ := io.ReadAll(io.LimitReader(response.Body, 1<<16))
	if response.StatusCode != http.StatusOK {
		return acknowledged, fmt.Errorf("the hold answered %d: %s", response.StatusCode, answer)
	}
	if err := json.Unmarshal(answer, &acknowledged); err != nil {
		return acknowledged, fmt.Errorf("decoding the hold's answer %s: %w", answer, err)
	}

	return acknowledged, acknowledged.Validate()
}
