package steps

import (
	"context"
	"fmt"
	"strings"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/brine-dev/brine-go/pkg/brine"
	concourse "github.com/concourse/concourse"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// RegistrarDefinitions migrates registrar_test.go — how the Kubernetes worker
// presents itself to the rest of Concourse: its name, its identity, how many
// containers it claims to be running, which resource types it offers, and how
// its lease is kept alive.

func RegistrarDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMapUsing[brine.Empty, RegistrarReady](
			"a Kubernetes worker registrar for namespace {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, res brine.Resources) (RegistrarReady, error) {
				ns, ok := p.GetString(0)
				if !ok {
					return RegistrarReady{}, fmt.Errorf("expected a namespace parameter")
				}
				database, ok := res.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return RegistrarReady{}, fmt.Errorf("jetbridge-db resource is %T", res.Get("jetbridge-db"))
				}
				clientset := fake.NewSimpleClientset()
				cfg := jetbridge.NewConfig(ns, "")
				return RegistrarReady{
					Namespace: ns,
					Clientset: clientset,
					DB:        database,
					Config:    cfg,
					Registrar: jetbridge.NewRegistrar(
						lagertest.NewTestLogger("registrar"), clientset, cfg, database.WorkerFactory),
					Ctx: context.Background(),
				}, nil
			},
		),

		// Overrides have to be applied before the registrar is built, so this
		// replaces it rather than mutating one.
		brine.DefineMap[RegistrarReady, RegistrarReady](
			"the operator overrides the resource type images with {string}",
			func(in RegistrarReady, p brine.Params, _ *brine.Recorder) (RegistrarReady, error) {
				spec, ok := p.GetString(0)
				if !ok {
					return RegistrarReady{}, fmt.Errorf("expected an overrides parameter")
				}
				var overrides []string
				for _, o := range strings.Split(spec, ",") {
					if o = strings.TrimSpace(o); o != "" {
						overrides = append(overrides, o)
					}
				}
				in.Config.ResourceTypeImages = jetbridge.MergeResourceTypeImages(overrides)
				in.Registrar = jetbridge.NewRegistrar(
					lagertest.NewTestLogger("registrar"), in.Clientset, in.Config, in.DB.WorkerFactory)
				return in, nil
			},
		),

		brine.DefineMap[RegistrarReady, RegistrarReady](
			"{int} pods belonging to this worker are running",
			func(in RegistrarReady, p brine.Params, _ *brine.Recorder) (RegistrarReady, error) {
				n, ok := p.GetInt(0)
				if !ok {
					return RegistrarReady{}, fmt.Errorf("expected a count parameter")
				}
				for i := 0; i < n; i++ {
					if err := in.createPod(fmt.Sprintf("worker-pod-%d", i), true); err != nil {
						return RegistrarReady{}, err
					}
				}
				return in, nil
			},
		),

		brine.DefineMap[RegistrarReady, RegistrarReady](
			"{int} pods belonging to nobody are running",
			func(in RegistrarReady, p brine.Params, _ *brine.Recorder) (RegistrarReady, error) {
				n, ok := p.GetInt(0)
				if !ok {
					return RegistrarReady{}, fmt.Errorf("expected a count parameter")
				}
				for i := 0; i < n; i++ {
					if err := in.createPod(fmt.Sprintf("stranger-pod-%d", i), false); err != nil {
						return RegistrarReady{}, err
					}
				}
				return in, nil
			},
		),

		// The database goes away underneath the registrar. Registration must
		// report that rather than appearing to succeed.
		brine.DefineMap[RegistrarReady, RegistrarReady](
			"the database connection has been lost",
			func(in RegistrarReady, _ brine.Params, _ *brine.Recorder) (RegistrarReady, error) {
				closedConn, err := in.DB.ClosedConn()
				if err != nil {
					return RegistrarReady{}, err
				}
				factory := db.NewWorkerFactory(closedConn,
					db.NewStaticWorkerCache(lagertest.NewTestLogger("closed"), closedConn, 0))
				in.Registrar = jetbridge.NewRegistrar(
					lagertest.NewTestLogger("registrar"), in.Clientset, in.Config, factory)
				return in, nil
			},
		),

		brine.DefineMap[RegistrarReady, RegistrationOutcome](
			"the worker registers itself",
			func(in RegistrarReady, _ brine.Params, _ *brine.Recorder) (RegistrationOutcome, error) {
				err := in.Registrar.Register(in.Ctx)
				out := RegistrationOutcome{Ready: in, Err: err}
				if err != nil {
					out.Message = err.Error()
					return out, nil
				}
				worker, loadErr := in.reloadWorker()
				if loadErr != nil {
					return RegistrationOutcome{}, loadErr
				}
				out.Worker = worker
				return out, nil
			},
		),

		// WR-03: the lease is refreshed by the same idempotent call.
		brine.DefineMap[RegistrationOutcome, RegistrationOutcome](
			"the lease expires and the worker heartbeats",
			func(in RegistrationOutcome, _ brine.Params, _ *brine.Recorder) (RegistrationOutcome, error) {
				if _, err := in.Ready.DB.Conn.Exec(
					`UPDATE workers SET expires = NOW() - INTERVAL '1 minute' WHERE name = $1`,
					in.Ready.Registrar.WorkerName(),
				); err != nil {
					return RegistrationOutcome{}, fmt.Errorf("expire the lease: %w", err)
				}
				if err := in.Ready.Registrar.Heartbeat(in.Ready.Ctx); err != nil {
					return RegistrationOutcome{}, fmt.Errorf("heartbeat: %w", err)
				}
				worker, err := in.Ready.reloadWorker()
				if err != nil {
					return RegistrationOutcome{}, err
				}
				in.Worker = worker
				return in, nil
			},
		),

		CheckString[RegistrationOutcome]("the worker is registered as {string}",
			"the worker's registered name",
			func(in RegistrationOutcome) (string, error) {
				if err := in.ok(); err != nil {
					return "", err
				}
				return in.Worker.Name(), nil
			}),

		CheckThat[RegistrationOutcome](
			"it presents itself as a running linux worker on this Concourse version",
			func(in RegistrationOutcome) error {
				if err := in.ok(); err != nil {
					return err
				}
				if in.Worker.Platform() != "linux" {
					return fmt.Errorf("expected platform linux, got %q", in.Worker.Platform())
				}
				if in.Worker.State() != db.WorkerStateRunning {
					return fmt.Errorf("expected state running, got %q", in.Worker.State())
				}
				if in.Worker.Version() == nil {
					return fmt.Errorf("expected a worker version, got nil")
				}
				if *in.Worker.Version() != concourse.WorkerVersion {
					return fmt.Errorf("expected version %q, got %q", concourse.WorkerVersion, *in.Worker.Version())
				}
				return nil
			}),

		// WR-02's "global worker" identity: not team-scoped, not ephemeral,
		// no tags. A regression here would silently scope the worker to a team.
		CheckThat[RegistrationOutcome](
			"it belongs to no team and is not ephemeral",
			func(in RegistrationOutcome) error {
				if err := in.ok(); err != nil {
					return err
				}
				if in.Worker.TeamID() != 0 || in.Worker.TeamName() != "" {
					return fmt.Errorf("expected a global worker, got team %d/%q",
						in.Worker.TeamID(), in.Worker.TeamName())
				}
				if in.Worker.Ephemeral() {
					return fmt.Errorf("expected a non-ephemeral worker")
				}
				if len(in.Worker.Tags()) != 0 {
					return fmt.Errorf("expected no tags, got %v", in.Worker.Tags())
				}
				return nil
			}),

		// A deletion probe found the lease check was a LOWER bound only:
		// widening heartbeatTTL from 30s to 24h passed a fully green suite.
		// The TTL is the window in which a dead worker still looks alive to
		// the scheduler, so its CEILING is the safety property — an unbounded
		// lease means work is placed on a worker that is gone. Keeps its own body:
		// the minutes parameter bounds the expiry, it is not compared to it.
		brine.DefineCheck[RegistrationOutcome](
			"its lease expires within {int} minute",
			func(in RegistrationOutcome, p brine.Params, _ *brine.Recorder) error {
				mins, ok := p.GetInt(0)
				if !ok {
					return fmt.Errorf("expected a minutes parameter")
				}
				if err := in.ok(); err != nil {
					return err
				}
				ceiling := time.Now().Add(time.Duration(mins) * time.Minute)
				if !in.Worker.ExpiresAt().Before(ceiling) {
					return fmt.Errorf(
						"expected the lease to expire within %d minute(s) so a dead worker stops being scheduled; "+
							"it expires at %s, which is %s away",
						mins, in.Worker.ExpiresAt(), time.Until(in.Worker.ExpiresAt()).Round(time.Second))
				}
				return nil
			},
		),

		CheckThat[RegistrationOutcome](
			"its lease has not expired",
			func(in RegistrationOutcome) error {
				if err := in.ok(); err != nil {
					return err
				}
				if !in.Worker.ExpiresAt().After(time.Now()) {
					return fmt.Errorf("expected the lease to be in the future, it expires at %s", in.Worker.ExpiresAt())
				}
				return nil
			}),

		CheckInt[RegistrationOutcome]("it reports {int} active containers",
			"the active container count",
			func(in RegistrationOutcome) (int, error) {
				if err := in.ok(); err != nil {
					return 0, err
				}
				return in.Worker.ActiveContainers(), nil
			}),

		// Keeps its own body: an empty image means "present, any image", which a
		// string comparison cannot express.
		brine.DefineCheck[RegistrationOutcome](
			"it offers the resource type {string} as image {string}",
			func(in RegistrationOutcome, p brine.Params, _ *brine.Recorder) error {
				name, _ := p.GetString(0)
				want, ok := p.GetString(1)
				if !ok {
					return fmt.Errorf("expected a type and an image")
				}
				if err := in.ok(); err != nil {
					return err
				}
				for _, rt := range in.Worker.ResourceTypes() {
					if rt.Type == name {
						if want == "" || rt.Image == want {
							return nil
						}
						return fmt.Errorf("expected resource type %q to be %q, got %q", name, want, rt.Image)
					}
				}
				return fmt.Errorf("expected the worker to offer resource type %q; it offers %d types",
					name, len(in.Worker.ResourceTypes()))
			},
		),

		CheckContains[RegistrationOutcome]("registration fails saying {string}",
			"the registration failure",
			func(in RegistrationOutcome) (string, error) {
				if in.Err == nil {
					return "", fmt.Errorf("expected registration to fail, it succeeded")
				}
				return in.Message, nil
			}),
	}
}

func (r RegistrarReady) createPod(name string, mine bool) error {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: r.Namespace},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
	if mine {
		pod.ObjectMeta.Labels = map[string]string{"concourse.ci/worker": r.Registrar.WorkerName()}
	}
	_, err := r.Clientset.CoreV1().Pods(r.Namespace).Create(r.Ctx, pod, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("create pod %q: %w", name, err)
	}
	return nil
}

func (r RegistrarReady) reloadWorker() (db.Worker, error) {
	worker, found, err := r.DB.WorkerFactory.GetWorker(r.Registrar.WorkerName())
	if err != nil {
		return nil, fmt.Errorf("get worker: %w", err)
	}
	if !found {
		return nil, fmt.Errorf("worker %q not found after registration", r.Registrar.WorkerName())
	}
	if _, err := worker.Reload(); err != nil {
		return nil, fmt.Errorf("reload worker: %w", err)
	}
	return worker, nil
}

func (o RegistrationOutcome) ok() error {
	if o.Err != nil {
		return fmt.Errorf("registration failed: %v", o.Err)
	}
	if o.Worker == nil {
		return fmt.Errorf("no worker was registered")
	}
	return nil
}

// RegistrarIdentityDefinitions closes a gap a deletion probe found in
// registrar_test.go: the ginkgo suite asserted ActiveVolumes and StartTime
// were zero, and brine asserted nothing about either.
//
// Both are zero by OMISSION — the registrar never sets them on the atc.Worker
// it builds — which is exactly the kind of property that regresses when
// somebody adds a field "for completeness". A worker reporting volumes it does
// not have, or a start time it did not start at, lies to `fly workers` and to
// anything scheduling on volume locality.
func RegistrarIdentityDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		CheckThat[RegistrationOutcome](
			"it claims no volumes and no start time",
			func(in RegistrationOutcome) error {
				if err := in.ok(); err != nil {
					return err
				}
				if got := in.Worker.ActiveVolumes(); got != 0 {
					return fmt.Errorf(
						"expected the worker to claim no volumes, it claimed %d — a k8s worker "+
							"holds no Concourse volumes, so any count here is fabricated", got)
				}
				if got := in.Worker.StartTime().Unix(); got != 0 {
					return fmt.Errorf(
						"expected no start time, got %d — the registrar does not track uptime, "+
							"so a non-zero value is invented", got)
				}
				return nil
			}),
	}
}
