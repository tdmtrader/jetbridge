package main

import (
	"context"
	"os"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// TestMain runs the test binary as the resource when asked, so every call in
// the specs is a fresh process, as the CI system makes it.
func TestMain(m *testing.M) {
	if as := os.Getenv("QUEUE_RESOURCE_AS"); as != "" {
		os.Exit(entry(context.Background(), as, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

func TestQueueResource(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Queue Resource Suite")
}
