package credhub_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestCredHub(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "CredHub Suite")
}
