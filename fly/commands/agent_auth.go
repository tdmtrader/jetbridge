package commands

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/credentials"
	"github.com/concourse/concourse/fly/rc"
)

type AgentAuthCommand struct {
	Token     string        `long:"token" description:"Token value. If omitted, fly walks you through claude setup-token and reads the pasted token from stdin."`
	Kind      string        `long:"kind" default:"anthropic_oauth" choice:"anthropic_oauth" choice:"anthropic_api_key" description:"Credential kind"`
	ExpiresIn time.Duration `long:"expires-in" default:"8760h" description:"How long until the token expires (claude setup-token issues ~1-year tokens)"`
	Delete    bool          `long:"delete" description:"Delete the stored credential of --kind instead of storing one"`
	Platform  bool          `long:"platform" description:"Manage the shared platform credential (funds harvest judge / retrospective work) instead of your own. Admin only."`
}

func (command *AgentAuthCommand) Execute([]string) error {
	target, err := rc.LoadTarget(Fly.Target, Fly.Verbose)
	if err != nil {
		return err
	}
	if err := target.Validate(); err != nil {
		return err
	}

	whose := "your"
	if command.Platform {
		whose = "the platform"
	}

	if command.Delete {
		if err := target.Client().DeleteAgentUserCredential(command.Kind, command.Platform); err != nil {
			return err
		}
		fmt.Printf("deleted %s %s credential\n", whose, command.Kind)
		return nil
	}

	token := command.Token
	if token == "" {
		fmt.Println("Run `claude setup-token` in a terminal where you can complete the browser login,")
		fmt.Println("then paste the resulting token below. It is stored encrypted on your Concourse")
		fmt.Println("and attached (as CLAUDE_CODE_OAUTH_TOKEN) only to agent runs you trigger.")
		fmt.Print("token: ")
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil {
			return fmt.Errorf("reading token from stdin: %w", err)
		}
		token = strings.TrimSpace(line)
	}
	if token == "" {
		return fmt.Errorf("no token provided")
	}

	req := credentials.PutRequest{
		Kind:      command.Kind,
		Token:     token,
		ExpiresAt: time.Now().Add(command.ExpiresIn).Unix(),
	}
	if command.Platform {
		req.User = credentials.PlatformUserName
	}
	if err := target.Client().SetAgentUserCredential(req); err != nil {
		return err
	}

	fmt.Printf("stored %s %s credential; expires %s\n", whose, command.Kind, time.Unix(req.ExpiresAt, 0).Format("2006-01-02"))
	return nil
}
