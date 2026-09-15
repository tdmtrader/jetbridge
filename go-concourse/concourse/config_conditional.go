package concourse

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/go-concourse/concourse/internal"
	"github.com/tedsuo/rata"
)

var (
	ErrConfigVersionConflict   = errors.New("pipeline config version conflict; read the current version and review before retrying")
	ErrStrictConfigUnsupported = errors.New("strict config write route or target unavailable; no legacy write was attempted")
	ErrConfigWriteRejected     = errors.New("pipeline config write rejected")
	ErrConfigOutcomeUnknown    = errors.New("pipeline config outcome unknown; do not retry automatically")
)

type ConfigWriteReceipt struct {
	Version  string          `json:"version"`
	Created  bool            `json:"created"`
	Warnings []ConfigWarning `json:"warnings"`
}

// SetPipelineConfigConditional opts into the additive atomic write contract.
// It never falls back to SaveConfig, including against old or mixed replicas.
func (team *team) SetPipelineConfigConditional(ref atc.PipelineRef, version string, config []byte, checkCredentials bool) (ConfigWriteReceipt, error) {
	if _, err := atc.ParseConfigVersion(version); err != nil {
		return ConfigWriteReceipt{}, err
	}
	query := ref.QueryParams()
	if query == nil {
		query = url.Values{}
	}
	if checkCredentials {
		query.Set(atc.SaveConfigCheckCreds, "")
	}
	response, err := team.httpAgent.Send(internal.Request{ReturnResponseBody: true, RequestName: atc.SaveConfigConditional,
		Params: rata.Params{"team_name": team.Name(), "pipeline_name": ref.Name}, Query: query, Body: bytes.NewReader(config),
		Header: http.Header{"Content-Type": {"application/x-yaml"}, atc.ConfigVersionHeader: {version}},
	})
	if err != nil {
		return ConfigWriteReceipt{}, fmt.Errorf("%w: %v", ErrConfigOutcomeUnknown, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1024*1024+1))
	if err != nil || len(body) > 1024*1024 {
		return ConfigWriteReceipt{}, ErrConfigOutcomeUnknown
	}
	if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusMethodNotAllowed {
		return ConfigWriteReceipt{}, ErrStrictConfigUnsupported
	}
	var envelope struct {
		Code     string          `json:"code"`
		Errors   []string        `json:"errors"`
		Warnings []ConfigWarning `json:"warnings"`
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		receiptVersion := response.Header.Get(atc.ConfigVersionHeader)
		number, parseErr := atc.ParseConfigVersion(receiptVersion)
		expectedStatus := http.StatusOK
		if version == "0" {
			expectedStatus = http.StatusCreated
		}
		if parseErr != nil || number == 0 || receiptVersion == version || response.StatusCode != expectedStatus || response.Header.Get("Content-Type") != "application/json" || json.Unmarshal(body, &envelope) != nil {
			return ConfigWriteReceipt{}, ErrConfigOutcomeUnknown
		}
		if envelope.Warnings == nil {
			envelope.Warnings = []ConfigWarning{}
		}
		return ConfigWriteReceipt{receiptVersion, response.StatusCode == http.StatusCreated, envelope.Warnings}, nil
	}
	if response.StatusCode >= 500 {
		return ConfigWriteReceipt{}, ErrConfigOutcomeUnknown
	}
	if json.Unmarshal(body, &envelope) == nil && response.StatusCode == http.StatusConflict && envelope.Code == atc.ConfigVersionConflictCode {
		return ConfigWriteReceipt{}, ErrConfigVersionConflict
	}
	return ConfigWriteReceipt{}, fmt.Errorf("%w (HTTP %d): %s", ErrConfigWriteRejected, response.StatusCode, body)
}
