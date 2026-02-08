package main

import (
	"github.com/concourse/concourse/atc/atccmd"
	"github.com/jessevdk/go-flags"
)

type ConcourseCommand struct {
	Version func() `short:"v" long:"version" description:"Print the version of Concourse and exit"`

	Web     WebCommand       `command:"web"     description:"Run the web UI and build scheduler."`
	Migrate atccmd.Migration `command:"migrate" description:"Run database migrations."`

	GenerateKey GenerateKeyCommand `command:"generate-key" description:"Generate RSA key for use with Concourse components."`
}

func (cmd ConcourseCommand) LessenRequirements(parser *flags.Parser) {
	cmd.Web.LessenRequirements(parser.Find("web"))
}
