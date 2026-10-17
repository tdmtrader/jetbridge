package steps

import (
	"crypto/x509"
	"encoding/pem"
	"net"
	"testing"
)

// The disposable kubelet's Pods and the ATC dial the node's artifact daemon at
// the node's InternalIP. Two things have to hold for that dial to land, and
// both broke silently once already: the daemon must bind every address (the
// runtime used to rewrite a --listen flag the daemon no longer takes, and
// failed every scenario at its Given), and its certificate must name the
// address dialled, because the ATC verifies against it.

func TestTheHangarDaemonArgvBindsEveryAddress(t *testing.T) {
	args := append([]string{"artifact-daemon"}, addressableArgs("artifact-daemon", "127.0.0.1", 7780, "/node")...)
	if address := daemonListenAddress(args); address != "" {
		t.Fatalf("the default launch holds %q; the node's Pods could not reach it", address)
	}

	for _, held := range [][]string{
		append(append([]string{}, args...), "--listen-address", "127.0.0.2"),
		append(append([]string{}, args...), "--listen-address=127.0.0.2"),
	} {
		if address := daemonListenAddress(held); address != "127.0.0.2" {
			t.Fatalf("argv %q holds 127.0.0.2, but daemonListenAddress said %q", held, address)
		}
	}
}

func TestTheHangarDaemonCertificateNamesTheNodeAddress(t *testing.T) {
	nodeIP := net.ParseIP("10.42.7.9")
	material, err := hangarDaemonMaterial(nodeIP)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(material.serverCert)
	if block == nil {
		t.Fatal("the server certificate is not PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"10.42.7.9", "127.0.0.1", "artifact-daemon"} {
		if err := cert.VerifyHostname(name); err != nil {
			t.Errorf("the daemon's certificate does not cover %s: %v", name, err)
		}
	}
	if err := cert.VerifyHostname("10.42.7.10"); err == nil {
		t.Error("the daemon's certificate covers an address it was not minted for")
	}
}
