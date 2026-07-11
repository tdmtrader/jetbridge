package atc_test

import (
	"github.com/concourse/concourse/atc"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Template pipeline config", func() {
	It("round-trips template, params and run_retention through UnmarshalConfig", func() {
		payload := []byte(`
template: true
params:
- name: commit
  type: string
  required: true
- name: suite
  type: enum
  values: [unit, integration]
  default: unit
run_retention:
  keep_last: 5
  ttl_days: 7
jobs:
- name: entry
  plan:
  - task: t
    file: task.yml
`)
		var config atc.Config
		err := atc.UnmarshalConfig(payload, &config)
		Expect(err).ToNot(HaveOccurred())
		Expect(config.Template).To(BeTrue())
		Expect(config.Params).To(HaveLen(2))
		Expect(config.Params[0].Name).To(Equal("commit"))
		Expect(config.Params[0].Type).To(Equal("string"))
		Expect(config.Params[0].Required).To(BeTrue())
		Expect(config.Params[1].Values).To(Equal([]string{"unit", "integration"}))
		Expect(config.Params[1].Default).To(Equal("unit"))
		Expect(config.RunRetention.KeepLast).To(Equal(5))
		Expect(config.RunRetention.TTLDays).To(Equal(7))
	})
})
