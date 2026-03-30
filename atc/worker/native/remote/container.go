package remote

import (
	"context"
	"fmt"
	"sync"

	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/native/agentpb"
)

var _ runtime.Container = (*Container)(nil)

// Container implements runtime.Container by proxying execution to a remote
// native agent via gRPC.
type Container struct {
	handle        string
	containerSpec runtime.ContainerSpec
	dbContainer   db.CreatedContainer
	workerName    string
	client        agentpb.NativeAgentClient
	compression   compression.Compression
	inputVolumes  []*Volume // one per ContainerSpec.Inputs entry, same order

	mu         sync.RWMutex
	properties map[string]string
}

func newContainer(
	handle string,
	containerSpec runtime.ContainerSpec,
	dbContainer db.CreatedContainer,
	workerName string,
	client agentpb.NativeAgentClient,
	enc compression.Compression,
	inputVolumes []*Volume,
) *Container {
	return &Container{
		handle:        handle,
		containerSpec: containerSpec,
		dbContainer:   dbContainer,
		workerName:    workerName,
		client:        client,
		compression:   enc,
		inputVolumes:  inputVolumes,
		properties:    make(map[string]string),
	}
}

func (c *Container) Run(ctx context.Context, spec runtime.ProcessSpec, pio runtime.ProcessIO) (runtime.Process, error) {
	// Stream remote inputs into local volumes on the agent before starting.
	if err := c.streamInputs(ctx); err != nil {
		return nil, fmt.Errorf("stream inputs: %w", err)
	}

	// Build the ExecRequest. Environment merging: ContainerSpec.Env merged
	// with ProcessSpec.Env. Host defaults are added on the agent side.
	env := mergeEnv(c.containerSpec.Env, spec.Env)

	// Working directory: the agent creates workDir/containers/<id>/work/.
	// If ProcessSpec.Dir differs from ContainerSpec.Dir, pass it as a
	// relative path for the agent.
	dir := ""
	if spec.Dir != "" && spec.Dir != c.containerSpec.Dir {
		dir = spec.Dir
	}

	req := &agentpb.ExecRequest{
		Id:   c.handle,
		Path: spec.Path,
		Args: spec.Args,
		Env:  env,
		Dir:  dir,
	}

	// Create a cancellable context for the Exec stream. This is used to
	// clean up the reader goroutine when Wait returns.
	streamCtx, streamCancel := context.WithCancel(ctx)

	stream, err := c.client.Exec(streamCtx, req)
	if err != nil {
		streamCancel()
		return nil, fmt.Errorf("exec on remote agent: %w", err)
	}

	return NewProcess(c.handle, stream, pio, c.client, streamCancel), nil
}

func (c *Container) Attach(ctx context.Context, processID string, pio runtime.ProcessIO) (runtime.Process, error) {
	return nil, fmt.Errorf("remote native runner does not support attach")
}

func (c *Container) Properties() (map[string]string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	props := make(map[string]string, len(c.properties))
	for k, v := range c.properties {
		props[k] = v
	}
	return props, nil
}

func (c *Container) SetProperty(name string, value string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.properties[name] = value
	return nil
}

func (c *Container) DBContainer() db.CreatedContainer {
	return c.dbContainer
}

// streamInputs copies remote artifact data to the agent via gRPC StreamIn.
// Mirrors native/container.go:168-198.
func (c *Container) streamInputs(ctx context.Context) error {
	for i, input := range c.containerSpec.Inputs {
		if input.Artifact == nil {
			continue
		}

		// Skip if the artifact is already on this worker (local volume).
		if input.Artifact.Source() == c.workerName {
			continue
		}

		if i >= len(c.inputVolumes) {
			continue
		}

		reader, err := input.Artifact.StreamOut(ctx, ".", c.compression)
		if err != nil {
			return fmt.Errorf("stream out artifact for input %q: %w", input.DestinationPath, err)
		}

		err = c.inputVolumes[i].StreamIn(ctx, ".", c.compression, 0, reader)
		reader.Close()
		if err != nil {
			return fmt.Errorf("stream in artifact for input %q: %w", input.DestinationPath, err)
		}
	}

	return nil
}

// mergeEnv merges two env slices. Variables in override take precedence.
func mergeEnv(base, override []string) []string {
	if len(base) == 0 && len(override) == 0 {
		return nil
	}

	// Simple append — the agent merges host defaults underneath.
	// Override keys in base are replaced.
	seen := make(map[string]int, len(base)+len(override))
	var result []string

	for _, env := range base {
		key := envKey(env)
		seen[key] = len(result)
		result = append(result, env)
	}
	for _, env := range override {
		key := envKey(env)
		if idx, ok := seen[key]; ok {
			result[idx] = env
		} else {
			seen[key] = len(result)
			result = append(result, env)
		}
	}
	return result
}

func envKey(env string) string {
	if i := indexOf(env, '='); i >= 0 {
		return env[:i]
	}
	return env
}

func indexOf(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}
