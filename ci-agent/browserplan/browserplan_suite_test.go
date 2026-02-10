package browserplan_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestBrowserPlan(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "BrowserPlan Suite")
}
