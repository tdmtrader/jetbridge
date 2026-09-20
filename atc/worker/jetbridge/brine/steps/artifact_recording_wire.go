package steps

import (
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"reflect"
	"strconv"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc/worker/jetbridge"
)

// Observe only the producing daemon's real requests during the action. Alias
// clients are constructed lazily by RecordOutputs, so constructor-only tracing
// cannot see them. Neither daemon responses nor Kubernetes state are supplied.
type artifactRecordingObservation struct {
	wire     *daemonWireObservation
	cacheKey string
}

func (in *ArtifactCluster) observeRecording(cacheKey string, action func()) error {
	if err := in.ensureLiveMirrorPeer(); err != nil {
		return err
	}
	if in.Peer == nil {
		action()
		return nil
	}
	wire, err := observeDaemonTraffic(map[string]bool{
		net.JoinHostPort(in.Node.host, strconv.Itoa(in.Node.port)): true,
	}, func() {
		in.Backend.SetDaemonClient(jetbridge.NewDaemonClient(
			lagertest.NewTestLogger("brine-recording-wire"), in.Clientset,
			in.Namespace, in.daemonService(), in.Node.port, nil,
		))
		action()
	})
	if err != nil {
		return err
	}
	in.recording = &artifactRecordingObservation{wire: wire, cacheKey: cacheKey}
	return nil
}

func (in ArtifactCluster) requireRecording() error {
	if in.recording == nil {
		return fmt.Errorf("no producer recording observation")
	}
	requests, err := in.recording.wire.capturedRequests()
	if err != nil {
		return err
	}
	type output struct{ alias, directory string }
	var outputs []output
	if in.recording.cacheKey != "" {
		outputs = append(outputs, output{in.recording.cacheKey, "dir"})
	} else {
		for name, path := range in.Outputs {
			handle, ok := in.ExpectedVolumes[path]
			if !ok || handle == "" {
				return fmt.Errorf("no independent volume handle for output %q", name)
			}
			outputs = append(outputs, output{handle, name})
		}
	}
	if len(outputs) == 0 {
		return fmt.Errorf("no expected outputs to check")
	}
	if len(requests) != 2*len(outputs) {
		return fmt.Errorf("producer recording: want %d requests, got %d: %+v", 2*len(outputs), len(requests), requests)
	}
	for _, out := range outputs {
		// The worker keys the daemon by the canonical path even when the
		// handle was given in an equivalent spelling ("build-42/."); that
		// canonical form is what the scenario expects on the wire.
		key := filepath.Clean(in.Handle + "/" + out.directory)
		localPath := filepath.Join(in.StoreRoot, "steps", key)
		// A cache is registered through RegisterAlias (durable_key); an output
		// through the worker's own volume registration, which always says
		// whether the path is read-only (storage_daemonset.go) and for an
		// output says false.
		aliasBody := map[string]any{"key": out.alias, "local_path": localPath}
		if in.recording.cacheKey != "" {
			aliasBody["durable_key"] = ""
		} else {
			aliasBody["read_only"] = false
		}
		register, mirror := -1, -1
		for i, req := range requests {
			// Decoded as any, not string: the worker's own volume registration
			// (storage_daemonset.go, POST /register) carries a boolean
			// `read_only`, and a string-only decode refused the whole request
			// list on it (build 873827, three mirroring scenarios).
			var body map[string]any
			if err := json.Unmarshal(req.Body, &body); err != nil {
				return fmt.Errorf("producer request JSON: %w", err)
			}
			if req.Method == "POST" && req.Path == "/register" && reflect.DeepEqual(body, aliasBody) {
				if register >= 0 {
					return fmt.Errorf("duplicate registration for %q", out.alias)
				}
				register = i
			}
			if req.Method == "POST" && req.Path == "/mirror" && reflect.DeepEqual(body, map[string]any{"key": key}) {
				if mirror >= 0 {
					return fmt.Errorf("duplicate mirror for %q", key)
				}
				mirror = i
			}
		}
		if register < 0 || mirror < 0 {
			return fmt.Errorf("producer recording missing exact registration or mirror for %q: %+v", out.alias, requests)
		}
		if (in.recording.cacheKey == "" && register > mirror) || (in.recording.cacheKey != "" && mirror > register) {
			return fmt.Errorf("producer recording order for %q: register=%d mirror=%d cache=%t", out.alias, register, mirror, in.recording.cacheKey != "")
		}
		loc, found := in.Locator.Locate(jetbridge.ArtifactKey(out.alias))
		hostDir := key
		if in.recording.cacheKey != "" {
			hostDir = out.alias
		}
		if !found || loc.NodeName != in.NodeName || loc.HostDir != hostDir {
			return fmt.Errorf("recorded location for %q: found=%t location=%+v; want node=%q directory=%q", out.alias, found, loc, in.NodeName, hostDir)
		}
	}
	return nil
}
