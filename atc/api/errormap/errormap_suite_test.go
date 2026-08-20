package errormap_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestErrorMap(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Mutation Error Map Suite")
}
