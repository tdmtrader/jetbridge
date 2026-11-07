// jb-launcher is the entrypoint of a step pod's main container under the
// job step runtime. The program lives in atc/worker/jetbridge/launcher; this
// is its composition root and starts no subprocess itself.
package main

import (
	"os"

	"github.com/concourse/concourse/atc/worker/jetbridge/launcher"
)

func main() {
	os.Exit(launcher.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
