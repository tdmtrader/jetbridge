package steps

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strconv"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/google/jsonschema-go/jsonschema"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func RunReviewClientDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[RunResultPublication, RunResultPublication]("a fresh review CLI reads the {string} Run status", []string{"auth-server", "review-binaries"}, func(in RunResultPublication, p brine.Params, _ *brine.Recorder, res brine.Resources) (RunResultPublication, error) {
			auth, err := authServer(res)
			if err != nil {
				return in, err
			}
			binaries := res.Get("review-binaries").(reviewBinaries)
			target, _ := p.GetString(0)
			number := in.Start.Creation.Run.Number()
			if target == "unknown" {
				number += 100000
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binaries.CLI, "review", "status", "--target", "auth", "--team", "output-start", "--template", "review", "--run", strconv.Itoa(number))
			cmd.Env = append(os.Environ(), "FLY_HOME="+auth.Home)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err = cmd.Run()
			if ctx.Err() != nil {
				return in, ctx.Err()
			}
			if target == "unknown" {
				if err == nil || stdout.Len() != 0 || !bytes.Contains(stderr.Bytes(), []byte("404")) {
					return in, fmt.Errorf("unknown Run did not produce an explicit client refusal")
				}
				return in, nil
			}
			if err != nil {
				return in, fmt.Errorf("review status CLI: %w: %s", err, stderr.String())
			}
			var got atc.PipelineRun
			if err = json.Unmarshal(stdout.Bytes(), &got); err != nil {
				return in, err
			}
			return in, checkReviewClientStatus(ctx, in, got)
		}),
		brine.DefineMapUsing[RunResultPublication, RunResultPublication]("a fresh review MCP reads the {string} Run status", []string{"auth-server", "review-binaries"}, func(in RunResultPublication, p brine.Params, _ *brine.Recorder, res brine.Resources) (RunResultPublication, error) {
			auth, err := authServer(res)
			if err != nil {
				return in, err
			}
			binaries := res.Get("review-binaries").(reviewBinaries)
			target, _ := p.GetString(0)
			number := in.Start.Creation.Run.Number()
			if target == "unknown" {
				number += 100000
			}
			if target == "invalid" {
				number = 0
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binaries.CLI, "review", "mcp", "--target", "auth", "--team", "output-start", "--template", "review")
			cmd.Env = append(os.Environ(), "FLY_HOME="+auth.Home)
			session, err := sdk.NewClient(&sdk.Implementation{Name: "brine-review", Version: "1"}, nil).Connect(ctx, &sdk.CommandTransport{Command: cmd}, nil)
			if err != nil {
				return in, fmt.Errorf("review MCP initialize: %w", err)
			}
			defer session.Close()
			listed, err := session.ListTools(ctx, nil)
			if err != nil {
				return in, err
			}
			var tool *sdk.Tool
			for _, candidate := range listed.Tools {
				if candidate.Name == "review_status" {
					tool = candidate
				}
			}
			if tool == nil || tool.InputSchema == nil || tool.OutputSchema == nil || tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
				return in, fmt.Errorf("review_status has no typed read-only tool declaration")
			}
			result, err := session.CallTool(ctx, &sdk.CallToolParams{Name: "review_status", Arguments: map[string]any{"run": number}})
			if target == "unknown" || target == "invalid" {
				if err == nil && (result == nil || !result.IsError || result.StructuredContent != nil) {
					return in, fmt.Errorf("MCP did not refuse %s Run explicitly", target)
				}
			} else {
				if err != nil {
					return in, err
				}
				if result.IsError || result.StructuredContent == nil {
					return in, fmt.Errorf("MCP did not return a structured Run observation: %+v", result)
				}
				data, err := json.Marshal(result.StructuredContent)
				if err != nil {
					return in, err
				}
				declaration, err := json.Marshal(tool.OutputSchema)
				if err != nil {
					return in, err
				}
				var schema jsonschema.Schema
				if err = json.Unmarshal(declaration, &schema); err != nil {
					return in, err
				}
				resolved, err := schema.Resolve(nil)
				if err != nil {
					return in, err
				}
				if err = resolved.Validate(result.StructuredContent); err != nil {
					return in, fmt.Errorf("MCP output violates advertised schema: %w", err)
				}
				var got atc.PipelineRun
				if err = json.Unmarshal(data, &got); err != nil {
					return in, err
				}
				if target == "redacted" {
					if got.ID != in.Start.Creation.Run.ID() || got.Terminal != nil || len(got.Captures) != 0 {
						return in, fmt.Errorf("MCP disclosed terminal bindings or capture progress to a caller outside the team")
					}
				} else if err = checkReviewClientStatus(ctx, in, got); err != nil {
					return in, err
				}
			}
			if err = session.Close(); err != nil {
				return in, fmt.Errorf("MCP did not exit on closed stdin: %w", err)
			}
			if cmd.ProcessState == nil || !cmd.ProcessState.Success() {
				return in, fmt.Errorf("MCP did not exit cleanly")
			}
			return in, nil
		}),
	}
}

func checkReviewClientStatus(ctx context.Context, in RunResultPublication, got atc.PipelineRun) error {

	factory := db.NewPipelineRunFactory(in.Start.DB.Conn, in.Start.DB.LockFactory)
	progress, err := factory.CaptureProgress(ctx, in.Start.Creation.Run.ID())
	if err != nil {
		return err
	}
	if len(progress) == 0 || !reflect.DeepEqual(got.Captures, progress) {
		return fmt.Errorf("client lost retained capture progress")
	}
	retained, terminal, err := factory.TerminalResult(ctx, in.Start.Creation.Run.ID())
	if err != nil {
		return err
	}
	if got.ID != in.Start.Creation.Run.ID() || got.Number != in.Start.Creation.Run.Number() || got.ContractVersion != atc.RunContractV2 {
		return fmt.Errorf("client lost the durable Run identity")
	}
	if !terminal {
		if got.Status != atc.RunStatusRunning || got.Terminal != nil {
			return fmt.Errorf("client presented a pending candidate as a terminal result")
		}
		return nil
	}
	if got.Terminal == nil || got.Status != retained.Status || got.Terminal.Version != retained.Version || !got.Terminal.CompletedAt.Equal(retained.CompletedAt) || !reflect.DeepEqual(got.Terminal.Results, retained.Results) {
		return fmt.Errorf("client lost the retained terminal observation")
	}
	return nil
}
