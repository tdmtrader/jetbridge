package main_test

import (
	"encoding/json"

	"github.com/concourse/concourse/atc/postgresrunner"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"

	"testing"
)

func TestConcourse(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Concourse Suite")
}

var (
	concoursePath  string
	postgresRunner postgresrunner.Runner
)

type synchronizedSuiteConfig struct {
	ConcoursePath string `json:"concourse_path"`
	Postgres      []byte `json:"postgres"`
}

var _ = SynchronizedBeforeSuite(func() []byte {
	buildPath, err := gexec.Build("github.com/concourse/concourse/cmd/concourse")
	Expect(err).NotTo(HaveOccurred())
	data, err := json.Marshal(synchronizedSuiteConfig{
		ConcoursePath: buildPath,
		Postgres:      postgresrunner.InitializeRunnerForGinkgo(&postgresRunner),
	})
	Expect(err).NotTo(HaveOccurred())
	return data
}, func(data []byte) {
	var config synchronizedSuiteConfig
	err := json.Unmarshal(data, &config)
	Expect(err).NotTo(HaveOccurred())
	concoursePath = config.ConcoursePath
	postgresrunner.SynchronizeRunnerForGinkgo(&postgresRunner, config.Postgres)
})

var _ = SynchronizedAfterSuite(func() {
	postgresrunner.CleanupRunnerForGinkgo(&postgresRunner)
}, func() {
	postgresrunner.FinalizeRunnerForGinkgo(&postgresRunner)
	gexec.CleanupBuildArtifacts()
})
