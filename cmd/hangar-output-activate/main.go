// Command hangar-output-activate moves the Hangar output plane's activation
// epoch through its four guarded transitions.
//
// It is an INTERNAL command, run as a one-shot Kubernetes Job under its own
// service account and its own least-privilege PostgreSQL role. There is
// deliberately no public HTTP activation endpoint and no receipt private key
// mounted into any activation Job: the epoch row is the plane's single
// authority, and an authority reachable over the API is one an exploit of the
// web node inherits.
//
// No single Helm boolean, no single invocation of this command and no node label
// enables either capability. `enable` refuses a facet that was not `attested`,
// `attest` refuses a mixed cohort, and output can never be enabled while base is
// not -- in the schema, not here.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/concourse/concourse/atc/hangaroutput/activation"
	"github.com/concourse/concourse/atc/hangaroutput/controller"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

func main() {
	config := Config{}
	BindFlags(flag.CommandLine, &config)
	flag.Parse()

	if err := run(context.Background(), config, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "hangar-output-activate: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, config Config, out *os.File) error {
	if err := config.Validate(); err != nil {
		return err
	}

	conn, err := controller.OpenDatabase(config.DSN, 2)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	epochs := activation.Epochs{DB: conn}
	epoch := executioncontrol.ActivationEpoch(config.Epoch)

	switch config.Mode {
	case ModeBegin:
		if err := epochs.Begin(ctx, epoch); err != nil {
			return err
		}
		fmt.Fprintf(out, "began activation epoch %d (base=initial output=initial)\n", epoch)

	case ModeAttest:
		evidence, err := attest(ctx, config, epoch)
		if err != nil {
			return err
		}
		if err := epochs.Attest(ctx, epoch, config.Facet, evidence); err != nil {
			return err
		}
		fmt.Fprintf(out, "attested epoch %d's %s facet over the cohort digest %s\n",
			epoch, config.Facet, evidence.CohortDigest)

	case ModeEnable:
		if err := epochs.Enable(ctx, epoch, config.Facet); err != nil {
			return err
		}
		fmt.Fprintf(out, "enabled epoch %d's %s facet\n", epoch, config.Facet)

	case ModeDrain:
		if err := drain(ctx, epochs, epoch, config, out); err != nil {
			return err
		}

	default:
		return fmt.Errorf("%w: --mode %q; there are four: begin, attest, enable, drain",
			output.ErrUnknownMember, config.Mode)
	}

	state, err := epochs.Read(ctx, epoch)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "epoch %d: base=%s output=%s revision=%d\n",
		state.Epoch, state.Base, state.Output, state.Revision)

	return nil
}

// drain stops new admission first, and only then asks whether anything is left.
//
// That ORDER is requirement 58: emission stops before the predicate runs, so the
// set the predicate is counting cannot grow while it counts. A drain that
// checked first and stopped emission afterwards would refuse on state that
// arrived in between and admit state that arrived after the check.
//
// `--facet=all` takes OUTPUT down first and base second. Base is what settles a
// capture, so a plane that lost exact execution control while an output capture
// was still nonterminal would have removed the only thing that could finish it.
func drain(ctx context.Context, epochs activation.Epochs,
	epoch executioncontrol.ActivationEpoch, config Config, out *os.File) error {
	facets := []activation.Facet{config.Facet}
	if config.All {
		facets = []activation.Facet{activation.FacetOutput, activation.FacetBase}
	}

	for _, facet := range facets {
		state, err := epochs.Read(ctx, epoch)
		if err != nil {
			return err
		}
		current := state.Base
		if facet == activation.FacetOutput {
			current = state.Output
		}

		if current == "enabled" {
			if err := epochs.Drain(ctx, epoch, facet); err != nil {
				return err
			}
			fmt.Fprintf(out, "epoch %d's %s facet is draining: no new admission, and every "+
				"release, settlement and already-admitted delete continues\n", epoch, facet)
			current = "draining"
		}
		if current != "draining" {
			fmt.Fprintf(out, "epoch %d's %s facet is %q; nothing to drain\n",
				epoch, facet, current)

			continue
		}

		residue, err := epochs.DrainResidue(ctx, epoch, facet)
		if err != nil {
			return err
		}
		if len(residue) != 0 {
			if !config.Finalize {
				fmt.Fprintf(out, "epoch %d's %s facet still holds state and stays draining:\n",
					epoch, facet)
				for _, one := range residue {
					fmt.Fprintf(out, "  - %s\n", one)
				}

				continue
			}

			return activation.RefuseDrain(epoch, facet, residue)
		}
		if !config.Finalize {
			fmt.Fprintf(out, "epoch %d's %s facet holds nothing; re-run with --finalize to "+
				"disable it\n", epoch, facet)

			continue
		}
		if err := epochs.Disable(ctx, epoch, facet); err != nil {
			return err
		}
		fmt.Fprintf(out, "epoch %d's %s facet is disabled\n", epoch, facet)
	}

	return nil
}

// attest builds the evidence from the live cohort.
func attest(ctx context.Context, config Config,
	epoch executioncontrol.ActivationEpoch) (activation.Evidence, error) {
	restConfig, err := rest.InClusterConfig()
	if err != nil {
		return activation.Evidence{}, fmt.Errorf("%w: attestation enumerates the daemon cohort "+
			"from the Kubernetes API rather than from node labels -- a label is a hint the "+
			"daemon itself wrote -- and this Job cannot reach the API: %v",
			output.ErrInfrastructure, err)
	}
	client, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return activation.Evidence{}, fmt.Errorf("%w: building the Kubernetes client: %v",
			output.ErrInfrastructure, err)
	}

	source := &podCohort{
		pods:      client,
		namespace: config.Namespace,
		selector:  config.DaemonSelector,
		port:      config.DaemonPort,
	}
	handshaker, err := newHandshaker(config)
	if err != nil {
		return activation.Evidence{}, err
	}

	if config.Facet == activation.FacetBase {
		return activation.AttestBase(ctx, source, handshaker, epoch)
	}

	evidence, err := activation.AttestOutput(ctx, source, handshaker, epoch)
	if err != nil {
		return activation.Evidence{}, err
	}
	evidence.ReceiptKeyValidFrom = time.Now().UTC()
	evidence.ReceiptKeyValidUntil = evidence.ReceiptKeyValidFrom.Add(config.ReceiptKeyLifetime)

	return evidence, nil
}

// podCohort enumerates the output daemon's pods from the Kubernetes API.
type podCohort struct {
	pods      kubernetes.Interface
	namespace string
	selector  string
	port      int
}

func (cohort *podCohort) Members(ctx context.Context) ([]activation.Member, error) {
	list, err := cohort.pods.CoreV1().Pods(cohort.namespace).List(ctx,
		metav1.ListOptions{LabelSelector: cohort.selector})
	if err != nil {
		return nil, fmt.Errorf("%w: listing output daemon pods in %s: %v",
			output.ErrInfrastructure, cohort.namespace, err)
	}

	var members []activation.Member
	for _, pod := range list.Items {
		// A pod with no IP has not been scheduled, and one that is not running
		// is not serving. Attesting over it would be attesting a handshake
		// nobody can make.
		if pod.Status.PodIP == "" || pod.Status.Phase != "Running" {
			continue
		}
		members = append(members, activation.Member{
			Node:    pod.Spec.NodeName,
			Address: fmt.Sprintf("%s:%d", pod.Status.PodIP, cohort.port),
		})
	}

	return members, nil
}

// httpHandshaker speaks the daemon's control API over the control plane's own
// client certificate.
type httpHandshaker struct {
	client *http.Client
	scheme string
}

func newHandshaker(config Config) (*httpHandshaker, error) {
	if !config.TLSEnabled() {
		return &httpHandshaker{client: &http.Client{Timeout: 15 * time.Second}, scheme: "http"}, nil
	}

	certificate, err := tls.LoadX509KeyPair(config.TLSCert, config.TLSKey)
	if err != nil {
		return nil, fmt.Errorf("%w: loading the activation client certificate: %v",
			output.ErrIncomplete, err)
	}
	caPEM, err := os.ReadFile(config.TLSCACert)
	if err != nil {
		return nil, fmt.Errorf("%w: reading the daemon CA: %v", output.ErrIncomplete, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("%w: no certificates in %s", output.ErrCorrupt, config.TLSCACert)
	}

	return &httpHandshaker{
		scheme: "https",
		client: &http.Client{
			Timeout: 15 * time.Second,
			Transport: &http.Transport{TLSClientConfig: &tls.Config{
				MinVersion:   tls.VersionTLS12,
				Certificates: []tls.Certificate{certificate},
				RootCAs:      pool,
			}},
		},
	}, nil
}

func (handshaker *httpHandshaker) Base(ctx context.Context,
	member activation.Member) (executioncontrol.Handshake, error) {
	var handshake executioncontrol.Handshake
	err := handshaker.get(ctx, member, "/handshake", &handshake)

	return handshake, err
}

func (handshaker *httpHandshaker) Extension(ctx context.Context,
	member activation.Member) (output.ExtensionHandshake, error) {
	var handshake output.ExtensionHandshake
	err := handshaker.get(ctx, member, "/capture/v1/handshake", &handshake)

	return handshake, err
}

func (handshaker *httpHandshaker) get(ctx context.Context, member activation.Member,
	path string, into any) error {
	url := fmt.Sprintf("%s://%s%s", handshaker.scheme, member.Address, path)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	response, err := handshaker.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: %s answered %d", output.ErrIncomplete, url, response.StatusCode)
	}

	return json.NewDecoder(response.Body).Decode(into)
}
