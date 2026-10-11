package main

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestQueueCommand(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Queue Command Suite")
}
