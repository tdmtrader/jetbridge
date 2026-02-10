package orchestrator_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestOrchestrator(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Orchestrator Suite")
}
