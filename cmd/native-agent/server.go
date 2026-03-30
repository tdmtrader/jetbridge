package main

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	goruntime "runtime"

	concourse "github.com/concourse/concourse"
	"github.com/concourse/concourse/atc/worker/native/agentpb"
	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/s2"
	"github.com/klauspost/compress/zstd"
)

const (
	streamChunkSize      = 32 * 1024 // 32KB per stdout/stderr chunk
	killGracePeriod      = 10 * time.Second
)

type server struct {
	agentpb.UnimplementedNativeAgentServer
	workDir   string
	cacheDir  string
	processes sync.Map // id → *os.Process
}

// streamEvent is sent from stdout/stderr reader goroutines to the single
// sender goroutine. stream.Send() is not thread-safe so all sends go through
// one goroutine.
type streamEvent struct {
	event *agentpb.ExecEvent
	err   error // non-nil means the reader hit an error
}

func (s *server) Exec(req *agentpb.ExecRequest, stream agentpb.NativeAgent_ExecServer) error {
	containerDir := filepath.Join(s.workDir, "containers", req.Id)
	workDir := filepath.Join(containerDir, "work")
	if err := os.MkdirAll(workDir, 0755); err != nil {
		return sendError(stream, fmt.Sprintf("create work dir: %v", err))
	}

	// Resolve executable.
	execPath := req.Path
	if !filepath.IsAbs(execPath) {
		resolved, err := exec.LookPath(execPath)
		if err != nil {
			return sendError(stream, fmt.Sprintf("executable %q not found: %s", execPath, err))
		}
		execPath = resolved
	} else {
		if _, err := os.Stat(execPath); os.IsNotExist(err) {
			return sendError(stream, fmt.Sprintf("executable %q not found", execPath))
		}
	}

	cmd := exec.Command(execPath, req.Args...)

	// Set working directory.
	cmd.Dir = workDir
	if req.Dir != "" {
		cmd.Dir = filepath.Join(workDir, req.Dir)
	}

	// Build environment: host defaults merged with request env.
	cmd.Env = mergeEnv(hostDefaults(), req.Env)

	// Pipe stdin if provided.
	if len(req.Stdin) > 0 {
		cmd.Stdin = bytes.NewReader(req.Stdin)
	}

	// Set up process group for signal delivery.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return sendError(stream, fmt.Sprintf("stdout pipe: %v", err))
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return sendError(stream, fmt.Sprintf("stderr pipe: %v", err))
	}

	if err := cmd.Start(); err != nil {
		return sendError(stream, fmt.Sprintf("start process: %v", err))
	}

	// Write PID file for crash recovery.
	pidFile := filepath.Join(containerDir, req.Id+".pid")
	_ = os.WriteFile(pidFile, []byte(fmt.Sprintf("%d", cmd.Process.Pid)), 0644)

	// Track process for Kill RPC.
	s.processes.Store(req.Id, cmd.Process)
	defer s.processes.Delete(req.Id)

	// Single sender goroutine: all stream.Send calls go through here.
	events := make(chan streamEvent, 4)
	sendDone := make(chan error, 1)
	go func() {
		var lastErr error
		for ev := range events {
			if ev.err != nil {
				continue // reader error, don't send
			}
			if err := stream.Send(ev.event); err != nil {
				lastErr = err
			}
		}
		sendDone <- lastErr
	}()

	// Read stdout and stderr in parallel, feeding the single sender.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		pipeToEvents(stdout, func(data []byte) *agentpb.ExecEvent {
			return &agentpb.ExecEvent{Event: &agentpb.ExecEvent_Stdout{Stdout: data}}
		}, events)
	}()
	go func() {
		defer wg.Done()
		pipeToEvents(stderr, func(data []byte) *agentpb.ExecEvent {
			return &agentpb.ExecEvent{Event: &agentpb.ExecEvent_Stderr{Stderr: data}}
		}, events)
	}()

	// Wait for both pipes to drain. Use a goroutine + channel so we can
	// also handle stream context cancellation (client disconnect).
	pipesDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(pipesDone)
	}()

	// If the stream context is cancelled (client disconnects or calls Kill
	// then cancels), we must kill the process so the pipe readers unblock.
	select {
	case <-pipesDone:
		// Pipes drained normally — process is exiting or has exited.
	case <-stream.Context().Done():
		// Client disconnected. Kill the process to unblock pipe readers.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-pipesDone
		close(events)
		<-sendDone
		return nil
	}

	// Wait for process exit.
	waitErr := cmd.Wait()

	exitCode := int32(0)
	if waitErr != nil {
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			exitCode = int32(exitErr.ExitCode())
		} else {
			// Unexpected error — send as error event.
			events <- streamEvent{event: &agentpb.ExecEvent{
				Event: &agentpb.ExecEvent_Error{Error: waitErr.Error()},
			}}
			close(events)
			<-sendDone
			return nil
		}
	}

	events <- streamEvent{event: &agentpb.ExecEvent{
		Event: &agentpb.ExecEvent_ExitStatus{ExitStatus: exitCode},
	}}
	close(events)
	<-sendDone

	return nil
}

func (s *server) Kill(ctx context.Context, req *agentpb.KillRequest) (*agentpb.KillResponse, error) {
	val, ok := s.processes.Load(req.Id)
	if !ok {
		// Process already exited — idempotent.
		return &agentpb.KillResponse{}, nil
	}
	proc := val.(*os.Process)

	// SIGTERM to process group.
	_ = syscall.Kill(-proc.Pid, syscall.SIGTERM)

	// Wait for grace period, then SIGKILL if still alive.
	go func() {
		time.Sleep(killGracePeriod)
		// Check if still tracked (might have exited during grace period).
		if _, ok := s.processes.Load(req.Id); ok {
			_ = syscall.Kill(-proc.Pid, syscall.SIGKILL)
		}
	}()

	return &agentpb.KillResponse{}, nil
}

func (s *server) Ping(ctx context.Context, req *agentpb.PingRequest) (*agentpb.PingResponse, error) {
	return &agentpb.PingResponse{
		Platform: goruntime.GOOS,
		Arch:     goruntime.GOARCH,
		Version:  concourse.WorkerVersion,
	}, nil
}

func (s *server) StreamIn(stream agentpb.NativeAgent_StreamInServer) error {
	// First message must be meta.
	msg, err := stream.Recv()
	if err != nil {
		return fmt.Errorf("stream in: recv meta: %w", err)
	}
	meta := msg.GetMeta()
	if meta == nil {
		return fmt.Errorf("stream in: first message must be meta, got data")
	}

	if meta.ContainerId == "" {
		return fmt.Errorf("stream in: container_id is required")
	}

	targetDir := filepath.Join(s.workDir, "containers", meta.ContainerId, "work")
	if meta.Path != "" && meta.Path != "." {
		targetDir = filepath.Join(targetDir, meta.Path)
	}
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return fmt.Errorf("stream in: create target dir: %w", err)
	}

	// Wrap remaining stream messages as an io.Reader.
	reader := &grpcStreamReader{stream: stream}

	// Decompress if needed.
	var actualReader io.Reader = reader
	if meta.Encoding != "" && meta.Encoding != "raw" {
		decompressed, err := newDecompressReader(reader, meta.Encoding)
		if err != nil {
			return fmt.Errorf("stream in: create decompressor: %w", err)
		}
		defer decompressed.Close()
		actualReader = decompressed
	}

	// Extract tar.
	if err := extractTar(actualReader, targetDir); err != nil {
		return err
	}

	return stream.SendAndClose(&agentpb.StreamInResponse{})
}

// extractTar extracts a tar stream to the target directory. Logic matches
// native/volume.go:92-141.
func extractTar(r io.Reader, targetDir string) error {
	tr := tar.NewReader(r)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("stream in: read tar header: %w", err)
		}

		cleanName := filepath.Clean(header.Name)
		if strings.HasPrefix(cleanName, "..") {
			return fmt.Errorf("stream in: invalid tar path %q", header.Name)
		}
		target := filepath.Join(targetDir, cleanName)

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(header.Mode)); err != nil {
				return fmt.Errorf("stream in: create dir %q: %w", cleanName, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return fmt.Errorf("stream in: create parent dir for %q: %w", cleanName, err)
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return fmt.Errorf("stream in: create file %q: %w", cleanName, err)
			}
			_, copyErr := io.Copy(f, tr)
			f.Close()
			if copyErr != nil {
				return fmt.Errorf("stream in: write file %q: %w", cleanName, copyErr)
			}
		case tar.TypeSymlink:
			if err := os.Symlink(header.Linkname, target); err != nil {
				return fmt.Errorf("stream in: create symlink %q: %w", cleanName, err)
			}
		case tar.TypeLink:
			linkTarget := filepath.Join(targetDir, filepath.Clean(header.Linkname))
			if err := os.Link(linkTarget, target); err != nil {
				return fmt.Errorf("stream in: create hard link %q: %w", cleanName, err)
			}
		}
	}
	return nil
}

// grpcStreamReader wraps a NativeAgent_StreamInServer as an io.Reader.
// It reads Data chunks from the stream, returning io.EOF when the client
// closes the send direction.
type grpcStreamReader struct {
	stream agentpb.NativeAgent_StreamInServer
	buf    []byte // leftover from previous Recv
}

func (r *grpcStreamReader) Read(p []byte) (int, error) {
	if len(r.buf) > 0 {
		n := copy(p, r.buf)
		r.buf = r.buf[n:]
		return n, nil
	}

	msg, err := r.stream.Recv()
	if err != nil {
		return 0, err // io.EOF when client closes
	}

	data := msg.GetData()
	if data == nil {
		// Unexpected meta message after the first — skip it.
		return 0, nil
	}

	n := copy(p, data)
	if n < len(data) {
		r.buf = data[n:]
	}
	return n, nil
}

// newDecompressReader creates a decompressing reader for the given encoding
// string. The agent does not import the atc/compression package — it maps
// encoding strings to decompressors directly.
func newDecompressReader(r io.Reader, encoding string) (io.ReadCloser, error) {
	switch encoding {
	case "gzip":
		return gzip.NewReader(r)
	case "zstd":
		dec, err := zstd.NewReader(r)
		if err != nil {
			return nil, err
		}
		return dec.IOReadCloser(), nil
	case "s2":
		return io.NopCloser(s2.NewReader(r)), nil
	default:
		return nil, fmt.Errorf("unsupported encoding %q", encoding)
	}
}

func (s *server) StreamOut(req *agentpb.StreamOutRequest, stream agentpb.NativeAgent_StreamOutServer) error {
	if req.ContainerId == "" {
		return fmt.Errorf("stream out: container_id is required")
	}

	sourcePath := filepath.Join(s.workDir, "containers", req.ContainerId, "work")
	if req.Path != "" && req.Path != "." {
		sourcePath = filepath.Join(sourcePath, req.Path)
	}

	info, err := os.Lstat(sourcePath)
	if err != nil {
		return fmt.Errorf("stream out: stat %q: %w", sourcePath, err)
	}

	writer := &grpcStreamWriter{stream: stream}

	var tarDest io.Writer = writer
	var compressor io.WriteCloser
	if req.Encoding != "" && req.Encoding != "raw" {
		compressor = newCompressWriter(writer, req.Encoding)
		tarDest = compressor
	}

	tw := tar.NewWriter(tarDest)

	var walkErr error
	if info.IsDir() {
		walkErr = filepath.Walk(sourcePath, func(filePath string, fi os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			return addToTar(tw, sourcePath, filePath, fi)
		})
	} else {
		walkErr = addToTar(tw, filepath.Dir(sourcePath), sourcePath, info)
	}

	if closeErr := tw.Close(); closeErr != nil && walkErr == nil {
		walkErr = closeErr
	}
	if compressor != nil {
		if closeErr := compressor.Close(); closeErr != nil && walkErr == nil {
			walkErr = closeErr
		}
	}

	return walkErr
}

// grpcStreamWriter wraps a NativeAgent_StreamOutServer as an io.Writer.
type grpcStreamWriter struct {
	stream agentpb.NativeAgent_StreamOutServer
}

func (w *grpcStreamWriter) Write(p []byte) (int, error) {
	chunk := make([]byte, len(p))
	copy(chunk, p)
	if err := w.stream.Send(&agentpb.StreamOutChunk{Data: chunk}); err != nil {
		return 0, err
	}
	return len(p), nil
}

// newCompressWriter creates a compressing io.WriteCloser for the given encoding
// string. The agent does not import atc/compression — it maps strings directly.
func newCompressWriter(w io.Writer, encoding string) io.WriteCloser {
	switch encoding {
	case "zstd":
		enc, err := zstd.NewWriter(w)
		if err != nil {
			// zstd.NewWriter only errors on invalid options; safe to panic.
			panic(fmt.Sprintf("zstd.NewWriter: %v", err))
		}
		return enc
	case "s2":
		return s2.NewWriter(w)
	default:
		return gzip.NewWriter(w)
	}
}

// addToTar adds a single file, directory, or symlink entry to a tar writer.
// Paths in the archive are relative to baseDir.
func addToTar(tw *tar.Writer, baseDir, filePath string, fi os.FileInfo) error {
	relPath, err := filepath.Rel(baseDir, filePath)
	if err != nil {
		return err
	}

	var link string
	if fi.Mode()&os.ModeSymlink != 0 {
		link, err = os.Readlink(filePath)
		if err != nil {
			return err
		}
	}

	header, err := tar.FileInfoHeader(fi, link)
	if err != nil {
		return err
	}
	header.Name = relPath

	if err := tw.WriteHeader(header); err != nil {
		return err
	}

	if fi.Mode().IsRegular() {
		f, err := os.Open(filePath)
		if err != nil {
			return err
		}
		defer f.Close()
		if _, err := io.Copy(tw, f); err != nil {
			return err
		}
	}

	return nil
}

// pipeToEvents reads from r in chunks and sends events to the channel.
func pipeToEvents(r io.Reader, makeEvent func([]byte) *agentpb.ExecEvent, out chan<- streamEvent) {
	buf := make([]byte, streamChunkSize)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			data := make([]byte, n)
			copy(data, buf[:n])
			out <- streamEvent{event: makeEvent(data)}
		}
		if err != nil {
			return
		}
	}
}

// sendError sends an error event on the stream and returns nil (the RPC
// itself succeeds; the error is conveyed as an ExecEvent).
func sendError(stream agentpb.NativeAgent_ExecServer, msg string) error {
	_ = stream.Send(&agentpb.ExecEvent{
		Event: &agentpb.ExecEvent_Error{Error: msg},
	})
	return nil
}

// hostDefaults returns the host environment variables that native processes
// need to find executables and run correctly.
func hostDefaults() []string {
	var env []string
	for _, key := range []string{"HOME", "USER", "PATH", "SHELL", "LANG", "TERM"} {
		if val, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+val)
		}
	}
	return env
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
