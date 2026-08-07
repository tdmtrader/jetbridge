package resourcecapture_test

import (
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/postgresrunner"
)

var resourceCapturePostgres postgresrunner.StandardTestRunner

func TestMain(m *testing.M) {
	os.Exit(resourceCapturePostgres.Main(m))
}

type resourceCaptureDB struct {
	Teams           db.TeamFactory
	ResourceConfigs db.ResourceConfigFactory
	Templates       db.WorkflowRunTemplateFactory
}

func useRealResourceCaptureDB(t *testing.T) resourceCaptureDB {
	t.Helper()

	db.CleanupBaseResourceTypesCache()
	conn := resourceCapturePostgres.OpenConn(t)
	locks := lock.NewTestLockFactory(&resourceCaptureLockDB{held: map[string]bool{}})
	return resourceCaptureDB{
		Teams:           db.NewTeamFactory(conn, locks),
		ResourceConfigs: db.NewResourceConfigFactory(conn, locks),
		Templates:       db.NewWorkflowRunTemplateFactory(conn, locks),
	}
}

// resourceCaptureLockDB gives the production test lock factory advisory-lock
// semantics without opening connections outside StandardTestRunner's clone.
type resourceCaptureLockDB struct {
	mu   sync.Mutex
	held map[string]bool
}

func (database *resourceCaptureLockDB) Acquire(id lock.LockID) (bool, error) {
	database.mu.Lock()
	defer database.mu.Unlock()

	key := fmt.Sprint([]int(id))
	if database.held[key] {
		return false, nil
	}
	database.held[key] = true
	return true, nil
}

func (database *resourceCaptureLockDB) Release(id lock.LockID) (bool, error) {
	database.mu.Lock()
	defer database.mu.Unlock()

	key := fmt.Sprint([]int(id))
	if !database.held[key] {
		return false, nil
	}
	delete(database.held, key)
	return true, nil
}
