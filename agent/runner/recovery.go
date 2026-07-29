package runner

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/concourse/concourse/agent/provider"
)

const (
	recoveryTransportEnv = "CONCOURSE_AGENT_RECOVERY"
	maxRecoverySpecBytes = 4096
	maxRecoveryToolIDs   = 128
	maxRecoveryToolIDLen = 256

	recoveryNotice = "This is a fresh recovery attempt. No live processes, sockets, credentials, mounts, or other ephemeral state were restored. Inspect the restored workspace and reconstruct any required ephemeral state before continuing."
)

type recoveryMode string

const (
	recoveryCheckpointZero recoveryMode = "checkpoint_zero"
	recoveryWorkspaceOnly  recoveryMode = "workspace_only"
	recoveryNativeResume   recoveryMode = "native_resume"
)

type recoverySpec struct {
	Mode                 recoveryMode
	Adapter              provider.Identity
	ExecutionAttempt     int
	CheckpointGeneration int
	TranscriptCursor     int64
	CompletedToolCallIDs []string
	SessionID            string
}

func decodeRecoverySpec(raw string) (recoverySpec, bool, error) {
	if raw == "" {
		return recoverySpec{}, false, nil
	}
	if len(raw) > maxRecoverySpecBytes {
		return recoverySpec{}, false, fmt.Errorf("%s exceeds %d bytes", recoveryTransportEnv, maxRecoverySpecBytes)
	}
	fields, err := strictJSONObject([]byte(raw), map[string]struct{}{
		"mode": {}, "adapter": {}, "attempt": {}, "generation": {}, "cursor": {},
		"completed_tool_ids": {}, "session_id": {},
	})
	if err != nil {
		return recoverySpec{}, false, fmt.Errorf("invalid %s: %w", recoveryTransportEnv, err)
	}
	for _, name := range []string{"mode", "adapter", "attempt", "generation", "cursor", "completed_tool_ids"} {
		if _, found := fields[name]; !found {
			return recoverySpec{}, false, fmt.Errorf("invalid %s: %s is required", recoveryTransportEnv, name)
		}
		if bytes.Equal(bytes.TrimSpace(fields[name]), []byte("null")) {
			return recoverySpec{}, false, fmt.Errorf("invalid %s: %s cannot be null", recoveryTransportEnv, name)
		}
	}
	var spec recoverySpec
	if err := json.Unmarshal(fields["mode"], &spec.Mode); err != nil {
		return recoverySpec{}, false, fmt.Errorf("invalid %s mode: %w", recoveryTransportEnv, err)
	}
	adapter, err := strictJSONObject(fields["adapter"], map[string]struct{}{"name": {}, "version": {}})
	if err != nil {
		return recoverySpec{}, false, fmt.Errorf("invalid %s adapter: %w", recoveryTransportEnv, err)
	}
	if len(adapter) != 2 || adapter["name"] == nil || adapter["version"] == nil {
		return recoverySpec{}, false, fmt.Errorf("invalid %s adapter: name and version are required", recoveryTransportEnv)
	}
	if err := json.Unmarshal(adapter["name"], &spec.Adapter.Name); err != nil {
		return recoverySpec{}, false, fmt.Errorf("invalid %s adapter name: %w", recoveryTransportEnv, err)
	}
	if err := json.Unmarshal(adapter["version"], &spec.Adapter.Version); err != nil {
		return recoverySpec{}, false, fmt.Errorf("invalid %s adapter version: %w", recoveryTransportEnv, err)
	}
	if err := json.Unmarshal(fields["attempt"], &spec.ExecutionAttempt); err != nil {
		return recoverySpec{}, false, fmt.Errorf("invalid %s attempt: %w", recoveryTransportEnv, err)
	}
	if err := json.Unmarshal(fields["generation"], &spec.CheckpointGeneration); err != nil {
		return recoverySpec{}, false, fmt.Errorf("invalid %s generation: %w", recoveryTransportEnv, err)
	}
	if err := json.Unmarshal(fields["cursor"], &spec.TranscriptCursor); err != nil {
		return recoverySpec{}, false, fmt.Errorf("invalid %s cursor: %w", recoveryTransportEnv, err)
	}
	if err := json.Unmarshal(fields["completed_tool_ids"], &spec.CompletedToolCallIDs); err != nil {
		return recoverySpec{}, false, fmt.Errorf("invalid %s completed tool IDs: %w", recoveryTransportEnv, err)
	}
	if rawSessionID, found := fields["session_id"]; found {
		if err := json.Unmarshal(rawSessionID, &spec.SessionID); err != nil {
			return recoverySpec{}, false, fmt.Errorf("invalid %s session ID: %w", recoveryTransportEnv, err)
		}
	}
	if err := spec.validate(); err != nil {
		return recoverySpec{}, false, fmt.Errorf("invalid %s: %w", recoveryTransportEnv, err)
	}
	return spec, true, nil
}

func (spec recoverySpec) validate() error {
	if spec.Mode != recoveryCheckpointZero && spec.Mode != recoveryWorkspaceOnly && spec.Mode != recoveryNativeResume {
		return errors.New("recovery mode is not automatic")
	}
	if err := spec.Adapter.Validate(); err != nil {
		return err
	}
	if spec.ExecutionAttempt <= 0 {
		return errors.New("execution attempt must be positive")
	}
	if spec.CheckpointGeneration < 0 || spec.TranscriptCursor < 0 {
		return errors.New("checkpoint generation and transcript cursor cannot be negative")
	}
	if len(spec.CompletedToolCallIDs) > maxRecoveryToolIDs {
		return errors.New("too many completed tool IDs")
	}
	for i, id := range spec.CompletedToolCallIDs {
		if len(id) == 0 || len(id) > maxRecoveryToolIDLen || strings.TrimSpace(id) != id {
			return errors.New("completed tool ID is invalid")
		}
		if i > 0 && spec.CompletedToolCallIDs[i-1] >= id {
			return errors.New("completed tool IDs must be sorted and unique")
		}
	}
	if spec.Mode == recoveryNativeResume {
		if strings.TrimSpace(spec.SessionID) == "" || strings.TrimSpace(spec.SessionID) != spec.SessionID {
			return errors.New("native resume requires a session ID")
		}
	} else if spec.SessionID != "" {
		return errors.New("only native resume may carry a session ID")
	}
	return nil
}

func strictJSONObject(raw []byte, allowed map[string]struct{}) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return nil, errors.New("must be a JSON object")
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		name, ok := token.(string)
		if !ok {
			return nil, errors.New("object field name is invalid")
		}
		if _, known := allowed[name]; !known {
			return nil, fmt.Errorf("unknown field %q", name)
		}
		if _, duplicate := fields[name]; duplicate {
			return nil, fmt.Errorf("duplicate field %q", name)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		fields[name] = value
	}
	if closing, err := decoder.Token(); err != nil || closing != json.Delim('}') {
		return nil, errors.New("object is not closed")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("trailing JSON value")
		}
		return nil, err
	}
	return fields, nil
}

func appendRecoveryNotice(systemPrompt string) string {
	if systemPrompt == "" {
		return recoveryNotice
	}
	return systemPrompt + "\n\n" + recoveryNotice
}
