package resource

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/concourse/concourse/agent/pullrequest"
)

func Check(ctx context.Context, stdin io.Reader, stdout, _ io.Writer, dependencies Dependencies) error {
	request, err := decodeRequest(stdin)
	if err != nil {
		return err
	}
	locator, poll, fresh, err := request.Source.validate()
	if err != nil {
		return err
	}
	if request.Version != nil {
		if err := request.Version.validate(); err != nil {
			return err
		}
	}
	// The supplied prior version is never acknowledgement authority. Terminal
	// controls intentionally avoid both provider resolution and network access.
	if request.Source.Monitor.AttentionRequired || request.Source.Monitor.Paused || request.Source.Monitor.OperatorTerminated {
		return writePrior(stdout, request.Version)
	}
	observer, err := observerFor(request.Source, dependencies)
	if err != nil {
		return err
	}
	observation, err := observer.Observe(ctx, locator, pullrequest.Cursor(request.Source.Monitor.AcknowledgedCursor))
	if err != nil {
		return fmt.Errorf("forge-pr: observe pull request")
	}
	now := time.Now().UTC()
	if dependencies.Clock != nil {
		now = dependencies.Clock().UTC()
	}
	action, actionable, err := pullrequest.ActionFor(observation, pullrequest.TriggerPolicy{Now: now, PollInterval: poll, FreshnessInterval: fresh, LastCursor: pullrequest.Cursor(request.Source.Monitor.AcknowledgedCursor), LastTargetSHA: request.Source.Monitor.LastReconciledTarget, LastReconciledAt: request.Source.Monitor.LastReconciledAt, ActiveActionDigest: request.Source.Monitor.ActiveActionDigest})
	if err != nil {
		return fmt.Errorf("forge-pr: action selection")
	}
	if !actionable {
		return writePrior(stdout, request.Version)
	}
	return writeJSON(stdout, []Version{versionFor(request.Source, action)})
}

func writePrior(stdout io.Writer, prior *Version) error {
	if prior == nil {
		return writeJSON(stdout, []Version{})
	}
	return writeJSON(stdout, []Version{*prior})
}
