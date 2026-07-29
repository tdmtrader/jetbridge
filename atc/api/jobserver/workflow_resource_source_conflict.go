package jobserver

import (
	"errors"
	"net/http"

	"github.com/concourse/concourse/atc/db"
)

func writeWorkflowResourceSourceConflict(w http.ResponseWriter, err error) bool {
	if !errors.Is(err, db.ErrAgentWorkflowResourceSourceImmutable) {
		return false
	}
	http.Error(w, err.Error(), http.StatusConflict)
	return true
}
