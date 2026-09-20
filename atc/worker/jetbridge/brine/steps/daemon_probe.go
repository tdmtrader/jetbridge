package steps

import (
	"fmt"
	"net"
	"strconv"

	"github.com/concourse/concourse/atc/worker/jetbridge"
	"k8s.io/client-go/kubernetes"
)

// Observe only the production probe client, never setup or readback traffic.
// The sockets and daemon responses are real; a miss must not trigger a resolve
// request that can materialize a peer's bytes and misidentify their location.
type daemonProbeObservation struct {
	wire     *daemonWireObservation
	path     string
	expected map[string]bool
}

func (p DaemonPlan) probeClient(cs kubernetes.Interface, path string) (*jetbridge.DaemonClient, *daemonProbeObservation, error) {
	observed := &daemonProbeObservation{path: path, expected: map[string]bool{}}
	addresses := map[string]bool{}
	for _, ip := range p.IPs {
		address := net.JoinHostPort(ip, strconv.Itoa(p.Port))
		addresses[address] = true
		observed.expected[address] = true
	}
	var client *jetbridge.DaemonClient
	var err error
	observed.wire, err = observeDaemonConstruction(addresses, func() {
		client = p.daemonClient(cs)
	})
	return client, observed, err
}

// These callers either probe one holder or wait for every miss. Empty
// discovery deliberately expects zero requests. Repeated addresses may be
// deduplicated: the original regression requires a miss, not duplicate I/O.
func (o *daemonProbeObservation) requireRequests() error {
	if o == nil || o.wire == nil {
		return fmt.Errorf("probe has no real-request observation")
	}
	got, err := o.wire.requests()
	if err != nil {
		return err
	}
	if len(got) != len(o.expected) {
		return fmt.Errorf("probe contacted %d daemon addresses, want %d: %v", len(got), len(o.expected), got)
	}
	for address := range o.expected {
		if len(got[address]) == 0 {
			return fmt.Errorf("probe sent no request to discovered daemon %s", address)
		}
		for _, request := range got[address] {
			if want := "HEAD " + o.path; request != want {
				return fmt.Errorf("probe sent %q, want %q", request, want)
			}
		}
	}
	return nil
}
