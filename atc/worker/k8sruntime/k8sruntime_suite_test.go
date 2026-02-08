package k8sruntime_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestK8sRuntime(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "K8s Runtime Suite")
}
