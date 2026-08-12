package wrappa_test

import (
	"net/http"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestWrappa(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Wrappa Suite")
}

type stupidHandler struct{}

func (stupidHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
}

type descriptiveRoute struct {
	route   string
	handler http.Handler
}
