// Command hangar-output-activate moves the Hangar output plane's activation
// epoch through its four guarded transitions, one step at a time or as a walk
// toward a target.
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
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/concourse/concourse/atc/db"
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

	conn, err := controller.OpenDatabase(controller.ResolveDSN(config.DSN), 2)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	epochs := activation.Epochs{DB: conn}
	epoch := executioncontrol.ActivationEpoch(config.Epoch)

	switch config.Mode {
	case ModeBegin:
		if err := epochs.Begin(ctx, epoch); err != nil {
			return alreadyDone(out, err)
		}
		fmt.Fprintf(out, "began activation epoch %d (base=initial output=initial)\n", epoch)

	case ModeAttest:
		evidence, err := attest(ctx, config, epoch)
		if err != nil {
			return err
		}
		if err := epochs.Attest(ctx, epoch, config.Facet, evidence); err != nil {
			return alreadyDone(out, err)
		}
		fmt.Fprintf(out, "attested epoch %d's %s facet over the cohort digest %s\n",
			epoch, config.Facet, evidence.CohortDigest)

	case ModeEnable:
		if err := enable(ctx, epochs, epoch, config, out); err != nil {
			return alreadyDone(out, err)
		}

	case ModeReconcileIntegrity:
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		repository := &db.HangarOutputRepository{}
		if err := repository.ReconcilePolicyViolation(ctx, tx, config.Epoch,
			output.PolicyViolation(config.IntegrityViolation), config.IntegritySubject); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		fmt.Fprintf(out, "reconciled %s for %q in epoch %d; history retained. This acknowledgement does not restore lost objects or repair storage permissions.\n", config.IntegrityViolation, config.IntegritySubject, config.Epoch)

	case ModeDrain:
		if err := drain(ctx, epochs, epoch, config, out); err != nil {
			return err
		}

	case ModeWalk:
		// The walk prints each transition and the final row itself, and
		// returns here rather than reading the row below: a walk to off with
		// no row writes nothing and succeeds, and that read would fail it.
		return walk(ctx, epochs, epoch, config, out)

	default:
		return fmt.Errorf("%w: --mode %q; choose: %s", output.ErrUnknownMember, config.Mode, modeList)
	}

	state, err := epochs.Read(ctx, epoch)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "epoch %d: base=%s output=%s revision=%d\n",
		state.Epoch, state.Base, state.Output, state.Revision)

	return nil
}

// enable reads the facet's activation preconditions, and enables it only if
// every one of them is met.
//
// THE PRECONDITIONS WERE DEAD CODE. Ten typed checks against the live tables
// shipped implemented, tested and called by nothing, because this branch called
// `Epochs.Enable` -- the bare CAS -- and the checks were held by a sentence in
// the runbook instead. That is the third capability on this track to ship with
// no production caller, which is why `atc/hangaroutput/activation` is now
// inside the repository's reachability rule: a fourth fails a suite rather than
// costing a reviewer.
//
// The DECISION is activation.EnableStep's -- check first, enable second, and
// refuse with every unmet reason rather than the first -- for the same reason
// DrainStep owns the drain's. What is left here is printing, which is this
// binary's job, and the whole list is printed either way: an operator who is
// refused wants to see what WAS met as much as what was not, and one who
// succeeds wants the record of what was true at the moment they turned it on.
func enable(ctx context.Context, epochs activation.Epochs,
	epoch executioncontrol.ActivationEpoch, config Config, out *os.File) error {
	outcome, err := epochs.EnableStep(ctx, epoch, config.Facet, true)
	reportPreconditions(out, epoch, config.Facet, outcome.Preconditions)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "enabled epoch %d's %s facet\n", epoch, config.Facet)

	return nil
}

// alreadyDone turns a replay at the target into success: a recreated
// activation Job reruns its step, the row is already where the step leads, and
// nothing was written. Every other error stands.
func alreadyDone(out io.Writer, err error) error {
	if errors.Is(err, activation.ErrAlreadyAtTarget) {
		fmt.Fprintf(out, "%v\n", err)
		return nil
	}
	return err
}

// reportPreconditions prints every precondition, met and unmet, with what was
// found and why it matters.
//
// Unmet ones are printed with their reason as well as their finding, because a
// refusal an operator cannot act on is a refusal they will work around; met
// ones are printed at all because a list that shrinks to nothing when everything
// is fine gives an operator no way to see that the step asked anything.
func reportPreconditions(out io.Writer, epoch executioncontrol.ActivationEpoch,
	facet activation.Facet, preconditions []activation.Precondition) {
	if len(preconditions) == 0 {
		return
	}

	fmt.Fprintf(out, "epoch %d's %s facet, %d activation preconditions:\n",
		epoch, facet, len(preconditions))
	for _, precondition := range preconditions {
		mark := "met "
		if !precondition.Met {
			mark = "UNMET"
		}
		fmt.Fprintf(out, "  [%s] %s: %s\n", mark, precondition.Name, precondition.Detail)
		if !precondition.Met {
			fmt.Fprintf(out, "          why it matters: %s\n", precondition.Why)
		}
	}
}

// drain formats what activation.DrainStep decided, one facet at a time.
//
// The DECISION -- stop emission first, then count, then refuse or disable -- is
// activation.DrainStep's, and it lives there rather than here because
// `Epochs.Disable` deliberately does not decide emptiness, so this sequence was
// the only thing standing between a live plane and `disabled` and nothing
// exercised it. What is left here is printing, which is this binary's job.
func drain(ctx context.Context, epochs activation.Epochs,
	epoch executioncontrol.ActivationEpoch, config Config, out *os.File) error {
	for _, facet := range activation.DrainFacets(config.All, config.Facet) {
		outcome, err := epochs.DrainStep(ctx, epoch, facet, config.Finalize)
		if outcome.Drained {
			fmt.Fprintf(out, "epoch %d's %s facet is draining: no new admission, and every "+
				"release, settlement and already-admitted delete continues\n", epoch, facet)
		}
		if err != nil {
			return err
		}
		switch {
		case outcome.Skipped:
			fmt.Fprintf(out, "epoch %d's %s facet is %q; nothing to drain\n",
				epoch, facet, outcome.State)
		case len(outcome.Residue) != 0:
			fmt.Fprintf(out, "epoch %d's %s facet still holds state and stays draining:\n",
				epoch, facet)
			for _, one := range outcome.Residue {
				fmt.Fprintf(out, "  - %s\n", one)
			}
		case outcome.Disabled:
			fmt.Fprintf(out, "epoch %d's %s facet is disabled\n", epoch, facet)
		default:
			fmt.Fprintf(out, "epoch %d's %s facet holds nothing; re-run with --finalize to "+
				"disable it\n", epoch, facet)
		}
	}

	return nil
}

// walk moves the epoch toward --target through activation.Walker, which owns
// the order, the skips and the refusals; this wires it to the cluster.
func walk(ctx context.Context, epochs activation.Epochs,
	epoch executioncontrol.ActivationEpoch, config Config, out io.Writer) error {
	client, err := inClusterClient()
	if err != nil {
		return err
	}
	handshaker, err := newHandshaker(config)
	if err != nil {
		return err
	}

	walker := activation.Walker{
		Epochs: epochs,
		Cohort: &podCohort{
			pods:      client,
			namespace: config.Namespace,
			selector:  config.DaemonSelector,
			port:      config.DaemonPort,
		},
		Handshakes: handshaker,
		Readiness: &daemonSetCohort{
			client:    client,
			namespace: config.Namespace,
			name:      config.DaemonSetName,
			selector:  config.DaemonSelector,
		},
		ReceiptKeyLifetime: config.ReceiptKeyLifetime,
		ReadinessTimeout:   config.ReadinessTimeout,
		Out:                out,
	}

	return walker.Walk(ctx, epoch, config.Target, config.Finalize)
}

// inClusterClient is the Job's own Kubernetes client.
func inClusterClient() (kubernetes.Interface, error) {
	restConfig, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("%w: attestation enumerates the daemon cohort "+
			"from the Kubernetes API rather than from node labels -- a label is a hint the "+
			"daemon itself wrote -- and this Job cannot reach the API: %v",
			output.ErrInfrastructure, err)
	}
	client, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("%w: building the Kubernetes client: %v",
			output.ErrInfrastructure, err)
	}

	return client, nil
}

// attest builds the evidence from the live cohort.
func attest(ctx context.Context, config Config,
	epoch executioncontrol.ActivationEpoch) (activation.Evidence, error) {
	client, err := inClusterClient()
	if err != nil {
		return activation.Evidence{}, err
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

// daemonSetCohort reads the output DaemonSet's rollout state for the walk.
//
// The DaemonSet only through a namespaced get of the one name, which is all the
// activation Role grants on DaemonSets; its member pods through the same
// selector list the cohort is enumerated from.
type daemonSetCohort struct {
	client    kubernetes.Interface
	namespace string
	name      string
	selector  string
}

func (cohort *daemonSetCohort) Readiness(ctx context.Context) (activation.DaemonSetReadiness, error) {
	set, err := cohort.client.AppsV1().DaemonSets(cohort.namespace).Get(ctx, cohort.name,
		metav1.GetOptions{})
	if err != nil {
		return activation.DaemonSetReadiness{Name: cohort.name}, fmt.Errorf("%w: reading the "+
			"output DaemonSet %s in %s: %v", output.ErrInfrastructure, cohort.name,
			cohort.namespace, err)
	}
	pods, err := cohort.client.CoreV1().Pods(cohort.namespace).List(ctx,
		metav1.ListOptions{LabelSelector: cohort.selector})
	if err != nil {
		return activation.DaemonSetReadiness{Name: cohort.name}, fmt.Errorf("%w: listing "+
			"output daemon pods in %s: %v", output.ErrInfrastructure, cohort.namespace, err)
	}

	return daemonSetReadiness(*set, pods.Items), nil
}

// daemonSetReadiness is the pure reading of a DaemonSet and its member pods.
//
// numberReady and not numberAvailable: available adds minReadySeconds, which
// is a rollout pacing knob and not a statement about whether a daemon answers.
// updatedNumberScheduled is what says the rollout has finished, so the old
// pods that still speak the previous epoch are gone from the count. A member
// that is terminating is not Ready here whatever its condition says: it is
// leaving, and it may still answer a handshake for the epoch being replaced.
// The generation pair goes with the counts, because until the controller has
// observed the current template the counts are the previous template's.
func daemonSetReadiness(set appsv1.DaemonSet, pods []corev1.Pod) activation.DaemonSetReadiness {
	readiness := activation.DaemonSetReadiness{
		Name:               set.Name,
		Desired:            int(set.Status.DesiredNumberScheduled),
		Updated:            int(set.Status.UpdatedNumberScheduled),
		Ready:              int(set.Status.NumberReady),
		Generation:         set.Generation,
		ObservedGeneration: set.Status.ObservedGeneration,
	}
	for _, pod := range pods {
		ready := pod.DeletionTimestamp == nil
		if ready {
			ready = false
			for _, condition := range pod.Status.Conditions {
				if condition.Type == corev1.PodReady {
					ready = condition.Status == corev1.ConditionTrue
				}
			}
		}
		readiness.Members = append(readiness.Members,
			activation.MemberReadiness{Pod: pod.Name, Ready: ready})
	}

	return readiness
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
				ServerName:   config.TLSServerName,
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
