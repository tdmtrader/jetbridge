// mcp-oauth-probe exposes a control pipe for the existing real auth fixture.
// It is built from the Brine module; it is not a production service.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/concourse/concourse/atc/worker/jetbridge/brine/steps"
)

func main() {
	output, err := steps.ProtectEventStream()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	defer output.Close()
	encoder := json.NewEncoder(output)
	var probe *steps.MCPOAuthProbe
	defer func() {
		if probe != nil {
			_ = probe.Close()
		}
	}()
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		var request struct {
			Action   string   `json:"action"`
			ClientID string   `json:"client_id"`
			Callback string   `json:"callback"`
			URL      string   `json:"url"`
			Scopes   []string `json:"scopes"`
			Seconds  int      `json:"seconds"`
		}
		if err = json.Unmarshal(scanner.Bytes(), &request); err != nil {
			_ = encoder.Encode(map[string]any{"error": "invalid control request"})
			continue
		}
		result := map[string]any{"ok": true}
		err = nil
		switch request.Action {
		case "start":
			if probe != nil {
				result["error"] = "already started"
				break
			}
			probe, err = steps.NewMCPOAuthProbe(request.ClientID, request.Callback)
			if err == nil {
				result["endpoint"] = probe.Endpoint()
			}
		case "approve":
			err = probe.Approve(request.URL, request.Scopes)
		case "advance":
			probe.Advance(time.Duration(request.Seconds) * time.Second)
		case "revoke":
			result["revoked"], err = probe.Revoke()
		case "events":
			result["events"] = probe.Events()
		case "close":
			if probe != nil {
				err = probe.Close()
				probe = nil
			}
		default:
			result["error"] = "unknown control action"
		}
		if err != nil {
			result["ok"], result["error"] = false, err.Error()
		}
		_ = encoder.Encode(result)
	}
}
