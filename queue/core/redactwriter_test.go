package core_test

import (
	"bytes"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/queue/core"
)

var _ = Describe("RedactWriter", func() {
	It("hides a credential split across two writes", func() {
		var out bytes.Buffer
		w := core.NewRedactWriter(&out)
		_, _ = w.Write([]byte("fetch https://user:SEC"))
		Expect(out.String()).To(BeEmpty(), "nothing is written before the line ends")
		_, _ = w.Write([]byte("RET@host/x failed\nnext"))
		Expect(out.String()).To(Equal("fetch https://***@host/x failed\n"))
	})

	It("writes a last partial line, redacted, on flush", func() {
		var out bytes.Buffer
		w := core.NewRedactWriter(&out)
		_, _ = w.Write([]byte("tail https://user:SECRET@host"))
		Expect(w.Flush()).To(Succeed())
		Expect(out.String()).To(Equal("tail https://***@host"))
	})
})
