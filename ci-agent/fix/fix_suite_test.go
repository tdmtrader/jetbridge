package fix_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestFix(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "CI Agent Fix Suite")
}
