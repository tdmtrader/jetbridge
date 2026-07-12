package exec_test

import (
	"strings"

	"github.com/concourse/concourse/atc/exec"
	"github.com/onsi/gomega/gbytes"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The observer is the server-side half of the self-reported-cost fix (review
// finding, 2026-07-12): it watches the agent process's LIVE stdout for the
// claude CLI envelope and keeps a per-field maximum that ingestion uses as a
// floor under the pod-written (attacker-writable) flight recorder.
var _ = Describe("agentCostObserver", func() {
	var (
		dst      *gbytes.Buffer
		observer interface {
			Write([]byte) (int, error)
			Observed() exec.ObservedAgentCost
		}
	)

	envelope := `{"type":"result","subtype":"success","is_error":false,"result":"done","model":"m1","num_turns":9,"total_cost_usd":0.42,"usage":{"input_tokens":100,"output_tokens":50,"cache_read_input_tokens":1,"cache_creation_input_tokens":2}}`

	BeforeEach(func() {
		dst = gbytes.NewBuffer()
		observer = exec.NewAgentCostObserver(dst)
	})

	write := func(s string) {
		n, err := observer.Write([]byte(s))
		Expect(err).ToNot(HaveOccurred())
		Expect(n).To(Equal(len(s)))
	}

	It("tees every byte through to the underlying writer unchanged", func() {
		write("plain log line\n" + envelope + "\n")
		Expect(string(dst.Contents())).To(Equal("plain log line\n" + envelope + "\n"))
	})

	It("extracts the envelope's ledger-relevant counters", func() {
		write("agent-runner: starting\n" + envelope + "\n")
		obs := observer.Observed()
		Expect(obs.CostUSD).To(BeNumerically("~", 0.42, 1e-9))
		Expect(obs.InputTokens).To(Equal(int64(100)))
		Expect(obs.OutputTokens).To(Equal(int64(50)))
		Expect(obs.CacheReadTokens).To(Equal(int64(1)))
		Expect(obs.CacheCreationTokens).To(Equal(int64(2)))
		Expect(obs.Turns).To(Equal(9))
		Expect(obs.Model).To(Equal("m1"))
	})

	It("reassembles an envelope split across multiple writes", func() {
		mid := len(envelope) / 2
		write(envelope[:mid])
		write(envelope[mid:])
		write("\n")
		Expect(observer.Observed().CostUSD).To(BeNumerically("~", 0.42, 1e-9))
	})

	It("parses a final envelope with no trailing newline (claude may exit without one)", func() {
		write(envelope)
		Expect(observer.Observed().CostUSD).To(BeNumerically("~", 0.42, 1e-9))
	})

	It("tolerates TTY \\r\\n line endings", func() {
		write(envelope + "\r\n")
		Expect(observer.Observed().Turns).To(Equal(9))
	})

	It("keeps the floor at the per-field MAX so a forged later zero-cost envelope cannot lower it", func() {
		// The tamper shape from the finding: a leaked descendant prints a fake
		// zero-cost envelope after the real one (last-line-wins would take it —
		// the runner's parseEnvelope does exactly that in-pod).
		fake := `{"type":"result","model":"m1","num_turns":1,"total_cost_usd":0,"usage":{"input_tokens":0,"output_tokens":0}}`
		write(envelope + "\n" + fake + "\n")
		obs := observer.Observed()
		Expect(obs.CostUSD).To(BeNumerically("~", 0.42, 1e-9))
		Expect(obs.InputTokens).To(Equal(int64(100)))
		Expect(obs.Turns).To(Equal(9))
	})

	It("ignores non-JSON, non-result, and malformed lines (fail open)", func() {
		write("some tool output\n")
		write(`{"event":"tool.call","data":{}}` + "\n")    // JSON but not a result envelope
		write(`{"type":"result","total_cost_usd":` + "\n") // malformed
		obs := observer.Observed()
		Expect(obs.CostUSD).To(BeZero())
		Expect(obs.Turns).To(BeZero())
	})

	It("drops oversized lines un-parsed instead of buffering them (fail open)", func() {
		// A single line beyond the 5MB bound must not be held in memory or
		// parsed — and must not corrupt parsing of subsequent lines.
		write(`{"type":"result","total_cost_usd":9.99,"result":"`)
		huge := strings.Repeat("x", (5<<20)+1)
		write(huge)
		write(`"}` + "\n")
		write(envelope + "\n")
		obs := observer.Observed()
		Expect(obs.CostUSD).To(BeNumerically("~", 0.42, 1e-9)) // only the sane envelope counted
	})

	It("falls back to cost_usd when total_cost_usd is absent (older CLIs)", func() {
		write(`{"type":"result","model":"m1","cost_usd":0.07,"num_turns":2,"usage":{"input_tokens":10,"output_tokens":5}}` + "\n")
		Expect(observer.Observed().CostUSD).To(BeNumerically("~", 0.07, 1e-9))
	})
})
