package atc

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
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

// MaxSafeParamNumber bounds the magnitude of a number parameter to the
// largest integer exactly representable in binary64, so that a value survives
// the round trip through JSON, the database, and interpolation unchanged.
const MaxSafeParamNumber = float64(9007199254740991)

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

	var unknown []string
	for name := range params {
		if name == "run" || name == "run_id" {
			continue
		}
		if _, found := schemaByName[name]; !found {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		// Sort before reporting: Go randomizes map iteration order, so an
		// unsorted report gives a different message for the same request.
		sort.Strings(unknown)
		return nil, fmt.Errorf("unknown parameter %s", strings.Join(unknown, ", "))
	}

	normalized := RunParams{}
	for _, schema := range schemas {
		value, supplied := params[schema.Name]
		if value == nil {
			// An explicit null is not a value: it must neither satisfy a
			// required parameter nor shadow a declared default.
			supplied = false
		}
		if !supplied {
			if schema.Required {
				// Required wins unconditionally. Assigning the default first
				// and only then testing for nil silently downgraded every
				// `required: true` parameter that also declared a default.
				return nil, fmt.Errorf("parameter %s is required", schema.Name)
			}
			value = schema.Default
			if value == nil {
				continue
			}
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
			if err != nil {
				return nil, fmt.Errorf("parameter %s must be a number", schema.Name)
			}
			value = parsed
		}
		if _, isNumber := paramNumberValue(value); !isNumber {
			return nil, fmt.Errorf("parameter %s must be a number", schema.Name)
		}
		number, ok := NormalizeParamNumber(value)
		if !ok {
			return nil, fmt.Errorf("parameter %s must be a finite number no larger than %.0f in magnitude", schema.Name, MaxSafeParamNumber)
		}
		return number, nil
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
		allowedNumber, allowedOK := NormalizeParamNumber(allowed)
		valueNumber, valueOK := NormalizeParamNumber(value)
		return valueNumber, allowedOK && valueOK && valueNumber == allowedNumber
	}
}

// NormalizeParamNumber coerces a JSON scalar to the float64 domain used for
// number parameters. It reports false for non-numbers, for NaN and the
// infinities, and for magnitudes outside the safe binary64 integer range.
// Negative zero is folded to positive zero so that -0 and 0 compare and
// marshal identically.
func NormalizeParamNumber(value any) (float64, bool) {
	number, ok := paramNumberValue(value)
	if !ok {
		return 0, false
	}
	if math.IsNaN(number) || math.IsInf(number, 0) {
		return 0, false
	}
	if math.Abs(number) > MaxSafeParamNumber {
		return 0, false
	}
	if number == 0 {
		// Fold -0 to +0; the comparison above is true for both.
		number = 0
	}
	return number, true
}

// paramNumberValue converts a JSON scalar to float64 without applying the
// parameter numeric domain. It reports false only for values that are not
// numbers at all.
func paramNumberValue(value any) (float64, bool) {
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
		return typed, true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	default:
		return 0, false
	}
}
