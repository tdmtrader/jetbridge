package jetbridge

import (
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

// supervisorCommand returns the sh invocation that runs the given process
// spec under the task supervisor. The state dir is derived from the process
// ID, which is stable across web restarts, so a re-exec of the same process
// resolves to the same supervisor state.
func supervisorCommand(processID string, spec runtime.ProcessSpec) []string {
	words := make([]string, 0, 1+len(spec.Args))
	for _, w := range append([]string{spec.Path}, spec.Args...) {
		words = append(words, shellQuote(w))
	}

	script := strings.ReplaceAll(supervisorScriptTemplate, "__STATE_DIR__", shellQuote(taskStateDirPrefix+sanitizeForPath(processID)))
	script = strings.ReplaceAll(script, "__COMMAND__", strings.Join(words, " "))

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
