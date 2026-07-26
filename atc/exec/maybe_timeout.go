package exec

import (
	"context"
	"fmt"
	"time"
)

// ResolveTimeout returns the effective timeout for a step: an authored
// `timeout:` always wins; otherwise the platform default applies.
func ResolveTimeout(timeoutStr string, defaultTimeout time.Duration) (time.Duration, error) {
	if timeoutStr == "" {
		return defaultTimeout, nil
	}
	timeout, err := time.ParseDuration(timeoutStr)
	if err != nil {
		return 0, fmt.Errorf("parse timeout: %w", err)
	}
	return timeout, nil
}

func MaybeTimeout(ctx context.Context, timeoutStr string, defaultTimeout time.Duration) (context.Context, func(), error) {
	if timeoutStr == "" && defaultTimeout == 0 {
		return ctx, func() {}, nil
	}

	timeout, err := ResolveTimeout(timeoutStr, defaultTimeout)
	if err != nil {
		return nil, nil, err
	}

	processCtx, cancel := context.WithTimeout(ctx, timeout)
	return processCtx, cancel, nil
}
