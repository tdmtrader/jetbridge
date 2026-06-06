package jetbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	corev1 "k8s.io/api/core/v1"
)

const artifactDaemonHostPathVolumeName = "artifact-daemon-hostpath"

// Compile-time check that DaemonSetBackend satisfies StorageBackend.
var _ StorageBackend = (*DaemonSetBackend)(nil)

type DaemonSetBackend struct {
	config          Config
	artifactLocator *ArtifactLocator
	nodeIPResolver  *NodeIPResolver
	daemonClient    *DaemonClient
}

func NewDaemonSetBackend(config Config, locator *ArtifactLocator, resolver *NodeIPResolver) *DaemonSetBackend {
	return &DaemonSetBackend{
		config:          config,
		artifactLocator: locator,
		nodeIPResolver:  resolver,
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

func (b *DaemonSetBackend) CacheVolume(name string, jobID int, stepName, cachePath string) corev1.Volume {
	basePath := b.config.CacheHostPath
	if basePath == "" {
		basePath = filepath.Join(b.config.ArtifactDaemonHostPath, "caches")
	}
	dirType := corev1.HostPathDirectoryOrCreate
	key := stableCacheKey(jobID, stepName, cachePath)
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

// batchItem is a single key/dest pair for the /resolve-batch endpoint.
type batchItem struct {
	Key  string `json:"key"`
	Dest string `json:"dest"`
}

func (b *DaemonSetBackend) BuildFetchInitContainers(handle string, inputs []runtime.Input, podVolumes []corev1.Volume, mainMounts []corev1.VolumeMount) []corev1.Container {
	helperImage := b.helperImage()
	allowEscalation := false

	var items []batchItem
	var mounts []corev1.VolumeMount
	seenVolumes := map[string]bool{}

	for _, input := range inputs {
		if input.Artifact == nil {
			continue
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

		items = append(items, batchItem{Key: daemonKey, Dest: hostDestPath})

		if !seenVolumes[volumeName] {
			seenVolumes[volumeName] = true
			mounts = append(mounts, corev1.VolumeMount{Name: volumeName, MountPath: input.DestinationPath})
		}
	}

	if len(items) == 0 {
		return nil
	}

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

	// Note: when TLS is enabled the init container reaches the daemon over
	// HTTPS at ${HOST_IP}:7780 (the node IP, via hostPort). The node IP cannot
	// be a certificate SAN, so BusyBox wget can't verify the hostname; the
	// resolve command uses --no-check-certificate instead. The connection is
	// still TLS-encrypted, and /resolve(-batch) is an exempt, same-node,
	// NetworkPolicy-protected control path — artifact data flows via the shared
	// hostPath, not over this HTTP call. No CA cert mount is needed.

	return []corev1.Container{
		{
			Name:    "fetch-inputs",
			Image:   helperImage,
			Command: b.daemonResolveBatchCommand(items),
			Env:     envVars,
			VolumeMounts:    allMounts,
			ImagePullPolicy: corev1.PullIfNotPresent,
			SecurityContext: &corev1.SecurityContext{
				AllowPrivilegeEscalation: &allowEscalation,
			},
		},
	}
}

func (b *DaemonSetBackend) daemonScheme() string {
	return daemonURLScheme(b.config)
}

// wgetTLSOpts returns extra BusyBox wget options for daemon HTTPS calls. When
// TLS is enabled it adds --no-check-certificate: the init container dials the
// daemon by node IP (HOST_IP), which is not a cert SAN, so hostname
// verification cannot succeed. The connection is still encrypted; /resolve is
// an exempt, same-node, NetworkPolicy-protected control path.
func (b *DaemonSetBackend) wgetTLSOpts() string {
	if b.config.ArtifactDaemonTLSEnabled {
		return "--no-check-certificate"
	}
	return ""
}

func (b *DaemonSetBackend) daemonResolveCommand(key, hostDest string) []string {
	if key == "" {
		script := `echo "ERROR: artifact key is empty — producing step did not record its output location" >&2; exit 1`
		return []string{"sh", "-c", script}
	}

	port := b.config.ArtifactDaemonPort
	if port == 0 {
		port = 7780
	}

	script := fmt.Sprintf(`
set -e
KEY="%s"
DST="%s"
PORT=%d
DAEMON="%s://${HOST_IP}:${PORT}"
WGET_OPTS="%s"
echo "[artifact-fetch] resolving key=${KEY} dest=${DST} daemon=${DAEMON}" >&2
# Retry up to 10 times with backoff — the daemon may not be reachable
# immediately (hostPort iptables rules propagation, daemon restart after
# eviction, etc.).
ATTEMPT=0
MAX=10
while true; do
  ATTEMPT=$((ATTEMPT + 1))
  RESP=$(wget ${WGET_OPTS} -qO- -T 180 --post-data='{"key":"'"${KEY}"'","dest":"'"${DST}"'"}' "${DAEMON}/resolve" 2>&1) && break
  if [ "$ATTEMPT" -ge "$MAX" ]; then
    echo "[artifact-fetch] FAILED after ${MAX} attempts: ${RESP}" >&2
    exit 1
  fi
  echo "[artifact-fetch] attempt ${ATTEMPT}/${MAX} failed, retrying in 2s..." >&2
  sleep 2
done
echo "[artifact-fetch] resolved: ${RESP}" >&2
`, key, hostDest, port, b.daemonScheme(), b.wgetTLSOpts())

	return []string{"sh", "-c", script}
}

func (b *DaemonSetBackend) daemonResolveBatchCommand(items []batchItem) []string {
	if len(items) == 0 {
		return []string{"sh", "-c", "echo '[artifact-fetch] no items to resolve' >&2"}
	}

	port := b.config.ArtifactDaemonPort
	if port == 0 {
		port = 7780
	}

	// Build the JSON payload for /resolve-batch.
	type batchPayload struct {
		Items []batchItem `json:"items"`
	}
	payload, _ := json.Marshal(batchPayload{Items: items})

	script := fmt.Sprintf(`
set -e
PORT=%d
DAEMON="%s://${HOST_IP}:${PORT}"
WGET_OPTS="%s"
PAYLOAD='%s'
echo "[artifact-fetch] batch resolving %d artifacts via ${DAEMON}/resolve-batch" >&2
ATTEMPT=0
MAX=10
while true; do
  ATTEMPT=$((ATTEMPT + 1))
  RESP=$(wget ${WGET_OPTS} -qO- -T 180 --header='Content-Type: application/json' --post-data="${PAYLOAD}" "${DAEMON}/resolve-batch" 2>&1) && break
  if [ "$ATTEMPT" -ge "$MAX" ]; then
    echo "[artifact-fetch] FAILED after ${MAX} attempts: ${RESP}" >&2
    exit 1
  fi
  echo "[artifact-fetch] attempt ${ATTEMPT}/${MAX} failed, retrying in 2s..." >&2
  sleep 2
done
echo "[artifact-fetch] batch resolved: ${RESP}" >&2
# Check if the batch had any failures — the daemon returns {"status":"error",...} on partial failure.
case "${RESP}" in
  *'"status":"error"'*) echo "[artifact-fetch] batch had failures — see above" >&2; exit 1 ;;
esac
`, port, b.daemonScheme(), b.wgetTLSOpts(), string(payload), len(items))

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

func (b *DaemonSetBackend) BuildCleanupInitContainer(handle string, containerType db.ContainerType, reused bool) *corev1.Container {
	if !reused {
		return nil
	}
	if containerType == db.ContainerTypeCheck {
		return nil
	}

	helperImage := b.helperImage()
	cleanupPath := filepath.Join(ArtifactMountPath, "steps", handle)
	script := fmt.Sprintf(`echo "[cleanup-stale] removing stale hostPath data: %s" >&2; rm -rf %s; mkdir -p %s`, cleanupPath, cleanupPath, cleanupPath)

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
	}
}

func (b *DaemonSetBackend) BuildAffinity(inputs []runtime.Input) *corev1.Affinity {
	affinity := &corev1.Affinity{
		NodeAffinity: &corev1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{
					{
						MatchExpressions: []corev1.NodeSelectorRequirement{
							{
								Key:      "concourse.dev/artifact-cache",
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{"ready"},
							},
						},
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
		daemonKey := handle + "/" + subdir
		b.artifactLocator.Record(key, nodeName, daemonKey)

		if nodeName != "" {
			diskPath := filepath.Join(b.config.ArtifactDaemonHostPath, "steps", handle, subdir)
			b.registerDaemonAlias(nodeName, key, diskPath)
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

func (b *DaemonSetBackend) registerDaemonAlias(nodeName, volumeKey, diskPath string) {
	if b.nodeIPResolver == nil {
		fmt.Fprintf(os.Stderr, "WARNING: registerDaemonAlias: no node IP resolver configured\n")
		return
	}

	port := b.config.ArtifactDaemonPort
	if port == 0 {
		port = 7780
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	nodeIP, err := b.nodeIPResolver.Resolve(ctx, nodeName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: registerDaemonAlias: resolve node IP for %s: %v\n", nodeName, err)
		return
	}

	url := fmt.Sprintf("%s://%s:%d/register", b.daemonScheme(), nodeIP, port)
	body := fmt.Sprintf(`{"key":%q,"local_path":%q}`, volumeKey, diskPath)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: registerDaemonAlias: create request: %v\n", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	// /register is a protected daemon path; use the mTLS-aware client (carries
	// the client cert when TLS is enabled) rather than http.DefaultClient.
	client := newDaemonHTTPClient(b.config, 0)
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: registerDaemonAlias: %s → %v (key=%s)\n", url, err, volumeKey)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		fmt.Fprintf(os.Stderr, "WARNING: registerDaemonAlias: %s → status %d (key=%s)\n", url, resp.StatusCode, volumeKey)
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
		if daemonIP, found, err := b.daemonClient.ProbeResourceCache(probeCtx, key); err == nil && found {
			vol := NewDaemonSetVolumeFromIP(key, handle, workerName, daemonIP, b.config)
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
func (b *DaemonSetBackend) RegisterResourceCache(ctx context.Context, cacheID int, volumeHandle, nodeName string) error {
	if b.daemonClient == nil {
		return fmt.Errorf("daemon client not configured")
	}

	cacheKey := ResourceCacheKey(cacheID)

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
	if err := b.daemonClient.RegisterAlias(ctx, cacheKey, diskPath); err != nil {
		return fmt.Errorf("register resource cache alias: %w", err)
	}

	// Record in locator for affinity on downstream steps.
	if b.artifactLocator != nil && nodeName != "" {
		b.artifactLocator.Record(cacheKey, nodeName, cacheKey)
	}

	return nil
}

// FindResourceCache probes all daemon pods for a cached resource.
func (b *DaemonSetBackend) FindResourceCache(ctx context.Context, cacheID int) (string, bool, error) {
	if b.daemonClient == nil {
		return "", false, nil
	}
	return b.daemonClient.ProbeResourceCache(ctx, ResourceCacheKey(cacheID))
}
