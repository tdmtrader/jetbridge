package steps

import (
	"context"
	"fmt"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"k8s.io/client-go/kubernetes/fake"
)

// The shared setup layer.
//
// Every step family used to open the same way: pull jetbridge-db out of the
// resources, persist a worker row, make a clientset, build a config, apply
// whatever this scenario needs, and construct a worker. Eighteen sites did it
// by hand, plus three per-file helpers doing the same job with slightly
// different spellings.
//
// That is the opposite of what a Gherkin layer is for. Scenarios are supposed
// to share a vocabulary; sharing the vocabulary means sharing what backs it.
//
// Cluster is that preamble, once. What actually varies between call sites is
// small and regular — an executor, a volume repository, an artifact locator,
// and a handful of config fields — so it varies by option rather than by
// rewriting the preamble.

// Cluster is a worker, its cluster, and the database row behind it.
type Cluster struct {
	Namespace  string
	WorkerName string
	Worker     *jetbridge.Worker
	Clientset  *fake.Clientset
	DBWorker   db.Worker
	DB         JetbridgeDB
	TeamID     int
	Ctx        context.Context
}

// Ready is the common domain state, for the many families that need nothing
// more than "a worker on a cluster".
func (c Cluster) Ready() ClusterReady {
	return ClusterReady{
		Namespace: c.Namespace,
		Worker:    c.Worker,
		Clientset: c.Clientset,
		Ctx:       c.Ctx,
		TeamID:    c.TeamID,
	}
}

type clusterSpec struct {
	namespace  string
	workerName string
	applyCfg   func(*jetbridge.Config)
	executor   jetbridge.PodExecutor
	volumeRepo bool
	locator    *jetbridge.ArtifactLocator
	team       bool
}

// ClusterOption varies one axis of the preamble.
type ClusterOption func(*clusterSpec)

// WithNamespace overrides the default "test-namespace".
func WithNamespace(ns string) ClusterOption {
	return func(s *clusterSpec) { s.namespace = ns }
}

// WithWorkerName overrides the default "k8s-worker-1".
func WithWorkerName(name string) ClusterOption {
	return func(s *clusterSpec) { s.workerName = name }
}

// WithConfig applies scenario-specific config before the worker is built —
// timeouts, cache paths, registry settings.
func WithConfig(apply func(*jetbridge.Config)) ClusterOption {
	return func(s *clusterSpec) { s.applyCfg = apply }
}

// WithExecutor gives the worker something to exec with. Without it the worker
// has no executor and takes the direct-mode path.
func WithExecutor(e jetbridge.PodExecutor) ClusterOption {
	return func(s *clusterSpec) { s.executor = e }
}

// WithVolumeRepo lets the worker persist volumes. Without it
// CreateVolumeForArtifact refuses outright.
func WithVolumeRepo() ClusterOption {
	return func(s *clusterSpec) { s.volumeRepo = true }
}

// WithArtifactLocator installs the DaemonSet storage backend. Note this
// REPLACES the backend wholesale, so it must come after any config that the
// backend reads.
func WithArtifactLocator(l *jetbridge.ArtifactLocator) ClusterOption {
	return func(s *clusterSpec) { s.locator = l }
}

// WithTeam creates a real team row. Anything persisting a volume needs one:
// the volumes table has a foreign key onto teams, so a made-up team id fails.
func WithTeam() ClusterOption {
	return func(s *clusterSpec) { s.team = true }
}

// NewCluster builds the preamble from the jetbridge-db resource.
func NewCluster(res brine.Resources, opts ...ClusterOption) (Cluster, error) {
	spec := clusterSpec{namespace: "test-namespace", workerName: "k8s-worker-1"}
	for _, o := range opts {
		o(&spec)
	}

	database, ok := res.Get("jetbridge-db").(JetbridgeDB)
	if !ok {
		return Cluster{}, fmt.Errorf("jetbridge-db resource is %T", res.Get("jetbridge-db"))
	}
	dbWorker, err := database.PersistNamedWorker(spec.workerName)
	if err != nil {
		return Cluster{}, err
	}

	cfg := jetbridge.NewConfig(spec.namespace, "")
	if spec.applyCfg != nil {
		spec.applyCfg(&cfg)
	}

	clientset := fake.NewSimpleClientset()
	worker := jetbridge.NewWorker(dbWorker, clientset, cfg)
	if spec.executor != nil {
		worker.SetExecutor(spec.executor)
	}
	if spec.volumeRepo {
		worker.SetVolumeRepo(database.VolumeRepository)
	}
	if spec.locator != nil {
		worker.SetArtifactLocator(spec.locator)
	}

	teamID := 0
	if spec.team {
		team, err := database.TeamFactory.CreateTeam(atc.Team{
			Name: "fixture-" + spec.namespace + "-" + spec.workerName,
		})
		if err != nil {
			return Cluster{}, fmt.Errorf("create team: %w", err)
		}
		teamID = team.ID()
	}

	return Cluster{
		Namespace:  spec.namespace,
		WorkerName: spec.workerName,
		Worker:     worker,
		Clientset:  clientset,
		DBWorker:   dbWorker,
		DB:         database,
		TeamID:     teamID,
		Ctx:        context.Background(),
	}, nil
}
