package resource

import (
	"context"
	"fmt"
	"io"
)

// Out is intentionally unavailable: this resource observes and materializes
// server-authorized facts but must never mutate a forge.
func Out(_ context.Context, _ string, _ io.Reader, _ io.Writer, _ io.Writer, _ Dependencies) error {
	return fmt.Errorf("forge-pr: out is unsupported for this read-only resource")
}
