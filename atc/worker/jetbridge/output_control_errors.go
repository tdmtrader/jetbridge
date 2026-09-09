package jetbridge

import "errors"

// asRefusal is errors.As with the target already typed, kept separate so
// Refused reads as one line at every call site.
func asRefusal(err error, target **OutputControlRefusal) bool {
	return errors.As(err, target)
}
