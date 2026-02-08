package k8sruntime

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"github.com/concourse/concourse/tracing"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	utilexec "k8s.io/utils/exec"
)

// SPDYExecutor implements PodExecutor using the Kubernetes SPDY exec API
// (remotecommand). This is the production implementation used when running
// resource get/put/check steps that need stdin/stdout/stderr separation.
type SPDYExecutor struct {
	clientset  kubernetes.Interface
	restConfig *rest.Config
}

// NewSPDYExecutor creates a new SPDYExecutor backed by the given Kubernetes
// clientset and REST config.
func NewSPDYExecutor(clientset kubernetes.Interface, restConfig *rest.Config) *SPDYExecutor {
	return &SPDYExecutor{
		clientset:  clientset,
		restConfig: restConfig,
	}
}

func (e *SPDYExecutor) ExecInPod(
	ctx context.Context,
	namespace, podName, containerName string,
	command []string,
	stdin io.Reader,
	stdout, stderr io.Writer,
	tty bool,
) error {
	ctx, span := tracing.StartSpan(ctx, "k8s.spdy.exec", tracing.Attrs{
		"namespace":      namespace,
		"pod-name":       podName,
		"container-name": containerName,
		"tty":            fmt.Sprintf("%t", tty),
	})
	var spanErr error
	defer func() { tracing.End(span, spanErr) }()

	// K8s requires at least one of stdin/stdout/stderr to be enabled.
	// If none are provided, enable stdout with a discard writer.
	if stdin == nil && stdout == nil && stderr == nil {
		stdout = io.Discard
	}

	// When TTY is enabled, K8s combines stdout and stderr into a single
	// stream and allocates a pseudo-terminal for interactive sessions.
	if tty {
		stderr = nil
	}

	req := e.clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: containerName,
			Command:   command,
			Stdin:     stdin != nil,
			Stdout:    stdout != nil,
			Stderr:    stderr != nil,
			TTY:       tty,
		}, scheme.ParameterCodec)

	logger := lagerctx.FromContext(ctx).Session("exec-in-pod", lager.Data{
		"pod":       podName,
		"container": containerName,
	})

	exec, err := remotecommand.NewSPDYExecutor(e.restConfig, http.MethodPost, req.URL())
	if err != nil {
		logger.Error("failed-to-create-spdy-executor", err)
		spanErr = err
		return fmt.Errorf("create spdy executor: %w", err)
	}

	err = exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin:  stdin,
		Stdout: stdout,
		Stderr: stderr,
		Tty:    tty,
	})
	if err != nil {
		if exitErr, ok := err.(utilexec.ExitError); ok {
			return &ExecExitError{ExitCode: exitErr.ExitStatus()}
		}
		logger.Error("failed-to-exec-stream", err)
		spanErr = err
		return fmt.Errorf("exec stream: %w", err)
	}

	return nil
}
