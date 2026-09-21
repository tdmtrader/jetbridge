package jetbridge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"

	"github.com/concourse/concourse/hangar/executioncontrol"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	supervisorIdentityPrefix = "supervisor-v1:"
	resourceIdentityPrefix   = "resource-v1:"
)

// A Run's signed start retains the journal locator -- the task supervisor's,
// or an exact resource command's -- so cancellation can interrupt the command
// and recover its journal without reconstructing a command or reading secrets
// from the build plan. Other exact executions retain their existing opaque
// identity.
func (p *execProcess) exactProcessIdentity() executioncontrol.ProcessIdentity {
	if p.container != nil && p.container.recordWitness != nil {
		if state, resource, ok := p.exactJournal(); ok {
			if resource {
				return executioncontrol.ProcessIdentity(resourceIdentityPrefix + state)
			}
			return executioncontrol.ProcessIdentity(supervisorIdentityPrefix + state)
		}
	}
	return executioncontrol.ProcessIdentity(p.id)
}

// exactJournal is where this exact execution's in-pod journal lives, and
// whether it is a resource command's rather than the task supervisor's. Only
// those two write one; any other command has no outcome writer in the Pod.
func (p *execProcess) exactJournal() (string, bool, bool) {
	switch {
	case p.supervised():
		_, state := supervisorCommandParts(p.id, p.processSpec)
		return state, false, true
	case p.control != nil && p.resourceCommand():
		return exactResourceStateDir(p.control.Identity), true, true
	default:
		return "", false, false
	}
}

// executionJournalFromIdentity parses a retained journal locator. It accepts
// only a clean, single path segment under /tmp with the prefix its kind uses,
// so a signed start can never direct a stop or a read anywhere else.
func executionJournalFromIdentity(identity executioncontrol.ProcessIdentity) (string, bool, error) {
	state, resource, prefix := "", false, ""
	if located, ok := strings.CutPrefix(string(identity), supervisorIdentityPrefix); ok {
		state, prefix = located, taskStateDirPrefix
	} else if located, ok := strings.CutPrefix(string(identity), resourceIdentityPrefix); ok {
		state, resource, prefix = located, true, resourceStateDirPrefix
	} else {
		return "", false, fmt.Errorf("no retained journal locator")
	}
	if path.Dir(state) != "/tmp" || path.Clean(state) != state || !strings.HasPrefix(state, prefix) {
		return "", false, fmt.Errorf("no retained journal locator")
	}
	base := path.Base(state)
	if sanitizeForPath(base) != base || len(base) > 255 {
		return "", false, fmt.Errorf("invalid journal locator")
	}
	return state, resource, nil
}

// recoverJournaledOutcome reads the existing journal -- the supervisor's or
// the resource session's -- from the exact original Pod. It never attaches by
// rerunning the launch script. A missing, incomplete or unreachable journal
// leaves the execution unresolved.
func (p *execProcess) recoverJournaledOutcome(ctx context.Context) (executioncontrol.ExitOutcome, executioncontrol.Acknowledgement, error) {
	state, resource, ok := p.exactJournal()
	if !ok {
		return executioncontrol.ExitOutcome{}, executioncontrol.Acknowledgement{}, fmt.Errorf("this command has no in-pod journal")
	}
	// The caller has classified this identity as executing, so RecordStart is
	// an idempotent read of its original start, not permission for a first start.
	// It refuses a different process or Pod and returns the node's original UID.
	start, err := p.exact.client.RecordStart(ctx, p.control.Identity, p.exact.podUID, p.exactProcessIdentity())
	if err != nil {
		return executioncontrol.ExitOutcome{}, executioncontrol.Acknowledgement{}, err
	}
	if err = start.Validate(); err != nil {
		return executioncontrol.ExitOutcome{}, executioncontrol.Acknowledgement{}, err
	}
	if start.Kind != executioncontrol.AcknowledgementStart || start.Identity != p.control.Identity || start.ActivationEpoch != p.control.ActivationEpoch || start.PodUID != p.exact.podUID || start.ProcessIdentity != p.exactProcessIdentity() {
		return executioncontrol.ExitOutcome{}, executioncontrol.Acknowledgement{}, fmt.Errorf("retained start does not match the exact process")
	}
	// A start the Run never retained is retained now, while the node still
	// answers for it. No outcome may be recorded before it: once one exists
	// the node refuses the start, and the Run could never close the execution.
	p.exact.unretainedStart = &start
	if err = p.retainStartWitness(ctx); err != nil {
		return executioncontrol.ExitOutcome{}, executioncontrol.Acknowledgement{}, err
	}
	// A start no delivery claimed has no other outcome writer. Closing it is
	// a no-op for a command that did claim it, running or not.
	if err = closeUndeliveredStart(ctx, p.clientset, p.executor, p.config.Namespace, p.podName, p.exact.nodeName, state, resource, start); err != nil {
		return executioncontrol.ExitOutcome{}, executioncontrol.Acknowledgement{}, err
	}
	outcome, err := readJournaledOutcome(ctx, p.clientset, p.executor, p.config.Namespace, p.podName, p.exact.nodeName, state, start)
	return outcome, start, err
}

func readJournaledOutcome(ctx context.Context, client kubernetes.Interface, executor PodExecutor, namespace, podName, nodeName, state string, start executioncontrol.Acknowledgement) (executioncontrol.ExitOutcome, error) {
	if executor == nil {
		return executioncontrol.ExitOutcome{}, fmt.Errorf("no journal reader configured")
	}
	if err := checkSupervisorPod(ctx, client, namespace, podName, nodeName, start); err != nil {
		return executioncontrol.ExitOutcome{}, err
	}
	var stdout bytes.Buffer
	script := strings.ReplaceAll(exactOutcomeReadScript, "__STATE_DIR__", shellQuote(state))
	script = strings.ReplaceAll(script, "__UNRESOLVED_CODE__", strconv.Itoa(ExactUnresolvedExitCode))
	if err := executor.ExecInPod(ctx, namespace, podName, mainContainerName, []string{"sh", "-c", script}, nil, &stdout, nil, false, ExecAttrs{Purpose: "exact-outcome-recovery"}); err != nil {
		return executioncontrol.ExitOutcome{}, err
	}
	// Exec addresses a Pod by name. A replacement during the read must not
	// turn its files into an outcome for the old Pod's execution identity.
	if err := checkSupervisorPod(ctx, client, namespace, podName, nodeName, start); err != nil {
		return executioncontrol.ExitOutcome{}, err
	}
	text := strings.TrimSpace(stdout.String())
	code, err := strconv.Atoi(text)
	if err != nil || len(text) > 3 || code < 0 || code > 255 {
		return executioncontrol.ExitOutcome{}, fmt.Errorf("retained journal exit is invalid")
	}
	return executioncontrol.ExitOutcome{ExitCode: code}, nil
}

func checkSupervisorPod(ctx context.Context, client kubernetes.Interface, namespace, podName, nodeName string, start executioncontrol.Acknowledgement) error {
	pod, err := client.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if string(pod.UID) != string(start.PodUID) || pod.Spec.NodeName != nodeName {
		return fmt.Errorf("supervisor Pod was replaced")
	}
	node, err := client.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if string(node.UID) != string(start.NodeUID) {
		return fmt.Errorf("supervisor node was replaced")
	}
	return nil
}

// Bound the response before it enters the controller. A reader cannot recreate
// a missing start or exit journal; only the original supervisor writes those.
const exactOutcomeReadScript = `S=__STATE_DIR__
[ -f "$S/start" ] && [ -f "$S/exit" ] || exit __UNRESOLVED_CODE__
IFS= read -r E < "$S/exit" || exit __UNRESOLVED_CODE__
case "$E" in ''|*[!0-9]*) exit __UNRESOLVED_CODE__;; esac
[ "${#E}" -le 3 ] || exit __UNRESOLVED_CODE__
printf '%s\n' "$E"`

// An undelivered start is a signed start whose command never claimed the
// journal: the exec dial failed after the node recorded it, or the node's
// answer was lost and the command was never sent. Nothing in the Pod would
// ever write its exit, the read above stays unresolved for good, and a Run
// cannot finish a build with an execution it cannot close.
//
// So whoever finds one closes it, in the Pod, by claiming the start journal
// with the same O_EXCL creation the wrapper uses and writing the exit a
// command stopped before its producer ran would have written: 143 for a task
// (the supervisor's own stopped exit), 130 for a resource command (its
// cancellation marker's). A delivery that claimed the start first owns the
// journal and this writes nothing; a delivery that arrives after finds the
// start claimed and never runs its producer. The ATC still writes no outcome
// of its own: the ledger only ever records what this in-pod journal says.
const (
	exactTaskStoppedExit     = 143
	exactResourceStoppedExit = 130
)

const exactCloseUndeliveredFragment = `if ( set -C; umask 077; printf 'started\n' >"$S/start" ) 2>/dev/null; then
  printf '%s\n' __STOPPED_CODE__ >"$S/exit.tmp" && mv "$S/exit.tmp" "$S/exit" || exit 125
fi
`

// The journaled exit, for a delivery that is itself a stop: its exit status is
// the journal's, or unresolved when the journal's owner has not written one.
const exactReportJournalFragment = `[ -f "$S/exit" ] || exit __UNRESOLVED_CODE__
IFS= read -r E < "$S/exit" || exit __UNRESOLVED_CODE__
case "$E" in ''|*[!0-9]*) exit __UNRESOLVED_CODE__;; esac
exit "$E"`

// exactJournalCommand runs body with $S naming the journal, in each wrapper's
// own spelling: a task's state is assigned in the script's first line, as the
// supervisor and the outcome read assign it; a resource session's is its first
// argument, as its wrapper and cancel script take it.
func exactJournalCommand(state string, resource bool, name, body string) []string {
	if resource {
		return []string{"sh", "-c", "S=$1\n" + body, name, state}
	}
	return []string{"sh", "-c", "S=" + shellQuote(state) + "\n" + body}
}

// exactCloseBody closes an undelivered start and stops nothing: it is safe
// against a running command, whose start is already claimed.
func exactCloseBody(resource bool) string {
	code := exactTaskStoppedExit
	if resource {
		code = exactResourceStoppedExit
	}
	return `( umask 077; mkdir -p "$S" ) || exit 125
` + strings.ReplaceAll(exactCloseUndeliveredFragment, "__STOPPED_CODE__", strconv.Itoa(code))
}

// exactStopBody requests the stop FIRST -- the supervisor's stop marker, or
// the resource command's cancellation marker and group kill -- and then closes
// a start no delivery has claimed. A delivery that claimed it is stopped by the
// marker; one that has not is fenced by the claim. It leaves the stop request's
// own status in $status and does not exit.
func exactStopBody(resource bool) string {
	if resource {
		return `(
` + cancelResourceScript + `
)
status=$?
` + exactCloseBody(true)
	}
	return `mkdir -p "$S" || exit 125
printf '%s\n' requested > "$S/stop" || exit 125
status=0
` + exactCloseBody(false)
}

func exactCloseCommand(state string, resource bool) []string {
	return exactJournalCommand(state, resource, "exact-close", exactCloseBody(resource))
}

func exactResourceStopCommand(state string) []string {
	return exactJournalCommand(state, true, "cancel-resource", exactStopBody(true)+`exit "$status"`)
}

func exactTaskStopCommand(state string) []string {
	return exactJournalCommand(state, false, "exact-stop", exactStopBody(false)+`exit "$status"`)
}

// exactStoppedDelivery is the command delivered in place of the wrapper when
// the stop must precede it and may not be a separate request: the stop, the
// closing of the start, and the journal's exit, in one exec.
func exactStoppedDelivery(state string, resource bool) []string {
	return exactJournalCommand(state, resource, "exact-stopped-delivery", exactStopBody(resource)+
		strings.ReplaceAll(exactReportJournalFragment, "__UNRESOLVED_CODE__", strconv.Itoa(ExactUnresolvedExitCode)))
}

// closeUndeliveredStart runs the close against the exact original Pod.
func closeUndeliveredStart(ctx context.Context, client kubernetes.Interface, executor PodExecutor, namespace, podName, nodeName, state string, resource bool, start executioncontrol.Acknowledgement) error {
	if executor == nil {
		return fmt.Errorf("no journal writer configured")
	}
	if err := checkSupervisorPod(ctx, client, namespace, podName, nodeName, start); err != nil {
		return err
	}
	var stderr strings.Builder
	if err := executor.ExecInPod(ctx, namespace, podName, mainContainerName, exactCloseCommand(state, resource),
		nil, io.Discard, &stderr, false, ExecAttrs{Purpose: "exact-close-undelivered"}); err != nil {
		return fmt.Errorf("closing an undelivered start: %w: %s", err, stderr.String())
	}
	return checkSupervisorPod(ctx, client, namespace, podName, nodeName, start)
}

// exactStdoutLimit bounds a resource command's answer read back from its
// journal. The resource protocol's answer is a version and its metadata; the
// bound keeps a runaway file out of the controller.
const exactStdoutLimit = 1 << 20

// exactStdoutTooLarge is the read script's refusal of a journaled answer over
// the bound.
const exactStdoutTooLarge = 3

// The stdout is final once the exit exists: the runner journals the exit only
// after the command has exited.
const exactStdoutReadScript = `S=$1
[ -f "$S/exit" ] && [ -f "$S/stdout" ] || exit __UNRESOLVED_CODE__
N=$(wc -c <"$S/stdout") || exit __UNRESOLVED_CODE__
N=${N##* }
case "$N" in ''|*[!0-9]*) exit __UNRESOLVED_CODE__;; esac
[ "$N" -le __LIMIT__ ] || exit __TOO_LARGE__
exec cat "$S/stdout"`

// readJournaledStdout reads a resource command's journaled stdout from the
// exact original Pod. It is read only for an outcome the node already holds.
func readJournaledStdout(ctx context.Context, client kubernetes.Interface, executor PodExecutor, namespace, podName, nodeName, state string, start executioncontrol.Acknowledgement) ([]byte, error) {
	if executor == nil {
		return nil, fmt.Errorf("no journal reader configured")
	}
	if err := checkSupervisorPod(ctx, client, namespace, podName, nodeName, start); err != nil {
		return nil, err
	}
	script := strings.ReplaceAll(exactStdoutReadScript, "__UNRESOLVED_CODE__", strconv.Itoa(ExactUnresolvedExitCode))
	script = strings.ReplaceAll(script, "__LIMIT__", strconv.Itoa(exactStdoutLimit))
	script = strings.ReplaceAll(script, "__TOO_LARGE__", strconv.Itoa(exactStdoutTooLarge))
	answer := &boundedBuffer{limit: exactStdoutLimit}
	err := executor.ExecInPod(ctx, namespace, podName, mainContainerName,
		[]string{"sh", "-c", script, "exact-stdout", state}, nil, answer, io.Discard, false,
		ExecAttrs{Purpose: "exact-stdout-recovery"})
	var exited *ExecExitError
	if errors.As(err, &exited) && exited.ExitCode == exactStdoutTooLarge || answer.over {
		return nil, fmt.Errorf("the journaled resource answer exceeds %d bytes", exactStdoutLimit)
	}
	if err != nil {
		return nil, err
	}
	if err := checkSupervisorPod(ctx, client, namespace, podName, nodeName, start); err != nil {
		return nil, err
	}
	return answer.buf.Bytes(), nil
}

// boundedBuffer keeps at most limit bytes and remembers that it was offered
// more.
type boundedBuffer struct {
	buf   bytes.Buffer
	limit int
	over  bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.buf.Len()+len(p) > b.limit {
		b.over = true
		return 0, fmt.Errorf("more than %d bytes", b.limit)
	}
	return b.buf.Write(p)
}

// exposeJournaledAnswer hands a Run-owned resource command's journaled stdout
// to the step, for an outcome recovered rather than carried by the transport.
// Only a successful command's answer is read: that is the only one the
// resource protocol parses.
//
// witness is any signed node fact about this execution -- its start or its
// outcome -- and names the Pod and node the journal must be read from.
func (p *execProcess) exposeJournaledAnswer(ctx context.Context, witness executioncontrol.Acknowledgement, exitCode int) error {
	state, resource, journaled := p.exactJournal()
	if !journaled || !resource || exitCode != 0 || p.processIO.Stdout == nil {
		return nil
	}
	answer, err := readJournaledStdout(ctx, p.clientset, p.executor, p.config.Namespace, p.podName, p.exact.nodeName, state, witness)
	if err != nil {
		return fmt.Errorf("recovering the resource command's journaled answer: %w", err)
	}
	_, err = p.processIO.Stdout.Write(answer)
	return err
}
