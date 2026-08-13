package metric

import (
	"fmt"
	"maps"
	"strings"
	"time"

	"code.cloudfoundry.org/lager/v3"

	"github.com/concourse/concourse/atc/db"
	flags "github.com/jessevdk/go-flags"
)

type Event struct {
	Name       string
	Value      float64
	Attributes map[string]string
	Host       string
	Time       time.Time
	TraceID    string // optional trace ID for exemplar attachment
}

type Emitter interface {
	Emit(lager.Logger, Event)
}

type EmitterFactory interface {
	Description() string
	IsConfigured() bool
	NewEmitter(map[string]string) (Emitter, error)
}

type Monitor struct {
	emitter          Emitter
	eventHost        string
	eventAttributes  map[string]string
	emissions        chan eventEmission
	emitterFactories []EmitterFactory

	Databases       []db.DbConn
	DatabaseQueries Counter

	ContainersCreated Counter
	VolumesCreated    Counter

	// Durable artifact tier. Together these answer the only question that
	// matters about it: is it earning its egress?
	//
	// A lookup ends in exactly one of these, so they sum to the number of
	// resource-cache lookups that reached the daemon:
	//   ResourceCacheLocalHits  a node already had it -- the fast path
	//   DurableWarmHits         pulled from the store -- the tier paying off
	//   DurableWarmMisses       tried the store, nothing there or it failed
	//   DurableWarmSuppressed   skipped, because a recent warm for this key failed
	//
	// Suppressed rising is the signal that the bucket is unhealthy: it means the
	// negative cache is doing its job, absorbing a retry loop that would
	// otherwise cost a warm timeout every few seconds per waiting get step.
	ResourceCacheLocalHits Counter
	DurableWarmHits        Counter
	DurableWarmMisses      Counter
	DurableWarmSuppressed  Counter

	FailedContainers Counter
	FailedVolumes    Counter

	ContainersDeleted Counter
	VolumesDeleted    Counter

	JobsScheduled  Counter
	JobsScheduling Gauge

	BuildsStarted Counter
	BuildsRunning Gauge

	CheckBuildsStarted Counter
	CheckBuildsRunning Gauge

	JobStatuses  map[JobStatusLabels]*Gauge
	StepsWaiting map[StepsWaitingLabels]*Gauge

	// When global resource is not enabled, ChecksStarted should equal to CheckBuildsStarted.
	// But with global resource enabled, ChecksStarted measures how many checks really run.
	// For example, there are 10 resources having exact same config, so they belong to the same
	// resource configure scope. In each check period, 10 check builds will be created,
	// CheckBuildsStarted should be 10. But only 1 check build should run real check, rest 9 check
	// builds should reuse the first check's result, thus ChecksStarted will be 1.
	// The bigger diff between ChecksStarted and CheckBuildsStarted, the more global resource benefits.
	ChecksStarted Counter

	// ChecksFinishedWithError+ChecksFinishedWithSuccess should equal to ChecksStarted.
	ChecksFinishedWithError   Counter
	ChecksFinishedWithSuccess Counter

	ChecksEnqueued Counter

	ConcurrentRequests         map[string]*Gauge
	ConcurrentRequestsLimitHit map[string]*Counter

	GetStepCacheHits Counter

	K8sPodStartupDuration Gauge
	K8sImagePullFailures  Counter
}

var Metrics = NewMonitor()

func NewMonitor() *Monitor {
	return &Monitor{
		StepsWaiting:               map[StepsWaitingLabels]*Gauge{},
		ConcurrentRequests:         map[string]*Gauge{},
		ConcurrentRequestsLimitHit: map[string]*Counter{},
	}
}

func (m *Monitor) RegisterEmitter(factory EmitterFactory) {
	m.emitterFactories = append(m.emitterFactories, factory)
}

func (m *Monitor) WireEmitters(group *flags.Group) {
	for _, factory := range m.emitterFactories {
		_, err := group.AddGroup(fmt.Sprintf("Metric Emitter (%s)", factory.Description()), "", factory)
		if err != nil {
			panic(err)
		}
	}
}

type eventEmission struct {
	event  Event
	logger lager.Logger
}

func (m *Monitor) Initialize(logger lager.Logger, host string, attributes map[string]string, bufferSize uint32) error {
	logger.Debug("metric-initialize", lager.Data{
		"host":        host,
		"attributes":  attributes,
		"buffer-size": bufferSize,
	})

	var (
		emitterDescriptions []string
		err                 error
	)

	for _, factory := range m.emitterFactories {
		if factory.IsConfigured() {
			emitterDescriptions = append(emitterDescriptions, factory.Description())
		}
	}
	if len(emitterDescriptions) > 1 {
		return fmt.Errorf("multiple emitters configured: %s", strings.Join(emitterDescriptions, ", "))
	}

	var emitter Emitter

	for _, factory := range m.emitterFactories {
		if factory.IsConfigured() {
			emitter, err = factory.NewEmitter(attributes)
			if err != nil {
				return err
			}
		}
	}

	if emitter == nil {
		return nil
	}

	m.emitter = emitter
	m.eventHost = host
	m.eventAttributes = attributes
	m.emissions = make(chan eventEmission, int(bufferSize))

	go m.emitLoop()

	return nil
}

func (m *Monitor) emit(logger lager.Logger, event Event) {
	if m.emitter == nil {
		return
	}

	event.Host = m.eventHost
	event.Time = time.Now()

	mergedAttributes := maps.Clone(m.eventAttributes)

	if event.Attributes != nil {
		maps.Copy(mergedAttributes, event.Attributes)
	}

	event.Attributes = mergedAttributes

	select {
	case m.emissions <- eventEmission{logger: logger, event: event}:
	default:
		logger.Error("queue-full", nil)
	}
}

func (m *Monitor) emitLoop() {
	for emission := range m.emissions {
		m.emitter.Emit(emission.logger.Session("emit"), emission.event)
	}
}
