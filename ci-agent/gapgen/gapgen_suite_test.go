package gapgen_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestGapGen(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "GapGen Suite")
}
