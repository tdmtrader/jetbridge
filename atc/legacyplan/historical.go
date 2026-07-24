package legacyplan

import (
	"encoding/json"
	"errors"
	"fmt"
)

var ErrActiveHarvestPlan = errors.New("legacy plan: harvest is retired and cannot execute")

func ContainsHarvest(raw []byte) (bool, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, fmt.Errorf("decode legacy plan: %w", err)
	}

	return containsHarvest(value), nil
}

func DecodeCompletedPublic(raw *json.RawMessage) (*json.RawMessage, error) {
	if raw == nil {
		return nil, nil
	}

	var value any
	if err := json.Unmarshal(*raw, &value); err != nil {
		return nil, fmt.Errorf("decode historical public plan: %w", err)
	}

	rewritten, err := rewrite(value)
	if err != nil {
		return nil, err
	}

	payload, err := json.Marshal(rewritten)
	if err != nil {
		return nil, fmt.Errorf("encode historical public plan: %w", err)
	}

	result := json.RawMessage(payload)
	return &result, nil
}

func containsHarvest(value any) bool {
	switch value := value.(type) {
	case map[string]any:
		_, hasStringID := value["id"].(string)
		_, hasHarvestObject := value["harvest"].(map[string]any)
		if hasStringID && hasHarvestObject {
			return true
		}

		for _, child := range value {
			if containsHarvest(child) {
				return true
			}
		}
	case []any:
		for _, child := range value {
			if containsHarvest(child) {
				return true
			}
		}
	}

	return false
}

func rewrite(value any) (any, error) {
	switch value := value.(type) {
	case map[string]any:
		_, hasStringID := value["id"].(string)
		harvest, hasHarvest := value["harvest"]
		if hasStringID && hasHarvest {
			harvestObject, ok := harvest.(map[string]any)
			if !ok {
				return nil, errors.New("decode historical public plan: harvest node must be an object")
			}

			name := ""
			if rawName, found := harvestObject["name"]; found {
				var ok bool
				name, ok = rawName.(string)
				if !ok {
					return nil, errors.New("decode historical public plan: harvest name must be a string")
				}
			}

			delete(value, "harvest")
			value["retired_step"] = map[string]any{
				"kind": "harvest",
				"name": name,
			}
		}

		for key, child := range value {
			rewritten, err := rewrite(child)
			if err != nil {
				return nil, err
			}
			value[key] = rewritten
		}
	case []any:
		for index, child := range value {
			rewritten, err := rewrite(child)
			if err != nil {
				return nil, err
			}
			value[index] = rewritten
		}
	}

	return value, nil
}
