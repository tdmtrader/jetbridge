package jetbridge

// The capture-selected pod's control init container.
//
// Requirement 3 puts one ordering ahead of everything else: after the exact
// source incarnation exists but BEFORE the producer main process may start, the
// daemon durably acknowledges a provisional source hold. In a Pod that means an
// init container, because an init container is the only thing Kubernetes runs
// before the containers -- and it must be the FIRST init container, because
// every other one this pod builds writes into the very tree the hold protects:
// `cleanup-stale` removes it, `artifact-fetch` stages inputs into it.
//
// Requirement 24 decides where the credential goes. The task and sidecar
// containers receive no GCS credential, no receipt key, no materialization key
// and no publication capability, and the source-control grant is the capture
// extension's own attenuated capability -- so it is carried by THIS container
// and by nothing else in the pod. `buildPod` never copies it into the main
// container's env, and the capture-pod feature asserts the presence half
// before the absence half, because "no credential" passes on a worker that
// ignores its configuration entirely.
//
// The Pod, not the ATC, learns its own identity. Pod UID and node name come
// from the Downward API because the ATC cannot know either at the moment it
// composes the Pod: the UID does not exist until the API server assigns one,
// and the node does not exist until the scheduler binds. A control plane that
// filled these in would be filling in a guess.

import (
	"fmt"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/hangar/output"
)

// captureReservationAnnotation is where a capture-selected Pod says which
// incarnation it mounted.
//
// It exists for the one caller that has no ContainerSpec: `LookupContainer`
// builds its Container with an empty one -- there is nothing behind a lookup
// but a handle and a DB row -- and the hijack refusal Req 18 requires cannot
// ask about the incarnation it has never heard of. The handle is a SIBLING of
// the incarnation, so a guard that fell back to it could only ever be told
// `unmanaged`.
//
// The Pod is the right place for it: it is the object that exists for exactly
// as long as the thing being hijacked, the ATC already annotates it with the
// exit status, and the value is the daemon's own answer repeated rather than
// anything composed here.
const captureReservationAnnotation = "concourse.dev/hangar-reserved-directory"

// captureControlInitName is the container the hold is established from. It is
// a constant because the ordering assertion names it.
const captureControlInitName = "hangar-capture-control"

// The env names the control init reads. They are declared once here so the
// script and the pod builder cannot drift, and so a test can name them.
const (
	captureEnvProtocol   = "HANGAR_PROTOCOL_VERSION"
	captureEnvEndpoint   = "HANGAR_CONTROL_ENDPOINT"
	captureEnvExecution  = "HANGAR_EXECUTION_ID"
	captureEnvFence      = "HANGAR_EXECUTION_FENCE"
	captureEnvEpoch      = "HANGAR_ACTIVATION_EPOCH"
	captureEnvHandoff    = "HANGAR_HANDOFF_ID"
	captureEnvLease      = "HANGAR_SOURCE_LEASE_ID"
	captureEnvOutput     = "HANGAR_OUTPUT_NAME"
	captureEnvDeadline   = "HANGAR_CAPTURE_DEADLINE"
	captureEnvPodUID     = "HANGAR_POD_UID"
	captureEnvNodeName   = "HANGAR_NODE_NAME"
	captureEnvHostIP     = "HANGAR_HOST_IP"
	captureEnvGrant      = "HANGAR_SOURCE_CONTROL_GRANT"
	captureEnvHoldTries  = "HANGAR_HOLD_ATTEMPTS"
	captureEnvOutputPort = "HANGAR_OUTPUT_DAEMON_PORT"

	// The reserved incarnation, field by field. The control init PRESENTS it
	// at the hold, and presenting it is what proves this container is running
	// in the Pod the reservation was made for -- the daemon refuses a hold for
	// any other. The execution id and the output name are already above; these
	// are the two that are not.
	captureEnvIncarnationNode       = "HANGAR_INCARNATION_NODE"
	captureEnvIncarnationGeneration = "HANGAR_INCARNATION_GENERATION"
)

// hangarCredentialEnvNames is what "carries no Hangar credential" means, in
// one place. A container carrying any of these holds authority over the output
// plane; the task and its sidecars hold none of them.
func hangarCredentialEnvNames() []string {
	return []string{captureEnvGrant, captureEnvGrantLegacy}
}

// captureEnvGrantLegacy is deliberately a distinct spelling with no producer.
// The absence check scans for a SET of names rather than one, so a rename that
// moved the grant to a new variable would not silently pass a scan that only
// knew the old name.
const captureEnvGrantLegacy = "HANGAR_CAPTURE_CAPABILITY"

// defaultHoldAttempts bounds the init container's wait for the control plane.
//
// The init container runs as soon as the kubelet starts the Pod, and the ATC
// admits the exact execution as soon as the scheduler binds it -- two events
// with no ordering between them. So the hold RETRIES while the daemon says the
// execution is not admitted yet, and fails closed when the budget runs out:
// Req 3's "start and recovery fail closed until the hold matches current
// execution admission" is a refusal to start, not a hold taken on faith.
const defaultHoldAttempts = 60

// buildCaptureControlInitContainer returns the init container that establishes
// the provisional source hold, or nil when this execution selected no capture.
func (c *Container) buildCaptureControlInitContainer() *corev1.Container {
	control := c.containerSpec.ExecutionControl
	if !control.HasDurableOutputCapture() {
		return nil
	}
	capture := control.Capture

	port := c.config.OutputDaemonPort
	if port == 0 {
		port = DefaultOutputDaemonPort
	}

	allowEscalation := false

	return &corev1.Container{
		Name:  captureControlInitName,
		Image: c.helperImage(),
		Command: []string{"sh", "-c", captureHoldScript(outputDaemonURLScheme(),
			outputWgetTLSOptions())},
		Env: append([]corev1.EnvVar{
			{Name: captureEnvProtocol, Value: output.ProtocolVersion},
			{Name: captureEnvEndpoint, Value: control.Endpoint},
			{Name: captureEnvExecution, Value: string(control.Identity.ExecutionID)},
			{Name: captureEnvFence, Value: strconv.FormatUint(uint64(control.Identity.Fence), 10)},
			{Name: captureEnvEpoch, Value: strconv.FormatUint(uint64(control.ActivationEpoch), 10)},
			{Name: captureEnvHandoff, Value: string(capture.HandoffID)},
			{Name: captureEnvLease, Value: string(capture.SourceLeaseID)},
			{Name: captureEnvOutput, Value: capture.Output},
			{Name: captureEnvDeadline, Value: capture.CaptureDeadline.UTC().Format(time.RFC3339Nano)},
			{Name: captureEnvOutputPort, Value: strconv.Itoa(port)},
			{Name: captureEnvHoldTries, Value: strconv.Itoa(defaultHoldAttempts)},

			// The reservation, repeated. The ATC composed none of it: these
			// values came off the wire from `reserve-incarnation`, and the
			// same reservation is the hostPath of the selected output's
			// volume in this very Pod.
			{Name: captureEnvIncarnationNode,
				Value: string(capture.ReservedIncarnation.NodeUID)},
			{Name: captureEnvIncarnationGeneration, Value: strconv.FormatUint(
				uint64(capture.ReservedIncarnation.HandleGeneration), 10)},

			// The credential, in this container and in no other.
			{Name: captureEnvGrant, Value: string(capture.SourceControlGrant)},
		}, downwardAPIPodAndNodeFields()...),
		ImagePullPolicy: corev1.PullIfNotPresent,
		SecurityContext: &corev1.SecurityContext{
			AllowPrivilegeEscalation: &allowEscalation,
		},
	}
}

// downwardAPIPodAndNodeFields are the exact field refs the hold binds itself
// to. They are exact rather than "whatever the pod knows": `metadata.uid` is
// the Pod incarnation a writer ticket is bound to, and a recreated Pod is a new
// UID that may not write; `spec.nodeName` and `status.hostIP` are how the
// container reaches the daemon that owns this node's ledger.
func downwardAPIPodAndNodeFields() []corev1.EnvVar {
	fieldRef := func(name, path string) corev1.EnvVar {
		return corev1.EnvVar{
			Name: name,
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{FieldPath: path},
			},
		}
	}

	return []corev1.EnvVar{
		fieldRef(captureEnvPodUID, "metadata.uid"),
		fieldRef(captureEnvNodeName, "spec.nodeName"),
		fieldRef(captureEnvHostIP, "status.hostIP"),
	}
}

// captureHoldScript is the POSIX sh the control init runs.
//
// It is BusyBox-compatible on purpose: the helper image is the one the artifact
// init containers already use, and `wget -O -` plus `case` is the whole
// vocabulary. What it must not do is invent an answer -- an unreachable daemon,
// a refusal and a malformed body are all "no hold", and no hold means the
// producer does not start.
//
// The scheme and the wget options come from the OUTPUT plane's own two
// helpers, and no longer from the artifact daemon's. J6 wrapped the output
// daemon's ONE listener in tls.NewListener, so the node-local hold exemption is
// a client-CERTIFICATE exemption and not a plaintext port: a script that
// hard-coded `http://` -- or that derived its scheme from
// `artifactDaemon.tls.enabled`, a switch belonging to a different daemon and
// false by default, which is what it used to do -- is answered "Client sent an
// HTTP request to an HTTPS server" and the producer never starts.
func captureHoldScript(scheme, wgetOpts string) string {
	return fmt.Sprintf(`
set -u
ENDPOINT="${%[2]s}"
if [ -z "${ENDPOINT}" ]; then
  ENDPOINT="%[19]s://${%[11]s}:${%[15]s}"
fi
WGET_OPTS="%[20]s"
INCARNATION='{"execution_id":"'"${%[3]s}"'","node_uid":"'"${%[17]s}"'","handle_generation":'"${%[18]s}"',"output":"'"${%[8]s}"'"}'
BODY='{"protocol_version":"'"${%[1]s}"'","execution":{"execution_id":"'"${%[3]s}"'","fence":'"${%[4]s}"'},"activation_epoch":'"${%[5]s}"',"handoff_id":"'"${%[6]s}"'","source_lease_id":"'"${%[7]s}"'","output":"'"${%[8]s}"'","capture_deadline_at":"'"${%[9]s}"'","pod_uid":"'"${%[10]s}"'","incarnation":'"${INCARNATION}"'}'
echo "[hangar-capture-control] holding the source for output ${%[8]s} on node ${%[12]s} (pod ${%[10]s})" >&2
ATTEMPT=0
while [ "${ATTEMPT}" -lt "${%[13]s}" ]; do
  ATTEMPT=$((ATTEMPT+1))
  RESP="$(wget ${WGET_OPTS} -q -O - --header="Content-Type: application/json" --header="%[14]s: ${%[16]s}" --post-data="${BODY}" "${ENDPOINT}/capture/v1/hold" 2>/dev/null || true)"
  case "${RESP}" in
    *'"kind":"hold_acknowledged"'*)
      echo "[hangar-capture-control] hold acknowledged" >&2
      exit 0
      ;;
  esac
  echo "[hangar-capture-control] no hold yet (attempt ${ATTEMPT}): ${RESP}" >&2
  sleep 1
done
echo "[hangar-capture-control] the daemon never acknowledged the source hold; the producer must not start" >&2
exit 1
`,
		captureEnvProtocol,   // 1
		captureEnvEndpoint,   // 2
		captureEnvExecution,  // 3
		captureEnvFence,      // 4
		captureEnvEpoch,      // 5
		captureEnvHandoff,    // 6
		captureEnvLease,      // 7
		captureEnvOutput,     // 8
		captureEnvDeadline,   // 9
		captureEnvPodUID,     // 10
		captureEnvHostIP,     // 11
		captureEnvNodeName,   // 12
		captureEnvHoldTries,  // 13
		CapabilityHeaderName, // 14
		captureEnvOutputPort, // 15
		captureEnvGrant,      // 16

		captureEnvIncarnationNode,       // 17
		captureEnvIncarnationGeneration, // 18

		scheme,   // 19
		wgetOpts, // 20
	)
}

// CapabilityHeaderName is the header the output daemon reads an attenuated
// control capability from. It is restated here rather than imported because
// cmd/hangar-output-daemon is package main; capture_control_test.go pins the
// two spellings against each other so they cannot drift.
const CapabilityHeaderName = "Hangar-Control-Capability"

// DefaultOutputDaemonPort is the output daemon's control port.
const DefaultOutputDaemonPort = 7781

func (c *Container) helperImage() string {
	if c.config.ArtifactHelperImage != "" {
		return c.config.ArtifactHelperImage
	}

	return DefaultArtifactHelperImage
}

// captureReservedDirectory is the daemon-issued directory the selected output's
// volume must resolve to, or "" when nothing is captured.
//
// It is a READ of a field that came off the wire. There is deliberately no
// function here that builds one: Req 7 says no API accepts a caller-chosen
// path, and a control plane that could spell a source directory could spell a
// stale generation pointing at somebody else's live source.
func captureReservedDirectory(spec runtime.ContainerSpec) string {
	if !spec.ExecutionControl.HasDurableOutputCapture() {
		return ""
	}

	return spec.ExecutionControl.Capture.ReservedDirectory
}

// captureSelectedOutputName is the name of the one output selected for capture,
// or "" when nothing is captured.
func captureSelectedOutputName(spec runtime.ContainerSpec) string {
	if !spec.ExecutionControl.HasDurableOutputCapture() {
		return ""
	}

	return spec.ExecutionControl.Capture.Output
}

// captureSelectedOutputPath is the container path of the one selected output,
// or "" when nothing is captured.
func captureSelectedOutputPath(spec runtime.ContainerSpec) string {
	if !spec.ExecutionControl.HasDurableOutputCapture() {
		return ""
	}

	return spec.Outputs[spec.ExecutionControl.Capture.Output]
}
