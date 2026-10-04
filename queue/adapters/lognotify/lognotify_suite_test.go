package lognotify_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestLognotify(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Queue Log Notifier Suite")
}
