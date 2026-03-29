package native

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
)

// Compile-time check that Container satisfies runtime.Container.
var _ runtime.Container = (*Container)(nil)

// Container implements runtime.Container by running processes as local OS
// commands via os/exec.
type Container struct {
	handle        string
	containerDir  string // base scratch dir for this container
	containerSpec runtime.ContainerSpec
	dbContainer   db.CreatedContainer
	workerName    string
	compression   compression.Compression
	inputVolumes  []*Volume // one per ContainerSpec.Inputs entry, same order

	mu         sync.RWMutex
	properties map[string]string
}

func newContainer(
	handle string,
	containerDir string,
	containerSpec runtime.ContainerSpec,
	dbContainer db.CreatedContainer,
	workerName string,
	enc compression.Compression,
	inputVolumes []*Volume,
) *Container {
	return &Container{
		handle:        handle,
		containerDir:  containerDir,
		containerSpec: containerSpec,
		dbContainer:   dbContainer,
		workerName:    workerName,
		compression:   enc,
		inputVolumes:  inputVolumes,
		properties:    make(map[string]string),
	}
}

// Run starts a local OS process. Before starting, it streams any remote input
// artifacts into local volumes. If the executable does not exist, an
// ExecutableNotFoundError is returned.
func (c *Container) Run(ctx context.Context, spec runtime.ProcessSpec, pio runtime.ProcessIO) (runtime.Process, error) {
	// Stream remote inputs into local volumes before starting the process.
	if err := c.streamInputs(ctx); err != nil {
		return nil, fmt.Errorf("stream inputs: %w", err)
	}

	execPath := spec.Path

	// Check if the executable exists. Use LookPath if it's not an absolute
	// path so we match the OS exec behavior.
	if !filepath.IsAbs(execPath) {
		resolved, err := exec.LookPath(execPath)
		if err != nil {
			return nil, runtime.ExecutableNotFoundError{
				Message: fmt.Sprintf("executable %q not found: %s", execPath, err),
			}
		}
		execPath = resolved
	} else {
		if _, err := os.Stat(execPath); os.IsNotExist(err) {
			return nil, runtime.ExecutableNotFoundError{
				Message: fmt.Sprintf("executable %q not found", execPath),
			}
		}
	}

	// Use exec.Command (not CommandContext) because the Run() contract says
	// the context only applies to starting the process, not its lifetime.
	// Process cancellation is handled in Process.Wait() with graceful
	// SIGTERM → SIGKILL.
	cmd := exec.Command(execPath, spec.Args...)

	// Set working directory. ProcessSpec.Dir takes precedence over
	// ContainerSpec.Dir.
	cmd.Dir = c.containerSpec.Dir
	if spec.Dir != "" {
		cmd.Dir = spec.Dir
	}

	// Build environment: ContainerSpec.Env is the base, ProcessSpec.Env
	// overrides.
	cmd.Env = mergeEnv(c.containerSpec.Env, spec.Env)

	// Wire I/O.
	cmd.Stdin = pio.Stdin
	cmd.Stdout = pio.Stdout
	cmd.Stderr = pio.Stderr

	// Start in a new process group so SIGTERM can be sent to the group.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start process: %w", err)
	}

	// Write PID file for orphan cleanup on crash recovery.
	pidFile := filepath.Join(c.containerDir, c.handle+".pid")
	_ = os.WriteFile(pidFile, []byte(fmt.Sprintf("%d", cmd.Process.Pid)), 0644)

	return NewProcess(cmd), nil
}

// Attach is not supported for native containers (MVP).
func (c *Container) Attach(ctx context.Context, processID string, pio runtime.ProcessIO) (runtime.Process, error) {
	return nil, fmt.Errorf("native runner does not support attach")
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

// streamInputs copies remote artifact data into local input volumes.
// inputVolumes is indexed 1:1 with ContainerSpec.Inputs (not the full
// volumes slice, which also contains Dir/Output/Cache/Scratch volumes).
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

		// StreamIn expects a compressed tar stream matching the compression
		// used by StreamOut. Pass the same compression to decompress.
		err = c.inputVolumes[i].StreamIn(ctx, ".", c.compression, 0, reader)
		reader.Close()
		if err != nil {
			return fmt.Errorf("stream in artifact for input %q: %w", input.DestinationPath, err)
		}
	}

	return nil
}

// mergeEnv merges two environment variable slices. Variables in override take
// precedence over those in base. Both are in "NAME=VALUE" format.
func mergeEnv(base, override []string) []string {
	envMap := make(map[string]string, len(base)+len(override))
	var order []string

	for _, env := range base {
		key, _, _ := strings.Cut(env, "=")
		if _, exists := envMap[key]; !exists {
			order = append(order, key)
		}
		envMap[key] = env
	}
	for _, env := range override {
		key, _, _ := strings.Cut(env, "=")
		if _, exists := envMap[key]; !exists {
			order = append(order, key)
		}
		envMap[key] = env
	}

	result := make([]string, 0, len(order))
	for _, key := range order {
		result = append(result, envMap[key])
	}
	return result
}
