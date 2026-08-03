package behavioral_test

import (
	"archive/tar"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"log"

	workflowrunsapi "github.com/concourse/concourse/agent/api/workflowruns"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc/db"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"
)

var _ = Describe("Agentic workflows", func() {
	// taskImageDigest returns the sha256 digest a workflow task must pin its
	// image to. Schema v3 requires an explicit immutable version
	// (agent/workflow/extract.go, "task image resource requires an explicit
	// immutable version"), so a bare `repository: busybox` is refused at import
	// with 422 — the platform enforcing reproducibility, not a defect.
	//
	// It has to be a real digest rather than the placeholder the shipped seeds
	// carry, because this workflow is actually executed: the task pod pulls the
	// image. Reusing the artifact helper's resolution keeps that digest tied to
	// the same busybox the suite already loads into the cluster, so it resolves
	// from the local containerd store instead of depending on a registry fetch.
	taskImageDigest := func() string {
		reference := resolvedArtifactHelperImage()
		at := strings.LastIndex(reference, "@")
		if at < 0 {
			log.Fatalf("artifact helper reference %q carries no digest", reference)
		}
		return reference[at+1:]
	}

	It("runs a versioned task function over an immutable snapshot and seals its output", func() {
		inputDir := filepath.Join(tmp, "agentic-input")
		Expect(os.MkdirAll(inputDir, 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(inputDir, "payload.txt"), []byte("immutable input\n"), 0o644)).To(Succeed())

		create := fly.Start(
			"agent", "snapshots", "create",
			"--type=opaque/v1", "--from="+inputDir, "--json",
		)
		Eventually(create).Should(gexec.Exit(0))
		var input snapshot.Snapshot
		Expect(json.Unmarshal(create.Out.Contents(), &input)).To(Succeed())
		Expect(input.ID.Validate()).To(Succeed())
		Expect(input.ContentState).To(Equal(snapshot.ContentStateAvailable))

		workflowName := pipelineName + "-snapshot-copy"
		workflowDir := filepath.Join(tmp, "agentic-workflow")
		Expect(os.MkdirAll(workflowDir, 0o755)).To(Succeed())
		definition := fmt.Sprintf(`schema_version: 3
name: %s
signature_version: 1
description: Copy one immutable tree through an ordinary deterministic task.
inputs:
  - name: source
    type: opaque/v1
outputs:
  - name: result
    type: opaque/v1
    from: result
plan:
  - task: copy
    function_id: copy
    config:
      platform: linux
      image_resource:
        type: registry-image
        source: {repository: busybox}
        version: {digest: %s}
      inputs: [{name: source}]
      outputs: [{name: result}]
      run:
        path: sh
        args: ["-ec", "cp -R source/. result/"]
    input_types:
      source: {type: opaque/v1}
    output_types:
      result: opaque/v1
`, workflowName, taskImageDigest())
		Expect(os.WriteFile(filepath.Join(workflowDir, "workflow.yml"), []byte(definition), 0o644)).To(Succeed())

		imported := fly.Start("agent", "workflows", "import", workflowDir, "--set-live")
		Eventually(imported).Should(gexec.Exit(0))
		Expect(string(imported.Out.Contents())).To(ContainSubstring("is now live"))

		run := fly.Start(
			"agent", "workflows", "run", workflowName,
			"--input=source="+input.ID.String(), "--wait", "--json",
		)
		Eventually(run).Should(gexec.Exit(0))
		var detail workflowrunsapi.RunDetail
		Expect(json.Unmarshal(run.Out.Contents(), &detail)).To(Succeed())
		Expect(detail.Status).To(Equal(db.AgentWorkflowRunStatusSucceeded))
		Expect(detail.WorkflowRunID.Validate()).To(Succeed())
		Expect(detail.Outputs).To(HaveLen(1))
		Expect(detail.Outputs[0].Port).To(Equal("result"))
		Expect(detail.Outputs[0].Snapshot.Type.String()).To(Equal("opaque/v1"))
		Expect(detail.Outputs[0].Snapshot.ContentState).To(Equal(snapshot.ContentStateAvailable))

		download := filepath.Join(tmp, "agentic-result.tar")
		fly.Run(
			"agent", "snapshots", "download",
			detail.Outputs[0].Snapshot.ID.String(), "--to="+download,
		)
		Expect(readTarFile(download, "payload.txt")).To(Equal("immutable input\n"))
	})

	// This is the only tier that can prove the deployed image can seal a
	// repository/v1 at all.
	//
	// repository/v1 validation execs git INSIDE the web process
	// (agent/snapshot/contracts/repository.go: rev-parse, rev-list, fsck,
	// status). Until 2026-08-01 no shipped runtime image installed git, so
	// every seal failed on the cluster while every unit test stayed green —
	// they run on dev hosts that happen to have git. Asserting the package
	// list in a Dockerfile only proves a string is present in a file; this
	// proves the capability on the image that actually ships.
	//
	// Nothing here is agent- or model-dependent: it is a pure server-side
	// validation path, so it costs no tokens and cannot flake on a model.
	It("seals a repository/v1 snapshot, proving the deployed image can run git", func() {
		repoDir := filepath.Join(tmp, "agentic-repository")
		Expect(os.MkdirAll(repoDir, 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("seal me\n"), 0o644)).To(Succeed())

		// A plain init plus one commit satisfies every rule the contract
		// enforces: a real .git directory, complete (non-shallow) history, a
		// HEAD resolving to a full commit, no submodule gitlinks, and a clean
		// work tree and index.
		for _, args := range [][]string{
			{"init", "--quiet"},
			{"config", "user.email", "behavioral@concourse.test"},
			{"config", "user.name", "Behavioral Suite"},
			{"add", "README.md"},
			{"commit", "--quiet", "-m", "seed"},
		} {
			command := exec.Command("git", args...)
			command.Dir = repoDir
			output, err := command.CombinedOutput()
			Expect(err).ToNot(HaveOccurred(), "git %s: %s", strings.Join(args, " "), output)
		}

		create := fly.Start(
			"agent", "snapshots", "create",
			"--type=repository/v1", "--from="+repoDir, "--json",
		)
		Eventually(create).Should(gexec.Exit(0),
			"sealing repository/v1 failed. If the server reports a git failure, the deployed "+
				"runtime image is missing git — see deploy/runtime_image_parity_test.go")

		var sealed snapshot.Snapshot
		Expect(json.Unmarshal(create.Out.Contents(), &sealed)).To(Succeed())
		Expect(sealed.ID.Validate()).To(Succeed())
		Expect(sealed.Type.String()).To(Equal("repository/v1"))
		Expect(sealed.ContentState).To(Equal(snapshot.ContentStateAvailable))

		// The seal is only meaningful if the server actually inspected the
		// repository rather than storing the bytes opaquely, so require the
		// derived metadata git produced.
		show := fly.Start("agent", "snapshots", "show", sealed.ID.String(), "--json")
		Eventually(show).Should(gexec.Exit(0))
		Expect(show.Out.Contents()).To(ContainSubstring("head_sha"))
	})
})

func readTarFile(path, name string) string {
	GinkgoHelper()
	file, err := os.Open(path)
	Expect(err).ToNot(HaveOccurred())
	defer file.Close()

	reader := tar.NewReader(file)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			Fail(fmt.Sprintf("%s is absent from %s", name, path))
			return ""
		}
		Expect(err).ToNot(HaveOccurred())
		if strings.TrimPrefix(header.Name, "./") != name {
			continue
		}
		contents, err := io.ReadAll(reader)
		Expect(err).ToNot(HaveOccurred())
		return string(contents)
	}
}
