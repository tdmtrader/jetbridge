package runlifecycle_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestRunLifecycle(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "RunLifecycle Suite")
}
