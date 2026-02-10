package specparser_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestSpecParser(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "SpecParser Suite")
}
