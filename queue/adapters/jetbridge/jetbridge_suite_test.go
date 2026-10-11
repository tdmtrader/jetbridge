package jetbridge_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestJetBridge(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "JetBridge Runner Suite")
}
