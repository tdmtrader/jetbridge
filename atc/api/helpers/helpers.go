package helpers

import (
	"encoding/json"
	"fmt"
	"net/http"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc"
)

func HandleBadRequest(w http.ResponseWriter, errorMessages ...string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	WriteErrorResponse(w, atc.ErrorResponse{
		Errors: errorMessages,
	})
}

func WriteErrorResponse(w http.ResponseWriter, saveConfigResponse atc.ErrorResponse) {
	responseJSON, err := json.Marshal(saveConfigResponse)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "failed to generate error response: %s", err)
		return
	}

	_, err = w.Write(responseJSON)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
}

func WriteJSONResponse(logger lager.Logger, w http.ResponseWriter, obj any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	responseJSON, err := json.Marshal(obj)
	if err != nil {
		logger.Error("failed-to-marshal-response", err)
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "failed to generate error response: %s", err)
		return
	}

	_, err = w.Write(responseJSON)
	if err != nil {
		logger.Error("failed-to-write-response", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
}
