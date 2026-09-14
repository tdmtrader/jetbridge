// jb-mcp-client is a runnable reference for JetBridge's registered OAuth profile.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/concourse/concourse/internal/mcpclient"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/skratchdot/open-golang/open"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: jb-mcp-client connect|list|status|logout --endpoint URL --client-id ID [options]")
	}
	command := args[0]
	flags := flag.NewFlagSet("jb-mcp-client "+command, flag.ContinueOnError)
	var cfg mcpclient.Config
	var scope, team, pipeline string
	var browser bool
	flags.StringVar(&cfg.Endpoint, "endpoint", "", "MCP resource URL, ending in /api/v1/mcp")
	flags.StringVar(&cfg.ClientID, "client-id", "jetbridge-reference", "Pre-registered public OAuth client ID")
	flags.StringVar(&cfg.RedirectURL, "redirect-url", "http://127.0.0.1:8964/callback", "Exact pre-registered loopback callback URL")
	flags.StringVar(&cfg.StatePath, "credentials", "", "Credential file (defaults to the user config directory)")
	flags.StringVar(&scope, "scope", "read offline_access", "Space- or comma-separated requested capabilities")
	flags.StringVar(&team, "team", "main", "Team for pipeline status")
	flags.StringVar(&pipeline, "pipeline", "", "Pipeline for status")
	flags.BoolVar(&browser, "open-browser", true, "Open the authorization URL automatically")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if cfg.StatePath == "" {
		root, err := os.UserConfigDir()
		if err != nil {
			return err
		}
		hash := sha256.Sum256([]byte(cfg.Endpoint + "\n" + cfg.ClientID))
		cfg.StatePath = filepath.Join(root, "jetbridge", "mcp", hex.EncodeToString(hash[:8])+".json")
	}
	cfg.Scopes = strings.Fields(strings.ReplaceAll(scope, ",", " "))
	client, err := mcpclient.New(cfg)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if command == "connect" {
		err := client.Login(ctx, func(url string) error {
			fmt.Printf("Authorize this connection in your browser:\n%s\n", url)
			if browser {
				_ = open.Start(url)
			}
			return nil
		})
		if err != nil {
			return err
		}
		fmt.Printf("MCP login saved to %s\n", cfg.StatePath)
		return nil
	}
	timeout, cancelTimeout := context.WithTimeout(ctx, 45*time.Second)
	defer cancelTimeout()
	if command == "logout" {
		if err := client.Logout(timeout); err != nil {
			return err
		}
		fmt.Println("MCP connection revoked and local credentials removed")
		return nil
	}
	if command != "list" && command != "status" {
		return errors.New("command must be connect, list, status, or logout")
	}
	if command == "status" && pipeline == "" {
		return errors.New("status requires --pipeline")
	}
	session, err := client.Connect(timeout)
	if err != nil {
		return err
	}
	defer session.Close()
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if command == "list" {
		result, err := session.ListTools(timeout, nil)
		if err != nil {
			return err
		}
		return encoder.Encode(result)
	}
	result, err := session.CallTool(timeout, &sdk.CallToolParams{Name: "pipeline_status", Arguments: map[string]any{"team": team, "pipeline": pipeline}})
	if err != nil {
		return err
	}
	if err := encoder.Encode(result); err != nil {
		return err
	}
	if result.IsError {
		return errors.New("pipeline status failed")
	}
	return nil
}
