package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("queue health --source", func() {
	var file string
	ctx := context.Background()
	BeforeEach(func() {
		dir := GinkgoT().TempDir()
		remote := filepath.Join(dir, "remote.git")
		Expect(exec.Command("git", "init", "-q", "--bare", remote).Run()).To(Succeed())
		file = filepath.Join(dir, "queue.yaml")
		Expect(os.WriteFile(file, fmt.Appendf(nil, sample, remote, dir), 0o600)).To(Succeed())
	})

	It("Health reads the resource's source, from a file or on stdin, as well as a config file", func() {
		cfg, err := os.ReadFile(file)
		Expect(err).NotTo(HaveOccurred())
		src, err := json.Marshal(map[string]any{"source": map[string]string{"mode": "candidate", "config": string(cfg)}})
		Expect(err).NotTo(HaveOccurred())
		srcFile := filepath.Join(GinkgoT().TempDir(), "source.json")
		Expect(os.WriteFile(srcFile, src, 0o600)).To(Succeed())
		var o, errw bytes.Buffer
		Expect(run(ctx, []string{"health", "--source", srcFile}, &o, &errw)).To(Equal(0), errw.String())
		Expect(o.String()).To(Equal("healthy\n"))
		stdin := os.Stdin
		DeferCleanup(func() { os.Stdin = stdin })
		os.Stdin, err = os.Open(srcFile)
		Expect(err).NotTo(HaveOccurred())
		o.Reset()
		Expect(run(ctx, []string{"health"}, &o, &errw)).To(Equal(0), errw.String())
		Expect(o.String()).To(Equal("healthy\n"))
		o.Reset()
		Expect(run(ctx, []string{"health", "--config", file}, &o, &errw)).To(Equal(0), errw.String())
		Expect(o.String()).To(Equal("healthy\n"))
		o.Reset()
		Expect(run(ctx, []string{"health", "--source", srcFile, "--config", file}, &o, &errw)).NotTo(BeElementOf(0, 3))
		Expect(o.String()).To(BeEmpty())
	})
})
