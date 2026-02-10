package scoring_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestScoring(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "CI Agent Scoring Suite")
}
