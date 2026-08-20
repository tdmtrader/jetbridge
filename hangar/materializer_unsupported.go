//go:build !linux && !darwin

package hangar

import (
	"context"
	"fmt"
)

func materializeCapturedTree(context.Context, string, string, string, TreeRef, string, materializerHooks) error {
	return fmt.Errorf("hangar: materialization is unsupported on this operating system")
}
