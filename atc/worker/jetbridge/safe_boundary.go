package jetbridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
)

const (
	safeBoundaryOutputMaxBytes = 128
	safeBoundaryStderrMaxBytes = 4096
)

var _ runtime.SafeBoundaryProcess = (*execProcess)(nil)

// AcquireSafeBoundary cooperatively pauses only the exact child started by
// supervisorCommand. It is intentionally not an ATC checkpoint coordinator.
// Its helper owns the stopped state: when its isolated stdin closes, its EXIT
// trap verifies the same PID/starttime and resumes it with CONT and USR2.
func (p *execProcess) AcquireSafeBoundary(ctx context.Context) (runtime.SafeBoundaryLease, error) {
	if ctx == nil {
		return nil, errors.New("safe boundary context is required")
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		return nil, errors.New("safe boundary requires a caller deadline")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p == nil || p.container == nil || p.clientset == nil || nilPodExecutor(p.executor) || strings.TrimSpace(p.config.Namespace) == "" || strings.TrimSpace(p.id) == "" {
		return nil, errors.New("safe boundary process is not configured")
	}
	// Never borrow ProcessIO stdin: the lease gets its own private pipe solely
	// to bind helper lifetime to remote ownership.
	if p.processIO.Stdin != nil {
		return nil, errors.New("safe boundary refuses a process with stdin")
	}
	if p.container.metadata.Type != db.ContainerTypeAgent || !p.container.containerSpec.Hermetic || !p.container.containerSpec.CheckpointCapture {
		return nil, errors.New("safe boundary is unavailable for this process")
	}
	if !checkpointCaptureHandle(p.container.handle) || strings.TrimSpace(p.podName) == "" {
		return nil, errors.New("safe boundary process has invalid pod identity")
	}

	p.container.boundaryMu.Lock()
	if p.container.boundaryActive {
		p.container.boundaryMu.Unlock()
		return nil, errors.New("safe boundary container already has an active process lease")
	}
	p.container.boundaryActive = true
	p.container.boundaryMu.Unlock()
	releaseReservation := func() {
		p.container.boundaryMu.Lock()
		p.container.boundaryActive = false
		p.container.boundaryMu.Unlock()
	}
	fail := func(cause error, helper *safeBoundaryHelper) (runtime.SafeBoundaryLease, error) {
		if helper == nil {
			releaseReservation()
			return nil, cause
		}
		releaseCtx, cancel := context.WithTimeout(context.Background(), checkpointLeaseReleaseTimeout)
		defer cancel()
		releaseErr, complete := helper.release(releaseCtx)
		if complete {
			releaseReservation()
		} else {
			go func() { <-helper.done; releaseReservation() }()
		}
		return nil, errors.Join(cause, releaseErr)
	}

	pod, err := p.exactCheckpointPod(ctx)
	if err != nil {
		return fail(err, nil)
	}
	identity, containers, err := checkpointPodIdentity(pod, p.container.handle, p.podName)
	if err != nil {
		return fail(err, nil)
	}
	stateDir, _ := supervisorState(p.id, p.processSpec)
	helper, child, err := p.acquireSafeBoundaryHelper(ctx, pod.Name, stateDir)
	if err != nil {
		// A timeout or malformed READY may arrive after USR1 but before the
		// helper can own the stopped state. Cancel that pending request using
		// the persisted exact identity; this never falls back to a group kill.
		cancelCtx, cancel := context.WithTimeout(context.Background(), checkpointLeaseReleaseTimeout)
		cancelErr := p.cancelSafeBoundaryRequest(cancelCtx, pod.Name, stateDir)
		cancel()
		err = errors.Join(err, cancelErr)
		return fail(err, helper)
	}
	if err := ctx.Err(); err != nil {
		return fail(err, helper)
	}
	current, err := p.exactCheckpointPod(ctx)
	if err != nil {
		return fail(fmt.Errorf("revalidate safe boundary pod: %w", err), helper)
	}
	currentIdentity, currentContainers, err := checkpointPodIdentity(current, p.container.handle, p.podName)
	if err != nil || !identity.equal(currentIdentity) || !sameCheckpointStrings(containers, currentContainers) {
		if err == nil {
			err = errors.New("safe boundary pod identity changed while acquiring process lease")
		}
		return fail(err, helper)
	}
	lease := &safeBoundaryLease{helper: helper, child: child, releaseReservation: releaseReservation, done: make(chan struct{}), releaseTimeout: checkpointLeaseReleaseTimeout}
	go lease.releaseOnDeadline(ctx)
	return lease, nil
}

func (p *execProcess) cancelSafeBoundaryRequest(ctx context.Context, podName, stateDir string) error {
	script := strings.ReplaceAll(safeBoundaryCancelShell, "__STATE_DIR__", shellQuote(stateDir))
	stdout := &checkpointBoundedBuffer{limit: safeBoundaryOutputMaxBytes}
	stderr := &checkpointBoundedBuffer{limit: safeBoundaryStderrMaxBytes}
	if err := p.executor.ExecInPod(ctx, p.config.Namespace, podName, mainContainerName, []string{"/bin/sh", "-ceu", script}, nil, stdout, stderr, false, ExecAttrs{Purpose: "safe-boundary-cancel"}); err != nil {
		return safeBoundaryHelperError("cancel pending safe boundary", err, stderr)
	}
	return nil
}

type safeBoundaryChild struct {
	pid       int
	startTime uint64
}

type safeBoundaryHelper struct {
	stdin    *io.PipeWriter
	cancel   context.CancelFunc
	done     chan struct{}
	result   error
	resultMu sync.Mutex
	stdout   *checkpointProtocolWriter
	close    sync.Once
}

func (p *execProcess) acquireSafeBoundaryHelper(ctx context.Context, podName, stateDir string) (*safeBoundaryHelper, safeBoundaryChild, error) {
	helperCtx, cancel := context.WithCancel(context.Background())
	stdinReader, stdinWriter := io.Pipe()
	stdout := &checkpointProtocolWriter{notify: make(chan struct{}, 1)}
	stderr := &checkpointBoundedBuffer{limit: safeBoundaryStderrMaxBytes}
	helper := &safeBoundaryHelper{stdin: stdinWriter, cancel: cancel, done: make(chan struct{}), stdout: stdout}
	script := strings.ReplaceAll(safeBoundaryLeaseShell, "__STATE_DIR__", shellQuote(stateDir))
	go func() {
		err := p.executor.ExecInPod(helperCtx, p.config.Namespace, podName, mainContainerName, []string{"/bin/sh", "-ceu", script}, stdinReader, stdout, stderr, false, ExecAttrs{Purpose: "safe-boundary-lease"})
		helper.resultMu.Lock()
		helper.result = err
		helper.resultMu.Unlock()
		close(helper.done)
	}()
	for {
		select {
		case <-ctx.Done():
			return helper, safeBoundaryChild{}, ctx.Err()
		case <-helper.done:
			return helper, safeBoundaryChild{}, safeBoundaryHelperError("safe boundary helper exited before READY", helper.executionError(), stderr)
		case <-stdout.notify:
			child, complete, err := parseSafeBoundaryReady(stdout.snapshot())
			if err != nil {
				return helper, safeBoundaryChild{}, err
			}
			if complete {
				return helper, child, nil
			}
		}
	}
}

func (helper *safeBoundaryHelper) closeInput() {
	helper.close.Do(func() { _ = helper.stdin.Close() })
}

func (helper *safeBoundaryHelper) executionError() error {
	helper.resultMu.Lock()
	defer helper.resultMu.Unlock()
	return helper.result
}

func (helper *safeBoundaryHelper) release(ctx context.Context) (error, bool) {
	helper.closeInput()
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

func parseSafeBoundaryReady(output string, overflow bool) (safeBoundaryChild, bool, error) {
	if overflow || len(output) > safeBoundaryOutputMaxBytes {
		return safeBoundaryChild{}, false, errors.New("safe boundary helper protocol exceeds bound")
	}
	if !strings.Contains(output, "\n") {
		return safeBoundaryChild{}, false, nil
	}
	child, err := parseSafeBoundaryChild(output, "READY")
	return child, true, err
}

func parseSafeBoundaryChild(output, verb string) (safeBoundaryChild, error) {
	if len(output) == 0 || !strings.HasSuffix(output, "\n") || strings.Count(output, "\n") != 1 {
		return safeBoundaryChild{}, errors.New("safe boundary child protocol is invalid")
	}
	fields := strings.Fields(strings.TrimSuffix(output, "\n"))
	if len(fields) != 3 || fields[0] != verb {
		return safeBoundaryChild{}, errors.New("safe boundary child protocol is invalid")
	}
	pid, err := strconv.Atoi(fields[1])
	if err != nil || pid <= 1 || strconv.Itoa(pid) != fields[1] {
		return safeBoundaryChild{}, errors.New("safe boundary child PID is invalid")
	}
	startTime, err := strconv.ParseUint(fields[2], 10, 64)
	if err != nil || startTime == 0 || strconv.FormatUint(startTime, 10) != fields[2] {
		return safeBoundaryChild{}, errors.New("safe boundary child starttime is invalid")
	}
	return safeBoundaryChild{pid: pid, startTime: startTime}, nil
}

type safeBoundaryLease struct {
	helper             *safeBoundaryHelper
	child              safeBoundaryChild
	releaseReservation func()
	release            sync.Once
	releaseErr         error
	done               chan struct{}
	releaseTimeout     time.Duration
}

func (lease *safeBoundaryLease) Release(ctx context.Context) error {
	if lease == nil || ctx == nil {
		return errors.New("safe boundary lease release context is required")
	}
	lease.release.Do(func() {
		defer close(lease.done)
		releaseCtx, cancel := lease.releaseContext(ctx)
		defer cancel()
		var complete bool
		lease.releaseErr, complete = lease.helper.release(releaseCtx)
		if complete {
			lease.releaseReservation()
		} else {
			go func() { <-lease.helper.done; lease.releaseReservation() }()
		}
	})
	return lease.releaseErr
}

func (lease *safeBoundaryLease) releaseContext(caller context.Context) (context.Context, context.CancelFunc) {
	timeout := lease.releaseTimeout
	if timeout <= 0 {
		timeout = checkpointLeaseReleaseTimeout
	}
	// Closing the private pipe requests the remote EXIT trap. Do not inherit a
	// cancelled caller context here: the safety invariant is to make a bounded
	// resume attempt even when the original checkpoint request has expired.
	return context.WithTimeout(context.Background(), timeout)
}

func (lease *safeBoundaryLease) releaseOnDeadline(ctx context.Context) {
	select {
	case <-lease.done:
	case <-ctx.Done():
		// The acquisition deadline is already elapsed, so deliver the EXIT
		// trap with a fresh, bounded context rather than an expired one.
		_ = lease.Release(context.Background())
	}
}

func safeBoundaryHelperError(prefix string, err error, stderr *checkpointBoundedBuffer) error {
	if err == nil {
		err = errors.New("unexpected successful exit")
	}
	if detail := strings.TrimSpace(stderr.String()); detail != "" {
		return fmt.Errorf("%s: %w: %s", prefix, err, detail)
	}
	return fmt.Errorf("%s: %w", prefix, err)
}

// This helper is deliberately a non-TTY secondary exec. Its only stdin is a
// private ownership pipe, never ProcessIO stdin. No signal ever targets a
// process group: every operation revalidates the strict PID/starttime pair.
const safeBoundaryLeaseShell = `
pid=""
start=""
verify() {
  record="$(cat __STATE_DIR__/child 2>/dev/null)"
  set -- $record
  [ "$#" -eq 2 ] || return 1
  if [ -z "$pid" ]; then
    pid="$1"
    start="$2"
  else
    [ "$1" = "$pid" ] && [ "$2" = "$start" ] || return 1
  fi
  case "$pid:$start" in *[!0-9:]*|:*|*:) return 1;; esac
  [ -r "/proc/$pid/stat" ] || return 1
  stat="$(cat "/proc/$pid/stat" 2>/dev/null)"
  rest="${stat##*) }"
  set -- $rest
  [ "$#" -ge 20 ] && [ "${20}" = "$start" ]
}
state() {
  value=""
  while read -r label word rest; do case "$label" in State:) value="$word"; break;; esac; done <"/proc/$pid/status"
  printf '%s' "$value"
}
resume() {
  verify || return 0
  kill -CONT "$pid" 2>/dev/null || true
  kill -USR2 "$pid" 2>/dev/null || true
}
trap 'resume' EXIT HUP INT TERM
verify || exit 1
initial="$(state)"
case "$initial" in T|t|"") exit 1;; esac
kill -USR1 "$pid"
while :; do
  verify || exit 1
  case "$(state)" in T|t) break;; esac
  sleep 1
done
printf 'READY %s %s\n' "$pid" "$start"
cat >/dev/null
exit 0`

const safeBoundaryCancelShell = `
record="$(cat __STATE_DIR__/child 2>/dev/null)"
set -- $record
[ "$#" -eq 2 ] || exit 1
pid="$1"
start="$2"
case "$pid:$start" in *[!0-9:]*|:*|*:) exit 1;; esac
[ -r "/proc/$pid/stat" ] || exit 1
stat="$(cat "/proc/$pid/stat" 2>/dev/null)"
rest="${stat##*) }"
set -- $rest
[ "$#" -ge 20 ] && [ "${20}" = "$start" ] || exit 1
kill -USR2 "$pid"`
