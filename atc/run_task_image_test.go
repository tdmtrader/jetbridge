package atc_test

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/concourse/concourse/atc"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/yaml"
)

// A credential is delivered into the Pod of the task producing a Run result.
// What runs there is the task's image, and anything else able to put a
// container in that Pod.
var _ = Describe("RunTaskImage", func() {
	const taskID = "e5c35b74-c6ad-4ad5-b4bd-8a703759301d"
	const pinned = "docker:///registry.example/review-worker@sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	installed := func() atc.Config {
		GinkgoHelper()
		data, err := os.ReadFile(filepath.Join("..", "deploy", "review-template.yml"))
		Expect(err).NotTo(HaveOccurred())
		text := strings.NewReplacer(
			"((review_worker_image))", strings.TrimPrefix(pinned, "docker:///"),
			"((review_model))", "some-model",
		).Replace(string(data))
		var config atc.Config
		Expect(yaml.Unmarshal([]byte(text), &config)).To(Succeed())
		return config
	}
	producer := func(config atc.Config) *atc.TaskStep {
		return config.Jobs[0].PlanSequence[0].Config.(*atc.TaskStep)
	}

	It("is the installed review template's rootfs_uri", func() {
		Expect(atc.RunTaskImage(installed(), taskID)).To(Equal(pinned))
	})

	It("is empty when an image artifact replaces it", func() {
		config := installed()
		producer(config).ImageArtifactName = "attacker-image"
		Expect(atc.RunTaskImage(config, taskID)).To(BeEmpty())
	})

	It("is empty when an image_resource supplies it", func() {
		config := installed()
		producer(config).Config.ImageResource = &atc.ImageResource{Type: "registry-image", Source: atc.Source{"repository": "attacker"}}
		Expect(atc.RunTaskImage(config, taskID)).To(BeEmpty())
	})

	It("is empty when a sidecar shares the Pod", func() {
		config := installed()
		producer(config).Sidecars = []atc.SidecarSource{{File: "sidecar.yml"}}
		Expect(atc.RunTaskImage(config, taskID)).To(BeEmpty())
	})

	It("is empty for a task the config does not declare", func() {
		Expect(atc.RunTaskImage(installed(), "00000000-0000-4000-8000-000000000000")).To(BeEmpty())
	})
})
