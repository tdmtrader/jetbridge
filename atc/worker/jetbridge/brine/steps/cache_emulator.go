package steps

import (
	"crypto/rand"
	"fmt"
	"sort"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/fsouza/fake-gcs-server/fakestorage"
)

// The resource-cache bucket behind every durable-tier scenario.
//
// The daemon's fail-open cache is a wrapper over the same object interface as
// Hangar, with the same two backends, GCS and the disk store; the filesystem
// backend these scenarios used to seed by writing files is gone. The cache
// here is therefore a GCS bucket on an in-process fake-gcs-server, reached
// through the daemon's own --durable-store=gcs --durable-endpoint flags, and
// the fixture seeds and inspects it through the emulator's server-side API.
//
// IN-PROCESS EVEN IN CI, unlike the Hangar fixture's emulator, for one reason:
// the retention scenario needs an object LAST WRITTEN 48 HOURS AGO, and only
// the server side can say so. An upload through the API is created now, and a
// shared emulator offers no backdating. The adapter and the daemon share the
// private network namespace, so the daemon reaches 127.0.0.1 here directly.
//
// THE FIXTURE RECORDS NOTHING (convention 10): the emulator stores objects and
// answers requests; every assertion is on what the bucket holds afterwards.
type cacheEmulator struct {
	server  *fakestorage.Server
	bucket  string
	stopped bool
}

func startCacheEmulator(rec *brine.Recorder) (*cacheEmulator, error) {
	server, err := fakestorage.NewServerWithOptions(fakestorage.Options{
		Scheme: "http", Host: "127.0.0.1", Port: 0,
	})
	if err != nil {
		return nil, fmt.Errorf("start the cache bucket's emulator: %w", err)
	}
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		server.Stop()
		return nil, err
	}
	emulator := &cacheEmulator{server: server, bucket: fmt.Sprintf("brine-cache-%x", suffix)}
	TrackDisposer(rec, "the cache bucket's emulator", func() error { emulator.stop(); return nil })
	server.CreateBucketWithOpts(fakestorage.CreateBucketOpts{Name: emulator.bucket})
	return emulator, nil
}

// daemonArgs points a daemon's fail-open cache at this bucket.
func (e *cacheEmulator) daemonArgs() []string {
	return []string{"--durable-store", "gcs", "--durable-bucket", e.bucket, "--durable-endpoint", e.server.URL()}
}

// put seeds an object written at a chosen time, the way a producer that ran
// then would have left it.
func (e *cacheEmulator) put(key string, body []byte, written time.Time) {
	e.server.CreateObject(fakestorage.Object{
		ObjectAttrs: fakestorage.ObjectAttrs{
			BucketName: e.bucket, Name: key, Created: written, Updated: written,
		},
		Content: body,
	})
}

func (e *cacheEmulator) get(key string) ([]byte, bool) {
	object, err := e.server.GetObject(e.bucket, key)
	if err != nil {
		return nil, false
	}
	return object.Content, true
}

func (e *cacheEmulator) has(key string) bool {
	_, ok := e.get(key)
	return ok
}

// keys lists the bucket's objects, sorted.
func (e *cacheEmulator) keys() ([]string, error) {
	objects, _, err := e.server.ListObjectsWithOptions(e.bucket, fakestorage.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list the cache bucket: %w", err)
	}
	keys := make([]string, 0, len(objects))
	for _, object := range objects {
		keys = append(keys, object.Name)
	}
	sort.Strings(keys)
	return keys, nil
}

// stop takes the bucket away mid-scenario: every daemon request to it now
// fails at the transport, which is what an unreachable store is.
func (e *cacheEmulator) stop() {
	if !e.stopped {
		e.stopped = true
		e.server.Stop()
	}
}
