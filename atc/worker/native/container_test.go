package native_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/native"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// fakeArtifact is a test double for runtime.Artifact that returns
// predetermined tar stream data.
type fakeArtifact struct {
	handle    string
	source    string
	streamOut []byte
}

func (a *fakeArtifact) StreamOut(_ context.Context, _ string, _ compression.Compression) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(a.streamOut)), nil
}
func (a *fakeArtifact) Handle() string { return a.handle }
func (a *fakeArtifact) Source() string { return a.source }

var _ = Describe("Container", func() {
	var (
		ctx          context.Context
		fakeDBWorker *dbfakes.FakeWorker
		worker       *native.Worker
		config       native.Config
		delegate     runtime.BuildStepDelegate
	)

	BeforeEach(func() {
		ctx = context.Background()
		fakeDBWorker = new(dbfakes.FakeWorker)
		fakeDBWorker.NameReturns("native-darwin")
		delegate = &noopDelegate{}

		config = native.Config{
			WorkDir:    filepath.Join(GinkgoT().TempDir(), "work"),
			CacheDir:   filepath.Join(GinkgoT().TempDir(), "cache"),
			Platform:   "darwin",
			WorkerName: "native-darwin",
		}

		// Use nil compression so streamInputs tests work with raw tar from fake artifacts.
		// Cross-worker compressed streaming is covered by volume_test.go.
		worker = native.NewWorker(fakeDBWorker, config, nil, nil)
	})

	Describe("Run", func() {
		It("runs an executable and returns a process", func() {
			setupFakeDBContainer(fakeDBWorker, "run-handle")

			container, _, err := worker.FindOrCreateContainer(ctx,
				newOwner("run-handle"), newMeta(), newSpec(GinkgoT().TempDir()), delegate)
			Expect(err).ToNot(HaveOccurred())

			var stdout bytes.Buffer
			process, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "echo hello"},
			}, runtime.ProcessIO{Stdout: &stdout})
			Expect(err).ToNot(HaveOccurred())

			result, err := process.Wait(context.Background())
			Expect(err).ToNot(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(0))
			Expect(stdout.String()).To(ContainSubstring("hello"))
		})

		It("returns ExecutableNotFoundError for missing executable", func() {
			setupFakeDBContainer(fakeDBWorker, "notfound-handle")

			container, _, err := worker.FindOrCreateContainer(ctx,
				newOwner("notfound-handle"), newMeta(), newSpec("/tmp"), delegate)
			Expect(err).ToNot(HaveOccurred())

			_, err = container.Run(ctx, runtime.ProcessSpec{
				Path: "totally-nonexistent-binary",
			}, runtime.ProcessIO{})
			Expect(err).To(HaveOccurred())

			Expect(err).To(BeAssignableToTypeOf(runtime.ExecutableNotFoundError{}))
		})

		It("returns ExecutableNotFoundError for missing absolute path", func() {
			setupFakeDBContainer(fakeDBWorker, "abs-handle")

			container, _, err := worker.FindOrCreateContainer(ctx,
				newOwner("abs-handle"), newMeta(), newSpec("/tmp"), delegate)
			Expect(err).ToNot(HaveOccurred())

			_, err = container.Run(ctx, runtime.ProcessSpec{
				Path: "/nonexistent/binary",
			}, runtime.ProcessIO{})
			Expect(err).To(HaveOccurred())
		})

		It("merges environment with ProcessSpec taking precedence", func() {
			setupFakeDBContainer(fakeDBWorker, "env-handle")

			spec := runtime.ContainerSpec{
				TeamID: 1,
				Dir:    "/tmp",
				Env:    []string{"FOO=base", "BAR=base"},
			}
			container, _, err := worker.FindOrCreateContainer(ctx,
				newOwner("env-handle"), newMeta(), spec, delegate)
			Expect(err).ToNot(HaveOccurred())

			var stdout bytes.Buffer
			process, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "echo $FOO $BAR $BAZ"},
				Env:  []string{"FOO=override", "BAZ=new"},
			}, runtime.ProcessIO{Stdout: &stdout})
			Expect(err).ToNot(HaveOccurred())

			result, err := process.Wait(context.Background())
			Expect(err).ToNot(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(0))
			Expect(stdout.String()).To(ContainSubstring("override base new"))
		})

		It("writes PID file to container dir", func() {
			setupFakeDBContainer(fakeDBWorker, "pid-handle")

			container, _, err := worker.FindOrCreateContainer(ctx,
				newOwner("pid-handle"), newMeta(), newSpec("/tmp"), delegate)
			Expect(err).ToNot(HaveOccurred())

			process, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sleep",
				Args: []string{"60"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			pidFile := filepath.Join(config.WorkDir, "containers", "pid-handle", "pid-handle.pid")
			Expect(pidFile).To(BeAnExistingFile())

			// Clean up.
			cancelCtx, cancel := context.WithCancel(context.Background())
			cancel()
			process.Wait(cancelCtx)
		})

		It("does NOT kill process when Run ctx is cancelled (Bug 2 contract)", func() {
			setupFakeDBContainer(fakeDBWorker, "ctx-handle")

			// Use a context for Run that we'll cancel.
			runCtx, runCancel := context.WithCancel(ctx)

			container, _, err := worker.FindOrCreateContainer(runCtx,
				newOwner("ctx-handle"), newMeta(), newSpec("/tmp"), delegate)
			Expect(err).ToNot(HaveOccurred())

			process, err := container.Run(runCtx, runtime.ProcessSpec{
				Path: "/bin/sleep",
				Args: []string{"60"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			// Cancel the Run context.
			runCancel()

			// The process should still be alive — use a fresh context for Wait.
			// Give it a short timeout to check it's running, then kill it.
			waitCtx, waitCancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer waitCancel()

			_, err = process.Wait(waitCtx)
			// Should timeout (context deadline exceeded), proving process is still alive.
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("streamInputs (via Run)", func() {
		It("streams remote artifacts into correct input volumes, not Dir volume (Bug 1 regression)", func() {
			setupFakeDBContainer(fakeDBWorker, "stream-handle")

			// Create a tar with a marker file.
			tarData := createTar(map[string]string{"marker.txt": "from-remote"})

			workDir := GinkgoT().TempDir()
			spec := runtime.ContainerSpec{
				TeamID: 1,
				Dir:    workDir,
				Inputs: []runtime.Input{
					{
						Artifact:        &fakeArtifact{handle: "art-0", source: "k8s-worker", streamOut: tarData},
						DestinationPath: filepath.Join(workDir, "input-0"),
					},
				},
			}

			container, mounts, err := worker.FindOrCreateContainer(ctx,
				newOwner("stream-handle"), newMeta(), spec, delegate)
			Expect(err).ToNot(HaveOccurred())

			// Run a trivial command to trigger streamInputs.
			var stdout bytes.Buffer
			process, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "echo done"},
			}, runtime.ProcessIO{Stdout: &stdout})
			Expect(err).ToNot(HaveOccurred())
			process.Wait(context.Background())

			By("verifying the artifact landed in the input volume, not the Dir volume")
			// Find the input mount.
			Expect(mounts).To(HaveLen(2)) // Dir + 1 input
			inputMount := mounts[1]
			Expect(inputMount.MountPath).To(Equal(filepath.Join(workDir, "input-0")))

			inputVol := inputMount.Volume.(*native.Volume)
			markerPath := filepath.Join(inputVol.Path(), "marker.txt")
			data, err := os.ReadFile(markerPath)
			Expect(err).ToNot(HaveOccurred())
			Expect(string(data)).To(Equal("from-remote"))

			By("verifying the Dir volume does NOT contain the marker")
			dirVol := mounts[0].Volume.(*native.Volume)
			_, err = os.ReadFile(filepath.Join(dirVol.Path(), "marker.txt"))
			Expect(os.IsNotExist(err)).To(BeTrue())
		})

		It("skips artifacts from the same worker", func() {
			setupFakeDBContainer(fakeDBWorker, "local-handle")

			tarData := createTar(map[string]string{"should-not-appear.txt": "x"})
			workDir := GinkgoT().TempDir()
			spec := runtime.ContainerSpec{
				TeamID: 1,
				Dir:    workDir,
				Inputs: []runtime.Input{
					{
						// Same source as worker name — should be skipped.
						Artifact:        &fakeArtifact{handle: "local-art", source: "native-darwin", streamOut: tarData},
						DestinationPath: filepath.Join(workDir, "input-0"),
					},
				},
			}

			container, mounts, err := worker.FindOrCreateContainer(ctx,
				newOwner("local-handle"), newMeta(), spec, delegate)
			Expect(err).ToNot(HaveOccurred())

			process, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh", Args: []string{"-c", "true"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())
			process.Wait(context.Background())

			// Input volume should be empty (artifact was skipped).
			inputVol := mounts[1].Volume.(*native.Volume)
			entries, _ := os.ReadDir(inputVol.Path())
			Expect(entries).To(BeEmpty())
		})
	})

	Describe("Attach", func() {
		It("returns an error", func() {
			setupFakeDBContainer(fakeDBWorker, "attach-handle")

			container, _, err := worker.FindOrCreateContainer(ctx,
				newOwner("attach-handle"), newMeta(), newSpec("/tmp"), delegate)
			Expect(err).ToNot(HaveOccurred())

			_, err = container.Attach(ctx, "some-id", runtime.ProcessIO{})
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("Properties", func() {
		It("returns empty map initially", func() {
			setupFakeDBContainer(fakeDBWorker, "props-handle")

			container, _, err := worker.FindOrCreateContainer(ctx,
				newOwner("props-handle"), newMeta(), newSpec("/tmp"), delegate)
			Expect(err).ToNot(HaveOccurred())

			props, err := container.Properties()
			Expect(err).ToNot(HaveOccurred())
			Expect(props).To(BeEmpty())
		})

		It("returns set properties", func() {
			setupFakeDBContainer(fakeDBWorker, "props2-handle")

			container, _, err := worker.FindOrCreateContainer(ctx,
				newOwner("props2-handle"), newMeta(), newSpec("/tmp"), delegate)
			Expect(err).ToNot(HaveOccurred())

			Expect(container.SetProperty("key", "value")).To(Succeed())
			props, err := container.Properties()
			Expect(err).ToNot(HaveOccurred())
			Expect(props["key"]).To(Equal("value"))
		})

		It("is thread-safe", func() {
			setupFakeDBContainer(fakeDBWorker, "conc-handle")

			container, _, err := worker.FindOrCreateContainer(ctx,
				newOwner("conc-handle"), newMeta(), newSpec("/tmp"), delegate)
			Expect(err).ToNot(HaveOccurred())

			var wg sync.WaitGroup
			for i := 0; i < 50; i++ {
				wg.Add(1)
				go func(n int) {
					defer wg.Done()
					container.SetProperty(fmt.Sprintf("k%d", n), "v")
					container.Properties()
				}(i)
			}
			wg.Wait()

			props, err := container.Properties()
			Expect(err).ToNot(HaveOccurred())
			Expect(props).To(HaveLen(50))
		})
	})

	Describe("DBContainer", func() {
		It("returns the stored db.CreatedContainer", func() {
			_, fakeCreated := setupFakeDBContainer(fakeDBWorker, "dbc-handle")

			container, _, err := worker.FindOrCreateContainer(ctx,
				newOwner("dbc-handle"), newMeta(), newSpec("/tmp"), delegate)
			Expect(err).ToNot(HaveOccurred())

			Expect(container.DBContainer()).To(Equal(fakeCreated))
		})
	})
})

// --- Test helpers ---

func newOwner(handle string) db.ContainerOwner {
	return db.NewFixedHandleContainerOwner(handle)
}

func newMeta() db.ContainerMetadata {
	return db.ContainerMetadata{Type: db.ContainerTypeTask}
}

func newSpec(dir string) runtime.ContainerSpec {
	return runtime.ContainerSpec{
		TeamID: 1,
		Dir:    dir,
	}
}
