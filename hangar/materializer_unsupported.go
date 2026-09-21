//go:build !linux && !darwin

package hangar

import (
	"context"
	"fmt"
	"os"
)

func materializeCapturedTree(context.Context, string, string, string, TreeRef, *os.Root, materializerHooks) error {
	return fmt.Errorf("hangar: materialization is unsupported on this operating system")
}

func materializeCapturedTreeRoot(context.Context, *os.Root, string, string, TreeRef, *os.Root) error {
	return fmt.Errorf("hangar: materialization is unsupported on this operating system")
}
