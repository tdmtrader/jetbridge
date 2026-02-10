package implement_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestImplement(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Implement Suite")
}
