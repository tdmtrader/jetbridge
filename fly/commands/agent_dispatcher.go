package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"

	"github.com/concourse/concourse/fly/commands/internal/displayhelpers"
)

// AgentDispatcherCommand is a leaf command (not a subcommand group, so the
// no-argument invocation runs Execute and prints status — go-flags would
// otherwise demand a subcommand). An optional positional ACTION sets the mode:
//
//	fly agent dispatcher            → show current status
//	fly agent dispatcher pause      → mode=paused (reconciler stays alive)
//	fly agent dispatcher resume     → mode=active (auto-dispatch on)
//	fly agent dispatcher off        → mode=off   (fully dormant)
type AgentDispatcherCommand struct {
	Args struct {
		Action string `positional-arg-name:"ACTION" description:"pause | resume | off (omit to show status)"`
	} `positional-args:"yes"`
	Json bool `long:"json" description:"Print status as JSON"`
}

// dispatcherStatus mirrors agent/api/dispatcher.Response.
type dispatcherStatus struct {
	Mode      string  `json:"mode"`
	UpdatedAt *string `json:"updated_at"`
	UpdatedBy *string `json:"updated_by"`
}

// actionToMode maps the CLI verb to the wire mode. ok=false for an unknown verb.
func actionToMode(action string) (string, bool) {
	switch action {
	case "pause":
		return "paused", true
	case "resume", "active":
		return "active", true
	case "off":
		return "off", true
	default:
		return "", false
	}
}

func (command *AgentDispatcherCommand) Execute([]string) error {
	target, err := loadAgentTarget()
	if err != nil {
		return err
	}

	var status dispatcherStatus
	if command.Args.Action == "" {
		resp, err := agentAPIRequest(target, "GET", "/api/v1/agent/dispatcher", nil)
		if err != nil {
			return err
		}
		if err := decodeOrError(resp, &status); err != nil {
			return err
		}
		return printDispatcherStatus(status, command.Json)
	}

	mode, ok := actionToMode(command.Args.Action)
	if !ok {
		return fmt.Errorf("unknown action %q: want one of pause, resume, off", command.Args.Action)
	}
	payload, err := json.Marshal(map[string]string{"mode": mode})
	if err != nil {
		return err
	}
	resp, err := agentAPIRequestWithType(target, "PUT", "/api/v1/agent/dispatcher",
		"application/json", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	if err := decodeOrError(resp, &status); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "dispatcher mode set to %s\n", status.Mode)
	return printDispatcherStatus(status, command.Json)
}

func printDispatcherStatus(status dispatcherStatus, asJSON bool) error {
	if asJSON {
		return displayhelpers.JsonPrint(status)
	}
	updated := "unknown (no settings row)"
	if status.UpdatedAt != nil {
		by := ""
		if status.UpdatedBy != nil {
			by = " by " + *status.UpdatedBy
		}
		updated = *status.UpdatedAt + by
	}
	fmt.Printf("dispatcher mode: %s\n", status.Mode)
	fmt.Printf("last updated:    %s\n", updated)
	return nil
}
