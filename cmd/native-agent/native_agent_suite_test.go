package main

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestNativeAgent(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Native Agent Suite")
}
