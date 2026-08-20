package configserver_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestConfigServer(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Config Server Suite")
}
