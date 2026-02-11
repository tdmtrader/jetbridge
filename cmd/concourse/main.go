package main

import (
	"fmt"
	"os"

	"github.com/concourse/concourse"
	flags "github.com/jessevdk/go-flags"
	"github.com/vito/twentythousandtonnesofcrudeoil"
)

func main() {
	var cmd ConcourseCommand

	cmd.Version = func() {
		fmt.Printf("JetBridge %s (Concourse %s)\n", concourse.JetBridgeVersion, concourse.ConcourseVersion)
		os.Exit(0)
	}

	parser := flags.NewParser(&cmd, flags.HelpFlag|flags.PassDoubleDash)
	parser.NamespaceDelimiter = "-"

	cmd.LessenRequirements(parser)

	cmd.Web.WireDynamicFlags(parser.Command.Find("web"))

	twentythousandtonnesofcrudeoil.TheEnvironmentIsPerfectlySafe(parser, "CONCOURSE_")

	_, err := parser.Parse()
	handleError(err)
}

func handleError(err error) {
	if err != nil {
		if flagsErr, ok := err.(*flags.Error); ok && flagsErr.Type == flags.ErrHelp {
			fmt.Println(err)
			os.Exit(0)
		} else {
			fmt.Fprintf(os.Stderr, "error: %s\n", err)
		}

		os.Exit(1)
	}
}
