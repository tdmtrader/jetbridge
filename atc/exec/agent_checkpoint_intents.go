package exec

import (
	"context"
	"errors"
	"sync"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/runtime"
)

// agentCheckpointLiveIntents owns only best-effort intent observation around
// an already-running process. It never decides that a process is safe: every
// actual capture remains gated by the exact runtime provider-boundary and
// quiescence seams carried in its request.
type agentCheckpointLiveIntents struct {
	context context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup

	controller AgentCheckpointController
	request    AgentCheckpointCaptureRequest
	intents    chan CheckpointCaptureTrigger
	logger     lager.Logger
}

func startAgentCheckpointLiveIntents(
	parent context.Context,
	controller AgentCheckpointController,
	process runtime.Process,
	request AgentCheckpointCaptureRequest,
	elapsedInterval time.Duration,
	explicit AgentCheckpointExplicitIntentSource,
	logger lager.Logger,
) *agentCheckpointLiveIntents {
	if controller == nil || process == nil || elapsedInterval <= 0 {
		return nil
	}
	boundary, hasBoundary := process.(runtime.SafeBoundaryProcess)
	quiescence, hasQuiescence := process.(runtime.CheckpointProcess)
	if !hasBoundary || !hasQuiescence {
		return nil
	}

	ctx, cancel := context.WithCancel(parent)
	live := &agentCheckpointLiveIntents{
		context:    ctx,
		cancel:     cancel,
		controller: controller,
		request:    request,
		intents:    make(chan CheckpointCaptureTrigger, 1),
		logger:     logger,
	}
	live.request.Boundary = boundary
	live.request.Quiescence = quiescence

	live.wg.Add(1)
	go live.captureLoop()
	live.wg.Add(1)
	go live.watchElapsed(elapsedInterval)
	if preemption, supported := process.(runtime.CheckpointPreemptionProcess); supported {
		live.wg.Add(1)
		go live.watchPreemption(preemption)
	}
	if explicit != nil {
		live.wg.Add(1)
		go live.watchExplicit(explicit)
	}
	return live
}

// Stop first cancels in-flight safe-boundary capture, then joins every
// observer. AgentStep calls it after Process.Wait and before terminal capture,
// so all capture leases have an opportunity to release before finalization.
func (live *agentCheckpointLiveIntents) Stop() {
	if live == nil {
		return
	}
	live.cancel()
	live.wg.Wait()
}

func (live *agentCheckpointLiveIntents) captureLoop() {
	defer live.wg.Done()
	for {
		select {
		case <-live.context.Done():
			return
		case trigger := <-live.intents:
			if _, err := live.controller.CaptureLive(live.context, trigger, live.request); err != nil && !errors.Is(err, context.Canceled) {
				live.logError("agent-checkpoint-live-capture-failed", err, lager.Data{"trigger": string(trigger)})
			}
		}
	}
}

func (live *agentCheckpointLiveIntents) watchElapsed(interval time.Duration) {
	defer live.wg.Done()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-live.context.Done():
			return
		case <-ticker.C:
			live.enqueue(CheckpointCaptureTriggerElapsed)
		}
	}
}

func (live *agentCheckpointLiveIntents) watchPreemption(source runtime.CheckpointPreemptionProcess) {
	defer live.wg.Done()
	_, err := source.WaitForCheckpointPreemption(live.context)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			live.logError("agent-checkpoint-preemption-watch-failed", err, nil)
		}
		return
	}
	live.enqueue(CheckpointCaptureTriggerPreemption)
}

func (live *agentCheckpointLiveIntents) watchExplicit(source AgentCheckpointExplicitIntentSource) {
	defer live.wg.Done()
	if err := source.WaitForAgentCheckpointIntent(live.context, live.request.Provenance.Identity); err != nil {
		if !errors.Is(err, context.Canceled) {
			live.logError("agent-checkpoint-explicit-intent-watch-failed", err, nil)
		}
		return
	}
	live.enqueue(CheckpointCaptureTriggerExplicit)
}

func (live *agentCheckpointLiveIntents) enqueue(trigger CheckpointCaptureTrigger) {
	select {
	case <-live.context.Done():
	case live.intents <- trigger:
	default:
		// A pending intent is sufficient: it will still ask the provider for a
		// real safe boundary, and no timer/preemption burst can run captures in
		// parallel or grow unbounded in memory.
	}
}

func (live *agentCheckpointLiveIntents) logError(message string, err error, data lager.Data) {
	if live.logger != nil {
		live.logger.Error(message, err, data)
	}
}
