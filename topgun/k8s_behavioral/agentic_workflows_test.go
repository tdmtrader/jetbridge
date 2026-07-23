package behavioral_test

import (
	"archive/tar"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	workflowrunsapi "github.com/concourse/concourse/agent/api/workflowruns"
	"github.com/concourse/concourse/agent/snapshot"
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
