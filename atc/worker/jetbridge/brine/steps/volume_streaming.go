package steps

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/worker/jetbridge"
)

// localExecAdapter is a REAL PodExecutor, not a spy.
//
// It implements the same interface the SPDY executor implements, with one
// behavioral difference we can name: the command runs in a local directory
// instead of inside a pod. Real tar, real filesystem, deterministic and
// synchronous — PHILOSOPHY.md's "test adapters are real adapters", the same
// argument that makes SynchronousTestBus legitimate.
//
// This is the answer to the spy sites. The ginkgo tests assert
// `call.command == ["tar","xf","-","-C","/tmp/build/inputs"]` because a
// RECORDING double is the only thing a recording double can tell you. A
// WORKING double lets the scenario assert what a real consumer of the volume
// port actually experiences: bytes put in come back out.
//
// It records nothing. There is nothing to assert on but the artifact.
type localExecAdapter struct {
	root    string // stands in for the pod's filesystem
	failure string // non-empty: this cluster cannot run commands
}

func (l *localExecAdapter) ExecInPod(
	ctx context.Context,
	_, _, _ string,
	command []string,
	stdin io.Reader,
	stdout, stderr io.Writer,
	_ bool,
	_ jetbridge.ExecAttrs,
) error {
	if l.failure != "" {
		return errors.New(l.failure)
	}
	if len(command) == 0 {
		return fmt.Errorf("empty command")
	}

	// Translate the pod-absolute -C target into this adapter's root. The
	// runtime builds the path; we honour it rather than asserting on it.
	translated := make([]string, len(command))
	copy(translated, command)
	for i, arg := range translated {
		if i > 0 && translated[i-1] == "-C" {
			dir := filepath.Join(l.root, filepath.Clean("/"+arg))
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return fmt.Errorf("prepare %q: %w", dir, err)
			}
			translated[i] = dir
		}
	}

	cmd := exec.CommandContext(ctx, translated[0], translated[1:]...)
	// macOS bsdtar writes AppleDouble "._name" entries for extended
	// attributes. That is this adapter's platform leaking into the archive,
	// not anything the runtime does, so switch it off at the source rather
	// than filtering it out of the assertion.
	cmd.Env = append(os.Environ(), "COPYFILE_DISABLE=1")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("exec %v: %w", translated, err)
	}
	return nil
}

// VolumeStreamingDefinitions expresses volume behavior as artifact movement.
// Nothing here names tar, exec, a pod, or ExecAttrs.
func VolumeStreamingDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMap[brine.Empty, VolumeSet](
			"a volume {string} mounted at {string}",
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder) (VolumeSet, error) {
				set := VolumeSet{
					Volumes: map[string]*jetbridge.Volume{},
					Ctx:     context.Background(),
				}
				return addVolume(set, p)
			},
		),

		brine.DefineMap[VolumeSet, VolumeSet](
			"another volume {string} mounted at {string}",
			func(in VolumeSet, p brine.Params, _ *brine.Recorder) (VolumeSet, error) {
				return addVolume(in, p)
			},
		),

		// VT-05: a stub volume has no executor and cannot perform I/O.
		brine.DefineMap[VolumeSet, VolumeSet](
			"a stub volume {string} with no cluster behind it",
			func(in VolumeSet, p brine.Params, _ *brine.Recorder) (VolumeSet, error) {
				name, ok := p.GetString(0)
				if !ok {
					return VolumeSet{}, fmt.Errorf("expected a volume name parameter")
				}
				in.Volumes[name] = jetbridge.NewStubVolume(name+"-handle", "k8s-worker-1", "/tmp/stub")
				return in, nil
			},
		),

		brine.DefineMap[VolumeSet, VolumeSet](
			"volume {string} sits on a cluster that cannot run commands",
			func(in VolumeSet, p brine.Params, _ *brine.Recorder) (VolumeSet, error) {
				name, ok := p.GetString(0)
				if !ok {
					return VolumeSet{}, fmt.Errorf("expected a volume name parameter")
				}
				root, err := os.MkdirTemp("", "brine-volume")
				if err != nil {
					return VolumeSet{}, fmt.Errorf("create volume root: %w", err)
				}
				volume := jetbridge.NewDeferredVolume(
					name+"-handle", "k8s-worker-1",
					&localExecAdapter{root: root, failure: "exec failed: pod terminated"},
					"test-namespace", "main", "/tmp/build/inputs",
				)
				volume.SetPodName(name + "-pod")
				in.Volumes[name] = volume
				return in, nil
			},
		),

		brine.DefineMap[VolumeSet, VolumeSet](
			"a file {string} containing {string} is put into volume {string} at {string}",
			func(in VolumeSet, p brine.Params, _ *brine.Recorder) (VolumeSet, error) {
				name, _ := p.GetString(0)
				content, _ := p.GetString(1)
				volName, _ := p.GetString(2)
				destPath, ok := p.GetString(3)
				if !ok {
					return VolumeSet{}, fmt.Errorf("expected four parameters")
				}

				volume, err := in.volume(volName)
				if err != nil {
					return VolumeSet{}, err
				}
				archive, err := tarOfOneFile(name, content)
				if err != nil {
					return VolumeSet{}, err
				}
				if err := volume.StreamIn(in.Ctx, destPath, compression.NewGzipCompression(), 0, archive); err != nil {
					return VolumeSet{}, fmt.Errorf("stream into %q: %w", volName, err)
				}
				return in, nil
			},
		),

		// The user story the volume-to-volume ginkgo test was really about:
		// one step's output becomes the next step's input.
		brine.DefineMap[VolumeSet, VolumeSet](
			"the contents of volume {string} are moved into volume {string}",
			func(in VolumeSet, p brine.Params, _ *brine.Recorder) (VolumeSet, error) {
				srcName, _ := p.GetString(0)
				dstName, ok := p.GetString(1)
				if !ok {
					return VolumeSet{}, fmt.Errorf("expected two volume name parameters")
				}

				src, err := in.volume(srcName)
				if err != nil {
					return VolumeSet{}, err
				}
				dst, err := in.volume(dstName)
				if err != nil {
					return VolumeSet{}, err
				}

				stream, err := src.StreamOut(in.Ctx, ".", compression.NewGzipCompression())
				if err != nil {
					return VolumeSet{}, fmt.Errorf("stream out of %q: %w", srcName, err)
				}
				defer stream.Close()

				if err := dst.StreamIn(in.Ctx, ".", compression.NewGzipCompression(), 0, stream); err != nil {
					return VolumeSet{}, fmt.Errorf("stream into %q: %w", dstName, err)
				}
				return in, nil
			},
		),

		// Reading is an attempt, so that failure is assertable rather than
		// fatal to the scenario.
		brine.DefineMap[VolumeSet, VolumeRead](
			"volume {string} is read from {string}",
			func(in VolumeSet, p brine.Params, _ *brine.Recorder) (VolumeRead, error) {
				volName, _ := p.GetString(0)
				srcPath, ok := p.GetString(1)
				if !ok {
					return VolumeRead{}, fmt.Errorf("expected two parameters")
				}

				volume, err := in.volume(volName)
				if err != nil {
					return VolumeRead{}, err
				}

				stream, streamErr := volume.StreamOut(in.Ctx, srcPath, compression.NewGzipCompression())
				if streamErr != nil {
					return VolumeRead{Err: streamErr, Message: streamErr.Error()}, nil
				}
				defer stream.Close()

				files, readErr := filesInGzippedTar(stream)
				if readErr != nil {
					return VolumeRead{Err: readErr, Message: readErr.Error()}, nil
				}
				return VolumeRead{Files: files}, nil
			},
		),

		brine.DefineMap[VolumeSet, VolumeRead](
			"a file is put into volume {string}",
			func(in VolumeSet, p brine.Params, _ *brine.Recorder) (VolumeRead, error) {
				volName, ok := p.GetString(0)
				if !ok {
					return VolumeRead{}, fmt.Errorf("expected a volume name parameter")
				}
				volume, err := in.volume(volName)
				if err != nil {
					return VolumeRead{}, err
				}
				archive, err := tarOfOneFile("probe.txt", "probe")
				if err != nil {
					return VolumeRead{}, err
				}
				writeErr := volume.StreamIn(in.Ctx, ".", compression.NewGzipCompression(), 0, archive)
				if writeErr != nil {
					return VolumeRead{Err: writeErr, Message: writeErr.Error()}, nil
				}
				return VolumeRead{}, nil
			},
		),

		brine.DefineCheck[VolumeRead](
			"the artifact {string} containing {string} is there",
			func(in VolumeRead, p brine.Params, _ *brine.Recorder) error {
				name, _ := p.GetString(0)
				want, ok := p.GetString(1)
				if !ok {
					return fmt.Errorf("expected two parameters")
				}
				if in.Err != nil {
					return fmt.Errorf("reading the volume failed: %w", in.Err)
				}
				got, found := in.Files[name]
				if !found {
					names := make([]string, 0, len(in.Files))
					for n := range in.Files {
						names = append(names, n)
					}
					return fmt.Errorf("expected %q, found %v", name, names)
				}
				if got != want {
					return fmt.Errorf("expected %q to contain %q, got %q", name, want, got)
				}
				return nil
			},
		),

		brine.DefineCheck[VolumeRead](
			"it fails rather than panicking, saying {string}",
			func(in VolumeRead, p brine.Params, _ *brine.Recorder) error {
				want, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a message parameter")
				}
				if in.Err == nil {
					return fmt.Errorf("expected a failure mentioning %q, but it succeeded", want)
				}
				if !containsFold(in.Message, want) {
					return fmt.Errorf("expected the failure to mention %q, got %q", want, in.Message)
				}
				return nil
			},
		),
	}
}

func addVolume(set VolumeSet, p brine.Params) (VolumeSet, error) {
	name, _ := p.GetString(0)
	mountPath, ok := p.GetString(1)
	if !ok {
		return VolumeSet{}, fmt.Errorf("expected a name and a mount path")
	}

	root, err := os.MkdirTemp("", "brine-volume")
	if err != nil {
		return VolumeSet{}, fmt.Errorf("create volume root: %w", err)
	}

	volume := jetbridge.NewDeferredVolume(
		name+"-handle", "k8s-worker-1",
		&localExecAdapter{root: root},
		"test-namespace", "main", mountPath,
	)
	volume.SetPodName(name + "-pod")
	set.Volumes[name] = volume
	return set, nil
}
