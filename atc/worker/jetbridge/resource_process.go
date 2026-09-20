package jetbridge

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"time"

	"github.com/concourse/concourse/atc/db"
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

func cancellableResourceCommand(command []string) ([]string, string) {
	state := "/tmp/concourse-resource-" + uuid.NewString()
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

func (p *execProcess) cancelResourceCommand(state string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var stderr bytes.Buffer
	if err := p.executor.ExecInPod(ctx, p.config.Namespace, p.podName, mainContainerName,
		[]string{"sh", "-c", cancelResourceScript, "cancel-resource", state},
		nil, io.Discard, &stderr, false, ExecAttrs{Purpose: "cancel-resource"}); err != nil {
		return fmt.Errorf("stop resource command: %w: %s", err, stderr.String())
	}
	return nil
}
