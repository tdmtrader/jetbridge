package commands

import (
	"io"

	"github.com/concourse/concourse/atc"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// chunkReader hands out one chunk per Read, the way a pipe delivers stdin.
// It has no WriteTo, so io.Copy reads into its own buffer and reuses it.
// After copying a chunk past the first into p it closes refilled, so the test
// can look at an already-sent input only once io.Copy has read into the
// buffer again.
type chunkReader struct {
	chunks   [][]byte
	reads    int
	refilled chan struct{}
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if r.reads == len(r.chunks) {
		return 0, io.EOF
	}
	n := copy(p, r.chunks[r.reads])
	r.reads++
	if r.reads == 2 {
		close(r.refilled)
	}
	return n, nil
}

var _ = Describe("stdinWriter", func() {
	It("sends each stdin read as it was read, even after io.Copy reuses its buffer", func() {
		inputs := make(chan atc.HijackInput, 1)
		reader := &chunkReader{
			chunks:   [][]byte{[]byte("first-chunk"), []byte("SECOND")},
			refilled: make(chan struct{}),
		}

		go func() {
			defer GinkgoRecover()
			io.Copy(&stdinWriter{inputs}, reader)
			inputs <- atc.HijackInput{Closed: true}
		}()

		Eventually(reader.refilled).Should(BeClosed())

		var first, second atc.HijackInput
		Eventually(inputs).Should(Receive(&first))
		Eventually(inputs).Should(Receive(&second))

		Expect(string(first.Stdin)).To(Equal("first-chunk"))
		Expect(string(second.Stdin)).To(Equal("SECOND"))
	})
})
