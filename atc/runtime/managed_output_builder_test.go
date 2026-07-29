package runtime

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/outputbuilder"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

func TestValidateManagedOutputBuilderRejectsUnboundAuthorityLayouts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*ContainerSpec, *outputbuilder.NodeAuthority)
	}{
		{"task container", func(spec *ContainerSpec, _ *outputbuilder.NodeAuthority) { spec.Type = db.ContainerTypeTask }},
		{"mutable image", func(spec *ContainerSpec, _ *outputbuilder.NodeAuthority) {
			spec.ImageSpec.ImageURL = "registry.example.test/agent:latest"
			spec.Sidecars[0].Image = spec.ImageSpec.ImageURL
		}},
		{"projection mismatch", func(spec *ContainerSpec, _ *outputbuilder.NodeAuthority) {
			spec.ManagedOutputBuilder.InputMountPaths = []string{"/work/forged"}
		}},
		{"work root projection", func(spec *ContainerSpec, _ *outputbuilder.NodeAuthority) {
			spec.ManagedOutputBuilder.InputMountPaths = []string{"/work"}
		}},
		{"input output root overlap", func(spec *ContainerSpec, authority *outputbuilder.NodeAuthority) {
			delete(authority.Outputs, "review")
			authority.Outputs["change"] = outputbuilder.OutputAuthority{
				Port: snapshot.Port{Name: "change", Type: "review/v1"}, MountRoot: "/work/change",
			}
			spec.Outputs = OutputPaths{"change": "/work/change"}
			spec.ManagedOutputBuilder.OutputMountPaths = []string{"/work/change"}
		}},
		{"cache overlap", func(spec *ContainerSpec, _ *outputbuilder.NodeAuthority) {
			spec.Caches = []string{"/work/review/cache"}
		}},
		{"ordinary secret overlap", func(spec *ContainerSpec, _ *outputbuilder.NodeAuthority) {
			spec.SecretMounts = []SecretMount{{SecretName: "ordinary", MountPath: "/work/review/secret"}}
		}},
		{"scratch overlap", func(spec *ContainerSpec, _ *outputbuilder.NodeAuthority) {
			spec.ScratchPaths = []string{"/work/change/scratch"}
		}},
		{"untyped input overlap", func(spec *ContainerSpec, _ *outputbuilder.NodeAuthority) {
			spec.Inputs = append(spec.Inputs, Input{DestinationPath: "/work/change/extra", ReadOnly: true})
		}},
		{"authority candidate", func(_ *ContainerSpec, authority *outputbuilder.NodeAuthority) {
			input := authority.Inputs["change"]
			input.Candidate = true
			authority.Inputs["change"] = input
		}},
		{"non-record authority output", func(_ *ContainerSpec, authority *outputbuilder.NodeAuthority) {
			output := authority.Outputs["review"]
			output.Port.Type = "opaque/v1"
			authority.Outputs["review"] = output
		}},
		{"authority root mismatch", func(_ *ContainerSpec, authority *outputbuilder.NodeAuthority) {
			output := authority.Outputs["review"]
			output.MountRoot = "/work/other"
			authority.Outputs["review"] = output
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec, authority := canonicalManagedOutputBuilderSpec(t)
			tc.mutate(&spec, &authority)
			rewriteManagedAuthority(t, &spec, authority)
			if err := ValidateManagedOutputBuilder(spec); err == nil {
				t.Fatal("expected unbound managed output builder to be rejected")
			}
		})
	}
}

func canonicalManagedOutputBuilderSpec(t *testing.T) (ContainerSpec, outputbuilder.NodeAuthority) {
	t.Helper()
	image := "registry.example.test/agent@sha256:" + strings.Repeat("a", 64)
	digest := snapshot.Digest("sha256:" + strings.Repeat("b", 64))
	authority := outputbuilder.NodeAuthority{
		WorkRoot: "/work",
		Inputs: map[string]outputbuilder.InputAuthority{"change": {
			Ref: snapshot.SnapshotRef{ID: 1, Type: "repository-change/v1", Digest: digest}, MountRoot: "/work/change", Exposure: snapshot.FullTreeExposure("/work/change", digest),
		}},
		Outputs: map[string]outputbuilder.OutputAuthority{"review": {Port: snapshot.Port{Name: "review", Type: "review/v1"}, MountRoot: "/work/review"}},
	}
	spec := ContainerSpec{
		Type: db.ContainerTypeAgent, ImageSpec: ImageSpec{ImageURL: image}, Dir: "/work",
		Inputs: []Input{{DestinationPath: "/work/change"}}, Outputs: OutputPaths{"review": "/work/review"},
		Sidecars:             []atc.SidecarConfig{{Name: ManagedOutputBuilderName, Image: image, Command: []string{"/usr/local/bin/agent-output", "serve"}, Ports: []atc.SidecarPort{{ContainerPort: 7783, Protocol: "TCP"}}, WorkingDir: "/"}},
		ManagedOutputBuilder: &ManagedOutputBuilder{Authority: PrivateFileMount{MountPath: ManagedOutputBuilderAuthorityMountRoot, Files: map[string][]byte{}}, InputMountPaths: []string{"/work/change"}, OutputMountPaths: []string{"/work/review"}},
	}
	rewriteManagedAuthority(t, &spec, authority)
	return spec, authority
}

func rewriteManagedAuthority(t *testing.T, spec *ContainerSpec, authority outputbuilder.NodeAuthority) {
	t.Helper()
	raw, err := json.Marshal(authority)
	if err != nil {
		t.Fatal(err)
	}
	spec.ManagedOutputBuilder.Authority.Files[ManagedOutputBuilderAuthorityFile] = raw
}
