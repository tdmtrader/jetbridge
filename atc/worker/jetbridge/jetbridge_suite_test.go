package jetbridge_test

import (
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db/dbfakes"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestJetbridge(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Jetbridge Suite")
}

// noopDelegate satisfies runtime.BuildStepDelegate for tests that don't
// need volume streaming or build timing.
type noopDelegate struct{}

func (d *noopDelegate) StreamingVolume(_ lager.Logger, _, _, _ string)       {}
func (d *noopDelegate) WaitingForStreamedVolume(_ lager.Logger, _, _ string) {}
func (d *noopDelegate) BuildStartTime() time.Time                           { return time.Time{} }

// setupFakeDBContainer wires up a FakeWorker so that FindOrCreateContainer
// creates a container with the given handle. This pattern is repeated in
// nearly every test and extracted here for reuse.
func setupFakeDBContainer(fakeDBWorker *dbfakes.FakeWorker, handle string) {
	fakeCreatingContainer := new(dbfakes.FakeCreatingContainer)
	fakeCreatingContainer.HandleReturns(handle)
	fakeCreatedContainer := new(dbfakes.FakeCreatedContainer)
	fakeCreatedContainer.HandleReturns(handle)
	fakeCreatingContainer.CreatedReturns(fakeCreatedContainer, nil)
	fakeDBWorker.FindContainerReturns(nil, nil, nil)
	fakeDBWorker.CreateContainerReturns(fakeCreatingContainer, nil)
}
