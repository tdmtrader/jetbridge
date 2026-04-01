package remote_test

import (
	"bytes"
	"context"
	"io"
	"time"

	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/native/agentpb"
	"github.com/concourse/concourse/atc/worker/native/remote"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"google.golang.org/grpc"
)

// noopDelegate satisfies runtime.BuildStepDelegate.
type noopDelegate struct{}

func (d *noopDelegate) BuildStartTime() time.Time { return time.Time{} }

var _ = Describe("RemoteWorker", func() {
	var (
		worker       *remote.Worker
		fakeDBWorker *dbfakes.FakeWorker
		fakeVolRepo  *dbfakes.FakeVolumeRepository
		client       agentpb.NativeAgentClient
		conn         *grpc.ClientConn
		grpcServer   *grpc.Server
		mock         *mockAgentServer
	)

	BeforeEach(func() {
		fakeDBWorker = new(dbfakes.FakeWorker)
		fakeDBWorker.NameReturns("remote-darwin")
		fakeVolRepo = new(dbfakes.FakeVolumeRepository)

		mock = &mockAgentServer{
			execFunc: func(req *agentpb.ExecRequest, stream agentpb.NativeAgent_ExecServer) error {
				stream.Send(&agentpb.ExecEvent{Event: &agentpb.ExecEvent_ExitStatus{ExitStatus: 0}})
				return nil
			},
		}
		client, conn, grpcServer = startMockServer(mock)

		worker = remote.NewWorker(fakeDBWorker, client, fakeVolRepo, compression.NewGzipCompression())
	})

	AfterEach(func() {
		if conn != nil {
			conn.Close()
		}
		if grpcServer != nil {
			grpcServer.Stop()
		}
	})

	Describe("Name", func() {
		It("returns the db worker name", func() {
			Expect(worker.Name()).To(Equal("remote-darwin"))
		})
	})

	Describe("FindOrCreateContainer", func() {
		var (
			fakeCreatingContainer *dbfakes.FakeCreatingContainer
			fakeCreatedContainer  *dbfakes.FakeCreatedContainer
		)

		BeforeEach(func() {
			fakeCreatingContainer = new(dbfakes.FakeCreatingContainer)
			fakeCreatingContainer.HandleReturns("test-handle")
			fakeCreatedContainer = new(dbfakes.FakeCreatedContainer)
			fakeCreatedContainer.HandleReturns("test-handle")
			fakeCreatingContainer.CreatedReturns(fakeCreatedContainer, nil)
			fakeDBWorker.FindContainerReturns(nil, nil, nil)
			fakeDBWorker.CreateContainerReturns(fakeCreatingContainer, nil)
		})

		It("creates a container in the DB and returns a RemoteContainer", func() {
			container, mounts, err := worker.FindOrCreateContainer(
				context.Background(),
				db.NewFixedHandleContainerOwner("test-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					Dir: "/tmp/build/src",
				},
				&noopDelegate{},
			)
			Expect(err).ToNot(HaveOccurred())
			Expect(container).ToNot(BeNil())
			Expect(container.DBContainer()).To(Equal(fakeCreatedContainer))
			Expect(len(mounts)).To(BeNumerically(">", 0))
		})

		It("reuses an existing created container", func() {
			fakeDBWorker.FindContainerReturns(nil, fakeCreatedContainer, nil)

			container, _, err := worker.FindOrCreateContainer(
				context.Background(),
				db.NewFixedHandleContainerOwner("test-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{Dir: "/tmp/build/src"},
				&noopDelegate{},
			)
			Expect(err).ToNot(HaveOccurred())
			Expect(container.DBContainer()).To(Equal(fakeCreatedContainer))
			Expect(fakeDBWorker.CreateContainerCallCount()).To(Equal(0))
		})

		It("builds volume mounts for inputs and outputs", func() {
			_, mounts, err := worker.FindOrCreateContainer(
				context.Background(),
				db.NewFixedHandleContainerOwner("test-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					Dir: "/tmp/build/src",
					Inputs: []runtime.Input{
						{DestinationPath: "/tmp/build/src/input-1"},
						{DestinationPath: "/tmp/build/src/input-2"},
					},
					Outputs: runtime.OutputPaths{
						"result": "/tmp/build/src/result",
					},
				},
				&noopDelegate{},
			)
			Expect(err).ToNot(HaveOccurred())
			// Dir + 2 inputs + 1 output = 4 mounts
			Expect(mounts).To(HaveLen(4))
		})
	})

	Describe("Container.Run", func() {
		It("sends ExecRequest with correct fields and returns a process", func() {
			var capturedReq *agentpb.ExecRequest
			mock.execFunc = func(req *agentpb.ExecRequest, stream agentpb.NativeAgent_ExecServer) error {
				capturedReq = req
				stream.Send(&agentpb.ExecEvent{Event: &agentpb.ExecEvent_ExitStatus{ExitStatus: 0}})
				return nil
			}

			fakeCreatingContainer := new(dbfakes.FakeCreatingContainer)
			fakeCreatingContainer.HandleReturns("exec-handle")
			fakeCreatedContainer := new(dbfakes.FakeCreatedContainer)
			fakeCreatedContainer.HandleReturns("exec-handle")
			fakeCreatingContainer.CreatedReturns(fakeCreatedContainer, nil)
			fakeDBWorker.FindContainerReturns(nil, nil, nil)
			fakeDBWorker.CreateContainerReturns(fakeCreatingContainer, nil)

			container, _, err := worker.FindOrCreateContainer(
				context.Background(),
				db.NewFixedHandleContainerOwner("exec-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					Dir: "/tmp/build/src",
					Env: []string{"PIPELINE_VAR=foo"},
				},
				&noopDelegate{},
			)
			Expect(err).ToNot(HaveOccurred())

			process, err := container.Run(context.Background(), runtime.ProcessSpec{
				Path: "/bin/echo",
				Args: []string{"hello"},
				Env:  []string{"STEP_VAR=bar"},
			}, runtime.ProcessIO{
				Stdout: io.Discard,
				Stderr: io.Discard,
			})
			Expect(err).ToNot(HaveOccurred())

			result, err := process.Wait(context.Background())
			Expect(err).ToNot(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(0))

			Expect(capturedReq).ToNot(BeNil())
			Expect(capturedReq.Id).To(Equal("exec-handle"))
			Expect(capturedReq.Path).To(Equal("/bin/echo"))
			Expect(capturedReq.Args).To(Equal([]string{"hello"}))
			Expect(capturedReq.Env).To(ContainElement("PIPELINE_VAR=foo"))
			Expect(capturedReq.Env).To(ContainElement("STEP_VAR=bar"))
		})
	})

	Describe("Container.Attach", func() {
		It("returns an error", func() {
			fakeCreatingContainer := new(dbfakes.FakeCreatingContainer)
			fakeCreatingContainer.HandleReturns("attach-handle")
			fakeCreatedContainer := new(dbfakes.FakeCreatedContainer)
			fakeCreatedContainer.HandleReturns("attach-handle")
			fakeCreatingContainer.CreatedReturns(fakeCreatedContainer, nil)
			fakeDBWorker.FindContainerReturns(nil, nil, nil)
			fakeDBWorker.CreateContainerReturns(fakeCreatingContainer, nil)

			container, _, err := worker.FindOrCreateContainer(
				context.Background(),
				db.NewFixedHandleContainerOwner("attach-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{Dir: "/tmp/build/src"},
				&noopDelegate{},
			)
			Expect(err).ToNot(HaveOccurred())

			_, err = container.Attach(context.Background(), "some-id", runtime.ProcessIO{})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("not support attach"))
		})
	})

	Describe("Volume.StreamIn", func() {
		It("sends meta and data chunks to the gRPC server", func() {
			var capturedMeta *agentpb.StreamInMeta
			var capturedData []byte

			mock.streamInFunc = func(stream agentpb.NativeAgent_StreamInServer) error {
				for {
					msg, err := stream.Recv()
					if err == io.EOF {
						return stream.SendAndClose(&agentpb.StreamInResponse{})
					}
					if err != nil {
						return err
					}
					if meta := msg.GetMeta(); meta != nil {
						capturedMeta = meta
					}
					if data := msg.GetData(); data != nil {
						capturedData = append(capturedData, data...)
					}
				}
			}

			vol := remote.NewStreamableVolume("test-vol", "remote-darwin", client, "my-container")

			inputData := bytes.Repeat([]byte("x"), 100)
			err := vol.StreamIn(
				context.Background(),
				"my-input",
				compression.NewGzipCompression(),
				0,
				bytes.NewReader(inputData),
			)
			Expect(err).ToNot(HaveOccurred())

			Expect(capturedMeta).ToNot(BeNil())
			Expect(capturedMeta.ContainerId).To(Equal("my-container"))
			Expect(capturedMeta.Path).To(Equal("my-input"))
			Expect(capturedMeta.Encoding).To(Equal("gzip"))
			Expect(capturedData).To(Equal(inputData))
		})

		It("returns error when no gRPC client is configured", func() {
			vol := remote.NewVolume("test-vol", "remote-darwin", nil)

			err := vol.StreamIn(
				context.Background(),
				".",
				compression.NewGzipCompression(),
				0,
				bytes.NewReader([]byte("data")),
			)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("no gRPC client"))
		})
	})

	Describe("Volume.StreamOut", func() {
		It("reads data chunks from the gRPC stream", func() {
			expectedData := []byte("chunk1chunk2chunk3")

			mock.streamOutFunc = func(req *agentpb.StreamOutRequest, stream agentpb.NativeAgent_StreamOutServer) error {
				stream.Send(&agentpb.StreamOutChunk{Data: []byte("chunk1")})
				stream.Send(&agentpb.StreamOutChunk{Data: []byte("chunk2")})
				stream.Send(&agentpb.StreamOutChunk{Data: []byte("chunk3")})
				return nil
			}

			vol := remote.NewStreamableVolume("test-vol", "remote-darwin", client, "my-container")

			reader, err := vol.StreamOut(context.Background(), ".", nil)
			Expect(err).ToNot(HaveOccurred())
			defer reader.Close()

			data, err := io.ReadAll(reader)
			Expect(err).ToNot(HaveOccurred())
			Expect(data).To(Equal(expectedData))
		})

		It("returns error when no gRPC client is configured", func() {
			vol := remote.NewVolume("test-vol", "remote-darwin", nil)

			_, err := vol.StreamOut(context.Background(), ".", nil)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("no gRPC client"))
		})
	})

	Describe("Container.Properties", func() {
		It("stores and retrieves properties", func() {
			fakeCreatingContainer := new(dbfakes.FakeCreatingContainer)
			fakeCreatingContainer.HandleReturns("props-handle")
			fakeCreatedContainer := new(dbfakes.FakeCreatedContainer)
			fakeCreatedContainer.HandleReturns("props-handle")
			fakeCreatingContainer.CreatedReturns(fakeCreatedContainer, nil)
			fakeDBWorker.FindContainerReturns(nil, nil, nil)
			fakeDBWorker.CreateContainerReturns(fakeCreatingContainer, nil)

			container, _, err := worker.FindOrCreateContainer(
				context.Background(),
				db.NewFixedHandleContainerOwner("props-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{Dir: "/tmp/build/src"},
				&noopDelegate{},
			)
			Expect(err).ToNot(HaveOccurred())

			err = container.SetProperty("key", "value")
			Expect(err).ToNot(HaveOccurred())

			props, err := container.Properties()
			Expect(err).ToNot(HaveOccurred())
			Expect(props).To(HaveKeyWithValue("key", "value"))
		})
	})
})
