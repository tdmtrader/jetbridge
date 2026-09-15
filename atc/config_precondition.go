package atc

import (
	"errors"
	"strconv"
)

const ConfigVersionConflictCode = "config_version_conflict"

// ParseConfigVersion is the strict wire contract shared by HTTP, Go and MCP.
// No whitespace, sign, suffix or missing header is accepted.
func ParseConfigVersion(value string) (int, error) {
	if value == "" || (len(value) > 1 && value[0] == '0') {
		return 0, errors.New("config version must be a decimal integer in 0..2147483647")
	}
	for _, c := range value {
		if c < '0' || c > '9' {
			return 0, errors.New("config version must be a decimal integer in 0..2147483647")
		}
	}
	number, err := strconv.ParseInt(value, 10, 32)
	if err != nil {
		return 0, errors.New("config version must be a decimal integer in 0..2147483647")
	}
	return int(number), nil
}

// CanonicalAction preserves an existing authority when a new wire route adds
// stronger semantics. Dispatch and metrics may retain the distinct route ID.
func CanonicalAction(action string) string {
	if action == SaveConfigConditional {
		return SaveConfig
	}
	return action
}
