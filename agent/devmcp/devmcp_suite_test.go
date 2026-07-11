package devmcp_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestDevMCP(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "DevMCP Suite")
}
