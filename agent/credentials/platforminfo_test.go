package credentials_test

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/concourse/concourse/agent/credentials"
)

func TestImageVersionSkew(t *testing.T) {
	for _, tc := range []struct {
		name       string
		imageRef   string
		webVersion string
		skew       bool
		known      bool
	}{
		{
			name:     "matching family",
			imageRef: "registry.home/agent-runner:v0.2.195", webVersion: "0.2.195",
			skew: false, known: true,
		},
		{
			name:     "older (the ticket #45 incident: 28 releases behind)",
			imageRef: "registry.home/agent-runner:v0.2.167", webVersion: "0.2.195",
			skew: true, known: true,
		},
		{
			name:     "older minor family",
			imageRef: "registry.home/agent-runner:v0.1.9", webVersion: "0.2.0",
			skew: true, known: true,
		},
		{
			name:     "newer image is not a lag",
			imageRef: "registry.home/agent-runner:v0.3.0", webVersion: "0.2.195",
			skew: false, known: true,
		},
		{
			name:     "latest tag is unknown, never an error",
			imageRef: "registry.home/agent-runner:latest", webVersion: "0.2.195",
			skew: false, known: false,
		},
		{
			name:     "digest pin is unknown",
			imageRef: "registry.home/agent-runner@sha256:deadbeef", webVersion: "0.2.195",
			skew: false, known: false,
		},
		{
			name:     "tag plus digest pin is still unknown (the digest wins)",
			imageRef: "registry.home/agent-runner:v0.2.167@sha256:deadbeef", webVersion: "0.2.195",
			skew: false, known: false,
		},
		{
			name:     "garbage tag is unknown",
			imageRef: "registry.home/agent-runner:banana", webVersion: "0.2.195",
			skew: false, known: false,
		},
		{
			name:     "no tag at all is unknown",
			imageRef: "registry.home/agent-runner", webVersion: "0.2.195",
			skew: false, known: false,
		},
		{
			name:     "registry port colon is not a tag",
			imageRef: "registry.home:5000/agent-runner", webVersion: "0.2.195",
			skew: false, known: false,
		},
		{
			name:     "registry port with a real tag still parses",
			imageRef: "registry.home:5000/agent-runner:v0.2.100", webVersion: "0.2.195",
			skew: true, known: true,
		},
		{
			name:     "tag without the v prefix",
			imageRef: "registry.home/agent-runner:0.2.100", webVersion: "0.2.195",
			skew: true, known: true,
		},
		{
			name:     "empty image (flag unset) is unknown",
			imageRef: "", webVersion: "0.2.195",
			skew: false, known: false,
		},
		{
			name:     "unparseable web version is unknown",
			imageRef: "registry.home/agent-runner:v0.2.167", webVersion: "garbage",
			skew: false, known: false,
		},
		{
			name:     "dev web build (0.0.0-dev) never warns",
			imageRef: "registry.home/agent-runner:v0.2.167", webVersion: "0.0.0-dev",
			skew: false, known: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			skew, known := credentials.ImageVersionSkew(tc.imageRef, tc.webVersion)
			if skew != tc.skew || known != tc.known {
				t.Fatalf("ImageVersionSkew(%q, %q) = (skew=%v, known=%v); want (skew=%v, known=%v)",
					tc.imageRef, tc.webVersion, skew, known, tc.skew, tc.known)
			}
		})
	}
}

func TestPlatformInfoHandlerPopulatesFields(t *testing.T) {
	h := credentials.PlatformInfoHandler(credentials.NewPlatformInfo(
		"registry.home/agent-runner:v0.2.167", "0.2.195",
	))

	req := httptest.NewRequest("GET", "/api/v1/agent/platform-info", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["agent_step_image"] != "registry.home/agent-runner:v0.2.167" {
		t.Fatalf("agent_step_image: %v", body["agent_step_image"])
	}
	if body["web_version"] != "0.2.195" {
		t.Fatalf("web_version: %v", body["web_version"])
	}
	if body["image_version_known"] != true {
		t.Fatalf("image_version_known: %v", body["image_version_known"])
	}
	if body["image_version_skew"] != true {
		t.Fatalf("image_version_skew: %v", body["image_version_skew"])
	}
}

func TestPlatformInfoHandlerUnknownTagNeverErrors(t *testing.T) {
	h := credentials.PlatformInfoHandler(credentials.NewPlatformInfo(
		"registry.home/agent-runner:latest", "0.2.195",
	))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/agent/platform-info", nil))

	if rec.Code != 200 {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	var info credentials.PlatformInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.ImageVersionKnown || info.ImageVersionSkew {
		t.Fatalf("latest tag must report unknown, no skew: %+v", info)
	}
}
