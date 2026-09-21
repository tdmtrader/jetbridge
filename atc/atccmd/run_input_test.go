package atccmd

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/atc/runs"
)

// Narrow key-file validation. Upload, authorization, expiry and HTTP behavior
// run against real dependencies in Brine's run-input-* features.
func TestRunInputSigningKeyIsReadAndSeparatedAtStartup(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, key []byte) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, key, 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	valid := write("web.key", bytes.Repeat([]byte{0x51}, 32))
	cmd := &RunCommand{RunInputSigningKey: valid}
	cmd.Kubernetes.Namespace = "default"
	cmd.Kubernetes.OutputPlaneEnabled = true
	cmd.Kubernetes.OutputCaptureEnabled = true
	if err := cmd.validateRunInputSigningKey(); err != nil || cmd.runInputAuthority == nil {
		t.Fatalf("valid independent key was not loaded: %v", err)
	}
	for name, path := range map[string]string{
		"missing": filepath.Join(dir, "absent.key"),
		"short":   write("short.key", bytes.Repeat([]byte{0x52}, 31)),
		"long":    write("long.key", bytes.Repeat([]byte{0x53}, 33)),
	} {
		t.Run(name, func(t *testing.T) {
			cmd.RunInputSigningKey = path
			if err := cmd.validateRunInputSigningKey(); err == nil || cmd.runInputAuthority != nil {
				t.Fatal("invalid key retained signing authority")
			}
		})
	}
	cmd.RunInputSigningKey = valid
	cmd.Kubernetes.OutputMaterializationKey = write("same-material.key", bytes.Repeat([]byte{0x51}, 32))
	if err := cmd.validateRunInputSigningKey(); err == nil || cmd.runInputAuthority != nil {
		t.Fatal("node-owned key material could mint Run input grants")
	}
	cmd.Kubernetes.OutputMaterializationKey = write("different.key", bytes.Repeat([]byte{0x54}, 32))
	if err := cmd.validateRunInputSigningKey(); err != nil {
		t.Fatal(err)
	}
	cmd.RunInputSigningKey = ""
	if err := cmd.validateRunInputSigningKey(); err != nil || cmd.runInputAuthority != nil {
		t.Fatal("omitting the key retained upload authority")
	}
}

// The review worker image is an operator choice, not a template author's.
// Unset, the credential handoff admits no image at all.
func TestRunCredentialWorkerImagesArePinnedAtStartup(t *testing.T) {
	pinned := "registry.example/review-worker@sha256:" + strings.Repeat("ab", 32)

	unset := &RunCommand{}
	if err := unset.validateRunCredentialWorkerImages(); err != nil {
		t.Fatalf("an unset pin must start (and refuse delivery), got %v", err)
	}
	if err := unset.credentialHandoffConfig(nil).AdmitWorkerImage("docker:///" + pinned); !errors.Is(err, runs.ErrCredentialDelivery) {
		t.Fatalf("unset pin admitted delivery: %v", err)
	}

	set := &RunCommand{RunCredentialWorkerImages: []string{pinned}}
	if err := set.validateRunCredentialWorkerImages(); err != nil {
		t.Fatalf("digest pin refused: %v", err)
	}
	config := set.credentialHandoffConfig(nil)
	if err := config.AdmitWorkerImage("docker:///" + pinned); err != nil {
		t.Fatalf("pinned image refused: %v", err)
	}
	if config.Helper != "/usr/local/bin/jb-review-worker" || config.Socket != "/dev/shm/jb-review/auth.sock" {
		t.Fatalf("handoff helper wiring changed: %+v", config)
	}

	for _, image := range []string{"registry.example/review-worker:latest", "docker:///" + pinned, ""} {
		cmd := &RunCommand{RunCredentialWorkerImages: []string{pinned, image}}
		if err := cmd.validateRunCredentialWorkerImages(); err == nil {
			t.Errorf("startup accepted credential worker image %q", image)
		}
	}
}
