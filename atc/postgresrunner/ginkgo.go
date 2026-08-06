package postgresrunner

import (
	"context"
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type none struct{}

func GinkgoRunner(runner *Runner) none {
	SynchronizedBeforeSuite(
		func() []byte { return InitializeRunnerForGinkgo(runner) },
		func(data []byte) { SynchronizeRunnerForGinkgo(runner, data) },
	)
	SynchronizedAfterSuite(
		func() { CleanupRunnerForGinkgo(runner) },
		func() { FinalizeRunnerForGinkgo(runner) },
	)
	return none{}
}

func InitializeRunnerForGinkgo(runner *Runner) []byte {
	config, err := runner.CreateSuiteTemplate(context.Background())
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	data, err := marshalSuiteConfig(config)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	return data
}

func SynchronizeRunnerForGinkgo(runner *Runner, data []byte) {
	config, err := unmarshalSuiteConfig(data)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	err = runner.AdoptSuiteConfig(config, GinkgoParallelProcess())
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
}

func CleanupRunnerForGinkgo(runner *Runner) {
	ExpectWithOffset(1, runner.CleanupProcess(context.Background())).To(Succeed())
}

func FinalizeRunnerForGinkgo(runner *Runner) {
	ExpectWithOffset(1, runner.CleanupSuite(context.Background())).To(Succeed())
}

func marshalSuiteConfig(config SuiteConfig) ([]byte, error) {
	if err := validateSuiteConfig(config); err != nil {
		return nil, err
	}
	return json.Marshal(config)
}

func unmarshalSuiteConfig(data []byte) (SuiteConfig, error) {
	var config SuiteConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return SuiteConfig{}, err
	}
	if err := validateSuiteConfig(config); err != nil {
		return SuiteConfig{}, err
	}
	return config, nil
}

func validateSuiteConfig(config SuiteConfig) error {
	var runner Runner
	return runner.AdoptSuiteConfig(config, 1)
}
