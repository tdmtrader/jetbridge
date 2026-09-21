package jetbridge

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/google/uuid"
)

// Resource exec has no terminal: closing its SPDY stream does not stop its
// children. Give each invocation its own process group, without redirecting
// the resource protocol through the task supervisor's log files. This requires
// sh, setsid, mkdir, mv, rm and rmdir in the resource image (including BusyBox).
// A command that deliberately detaches into another session is outside this
// group; this is not a per-command cgroup or a substitute for pod teardown.
func (p *execProcess) resourceCommand() bool {
	if p.container == nil || p.container.lookedUp {
		return false
	}
	switch p.container.metadata.Type {
	case db.ContainerTypeGet, db.ContainerTypePut, db.ContainerTypeCheck:
		return true
	default:
		return false
	}
}

// /proc field 22 identifies a process lifetime, not just a reusable PID.
// Remove through the LAST ') ' because comm may itself contain parentheses.
const resourceProcessIdentity = `identity() {
  IFS= read -r stat < "/proc/$1/stat" || return 1
  stat=${stat##*) }
  set -- $stat
  [ "$#" -ge 20 ] || return 1
  shift 19
  printf '%s\n' "$1"
}
`

const resourceCommandScript = resourceProcessIdentity + `S=$1
shift
mask=$(umask)
umask 077
mkdir -p "$S" || exit 125
birth=$(identity "$$") || exit 125
printf '%s %s\n' "$$" "$birth" > "$S/pid.tmp" && mv "$S/pid.tmp" "$S/pid" || exit 125
umask "$mask"
if [ -f "$S/cancel" ]; then rm -f "$S/pid"; exit 130; fi
"$@"
status=$?
rm -f "$S/pid"
rmdir "$S" 2>/dev/null || :
exit "$status"`

const cancelResourceScript = resourceProcessIdentity + `S=$1
umask 077
mkdir -p "$S" && : > "$S/cancel" || exit 125
# Leave the cancellation marker: a late exec must not start after this returns.
[ -f "$S/pid" ] || exit 0
read -r pid birth < "$S/pid" || exit 125
case "$pid" in ''|*[!0-9]*) exit 125;; esac
[ "$pid" -gt 1 ] || exit 125
current=$(identity "$pid" 2>/dev/null) || exit 0
[ "$current" = "$birth" ] || exit 0
# Kill the group, not the pod, its PID 1, or a concurrent hijack session.
# "kill -9 -PGID" is the one spelling every /bin/sh accepts: dash rejects
# "-s KILL -PGID" as an illegal option, and BusyBox rejects a "--" before it.
kill -9 "-$pid" 2>/dev/null || {
  current=$(identity "$pid" 2>/dev/null) || exit 0
  [ "$current" != "$birth" ]
}`

const resourceStateDirPrefix = "/tmp/concourse-resource-"

func cancellableResourceCommand(command []string) ([]string, string) {
	state := resourceStateDirPrefix + uuid.NewString()
	// BusyBox setsid has no portable --wait. Start it as a non-job-controlled
	// background child so it cannot already be a group leader and fork away
	// from the status we wait for. Explicit stdin avoids sh's async /dev/null.
	wrapped := []string{"sh", "-c", `exec 3<&0
setsid "$@" <&3 3<&- &
child=$!
exec 3<&-
wait "$child"`, "resource-session", "sh", "-c", resourceCommandScript, "resource-command", state}
	return append(wrapped, command...), state
}

// An exact resource command also journals its own exit, because nothing else
// in the Pod would: the task supervisor is not used for the resource protocol,
// and an ATC that loses the command -- a restart, a lost answer, an expired
// stop grace -- would otherwise leave the node's ledger executing with no
// outcome writer and Run cancellation with nothing to interrupt or recover.
//
// The journal is written outside the command's own process group -- by a
// runner subshell of this session that waits for it -- never from inside it:
// the cancel script kills that whole group, and a journal written from inside
// it would die with it. The state directory is derived from the execution
// identity rather than minted, so a restarted ATC -- and the signed start the
// Run retains -- can name it again.
//
// The command's stdout and stderr are journaled too, and the command writes
// ONLY there, never to the exec stream. A closed stream -- the web gone, the
// transport torn down -- would otherwise meet the command's next write as a
// SIGPIPE, and 141 journaled as its own exit is a failure it never had. This
// session is the only writer to the stream and ignores SIGPIPE (and SIGHUP,
// as the task supervisor does): it follows stderr live, so a user still sees
// a resource's progress, and sends stdout -- the resource protocol's answer,
// read only once the command exits -- after the exit is journaled. Recovery
// reads the same stdout back (readJournaledStdout), so a get or put recovered
// from the journal still has its version. It needs only POSIX sh plus wc,
// tail -c, head -c and cat, which BusyBox and coreutils both provide.
//
// Like the exact supervisor, a delivery that cannot claim the start -- it is
// created with O_EXCL, so one claimant wins a race -- refuses with the
// unresolved code rather than running the command twice. Its exit is the
// command's own status: 130 when its cancellation marker preceded it, 137 when
// the cancel script killed its group. The ATC never writes one.
const journaledResourceSessionScript = `S=$1
shift
( umask 077; mkdir -p "$S" ) || exit 125
if ! ( set -C; umask 077; printf 'started\n' >"$S/start" ) 2>/dev/null; then
  echo "[exact-resource] this command already started; it is not run again" >&2
  exit __UNRESOLVED_CODE__
fi
trap '' HUP
journal() { printf '%s\n' "$1" >"$S/exit.tmp" && mv "$S/exit.tmp" "$S/exit"; }
if ! ( umask 077; : >"$S/stdout" && : >"$S/stderr" ); then journal 125; exit 125; fi
exec 3<&0
(
  setsid "$@" <&3 3<&- >"$S/stdout" 2>"$S/stderr"
  journal "$?"
) &
exec 3<&-
trap '' PIPE
O=0
drain() {
  N=$(wc -c <"$S/stderr" 2>/dev/null) || return 0
  N=${N##* }
  case "$N" in ''|*[!0-9]*) return 0;; esac
  [ "$N" -gt "$O" ] || return 0
  tail -c "+$((O + 1))" "$S/stderr" 2>/dev/null | head -c "$((N - O))" >&2
  O=$N
}
while [ ! -f "$S/exit" ]; do
  drain
  sleep 0.2 2>/dev/null || sleep 1
done
wait
drain
cat "$S/stdout" 2>/dev/null
IFS= read -r E <"$S/exit" || exit __UNRESOLVED_CODE__
case "$E" in ''|*[!0-9]*) exit __UNRESOLVED_CODE__;; esac
exit "$E"`

// exactResourceStateDir is where an exact resource command journals. It is a
// function of the execution identity alone.
func exactResourceStateDir(identity executioncontrol.Identity) string {
	return resourceStateDirPrefix + sanitizeForPath(string(identity.ExecutionID)) + "-" +
		strconv.FormatUint(uint64(identity.Fence), 10)
}

// journaledResourceCommand is cancellableResourceCommand under an outer
// session that journals the start and the exit in state. The cancellation
// marker, PID record and their Dekker ordering are the inner script's,
// unchanged.
func journaledResourceCommand(command []string, state string) []string {
	script := strings.ReplaceAll(journaledResourceSessionScript, "__UNRESOLVED_CODE__",
		strconv.Itoa(ExactUnresolvedExitCode))
	wrapped := []string{"sh", "-c", script, "resource-session", state,
		"sh", "-c", resourceCommandScript, "resource-command", state}
	return append(wrapped, command...)
}

func (p *execProcess) cancelResourceCommand(ctx context.Context, state string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	command := []string{"sh", "-c", cancelResourceScript, "cancel-resource", state}
	if p.control != nil {
		// An exact command's stop also closes a start its delivery has not
		// claimed yet, so the journal it names gets an outcome writer.
		command = exactResourceStopCommand(state)
	}
	var stderr bytes.Buffer
	if err := p.executor.ExecInPod(ctx, p.config.Namespace, p.podName, mainContainerName,
		command, nil, io.Discard, &stderr, false, ExecAttrs{Purpose: "cancel-resource"}); err != nil {
		return fmt.Errorf("stop resource command: %w: %s", err, stderr.String())
	}
	return nil
}
