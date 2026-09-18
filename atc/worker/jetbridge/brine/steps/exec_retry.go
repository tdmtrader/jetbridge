package steps

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"

	"code.cloudfoundry.org/clock"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/engine"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/policy"
	atcresource "github.com/concourse/concourse/atc/resource"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// Observe a real failed pod creation before aborting the build. Forward the
// response and error unchanged: this is a cancellation checkpoint, never a
// substitute API response, worker, process or step result.
type abortOnFailedPodRequest struct {
	next   http.RoundTripper
	cancel context.CancelFunc
}

func (t abortOnFailedPodRequest) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := t.next.RoundTrip(request)
	if request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/pods") && errors.Is(err, syscall.ECONNREFUSED) {
		t.cancel()
		fmt.Println("aborted build after actual pod-create connection refusal")
	}
	return response, err
}

// This route forwards unmodified TLS to envtest before it is closed.
// Connection refusal subsequently comes from the kernel, not a fake client.
func closedExecAPIRoute(ctx context.Context, rec *brine.Recorder, res brine.Resources) (*rest.Config, error) {
	cluster, ok := res.Get("real-cluster").(*realCluster)
	if !ok {
		return nil, fmt.Errorf("missing real Kubernetes API")
	}
	endpoint, err := url.Parse(cluster.RESTConfig.Host)
	if err != nil {
		return nil, err
	}
	target := endpoint.Host
	if endpoint.Port() == "" {
		target = net.JoinHostPort(endpoint.Hostname(), "443")
	}
	route, err := routeWithDrops("127.0.0.1:0", target, 0, false)
	if err != nil {
		return nil, err
	}
	rec.RegisterDisposer(func() {
		if err := route.Close(); err != nil {
			panic(err)
		}
		fmt.Printf("stopped retry API route %s\n", route.Addr())
	})
	config := rest.CopyConfig(cluster.RESTConfig)
	if config.ServerName == "" {
		config.ServerName = endpoint.Hostname()
	}
	endpoint.Host = route.Addr().String()
	config.Host = endpoint.String()
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	probe := func() error {
		return client.Discovery().RESTClient().Get().AbsPath("/version").Do(ctx).Error()
	}
	if err := probe(); err != nil {
		return nil, fmt.Errorf("real API route did not work before interruption: %w", err)
	}
	if err := route.Close(); err != nil {
		return nil, err
	}
	if err := probe(); !errors.Is(err, syscall.ECONNREFUSED) {
		return nil, fmt.Errorf("closed real API route did not refuse connection: %v", err)
	}
	fmt.Printf("real retry API route %s reached envtest then refused connections\n", config.Host)
	return config, nil
}

func (in ExecBuild) classifyRealFailure(rec *brine.Recorder, res brine.Resources) (ExecRun, error) {
	ctx, cancel := context.WithTimeout(in.core.Ctx, 20*time.Second)
	defer cancel()
	var routed *rest.Config
	plan := execPutPlan("classified", "unreachable")
	switch in.retryFailure {
	case "api":
		var err error
		routed, err = closedExecAPIRoute(ctx, rec, res)
		if err != nil {
			return ExecRun{}, err
		}
		if in.aborted {
			routed.Wrap(func(next http.RoundTripper) http.RoundTripper {
				return abortOnFailedPodRequest{next: next, cancel: in.core.Cancel}
			})
		}
		// A valid explicit image ensures the failure is an actual API call,
		// not image validation.
		plan.TypeImage = atc.TypeImage{ImageRef: "docker:///busybox:1.37.0"}
	case "input":
		plan.Inputs = &atc.InputsConfig{Specified: []string{"missing-artifact"}}
	default:
		return ExecRun{}, fmt.Errorf("no real retry failure was described")
	}
	pool, err := realExecPool(ctx, rec, in.core.DB, res, routed)
	if err != nil {
		return ExecRun{}, err
	}
	step := exec.NewPutStep("classified", plan, in.stepMetadata(),
		db.ContainerMetadata{WorkingDirectory: atcresource.ResourcesDir("put"), PipelineID: in.core.Pipeline.ID(), Type: db.ContainerTypePut, StepName: plan.Name},
		pool, execPutDelegates(func(state exec.RunState) exec.PutDelegate {
			return engine.NewPutDelegate(in.core.Build, "classified", state, clock.NewClock(), policy.NoopChecker{})
		}), 0)
	classified := exec.RetryError(step, execBuildStepDelegates(func(state exec.RunState) exec.BuildStepDelegate {
		return engine.NewBuildStepDelegate(in.core.Build, "classified", state, clock.NewClock(), policy.NoopChecker{}, false)
	}))
	ok, runErr := classified.Run(ctx, in.core.State)
	cause := runErr
	if retry, ok := runErr.(exec.Retriable); ok {
		cause = retry.Cause
	}
	if in.retryFailure == "api" {
		var requestError *url.Error
		if !errors.Is(cause, syscall.ECONNREFUSED) || !errors.As(cause, &requestError) {
			return ExecRun{}, fmt.Errorf("expected the real HTTP connection refusal, got %v", cause)
		}
		if in.aborted && !errors.Is(ctx.Err(), context.Canceled) {
			return ExecRun{}, fmt.Errorf("build was not aborted at its actual API failure")
		}
	} else {
		var missing exec.PutInputNotFoundError
		if !errors.As(cause, &missing) || missing.Input != "missing-artifact" {
			return ExecRun{}, fmt.Errorf("expected the real missing-input failure, got %v", cause)
		}
	}
	fmt.Printf("real retry step failure (%s, aborted=%t): %v\n", in.retryFailure, in.aborted, runErr)
	return ExecRun{core: in.core, Ok: ok, Err: runErr}, nil
}
