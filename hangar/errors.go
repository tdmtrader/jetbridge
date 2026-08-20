package hangar

import "errors"

var (
	ErrNotFound       = errors.New("hangar: object not found")
	ErrConflict       = errors.New("hangar: immutable object conflict")
	ErrCorrupt        = errors.New("hangar: object corrupt")
	ErrUnauthorized   = errors.New("hangar: unauthorized")
	ErrLimitExceeded  = errors.New("hangar: limit exceeded")
	ErrInfrastructure = errors.New("hangar: infrastructure failure")
)
