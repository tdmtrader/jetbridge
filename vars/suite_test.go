package vars_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	. "github.com/concourse/concourse/vars"
)

func TestReg(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "director/template")
}

type errorVariables struct {
	err error
}

func (v errorVariables) Get(Reference) (any, bool, error) {
	return nil, false, v.err
}

func (v errorVariables) List() ([]Reference, error) {
	return nil, v.err
}
