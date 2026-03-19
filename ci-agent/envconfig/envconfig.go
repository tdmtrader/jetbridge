package envconfig

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// StringOrDefault returns the env var value or the default.
func StringOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Float64OrDefault parses an env var as float64 or returns the default.
func Float64OrDefault(key string, def float64) float64 {
	s := os.Getenv(key)
	if s == "" {
		return def
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return def
	}
	return f
}

// IntOrDefault parses an env var as int or returns the default.
func IntOrDefault(key string, def int) int {
	s := os.Getenv(key)
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

// DurationOrDefault parses an env var as time.Duration or returns the default.
func DurationOrDefault(key string, def time.Duration) time.Duration {
	s := os.Getenv(key)
	if s == "" {
		return def
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return def
	}
	return d
}

// BoolOrDefault parses an env var as bool or returns the default.
func BoolOrDefault(key string, def bool) bool {
	s := os.Getenv(key)
	if s == "" {
		return def
	}
	b, err := strconv.ParseBool(s)
	if err != nil {
		return def
	}
	return b
}

// Required returns the env var value or an error if not set.
func Required(key string) (string, error) {
	v := os.Getenv(key)
	if v == "" {
		return "", fmt.Errorf("environment variable %s is required", key)
	}
	return v, nil
}
