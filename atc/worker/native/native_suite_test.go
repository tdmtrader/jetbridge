package native_test

import (
	"testing"
	"time"

	"github.com/concourse/concourse/atc/db/dbfakes"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestNative(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Native Worker Suite")
}

// noopDelegate satisfies runtime.BuildStepDelegate for tests that don't
// need volume streaming or build timing.
type noopDelegate struct{}

func (d *noopDelegate) BuildStartTime() time.Time { return time.Time{} }

// setupFakeDBContainer wires up a FakeWorker so that FindOrCreateContainer
// creates a container with the given handle. This pattern is repeated in
// nearly every test and extracted here for reuse.
func setupFakeDBContainer(fakeDBWorker *dbfakes.FakeWorker, handle string) (*dbfakes.FakeCreatingContainer, *dbfakes.FakeCreatedContainer) {
	fakeCreatingContainer := new(dbfakes.FakeCreatingContainer)
	fakeCreatingContainer.HandleReturns(handle)
	fakeCreatedContainer := new(dbfakes.FakeCreatedContainer)
	fakeCreatedContainer.HandleReturns(handle)
	fakeCreatingContainer.CreatedReturns(fakeCreatedContainer, nil)
	fakeDBWorker.FindContainerReturns(nil, nil, nil)
	fakeDBWorker.CreateContainerReturns(fakeCreatingContainer, nil)
	return fakeCreatingContainer, fakeCreatedContainer
}
