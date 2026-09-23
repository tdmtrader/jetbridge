package jetbridge

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/concourse/concourse/artifactcap"
	"github.com/concourse/concourse/artifactwire"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
	corev1 "k8s.io/api/core/v1"
)

const artifactDaemonHostPathVolumeName = "artifact-daemon-hostpath"

const (
	maxHangarMaterializationItems = 64
	maxHangarMaterializationBytes = 64 << 10
)

// Compile-time check that DaemonSetBackend satisfies StorageBackend.
var _ StorageBackend = (*DaemonSetBackend)(nil)

type DaemonSetBackend struct {
	config          Config
	resolveSigner   *artifactcap.Signer
	artifactLocator *ArtifactLocator
	nodeIPResolver  *NodeIPResolver
	daemonClient    *DaemonClient
	warmNegative    *warmNegativeCache
	wire            *artifactwire.Client
}

func NewDaemonSetBackend(config Config, locator *ArtifactLocator, resolver *NodeIPResolver) *DaemonSetBackend {
	if config.ArtifactDaemonWarmTimeout <= 0 {
		config.ArtifactDaemonWarmTimeout = defaultWarmTimeout
	}

	// A configured key ALWAYS signs. The chart hands the key to the daemon and
	// the web off the SAME value, so a key here means the daemon is requiring
	// one — and quietly not signing is then not a safe degraded mode, it 403s
	// every resolve on the node with no diagnostic.
	// ValidateResolveCapabilityConfig refuses the misconfigurations at
	// startup (TTL at or below the floor, malformed key); if an unvalidated
	// config reaches here anyway, a short-TTL capability 403s only pods
	// slower than the TTL, which still beats not signing at all.
	var signer *artifactcap.Signer
	if len(config.ArtifactDaemonResolveCapabilityKey) != 0 {
		signer, _ = artifactcap.NewSigner(config.ArtifactDaemonResolveCapabilityKey)
	}

	return &DaemonSetBackend{
		config:          config,
		resolveSigner:   signer,
		artifactLocator: locator,
		nodeIPResolver:  resolver,
		warmNegative:    newWarmNegativeCache(),
		wire:            newWireClient(config),
	}
}

// SetDaemonClient sets the DaemonClient used for probing daemon pods for
// cached resources. Must be called after construction when the K8s clientset
// is available.
func (b *DaemonSetBackend) SetDaemonClient(client *DaemonClient) {
	b.daemonClient = client
}

func (b *DaemonSetBackend) StepVolume(name, handle, subdir string) corev1.Volume {
	dirType := corev1.HostPathDirectoryOrCreate
	return corev1.Volume{
		Name: name,
		VolumeSource: corev1.VolumeSource{
			HostPath: &corev1.HostPathVolumeSource{
				Path: filepath.Join(b.config.ArtifactDaemonHostPath, "steps", handle, subdir),
				Type: &dirType,
			},
		},
	}
}

// ReservedIncarnationVolume mounts the location the output daemon reserved.
//
// It joins the node's artifact root, the managed steps directory and the
// daemon's own answer, and it derives nothing else: `reservedDir` is
// `ReservedIncarnation.Directory` verbatim, which the ATC validated against the
// incarnation beside it before it ever reached here.
//
// The type is DirectoryOrCreate for the same reason StepVolume's is, and it is
// very nearly moot: the reservation already created the directory under the
// daemon's own root, with the daemon's ownership, before this Pod was built.
// That ordering is the point of reserving at all.
func (b *DaemonSetBackend) ReservedIncarnationVolume(name, reservedDir string) corev1.Volume {
	dirType := corev1.HostPathDirectoryOrCreate

	return corev1.Volume{
		Name: name,
		VolumeSource: corev1.VolumeSource{
			HostPath: &corev1.HostPathVolumeSource{
				Path: filepath.Join(b.config.ArtifactDaemonHostPath, "steps", reservedDir),
				Type: &dirType,
			},
		},
	}
}

func (b *DaemonSetBackend) CacheVolume(name string, identity atc.TaskCacheIdentity, stepName, cachePath string) corev1.Volume {
	basePath := b.config.CacheHostPath
	if basePath == "" {
		basePath = filepath.Join(b.config.ArtifactDaemonHostPath, "caches")
	}
	dirType := corev1.HostPathDirectoryOrCreate
	key := stableCacheKey(identity, stepName, cachePath)
	return corev1.Volume{
		Name: name,
		VolumeSource: corev1.VolumeSource{
			HostPath: &corev1.HostPathVolumeSource{
				Path: filepath.Join(basePath, key),
				Type: &dirType,
			},
		},
	}
}

func (b *DaemonSetBackend) ArtifactStoreVolume(containerType db.ContainerType) *corev1.Volume {
	if containerType == db.ContainerTypeCheck {
		return nil
	}
	dirType := corev1.HostPathDirectoryOrCreate
	return &corev1.Volume{
		Name: artifactDaemonHostPathVolumeName,
		VolumeSource: corev1.VolumeSource{
			HostPath: &corev1.HostPathVolumeSource{
				Path: b.config.ArtifactDaemonHostPath,
				Type: &dirType,
			},
		},
	}
}

func (b *DaemonSetBackend) ArtifactStoreVolumeName() string {
	return artifactDaemonHostPathVolumeName
}

// artifactwire.ResolveRequest is a single key/dest pair for the /resolve-batch endpoint.
// wireTreeRef converts at this edge: the wire module carries a shape-only
// TreeRef because it cannot import hangar (ADR-0002), and the daemon
// converts back at its own edge.
func wireTreeRef(ref hangar.TreeRef) artifactwire.TreeRef {
	return artifactwire.TreeRef{Scope: string(ref.Scope), Digest: string(ref.Digest), Generation: ref.Generation}
}

func (b *DaemonSetBackend) BuildFetchInitContainers(handle string, inputs []runtime.Input, podVolumes []corev1.Volume, mainMounts []corev1.VolumeMount) ([]corev1.Container, error) {
	helperImage := b.helperImage()
	allowEscalation := false

	var items []artifactwire.ResolveRequest
	var mounts []corev1.VolumeMount
	seenVolumes := map[string]bool{}
	var hangarItems []artifactwire.MaterializationItem
	var hangarMounts []corev1.VolumeMount
	var hangarReceiptBytes []string
	seenHangarVolumes := map[string]bool{}

	for _, input := range inputs {
		if input.HangarTree != nil {
			if !b.config.HangarEnabled {
				return nil, fmt.Errorf("Hangar tree input requires Hangar to be enabled")
			}
			if b.config.HangarWarrantSigner == nil {
				return nil, fmt.Errorf("Hangar tree input requires a materialization warrant signer")
			}
			if err := input.HangarTree.Validate(); err != nil {
				return nil, fmt.Errorf("invalid Hangar tree input: %w", err)
			}
			volumeName := volumeNameForMountPath(mainMounts, input.DestinationPath)
			if volumeName == "" {
				return nil, fmt.Errorf("Hangar tree input %q has no task volume mount", input.DestinationPath)
			}
			expectedHostPath := filepath.Join(b.config.ArtifactDaemonHostPath, "steps", handle, volumeName)
			if actualHostPath := hostPathForVolume(podVolumes, volumeName); actualHostPath != expectedHostPath {
				return nil, fmt.Errorf("Hangar tree input %q volume does not resolve to its exact node-local destination", input.DestinationPath)
			}
			warrant, err := b.config.HangarWarrantSigner.Sign(*input.HangarTree, handle, volumeName)
			if err != nil {
				return nil, fmt.Errorf("sign Hangar tree input warrant: %w", err)
			}
			hangarItems = append(hangarItems, artifactwire.MaterializationItem{
				Ref: wireTreeRef(*input.HangarTree), Handle: handle, Volume: volumeName, Warrant: "Bearer " + warrant,
			})
			receipt, err := json.Marshal(*input.HangarTree)
			if err != nil {
				return nil, fmt.Errorf("marshal expected Hangar materialization receipt: %w", err)
			}
			hangarReceiptBytes = append(hangarReceiptBytes, base64.StdEncoding.EncodeToString(receipt))
			if !seenHangarVolumes[volumeName] {
				seenHangarVolumes[volumeName] = true
				hangarMounts = append(hangarMounts, corev1.VolumeMount{
					Name: volumeName, MountPath: fmt.Sprintf("/hangar-inputs/input-%d", len(hangarMounts)), ReadOnly: true,
				})
			}
			continue
		}

		// (*Container).validateInputs refuses an input with neither an
		// Artifact nor a HangarTree before buildPod gets here, and neither
		// producer of runtime.Input can emit one. This is an exported method,
		// so it refuses too, rather than skipping: a silently skipped input
		// arrives as an empty directory with nothing in the build log to say
		// why.
		if input.Artifact == nil {
			return nil, fmt.Errorf("input %q has no artifact to fetch", input.DestinationPath)
		}

		volumeName := volumeNameForMountPath(mainMounts, input.DestinationPath)
		if volumeName == "" {
			continue
		}

		key := ArtifactKey(input.Artifact.Handle())
		daemonKey := key
		if loc, hasLoc := b.artifactLocate(key); hasLoc {
			daemonKey = loc.HostDir
		}

		hostDestPath := hostPathForVolume(podVolumes, volumeName)
		if hostDestPath == "" {
			hostDestPath = filepath.Join(b.config.ArtifactDaemonHostPath, "steps", handle, volumeName)
		}

		item := artifactwire.ResolveRequest{Key: daemonKey, Dest: hostDestPath}
		if b.resolveSigner != nil {
			capability, err := b.resolveSigner.SignResolve(daemonKey, hostDestPath, resolveCapabilityExpiry(b.config))
			if err != nil {
				return nil, fmt.Errorf("sign resolve capability for %q: %w", daemonKey, err)
			}
			item.Capability = capability
		}
		items = append(items, item)

		if !seenVolumes[volumeName] {
			seenVolumes[volumeName] = true
			mounts = append(mounts, corev1.VolumeMount{Name: volumeName, MountPath: input.DestinationPath})
		}
	}

	var initContainers []corev1.Container

	// Prepend the hostpath volume mount.
	allMounts := append([]corev1.VolumeMount{
		{Name: artifactDaemonHostPathVolumeName, MountPath: ArtifactMountPath, ReadOnly: true},
	}, mounts...)

	envVars := []corev1.EnvVar{
		{
			Name: "HOST_IP",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{FieldPath: "status.hostIP"},
			},
		},
	}

	// The init reaches the same-node daemon through ${HOST_IP}:7780. BusyBox wget
	// cannot authenticate the node-IP endpoint because it is not a certificate
	// SAN, and NetworkPolicy enforcement is optional and CNI-dependent. Strict
	// Hangar success therefore does not trust the transport response alone: the
	// init verifies the daemon's sealed receipt through the read-only input mount.

	if len(items) > 0 {
		initContainers = append(initContainers, corev1.Container{
			Name:            "fetch-inputs",
			Image:           helperImage,
			Command:         b.daemonResolveBatchCommand(items),
			Env:             envVars,
			VolumeMounts:    allMounts,
			ImagePullPolicy: corev1.PullIfNotPresent,
			SecurityContext: &corev1.SecurityContext{
				AllowPrivilegeEscalation: &allowEscalation,
			},
		})
	}

	if len(hangarItems) > 0 {
		if len(hangarItems) > maxHangarMaterializationItems {
			return nil, fmt.Errorf("Hangar materialization batch exceeds %d items", maxHangarMaterializationItems)
		}
		payload, err := json.Marshal(artifactwire.MaterializationRequest{Items: hangarItems})
		if err != nil {
			return nil, fmt.Errorf("marshal Hangar materialization batch: %w", err)
		}
		if len(payload) > maxHangarMaterializationBytes {
			return nil, fmt.Errorf("Hangar materialization batch exceeds %d bytes", maxHangarMaterializationBytes)
		}
		initContainers = append(initContainers, corev1.Container{
			Name:            "materialize-hangar-inputs",
			Image:           helperImage,
			Command:         b.daemonHangarMaterializationCommand(payload, hangarReceiptBytes),
			Env:             envVars,
			VolumeMounts:    hangarMounts,
			ImagePullPolicy: corev1.PullIfNotPresent,
			SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: &allowEscalation},
		})
	}

	return initContainers, nil
}

func (b *DaemonSetBackend) daemonHangarMaterializationCommand(payload []byte, expectedReceipts []string) []string {
	request := base64.StdEncoding.EncodeToString(payload)
	var receiptChecks strings.Builder
	for index, expected := range expectedReceipts {
		fmt.Fprintf(&receiptChecks, "verify_receipt '/hangar-inputs/input-%d' '%s' '%d'\n", index, expected, index)
	}
	script := fmt.Sprintf(`
set -u
umask 077
%sREQUEST_B64='%s'
TMP_DIR=$(mktemp -d "${TMPDIR:-/tmp}/hangar-materialize.XXXXXX") || exit 1
cleanup_files() {
  rm -rf "$TMP_DIR"
}
on_exit() {
  STATUS=$?
  trap - 0
  cleanup_files
  exit "$STATUS"
}
on_signal() {
  trap - 0 1 2 15
  cleanup_files
  exit 1
}
trap on_exit 0
trap on_signal 1 2 15
REQUEST="$TMP_DIR/request.json"
RESPONSE="$TMP_DIR/response"
HEADERS="$TMP_DIR/headers"
if ! printf '%%s' "$REQUEST_B64" | base64 -d >"$REQUEST"; then
  printf 'hangar materialization request preparation failed\n' >&2
  exit 1
fi
ATTEMPT=0
MAX_ATTEMPTS=5
while [ "$ATTEMPT" -lt "$MAX_ATTEMPTS" ]; do
  ATTEMPT=$((ATTEMPT + 1))
  : >"$RESPONSE"
  : >"$HEADERS"
  wget ${WGET_OPTS} -S -q -O "$RESPONSE" -T 180 --header='Content-Type: application/json' --post-file="$REQUEST" "${DAEMON}%s" 2>"$HEADERS"
  WGET_STATUS=$?
  HTTP_STATUS=$(sed -n 's/^[[:space:]]*HTTP\/[0-9.]* \([0-9][0-9][0-9]\).*/\1/p' "$HEADERS" | tail -n 1)
  if [ -z "$HTTP_STATUS" ] || [ "$HTTP_STATUS" = 503 ]; then
    if [ "$ATTEMPT" -ge "$MAX_ATTEMPTS" ]; then
      printf 'hangar materialization unavailable after %%s attempts\n' "$MAX_ATTEMPTS" >&2
      exit 1
    fi
    sleep 2
    continue
  fi
  if [ "$WGET_STATUS" -ne 0 ] || [ "$HTTP_STATUS" != 204 ] || [ -s "$RESPONSE" ]; then
    printf 'hangar materialization did not return an exact empty HTTP 204\n' >&2
    exit 1
  fi
  break
done
mode_of() {
  stat -c '%%a' "$1" 2>/dev/null || stat -f '%%Lp' "$1" 2>/dev/null
}
verify_receipt() {
  ROOT=$1
  EXPECTED_B64=$2
  INDEX=$3
  RECEIPT="$ROOT/.hangar-materialized"
  EXPECTED="$TMP_DIR/expected-$INDEX.json"
  if [ -L "$ROOT" ] || [ ! -d "$ROOT" ] || [ "$(mode_of "$ROOT")" != 555 ]; then
    printf 'hangar materialization root verification failed\n' >&2
    exit 1
  fi
  if [ -L "$RECEIPT" ] || [ ! -f "$RECEIPT" ] || [ "$(mode_of "$RECEIPT")" != 444 ]; then
    printf 'hangar materialization receipt verification failed\n' >&2
    exit 1
  fi
  if ! printf '%%s' "$EXPECTED_B64" | base64 -d >"$EXPECTED"; then
    printf 'hangar materialization receipt expectation preparation failed\n' >&2
    exit 1
  fi
  if ! cmp "$RECEIPT" "$EXPECTED" >/dev/null 2>&1; then
    printf 'hangar materialization receipt did not match the exact tree reference\n' >&2
    exit 1
  fi
}
%sexit 0
`, b.wire.ShellPrelude(), request, artifactwire.HangarMaterializations.Path, receiptChecks.String())
	return []string{"sh", "-c", script}
}

func (b *DaemonSetBackend) daemonResolveCommand(key, hostDest string) []string {
	if key == "" {
		script := `echo "ERROR: artifact key is empty — producing step did not record its output location" >&2; exit 1`
		return []string{"sh", "-c", script}
	}

	// The body is the same ResolveRequest the daemon decodes, marshalled
	// here rather than spelled out in shell, and every value spliced into the
	// script is a single-quoted word: an output name is the task author's,
	// and may carry a quote or a command substitution.
	payload, _ := json.Marshal(artifactwire.ResolveRequest{Key: key, Dest: hostDest})

	script := fmt.Sprintf(`
set -e
KEY=%s
DST=%s
%sPAYLOAD=%s
echo "[artifact-fetch] resolving key=${KEY} dest=${DST} daemon=${DAEMON}" >&2
# Retry up to 10 times with backoff — the daemon may not be reachable
# immediately (hostPort iptables rules propagation, daemon restart after
# eviction, etc.).
ATTEMPT=0
MAX=10
while true; do
  ATTEMPT=$((ATTEMPT + 1))
  RESP=$(wget ${WGET_OPTS} -qO- -T 180 --header='Content-Type: application/json' --post-data="${PAYLOAD}" "${DAEMON}%s" 2>&1) && break
  if [ "$ATTEMPT" -ge "$MAX" ]; then
    echo "[artifact-fetch] FAILED after ${MAX} attempts: ${RESP}" >&2
    exit 1
  fi
  echo "[artifact-fetch] attempt ${ATTEMPT}/${MAX} failed, retrying in 2s..." >&2
  sleep 2
done
echo "[artifact-fetch] resolved: ${RESP}" >&2
`, shellQuote(key), shellQuote(hostDest), b.wire.ShellPrelude(), shellQuote(string(payload)), artifactwire.Resolve.Path)

	return []string{"sh", "-c", script}
}

func (b *DaemonSetBackend) daemonResolveBatchCommand(items []artifactwire.ResolveRequest) []string {
	if len(items) == 0 {
		return []string{"sh", "-c", "echo '[artifact-fetch] no items to resolve' >&2"}
	}

	payload, _ := json.Marshal(artifactwire.BatchResolveRequest{Items: items})

	// The keys, named in the script itself. BusyBox wget discards the response
	// BODY on a non-2xx and prints only the status line, so on the failure
	// that matters the daemon's own account of which artifact it could not
	// find never reaches the build log. What the log can always have is what
	// this pod ASKED for, because that is a constant of the script.
	keys := make([]string, 0, len(items))
	for _, it := range items {
		keys = append(keys, it.Key)
	}

	script := fmt.Sprintf(`
set -e
%sPAYLOAD=%s
KEYS=%s
echo "[artifact-fetch] batch resolving %d artifacts via ${DAEMON}%s: ${KEYS}" >&2
ATTEMPT=0
MAX=10
while true; do
  ATTEMPT=$((ATTEMPT + 1))
  RESP=$(wget ${WGET_OPTS} -qO- -T 180 --header='Content-Type: application/json' --post-data="${PAYLOAD}" "${DAEMON}%s" 2>&1) && break
  # A 4xx is the daemon's considered answer about these keys — missing
  # artifact, refused destination, expired capability — and no number of
  # retries turns it into a different one. Retrying it burned twenty seconds
  # and buried the answer under nine identical lines.
  case "${RESP}" in
    *"HTTP/1.1 4"*)
      echo "[artifact-fetch] FAILED (not retryable): ${RESP}" >&2
      echo "[artifact-fetch] the daemon at ${DAEMON} would not resolve: ${KEYS}" >&2
      exit 1
      ;;
  esac
  if [ "$ATTEMPT" -ge "$MAX" ]; then
    echo "[artifact-fetch] FAILED after ${MAX} attempts: ${RESP}" >&2
    echo "[artifact-fetch] the daemon at ${DAEMON} would not resolve: ${KEYS}" >&2
    exit 1
  fi
  echo "[artifact-fetch] attempt ${ATTEMPT}/${MAX} failed, retrying in 2s: ${RESP}" >&2
  sleep 2
done
echo "[artifact-fetch] batch resolved: ${RESP}" >&2
# Check if the batch had any failures — the daemon returns {"status":"error",...} on partial failure.
case "${RESP}" in
  *'"status":"error"'*) echo "[artifact-fetch] batch had failures — see above" >&2; exit 1 ;;
esac
`, b.wire.ShellPrelude(), shellQuote(string(payload)), shellQuote(strings.Join(keys, " ")), len(items), artifactwire.ResolveBatch.Path, artifactwire.ResolveBatch.Path)

	return []string{"sh", "-c", script}
}

func (b *DaemonSetBackend) artifactLocate(key string) (ArtifactLocation, bool) {
	if b.artifactLocator == nil {
		return ArtifactLocation{}, false
	}
	return b.artifactLocator.Locate(key)
}

func (b *DaemonSetBackend) helperImage() string {
	if b.config.ArtifactHelperImage != "" {
		return b.config.ArtifactHelperImage
	}
	return DefaultArtifactHelperImage
}

// BuildCleanupInitContainer removes a reused handle's stale hostPath data --
// and, when the output plane is on, asks the ledger first.
//
// This container is the most destructive thing in a Pod: `rm -rf` over the step
// directory, running before anything else, reached by no guard the daemon
// added. Every OTHER destructive path on a node -- DELETE /artifacts, the
// sweeper, a stream-in replacement, a registry remap or reuse -- consults the
// output ledger's classifier and refuses a capture-held source with a 409. This
// one asked nobody, and a reused handle whose previous execution's capture is
// still unsettled would have its held source destroyed by the next build's
// first init container.
//
// It cannot consult the ledger the way the ATC does: an init container holds no
// client certificate (see wgetTLSOpts), so /artifacts/ is closed to it. So it
// asks the read-only classification route, which is mTLS-exempt for exactly the
// reason /resolve is -- it is a question a pod on this node must be able to ask
// about its own step directory, and the answer is a boolean about a handle the
// caller already named.
//
// It FAILS CLOSED, and that is why the probe is only emitted when the output
// plane is configured. An unreachable daemon is not "nothing is held"; but on a
// deployment with no output plane there is nothing to ask and today's script is
// emitted byte for byte, so Req 59's unchanged ordinary behaviour is not
// traded for this.
func (b *DaemonSetBackend) BuildCleanupInitContainer(handle string, containerType db.ContainerType, reused bool) (*corev1.Container, error) {
	if !reused {
		return nil, nil
	}
	if containerType == db.ContainerTypeCheck {
		return nil, nil
	}

	helperImage := b.helperImage()
	cleanupPath := filepath.Join(ArtifactMountPath, "steps", handle)
	// THE TARGET IS ONE WORD, and it reaches the shell as one. This is the
	// only container in the deployment that mounts the whole managed hostPath
	// read-write, and the path it removes ends in a handle -- so the handle
	// was interpolated into `rm -rf` with nothing between it and the shell's
	// own parser. Demonstrated, by running the emitted script: a handle
	// carrying `; touch x` ran it, and one carrying `$(...)` ran that too,
	// including through the probe's URL and its messages. Handles are the
	// ATC's own UUIDs today, which is a fact about a caller rather than a
	// property of this function, and it is the one container where being
	// wrong costs a node's worth of somebody else's sources.
	script := fmt.Sprintf(`TARGET=%s
echo "[cleanup-stale] removing stale hostPath data: ${TARGET}" >&2
rm -rf "${TARGET}"
mkdir -p "${TARGET}"`, shellQuote(cleanupPath))
	if b.config.OutputPlaneEnabled {
		script = b.ledgerCheckedCleanupScript(handle, cleanupPath)
	}

	allowEscalation := false
	return &corev1.Container{
		Name:    "cleanup-stale",
		Image:   helperImage,
		Command: []string{"sh", "-c", script},
		VolumeMounts: []corev1.VolumeMount{
			{Name: artifactDaemonHostPathVolumeName, MountPath: ArtifactMountPath},
		},
		ImagePullPolicy: corev1.PullIfNotPresent,
		SecurityContext: &corev1.SecurityContext{
			AllowPrivilegeEscalation: &allowEscalation,
		},
	}, nil
}

// ledgerCheckedCleanupScript is the same removal, behind the classifier.
//
// The three arms mirror the classifier's own (hangar/output/ledger): "held" is
// a refusal, "unmanaged" -- which is what an absent control directory means,
// and it is a real answer rather than an error -- proceeds, and anything else,
// including no answer at all, is a refusal. A guard whose failure mode is
// "delete it anyway" would be the exposure it was written to close.
func (b *DaemonSetBackend) ledgerCheckedCleanupScript(handle, cleanupPath string) string {
	// The handle and the target are shell VARIABLES, assigned once from a
	// quoted word and expanded inside double quotes from there on. Neither is
	// interpolated into a command, a URL or a message, because every one of
	// those contexts parses: `;` escapes an unquoted word, an apostrophe
	// escapes a single-quoted one, and `$(...)` runs inside the double-quoted
	// URL this probe fetches. All three were demonstrated against the script
	// this function used to emit.
	return fmt.Sprintf(`
set -u
HANDLE=%[1]s
TARGET=%[4]s
%[2]sCLASS="$(wget -q -O - ${WGET_OPTS} "${DAEMON}%[3]s${HANDLE}" 2>/dev/null || true)"
case "${CLASS}" in
  *'"class":"unmanaged"'*)
    echo "[cleanup-stale] the output ledger holds nothing here; removing stale hostPath data: ${TARGET}" >&2
    rm -rf "${TARGET}"
    mkdir -p "${TARGET}"
    ;;
  *'"class":"held"'*)
    echo "[cleanup-stale] REFUSED: a durable output capture still holds ${HANDLE}. This step's stale workspace is somebody else's unsettled source, and removing it would destroy bytes no receipt has been written for yet." >&2
    exit 1
    ;;
  *)
    echo "[cleanup-stale] REFUSED: the output ledger did not answer for ${HANDLE} (got: ${CLASS}). An unreadable ledger is not an empty one." >&2
    exit 1
    ;;
esac
`, shellQuote(handle), b.wire.ShellPrelude(), artifactwire.CaptureHeldStepsPrefix, shellQuote(cleanupPath))
}

// BuildAffinity places the pod on a node that can serve every facet it needs.
//
// A capture-selected execution needs TWO ready labels and not one. The base
// control facet attests that this node's daemon, runtime and control key are a
// homogeneous attested cohort for the exact-execution protocol; the output
// facet attests the capture extension on top of it. They are separate labels
// because a base-only cohort is a real deployment -- it is the one the sibling
// `exact_execution_control` track schedules onto -- and a single label would
// make "attested for exact control" and "has an output bucket" the same claim.
//
// A ready label is a scheduling HINT and never authority: the authenticated
// handshake is. What the label buys is that the pod does not land somewhere the
// hold could never be acknowledged.
// CaptureClass reads what the output ledger says about one step directory,
// through the daemon that owns the node it is on.
//
// It is the ATC's half of the same question the cleanup init container asks,
// and it goes to the same read-only route so there is one classifier and one
// answer. A node it cannot reach is an error and not "unmanaged": every caller
// of this is about to do something write-capable or destructive, and an
// unreadable ledger is not an empty one.
func (b *DaemonSetBackend) CaptureClass(ctx context.Context, handle, nodeName string) (string, error) {
	if !b.config.OutputPlaneEnabled {
		return captureClassUnmanaged, nil
	}
	if b.nodeIPResolver == nil || nodeName == "" {
		return "", fmt.Errorf("no node to ask about %s", handle)
	}
	nodeIP, err := b.nodeIPResolver.Resolve(ctx, nodeName)
	if err != nil {
		return "", fmt.Errorf("resolving node %s: %w", nodeName, err)
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	answer, err := b.wire.CaptureClass(ctx, nodeIP, handle)
	if err != nil {
		var refusal *artifactwire.Refusal
		if errors.As(err, &refusal) {
			return "", fmt.Errorf("the daemon on %s answered %d", nodeName, refusal.Status)
		}
		return "", err
	}

	return answer.Class, nil
}

func (b *DaemonSetBackend) BuildAffinity(inputs []runtime.Input, control *runtime.ExecutionControl) *corev1.Affinity {
	requiredExpressions := []corev1.NodeSelectorRequirement{
		{
			Key:      "concourse.dev/artifact-cache",
			Operator: corev1.NodeSelectorOpIn,
			Values:   []string{"ready"},
		},
	}
	for _, input := range inputs {
		if input.HangarTree != nil {
			requiredExpressions = append(requiredExpressions, corev1.NodeSelectorRequirement{
				Key:      "concourse.dev/hangar-v1",
				Operator: corev1.NodeSelectorOpIn,
				Values:   []string{"ready"},
			})
			break
		}
	}
	if control.HasDurableOutputCapture() {
		for _, label := range []string{executioncontrol.ReadyLabel, output.ReadyLabel} {
			requiredExpressions = append(requiredExpressions, corev1.NodeSelectorRequirement{
				Key:      label,
				Operator: corev1.NodeSelectorOpIn,
				Values:   []string{"ready"},
			})
		}
		// And the reserving node itself, by name.
		//
		// The two labels above pick a COHORT: nodes whose daemons are up and
		// attested, which is where a hold could be acknowledged at all. The
		// reservation is narrower than that -- it is a directory on one node's
		// disk, made before this Pod existed -- so a cohort-wide placement lets
		// the scheduler land the producer on a node that reserved nothing,
		// where the hostPath's DirectoryOrCreate makes an empty unheld
		// directory and the control init's hold is refused. Requiring the node
		// is what turns that outage into a pending Pod.
		requiredExpressions = append(requiredExpressions, corev1.NodeSelectorRequirement{
			Key:      corev1.LabelHostname,
			Operator: corev1.NodeSelectorOpIn,
			Values:   []string{control.Capture.ReservingNode},
		})
	}
	affinity := &corev1.Affinity{
		NodeAffinity: &corev1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{
					{
						MatchExpressions: requiredExpressions,
					},
				},
			},
		},
	}

	if b.artifactLocator != nil {
		preferredNode := b.preferredInputNode(inputs)
		if preferredNode != "" {
			affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution = []corev1.PreferredSchedulingTerm{
				{
					Weight: 100,
					Preference: corev1.NodeSelectorTerm{
						MatchExpressions: []corev1.NodeSelectorRequirement{
							{
								Key:      "kubernetes.io/hostname",
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{preferredNode},
							},
						},
					},
				},
			}
		}
	}

	return affinity
}

func (b *DaemonSetBackend) preferredInputNode(inputs []runtime.Input) string {
	if b.artifactLocator == nil {
		return ""
	}
	counts := make(map[string]int)
	for _, input := range inputs {
		// Not dead code: a Hangar tree input passes validateInputs with a nil
		// Artifact, and it is not located by artifact key at all.
		if input.Artifact == nil {
			continue
		}
		key := ArtifactKey(input.Artifact.Handle())
		if node, found := b.artifactLocator.LocateNode(key); found {
			counts[node]++
		}
	}
	bestNode := ""
	bestCount := 0
	for node, count := range counts {
		if count > bestCount {
			bestNode = node
			bestCount = count
		}
	}
	return bestNode
}

func (b *DaemonSetBackend) RecordOutputs(ctx context.Context, handle, nodeName string, volumes []*Volume, spec runtime.ContainerSpec) {
	if b.artifactLocator == nil {
		return
	}

	outputPaths := make(map[string]bool)
	for _, path := range spec.Outputs {
		outputPaths[filepath.Clean(path)] = true
	}
	if spec.Dir != "" && spec.Type != db.ContainerTypeTask && spec.Type != db.ContainerTypeCheck {
		outputPaths[spec.Dir] = true
	}

	mountToOutputName := make(map[string]string)
	for name, path := range spec.Outputs {
		mountToOutputName[filepath.Clean(path)] = name
	}
	if spec.Dir != "" {
		mountToOutputName[spec.Dir] = "dir"
	}

	recordedPaths := make(map[string]bool)
	recorded := 0
	for _, vol := range volumes {
		cleanPath := filepath.Clean(vol.MountPath())
		if cleanPath == "." || !outputPaths[cleanPath] {
			continue
		}
		if recordedPaths[cleanPath] {
			continue
		}
		recordedPaths[cleanPath] = true

		key := ArtifactKey(vol.Handle())
		subdir := mountToOutputName[cleanPath]
		if subdir == "" {
			subdir = "unknown"
		}
		// path.Join, not concatenation: this key and the diskPath below are two
		// derivations of one directory, and the join that builds the path — in
		// StepVolume too — cleans a leading slash away. An output the pipeline
		// named "/data" (a task may name one for the directory it wants to
		// capture, and the ATC resolves an absolute name to itself) then lands
		// in steps/<handle>/data while a concatenated key says
		// "<handle>//data". The daemon refuses that key outright rather than
		// cleaning it — a caller splitting it on "/" gets an empty segment
		// where the sweeper gets a handle — so the next step's fetch 400s on
		// the one node holding the data, and the mirror was refused the same
		// way and swallowed. Slash-separated key, so path and not filepath.
		daemonKey := path.Join(handle, subdir)

		// The ONE selected output lives somewhere else, and capture is
		// ADDITIVE: it is still an ordinary output and downstream steps still
		// resolve it in the ordinary way. `Container.buildPod` mounts the
		// reserved incarnation as its volume, so `steps/<handle>/<output>` is
		// a sibling directory nothing wrote into -- recording that one would
		// hand a consumer an empty tree with nothing to say it was empty.
		//
		// The ATC composes nothing here either: ReservedDirectory came off the
		// wire from `reserve-incarnation` and is repeated.
		readOnly := false
		if reserved := captureReservedDirectory(spec); reserved != "" &&
			subdir == captureSelectedOutputName(spec) {
			daemonKey = reserved
			// And the alias onto it is read-only, because the incarnation is
			// held: a write-capable second name is what the register guard
			// exists to refuse.
			readOnly = true
		}
		b.artifactLocator.Record(key, nodeName, daemonKey)

		if nodeName != "" {
			diskPath := filepath.Join(b.config.ArtifactDaemonHostPath, "steps", daemonKey)
			b.registerAlias(nodeName, key, diskPath, readOnly)
			// Trigger an outbound mirror on the producer's daemon so the
			// step output survives loss of this node. Best-effort: if the
			// trigger fails, the build still succeeds — node loss just
			// reverts to today's behavior (rerun required).
			b.triggerMirror(nodeName, daemonKey)
		}

		recorded++
	}
	if recorded == 0 && len(volumes) > 0 {
		fmt.Fprintf(os.Stderr, "WARNING: RecordOutputs: %d volumes but 0 matched outputPaths %v (handle=%s type=%s)\n",
			len(volumes), outputPaths, handle, spec.Type)
	}
}

// triggerMirror fires a best-effort POST /mirror on the producer's daemon
// for daemonKey (the on-disk path under steps/, e.g. "handle/result").
// All errors are swallowed — the build's outputs are already persisted on
// the producer; absence of mirror is not a step failure, just lost
// resilience.
func (b *DaemonSetBackend) triggerMirror(nodeName, daemonKey string) {
	if b.daemonClient == nil || b.nodeIPResolver == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	nodeIP, err := b.nodeIPResolver.Resolve(ctx, nodeName)
	if err != nil {
		// Already logged by Resolve.
		return
	}
	_ = b.daemonClient.TriggerMirror(ctx, nodeIP, daemonKey)
}

// registerReadOnlyDaemonAlias registers a name for bytes another authority
// owns.
//
// The register guard refuses an alias onto a capture-held location, and it is
// right to: a second WRITE-CAPABLE name for bytes a capture is about to seal
// hands every key-taking destructive path on that daemon a way to reach them
// under a name the capture never heard of. A read is not that, and Req 16
// forbids the mount, not the read -- so the captured output stays an ordinary
// output that downstream steps resolve in the ordinary way, and the alias says
// which of the two it is.
func (b *DaemonSetBackend) registerReadOnlyDaemonAlias(nodeName, volumeKey, diskPath string) {
	b.registerAlias(nodeName, volumeKey, diskPath, true)
}

func (b *DaemonSetBackend) registerDaemonAlias(nodeName, volumeKey, diskPath string) {
	b.registerAlias(nodeName, volumeKey, diskPath, false)
}

func (b *DaemonSetBackend) registerAlias(nodeName, volumeKey, diskPath string, readOnly bool) {
	if b.nodeIPResolver == nil {
		fmt.Fprintf(os.Stderr, "WARNING: registerDaemonAlias: no node IP resolver configured\n")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	nodeIP, err := b.nodeIPResolver.Resolve(ctx, nodeName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: registerDaemonAlias: resolve node IP for %s: %v\n", nodeName, err)
		return
	}

	// /register is a protected daemon route; the wire client carries the
	// client cert when TLS is enabled.
	err = b.wire.Register(ctx, nodeIP, artifactwire.RegisterRequest{Key: volumeKey, LocalPath: diskPath, ReadOnly: readOnly})
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: registerDaemonAlias: %v (key=%s)\n", err, volumeKey)
	}
}

func (b *DaemonSetBackend) WrapVolumeForArtifact(key, handle, workerName string, dbVolume db.CreatedVolume) runtime.Volume {
	vol := NewDaemonSetVolume(key, handle, workerName, dbVolume, "", b.config, b.nodeIPResolver)
	if b.daemonClient != nil {
		vol.SetDaemonClient(b.daemonClient)
	}
	return vol
}

func (b *DaemonSetBackend) WrapVolumeForLookup(ctx context.Context, key, handle, workerName string, dbVolume db.CreatedVolume) runtime.Volume {
	var sourceNode string
	if b.artifactLocator != nil {
		sourceNode, _ = b.artifactLocator.LocateNode(key)
	}

	// Resource-cache handles (rc-{id}) never appear in the locator as
	// an authoritative node-keyed entry: the original get step that
	// populated the cache may have run in a different ATC process, on
	// a different build, or long before the current lookup. When the
	// locator has no entry, probe the live daemons to find which one
	// currently has the cache and bind the volume directly to that
	// pod IP. This sidesteps NodeIPResolver (which can't help — we
	// never learned a node name) and avoids stale-entry risk.
	if sourceNode == "" && b.daemonClient != nil && isResourceCacheKey(key) {
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		// Local probe only. A lookup is not a cache-hit decision — the artifact
		// is already believed to exist — so there is nothing here to warm.
		if probe, found := b.daemonClient.ProbeResourceCache(probeCtx, key); found {
			vol := NewDaemonSetVolumeFromIP(key, handle, workerName, probe.IP, b.config)
			vol.SetDaemonClient(b.daemonClient)
			return vol
		}
	}

	vol := NewDaemonSetVolume(key, handle, workerName, dbVolume, sourceNode, b.config, b.nodeIPResolver)
	// Wire the daemonClient so lookup-wrapped reads (e.g. a web-process
	// file-config StreamOut on a get-step output) get peer-fallback and
	// daemon discovery when the recorded source node is unreachable —
	// matching WrapVolumeForArtifact. Without this the volume can only hit
	// the recorded node and hard-fails with no recovery.
	if b.daemonClient != nil {
		vol.SetDaemonClient(b.daemonClient)
	}
	return vol
}

// RegisterResourceCache registers a resource cache alias on the daemon using
// the daemon's POST /register API. The alias maps the cache key (rc-{id}) to
// the physical disk path of the get step output, making it discoverable via
// HEAD /artifacts/steps/rc-{id} (filesystem fallback) and /resolve.
//
// Instead of using NodeIPResolver (which needs nodes/get RBAC), this discovers
// the daemon pod IP from EndpointSlices (only needs discovery.k8s.io RBAC) and
// POSTs the registration directly.
func (b *DaemonSetBackend) RegisterResourceCache(ctx context.Context, cacheKey, durableKey, volumeHandle, nodeName string) error {
	if b.daemonClient == nil {
		return fmt.Errorf("daemon client not configured")
	}

	// Resolve the disk path from the locator or by convention.
	var diskPath string
	if b.artifactLocator != nil {
		if loc, found := b.artifactLocator.Locate(ArtifactKey(volumeHandle)); found {
			diskPath = filepath.Join(b.config.ArtifactDaemonHostPath, "steps", loc.HostDir)
		}
	}
	if diskPath == "" {
		containerHandle := strings.TrimSuffix(volumeHandle, "-dir")
		diskPath = filepath.Join(b.config.ArtifactDaemonHostPath, "steps", containerHandle, "dir")
	}

	// Trigger mirror BEFORE the alias broadcast so peers have the
	// underlying step output by the time RegisterAlias requires the path
	// to exist on disk. The daemonKey is the path under steps/ on disk —
	// derived from diskPath by stripping the storage hostPath prefix.
	if daemonKey := strings.TrimPrefix(diskPath, b.config.ArtifactDaemonHostPath+"/steps/"); daemonKey != diskPath {
		b.triggerMirror(nodeName, daemonKey)
	}

	// Find a daemon pod IP to register with. On the same node as the get
	// step, the daemon has the data locally. On a single-node cluster
	// there's only one daemon; on multi-node we register on all daemons
	// but only the one with local data will have the path.
	if err := b.daemonClient.RegisterAlias(ctx, cacheKey, diskPath, durableKey); err != nil {
		return fmt.Errorf("register resource cache alias: %w", err)
	}

	// Record in locator for affinity on downstream steps.
	if b.artifactLocator != nil && nodeName != "" {
		b.artifactLocator.Record(cacheKey, nodeName, cacheKey)
	}

	return nil
}

// FindResourceCache finds a daemon that can serve the cache, warming it from
// the durable tier if no node holds it and the ATC supplied a content key.
//
// The two phases are deliberately separate channels. A probe 200 means "these
// bytes are on this node's disk right now", which is what makes the returned
// pod worth binding to; if the durable store could also answer that probe,
// every daemon would say yes for anything in the bucket and the winner would be
// arbitrary — destroying the node affinity the probe exists to provide. The
// warm verb answers a different question, "who can get it", and makes its own
// answer true before returning.
func (b *DaemonSetBackend) FindResourceCache(ctx context.Context, cacheKey, durableKey, workerName string) (runtime.Volume, bool) {
	if b.daemonClient == nil {
		return nil, false
	}

	probe, found := b.daemonClient.ProbeResourceCache(ctx, cacheKey)
	if found {
		metric.Metrics.ResourceCacheLocalHits.Inc()

		return b.bindProbed(cacheKey, workerName, probe.IP), true
	}

	// Silence is the protocol: no content key means the ATC is not offering
	// this cache to the durable tier, so no request is made.
	if durableKey == "" || !probe.DurableCapable {
		return nil, false
	}

	// A get step's own `timeout:` does not bound this — MaybeTimeout is applied
	// further in, and attemptGet re-enters every GetResourceLockInterval. Without
	// suppression a degraded bucket turns a 5s lock tick into a warm-timeout tick
	// for as long as the bucket stays degraded.
	if b.warmNegative.suppressed(cacheKey) {
		metric.Metrics.DurableWarmSuppressed.Inc()

		return nil, false
	}

	warmCtx, cancel := context.WithTimeout(ctx, b.config.ArtifactDaemonWarmTimeout)
	defer cancel()

	ip, ok := b.daemonClient.WarmResourceCache(warmCtx, cacheKey, durableKey, probe.Endpoints)
	if !ok {
		metric.Metrics.DurableWarmMisses.Inc()
		b.warmNegative.suppress(cacheKey, warmSuppressionWindow)

		return nil, false
	}

	metric.Metrics.DurableWarmHits.Inc()

	return b.bindProbed(cacheKey, workerName, ip), true
}

// bindProbed wraps a daemon pod IP as a volume.
//
// SetDaemonClient is not optional: NewDaemonSetVolumeFromIP leaves the client
// nil, and without it fetchArtifactWithPeerFallback has no peer to fall back
// to. An alias swept between the probe and the read then surfaces as a bare 404
// and a red build, with the bytes still sitting on another node.
func (b *DaemonSetBackend) bindProbed(cacheKey, workerName, ip string) runtime.Volume {
	vol := NewDaemonSetVolumeFromIP(cacheKey, cacheKey, workerName, ip, b.config)
	vol.SetDaemonClient(b.daemonClient)

	return vol
}

// ValidateResolveCapabilityConfig refuses at STARTUP the configurations that
// would otherwise mint capabilities a legitimately slow pod cannot use, or
// silently sign nothing while the daemon requires a signature.
//
// The daemon fails closed the moment --resolve-capability-key is set, and the
// chart derives both sides' flags from one value — so a key reaching the ATC
// means the daemon is enforcing, and the only honest states here are
// "signing, with a TTL that clears the floor" and "refusing to start, saying
// exactly what to change". No key means signing is deliberately off, matching
// a daemon started without one.
//
// atccmd calls this wherever it builds a jetbridge Config; a nil error there
// is what makes NewDaemonSetBackend's unconditional signing sound.
func ValidateResolveCapabilityConfig(cfg Config) error {
	if len(cfg.ArtifactDaemonResolveCapabilityKey) == 0 {
		return nil
	}
	if _, err := artifactcap.NewSigner(cfg.ArtifactDaemonResolveCapabilityKey); err != nil {
		return fmt.Errorf("artifact resolve capability key: %w", err)
	}
	// Effective timeouts, not raw: a zero Config timeout means "use the
	// default" at pod-build time (podSchedulingTimeout/podStartupTimeout), so
	// computing the floor from the raw zeros would underestimate the wait a
	// pod actually experiences and let a too-short TTL through.
	scheduling, startup := podSchedulingTimeout(cfg), podStartupTimeout(cfg)
	minimumTTL, err := MinimumArtifactResolveCapabilityTTL(scheduling, startup)
	if err != nil {
		return fmt.Errorf("artifact resolve capability TTL floor: %w", err)
	}
	if lifetime := resolveCapabilityLifetime(cfg); lifetime <= minimumTTL {
		return fmt.Errorf(
			"artifact resolve capability TTL %v does not clear its floor of %v (pod scheduling timeout %v + pod startup timeout %v + init retry budget %v + safety margin %v): a slow-to-start pod would present an expired capability and 403 on a legitimate request; raise --kubernetes-artifact-daemon-resolve-capability-ttl or lower the pod timeouts",
			lifetime, minimumTTL,
			scheduling, startup,
			ArtifactResolveInitRetryBudget, artifactResolveExpirySafetyMargin,
		)
	}
	return nil
}

// resolveCapabilityLifetime is how long a signed capability stays valid.
func resolveCapabilityLifetime(cfg Config) time.Duration {
	if cfg.ArtifactDaemonResolveCapabilityTTL <= 0 {
		return DefaultArtifactResolveCapabilityTTL
	}
	return cfg.ArtifactDaemonResolveCapabilityTTL
}

func resolveCapabilityExpiry(cfg Config) time.Time {
	return time.Now().Add(resolveCapabilityLifetime(cfg))
}
