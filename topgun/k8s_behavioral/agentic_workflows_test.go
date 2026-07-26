package behavioral_test

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	workflowrunsapi "github.com/concourse/concourse/agent/api/workflowruns"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
	"github.com/concourse/concourse/atc/db"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"
)

var _ = Describe("Agentic workflows", func() {
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
      inputs: [{name: source}]
      outputs: [{name: result}]
      run:
        path: sh
        args: ["-ec", "cp -R source/. result/"]
    input_types:
      source: {type: opaque/v1}
    output_types:
      result: opaque/v1
`, workflowName)
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

	It("runs a real agent node that seals a review record through the fixture agent", func() {
		subjectDir := filepath.Join(tmp, "agent-node-subject")
		Expect(os.MkdirAll(subjectDir, 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(subjectDir, "payload.txt"), []byte("subject under review\n"), 0o644)).To(Succeed())

		create := fly.Start("agent", "snapshots", "create", "--type=opaque/v1", "--from="+subjectDir, "--json")
		Eventually(create).Should(gexec.Exit(0))
		var subject snapshot.Snapshot
		Expect(json.Unmarshal(create.Out.Contents(), &subject)).To(Succeed())
		Expect(subject.ID.Validate()).To(Succeed())

		// An ordinary pipeline, not a workflow: a workflow containing an
		// agent node would require --agent-step-image to be an exact @sha256
		// digest (agent/workflowrun/binder.go), which a locally built image
		// cannot supply. The claim under test is that a real agent: node runs,
		// seals, and its bytes come back — not how workflows pin images.
		pipeline := fmt.Sprintf(`---
jobs:
- name: fixture-review
  plan:
  - load_snapshot: change
    id: "%s"
    type: opaque/v1
  - agent: review
    prompt: The fixture agent ignores this prompt.
    inputs: [change]
    outputs: [review]
    input_types:
      change: {type: opaque/v1}
    output_types:
      review: review/v1
    env:
      FIXTURE_CASE: review-accept
      FIXTURE_SUBJECT_INPUT: change
`, subject.ID.String())
		pipelinePath := filepath.Join(tmp, "agent-node-pipeline.yml")
		Expect(os.WriteFile(pipelinePath, []byte(pipeline), 0o644)).To(Succeed())

		fly.Run("set-pipeline", "-n", "-p", pipelineName, "-c", pipelinePath)
		fly.Run("unpause-pipeline", "-p", pipelineName)

		trigger := fly.Start("trigger-job", "-j", pipelineName+"/fixture-review", "-w")
		Eventually(trigger, 10*time.Minute).Should(gexec.Exit(0))

		// The seal committed a review/v1 for this team; there is exactly one,
		// because each spec gets a fresh randomly named pipeline and a fresh
		// subject snapshot.
		list := fly.Start("agent", "snapshots", "list", "--type=review/v1", "--json")
		Eventually(list).Should(gexec.Exit(0))
		var reviews []snapshot.Snapshot
		Expect(json.Unmarshal(list.Out.Contents(), &reviews)).To(Succeed())
		Expect(reviews).NotTo(BeEmpty())
		sealed := reviews[0]
		Expect(sealed.Type.String()).To(Equal("review/v1"))
		Expect(sealed.ContentState).To(Equal(snapshot.ContentStateAvailable))

		download := filepath.Join(tmp, "agent-node-review.tar")
		fly.Run("agent", "snapshots", "download", sealed.ID.String(), "--to="+download)

		// The record the fixture wrote, bound to the exact subject the platform
		// exposed to it — and the archive's bytes hash to the digest the seal
		// committed, which is the whole content-addressing claim.
		var record contracts.Record[contracts.ReviewBody]
		Expect(json.Unmarshal([]byte(readTarFile(download, "record.json")), &record)).To(Succeed())
		Expect(record.Type.String()).To(Equal("review/v1"))
		Expect(record.Body.Conclusion).To(Equal("accept"))
		Expect(record.Subjects).To(HaveLen(1))
		Expect(record.Subjects[0].Input).To(Equal("change"))
		Expect(record.Subjects[0].Digest).To(Equal(subject.Digest))

		raw, err := os.ReadFile(download)
		Expect(err).NotTo(HaveOccurred())
		sum := sha256.Sum256(raw)
		Expect(snapshot.Digest("sha256:" + hex.EncodeToString(sum[:]))).To(Equal(sealed.Digest))
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
