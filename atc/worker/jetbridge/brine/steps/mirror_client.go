package steps

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"syscall"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/worker/jetbridge"
)

type MirrorClientCall struct {
	Mode string
	Key  string
	IP   string
	Err  error

	client      *jetbridge.DaemonClient
	observation *daemonWireObservation
	cancelled   bool
}

func mirrorClientGiven(mode string, rec *brine.Recorder, resources brine.Resources) (MirrorClientCall, error) {
	switch mode {
	case "accepted", "rejected", "stopped", "empty address", "cancelled":
	default:
		return MirrorClientCall{}, fmt.Errorf("unknown mirror condition %q", mode)
	}
	cluster, ok := resources.Get("real-cluster").(*realCluster)
	if !ok {
		return MirrorClientCall{}, fmt.Errorf("missing actual Kubernetes client")
	}
	daemon, err := startRealDaemon()
	if err != nil {
		return MirrorClientCall{}, err
	}
	rec.RegisterDisposer(func() { _ = daemon.stop() })
	u, err := url.Parse(daemon.URL)
	if err != nil {
		return MirrorClientCall{}, err
	}
	host, portText, err := net.SplitHostPort(u.Host)
	if err != nil {
		return MirrorClientCall{}, err
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return MirrorClientCall{}, err
	}
	out := MirrorClientCall{Mode: mode, Key: "handle/output", IP: host}
	status := http.StatusAccepted
	if mode == "rejected" {
		out.Key = "../outside"
		status = http.StatusBadRequest
	}
	body, err := json.Marshal(map[string]string{"key": out.Key})
	if err != nil {
		return MirrorClientCall{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Prove the daemon's real acceptance/refusal before applying the transport
	// or cancellation condition. Setup traffic is not part of the observation.
	if _, err := readDaemonHTTP(ctx, http.DefaultClient, http.MethodPost, daemon.URL+"/mirror", bytes.NewReader(body), status); err != nil {
		return MirrorClientCall{}, fmt.Errorf("mirror preflight: %w", err)
	}
	if mode == "stopped" || mode == "empty address" {
		if err := daemon.stop(); err != nil {
			return MirrorClientCall{}, err
		}
		conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", u.Host)
		if conn != nil {
			conn.Close()
		}
		if !errors.Is(err, syscall.ECONNREFUSED) {
			return MirrorClientCall{}, fmt.Errorf("stopped daemon must refuse TCP connections, got %v", err)
		}
	}
	if mode == "empty address" {
		out.IP = ""
	}
	out.observation, err = observeDaemonConstruction(map[string]bool{u.Host: true}, func() {
		out.client = jetbridge.NewDaemonClient(lagertest.NewTestLogger("brine-mirror-client"),
			cluster.Clientset, "default", "artifact-daemon", port, nil)
	})
	return out, err
}

func MirrorClientDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, MirrorClientCall](
			"a real mirror endpoint with condition {string}", []string{"real-cluster"},
			func(_ brine.Empty, params brine.Params, rec *brine.Recorder, resources brine.Resources) (MirrorClientCall, error) {
				mode, ok := params.GetString(0)
				if !ok {
					return MirrorClientCall{}, fmt.Errorf("missing mirror condition")
				}
				return mirrorClientGiven(mode, rec, resources)
			},
		),
		Transform[MirrorClientCall, MirrorClientCall]("the ATC requests the best-effort mirror",
			func(in MirrorClientCall, _ Args) (MirrorClientCall, error) {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if in.Mode == "cancelled" {
					cancel()
					in.cancelled = errors.Is(ctx.Err(), context.Canceled)
				}
				in.Err = in.client.TriggerMirror(ctx, in.IP, in.Key)
				return in, nil
			}),
		CheckThat[MirrorClientCall]("the client reports no error and sends only the expected mirror request",
			func(in MirrorClientCall) error {
				if in.Err != nil {
					return fmt.Errorf("best-effort mirror returned an error: %w", in.Err)
				}
				if in.Mode == "cancelled" && !in.cancelled {
					return fmt.Errorf("mirror context was not cancelled before the call")
				}
				requests, err := in.observation.capturedRequests()
				if err != nil {
					return err
				}
				if in.Mode != "accepted" && in.Mode != "rejected" {
					if len(requests) != 0 {
						return fmt.Errorf("%s mirror sent %d requests, want none", in.Mode, len(requests))
					}
					return nil
				}
				if len(requests) != 1 {
					return fmt.Errorf("mirror sent %d requests, want exactly one", len(requests))
				}
				request := requests[0]
				if request.Method != http.MethodPost || request.Path != "/mirror" {
					return fmt.Errorf("mirror sent %s %s, want POST /mirror", request.Method, request.Path)
				}
				body, err := json.Marshal(map[string]string{"key": in.Key})
				if err != nil {
					return err
				}
				if !bytes.Equal(request.Body, body) && !bytes.Equal(request.Body, append(body, '\n')) {
					return fmt.Errorf("mirror sent body %q, want %q", request.Body, body)
				}
				return nil
			}),
	}
}
