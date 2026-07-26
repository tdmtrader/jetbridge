package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"

	"github.com/concourse/concourse/fly/commands/internal/displayhelpers"
)

// AgentActionsCommand is a leaf command (not a subcommand group, so the
// no-argument invocation runs Execute and prints status — go-flags would
// otherwise demand a subcommand), mirroring `fly agent dispatcher`. An
// optional positional ACTION flips the cluster-wide external-effect brake:
//
//	fly agent actions             → show current mode
//	fly agent actions status      → same, explicit
//	fly agent actions suppress    → mode=suppressed (no external side effects)
//	fly agent actions resume      → mode=active
//
// Suppression stops publisher writes ONLY. Dispatch, agent execution, and
// sealing keep running — that is what makes it a shadow mode.
type AgentActionsCommand struct {
	Args struct {
		Action string `positional-arg-name:"ACTION" description:"suppress | resume | status (omit to show status)"`
	} `positional-args:"yes"`
	Json bool `long:"json" description:"Print status as JSON"`
}

// actionsStatus mirrors agent/api/actions.Response.
type actionsStatus struct {
	Mode      string  `json:"mode"`
	Source    string  `json:"source"`
	UpdatedAt *string `json:"updated_at"`
	UpdatedBy *string `json:"updated_by"`
}

// actionsActionToMode maps the CLI verb to the wire mode. ok=false for an
// unknown verb; "status" is handled by the caller as a read, not a write.
func actionsActionToMode(action string) (string, bool) {
	switch action {
	case "suppress":
		return "suppressed", true
	case "resume", "active":
		return "active", true
	default:
		return "", false
	}
}

func (command *AgentActionsCommand) Execute([]string) error {
	target, err := loadAgentTarget()
	if err != nil {
		return err
	}

	var status actionsStatus
	if command.Args.Action == "" || command.Args.Action == "status" {
		resp, err := agentAPIRequest(target, "GET", "/api/v1/agent/actions", nil)
		if err != nil {
			return err
		}
		if err := decodeOrError(resp, &status); err != nil {
			return err
		}
		return printActionsStatus(status, command.Json)
	}

	mode, ok := actionsActionToMode(command.Args.Action)
	if !ok {
		return fmt.Errorf("unknown action %q: want one of suppress, resume, status", command.Args.Action)
	}
	payload, err := json.Marshal(map[string]string{"mode": mode})
	if err != nil {
		return err
	}
	resp, err := agentAPIRequestWithType(target, "PUT", "/api/v1/agent/actions",
		"application/json", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	if err := decodeOrError(resp, &status); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "external actions set to %s\n", status.Mode)
	return printActionsStatus(status, command.Json)
}

func printActionsStatus(status actionsStatus, asJSON bool) error {
	if asJSON {
		return displayhelpers.JsonPrint(status)
	}
	updated := "never (never engaged)"
	if status.UpdatedAt != nil {
		by := ""
		if status.UpdatedBy != nil {
			by = " by " + *status.UpdatedBy
		}
		updated = *status.UpdatedAt + by
	}
	fmt.Printf("actions: %s\n", status.Mode)
	fmt.Printf("source:  %s\n", status.Source)
	fmt.Printf("last updated: %s\n", updated)
	if status.Mode == "suppressed" {
		fmt.Println("external side effects (publisher writes) are REFUSED; runs still execute and seal")
	}
	return nil
}
