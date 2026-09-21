package steps

// The client half of the output daemon's control API.
//
// The fixture plays two production roles here and it is worth naming which.
// The ATC admits an execution and mints a capability per operation; the
// supervisor writes the start and outcome records. In Phase 4 execProcess takes
// both parts over. Neither role is a double of the daemon -- every call below
// goes over HTTP to the real binary, and every answer is decoded with the
// production types.
//
// NOTHING HERE COUNTS A REQUEST. There is no call log on this client and no
// counter a scenario could assert on. The scenarios that mean "the daemon was
// not asked to delete it" say instead that the source is still held.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync/atomic"

	"github.com/concourse/concourse/hangar/executioncontrol"
	hangaroutput "github.com/concourse/concourse/hangar/output"
)

// nonces make every minted capability distinct. A capability authorizes one
// operation, so a fixture that reused one would be testing the replay refusal
// by accident on every second call.
var controlNonces atomic.Uint64

// controlAnswer is one call's raw result. Err is a value rather than a fatal so
// a refusal is assertable, which is what every "the refusal says" line reads.
type controlAnswer struct {
	Status int
	Body   []byte
	Err    error
}

// says reports whether the daemon's refusal names a fragment. It reads the
// BODY, because a status alone cannot tell a collision from an unauthorized
// scope from a source that is sealed, and all three are 409.
func (answer controlAnswer) says(fragment string) bool {
	return bytes.Contains(bytes.ToLower(answer.Body), bytes.ToLower([]byte(fragment)))
}

func (answer controlAnswer) describe() string {
	if answer.Err != nil {
		return "no answer at all: " + answer.Err.Error()
	}

	return strconv.Itoa(answer.Status) + " " + abbrev(string(answer.Body))
}

// control calls one route with a capability minted for that route's own facet
// and operation.
//
// The facet is a PARAMETER because one scenario needs to present the wrong one:
// "a base control capability cannot hold, seal or publish" is exactly a valid
// token offered at a route it does not belong to, and a helper that always
// derived the facet from the path could not express it.
func (s HangarDaemon) control(facet executioncontrol.Facet, operation, path string,
	identity executioncontrol.Identity, body any) controlAnswer {
	token, err := s.Minter.Mint(executioncontrol.CapabilityClaims{
		Facet:           facet,
		Operation:       operation,
		Identity:        identity,
		ActivationEpoch: executioncontrol.ActivationEpoch(hangarEpoch),
	}, fmt.Sprintf("brine-%s-%d", operation, controlNonces.Add(1)))
	if err != nil {
		return controlAnswer{Err: err}
	}

	encoded, err := json.Marshal(body)
	if err != nil {
		return controlAnswer{Err: err}
	}
	request, err := http.NewRequest(http.MethodPost, s.Output.URL+path, bytes.NewReader(encoded))
	if err != nil {
		return controlAnswer{Err: err}
	}
	request.Header.Set("Hangar-Control-Capability", string(token))

	response, err := s.HTTP.Do(request)
	if err != nil {
		return controlAnswer{Err: err}
	}
	defer response.Body.Close()
	answer, err := io.ReadAll(response.Body)

	return controlAnswer{Status: response.StatusCode, Body: answer, Err: err}
}

// capture is control with the extension's facet, which is what every route
// under /capture/v1 requires.
func (s HangarDaemon) capture(operation, path string, identity executioncontrol.Identity,
	body any) controlAnswer {
	return s.control(hangaroutput.CaptureFacet, operation, path, identity, body)
}

// base is control with the base protocol's facet.
func (s HangarDaemon) base(operation, path string, identity executioncontrol.Identity,
	body any) controlAnswer {
	return s.control(executioncontrol.BaseFacet, operation, path, identity, body)
}

// rawCapture posts a body the production types cannot express.
//
// It exists for exactly one scenario -- a hold request that names a path -- and
// it takes a map rather than a struct because the whole point is a field no Go
// type here declares. A hostile client really can send one.
func (s HangarDaemon) rawCapture(operation, path string, identity executioncontrol.Identity,
	body map[string]any) controlAnswer {
	return s.capture(operation, path, identity, body)
}

func decodeControl[T any](answer controlAnswer) (T, error) {
	var value T
	if answer.Err != nil {
		return value, answer.Err
	}
	if answer.Status != http.StatusOK {
		return value, fmt.Errorf("the daemon answered %s", answer.describe())
	}
	if err := json.Unmarshal(answer.Body, &value); err != nil {
		return value, fmt.Errorf("decoding the daemon's answer %s: %w", abbrev(string(answer.Body)), err)
	}

	return value, nil
}
