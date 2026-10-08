//go:build !linux && !darwin

package hangar

import (
	"context"
	"fmt"
	"os"
)

func materializeCapturedTreeRoot(context.Context, *os.Root, string, string, TreeRef, *os.Root, materializerHooks) error {
	return fmt.Errorf("hangar: materialization is unsupported on this operating system")
}
