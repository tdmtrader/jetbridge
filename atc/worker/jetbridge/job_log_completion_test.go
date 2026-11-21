package jetbridge

// On the job step runtime a step's output is main's container log, followed
// while main runs. After main ends the follow gets five seconds, then is
// cut. What the follow did not deliver -- it opened and returned nothing, it
// returned a prefix, it was cut by that cancel -- is appended from one plain
// read of the finished log, exactly once: never dropped, never duplicated.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/kubernetes/scheme"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	restfake "k8s.io/client-go/rest/fake"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
)

// logServe says how the kubelet answers main's log: the follow and the plain
// read separately. A nil body is an error response.
type logServe struct {
	// follow is what a follow delivers; hang keeps it open after that until
	// its request is cancelled, as a follow the kubelet never ends does.
	follow *string
	hang   bool
	plain  *string
	// plainHang keeps the plain read open after its body, as a kubelet that
	// stops answering does.
	plainHang bool
}

func str(s string) *string { return &s }

// splitLogsClientset serves main's log by logServe, every other request by
// the fake clientset.
type splitLogsClientset struct {
	*fake.Clientset
	serve logServe
}

func (c splitLogsClientset) CoreV1() typedcorev1.CoreV1Interface {
	return splitLogsCoreV1{CoreV1Interface: c.Clientset.CoreV1(), serve: c.serve}
}

type splitLogsCoreV1 struct {
	typedcorev1.CoreV1Interface
	serve logServe
}

func (c splitLogsCoreV1) Pods(namespace string) typedcorev1.PodInterface {
	return splitLogsPods{PodInterface: c.CoreV1Interface.Pods(namespace), serve: c.serve}
}

type splitLogsPods struct {
	typedcorev1.PodInterface
	serve logServe
}

func (p splitLogsPods) GetLogs(name string, opts *corev1.PodLogOptions) *rest.Request {
	body, hang := p.serve.plain, p.serve.plainHang
	if opts.Follow {
		body, hang = p.serve.follow, p.serve.hang
	}
	client := &restfake.RESTClient{
		NegotiatedSerializer: scheme.Codecs.WithoutConversion(),
		Client: restfake.CreateHTTPClient(func(req *http.Request) (*http.Response, error) {
			if body == nil {
				return &http.Response{StatusCode: http.StatusInternalServerError, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("kubelet says no"))}, nil
			}
			var reader io.ReadCloser = io.NopCloser(strings.NewReader(*body))
			if hang {
				reader = &followingBody{body: strings.NewReader(*body), done: req.Context().Done()}
			}
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/plain"}}, Body: reader}, nil
		}),
	}
	return client.Get().Resource("pods").Name(name).SubResource("log")
}

// waitJobTask runs a task on the job runtime whose main has already ended
// with exit, its log served by serve, and returns what the step was handed.
func waitJobTask(t *testing.T, serve logServe, exit int32) (runtime.ProcessResult, string, string) {
	t.Helper()
	return waitJobStep(t, db.ContainerTypeTask, serve, exit)
}

// waitJobStep is waitJobTask for a step of any kind.
func waitJobStep(t *testing.T, kind db.ContainerType, serve logServe, exit int32) (runtime.ProcessResult, string, string) {
	t.Helper()
	clientset := fake.NewSimpleClientset()
	jobControllerDouble(clientset, func(job *batchv1.Job) *corev1.Pod {
		return terminatedJobPod(job.Name+"-abcde", exit)
	})
	spec := runtime.ContainerSpec{Dir: "/tmp/build/task", Type: kind}
	spec.ImageSpec.ImageURL = "docker:///busybox"
	metadata := db.ContainerMetadata{Type: kind, PipelineName: "p", JobName: "j", BuildName: "1", StepName: "s"}
	container := newContainer("step-handle", metadata, spec, nil, splitLogsClientset{Clientset: clientset, serve: serve}, jobRuntimeConfig(), "worker",
		nil, nil, nil, stepPodIdentities{}, false, false)
	container.envelopes = NewStepEnvelopes()

	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	process, err := container.Run(context.Background(), runtime.ProcessSpec{Path: "sh"}, runtime.ProcessIO{Stdout: stdout, Stderr: stderr})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := process.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	return result, stdout.String(), stderr.String()
}

const finishedLog = "line one\nline two\nline three\n"

func TestJobRuntimeLogIsCompletedFromTheFinishedLog(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		serve      logServe
		wantStdout string
		wantWarn   string
	}{
		{name: "a follow that opened and delivered nothing",
			serve: logServe{follow: str(""), plain: str(finishedLog)}, wantStdout: finishedLog},
		{name: "a follow that delivered a prefix",
			serve: logServe{follow: str("line one\nline t"), plain: str(finishedLog)}, wantStdout: finishedLog},
		{name: "a follow that delivered everything",
			serve: logServe{follow: str(finishedLog), plain: str(finishedLog)}, wantStdout: finishedLog},
		{name: "a follow cut by the drain's cancel after a prefix",
			serve: logServe{follow: str("line one\n"), hang: true, plain: str(finishedLog)}, wantStdout: finishedLog},
		{name: "a follow that never opened",
			serve: logServe{follow: nil, plain: str(finishedLog)}, wantStdout: finishedLog},
		{name: "a finished log shorter than what a follow that ended delivered",
			serve: logServe{follow: str(finishedLog), plain: str("line three\n")}, wantStdout: finishedLog},
		{name: "a finished log shorter than what a cut follow delivered",
			serve:      logServe{follow: str("line one\n"), hang: true, plain: str("x")},
			wantStdout: "line one\n", wantWarn: "the end of the output is not shown"},
		{name: "a finished log that no longer starts where the follow did",
			serve:      logServe{follow: str("line one\n"), plain: str("rotated away, then more\n")},
			wantStdout: "line one\n", wantWarn: "no longer matches what was streamed"},
		{name: "a plain read that fails after a follow delivered",
			serve:      logServe{follow: str("line one\n"), plain: nil},
			wantStdout: "line one\n", wantWarn: "could not be re-read to confirm the output above is complete"},
		{name: "a plain read that fails after a follow delivered nothing",
			serve:      logServe{follow: str(""), plain: nil},
			wantStdout: "", wantWarn: "the command's log could not be read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result, stdout, stderr := waitJobTask(t, tc.serve, 0)
			if result.ExitStatus != 0 {
				t.Errorf("exit = %d, want main's 0", result.ExitStatus)
			}
			if stdout != tc.wantStdout {
				t.Errorf("stdout = %q, want %q", stdout, tc.wantStdout)
			}
			if tc.wantWarn == "" && stderr != "" {
				t.Errorf("stderr = %q, want nothing", stderr)
			}
			if tc.wantWarn != "" && !strings.Contains(stderr, tc.wantWarn) {
				t.Errorf("stderr = %q, want it to say %q", stderr, tc.wantWarn)
			}
		})
	}
}

// A log larger than the tail kept to check the offset is completed exactly.
func TestJobRuntimeCompletesALogLongerThanTheCheckedTail(t *testing.T) {
	t.Parallel()
	var log strings.Builder
	for i := 0; log.Len() < 3*logFollowTail; i++ {
		log.WriteString(strings.Repeat(string(rune('a'+i%26)), 99) + "\n")
	}
	full := log.String()
	prefix := full[:2*logFollowTail+17]
	_, stdout, stderr := waitJobTask(t, logServe{follow: str(prefix), plain: str(full)}, 0)
	if stdout != full {
		t.Errorf("stdout is %d bytes, want the %d of the finished log (equal: %v)", len(stdout), len(full), stdout == full)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want nothing", stderr)
	}
}

// The exact task's drain completes the log the same way.
func TestExactJobDrainCompletesTheFinishedLog(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		serve      logServe
		wantStdout string
		wantWarn   string
	}{
		{name: "a follow that opened and delivered nothing",
			serve: logServe{follow: str(""), plain: str(finishedLog)}, wantStdout: finishedLog},
		{name: "a follow that delivered a prefix",
			serve: logServe{follow: str("line one\nli"), plain: str(finishedLog)}, wantStdout: finishedLog},
		{name: "a follow that delivered everything",
			serve: logServe{follow: str(finishedLog), plain: str(finishedLog)}, wantStdout: finishedLog},
		{name: "a finished log that no longer starts where the follow did",
			serve:      logServe{follow: str("line one\n"), plain: str("rotated away, then more\n")},
			wantStdout: "line one\n", wantWarn: "no longer matches what was streamed"},
		{name: "a plain read that fails",
			serve:      logServe{follow: str("line one\n"), plain: nil},
			wantStdout: "line one\n", wantWarn: "could not be re-read to confirm the output above is complete"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
			p := &jobProcess{
				clientset: splitLogsClientset{Clientset: fake.NewSimpleClientset(), serve: tc.serve},
				config:    jobRuntimeConfig(),
				processIO: runtime.ProcessIO{Stdout: stdout, Stderr: stderr},
			}
			logs, err := p.followExactLogs(context.Background(), "pod")
			if err != nil {
				t.Fatalf("followExactLogs: %v", err)
			}
			logs.drain()
			if stdout.String() != tc.wantStdout {
				t.Errorf("stdout = %q, want %q", stdout.String(), tc.wantStdout)
			}
			if tc.wantWarn == "" && stderr.String() != "" {
				t.Errorf("stderr = %q, want nothing", stderr.String())
			}
			if tc.wantWarn != "" && !strings.Contains(stderr.String(), tc.wantWarn) {
				t.Errorf("stderr = %q, want it to say %q", stderr.String(), tc.wantWarn)
			}
		})
	}
}

// A resource command's log is its stderr: the remainder goes there. A
// failing command reads no answer, so no artifact daemon is needed.
func TestJobRuntimeCompletesAResourceCommandsStderr(t *testing.T) {
	t.Parallel()
	result, stdout, stderr := waitJobStep(t, db.ContainerTypeCheck, logServe{follow: str("line one\n"), plain: str(finishedLog)}, 1)
	if result.ExitStatus != 1 {
		t.Errorf("exit = %d, want main's 1", result.ExitStatus)
	}
	if stderr != finishedLog {
		t.Errorf("stderr = %q, want %q", stderr, finishedLog)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing", stdout)
	}
}

// The kept tail is the last logFollowTail bytes delivered, whatever the
// sizes of the writes that delivered them.
func TestLogFollowKeepsTheLastBytesDelivered(t *testing.T) {
	t.Parallel()
	var all []byte
	for i := 0; len(all) < 3*logFollowTail; i++ {
		all = append(all, byte('a'+i%26))
	}
	for _, size := range []int{1, 7, logFollowTail - 1, logFollowTail, logFollowTail + 1, 3 * logFollowTail} {
		var follow logFollow
		for start := 0; start < len(all); start += size {
			follow.add(all[start:min(start+size, len(all))])
		}
		if follow.delivered != int64(len(all)) {
			t.Errorf("writes of %d: delivered = %d, want %d", size, follow.delivered, len(all))
		}
		if !bytes.Equal(follow.tail, all[len(all)-logFollowTail:]) {
			t.Errorf("writes of %d: the tail is not the last %d bytes delivered", size, logFollowTail)
		}
	}
	var short logFollow
	short.add([]byte("ab"))
	short.add([]byte("c"))
	if short.delivered != 3 || string(short.tail) != "abc" {
		t.Errorf("short follow = %d %q, want 3 \"abc\"", short.delivered, short.tail)
	}
}

// An aborted step does not wait on the read that completes its log, even
// when the kubelet stops answering it.
func TestCompletingALogEndsWithTheStep(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	clientset := splitLogsClientset{Clientset: fake.NewSimpleClientset(), serve: logServe{plain: str(""), plainHang: true}}
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		completeContainerLog(ctx, clientset, "test-ns", "pod", mainContainerName, stdout, stderr, logFollow{})
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("completing the log outlived the step's context")
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q after the step was aborted", stdout.String())
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Errorf("ctx = %v", ctx.Err())
	}
}
