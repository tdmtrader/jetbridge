package steps

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/hangar/output"
	corev1 "k8s.io/api/core/v1"
)

func exerciseManagedInputInit(ctx context.Context, in BoundOutput, mode string, warrant hangaroutput.ReadWarrant) error {
	daemon := in.Tree.Outcome.Source.Draft.Daemon
	_, host, portText, ok := splitDaemonAddress(daemon.Output.URL)
	if !ok {
		return fmt.Errorf("invalid fixture daemon address")
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return err
	}
	config := jetbridge.NewConfig("managed-input", "")
	config.ArtifactDaemonHostPath = daemon.Output.Root
	config.OutputPlaneEnabled = true
	config.HangarEnabled = true
	config.OutputDaemonPort = port
	backend := jetbridge.NewDaemonSetBackend(config, nil, nil)
	request := output.ManagedReadRequest{Ref: in.Tree.Ref, Destination: warrant.Record.Destination, Warrant: warrant.Token}
	// Before the new runtime field exists this same payload loses its read
	// authority and reaches the old strict-input path: the red is behavioral.
	data, err := json.Marshal(map[string]any{"HangarTree": in.Tree.Ref, "HangarRead": request, "DestinationPath": "/work/source"})
	if err != nil {
		return err
	}
	var input runtime.Input
	if err := json.Unmarshal(data, &input); err != nil {
		return err
	}
	volume := backend.StepVolume(request.Destination.Volume, request.Destination.Handle, request.Destination.Volume)
	mount := corev1.VolumeMount{Name: volume.Name, MountPath: input.DestinationPath, ReadOnly: true}
	inits, err := backend.BuildFetchInitContainers(request.Destination.Handle, []runtime.Input{input}, []corev1.Volume{volume}, []corev1.VolumeMount{mount})
	if err != nil {
		return fmt.Errorf("building managed input initialization: %w", err)
	}
	if len(inits) != 1 || len(inits[0].Command) != 3 || len(inits[0].VolumeMounts) != 1 || !inits[0].VolumeMounts[0].ReadOnly || inits[0].VolumeMounts[0].Name != volume.Name {
		return fmt.Errorf("managed initialization did not get exactly its declared read-only input volume")
	}
	root := volume.HostPath.Path
	if err := os.MkdirAll(root, 0755); err != nil {
		return err
	}
	// Follow the emitted verification mount in this process fixture. The
	// script and HTTP request remain production's; this does not claim kubelet
	// mount enforcement. The live tier proves that separately.
	assignment := "\nROOT=" + inits[0].VolumeMounts[0].MountPath + "\n"
	if strings.Count(inits[0].Command[2], assignment) != 1 {
		return fmt.Errorf("initialization does not identify its verification mount")
	}
	command := strings.Replace(inits[0].Command[2], assignment, "\nROOT='"+strings.ReplaceAll(root, "'", "'\\''")+"'\n", 1)
	run := func() error {
		cmd := exec.CommandContext(ctx, inits[0].Command[0], "-c", command)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOST_IP=" + host}
		out, err := cmd.CombinedOutput()
		if strings.Contains(string(out), warrant.Token) {
			return fmt.Errorf("initialization exposed its read warrant")
		}
		if err != nil {
			return fmt.Errorf("initialization refused: %w (%s)", err, abbrev(string(out)))
		}
		return nil
	}
	if mode == "a transient failure" {
		// The first attempt meets a destination the daemon cannot write --
		// an unavailable answer the init retries -- and the next finds it
		// writable again. The one lease minted for this Pod must still admit
		// that retry.
		handleDir := filepath.Dir(root)
		if err := os.Chmod(handleDir, 0o500); err != nil {
			return err
		}
		restored := make(chan error, 1)
		go func() {
			time.Sleep(time.Second)
			restored <- os.Chmod(handleDir, 0o755)
		}()
		err = run()
		if restoreErr := <-restored; restoreErr != nil {
			return restoreErr
		}
		if err != nil {
			return fmt.Errorf("a transient failure spent the read lease: %w", err)
		}
		if err := verifyManagedMaterialization(in, root); err != nil {
			return err
		}
		// The warrant binds its destination: the kept lease admits no other
		// holder, and once the read succeeded it is given back.
		for _, volume := range []string{"input-1", request.Destination.Volume} {
			replay := output.ManagedReadRequest{Ref: in.Tree.Ref, Destination: output.ReadDestination{Handle: request.Destination.Handle, Volume: volume}, Warrant: warrant.Token}
			body, err := json.Marshal(replay)
			if err != nil {
				return err
			}
			response, err := daemon.HTTP.Post(daemon.Output.URL+"/read/v1/materialize", "application/json", bytes.NewReader(body))
			if err != nil {
				return err
			}
			response.Body.Close()
			if response.StatusCode != http.StatusForbidden {
				return fmt.Errorf("a replay of a used read warrant into %s answered %d", volume, response.StatusCode)
			}
		}
		return nil
	}
	err = run()
	if mode == "released" || mode == "forged" {
		if err == nil || !strings.Contains(err.Error(), "managed input materialization was refused") {
			return fmt.Errorf("%s lease did not produce the expected authorization refusal: %v", mode, err)
		}
		if _, err := os.Stat(filepath.Join(root, ".hangar-materialized")); !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("refused initialization left a sealed receipt: %v", err)
		}
		return nil
	}
	if err != nil {
		return err
	}
	if err := verifyManagedMaterialization(in, root); err != nil {
		return err
	}
	if mode == "lost success response" || mode == "conflicting sealed receipt" {
		if err := daemon.Output.crash(); err != nil {
			return err
		}
		if mode == "conflicting sealed receipt" {
			receipt := filepath.Join(root, ".hangar-materialized")
			if err := os.Chmod(receipt, 0644); err != nil {
				return err
			}
			if err := os.WriteFile(receipt, []byte("{}"), 0444); err != nil {
				return err
			}
			if err := os.Chmod(receipt, 0444); err != nil {
				return err
			}
		}
		err := run()
		if mode == "conflicting sealed receipt" {
			if err == nil {
				return fmt.Errorf("a conflicting receipt let initialization finish")
			}
			return nil
		}
		return err
	}
	return nil
}
