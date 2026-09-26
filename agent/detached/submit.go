package detached

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/google/uuid"
)

// Workload tells the neutral client which sealed input a detached Run takes,
// which result's producer receives the owner's credentials, and how to load
// and verify the local input. It is fixed by the adapter, never by a caller.
type Workload struct {
	// Name is recorded in the submission receipt so a receipt cannot be
	// replayed under another workload.
	Name string
	// Input is the run_inputs name the sealed input is uploaded under.
	Input string
	// CredentialResult names the result whose producer receives the handoff.
	CredentialResult string
	// Load verifies the sealed input directory and returns its digest and
	// archive writer.
	Load func(dir string) (Input, error)
}

// Input is a verified local input. WriteArchive re-verifies the input before
// streaming it, so a change after Load is refused rather than uploaded.
type Input interface {
	Digest() string
	WriteArchive(ctx context.Context, w io.Writer) error
}

func (w Workload) validate() error {
	if w.Name == "" || w.Input == "" || w.CredentialResult == "" || w.Load == nil {
		return errors.New("a complete workload is required")
	}
	return nil
}

// SubmitOptions names local artifacts. Credentials are selected by local client
// configuration and never become Run parameters or MCP tool arguments.
type SubmitOptions struct {
	Team, Template           string
	Input, Receipt, AuthFile string
}

type Submission struct {
	RunID   int    `json:"run_id"`
	Handle  Handle `json:"handle"`
	Ready   bool   `json:"ready"`
	State   string `json:"state"`
	Message string `json:"message,omitempty"`
}

// Submit saves its request identity before uploading or admitting a Run. A
// stopped caller resumes using the same receipt. Ready means the detached
// worker acknowledged credentials; admission alone never means ready.
func (c *Client) Submit(ctx context.Context, workload Workload, options SubmitOptions) (Submission, error) {
	var result Submission
	if err := workload.validate(); err != nil {
		return result, err
	}
	if options.Team == "" || options.Template == "" {
		return result, errors.New("team and template are required")
	}
	input, err := workload.Load(options.Input)
	if err != nil {
		return result, err
	}
	file, err := openReceipt(ctx, options.Receipt, options.Input)
	if err != nil {
		return result, err
	}
	defer file.Close()
	receipt, found, err := file.Load()
	if err != nil {
		return result, err
	}
	if found {
		if receipt.Workload != workload.Name || receipt.Server != c.base || receipt.Team != options.Team || receipt.Template != options.Template || receipt.InputDigest != input.Digest() {
			return result, errors.New("saved receipt belongs to another workload, input or destination; use its original submission or a new receipt")
		}
	} else {
		receipt = submissionReceipt{Version: receiptVersion, Workload: workload.Name, Server: c.base, Team: options.Team, Template: options.Template, InputDigest: input.Digest(), InvocationKey: uuid.NewString()}
		if err := file.Save(receipt); err != nil {
			return result, err
		}
	}
	if receipt.RunID == 0 {
		var run atc.PipelineRun
		intent := atc.CreatePipelineRunV2Request{InvocationKey: receipt.InvocationKey, Inputs: map[string]atc.RunInputSource{workload.Input: {SourceID: receipt.SourceID}}}
		if receipt.SourceID != "" {
			run, err = c.CreateRun(ctx, options.Team, options.Template, intent)
			if err != nil {
				var refused HTTPError
				// Only a definite invalid/missing input grant permits another
				// upload. An uncertain response never changes invocation intent.
				if !errors.As(err, &refused) || refused.StatusCode != http.StatusBadRequest {
					return result, err
				}
			}
		}
		if run.ID == 0 {
			source, err := c.uploadInput(ctx, workload.Input, options, input)
			if err != nil {
				return result, err
			}
			receipt.SourceID = source.SourceID
			if err = file.Save(receipt); err != nil {
				return result, err
			}
			intent.Inputs[workload.Input] = source
			run, err = c.CreateRun(ctx, options.Team, options.Template, intent)
			if err != nil {
				return result, err
			}
		}
		receipt.RunID, receipt.Number = run.ID, run.Number
		result = submissionResult(receipt)
		if err = file.Save(receipt); err != nil {
			return result, err
		}
	}
	result = submissionResult(receipt)
	for {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		state, err := c.CredentialSession(ctx, result.Handle, workload.CredentialResult)
		if err != nil {
			return result, err
		}
		if state.RunID != result.RunID {
			return result, errors.New("credential session belongs to a different Run")
		}
		result.State = state.Status
		switch state.Status {
		case "ready":
			result.Ready = true
			return result, nil
		case "claimed":
			return result, errors.New("credential delivery is already claimed; inspect this Run instead of resending credentials")
		case "available":
			if options.AuthFile == "" {
				return result, errors.New("a local auth file is required before this Run can proceed")
			}
			auth, err := os.Open(options.AuthFile)
			if err != nil {
				return result, errors.New("cannot open the configured local auth file")
			}
			st, err := auth.Stat()
			if err != nil || !st.Mode().IsRegular() || st.Size() > 65536 {
				auth.Close()
				return result, errors.New("auth file must be a bounded regular file")
			}
			state, err = c.HandoffCredentials(ctx, result.Handle, workload.CredentialResult, auth)
			if err != nil {
				return result, err
			}
			if state.RunID != result.RunID {
				return result, errors.New("handoff returned a different Run")
			}
			result.State = state.Status
			if state.Status == "ready" {
				result.Ready = true
				return result, nil
			}
			if state.Status == "claimed" {
				return result, errors.New("credential delivery is already claimed; inspect this Run instead of resending credentials")
			}
		}
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func submissionResult(receipt submissionReceipt) Submission {
	return Submission{RunID: receipt.RunID, Handle: Handle{Team: receipt.Team, Template: receipt.Template, Number: receipt.Number}, State: "admitted"}
}

func (c *Client) uploadInput(ctx context.Context, name string, options SubmitOptions, input Input) (atc.RunInputSource, error) {
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() { err := input.WriteArchive(ctx, writer); writer.CloseWithError(err); done <- err }()
	source, err := c.UploadInput(ctx, options.Team, options.Template, name, reader)
	reader.CloseWithError(err)
	archiveErr := <-done
	if err != nil {
		return atc.RunInputSource{}, err
	}
	if archiveErr != nil {
		return atc.RunInputSource{}, archiveErr
	}
	return source, nil
}
