package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/concourse/concourse/agent/review"
	"github.com/concourse/concourse/atc"
	"github.com/google/uuid"
)

// Submit saves its request identity before uploading or admitting a Run. A
// stopped caller resumes using the same receipt. Ready means the detached
// worker acknowledged credentials; admission alone never means ready.
func (c *Client) Submit(ctx context.Context, options SubmitOptions) (Submission, error) {
	var result Submission
	if options.Team == "" || options.Template == "" {
		return result, errors.New("team and template are required")
	}
	bundle, err := review.LoadBundle(options.Input)
	if err != nil {
		return result, err
	}
	file, err := openReceipt(ctx, options.Receipt, bundle.Dir)
	if err != nil {
		return result, err
	}
	defer file.Close()
	receipt, found, err := file.Load()
	if err != nil {
		return result, err
	}
	if found {
		if receipt.Server != c.base || receipt.Team != options.Team || receipt.Template != options.Template || receipt.InputDigest != bundle.Digest {
			return result, errors.New("saved receipt belongs to another input or destination; use its original submission or a new receipt")
		}
	} else {
		receipt = submissionReceipt{Version: "review-invocation/v1", Server: c.base, Team: options.Team, Template: options.Template, InputDigest: bundle.Digest, InvocationKey: uuid.NewString()}
		if err := file.Save(receipt); err != nil {
			return result, err
		}
	}
	if receipt.RunID == 0 {
		var run atc.PipelineRun
		intent := atc.CreatePipelineRunV2Request{InvocationKey: receipt.InvocationKey, Inputs: map[string]atc.RunInputSource{"change": {SourceID: receipt.SourceID}}}
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
			source, err := c.uploadBundle(ctx, options, bundle)
			if err != nil {
				return result, err
			}
			receipt.SourceID = source.SourceID
			if err = file.Save(receipt); err != nil {
				return result, err
			}
			intent.Inputs["change"] = source
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
		state, err := c.CredentialSession(ctx, result.Handle, "findings")
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
				return result, errors.New("a local auth file is required before this review can run")
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
			state, err = c.HandoffCredentials(ctx, result.Handle, "findings", auth)
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

func (c *Client) uploadBundle(ctx context.Context, options SubmitOptions, bundle *review.Bundle) (atc.RunInputSource, error) {
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() { err := bundle.WriteRunInputArchive(ctx, writer); writer.CloseWithError(err); done <- err }()
	source, err := c.UploadInput(ctx, options.Team, options.Template, "change", reader)
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
