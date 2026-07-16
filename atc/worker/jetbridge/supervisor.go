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
//   - fresh start: launch the command in a background subshell with SIGHUP
//     ignored (survives pty teardown), record its pid, tee output to a log
//     file, and record the exit code to an exit file on completion.
//   - re-exec while the command is still running (web restarted): do NOT
//     restart; replay the log from the beginning and wait for the exit file.
//   - re-exec after completion: replay the log and exit with the recorded
//     code.
//
// State lives under /tmp inside the pod, which survives web restarts because
// the pod itself does, and is reclaimed when the pod is deleted.
//
// Like pauseCommand, this requires only POSIX sh built-ins plus tail/mv,
// which are present in busybox and coreutils images.
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
  ( trap '' HUP; __COMMAND__ >>"$S/log" 2>&1; echo $? >"$S/exit.tmp" && mv "$S/exit.tmp" "$S/exit" ) &
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

const taskStateDirPrefix = "/tmp/concourse-task-"

// supervisorState derives the shell-quoted command line and the supervisor
// state dir for a process spec. The state dir combines the process ID with a
// hash of the command, both stable across web restarts: a new web's
// byte-identical re-exec resolves to the same supervisor state and resumes,
// while a different command on the same container (e.g. a hijack shell) gets
// fresh state and actually runs.
func supervisorState(processID string, spec runtime.ProcessSpec) (stateDir, command string) {
	words := make([]string, 0, 1+len(spec.Args))
	for _, w := range append([]string{spec.Path}, spec.Args...) {
		words = append(words, shellQuote(w))
	}
	command = strings.Join(words, " ")

	h := sha256.Sum256([]byte(command))
	stateDir = taskStateDirPrefix + sanitizeForPath(processID) + "-" + hex.EncodeToString(h[:])[:8]
	return stateDir, command
}

// supervisorCommand returns the sh invocation that runs the given process
// spec under the task supervisor.
func supervisorCommand(processID string, spec runtime.ProcessSpec) []string {
	stateDir, command := supervisorState(processID, spec)

	script := strings.ReplaceAll(supervisorScriptTemplate, "__STATE_DIR__", shellQuote(stateDir))
	script = strings.ReplaceAll(script, "__COMMAND__", command)

	return []string{"sh", "-c", script}
}

// The supervisor deliberately keeps its command alive across severed exec
// sessions — that is the whole point (web-restart resume, park protocol).
// But a step timeout or build abort is a TERMINAL end: nothing will ever
// re-exec this process spec, so leaving the command running only burns
// resources (for an agent step: real API dollars, invisible to budget
// enforcement until PARK-V2 stream-json teeing lands). This script tears the
// supervised process tree down: it resolves the runner subshell's process
// group (which the whole tree shares — the supervisor sh was made session
// leader by the tty exec, and nothing under it changes group), sends the
// group SIGTERM (agent-runner traps it, kills claude, and flushes the flight
// recorder before exiting), waits up to the grace period for the group to
// die, then escalates to SIGKILL. It is idempotent and safe to run when the
// command never started or already finished. Like the supervisor script it
// needs only POSIX sh built-ins plus cat/sed/cut/sleep (busybox-compatible);
// `ps -o pgid=` is a fallback for /proc-less environments (test hosts).
// Group kills use `kill -SIG "-$pgid"` with NO `--` separator: the dash and
// busybox builtin kills reject `--` before a negative pid ("Illegal number:
// -"), which would silently no-op the teardown; the separator-less form
// parses on dash, busybox, and bash alike.
const supervisorKillScriptTemplate = `S=__STATE_DIR__
pid="$(cat "$S/pid" 2>/dev/null)"
[ -n "$pid" ] || exit 0
kill -0 "$pid" 2>/dev/null || exit 0
if [ -r "/proc/$pid/stat" ]; then
  pgid="$(sed 's/^.*) //' "/proc/$pid/stat" | cut -d' ' -f3)"
else
  pgid="$(ps -o pgid= -p "$pid" 2>/dev/null | tr -d ' ')"
fi
if [ -z "$pgid" ]; then
  kill -TERM "$pid" 2>/dev/null
  exit 0
fi
kill -TERM "-$pgid" 2>/dev/null
n=0
while [ "$n" -lt __GRACE_SECONDS__ ] && kill -0 "-$pgid" 2>/dev/null; do
  sleep 1
  n=$((n+1))
done
kill -0 "-$pgid" 2>/dev/null && kill -KILL "-$pgid" 2>/dev/null
exit 0`

// supervisorKillCommand returns the sh invocation that kills the supervised
// process tree previously started by supervisorCommand for the same process
// ID and spec (the state dir derivation must match exactly, or the kill
// resolves to the wrong — likely nonexistent — state).
func supervisorKillCommand(processID string, spec runtime.ProcessSpec, graceSeconds int) []string {
	stateDir, _ := supervisorState(processID, spec)

	script := strings.ReplaceAll(supervisorKillScriptTemplate, "__STATE_DIR__", shellQuote(stateDir))
	script = strings.ReplaceAll(script, "__GRACE_SECONDS__", strconv.Itoa(graceSeconds))

	return []string{"sh", "-c", script}
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
