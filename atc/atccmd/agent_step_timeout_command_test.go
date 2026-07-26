package atccmd_test

import (
	"strings"
	"testing"

	"github.com/concourse/concourse/atc/atccmd"
	"github.com/jessevdk/go-flags"
)

func TestAgentStepDefaultTimeoutFlagDefault(t *testing.T) {
	command := &atccmd.ATCCommand{}
	parser := flags.NewParser(command, flags.Default)
	parser.NamespaceDelimiter = "-"
	run := parser.Find("run")

	option := run.FindOptionByLongName("agent-step-default-timeout")
	if option == nil {
		t.Fatal("--agent-step-default-timeout is missing")
	}
	if got := strings.Join(option.Default, ","); got != "2h" {
		t.Fatalf("--agent-step-default-timeout default = %q, want %q", got, "2h")
	}
}
