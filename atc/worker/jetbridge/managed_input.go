package jetbridge

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/hangar/output"
	corev1 "k8s.io/api/core/v1"
)

// managedInputReadyTimeout covers scheduling/startup and the sequential input
// initializers. Their leases are admitted together before the pod is created,
// so each must protect the tree through this entire bounded queue. Retries
// share this total budget rather than restarting it.
func managedInputReadyTimeout(config Config, count int) time.Duration {
	return max(podStartupTimeout(config), podSchedulingTimeout(config)) +
		time.Duration(count)*output.ReadTransferTimeout(config.OutputOperationTimeout)
}

func (p *execProcess) podReadyTimeout() time.Duration {
	count := 0
	if p.container != nil {
		for _, input := range p.container.containerSpec.Inputs {
			if input.HangarRead != nil {
				count++
			}
		}
	}
	return managedInputReadyTimeout(p.config, count)
}

func (b *DaemonSetBackend) managedInputInit(handle string, input runtime.Input, volumes []corev1.Volume, mounts []corev1.VolumeMount, index int) (corev1.Container, error) {
	var empty corev1.Container
	read := input.HangarRead
	if !b.config.HangarEnabled || !b.config.OutputPlaneEnabled || read == nil || read.Validate() != nil || input.HangarTree == nil || input.Artifact != nil || read.Ref != *input.HangarTree {
		return empty, fmt.Errorf("managed input requires the output plane and exact read authority")
	}
	name := volumeNameForMountPath(mounts, input.DestinationPath)
	if name == "" || read.Destination.Handle != handle || read.Destination.Volume != name || hostPathForVolume(volumes, name) != filepath.Join(b.config.ArtifactDaemonHostPath, "steps", handle, name) {
		return empty, fmt.Errorf("managed input read authority does not match its node-local volume")
	}
	payload, err := json.Marshal(read)
	if err != nil {
		return empty, err
	}
	if len(payload) > maxHangarMaterializationBytes {
		return empty, fmt.Errorf("managed input materialization exceeds its request bound")
	}
	receipt, err := json.Marshal(read.Ref)
	if err != nil {
		return empty, err
	}
	port := b.config.OutputDaemonPort
	if port == 0 {
		port = 7781
	}
	allowEscalation := false
	return corev1.Container{
		Name: fmt.Sprintf("materialize-run-input-%d", index), Image: b.helperImage(), ImagePullPolicy: corev1.PullIfNotPresent,
		Command:         []string{"sh", "-c", fmt.Sprintf(managedInputScript, port, base64.StdEncoding.EncodeToString(payload), base64.StdEncoding.EncodeToString(receipt), int64(output.ReadTransferTimeout(b.config.OutputOperationTimeout).Seconds()))},
		Env:             []corev1.EnvVar{{Name: "HOST_IP", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "status.hostIP"}}}},
		VolumeMounts:    []corev1.VolumeMount{{Name: name, MountPath: "/hangar-input", ReadOnly: true}},
		SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: &allowEscalation},
	}, nil
}

// The local sealed receipt is the success authority, including after the
// daemon committed but its response was lost. Nothing writable is mounted in
// this init. HTTP error bodies and the lease token never enter task logs.
const managedInputScript = `set -u
umask 077
PORT=%d
REQUEST_B64='%s'
RECEIPT_B64='%s'
READ_TIMEOUT_SECONDS=%d
ROOT=/hangar-input
TMP_DIR=$(mktemp -d "${TMPDIR:-/tmp}/hangar-input.XXXXXX") || exit 1
trap 'rm -rf "$TMP_DIR"' 0
trap 'exit 1' 1 2 15
printf '%%s' "$REQUEST_B64" | base64 -d >"$TMP_DIR/request" || exit 1
printf '%%s' "$RECEIPT_B64" | base64 -d >"$TMP_DIR/expected" || exit 1
mode_of() {
  stat -c '%%a' "$1" 2>/dev/null || stat -f '%%Lp' "$1" 2>/dev/null
}
verified() {
  [ ! -L "$ROOT" ] && [ -d "$ROOT" ] && [ "$(mode_of "$ROOT")" = 555 ] &&
  [ ! -L "$ROOT/.hangar-materialized" ] && [ -f "$ROOT/.hangar-materialized" ] &&
  [ "$(mode_of "$ROOT/.hangar-materialized")" = 444 ] &&
  cmp "$ROOT/.hangar-materialized" "$TMP_DIR/expected" >/dev/null 2>&1
}
ATTEMPT=0
while [ "$ATTEMPT" -lt 5 ]; do
  verified && exit 0
  ATTEMPT=$((ATTEMPT + 1))
  : >"$TMP_DIR/response"
  : >"$TMP_DIR/headers"
  wget --no-check-certificate -S -q -O "$TMP_DIR/response" -T "$READ_TIMEOUT_SECONDS" --header='Content-Type: application/json' --post-file="$TMP_DIR/request" "https://${HOST_IP}:${PORT}/read/v1/materialize" 2>"$TMP_DIR/headers"
  verified && exit 0
  HTTP_STATUS=$(sed -n 's/^[[:space:]]*HTTP\/[0-9.]* \([0-9][0-9][0-9]\).*/\1/p' "$TMP_DIR/headers" | tail -n 1)
  if [ -n "$HTTP_STATUS" ] && [ "$HTTP_STATUS" != 503 ]; then
    printf 'managed input materialization was refused or could not be verified\n' >&2
    exit 1
  fi
  [ "$ATTEMPT" -ge 5 ] || sleep 2
done
printf 'managed input materialization is unavailable\n' >&2
exit 1
`
