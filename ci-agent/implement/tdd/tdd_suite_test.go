package tdd_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestTDD(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "TDD Suite")
}
