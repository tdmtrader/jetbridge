package core_test

import (
	"encoding/json"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/queue/core"
)

var _ = Describe("Stats", func() {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	ago := func(m int) time.Time { return now.Add(-time.Duration(m) * time.Minute) }
	rec := func(id string, k core.EventKind, at, adm time.Time, cause string) core.SettleRecord {
		return core.SettleRecord{ID: id, Kind: k, At: at, AdmittedAt: adm, Cause: cause}
	}
	landed := func(id string, atMin, admMin int) core.SettleRecord {
		return rec(id, core.LandedEvent, ago(atMin), ago(admMin), "")
	}
	stats := func(rs ...core.SettleRecord) core.Summary {
		return core.Stats(core.Snapshot{Settled: rs}, now, time.Hour)
	}

	It("The stats count changes landed in the last hour from settle records", func() {
		s := stats(landed("a", 10, 20), landed("b", 20, 30), landed("c", 30, 40), landed("old", 120, 130))
		Expect(s.Landed).To(Equal(3))
		Expect(s.LandedPerHour).To(Equal(3.0))
	})

	It("Admitted per hour counts admissions, not rows", func() {
		s := stats(rec("a", core.PausedEvent, ago(20), ago(30), ""), rec("a", core.PausedEvent, ago(15), ago(30), ""), landed("a", 10, 30))
		Expect(s.Admitted).To(Equal(1))
	})

	It("counts a record exactly on the window start and ignores one just before it", func() {
		s := stats(landed("in", 60, 70), rec("out", core.LandedEvent, ago(60).Add(-time.Second), ago(90), ""))
		Expect(s.Landed).To(Equal(1))
	})

	It("splits ejects by cause", func() {
		e := func(id, cause string) core.SettleRecord { return rec(id, core.EjectedEvent, ago(5), ago(9), cause) }
		s := stats(e("a", core.Culprit), e("b", core.ParentEjected), e("c", "weird"), e("d", ""), rec("r", core.RefusedEvent, ago(5), time.Time{}, ""))
		Expect(s.Ejected).To(Equal(core.EjectCauses{Culprit: 1, ParentEjected: 1, Refused: 1, Other: 2}))
	})

	It("takes the median and p90 of time in queue for an odd count", func() {
		s := stats(landed("a", 5, 15), landed("b", 5, 25), landed("c", 5, 35)) // 10, 20, 30 minutes
		Expect(s.MedianQueueSeconds).To(Equal(1200.0))
		Expect(s.P90QueueSeconds).To(Equal(1800.0))
	})

	It("takes the median and p90 of time in queue for an even count", func() {
		s := stats(landed("a", 5, 15), landed("b", 5, 25), landed("c", 5, 35), landed("d", 5, 45)) // 10, 20, 30, 40
		Expect(s.MedianQueueSeconds).To(Equal(1500.0))
		Expect(s.P90QueueSeconds).To(Equal(2400.0))
	})

	It("The stats count landings and time their walk", func() {
		in := func(id, run string, adm int) core.SettleRecord {
			r := landed(id, 10, adm)
			r.Run = run
			return r
		}
		later := in("c", "r2", 20)
		later.At = ago(5)
		s := stats(in("a", "r1", 40), in("b", "r1", 30), later, landed("old", 120, 130))
		Expect(s.Landed).To(Equal(3))
		Expect(s.LandsPerHour).To(Equal(2.0))
		Expect(s.MedianWalkSeconds).To(Equal(1350.0)) // 15 and 30 minutes
		Expect(s.P90WalkSeconds).To(Equal(1800.0))
	})

	It("keys the new stats in JSON beside the old ones", func() {
		b, _ := json.Marshal(stats())
		Expect(string(b)).To(ContainSubstring(`"landed_per_hour":0,"lands_per_hour":0,"median_walk_seconds":0,"p90_walk_seconds":0`))
	})

	It("Stats count the runs that gave no verdict inside the wait cap", func() {
		capped := rec("a", core.WaitCapEvent, ago(10), ago(80), "")
		capped.Waited = time.Hour
		s := stats(capped, rec("b", core.WaitCapEvent, ago(90), ago(150), ""))
		Expect(s.WaitCapExpired).To(Equal(1))
		Expect(s.WaitCapSeconds).To(Equal(3600.0))
		b, _ := json.Marshal(s)
		Expect(string(b)).To(ContainSubstring(`"waitcap_expired":1,"waitcap_wait_seconds":3600`))
	})

	It("gives zeros for an empty snapshot", func() {
		Expect(core.Stats(core.Snapshot{}, now, time.Hour)).To(Equal(core.Summary{}))
	})

	It("counts flaky records when present and reports the queue now", func() {
		sn := core.Snapshot{Queued: []core.Entry{{ID: "q", AdmittedAt: ago(5)}}, InFlight: []core.Flight{{}}, Paused: true, Why: "main red",
			Settled: []core.SettleRecord{rec("f", "flaky", ago(5), ago(9), "")}}
		s := core.Stats(sn, now, time.Hour)
		Expect(s.Flakes).To(Equal(1))
		Expect(s.Queued).To(Equal(1))
		Expect(s.InFlight).To(Equal(1))
		Expect(s.Paused).To(BeTrue())
		Expect(s.PausedWhy).To(Equal("main red"))
		Expect(s.Admitted).To(Equal(2))
	})

	It("hides a URL password in the pause reason", func() {
		s := core.Stats(core.Snapshot{Paused: true, Why: "push https://user:SECRET@host/r refused"}, now, time.Hour)
		Expect(s.PausedWhy).To(Equal("push https://***@host/r refused"))
	})

	It("marshals with lower_snake_case keys", func() {
		b, err := json.Marshal(core.Summary{})
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(ContainSubstring(`"landed_per_hour":0`))
		Expect(string(b)).To(ContainSubstring(`"parent_ejected":0`))
	})
})
