package jetbridge_test

import (
	"testing"
	"time"

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

func (d *noopDelegate) BuildStartTime() time.Time { return time.Time{} }

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

// expectSupervisedExec asserts that a task exec command was wrapped in the
// in-pod task supervisor, embedding the original command as quoted words.
// quotedCommand is the shell-quoted form, e.g. `'/bin/sh' '-c' 'npm test'`.
func expectSupervisedExec(command []string, quotedCommand string) {
	ExpectWithOffset(1, command).To(HaveLen(3))
	ExpectWithOffset(1, command[0]).To(Equal("sh"))
	ExpectWithOffset(1, command[1]).To(Equal("-c"))
	ExpectWithOffset(1, command[2]).To(ContainSubstring(quotedCommand))
	ExpectWithOffset(1, command[2]).To(ContainSubstring(`trap '' HUP`))
}
