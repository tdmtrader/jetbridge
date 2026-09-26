---
status: accepted
date: 2026-09-25
---

# Supervised commands leave the exec session; the pod owns their lifetime

A step's command reaches its pod through an exec session the web holds. A
task's session has a TTY, so when that session ends -- its pty hung up, or its
leader killed -- the kernel sends SIGHUP to the terminal's foreground process
group. A web that dies mid-step leaves its session to end that way. (A clean
close of the exec stream alone did not end it on concourse.home when measured
on 2026-09-25; the guarantee below does not depend on which path ends it.) The supervisor exists so that a new web can
re-exec the same wrapper and take the command over: follow its log, wait for
its exit record, or report a finished one without running it again. That only
works if the command is still alive when the new web arrives.

Ignoring SIGHUP in the wrapper was not enough. A disposition is inherited, not
enforced: any program that installs its own SIGHUP handler hands its children
SIGHUP at the default action. dockerd does, for config reload. On
2026-09-25 three CI builds (k8s-e2e #291 and #294, k8s-behavioral #196) went
red at the second a self-upgrade restarted the web: the docker-proxy processes
dockerd starts to publish ports died with the exec session, while containerd,
which dockerd starts under setsid, survived.

So every supervised command runs in a session of its own (`setsid`), with
only the wrapper's runner -- plain sh that waits for the command and writes its
exit -- left in the exec session with SIGHUP ignored. The exact supervisor and
the resource session already did this; the ordinary supervisor now does too.
The exec session's end is never a signal to the command. What ends a command
early is the pod: an abort or timeout deletes the pause pod with no grace,
which reaches every session in it, and exact execution stops its command
through the exit journal's stop record and a kill of its process group.

## Consequences

- A web restart never kills a supervised command or its descendants, whatever
  they do with SIGHUP. Processes started with `nohup`, as daemons, or under
  their own setsid were already safe and still are.
- Nothing may rely on the exec stream closing to stop a supervised command.
  The only stops are pod deletion and, for exact execution, the stop record.
- The command has no controlling terminal. Its output already went to the
  supervisor's log, not the pty; a program that opens `/dev/tty` gets ENXIO.
- An ordinary task image without `setsid` (BusyBox and util-linux both have
  it) falls back to the SIGHUP shield alone and says so once in the step's
  log. Exact execution and resource steps require `setsid` outright.
- Ordinary check/get/put commands run in their own session too, but they speak
  the resource protocol over the exec stream and have no log to resume from,
  so a new web cannot take one over and runs the step again; only exact
  resource commands journal an outcome a new web can recover.
