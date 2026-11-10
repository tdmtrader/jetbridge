//go:build live
// +build live

package jetbridge

import (
	"context"
	"fmt"
	"sync"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// LiveNodeOutputControls is a real artifact daemon output plane per node of
// the cluster, run by the live test's own process, each with its node's UID.
// The deployed daemons' control plane takes only the web's client
// certificate, so a live test that admits and starts an exact execution
// brings its own ledgers: what the test is about is the step Job's pod on a
// real kubelet, and every ledger answer is still a real daemon's.
type LiveNodeOutputControls struct {
	mu      sync.Mutex
	daemons map[string]*outputDaemonHarness
}

var _ OutputControlResolver = (*LiveNodeOutputControls)(nil)

// StartLiveNodeOutputControls starts one daemon for every registered node.
func StartLiveNodeOutputControls(ctx context.Context, clientset kubernetes.Interface) (*LiveNodeOutputControls, error) {
	nodes, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing nodes: %w", err)
	}
	controls := &LiveNodeOutputControls{daemons: map[string]*outputDaemonHarness{}}
	for _, node := range nodes.Items {
		daemon, err := startOutputDaemonForNode(true, string(node.UID))
		if err != nil {
			controls.Stop()
			return nil, fmt.Errorf("starting the output plane for node %s: %w", node.Name, err)
		}
		controls.daemons[node.Name] = daemon
	}
	if len(controls.daemons) == 0 {
		return nil, fmt.Errorf("no nodes listed")
	}
	return controls, nil
}

func (controls *LiveNodeOutputControls) ForNode(_ context.Context, nodeName string) (OutputControl, error) {
	controls.mu.Lock()
	defer controls.mu.Unlock()
	daemon, ok := controls.daemons[nodeName]
	if !ok {
		return nil, fmt.Errorf("no output plane for node %s", nodeName)
	}
	return daemon.Client, nil
}

// Endpoint is one daemon's control endpoint, for the execution control
// envelope's own validation; a task that captures nothing never dials it.
func (controls *LiveNodeOutputControls) Endpoint() string {
	controls.mu.Lock()
	defer controls.mu.Unlock()
	for _, daemon := range controls.daemons {
		return daemon.Endpoint
	}
	return ""
}

func (controls *LiveNodeOutputControls) Stop() {
	controls.mu.Lock()
	defer controls.mu.Unlock()
	for _, daemon := range controls.daemons {
		daemon.Stop()
	}
}
