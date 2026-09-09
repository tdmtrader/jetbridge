package jetbridge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	hangaroutput "github.com/concourse/concourse/hangar/output"
)

// postHold is the capture control init container's one request, made from the
// spec because no container runs here. It is written out rather than reused
// from OutputControlClient on purpose: the init container is NOT the ATC, it
// presents its own one-shot grant on a node-local plaintext path, and a fixture
// that used the ATC's client would be asserting the ATC twice.
// postHold is the capture control init's request, spelled as it is spelled in
// the generated script: the admission, plus the incarnation the control plane
// reserved and put in this container's environment.
func postHold(endpoint, grant string, admission hangaroutput.CaptureAdmission,
	incarnation hangaroutput.SourceIncarnation) error {
	body, err := json.Marshal(struct {
		hangaroutput.CaptureAdmission
		Incarnation hangaroutput.SourceIncarnation `json:"incarnation"`
	}{CaptureAdmission: admission, Incarnation: incarnation})
	if err != nil {
		return err
	}
	request, err := http.NewRequest(http.MethodPost, endpoint+"/capture/v1/hold", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(CapabilityHeaderName, grant)

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	answer, _ := io.ReadAll(io.LimitReader(response.Body, 1<<16))
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("the hold answered %d: %s", response.StatusCode, answer)
	}

	return nil
}
