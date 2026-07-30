// Package output owns the fail-closed boundary between harness-native text and
// the broker's fixed typed result contracts.
package output

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/concourse/concourse/agent/snapshot/contracts"
)

func Decode(tool string, raw []byte, subjects []contracts.Subject, maxBytes int) (any, error) {
	if maxBytes <= 0 {
		return nil, fmt.Errorf("broker output: positive byte limit is required")
	}
	if len(raw) > maxBytes {
		return nil, fmt.Errorf("broker output: output exceeds byte limit %d", maxBytes)
	}
	if err := rejectDuplicateKeysAndTrailing(raw); err != nil {
		return nil, fmt.Errorf("broker output: %w", err)
	}
	switch tool {
	case "request_review":
		body, err := decodeStrict[contracts.ReviewBody](raw)
		if err != nil {
			return nil, err
		}
		if err := body.Validate(subjects); err != nil {
			return nil, fmt.Errorf("broker output: invalid review/v1 body: %w", err)
		}
		return body, nil
	case "consult_agent":
		body, err := decodeStrict[contracts.ConsultationBody](raw)
		if err != nil {
			return nil, err
		}
		if err := body.Validate(subjects); err != nil {
			return nil, fmt.Errorf("broker output: invalid consultation/v1 body: %w", err)
		}
		return body, nil
	default:
		return nil, fmt.Errorf("broker output: unsupported tool %q", tool)
	}
}

func decodeStrict[T any](raw []byte) (T, error) {
	var body T
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		return body, fmt.Errorf("broker output: decode contract JSON: %w", err)
	}
	if err := requireEOF(decoder); err != nil {
		return body, err
	}
	return body, nil
}

func rejectDuplicateKeysAndTrailing(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := walkValue(decoder); err != nil {
		return err
	}
	if err := requireEOF(decoder); err != nil {
		return err
	}
	return nil
}

func walkValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return fmt.Errorf("invalid JSON object: %w", err)
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("invalid JSON object key")
			}
			if _, found := seen[key]; found {
				return fmt.Errorf("duplicate JSON object key %q", key)
			}
			seen[key] = struct{}{}
			if err := walkValue(decoder); err != nil {
				return err
			}
		}
		if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
			return fmt.Errorf("invalid JSON object")
		}
	case '[':
		for decoder.More() {
			if err := walkValue(decoder); err != nil {
				return err
			}
		}
		if token, err := decoder.Token(); err != nil || token != json.Delim(']') {
			return fmt.Errorf("invalid JSON array")
		}
	default:
		return fmt.Errorf("invalid JSON delimiter %q", delimiter)
	}
	return nil
}

func requireEOF(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	switch {
	case errors.Is(err, io.EOF):
		return nil
	case err == nil:
		return fmt.Errorf("broker output: expected exactly one JSON value")
	default:
		return fmt.Errorf("broker output: invalid trailing JSON: %w", err)
	}
}
