// Package errormap classifies typed domain errors for API mutation handlers.
package errormap

import (
	"errors"
	"net/http"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/helpers"
	"github.com/concourse/concourse/atc/db"
)

// Status reports the HTTP status for a known, user-actionable domain error.
func Status(err error) (int, bool) {
	var invalidParams atc.InvalidRunParamsError
	if errors.As(err, &invalidParams) {
		return http.StatusBadRequest, true
	}
	var cacheConflict db.TaskCacheIdentityConflictError
	if errors.As(err, &cacheConflict) || errors.Is(err, db.ErrPipelineRunNotTemplate) ||
		errors.Is(err, db.ErrPipelineRunInstanced) || errors.Is(err, db.ErrPipelineRunPaused) ||
		errors.Is(err, db.ErrPipelineRunArchived) || errors.Is(err, db.ErrPipelineRunPayloadMutation) ||
		errors.Is(err, db.ErrPipelineTemplateHasRuns) || errors.Is(err, db.ErrPipelineTemplateHasRunHistory) ||
		errors.Is(err, db.ErrPipelineTemplateHasOrdinaryJobState) ||
		errors.Is(err, db.ErrPipelineRunNotRunning) || errors.Is(err, db.ErrPipelineRunPayloadGone) ||
		errors.Is(err, db.ErrPipelineRunOneOffBuild) || errors.Is(err, db.ErrPipelineTemplateBuild) {
		return http.StatusConflict, true
	}
	return 0, false
}

// Write emits a classified domain error and reports whether it handled err.
//
// The body is the same envelope the rest of the API already answers errors
// with, atc.SaveConfigResponse (see helpers.HandleBadRequest). http.Error
// would stamp text/plain on handlers whose every other response is JSON, so a
// client switching on Content-Type saw plain text on every refusal.
func Write(w http.ResponseWriter, err error) bool {
	status, known := Status(err)
	if !known {
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	helpers.WriteSaveConfigResponse(w, atc.SaveConfigResponse{Errors: []string{err.Error()}})
	return true
}
