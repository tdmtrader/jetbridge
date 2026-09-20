package artifactwire

import (
	"errors"
	"fmt"
	"net/http"
)

// Sentinels a caller branches on with errors.Is. A Refusal matches every
// sentinel its status implies, so a 404 is both ErrNotFound and ErrRefused.
var (
	// ErrRefused is any 4xx: the daemon's considered answer about this
	// request, which no retry turns into a different one.
	ErrRefused = errors.New("the artifact daemon refused the request")
	// ErrNotFound is a 404: the key names nothing on this node.
	ErrNotFound = errors.New("the artifact daemon holds no such key")
	// ErrHeld is a 409: a durable output capture still holds the source, and
	// the daemon will not destroy or re-alias it.
	ErrHeld = errors.New("a capture holds the source")
	// ErrUnavailable is a 5xx: the daemon could not answer, and a retry may.
	ErrUnavailable = errors.New("the artifact daemon could not answer")
)

// Refusal is a daemon answer other than the one the operation wanted. The
// status is carried rather than flattened into "failed" because callers act on
// it: a reaper keeps its locator entry on a 409, a warm stops on a 404.
type Refusal struct {
	Method string
	URL    string
	Status int
	// Body is the daemon's own account, bounded, for a log line.
	Body string
}

func (r *Refusal) Error() string {
	if r.Body == "" {
		return fmt.Sprintf("%s %s: status %d", r.Method, r.URL, r.Status)
	}
	return fmt.Sprintf("%s %s: status %d: %s", r.Method, r.URL, r.Status, r.Body)
}

// Is classifies the status. It is the only place a status becomes a meaning.
func (r *Refusal) Is(target error) bool {
	switch target {
	case ErrRefused:
		return r.Status >= 400 && r.Status < 500
	case ErrNotFound:
		return r.Status == http.StatusNotFound
	case ErrHeld:
		return r.Status == http.StatusConflict
	case ErrUnavailable:
		return r.Status >= 500
	}
	return false
}
