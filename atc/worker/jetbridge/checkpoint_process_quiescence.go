package jetbridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	checkpointQuiescenceProtocolMaxBytes = 128
	checkpointQuiescenceStderrMaxBytes   = 4096
	checkpointLeaseReleaseTimeout        = 5 * time.Second
)

var _ runtime.CheckpointProcess = (*execProcess)(nil)

// AcquireCheckpointCapture stops every running container in the exact agent
// pod. It is intentionally attached to execProcess rather than runtime.Process
// so non-Jetbridge runtimes never gain checkpoint authority by accident.
func (p *execProcess) AcquireCheckpointCapture(ctx context.Context, maxBytes int64) (runtime.CheckpointCaptureLease, error) {
	if ctx == nil {
		return nil, errors.New("checkpoint capture context is required")
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		return nil, errors.New("checkpoint capture requires a caller deadline")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p == nil || p.container == nil || p.clientset == nil || nilPodExecutor(p.executor) || strings.TrimSpace(p.config.Namespace) == "" {
		return nil, errors.New("checkpoint capture process is not configured")
	}
	if p.container.metadata.Type != db.ContainerTypeAgent || !p.container.containerSpec.CheckpointCapture {
		return nil, errors.New("checkpoint capture is unavailable for this process")
	}
	if !checkpointCaptureHandle(p.container.handle) || p.podName == "" {
		return nil, errors.New("checkpoint capture process has invalid pod identity")
	}
	if _, _, err := p.container.checkpointSessionVolume(); err != nil {
		return nil, err
	}
	topology, err := runtime.CheckpointCaptureTopologyForSpec(p.container.containerSpec)
	if err != nil {
		return nil, err
	}
	archive, err := topology.ArchiveRequest(p.container.handle, maxBytes)
	if err != nil {
		return nil, err
	}

	p.container.checkpointMu.Lock()
	if p.container.checkpointActive {
		p.container.checkpointMu.Unlock()
		return nil, errors.New("checkpoint container already has an active process lease")
	}
	p.container.checkpointActive = true
	p.container.checkpointMu.Unlock()
	releaseReservation := func() {
		p.container.checkpointMu.Lock()
		p.container.checkpointActive = false
		p.container.checkpointMu.Unlock()
	}

	pod, err := p.exactCheckpointPod(ctx)
	if err != nil {
		releaseReservation()
		return nil, err
	}
	identity, containers, err := checkpointPodIdentity(pod, p.container.handle, p.podName)
	if err != nil {
		releaseReservation()
		return nil, err
	}
	helpers := make([]*checkpointContainerHelper, 0, len(containers))
	fail := func(cause error) (runtime.CheckpointCaptureLease, error) {
		resumeCtx, cancel := context.WithTimeout(context.Background(), checkpointLeaseReleaseTimeout)
		defer cancel()
		resumeErr, complete := releaseCheckpointHelpers(resumeCtx, helpers)
		if complete {
			releaseReservation()
		} else {
			releaseCheckpointReservationWhenHelpersExit(helpers, releaseReservation)
		}
		return nil, errors.Join(cause, resumeErr)
	}
	for _, name := range containers {
		helper, count, helperErr := p.acquireCheckpointHelper(ctx, pod.Name, name)
		if helper != nil {
			helpers = append(helpers, helper)
		}
		if helperErr != nil {
			return fail(fmt.Errorf("quiesce container %q: %w", name, helperErr))
		}
		if count <= 0 {
			return fail(fmt.Errorf("quiesce container %q: helper reported no managed processes", name))
		}
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	for _, helper := range helpers {
		if ended, helperErr := helper.ended(); ended {
			if helperErr == nil {
				helperErr = errors.New("unexpected successful helper exit")
			}
			return fail(fmt.Errorf("quiesce container %q: %w", helper.container, helperErr))
		}
		if err := helper.protocolValid(); err != nil {
			return fail(fmt.Errorf("quiesce container %q: %w", helper.container, err))
		}
	}
	// Fetch again after all helpers report STOP verification. A replacement pod,
	// a changed node, or changed container accounting invalidates the lease.
	current, err := p.clientset.CoreV1().Pods(p.config.Namespace).Get(ctx, p.podName, metav1.GetOptions{})
	if err != nil {
		return fail(fmt.Errorf("revalidate checkpoint pod: %w", err))
	}
	currentIdentity, currentContainers, err := checkpointPodIdentity(current, p.container.handle, p.podName)
	if err != nil || !identity.equal(currentIdentity) || !sameCheckpointStrings(containers, currentContainers) {
		if err == nil {
			err = errors.New("checkpoint pod identity changed while acquiring process lease")
		}
		return fail(err)
	}
	currentTopology, err := runtime.CheckpointCaptureTopologyForSpec(p.container.containerSpec)
	if err != nil || !reflect.DeepEqual(topology, currentTopology) {
		if err == nil {
			err = errors.New("checkpoint capture topology changed while acquiring process lease")
		}
		return fail(err)
	}
	target := runtime.CheckpointCaptureTarget{
		ContainerHandle: p.container.handle,
		PodName:         pod.Name,
		PodUID:          string(pod.UID),
		NodeName:        pod.Spec.NodeName,
		Archive:         archive,
	}
	if err := target.Validate(); err != nil {
		return fail(err)
	}
	lease := &checkpointCaptureLease{target: target.Clone(), helpers: helpers, releaseReservation: releaseReservation, done: make(chan struct{})}
	go lease.releaseOnDeadline(ctx)
	return lease, nil
}

func (p *execProcess) exactCheckpointPod(ctx context.Context) (*corev1.Pod, error) {
	pod, err := p.clientset.CoreV1().Pods(p.config.Namespace).Get(ctx, p.podName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get exact checkpoint pod: %w", err)
	}
	if pod.Name != p.podName {
		return nil, errors.New("checkpoint pod name changed while resolving")
	}
	return pod, nil
}

type checkpointPodEvidence struct {
	uid    string
	node   string
	labels map[string]string
}

func (e checkpointPodEvidence) equal(other checkpointPodEvidence) bool {
	return e.uid == other.uid && e.node == other.node && reflect.DeepEqual(e.labels, other.labels)
}

func checkpointPodIdentity(pod *corev1.Pod, handle, expectedName string) (checkpointPodEvidence, []string, error) {
	if pod == nil || pod.Name == "" || pod.Name != expectedName || pod.UID == "" || pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning || strings.TrimSpace(pod.Spec.NodeName) == "" || pod.Labels[handleLabelKey] != handle || pod.Labels[typeLabelKey] != string(db.ContainerTypeAgent) || pod.Labels[hermeticLabelKey] != "true" {
		return checkpointPodEvidence{}, nil, errors.New("checkpoint pod is not the exact live hermetic agent pod")
	}
	if len(pod.Spec.EphemeralContainers) != 0 || len(pod.Status.EphemeralContainerStatuses) != 0 {
		return checkpointPodEvidence{}, nil, errors.New("checkpoint pod has ephemeral containers")
	}
	if err := checkpointInitStatuses(pod); err != nil {
		return checkpointPodEvidence{}, nil, err
	}
	declared := make(map[string]struct{}, len(pod.Spec.Containers))
	containers := make([]string, 0, len(pod.Spec.Containers))
	for _, container := range pod.Spec.Containers {
		if container.Name == "" {
			return checkpointPodEvidence{}, nil, errors.New("checkpoint pod has unnamed container")
		}
		if _, duplicate := declared[container.Name]; duplicate {
			return checkpointPodEvidence{}, nil, errors.New("checkpoint pod has duplicate container")
		}
		declared[container.Name] = struct{}{}
		containers = append(containers, container.Name)
	}
	if _, found := declared[mainContainerName]; !found || len(declared) == 0 || len(pod.Status.ContainerStatuses) != len(declared) {
		return checkpointPodEvidence{}, nil, errors.New("checkpoint pod has unaccounted container state")
	}
	for _, status := range pod.Status.ContainerStatuses {
		if _, found := declared[status.Name]; !found || status.State.Running == nil {
			return checkpointPodEvidence{}, nil, errors.New("checkpoint pod has a non-running or unaccounted container")
		}
		delete(declared, status.Name)
	}
	if len(declared) != 0 {
		return checkpointPodEvidence{}, nil, errors.New("checkpoint pod has missing container state")
	}
	labels := make(map[string]string, len(pod.Labels))
	for key, value := range pod.Labels {
		labels[key] = value
	}
	return checkpointPodEvidence{uid: string(pod.UID), node: pod.Spec.NodeName, labels: labels}, containers, nil
}

func checkpointInitStatuses(pod *corev1.Pod) error {
	declared := make(map[string]struct{}, len(pod.Spec.InitContainers))
	for _, container := range pod.Spec.InitContainers {
		if container.Name == "" {
			return errors.New("checkpoint pod has unnamed init container")
		}
		if _, duplicate := declared[container.Name]; duplicate {
			return errors.New("checkpoint pod has duplicate init container")
		}
		declared[container.Name] = struct{}{}
	}
	if len(pod.Status.InitContainerStatuses) != len(declared) {
		return errors.New("checkpoint pod has unaccounted init container state")
	}
	for _, status := range pod.Status.InitContainerStatuses {
		if _, found := declared[status.Name]; !found || status.State.Running != nil || (status.State.Terminated == nil && status.State.Waiting == nil) {
			return errors.New("checkpoint pod has a running or unaccounted init container")
		}
		delete(declared, status.Name)
	}
	if len(declared) != 0 {
		return errors.New("checkpoint pod has missing init container state")
	}
	return nil
}

type checkpointContainerHelper struct {
	container string
	stdin     *io.PipeWriter
	cancel    context.CancelFunc
	done      chan struct{}
	resultMu  sync.Mutex
	result    error
	closeOnce sync.Once
	stdout    *checkpointProtocolWriter
}

func (p *execProcess) acquireCheckpointHelper(ctx context.Context, podName, container string) (*checkpointContainerHelper, int, error) {
	helperCtx, cancel := context.WithCancel(context.Background())
	stdinReader, stdinWriter := io.Pipe()
	stdout := &checkpointProtocolWriter{notify: make(chan struct{}, 1)}
	stderr := &checkpointBoundedBuffer{limit: checkpointQuiescenceStderrMaxBytes}
	helper := &checkpointContainerHelper{container: container, stdin: stdinWriter, cancel: cancel, done: make(chan struct{}), stdout: stdout}
	go func() {
		err := p.executor.ExecInPod(helperCtx, p.config.Namespace, podName, container, []string{"/bin/sh", "-ceu", checkpointQuiescenceShell}, stdinReader, stdout, stderr, false, ExecAttrs{Purpose: "checkpoint-process-quiescence"})
		helper.resultMu.Lock()
		helper.result = err
		helper.resultMu.Unlock()
		close(helper.done)
	}()
	for {
		select {
		case <-ctx.Done():
			return helper, 0, ctx.Err()
		case <-helper.done:
			return helper, 0, checkpointHelperError("helper exited before READY", helper.executionError(), stderr)
		case <-stdout.notify:
			count, complete, err := parseCheckpointReady(stdout.snapshot())
			if err != nil {
				return helper, 0, err
			}
			if complete {
				return helper, count, nil
			}
		}
	}
}

func (helper *checkpointContainerHelper) ended() (bool, error) {
	select {
	case <-helper.done:
		return true, helper.executionError()
	default:
		return false, nil
	}
}
func (helper *checkpointContainerHelper) closeInput() {
	helper.closeOnce.Do(func() { _ = helper.stdin.Close() })
}
func (helper *checkpointContainerHelper) executionError() error {
	helper.resultMu.Lock()
	defer helper.resultMu.Unlock()
	return helper.result
}
func (helper *checkpointContainerHelper) wait(ctx context.Context) (error, bool) {
	select {
	case <-helper.done:
		return helper.executionError(), true
	case <-ctx.Done():
		helper.cancel()
		select {
		case <-helper.done:
			return errors.Join(ctx.Err(), helper.executionError()), true
		default:
			return ctx.Err(), false
		}
	}
}
func (helper *checkpointContainerHelper) protocolValid() error {
	_, complete, err := parseCheckpointReady(helper.stdout.snapshot())
	if err != nil {
		return err
	}
	if !complete {
		return errors.New("checkpoint helper protocol is incomplete")
	}
	return nil
}

type checkpointCaptureLease struct {
	target             runtime.CheckpointCaptureTarget
	helpers            []*checkpointContainerHelper
	releaseReservation func()
	release            sync.Once
	releaseErr         error
	done               chan struct{}
}

func (lease *checkpointCaptureLease) CaptureTarget() runtime.CheckpointCaptureTarget {
	if lease == nil {
		return runtime.CheckpointCaptureTarget{}
	}
	return lease.target.Clone()
}
func (lease *checkpointCaptureLease) Release(ctx context.Context) error {
	if lease == nil || ctx == nil {
		return errors.New("checkpoint capture lease release context is required")
	}
	lease.release.Do(func() {
		defer close(lease.done)
		var complete bool
		lease.releaseErr, complete = releaseCheckpointHelpers(ctx, lease.helpers)
		if complete {
			lease.releaseReservation()
		} else {
			releaseCheckpointReservationWhenHelpersExit(lease.helpers, lease.releaseReservation)
		}
	})
	return lease.releaseErr
}
func (lease *checkpointCaptureLease) releaseOnDeadline(ctx context.Context) {
	select {
	case <-lease.done:
	case <-ctx.Done():
		releaseCtx, cancel := context.WithTimeout(context.Background(), checkpointLeaseReleaseTimeout)
		defer cancel()
		_ = lease.Release(releaseCtx)
	}
}

func releaseCheckpointHelpers(ctx context.Context, helpers []*checkpointContainerHelper) (error, bool) {
	for _, helper := range helpers {
		helper.closeInput()
	}
	var result error
	complete := true
	for _, helper := range helpers {
		err, exited := helper.wait(ctx)
		result = errors.Join(result, err)
		complete = complete && exited
	}
	return result, complete
}
func releaseCheckpointReservationWhenHelpersExit(helpers []*checkpointContainerHelper, release func()) {
	go func() {
		for _, helper := range helpers {
			<-helper.done
		}
		release()
	}()
}

type checkpointProtocolWriter struct {
	mutex    sync.Mutex
	output   []byte
	overflow bool
	notify   chan struct{}
}

func (writer *checkpointProtocolWriter) Write(value []byte) (int, error) {
	writer.mutex.Lock()
	if len(writer.output)+len(value) > checkpointQuiescenceProtocolMaxBytes {
		writer.overflow = true
	} else {
		writer.output = append(writer.output, value...)
	}
	writer.mutex.Unlock()
	select {
	case writer.notify <- struct{}{}:
	default:
	}
	return len(value), nil
}
func (writer *checkpointProtocolWriter) snapshot() (string, bool) {
	writer.mutex.Lock()
	defer writer.mutex.Unlock()
	return string(writer.output), writer.overflow
}

type checkpointBoundedBuffer struct {
	mutex    sync.Mutex
	limit    int
	output   []byte
	overflow bool
}

func (buffer *checkpointBoundedBuffer) Write(value []byte) (int, error) {
	buffer.mutex.Lock()
	defer buffer.mutex.Unlock()
	if len(buffer.output)+len(value) > buffer.limit {
		buffer.overflow = true
		remaining := buffer.limit - len(buffer.output)
		if remaining > 0 {
			buffer.output = append(buffer.output, value[:remaining]...)
		}
	} else {
		buffer.output = append(buffer.output, value...)
	}
	return len(value), nil
}
func (buffer *checkpointBoundedBuffer) String() string {
	buffer.mutex.Lock()
	defer buffer.mutex.Unlock()
	if buffer.overflow {
		return string(buffer.output) + " [truncated]"
	}
	return string(buffer.output)
}
func parseCheckpointReady(output string, overflow bool) (int, bool, error) {
	if overflow {
		return 0, false, errors.New("checkpoint helper protocol exceeds bound")
	}
	if !strings.Contains(output, "\n") {
		return 0, false, nil
	}
	if strings.Count(output, "\n") != 1 || !strings.HasSuffix(output, "\n") {
		return 0, false, errors.New("checkpoint helper protocol mutated")
	}
	fields := strings.Fields(strings.TrimSuffix(output, "\n"))
	if len(fields) != 2 || fields[0] != "READY" {
		return 0, false, errors.New("checkpoint helper protocol is invalid")
	}
	count, err := strconv.Atoi(fields[1])
	if err != nil || count <= 0 {
		return 0, false, errors.New("checkpoint helper reported invalid process count")
	}
	return count, true, nil
}
func checkpointHelperError(prefix string, err error, stderr *checkpointBoundedBuffer) error {
	if err == nil {
		err = errors.New("unexpected successful exit")
	}
	if detail := strings.TrimSpace(stderr.String()); detail != "" {
		return fmt.Errorf("%s: %w: %s", prefix, err, detail)
	}
	return fmt.Errorf("%s: %w", prefix, err)
}
func nilPodExecutor(executor PodExecutor) bool {
	if executor == nil {
		return true
	}
	value := reflect.ValueOf(executor)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
func checkpointCaptureHandle(handle string) bool {
	return handle != "" && strings.TrimSpace(handle) == handle && !strings.ContainsAny(handle, "/\\\x00")
}
func sameCheckpointStrings(left, right []string) bool { return reflect.DeepEqual(left, right) }

// The helper snapshots PID/start-time pairs, stops and verifies all processes,
// then holds stdin open as the lease. Closing stdin (including an SPDY
// disconnect) runs the EXIT trap and resumes only the processes it stopped.
const checkpointQuiescenceShell = `
set -f
MAX=256
PAIRS=""
COUNT=0
RELEASED=0
read_start() { START=""; [ -r "$1" ] || return 1; read -r line < "$1" || return 1; tail=${line##*) }; set -- $tail; [ "$#" -ge 20 ] || return 1; case "${20}" in ''|*[!0-9]*) return 1;; esac; START=${20}; }
read_state() { STATE=""; [ -r "$1" ] || return 1; while read -r label state rest; do case "$label" in State:) STATE=$state; return 0;; esac; done < "$1"; return 1; }
resume() { [ "$RELEASED" = 1 ] && return; RELEASED=1; for pair in $PAIRS; do pid=${pair%%:*}; start=${pair#*:}; stat=/proc/$pid/stat; if read_start "$stat" && [ "$START" = "$start" ]; then kill -CONT "$pid" 2>/dev/null || :; fi; done; }
fail() { resume; exit 1; }
trap 'resume' 0 HUP INT TERM
pass=0
while [ "$pass" -lt 4 ]; do added=0; for stat in /proc/[0-9]*/stat; do pid=${stat#/proc/}; pid=${pid%/stat}; [ "$pid" = "$$" ] && continue; read_start "$stat" || fail; pair=$pid:$START; case " $PAIRS " in *" $pair "*) continue;; esac; [ "$COUNT" -lt "$MAX" ] || fail; kill -STOP "$pid" 2>/dev/null || fail; read_state "${stat%/stat}/status" || fail; [ "$STATE" = T ] || fail; PAIRS="$PAIRS $pair"; COUNT=$((COUNT + 1)); added=1; done; [ "$added" = 0 ] && break; pass=$((pass + 1)); done
[ "$COUNT" -gt 0 ] || fail
[ "$pass" -lt 4 ] || fail
for stat in /proc/[0-9]*/stat; do pid=${stat#/proc/}; pid=${pid%/stat}; [ "$pid" = "$$" ] && continue; read_start "$stat" || fail; pair=$pid:$START; case " $PAIRS " in *" $pair "*) ;; *) fail;; esac; read_state "${stat%/stat}/status" || fail; [ "$STATE" = T ] || fail; done
printf 'READY %s\n' "$COUNT"
while read -r ignored; do :; done
`
