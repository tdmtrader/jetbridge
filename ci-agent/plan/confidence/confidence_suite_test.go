package confidence_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestConfidence(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Confidence Suite")
}
