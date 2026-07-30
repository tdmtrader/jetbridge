package resource

import (
	"context"
	"fmt"
	"io"
	"path"
)

// Run adapts Concourse's basename protocol without letting callers select an
// operation independently of the mounted executable path.
func Run(ctx context.Context, executable string, args []string, stdin io.Reader, stdout, stderr io.Writer, dependencies Dependencies) error {
	switch path.Base(executable) {
	case "check":
		if len(args) != 0 {
			return fmt.Errorf("forge-pr: check accepts no arguments")
		}
		return Check(ctx, stdin, stdout, stderr, dependencies)
	case "in":
		if len(args) != 1 {
			return fmt.Errorf("forge-pr: in requires exactly one destination")
		}
		return In(ctx, args[0], stdin, stdout, stderr, dependencies)
	case "out":
		if len(args) != 1 {
			return fmt.Errorf("forge-pr: out requires exactly one sources directory")
		}
		return Out(ctx, args[0], stdin, stdout, stderr, dependencies)
	default:
		return fmt.Errorf("forge-pr: executable must be check, in, or out")
	}
}
