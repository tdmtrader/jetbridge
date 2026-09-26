package jetbridge

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	"github.com/concourse/concourse/atc/runtime"
)

// Task steps run their command under a small in-pod supervisor script so
// that a build survives a web restart. The exec session runs with a TTY, so
// when the web dies mid-step the pty closes and the foreground process group
// receives SIGHUP — without a supervisor the task process dies and the next
// web's re-exec restarts the command from scratch in a dirty workspace
// (possibly alongside a survivor).
//
// The supervisor makes re-exec idempotent and resumptive:
//   - fresh start: launch the command detached from the exec session (see
//     below), record the runner's pid, send output to a log file, and record
//     the exit code to an exit file on completion.
//   - re-exec while the command is still running (web restarted): do NOT
//     restart; replay the log from the beginning and wait for the exit file.
//   - re-exec after completion: replay the log and exit with the recorded
//     code.
//
// State lives under /tmp inside the pod, which survives web restarts because
// the pod itself does, and is reclaimed when the pod is deleted.
//
// Detached means a session of its own. The pty's hangup SIGHUPs the exec
// session's foreground process group, and ignoring SIGHUP in the runner only
// protects a command that keeps it ignored: any program that installs its own
// handler hands its children SIGHUP at the default action again. dockerd is
// one -- it reloads config on SIGHUP -- and the docker-proxy processes it
// starts died with the exec session while containerd, which it starts under
// setsid, survived. So the command runs under setsid, as the exact supervisor
// already does, and only the runner -- plain sh, which waits for it and writes
// its exit -- stays in the exec session with SIGHUP ignored. Nothing else
// signals an ordinary task's command: an abort or timeout deletes the whole
// pod (process.go), which reaches every session in it.
//
// setsid is started as a non-job-controlled background child so it cannot
// already be a process group leader and fork away from the status the runner
// waits for (BusyBox setsid has no --wait), as in resource_process.go.
//
// An image without setsid (BusyBox and util-linux both provide it, and
// util-linux is in Debian's essential set) falls back to the runner's SIGHUP
// shield alone, and says so once in the step's log: its command survives a
// web restart only if it and its children leave SIGHUP ignored.
//
// Like pauseCommand, this requires only POSIX sh built-ins plus tail/mv,
// which are present in busybox and coreutils images, and setsid where it
// can get it.
// Note: the runner-liveness check must go through alive() — busybox
// `kill -0 ""` exits 0, so a bare kill on the (possibly empty) pid file
// would misread "never started" as "running".
const supervisorScriptTemplate = `S=__STATE_DIR__
alive() {
  pid="$(cat "$S/pid" 2>/dev/null)"
  [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null
}
mkdir -p "$S"
: >>"$S/log"
if [ ! -f "$S/exit" ] && ! alive; then
  (
    trap '' HUP
    if command -v setsid >/dev/null 2>&1; then
      setsid sh -c __DETACHED_COMMAND__ </dev/null >>"$S/log" 2>&1 &
    else
      echo "` + supervisorNoSetsidNotice + `" >>"$S/log"
      __COMMAND__ </dev/null >>"$S/log" 2>&1 &
    fi
    wait "$!"
    echo $? >"$S/exit.tmp" && mv "$S/exit.tmp" "$S/exit"
  ) &
  echo $! >"$S/pid"
fi
tail -n +1 -f "$S/log" 2>/dev/null &
T=$!
while [ ! -f "$S/exit" ] && alive; do sleep 1; done
sleep 2
kill "$T" 2>/dev/null
wait "$T" 2>/dev/null
if [ -f "$S/exit" ]; then exit "$(cat "$S/exit")"; fi
exit 255`

// supervisorNoSetsidNotice is the line an image without setsid gets in its
// step log, once, when the command is launched.
const supervisorNoSetsidNotice = "[supervisor] setsid is not in this image; " +
	"the command stays in the exec session and survives a web restart only while it and its children keep SIGHUP ignored"

// The exact-execution supervisor is the same script with one more durable
// record and one more refusal.
//
// Requirement 4 puts a start record before the child and an outcome record
// before the result, and requirement 6 forbids re-executing a producer from
// exact process start onward. Today's script violates the second: on a re-exec
// it relaunches whenever there is no exit file and nothing is alive, which is
// exactly the state a crash after real process start leaves behind. For an
// ordinary task that is the right behaviour and it is why the script exists;
// for a controlled execution it is a second run of a command that already ran,
// with whatever it did to the workspace still there.
//
// So the exact script records the start FIRST, atomically, and then refuses to
// relaunch when it finds one with no outcome and no live runner. That state is
// not a failure to be retried, it is a typed unresolved outcome: the command
// ran, its fate is unprovable from inside the pod, and only the daemon's ledger
// can say more.
//
// The start is CLAIMED, not written: under noclobber the shell creates it with
// O_EXCL, so of two deliveries racing -- or a delivery racing the closing of an
// undelivered start (process_outcome_recovery.go) -- exactly one proceeds. Only
// its existence is ever read. The stop check follows the claim and precedes the
// launch, so a stop written before a delivery claims the start always stops it.
const exactSupervisorScriptTemplate = `S=__STATE_DIR__
alive() {
  pid="$(cat "$S/pid" 2>/dev/null)"
  [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null
}
mkdir -p "$S"
: >>"$S/log"
if [ ! -f "$S/exit" ] && ! alive; then
  if ! ( set -C; echo "started" >"$S/start" ) 2>/dev/null; then
    echo "[exact-supervisor] this command already started and left no outcome; it is not run again" >&2
    exit __UNRESOLVED_CODE__
  fi
  (
    trap '' HUP
    if [ -f "$S/stop" ]; then
      E=143
    else
      setsid sh -c __DETACHED_COMMAND__ >>"$S/log" 2>&1 &
      C=$!
      (
        while kill -0 "$C" 2>/dev/null; do
          if [ -f "$S/stop" ]; then
            kill -TERM "-$C" 2>/dev/null
            sleep 2
            kill -KILL "-$C" 2>/dev/null
            exit 0
          fi
          sleep 1
        done
      ) &
      W=$!
      wait "$C"
      E=$?
      if [ ! -f "$S/stop" ]; then kill "$W" 2>/dev/null; fi
      wait "$W" 2>/dev/null
    fi
    printf '%s\n' "$E" >"$S/exit.tmp" && mv "$S/exit.tmp" "$S/exit"
  ) &
  echo $! >"$S/pid"
fi
tail -n +1 -f "$S/log" 2>/dev/null &
T=$!
while [ ! -f "$S/exit" ] && alive; do sleep 1; done
sleep 2
kill "$T" 2>/dev/null
wait "$T" 2>/dev/null
if [ -f "$S/exit" ]; then exit "$(cat "$S/exit")"; fi
exit 255`

// ExactUnresolvedExitCode is what the exact supervisor exits with when it finds
// a recorded start, no outcome and nothing running.
//
// It is a distinct code rather than a failure because the two mean opposite
// things to the caller: a failure is a producer that ran and lost, and this is
// a producer whose result nobody can prove. The first may fail the step; the
// second may not authorize cleanup, may not release a hold, and may never be
// re-run. 254 is chosen for the same reason 255 already is -- outside the range
// a task's own command is likely to use, and adjacent to the one that already
// means "the supervisor could not say".
const ExactUnresolvedExitCode = 254

const taskStateDirPrefix = "/tmp/concourse-task-"

// supervisorCommand returns the sh invocation that runs the given process
// spec under the task supervisor. The state dir is derived from the process
// ID plus a hash of the command, both stable across web restarts: a new
// web's byte-identical re-exec resolves to the same supervisor state and
// resumes, while a different command on the same container (e.g. a hijack
// shell) gets fresh state and actually runs.
func supervisorCommand(processID string, spec runtime.ProcessSpec) []string {
	return buildSupervisorCommand(processID, spec, supervisorScriptTemplate)
}

// exactSupervisorCommand is the same invocation under the exact-execution
// script. It is a separate function rather than a boolean parameter because the
// two scripts are two contracts: one may re-run a command that stopped, and one
// may not, and a call site that read `true` would not say which it chose.
func exactSupervisorCommand(processID string, spec runtime.ProcessSpec) []string {
	return buildSupervisorCommand(processID, spec, exactSupervisorScriptTemplate)
}

func buildSupervisorCommand(processID string, spec runtime.ProcessSpec, template string) []string {
	command, stateDir := supervisorCommandParts(processID, spec)
	script := strings.ReplaceAll(template, "__STATE_DIR__", shellQuote(stateDir))
	script = strings.ReplaceAll(script, "__COMMAND__", command)
	script = strings.ReplaceAll(script, "__DETACHED_COMMAND__", shellQuote(command))
	script = strings.ReplaceAll(script, "__UNRESOLVED_CODE__", strconv.Itoa(ExactUnresolvedExitCode))

	return []string{"sh", "-c", script}
}

func supervisorCommandParts(processID string, spec runtime.ProcessSpec) (string, string) {
	words := make([]string, 0, 1+len(spec.Args))
	for _, w := range append([]string{spec.Path}, spec.Args...) {
		words = append(words, shellQuote(w))
	}
	command := strings.Join(words, " ")

	h := sha256.Sum256([]byte(command))
	stateDir := taskStateDirPrefix + sanitizeForPath(processID) + "-" + hex.EncodeToString(h[:])[:8]

	return command, stateDir
}

// shellQuote returns s as a single-quoted POSIX shell word.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// sanitizeForPath replaces any character outside [A-Za-z0-9_-] with '-' so
// the process ID can be used as a filesystem path segment.
func sanitizeForPath(s string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, s)
}
