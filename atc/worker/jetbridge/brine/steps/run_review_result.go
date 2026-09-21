package steps

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/agent/review"
	"github.com/google/jsonschema-go/jsonschema"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func RunReviewResultDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[RunOutputRuntime, RunOutputCandidate]("its runtime producer publishes review worker findings", []string{"review-workspace", "review-binaries"}, func(in RunOutputRuntime, _ brine.Params, rec *brine.Recorder, res brine.Resources) (RunOutputCandidate, error) {
			change, err := newReviewChange(res)
			if err != nil {
				return RunOutputCandidate{}, err
			}
			if _, stderr, err := reviewCommand(change.Binaries.CLI, change.captureArgs(change.Input), ""); err != nil {
				return RunOutputCandidate{}, fmt.Errorf("capture real review: %w: %s", err, stderr)
			}
			bundle, err := review.LoadBundle(change.Input)
			if err != nil {
				return RunOutputCandidate{}, err
			}
			change.Digest = bundle.Digest
			change.RunID = in.Start.Creation.Run.ID()
			return publishRunCandidate(in, rec, func(directory string) error {
				result, err := change.runWorker("finding", reviewSyntheticAuth, "")
				if err != nil {
					return err
				}
				if result.CommandErr != nil {
					return fmt.Errorf("real review worker failed: %w: %s", result.CommandErr, result.Stderr)
				}
				if err := reviewNoCredentials(result); err != nil {
					return err
				}
				for _, name := range []string{"review.json", "review.md"} {
					body, err := os.ReadFile(filepath.Join(result.Output, name))
					if err != nil {
						return err
					}
					if err = os.WriteFile(filepath.Join(directory, name), body, 0600); err != nil {
						return err
					}
				}
				return nil
			})
		}),
		brine.DefineMapUsing[RunResultPublication, RunResultPublication]("a fresh review {string} retrieves typed findings", []string{"auth-server", "review-binaries"}, func(in RunResultPublication, p brine.Params, _ *brine.Recorder, res brine.Resources) (RunResultPublication, error) {
			surface, _ := p.GetString(0)
			auth, err := authServer(res)
			if err != nil {
				return in, err
			}
			binary := res.Get("review-binaries").(reviewBinaries).CLI
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			var data []byte
			args := []string{"review", "result", "--target", "auth", "--team", "output-start", "--template", "review", "--run", strconv.Itoa(in.Start.Creation.Run.Number())}
			if surface == "CLI" {
				cmd := exec.CommandContext(ctx, binary, args...)
				cmd.Env = append(os.Environ(), "FLY_HOME="+auth.Home)
				var stdout, stderr bytes.Buffer
				cmd.Stdout, cmd.Stderr = &stdout, &stderr
				if err := cmd.Run(); err != nil {
					return in, fmt.Errorf("review result CLI: %w: %s", err, stderr.String())
				}
				data = stdout.Bytes()
			} else {
				cmd := exec.CommandContext(ctx, binary, "review", "mcp", "--target", "auth", "--team", "output-start", "--template", "review")
				cmd.Env = append(os.Environ(), "FLY_HOME="+auth.Home)
				session, err := sdk.NewClient(&sdk.Implementation{Name: "brine-findings", Version: "1"}, nil).Connect(ctx, &sdk.CommandTransport{Command: cmd}, nil)
				if err != nil {
					return in, err
				}
				defer session.Close()
				list, err := session.ListTools(ctx, nil)
				if err != nil {
					return in, err
				}
				var tool *sdk.Tool
				for _, one := range list.Tools {
					if one.Name == "review_result" {
						tool = one
					}
				}
				if tool == nil || tool.OutputSchema == nil {
					return in, fmt.Errorf("review_result has no typed tool declaration")
				}
				result, err := session.CallTool(ctx, &sdk.CallToolParams{Name: "review_result", Arguments: map[string]any{"run": in.Start.Creation.Run.Number()}})
				if err != nil {
					return in, err
				}
				if result.IsError || result.StructuredContent == nil {
					return in, fmt.Errorf("MCP did not return typed findings: %+v", result)
				}
				schemaData, err := json.Marshal(tool.OutputSchema)
				if err != nil {
					return in, err
				}
				var schema jsonschema.Schema
				if err = json.Unmarshal(schemaData, &schema); err != nil {
					return in, err
				}
				resolved, err := schema.Resolve(nil)
				if err != nil {
					return in, err
				}
				if err = resolved.Validate(result.StructuredContent); err != nil {
					return in, err
				}
				data, err = json.Marshal(result.StructuredContent)
				if err != nil {
					return in, err
				}
				if err = session.Close(); err != nil {
					return in, err
				}
			}
			var report review.Report
			if err := json.Unmarshal(data, &report); err != nil {
				return in, err
			}
			if report.RunID == nil || *report.RunID != in.Start.Creation.Run.ID() || report.Verdict != "findings" || len(report.Assessment.Findings) != 1 || report.Assessment.Findings[0].ID != "f-001" || report.Assessment.Findings[0].Location.Path != "parser.go" {
				return in, fmt.Errorf("remote report lost its durable identity or typed finding")
			}
			return in, nil
		}),
	}
}
