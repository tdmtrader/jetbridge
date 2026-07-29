package pipelineserver

import (
	"errors"
	"net/http"

	"github.com/concourse/concourse/atc/db"
)

func writeWorkflowRunTemplateConflict(w http.ResponseWriter, err error) bool {
	if !errors.Is(err, db.ErrWorkflowRunTemplateImmutable) &&
		!errors.Is(err, db.ErrAgentWorkflowResourceSourceImmutable) {
		return false
	}
	http.Error(w, err.Error(), http.StatusConflict)
	return true
}
