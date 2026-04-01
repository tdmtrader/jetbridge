package remote

import (
	"context"
	"fmt"
	"io"

	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/native/agentpb"
)

var _ runtime.Process = (*Process)(nil)

// Process implements runtime.Process by reading an ExecEvent stream from a
// remote native agent.
type Process struct {
	id           string
	client       agentpb.NativeAgentClient
	streamCancel context.CancelFunc

	done   chan struct{}
	result runtime.ProcessResult
	err    error
}

// NewProcess creates a Process that reads from the given ExecEvent stream,
// piping stdout/stderr to the ProcessIO writers. The streamCancel function
// is called when Wait returns to clean up the reader goroutine.
func NewProcess(
	id string,
	stream agentpb.NativeAgent_ExecClient,
	pio runtime.ProcessIO,
	client agentpb.NativeAgentClient,
	streamCancel context.CancelFunc,
) *Process {
	p := &Process{
		id:           id,
		client:       client,
		streamCancel: streamCancel,
		done:         make(chan struct{}),
	}

	go p.readStream(stream, pio)

	return p
}

func (p *Process) ID() string {
	return p.id
}

// Wait blocks until the process exits or the context is cancelled. On
// cancellation, Kill is called and the stream context is cancelled to
// clean up the reader goroutine.
func (p *Process) Wait(ctx context.Context) (runtime.ProcessResult, error) {
	select {
	case <-p.done:
		p.streamCancel()
		return p.result, p.err

	case <-ctx.Done():
		// Build was cancelled — kill the remote process.
		_, _ = p.client.Kill(context.Background(), &agentpb.KillRequest{Id: p.id})
		p.streamCancel()
		return runtime.ProcessResult{ExitStatus: -1}, ctx.Err()
	}
}

func (p *Process) SetTTY(tty runtime.TTYSpec) error {
	return fmt.Errorf("remote native runner does not support TTY")
}

// readStream reads ExecEvent messages from the gRPC stream and dispatches
// stdout/stderr to the ProcessIO writers. When the stream ends or an
// exit_status event is received, it signals completion via the done channel.
func (p *Process) readStream(stream agentpb.NativeAgent_ExecClient, pio runtime.ProcessIO) {
	defer close(p.done)

	for {
		event, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				return
			}
			p.result = runtime.ProcessResult{ExitStatus: -1}
			p.err = fmt.Errorf("stream recv: %w", err)
			return
		}

		switch ev := event.Event.(type) {
		case *agentpb.ExecEvent_Stdout:
			if pio.Stdout != nil {
				_, _ = pio.Stdout.Write(ev.Stdout)
			}
		case *agentpb.ExecEvent_Stderr:
			if pio.Stderr != nil {
				_, _ = pio.Stderr.Write(ev.Stderr)
			}
		case *agentpb.ExecEvent_ExitStatus:
			p.result = runtime.ProcessResult{ExitStatus: int(ev.ExitStatus)}
			return
		case *agentpb.ExecEvent_Error:
			p.result = runtime.ProcessResult{ExitStatus: -1}
			p.err = fmt.Errorf("remote exec error: %s", ev.Error)
			return
		}
	}
}
