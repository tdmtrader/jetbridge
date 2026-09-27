package detached

import (
	"context"

	"github.com/concourse/concourse/atc"
)

// RunObservation is the compact view of a Run that every workload's stdio MCP
// status tool returns. Its schema matches its JSON representation. The larger
// PipelineRun HTTP type has custom Unix timestamp encoding and is not suitable
// for reflection-derived MCP schemas.
type RunObservation struct {
	ID              int                      `json:"id"`
	Number          int                      `json:"number"`
	ContractVersion atc.RunContractVersion   `json:"run_contract_version"`
	Status          atc.RunStatus            `json:"status"`
	Reclaimed       bool                     `json:"reclaimed"`
	Terminal        *atc.RunTerminalResult   `json:"terminal,omitempty"`
	Captures        []atc.RunCaptureProgress `json:"captures,omitempty"`
}

// Observe reads a Run and projects it. A pending Run has no terminal
// observation; a terminal one retains its result references after build
// cleanup, but never the result files themselves.
func (c *Client) Observe(ctx context.Context, handle Handle) (RunObservation, error) {
	run, err := c.Status(ctx, handle)
	if err != nil {
		return RunObservation{}, err
	}
	return RunObservation{
		ID: run.ID, Number: run.Number, ContractVersion: run.ContractVersion,
		Status: run.Status, Reclaimed: run.Reclaimed, Terminal: run.Terminal,
		Captures: run.Captures,
	}, nil
}
