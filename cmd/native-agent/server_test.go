package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	goruntime "runtime"
	"time"

	concourse "github.com/concourse/concourse"
	"github.com/concourse/concourse/atc/worker/native/agentpb"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"google.golang.org/grpc"
	grpccreds "google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/codes"
)

var _ = Describe("NativeAgent Server", func() {
	var (
		client     agentpb.NativeAgentClient
		conn       *grpc.ClientConn
		grpcServer *grpc.Server
		workDir    string
	)

	BeforeEach(func() {
		workDir = GinkgoT().TempDir()

		lis, err := net.Listen("tcp", "127.0.0.1:0")
		Expect(err).ToNot(HaveOccurred())

		grpcServer = grpc.NewServer()
		agentpb.RegisterNativeAgentServer(grpcServer, &server{
			workDir:  workDir,
			cacheDir: filepath.Join(workDir, "caches"),
		})

		go grpcServer.Serve(lis)

		conn, err = grpc.NewClient(
			lis.Addr().String(),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
		Expect(err).ToNot(HaveOccurred())

		client = agentpb.NewNativeAgentClient(conn)
	})

	AfterEach(func() {
		if conn != nil {
			conn.Close()
		}
		if grpcServer != nil {
			grpcServer.Stop()
		}
	})

	Describe("Ping", func() {
		It("returns the correct platform, arch, and version", func() {
			resp, err := client.Ping(context.Background(), &agentpb.PingRequest{})
			Expect(err).ToNot(HaveOccurred())
			Expect(resp.Platform).To(Equal(goruntime.GOOS))
			Expect(resp.Arch).To(Equal(goruntime.GOARCH))
			Expect(resp.Version).To(Equal(concourse.WorkerVersion))
		})
	})

	Describe("Exec", func() {
		It("streams stdout and returns exit status 0", func() {
			stream, err := client.Exec(context.Background(), &agentpb.ExecRequest{
				Id:   "test-echo",
				Path: "/bin/echo",
				Args: []string{"hello world"},
			})
			Expect(err).ToNot(HaveOccurred())

			var stdout []byte
			var exitStatus int32
			gotExit := false

			for {
				event, err := stream.Recv()
				if err == io.EOF {
					break
				}
				Expect(err).ToNot(HaveOccurred())

				switch ev := event.Event.(type) {
				case *agentpb.ExecEvent_Stdout:
					stdout = append(stdout, ev.Stdout...)
				case *agentpb.ExecEvent_ExitStatus:
					exitStatus = ev.ExitStatus
					gotExit = true
				case *agentpb.ExecEvent_Error:
					Fail("unexpected error event: " + ev.Error)
				}
			}

			Expect(gotExit).To(BeTrue())
			Expect(exitStatus).To(Equal(int32(0)))
			Expect(string(stdout)).To(ContainSubstring("hello world"))
		})

		It("streams stderr", func() {
			stream, err := client.Exec(context.Background(), &agentpb.ExecRequest{
				Id:   "test-stderr",
				Path: "/bin/sh",
				Args: []string{"-c", "echo error-output >&2"},
			})
			Expect(err).ToNot(HaveOccurred())

			var stderr []byte
			for {
				event, err := stream.Recv()
				if err == io.EOF {
					break
				}
				Expect(err).ToNot(HaveOccurred())

				if ev, ok := event.Event.(*agentpb.ExecEvent_Stderr); ok {
					stderr = append(stderr, ev.Stderr...)
				}
			}

			Expect(string(stderr)).To(ContainSubstring("error-output"))
		})

		It("returns non-zero exit status", func() {
			stream, err := client.Exec(context.Background(), &agentpb.ExecRequest{
				Id:   "test-exit-42",
				Path: "/bin/sh",
				Args: []string{"-c", "exit 42"},
			})
			Expect(err).ToNot(HaveOccurred())

			var exitStatus int32
			for {
				event, err := stream.Recv()
				if err == io.EOF {
					break
				}
				Expect(err).ToNot(HaveOccurred())

				if ev, ok := event.Event.(*agentpb.ExecEvent_ExitStatus); ok {
					exitStatus = ev.ExitStatus
				}
			}

			Expect(exitStatus).To(Equal(int32(42)))
		})

		It("sends error event for nonexistent binary", func() {
			stream, err := client.Exec(context.Background(), &agentpb.ExecRequest{
				Id:   "test-notfound",
				Path: "definitely-not-a-real-binary",
			})
			Expect(err).ToNot(HaveOccurred())

			var errorMsg string
			for {
				event, err := stream.Recv()
				if err == io.EOF {
					break
				}
				Expect(err).ToNot(HaveOccurred())

				if ev, ok := event.Event.(*agentpb.ExecEvent_Error); ok {
					errorMsg = ev.Error
				}
			}

			Expect(errorMsg).To(ContainSubstring("not found"))
		})

		It("creates the container work directory", func() {
			stream, err := client.Exec(context.Background(), &agentpb.ExecRequest{
				Id:   "test-workdir",
				Path: "/bin/pwd",
			})
			Expect(err).ToNot(HaveOccurred())

			var stdout []byte
			for {
				event, err := stream.Recv()
				if err == io.EOF {
					break
				}
				Expect(err).ToNot(HaveOccurred())

				if ev, ok := event.Event.(*agentpb.ExecEvent_Stdout); ok {
					stdout = append(stdout, ev.Stdout...)
				}
			}

			expectedDir := filepath.Join(workDir, "containers", "test-workdir", "work")
			Expect(string(stdout)).To(ContainSubstring(expectedDir))
		})

		It("merges request env with host defaults", func() {
			stream, err := client.Exec(context.Background(), &agentpb.ExecRequest{
				Id:   "test-env",
				Path: "/bin/sh",
				Args: []string{"-c", "echo MY_VAR=$MY_VAR"},
				Env:  []string{"MY_VAR=hello"},
			})
			Expect(err).ToNot(HaveOccurred())

			var stdout []byte
			for {
				event, err := stream.Recv()
				if err == io.EOF {
					break
				}
				Expect(err).ToNot(HaveOccurred())

				if ev, ok := event.Event.(*agentpb.ExecEvent_Stdout); ok {
					stdout = append(stdout, ev.Stdout...)
				}
			}

			Expect(string(stdout)).To(ContainSubstring("MY_VAR=hello"))
		})

		It("does not remove the container dir after process exits", func() {
			stream, err := client.Exec(context.Background(), &agentpb.ExecRequest{
				Id:   "test-persist",
				Path: "/bin/sh",
				Args: []string{"-c", "echo ok"},
			})
			Expect(err).ToNot(HaveOccurred())

			for {
				_, err := stream.Recv()
				if err == io.EOF {
					break
				}
				Expect(err).ToNot(HaveOccurred())
			}

			containerDir := filepath.Join(workDir, "containers", "test-persist")
			Expect(containerDir).To(BeADirectory())
		})
	})

	Describe("Kill", func() {
		It("terminates a running process", func() {
			stream, err := client.Exec(context.Background(), &agentpb.ExecRequest{
				Id:   "test-kill",
				Path: "/bin/sleep",
				Args: []string{"300"},
			})
			Expect(err).ToNot(HaveOccurred())

			// Give the process a moment to start.
			time.Sleep(200 * time.Millisecond)

			_, err = client.Kill(context.Background(), &agentpb.KillRequest{Id: "test-kill"})
			Expect(err).ToNot(HaveOccurred())

			// The stream should end with an exit status.
			gotExit := false
			for {
				event, err := stream.Recv()
				if err == io.EOF {
					break
				}
				if err != nil {
					break
				}
				if _, ok := event.Event.(*agentpb.ExecEvent_ExitStatus); ok {
					gotExit = true
				}
			}

			Expect(gotExit).To(BeTrue())
		})

		It("is idempotent for unknown process", func() {
			_, err := client.Kill(context.Background(), &agentpb.KillRequest{Id: "nonexistent"})
			Expect(err).ToNot(HaveOccurred())
		})
	})

	Describe("StreamIn", func() {
		It("extracts a gzip-compressed tar to the container work dir", func() {
			// Create the container work dir (normally done by Exec).
			containerID := "stream-in-test"
			containerWorkDir := filepath.Join(workDir, "containers", containerID, "work")
			Expect(os.MkdirAll(containerWorkDir, 0755)).To(Succeed())

			// Build a gzip-compressed tar with one file.
			tarData := createGzipTar(map[string]string{
				"hello.txt": "hello world\n",
			})

			// Send via StreamIn.
			stream, err := client.StreamIn(context.Background())
			Expect(err).ToNot(HaveOccurred())

			// First message: meta.
			err = stream.Send(&agentpb.StreamInMessage{
				Message: &agentpb.StreamInMessage_Meta{
					Meta: &agentpb.StreamInMeta{
						ContainerId: containerID,
						Path:        ".",
						Encoding:    "gzip",
					},
				},
			})
			Expect(err).ToNot(HaveOccurred())

			// Send data in chunks.
			for i := 0; i < len(tarData); i += 1024 {
				end := i + 1024
				if end > len(tarData) {
					end = len(tarData)
				}
				err = stream.Send(&agentpb.StreamInMessage{
					Message: &agentpb.StreamInMessage_Data{
						Data: tarData[i:end],
					},
				})
				Expect(err).ToNot(HaveOccurred())
			}

			_, err = stream.CloseAndRecv()
			Expect(err).ToNot(HaveOccurred())

			// Verify file was extracted.
			content, err := os.ReadFile(filepath.Join(containerWorkDir, "hello.txt"))
			Expect(err).ToNot(HaveOccurred())
			Expect(string(content)).To(Equal("hello world\n"))
		})

		It("extracts an uncompressed tar when encoding is raw", func() {
			containerID := "stream-in-raw"
			containerWorkDir := filepath.Join(workDir, "containers", containerID, "work")
			Expect(os.MkdirAll(containerWorkDir, 0755)).To(Succeed())

			tarData := createRawTar(map[string]string{
				"raw-file.txt": "raw content\n",
			})

			stream, err := client.StreamIn(context.Background())
			Expect(err).ToNot(HaveOccurred())

			err = stream.Send(&agentpb.StreamInMessage{
				Message: &agentpb.StreamInMessage_Meta{
					Meta: &agentpb.StreamInMeta{
						ContainerId: containerID,
						Path:        ".",
						Encoding:    "raw",
					},
				},
			})
			Expect(err).ToNot(HaveOccurred())

			err = stream.Send(&agentpb.StreamInMessage{
				Message: &agentpb.StreamInMessage_Data{Data: tarData},
			})
			Expect(err).ToNot(HaveOccurred())

			_, err = stream.CloseAndRecv()
			Expect(err).ToNot(HaveOccurred())

			content, err := os.ReadFile(filepath.Join(containerWorkDir, "raw-file.txt"))
			Expect(err).ToNot(HaveOccurred())
			Expect(string(content)).To(Equal("raw content\n"))
		})

		It("extracts to a subdirectory when path is set", func() {
			containerID := "stream-in-subdir"
			containerWorkDir := filepath.Join(workDir, "containers", containerID, "work")
			Expect(os.MkdirAll(containerWorkDir, 0755)).To(Succeed())

			tarData := createRawTar(map[string]string{
				"subfile.txt": "sub content\n",
			})

			stream, err := client.StreamIn(context.Background())
			Expect(err).ToNot(HaveOccurred())

			err = stream.Send(&agentpb.StreamInMessage{
				Message: &agentpb.StreamInMessage_Meta{
					Meta: &agentpb.StreamInMeta{
						ContainerId: containerID,
						Path:        "my-input",
						Encoding:    "raw",
					},
				},
			})
			Expect(err).ToNot(HaveOccurred())

			err = stream.Send(&agentpb.StreamInMessage{
				Message: &agentpb.StreamInMessage_Data{Data: tarData},
			})
			Expect(err).ToNot(HaveOccurred())

			_, err = stream.CloseAndRecv()
			Expect(err).ToNot(HaveOccurred())

			content, err := os.ReadFile(filepath.Join(containerWorkDir, "my-input", "subfile.txt"))
			Expect(err).ToNot(HaveOccurred())
			Expect(string(content)).To(Equal("sub content\n"))
		})

		It("rejects directory traversal paths in tar", func() {
			containerID := "stream-in-traversal"
			containerWorkDir := filepath.Join(workDir, "containers", containerID, "work")
			Expect(os.MkdirAll(containerWorkDir, 0755)).To(Succeed())

			// Create a tar with a path traversal entry.
			var buf bytes.Buffer
			tw := tar.NewWriter(&buf)
			tw.WriteHeader(&tar.Header{
				Name: "../../../etc/evil",
				Mode: 0644,
				Size: 4,
			})
			tw.Write([]byte("evil"))
			tw.Close()

			stream, err := client.StreamIn(context.Background())
			Expect(err).ToNot(HaveOccurred())

			err = stream.Send(&agentpb.StreamInMessage{
				Message: &agentpb.StreamInMessage_Meta{
					Meta: &agentpb.StreamInMeta{
						ContainerId: containerID,
						Path:        ".",
						Encoding:    "raw",
					},
				},
			})
			Expect(err).ToNot(HaveOccurred())

			err = stream.Send(&agentpb.StreamInMessage{
				Message: &agentpb.StreamInMessage_Data{Data: buf.Bytes()},
			})
			Expect(err).ToNot(HaveOccurred())

			_, err = stream.CloseAndRecv()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("invalid tar path"))
		})

		It("returns error when first message is data instead of meta", func() {
			stream, err := client.StreamIn(context.Background())
			Expect(err).ToNot(HaveOccurred())

			err = stream.Send(&agentpb.StreamInMessage{
				Message: &agentpb.StreamInMessage_Data{Data: []byte("unexpected")},
			})
			Expect(err).ToNot(HaveOccurred())

			_, err = stream.CloseAndRecv()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("first message must be meta"))
		})
	})

	Describe("StreamOut", func() {
		It("streams a gzip-compressed tar of the container work dir", func() {
			containerID := "stream-out-test"
			containerWorkDir := filepath.Join(workDir, "containers", containerID, "work")
			Expect(os.MkdirAll(containerWorkDir, 0755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(containerWorkDir, "output.txt"), []byte("built binary\n"), 0644)).To(Succeed())
			Expect(os.MkdirAll(filepath.Join(containerWorkDir, "subdir"), 0755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(containerWorkDir, "subdir", "nested.txt"), []byte("nested\n"), 0644)).To(Succeed())

			stream, err := client.StreamOut(context.Background(), &agentpb.StreamOutRequest{
				ContainerId: containerID,
				Path:        ".",
				Encoding:    "gzip",
			})
			Expect(err).ToNot(HaveOccurred())

			var data []byte
			for {
				chunk, err := stream.Recv()
				if err == io.EOF {
					break
				}
				Expect(err).ToNot(HaveOccurred())
				data = append(data, chunk.Data...)
			}

			// Decompress and untar.
			gr, err := gzip.NewReader(bytes.NewReader(data))
			Expect(err).ToNot(HaveOccurred())
			defer gr.Close()

			files := make(map[string]string)
			tr := tar.NewReader(gr)
			for {
				hdr, err := tr.Next()
				if err == io.EOF {
					break
				}
				Expect(err).ToNot(HaveOccurred())
				if hdr.Typeflag == tar.TypeReg {
					content, err := io.ReadAll(tr)
					Expect(err).ToNot(HaveOccurred())
					files[hdr.Name] = string(content)
				}
			}

			Expect(files).To(HaveKeyWithValue("output.txt", "built binary\n"))
			Expect(files).To(HaveKeyWithValue(filepath.Join("subdir", "nested.txt"), "nested\n"))
		})

		It("streams an uncompressed tar when encoding is raw", func() {
			containerID := "stream-out-raw"
			containerWorkDir := filepath.Join(workDir, "containers", containerID, "work")
			Expect(os.MkdirAll(containerWorkDir, 0755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(containerWorkDir, "raw.txt"), []byte("raw data\n"), 0644)).To(Succeed())

			stream, err := client.StreamOut(context.Background(), &agentpb.StreamOutRequest{
				ContainerId: containerID,
				Path:        ".",
				Encoding:    "raw",
			})
			Expect(err).ToNot(HaveOccurred())

			var data []byte
			for {
				chunk, err := stream.Recv()
				if err == io.EOF {
					break
				}
				Expect(err).ToNot(HaveOccurred())
				data = append(data, chunk.Data...)
			}

			files := make(map[string]string)
			tr := tar.NewReader(bytes.NewReader(data))
			for {
				hdr, err := tr.Next()
				if err == io.EOF {
					break
				}
				Expect(err).ToNot(HaveOccurred())
				if hdr.Typeflag == tar.TypeReg {
					content, err := io.ReadAll(tr)
					Expect(err).ToNot(HaveOccurred())
					files[hdr.Name] = string(content)
				}
			}

			Expect(files).To(HaveKeyWithValue("raw.txt", "raw data\n"))
		})

		It("returns error for nonexistent path", func() {
			containerID := "stream-out-notfound"
			containerWorkDir := filepath.Join(workDir, "containers", containerID, "work")
			Expect(os.MkdirAll(containerWorkDir, 0755)).To(Succeed())

			stream, err := client.StreamOut(context.Background(), &agentpb.StreamOutRequest{
				ContainerId: containerID,
				Path:        "does-not-exist",
				Encoding:    "gzip",
			})
			Expect(err).ToNot(HaveOccurred())

			_, err = stream.Recv()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("stat"))
		})

		It("streams a single file", func() {
			containerID := "stream-out-single"
			containerWorkDir := filepath.Join(workDir, "containers", containerID, "work")
			Expect(os.MkdirAll(containerWorkDir, 0755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(containerWorkDir, "single.txt"), []byte("one file\n"), 0644)).To(Succeed())

			stream, err := client.StreamOut(context.Background(), &agentpb.StreamOutRequest{
				ContainerId: containerID,
				Path:        "single.txt",
				Encoding:    "raw",
			})
			Expect(err).ToNot(HaveOccurred())

			var data []byte
			for {
				chunk, err := stream.Recv()
				if err == io.EOF {
					break
				}
				Expect(err).ToNot(HaveOccurred())
				data = append(data, chunk.Data...)
			}

			files := make(map[string]string)
			tr := tar.NewReader(bytes.NewReader(data))
			for {
				hdr, err := tr.Next()
				if err == io.EOF {
					break
				}
				Expect(err).ToNot(HaveOccurred())
				if hdr.Typeflag == tar.TypeReg {
					content, err := io.ReadAll(tr)
					Expect(err).ToNot(HaveOccurred())
					files[hdr.Name] = string(content)
				}
			}

			Expect(files).To(HaveKeyWithValue("single.txt", "one file\n"))
		})
	})

	Describe("startupSweep", func() {
		It("cleans up stale container directories", func() {
			containerDir := filepath.Join(workDir, "containers", "stale-handle")
			Expect(os.MkdirAll(containerDir, 0755)).To(Succeed())
			Expect(os.WriteFile(
				filepath.Join(containerDir, "stale-handle.pid"),
				[]byte("99999999"),
				0644,
			)).To(Succeed())

			startupSweep(workDir)

			Expect(containerDir).ToNot(BeADirectory())
		})

		It("does nothing when containers dir does not exist", func() {
			startupSweep(filepath.Join(GinkgoT().TempDir(), "nonexistent"))
		})
	})

	Describe("Token Auth", func() {
		const serverToken = "test-secret-token"

		It("rejects requests with no token", func() {
			authClient, authConn, authServer := startServerWithAuth(workDir,
				[]grpc.ServerOption{
					grpc.ChainUnaryInterceptor(tokenAuthUnaryInterceptor(serverToken)),
					grpc.ChainStreamInterceptor(tokenAuthStreamInterceptor(serverToken)),
				},
				[]grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())},
			)
			defer authConn.Close()
			defer authServer.Stop()

			_, err := authClient.Ping(context.Background(), &agentpb.PingRequest{})
			Expect(err).To(HaveOccurred())
			Expect(status.Code(err)).To(Equal(codes.Unauthenticated))
		})

		It("rejects requests with wrong token", func() {
			authClient, authConn, authServer := startServerWithAuth(workDir,
				[]grpc.ServerOption{
					grpc.ChainUnaryInterceptor(tokenAuthUnaryInterceptor(serverToken)),
					grpc.ChainStreamInterceptor(tokenAuthStreamInterceptor(serverToken)),
				},
				[]grpc.DialOption{
					grpc.WithTransportCredentials(insecure.NewCredentials()),
					grpc.WithPerRPCCredentials(testTokenCredentials{token: "wrong-token"}),
				},
			)
			defer authConn.Close()
			defer authServer.Stop()

			_, err := authClient.Ping(context.Background(), &agentpb.PingRequest{})
			Expect(err).To(HaveOccurred())
			Expect(status.Code(err)).To(Equal(codes.Unauthenticated))
		})

		It("accepts requests with correct token", func() {
			authClient, authConn, authServer := startServerWithAuth(workDir,
				[]grpc.ServerOption{
					grpc.ChainUnaryInterceptor(tokenAuthUnaryInterceptor(serverToken)),
					grpc.ChainStreamInterceptor(tokenAuthStreamInterceptor(serverToken)),
				},
				[]grpc.DialOption{
					grpc.WithTransportCredentials(insecure.NewCredentials()),
					grpc.WithPerRPCCredentials(testTokenCredentials{token: serverToken}),
				},
			)
			defer authConn.Close()
			defer authServer.Stop()

			resp, err := authClient.Ping(context.Background(), &agentpb.PingRequest{})
			Expect(err).ToNot(HaveOccurred())
			Expect(resp.Platform).To(Equal(goruntime.GOOS))
		})

		It("rejects streaming RPCs with wrong token", func() {
			authClient, authConn, authServer := startServerWithAuth(workDir,
				[]grpc.ServerOption{
					grpc.ChainUnaryInterceptor(tokenAuthUnaryInterceptor(serverToken)),
					grpc.ChainStreamInterceptor(tokenAuthStreamInterceptor(serverToken)),
				},
				[]grpc.DialOption{
					grpc.WithTransportCredentials(insecure.NewCredentials()),
					grpc.WithPerRPCCredentials(testTokenCredentials{token: "wrong"}),
				},
			)
			defer authConn.Close()
			defer authServer.Stop()

			stream, err := authClient.Exec(context.Background(), &agentpb.ExecRequest{
				Id:   "auth-test",
				Path: "/bin/echo",
				Args: []string{"hello"},
			})
			Expect(err).ToNot(HaveOccurred())

			_, err = stream.Recv()
			Expect(err).To(HaveOccurred())
			Expect(status.Code(err)).To(Equal(codes.Unauthenticated))
		})
	})

	Describe("mTLS Auth", func() {
		It("accepts requests with valid client certificate", func() {
			tmpDir := GinkgoT().TempDir()
			caCertFile, serverCertFile, serverKeyFile, clientCertFile, clientKeyFile := generateTestCerts(tmpDir)

			serverCreds, err := loadServerTLS(serverCertFile, serverKeyFile, caCertFile)
			Expect(err).ToNot(HaveOccurred())

			clientCert, err := tls.LoadX509KeyPair(clientCertFile, clientKeyFile)
			Expect(err).ToNot(HaveOccurred())
			caBytes, err := os.ReadFile(caCertFile)
			Expect(err).ToNot(HaveOccurred())
			caPool := x509.NewCertPool()
			Expect(caPool.AppendCertsFromPEM(caBytes)).To(BeTrue())

			authClient, authConn, authServer := startServerWithAuth(workDir,
				[]grpc.ServerOption{serverCreds},
				[]grpc.DialOption{
					grpc.WithTransportCredentials(grpccreds.NewTLS(&tls.Config{
						Certificates: []tls.Certificate{clientCert},
						RootCAs:      caPool,
					})),
				},
			)
			defer authConn.Close()
			defer authServer.Stop()

			resp, err := authClient.Ping(context.Background(), &agentpb.PingRequest{})
			Expect(err).ToNot(HaveOccurred())
			Expect(resp.Platform).To(Equal(goruntime.GOOS))
		})

		It("rejects requests without client certificate", func() {
			tmpDir := GinkgoT().TempDir()
			caCertFile, serverCertFile, serverKeyFile, _, _ := generateTestCerts(tmpDir)

			serverCreds, err := loadServerTLS(serverCertFile, serverKeyFile, caCertFile)
			Expect(err).ToNot(HaveOccurred())

			caBytes, err := os.ReadFile(caCertFile)
			Expect(err).ToNot(HaveOccurred())
			caPool := x509.NewCertPool()
			Expect(caPool.AppendCertsFromPEM(caBytes)).To(BeTrue())

			authClient, authConn, authServer := startServerWithAuth(workDir,
				[]grpc.ServerOption{serverCreds},
				[]grpc.DialOption{
					grpc.WithTransportCredentials(grpccreds.NewTLS(&tls.Config{
						RootCAs: caPool,
					})),
				},
			)
			defer authConn.Close()
			defer authServer.Stop()

			_, err = authClient.Ping(context.Background(), &agentpb.PingRequest{})
			Expect(err).To(HaveOccurred())
		})

		It("works with token + mTLS combined", func() {
			tmpDir := GinkgoT().TempDir()
			caCertFile, serverCertFile, serverKeyFile, clientCertFile, clientKeyFile := generateTestCerts(tmpDir)

			serverCreds, err := loadServerTLS(serverCertFile, serverKeyFile, caCertFile)
			Expect(err).ToNot(HaveOccurred())

			clientCert, err := tls.LoadX509KeyPair(clientCertFile, clientKeyFile)
			Expect(err).ToNot(HaveOccurred())
			caBytes, err := os.ReadFile(caCertFile)
			Expect(err).ToNot(HaveOccurred())
			caPool := x509.NewCertPool()
			Expect(caPool.AppendCertsFromPEM(caBytes)).To(BeTrue())

			token := "combined-secret"
			authClient, authConn, authServer := startServerWithAuth(workDir,
				[]grpc.ServerOption{
					serverCreds,
					grpc.ChainUnaryInterceptor(tokenAuthUnaryInterceptor(token)),
					grpc.ChainStreamInterceptor(tokenAuthStreamInterceptor(token)),
				},
				[]grpc.DialOption{
					grpc.WithTransportCredentials(grpccreds.NewTLS(&tls.Config{
						Certificates: []tls.Certificate{clientCert},
						RootCAs:      caPool,
					})),
					grpc.WithPerRPCCredentials(testTokenCredentials{token: token, requireTLS: true}),
				},
			)
			defer authConn.Close()
			defer authServer.Stop()

			resp, err := authClient.Ping(context.Background(), &agentpb.PingRequest{})
			Expect(err).ToNot(HaveOccurred())
			Expect(resp.Platform).To(Equal(goruntime.GOOS))
		})
	})
})

// startServerWithAuth creates a gRPC server with the given options and returns
// the listener address, server, and a client connection with the given dial options.
func startServerWithAuth(workDir string, serverOpts []grpc.ServerOption, dialOpts []grpc.DialOption) (agentpb.NativeAgentClient, *grpc.ClientConn, *grpc.Server) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	Expect(err).ToNot(HaveOccurred())

	grpcServer := grpc.NewServer(serverOpts...)
	agentpb.RegisterNativeAgentServer(grpcServer, &server{
		workDir:  workDir,
		cacheDir: filepath.Join(workDir, "caches"),
	})
	go grpcServer.Serve(lis)

	conn, err := grpc.NewClient(lis.Addr().String(), dialOpts...)
	Expect(err).ToNot(HaveOccurred())

	return agentpb.NewNativeAgentClient(conn), conn, grpcServer
}

// testTokenCredentials implements grpc credentials.PerRPCCredentials for tests.
type testTokenCredentials struct {
	token      string
	requireTLS bool
}

func (c testTokenCredentials) GetRequestMetadata(ctx context.Context, uri ...string) (map[string]string, error) {
	return map[string]string{"authorization": "Bearer " + c.token}, nil
}

func (c testTokenCredentials) RequireTransportSecurity() bool { return c.requireTLS }

// generateTestCerts creates a self-signed CA, server cert, and client cert.
// All certs are written as PEM files to tmpDir. Returns file paths.
func generateTestCerts(tmpDir string) (caCertFile, serverCertFile, serverKeyFile, clientCertFile, clientKeyFile string) {
	// Generate CA key and cert.
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	Expect(err).ToNot(HaveOccurred())

	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	caCertDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	Expect(err).ToNot(HaveOccurred())

	caCertFile = filepath.Join(tmpDir, "ca-cert.pem")
	writePEM(caCertFile, "CERTIFICATE", caCertDER)

	caCert, err := x509.ParseCertificate(caCertDER)
	Expect(err).ToNot(HaveOccurred())

	// Generate server key and cert.
	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	Expect(err).ToNot(HaveOccurred())

	serverTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	serverCertDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caCert, &serverKey.PublicKey, caKey)
	Expect(err).ToNot(HaveOccurred())

	serverCertFile = filepath.Join(tmpDir, "server-cert.pem")
	serverKeyFile = filepath.Join(tmpDir, "server-key.pem")
	writePEM(serverCertFile, "CERTIFICATE", serverCertDER)
	writeKeyPEM(serverKeyFile, serverKey)

	// Generate client key and cert.
	clientKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	Expect(err).ToNot(HaveOccurred())

	clientTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(3),
		Subject:      pkix.Name{CommonName: "test-client"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	clientCertDER, err := x509.CreateCertificate(rand.Reader, clientTemplate, caCert, &clientKey.PublicKey, caKey)
	Expect(err).ToNot(HaveOccurred())

	clientCertFile = filepath.Join(tmpDir, "client-cert.pem")
	clientKeyFile = filepath.Join(tmpDir, "client-key.pem")
	writePEM(clientCertFile, "CERTIFICATE", clientCertDER)
	writeKeyPEM(clientKeyFile, clientKey)

	return
}

func writePEM(path, pemType string, data []byte) {
	f, err := os.Create(path)
	Expect(err).ToNot(HaveOccurred())
	defer f.Close()
	Expect(pem.Encode(f, &pem.Block{Type: pemType, Bytes: data})).To(Succeed())
}

func writeKeyPEM(path string, key *ecdsa.PrivateKey) {
	der, err := x509.MarshalECPrivateKey(key)
	Expect(err).ToNot(HaveOccurred())
	writePEM(path, "EC PRIVATE KEY", der)
}

// createGzipTar creates a gzip-compressed tar archive from a map of
// filename → content.
func createGzipTar(files map[string]string) []byte {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	for name, content := range files {
		tw.WriteHeader(&tar.Header{
			Name: name,
			Mode: 0644,
			Size: int64(len(content)),
		})
		tw.Write([]byte(content))
	}
	tw.Close()
	gw.Close()
	return buf.Bytes()
}

// createRawTar creates an uncompressed tar archive from a map of
// filename → content.
func createRawTar(files map[string]string) []byte {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, content := range files {
		tw.WriteHeader(&tar.Header{
			Name: name,
			Mode: 0644,
			Size: int64(len(content)),
		})
		tw.Write([]byte(content))
	}
	tw.Close()
	return buf.Bytes()
}
