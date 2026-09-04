package concourse

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/go-concourse/concourse/internal"
)

// ErrUnauthorized is returned for 401 response codes.
var ErrUnauthorized = internal.ErrUnauthorized

// ErrForbidden is returned for 403 response codes.
var ErrForbidden = internal.ForbiddenError{}

// GenericError is used when no more specific error is available, i.e. a
// generic 500 Internal Server Error response with a message in the body.
type GenericError struct {
	Message string
}

// Error just returns the message from the response body.
func (err GenericError) Error() string {
	return err.Message
}

// InvalidConfigError is returned when saving a pipeline returns errors (i.e.
// validation failures).
type InvalidConfigError struct {
	Errors []string `json:"errors"`
}

// Error lists the errors returned for the config.
func (c InvalidConfigError) Error() string {
	return fmt.Sprintf("invalid pipeline config:\n%s", strings.Join(c.Errors, "\n"))
}

// InvalidPipelineRunError is returned when the run API refuses a request with
// the reasons it phrased, i.e. a 400 or 409 carrying the atc.SaveConfigResponse
// envelope.
type InvalidPipelineRunError struct {
	Errors []string `json:"errors"`
}

// Error lists the reasons the run was refused, one per line.
func (c InvalidPipelineRunError) Error() string {
	return strings.Join(c.Errors, "\n")
}

// APIRefusalError carries the reasons the server phrased for a request it
// refused with the atc.SaveConfigResponse envelope, so the refusal reaches the
// user as its own sentence.
type APIRefusalError struct {
	Errors []string `json:"errors"`
}

// Error lists the reasons the request was refused, one per line.
func (e APIRefusalError) Error() string {
	return strings.Join(e.Errors, "\n")
}

// refusalEnvelope reports the reasons carried by a 400 or 409 answered with the
// API's atc.SaveConfigResponse envelope. The connection has already consumed
// the response by the time an error surfaces, so the envelope is recognised by
// decoding it rather than by the Content-Type it arrived with; a body that is
// not the envelope is left to the caller's existing handling.
func refusalEnvelope(err error) ([]string, bool) {
	var unexpected internal.UnexpectedResponseError
	if !errors.As(err, &unexpected) {
		return nil, false
	}
	if unexpected.StatusCode != http.StatusBadRequest && unexpected.StatusCode != http.StatusConflict {
		return nil, false
	}

	var envelope atc.SaveConfigResponse
	if json.Unmarshal([]byte(unexpected.Body), &envelope) != nil || len(envelope.Errors) == 0 {
		return nil, false
	}
	return envelope.Errors, true
}
