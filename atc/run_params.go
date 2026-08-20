package atc

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
)

type RunParams map[string]any

// InvalidRunParamsError identifies a request whose supplied run parameters do
// not satisfy a template's declared schema.
type InvalidRunParamsError struct{ Err error }

func (e InvalidRunParamsError) Error() string { return e.Err.Error() }
func (e InvalidRunParamsError) Unwrap() error { return e.Err }

type ParamType string

const (
	ParamTypeString         ParamType = "string"
	ParamTypeNumber         ParamType = "number"
	ParamTypeBool           ParamType = "bool"
	ParamTypeEnum           ParamType = "enum"
	MaxRunRetentionKeepLast           = 2147483647
	MaxRunRetentionTTLDays            = 1000000
)

type ParamSchema struct {
	Name        string    `json:"name"`
	Type        ParamType `json:"type"`
	Required    bool      `json:"required,omitempty"`
	Default     any       `json:"default,omitempty"`
	Values      []any     `json:"values,omitempty"`
	Description string    `json:"description,omitempty"`
}

type RunRetentionConfig struct {
	KeepLast *int `json:"keep_last,omitempty"`
	TTLDays  *int `json:"ttl_days,omitempty"`
}

func ValidateRunParams(schemas []ParamSchema, params RunParams) (RunParams, error) {
	normalized, err := validateRunParams(schemas, params)
	if err != nil {
		return nil, InvalidRunParamsError{Err: err}
	}
	return normalized, nil
}

func validateRunParams(schemas []ParamSchema, params RunParams) (RunParams, error) {
	schemaByName := make(map[string]ParamSchema, len(schemas))
	for _, schema := range schemas {
		schemaByName[schema.Name] = schema
	}

	for name := range params {
		if name == "run" || name == "run_id" {
			continue
		}
		if _, found := schemaByName[name]; !found {
			return nil, fmt.Errorf("unknown parameter %s", name)
		}
	}

	normalized := RunParams{}
	for _, schema := range schemas {
		value, supplied := params[schema.Name]
		if !supplied {
			value = schema.Default
		}
		if value == nil {
			if schema.Required {
				return nil, fmt.Errorf("parameter %s is required", schema.Name)
			}
			continue
		}

		value, err := normalizeRunParam(schema, value, supplied)
		if err != nil {
			return nil, err
		}
		normalized[schema.Name] = value
	}

	return normalized, nil
}

func normalizeRunParam(schema ParamSchema, value any, supplied bool) (any, error) {
	switch schema.Type {
	case ParamTypeString:
		if stringValue, ok := value.(string); ok {
			return stringValue, nil
		}
		return nil, fmt.Errorf("parameter %s must be a string", schema.Name)
	case ParamTypeNumber:
		if stringValue, ok := value.(string); ok && supplied {
			parsed, err := strconv.ParseFloat(stringValue, 64)
			if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
				return nil, fmt.Errorf("parameter %s must be a number", schema.Name)
			}
			return parsed, nil
		}
		if number, ok := normalizedNumber(value); ok {
			return number, nil
		}
		return nil, fmt.Errorf("parameter %s must be a number", schema.Name)
	case ParamTypeBool:
		if boolValue, ok := value.(bool); ok {
			return boolValue, nil
		}
		if stringValue, ok := value.(string); ok && supplied {
			parsed, err := strconv.ParseBool(stringValue)
			if err == nil {
				return parsed, nil
			}
		}
		return nil, fmt.Errorf("parameter %s must be a bool", schema.Name)
	case ParamTypeEnum:
		return normalizeEnumRunParam(schema, value)
	default:
		return nil, fmt.Errorf("parameter %s has invalid type %q", schema.Name, schema.Type)
	}
}

func normalizeEnumRunParam(schema ParamSchema, value any) (any, error) {
	for _, allowed := range schema.Values {
		if normalized, equal := equalRunParamScalars(value, allowed); equal {
			return normalized, nil
		}
	}

	return nil, fmt.Errorf("parameter %s must be one of the declared enum values", schema.Name)
}

func equalRunParamScalars(value, allowed any) (any, bool) {
	switch allowedValue := allowed.(type) {
	case string:
		valueString, ok := value.(string)
		return valueString, ok && valueString == allowedValue
	case bool:
		valueBool, ok := value.(bool)
		return valueBool, ok && valueBool == allowedValue
	default:
		allowedNumber, allowedOK := normalizedNumber(allowed)
		valueNumber, valueOK := normalizedNumber(value)
		return valueNumber, allowedOK && valueOK && valueNumber == allowedNumber
	}
}

func normalizedNumber(value any) (float64, bool) {
	switch typed := value.(type) {
	case int:
		return float64(typed), true
	case int8:
		return float64(typed), true
	case int16:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case uint:
		return float64(typed), true
	case uint8:
		return float64(typed), true
	case uint16:
		return float64(typed), true
	case uint32:
		return float64(typed), true
	case uint64:
		return float64(typed), true
	case float32:
		return float64(typed), true
	case float64:
		return typed, !math.IsNaN(typed) && !math.IsInf(typed, 0)
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil && !math.IsNaN(parsed) && !math.IsInf(parsed, 0)
	default:
		return 0, false
	}
}
