package exec_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagertest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/event"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/postgresrunner"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/util"
	"github.com/concourse/concourse/atc/worker"
)

func init() {
	util.PanicSink = GinkgoWriter
}

func TestExec(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Exec Suite")
}

var testLogger = lagertest.NewTestLogger("test")

type execDBFixture struct {
	Conn                  db.DbConn
	LockFactory           lock.LockFactory
	Builder               dbtest.Builder
	TeamFactory           db.TeamFactory
	BuildFactory          db.BuildFactory
	WorkerFactory         db.WorkerFactory
	ResourceConfigFactory db.ResourceConfigFactory
	ResourceCacheFactory  db.ResourceCacheFactory
}

var execPostgresRunner postgresrunner.Runner

var _ = postgresrunner.GinkgoRunner(&execPostgresRunner)

func useExecDB() *execDBFixture {
	GinkgoHelper()

	execPostgresRunner.CreateTestDBFromTemplate()
	DeferCleanup(execPostgresRunner.DropTestDB)

	conn := execPostgresRunner.OpenConn()
	DeferCleanup(func() { Expect(conn.Close()).To(Succeed()) })
	db.CleanupBaseResourceTypesCache()

	var lockConns [lock.FactoryCount]*sql.DB
	for i := 0; i < lock.FactoryCount; i++ {
		lockConn := execPostgresRunner.OpenSingleton()
		lockConns[i] = lockConn
		connToClose := lockConn
		DeferCleanup(func() { Expect(connToClose.Close()).To(Succeed()) })
	}

	lockFactory := lock.NewLockFactory(
		lockConns,
		func(lager.Logger, lock.LockID) {},
		func(lager.Logger, lock.LockID) {},
	)
	logger := lagertest.NewTestLogger("exec-postgres-fixture")

	return &execDBFixture{
		Conn:                  conn,
		LockFactory:           lockFactory,
		Builder:               dbtest.NewBuilder(conn, lockFactory),
		TeamFactory:           db.NewTeamFactory(conn, lockFactory),
		BuildFactory:          db.NewBuildFactory(conn, lockFactory, 0, time.Hour),
		WorkerFactory:         db.NewWorkerFactory(conn, db.NewStaticWorkerCache(logger, conn, 0)),
		ResourceConfigFactory: db.NewResourceConfigFactory(conn, lockFactory),
		ResourceCacheFactory:  db.NewResourceCacheFactory(conn, lockFactory),
	}
}

func closedExecCloneConn() db.DbConn {
	GinkgoHelper()
	conn := execPostgresRunner.OpenConn()
	Expect(conn.Close()).To(Succeed())
	return conn
}

func createExecJobBuild(
	fixture *execDBFixture,
	teamName string,
	ref atc.PipelineRef,
	config atc.Config,
	createdBy string,
) (db.Team, db.Pipeline, db.Job, db.Build) {
	GinkgoHelper()
	_, configured := config.Jobs.Lookup("some-job")
	Expect(configured).To(BeTrue())

	team, err := fixture.TeamFactory.CreateTeam(atc.Team{Name: teamName})
	Expect(err).NotTo(HaveOccurred())
	pipeline, _, err := team.SavePipeline(ref, config, 0, false)
	Expect(err).NotTo(HaveOccurred())
	scenario := &dbtest.Scenario{Team: team, Pipeline: pipeline}
	job := scenario.Job("some-job")
	build, err := job.CreateBuild(createdBy)
	Expect(err).NotTo(HaveOccurred())
	return team, pipeline, job, build
}

func execBuildEventCount(fixture *execDBFixture, build db.Build) int {
	GinkgoHelper()
	var count int
	Expect(fixture.Conn.QueryRow(`
		SELECT COUNT(*)
		FROM build_events
		WHERE build_id = $1
	`, build.ID()).Scan(&count)).To(Succeed())
	return count
}

// execBuildEvents reads back every event the step delegates persisted for the
// build. The event source blocks once it drains an unfinished build, so the
// row count bounds the read.
func execBuildEvents(fixture *execDBFixture, build db.Build) []atc.Event {
	GinkgoHelper()
	count := execBuildEventCount(fixture, build)
	if count == 0 {
		return nil
	}

	source, err := build.Events(0)
	Expect(err).NotTo(HaveOccurred())
	defer func() { Expect(source.Close()).To(Succeed()) }()

	events := make([]atc.Event, 0, count)
	for i := 0; i < count; i++ {
		envelope, err := source.Next()
		Expect(err).NotTo(HaveOccurred())
		encoded, err := json.Marshal(envelope)
		Expect(err).NotTo(HaveOccurred())
		var message event.Message
		Expect(json.Unmarshal(encoded, &message)).To(Succeed())
		events = append(events, message.Event)
	}
	return events
}

// execBuildEventPayloads reads the raw persisted payloads of one event type.
// The parser registry maps "initialize", "start" and "finish" to their V10
// shapes, so fields like Finish.Succeeded only survive at this level.
func execBuildEventPayloads(fixture *execDBFixture, build db.Build, eventType atc.EventType) []json.RawMessage {
	GinkgoHelper()
	rows, err := fixture.Conn.Query(`
		SELECT payload
		FROM build_events
		WHERE build_id = $1 AND type = $2
		ORDER BY event_id ASC
	`, build.ID(), string(eventType))
	Expect(err).NotTo(HaveOccurred())
	defer func() { Expect(rows.Close()).To(Succeed()) }()

	var payloads []json.RawMessage
	for rows.Next() {
		var payload []byte
		Expect(rows.Scan(&payload)).To(Succeed())
		payloads = append(payloads, json.RawMessage(payload))
	}
	Expect(rows.Err()).NotTo(HaveOccurred())
	return payloads
}

func execBuildFinishEvents(fixture *execDBFixture, build db.Build) []event.Finish {
	GinkgoHelper()
	var finishes []event.Finish
	for _, payload := range execBuildEventPayloads(fixture, build, event.EventTypeFinish) {
		var finish event.Finish
		Expect(json.Unmarshal(payload, &finish)).To(Succeed())
		finishes = append(finishes, finish)
	}
	return finishes
}

func execBuildEventTypes(fixture *execDBFixture, build db.Build) []atc.EventType {
	GinkgoHelper()
	var types []atc.EventType
	for _, e := range execBuildEvents(fixture, build) {
		types = append(types, e.EventType())
	}
	return types
}

func execBuildErrorMessages(fixture *execDBFixture, build db.Build) []string {
	GinkgoHelper()
	var messages []string
	for _, e := range execBuildEvents(fixture, build) {
		if errored, ok := e.(event.Error); ok {
			messages = append(messages, errored.Message)
		}
	}
	return messages
}

func execBuildLog(fixture *execDBFixture, build db.Build, source event.OriginSource) string {
	GinkgoHelper()
	var payload string
	for _, e := range execBuildEvents(fixture, build) {
		if logged, ok := e.(event.Log); ok && logged.Origin.Source == source {
			payload += logged.Payload
		}
	}
	return payload
}

var noopStepper exec.Stepper = func(atc.Plan) exec.Step {
	Fail("cannot create substep")
	return nil
}

type buildStepDelegateFactory func(exec.RunState) exec.BuildStepDelegate

func (f buildStepDelegateFactory) BuildStepDelegate(state exec.RunState) exec.BuildStepDelegate {
	return f(state)
}

type setPipelineStepDelegateFactory func(exec.RunState) exec.SetPipelineStepDelegate

func (f setPipelineStepDelegateFactory) SetPipelineStepDelegate(state exec.RunState) exec.SetPipelineStepDelegate {
	return f(state)
}

type checkDelegateFactory func(exec.RunState) exec.CheckDelegate

func (f checkDelegateFactory) CheckDelegate(state exec.RunState) exec.CheckDelegate {
	return f(state)
}

type getDelegateFactory func(exec.RunState) exec.GetDelegate

func (f getDelegateFactory) GetDelegate(state exec.RunState) exec.GetDelegate {
	return f(state)
}

type putDelegateFactory func(exec.RunState) exec.PutDelegate

func (f putDelegateFactory) PutDelegate(state exec.RunState) exec.PutDelegate {
	return f(state)
}

type taskDelegateFactory func(exec.RunState) exec.TaskDelegate

func (f taskDelegateFactory) TaskDelegate(state exec.RunState) exec.TaskDelegate {
	return f(state)
}

// recordingStreamer wraps the real worker.Streamer, keeping track of how many
// files were streamed and whether the caller closed each stream it handed out.
type recordingStreamer struct {
	exec.Streamer

	callCount int
	streams   []*recordedStream
}

func newRecordingStreamer() *recordingStreamer {
	return &recordingStreamer{
		Streamer: worker.NewStreamer(compression.NewGzipCompression()),
	}
}

func (s *recordingStreamer) StreamFile(ctx context.Context, artifact runtime.Artifact, path string) (io.ReadCloser, error) {
	s.callCount++

	stream, err := s.Streamer.StreamFile(ctx, artifact, path)
	if err != nil {
		return nil, err
	}

	recorded := &recordedStream{ReadCloser: stream}
	s.streams = append(s.streams, recorded)
	return recorded, nil
}

type recordedStream struct {
	io.ReadCloser
	closed bool
}

func (s *recordedStream) Close() error {
	s.closed = true
	return s.ReadCloser.Close()
}
