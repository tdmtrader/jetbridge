package remote_test

import (
	"bytes"
	"context"
	"io"
	"net"
	"time"

	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/native/agentpb"
	"github.com/concourse/concourse/atc/worker/native/remote"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// mockAgentServer is a gRPC server that returns predetermined responses.
type mockAgentServer struct {
	agentpb.UnimplementedNativeAgentServer
	execFunc      func(*agentpb.ExecRequest, agentpb.NativeAgent_ExecServer) error
	killFunc      func(context.Context, *agentpb.KillRequest) (*agentpb.KillResponse, error)
	pingFunc      func(context.Context, *agentpb.PingRequest) (*agentpb.PingResponse, error)
	streamInFunc  func(agentpb.NativeAgent_StreamInServer) error
	streamOutFunc func(*agentpb.StreamOutRequest, agentpb.NativeAgent_StreamOutServer) error
}

func (m *mockAgentServer) Exec(req *agentpb.ExecRequest, stream agentpb.NativeAgent_ExecServer) error {
	if m.execFunc != nil {
		return m.execFunc(req, stream)
	}
	return nil
}

func (m *mockAgentServer) Kill(ctx context.Context, req *agentpb.KillRequest) (*agentpb.KillResponse, error) {
	if m.killFunc != nil {
		return m.killFunc(ctx, req)
	}
	return &agentpb.KillResponse{}, nil
}

func (m *mockAgentServer) Ping(ctx context.Context, req *agentpb.PingRequest) (*agentpb.PingResponse, error) {
	if m.pingFunc != nil {
		return m.pingFunc(ctx, req)
	}
	return &agentpb.PingResponse{Platform: "darwin", Arch: "arm64", Version: "2.5"}, nil
}

func (m *mockAgentServer) StreamOut(req *agentpb.StreamOutRequest, stream agentpb.NativeAgent_StreamOutServer) error {
	if m.streamOutFunc != nil {
		return m.streamOutFunc(req, stream)
	}
	return nil
}

func (m *mockAgentServer) StreamIn(stream agentpb.NativeAgent_StreamInServer) error {
	if m.streamInFunc != nil {
		return m.streamInFunc(stream)
	}
	// Default: drain all messages and respond.
	for {
		_, err := stream.Recv()
		if err == io.EOF {
			return stream.SendAndClose(&agentpb.StreamInResponse{})
		}
		if err != nil {
			return err
		}
	}
}

// startMockServer starts a gRPC server with the mock agent, returning the
// client, connection, and server for cleanup.
func startMockServer(mock *mockAgentServer) (agentpb.NativeAgentClient, *grpc.ClientConn, *grpc.Server) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	Expect(err).ToNot(HaveOccurred())

	grpcServer := grpc.NewServer()
	agentpb.RegisterNativeAgentServer(grpcServer, mock)
	go grpcServer.Serve(lis)

	conn, err := grpc.NewClient(
		lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	Expect(err).ToNot(HaveOccurred())

	return agentpb.NewNativeAgentClient(conn), conn, grpcServer
}

var _ = Describe("RemoteProcess", func() {
	var (
		client     agentpb.NativeAgentClient
		conn       *grpc.ClientConn
		grpcServer *grpc.Server
		mock       *mockAgentServer
	)

	AfterEach(func() {
		if conn != nil {
			conn.Close()
		}
		if grpcServer != nil {
			grpcServer.Stop()
		}
	})

	Describe("reading stdout and stderr from the stream", func() {
		It("pipes stdout and stderr to ProcessIO writers and returns exit status", func() {
			mock = &mockAgentServer{
				execFunc: func(req *agentpb.ExecRequest, stream agentpb.NativeAgent_ExecServer) error {
					stream.Send(&agentpb.ExecEvent{Event: &agentpb.ExecEvent_Stdout{Stdout: []byte("hello ")}})
					stream.Send(&agentpb.ExecEvent{Event: &agentpb.ExecEvent_Stdout{Stdout: []byte("world\n")}})
					stream.Send(&agentpb.ExecEvent{Event: &agentpb.ExecEvent_Stderr{Stderr: []byte("warn\n")}})
					stream.Send(&agentpb.ExecEvent{Event: &agentpb.ExecEvent_ExitStatus{ExitStatus: 0}})
					return nil
				},
			}
			client, conn, grpcServer = startMockServer(mock)

			var stdout, stderr bytes.Buffer
			pio := runtime.ProcessIO{
				Stdout: &stdout,
				Stderr: &stderr,
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			stream, err := client.Exec(ctx, &agentpb.ExecRequest{Id: "test-1"})
			Expect(err).ToNot(HaveOccurred())

			proc := remote.NewProcess("test-1", stream, pio, client, cancel)

			result, err := proc.Wait(context.Background())
			Expect(err).ToNot(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(0))
			Expect(stdout.String()).To(Equal("hello world\n"))
			Expect(stderr.String()).To(Equal("warn\n"))
		})
	})

	Describe("non-zero exit status", func() {
		It("returns the exit code without error", func() {
			mock = &mockAgentServer{
				execFunc: func(req *agentpb.ExecRequest, stream agentpb.NativeAgent_ExecServer) error {
					stream.Send(&agentpb.ExecEvent{Event: &agentpb.ExecEvent_ExitStatus{ExitStatus: 42}})
					return nil
				},
			}
			client, conn, grpcServer = startMockServer(mock)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			stream, err := client.Exec(ctx, &agentpb.ExecRequest{Id: "test-2"})
			Expect(err).ToNot(HaveOccurred())

			proc := remote.NewProcess("test-2", stream, runtime.ProcessIO{
				Stdout: io.Discard,
				Stderr: io.Discard,
			}, client, cancel)

			result, err := proc.Wait(context.Background())
			Expect(err).ToNot(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(42))
		})
	})

	Describe("error event", func() {
		It("returns an error with exit status -1", func() {
			mock = &mockAgentServer{
				execFunc: func(req *agentpb.ExecRequest, stream agentpb.NativeAgent_ExecServer) error {
					stream.Send(&agentpb.ExecEvent{Event: &agentpb.ExecEvent_Error{Error: "binary not found"}})
					return nil
				},
			}
			client, conn, grpcServer = startMockServer(mock)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			stream, err := client.Exec(ctx, &agentpb.ExecRequest{Id: "test-3"})
			Expect(err).ToNot(HaveOccurred())

			proc := remote.NewProcess("test-3", stream, runtime.ProcessIO{
				Stdout: io.Discard,
				Stderr: io.Discard,
			}, client, cancel)

			result, err := proc.Wait(context.Background())
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("binary not found"))
			Expect(result.ExitStatus).To(Equal(-1))
		})
	})

	Describe("context cancellation triggers Kill", func() {
		It("calls Kill and returns context error", func() {
			killCalled := make(chan string, 1)

			mock = &mockAgentServer{
				execFunc: func(req *agentpb.ExecRequest, stream agentpb.NativeAgent_ExecServer) error {
					// Simulate a long-running process — block until stream context is done.
					<-stream.Context().Done()
					return nil
				},
				killFunc: func(ctx context.Context, req *agentpb.KillRequest) (*agentpb.KillResponse, error) {
					killCalled <- req.Id
					return &agentpb.KillResponse{}, nil
				},
			}
			client, conn, grpcServer = startMockServer(mock)

			streamCtx, streamCancel := context.WithCancel(context.Background())

			stream, err := client.Exec(streamCtx, &agentpb.ExecRequest{Id: "test-cancel"})
			Expect(err).ToNot(HaveOccurred())

			proc := remote.NewProcess("test-cancel", stream, runtime.ProcessIO{
				Stdout: io.Discard,
				Stderr: io.Discard,
			}, client, streamCancel)

			// Cancel the Wait context to trigger Kill.
			waitCtx, waitCancel := context.WithCancel(context.Background())

			done := make(chan struct{})
			var result runtime.ProcessResult
			var waitErr error
			go func() {
				result, waitErr = proc.Wait(waitCtx)
				close(done)
			}()

			time.Sleep(100 * time.Millisecond)
			waitCancel()

			Eventually(done, 5*time.Second).Should(BeClosed())
			Expect(waitErr).To(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(-1))

			Eventually(killCalled, 2*time.Second).Should(Receive(Equal("test-cancel")))
		})
	})

	Describe("ID", func() {
		It("returns the process ID", func() {
			mock = &mockAgentServer{
				execFunc: func(req *agentpb.ExecRequest, stream agentpb.NativeAgent_ExecServer) error {
					stream.Send(&agentpb.ExecEvent{Event: &agentpb.ExecEvent_ExitStatus{ExitStatus: 0}})
					return nil
				},
			}
			client, conn, grpcServer = startMockServer(mock)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			stream, err := client.Exec(ctx, &agentpb.ExecRequest{Id: "my-id"})
			Expect(err).ToNot(HaveOccurred())

			proc := remote.NewProcess("my-id", stream, runtime.ProcessIO{
				Stdout: io.Discard,
				Stderr: io.Discard,
			}, client, cancel)

			Expect(proc.ID()).To(Equal("my-id"))

			proc.Wait(context.Background())
		})
	})

	Describe("SetTTY", func() {
		It("returns an error", func() {
			mock = &mockAgentServer{
				execFunc: func(req *agentpb.ExecRequest, stream agentpb.NativeAgent_ExecServer) error {
					stream.Send(&agentpb.ExecEvent{Event: &agentpb.ExecEvent_ExitStatus{ExitStatus: 0}})
					return nil
				},
			}
			client, conn, grpcServer = startMockServer(mock)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			stream, err := client.Exec(ctx, &agentpb.ExecRequest{Id: "tty-test"})
			Expect(err).ToNot(HaveOccurred())

			proc := remote.NewProcess("tty-test", stream, runtime.ProcessIO{
				Stdout: io.Discard,
				Stderr: io.Discard,
			}, client, cancel)

			err = proc.SetTTY(runtime.TTYSpec{})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("not support TTY"))

			proc.Wait(context.Background())
		})
	})
})
