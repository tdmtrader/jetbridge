package jetbridge

// The capture control init's GENERATED script, executed against a real output
// daemon.
//
// This file exists because `sh -n` is not evidence. The Phase 4 round-1 review
// took the script the pod builder emits, cut it after `BODY=` and posted those
// exact bytes at a real daemon: `400 ... json: unknown field "pod_uid"`, and
// then `InspectHold` said the handoff held nothing. A script that parses and is
// refused is a capture-selected producer that never starts, and every hold in
// every other suite is posted by Go rather than by the script, so nothing could
// see it.
//
// So the script is RUN here. `sh` executes the generated text with the
// environment the init container is given, and the daemon on the other end is
// `cmd/hangar-output-daemon` itself. The only thing the test supplies is a
// `wget` -- BusyBox has one and macOS does not -- and the shim is transport
// only: it re-spells the flags for curl and passes the body through byte for
// byte from a file it never parses.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/hangar/executioncontrol"
	hangaroutput "github.com/concourse/concourse/hangar/output"
)

const drivePodUID = executioncontrol.PodUID("dddddddd-dddd-4ddd-8ddd-dddddddddddd")

// wgetOnPath returns a directory to prepend to PATH that contains a `wget`.
//
// On a CI runner that has one, that is the runner's own: nothing is shimmed and
// the script is exercised against the real program. Otherwise a shim is written
// that translates ONLY the transport flags. It must never touch the body, so
// the body goes to a file and curl sends the file; a shim that re-quoted the
// JSON would be testing the shim.
func wgetOnPath(t *testing.T) string {
	t.Helper()

	if found, err := exec.LookPath("wget"); err == nil {
		return filepath.Dir(found)
	}
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("neither wget nor curl is available to drive the generated script")
	}

	dir := t.TempDir()
	shim := `#!/bin/sh
set -u
HEADERS="$(mktemp)"
BODY="$(mktemp)"
URL=""
INSECURE=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -q) ;;
    -O) shift ;;
    --no-check-certificate) INSECURE="-k" ;;
    --header=*) printf '%s\n' "${1#--header=}" >> "$HEADERS" ;;
    --post-data=*) printf '%s' "${1#--post-data=}" > "$BODY" ;;
    -*) ;;
    *) URL="$1" ;;
  esac
  shift
done
set --
while IFS= read -r line; do set -- "$@" -H "$line"; done < "$HEADERS"

exec curl -s --fail $INSECURE "$@" --data-binary "@$BODY" "$URL"
`
	path := filepath.Join(dir, "wget")
	if err := os.WriteFile(path, []byte(shim), 0o755); err != nil {
		t.Fatalf("writing the wget shim: %v", err)
	}

	return dir
}

// driveCaptureHold runs the generated script for a capture whose incarnation
// the daemon has really reserved, and returns what the script printed.
func driveCaptureHold(t *testing.T, harness *outputDaemonHarness, cfg Config) (string, error) {
	t.Helper()

	identity := executioncontrol.Identity{
		ExecutionID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Fence: 1,
	}
	admission := hangaroutput.CaptureAdmission{
		ProtocolVersion: hangaroutput.ProtocolVersion,
		Execution:       identity,
		ActivationEpoch: harnessEpoch,
		HandoffID:       "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
		SourceLeaseID:   "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
		Output:          "result",
		CaptureDeadline: hangaroutput.NewTimestamp(time.Now().UTC().Add(time.Hour)),
	}

	// Admitted and reserved through the real client, in the real order: no Pod
	// exists yet and neither call names one.
	if _, err := harness.Client.Admit(t.Context(), executioncontrol.Envelope{
		ProtocolVersion: executioncontrol.ProtocolVersion,
		Identity:        identity,
		ActivationEpoch: harnessEpoch,
		NodeUID:         harnessNodeUID,
		Capability:      "base-capability",
	}); err != nil {
		return "", fmt.Errorf("admitting: %w", err)
	}
	reserved, err := harness.Client.ReserveIncarnation(t.Context(), admission)
	if err != nil {
		return "", fmt.Errorf("reserving: %w", err)
	}
	grant, err := harness.Client.MintGrant(hangaroutput.CaptureFacet, "hold", identity)
	if err != nil {
		return "", fmt.Errorf("minting the grant: %w", err)
	}

	// The control envelope the pod builder reads, carrying the daemon's own
	// answers. Endpoint is left EMPTY on purpose: that is the deployed shape,
	// where the script composes its own endpoint from the Downward API host IP
	// and the port -- which is the line F2 is about.
	control := &runtime.ExecutionControl{
		Version:         runtime.ExecutionControlVersion,
		Phase:           runtime.ControlPhaseAdmitted,
		Identity:        identity,
		ActivationEpoch: harnessEpoch,
		Capability:      "base-capability",
	}
	if err := control.SelectCapture(runtime.DurableOutputCapture{
		Version:             runtime.DurableOutputCaptureVersion,
		Identity:            identity,
		ActivationEpoch:     harnessEpoch,
		HandoffID:           admission.HandoffID,
		SourceLeaseID:       admission.SourceLeaseID,
		Output:              string(admission.Output),
		SourceControlGrant:  executioncontrol.ControlCapability(grant),
		CaptureDeadline:     admission.CaptureDeadline.Time,
		ReservedIncarnation: reserved.Incarnation,
		ReservedDirectory:   reserved.Directory,
		ReservingNode:       testReservingNode,
	}); err != nil {
		return "", fmt.Errorf("selecting the capture: %w", err)
	}

	container := capturingContainer(t, cfg, false, control)
	init := container.buildCaptureControlInitContainer()
	if init == nil {
		return "", fmt.Errorf("a capture-selected container built no control init")
	}

	// The script, and the environment the kubelet would give it. The Downward
	// API values are the ones no ATC can compose, so they are supplied here the
	// way the kubelet supplies them.
	host, port, err := hostAndPortOf(harness.Endpoint)
	if err != nil {
		return "", err
	}
	env := []string{
		"PATH=" + wgetOnPath(t) + string(os.PathListSeparator) + os.Getenv("PATH"),
		captureEnvPodUID + "=" + string(drivePodUID),
		captureEnvNodeName + "=" + testReservingNode,
		captureEnvHostIP + "=" + host,
	}
	for _, variable := range init.Env {
		if variable.ValueFrom != nil {
			continue
		}
		value := variable.Value
		// The port the harness actually listens on. Everything else is the
		// builder's own value, unedited.
		if variable.Name == captureEnvOutputPort {
			value = strconv.Itoa(port)
		}
		env = append(env, variable.Name+"="+value)
	}

	scriptPath := filepath.Join(t.TempDir(), "hold.sh")
	if err := os.WriteFile(scriptPath, []byte(init.Command[2]), 0o600); err != nil {
		return "", err
	}
	// One attempt: the retry loop is a wait for admission, and this daemon is
	// already admitted. A test that let it run 60 times would take a minute to
	// report a refusal.
	env = append(env, captureEnvHoldTries+"=1")

	command := exec.CommandContext(t.Context(), "sh", scriptPath)
	command.Env = env
	out, runErr := command.CombinedOutput()

	// The hold really landed, or it did not. The script's own exit status is
	// the producer's gate; the daemon's record is the truth behind it.
	if _, err := harness.Client.InspectHold(t.Context(), identity, admission.HandoffID); err != nil {
		return string(out), fmt.Errorf("the daemon holds no source after the script ran "+
			"(script: %v): %w", runErr, err)
	}

	return string(out), runErr
}

func hostAndPortOf(endpoint string) (string, int, error) {
	trimmed := endpoint
	for _, prefix := range []string{"https://", "http://"} {
		trimmed = strings.TrimPrefix(trimmed, prefix)
	}
	host, port, found := strings.Cut(trimmed, ":")
	if !found {
		return "", 0, fmt.Errorf("the harness endpoint %q names no port", endpoint)
	}
	number, err := strconv.Atoi(port)
	if err != nil {
		return "", 0, err
	}

	return host, number, nil
}

// The generated script establishes a hold at a plaintext daemon.
func TestTheGeneratedControlInitScriptEstablishesAHoldAtARealDaemon(t *testing.T) {
	harness, err := startOutputDaemon()
	if err != nil {
		t.Fatalf("starting the output daemon: %v", err)
	}
	t.Cleanup(harness.Stop)

	out, err := driveCaptureHold(t, harness, capturePodConfig(true))
	if err != nil {
		t.Fatalf("the generated control init did not establish a hold: %v\n%s", err, out)
	}
	if !strings.Contains(out, "hold acknowledged") {
		t.Errorf("the script did not report an acknowledged hold:\n%s", out)
	}
}

// The same script against a daemon whose listener is TLS.
//
// J6 wrapped the ONE control listener in tls.NewListener, so the node-local
// exemption is a client-CERTIFICATE exemption and not a plaintext port. The
// script defaulted its endpoint to `http://` and dialled with no TLS options at
// all, which is `400 Client sent an HTTP request to an HTTPS server` -- and
// then, once the scheme was right, an unverifiable certificate, because the
// init dials the node by IP and no SAN covers that.
//
// The cleanup init has spoken this correctly since the artifact daemon got
// mTLS; this reuses its two helpers rather than inventing a third spelling.
func TestTheGeneratedControlInitScriptEstablishesAHoldOverTLS(t *testing.T) {
	harness, err := startTLSOutputDaemon()
	if err != nil {
		t.Fatalf("starting the TLS output daemon: %v", err)
	}
	t.Cleanup(harness.Stop)

	cfg := capturePodConfig(true)
	cfg.ArtifactDaemonTLSEnabled = true

	// The control, first: the script it generates dials https.
	control := capturingContainer(t, cfg, false, admittedCapture()).buildCaptureControlInitContainer()
	if control == nil {
		t.Fatal("a capture-selected container built no control init")
	}
	if !strings.Contains(control.Command[2], "https://") {
		t.Errorf("the control init composes a plaintext endpoint at a TLS daemon:\n%s",
			control.Command[2])
	}

	out, err := driveCaptureHold(t, harness, cfg)
	if err != nil {
		t.Fatalf("the generated control init did not establish a hold over TLS: %v\n%s", err, out)
	}
	if !strings.Contains(out, "hold acknowledged") {
		t.Errorf("the script did not report an acknowledged hold over TLS:\n%s", out)
	}
}
