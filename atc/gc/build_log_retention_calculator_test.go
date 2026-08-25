package gc_test

import (
	"math"
	"time"

	"github.com/concourse/concourse/atc"
	. "github.com/concourse/concourse/atc/gc"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("BuildLogRetentionCalculator", func() {
	It("nothing set returns zeros", func() {
		logRetention := NewBuildLogRetentionCalculator(
			0, // default builds to retain
			0, // max builds to retain
			0, // default days to retain
			0, // max days to retain
		).BuildLogsToRetain(makeJob(
			0, // builds to retain
			0, // days to retain
			0, // min success to retain
		))
		Expect(logRetention.Builds).To(Equal(0))
		Expect(logRetention.Days).To(Equal(0))
		Expect(logRetention.MinimumSucceededBuilds).To(Equal(0))
	})
	It("no default or max set, returns job values", func() {
		logRetention := NewBuildLogRetentionCalculator(
			0, // default builds to retain
			0, // max builds to retain
			0, // default days to retain
			0, // max days to retain
		).BuildLogsToRetain(makeJob(
			3, // builds to retain
			2, // days to retain
			1, // min success to retain
		))
		Expect(logRetention.Builds).To(Equal(3))
		Expect(logRetention.Days).To(Equal(2))
		Expect(logRetention.MinimumSucceededBuilds).To(Equal(1))
	})

	It("ignores min-succeeded above the build budget, as the max branch does", func() {
		// The two branches used to disagree: the max branch bounds min-succeeded
		// by the build budget, this one copied it verbatim, and a job whose
		// min-succeeded exceeds its count then made reapLogsOfJob's
		// over-retention correction index off the front of its keep list.
		logRetention := NewBuildLogRetentionCalculator(
			0, // default builds to retain
			0, // max builds to retain
			0, // default days to retain
			0, // max days to retain
		).BuildLogsToRetain(makeJob(
			2, // builds to retain
			0, // days to retain
			5, // min success to retain
		))
		Expect(logRetention.Builds).To(Equal(2))
		Expect(logRetention.MinimumSucceededBuilds).To(BeZero())
	})
	It("ignores min-succeeded above a build budget that came from the default flag", func() {
		// This is the route configvalidate does not close: its
		// min_success_builds > builds check is gated on builds > 0, so a job that
		// declares only min_success_builds is accepted, and the count then comes
		// from --default-build-logs-to-retain.
		logRetention := NewBuildLogRetentionCalculator(
			1, // default builds to retain
			0, // max builds to retain
			0, // default days to retain
			0, // max days to retain
		).BuildLogsToRetain(makeJob(
			0, // builds to retain
			0, // days to retain
			2, // min success to retain
		))
		Expect(logRetention.Builds).To(Equal(1))
		Expect(logRetention.MinimumSucceededBuilds).To(BeZero())
	})
	It("saturates an absurd operator build-count knob instead of truncating it negative", func() {
		// The knobs are uint64 flags and the retention decision works in int, so a
		// plain conversion turns anything from 2^63 up NEGATIVE. A negative budget
		// is not a smaller budget: it reaps every build and then panics the
		// over-retention correction on index [-1].
		Expect(NewBuildLogRetentionCalculator(math.MaxUint64, 0, 0, 0).
			BuildLogsToRetain(atc.JobConfig{}).Builds).To(Equal(math.MaxInt))
		Expect(NewBuildLogRetentionCalculator(math.MaxUint64, math.MaxUint64, 0, 0).
			BuildLogsToRetain(atc.JobConfig{}).Builds).To(Equal(math.MaxInt))
		// A sane job declaration still wins over the saturated ceiling.
		Expect(NewBuildLogRetentionCalculator(0, math.MaxUint64, 0, 0).
			BuildLogsToRetain(makeJob(2, 0, 0)).Builds).To(Equal(2))
	})
	It("bounds absurd day knobs and an absurd day declaration", func() {
		// The DAYS knobs truncate exactly as the build counts do, but with a worse
		// consequence and nothing to announce it: a negative Days makes every
		// build read as expired, so the days arm reaps a job's entire history in a
		// single pass. The job's own declaration reaches the same AddDate --
		// configvalidate rejects only a NEGATIVE build_log_retention.days.
		Expect(NewBuildLogRetentionCalculator(0, 0, math.MaxUint64, 0).
			BuildLogsToRetain(atc.JobConfig{}).Days).To(Equal(MaxRetentionDays))
		Expect(NewBuildLogRetentionCalculator(0, 0, 0, math.MaxUint64).
			BuildLogsToRetain(atc.JobConfig{}).Days).To(Equal(MaxRetentionDays))
		Expect(NewBuildLogRetentionCalculator(0, 0, 0, 0).
			BuildLogsToRetain(makeJob(0, math.MaxInt, 0)).Days).To(Equal(MaxRetentionDays))
		// A sane job declaration still wins over the bounded ceiling.
		Expect(NewBuildLogRetentionCalculator(0, 0, 0, math.MaxUint64).
			BuildLogsToRetain(makeJob(0, 2, 0)).Days).To(Equal(2))
	})
	It("bounds days so the expiry it produces stays in the future and exact", func() {
		// The bound's actual contract, pinned as arithmetic rather than as a
		// number: whatever MaxRetentionDays is, the instant reapLogsOfJob computes
		// from it must (a) still be in the FUTURE, or the days arm reaps what it
		// was told to keep, and (b) survive a round trip through time.Duration.
		// time.Sub does not wrap, it SATURATES, so an over-large expiry would not
		// be loud -- it would quietly stop matching the expiry it came from.
		for _, days := range []int{
			NewBuildLogRetentionCalculator(0, 0, math.MaxUint64, 0).BuildLogsToRetain(atc.JobConfig{}).Days,
			NewBuildLogRetentionCalculator(0, 0, 0, math.MaxUint64).BuildLogsToRetain(atc.JobConfig{}).Days,
			NewBuildLogRetentionCalculator(0, 0, 0, 0).BuildLogsToRetain(makeJob(0, math.MaxInt, 0)).Days,
		} {
			end := time.Now().Add(-time.Minute)
			expiry := end.AddDate(0, 0, days)
			Expect(expiry).To(BeTemporally(">", time.Now()), "days=%d expired immediately", days)
			Expect(end.Add(expiry.Sub(end))).To(BeTemporally("==", expiry), "days=%d overflowed time.Duration", days)
		}
	})
	It("default set gives default", func() {
		logRetention := NewBuildLogRetentionCalculator(
			5, // default builds to retain
			0, // max builds to retain
			4, // default days to retain
			0, // max days to retain
		).BuildLogsToRetain(makeJob(
			0, // builds to retain
			0, // days to retain
			0, // min success to retain
		))
		Expect(logRetention.Builds).To(Equal(5))
		Expect(logRetention.Days).To(Equal(4))
		Expect(logRetention.MinimumSucceededBuilds).To(Equal(0))
	})
	It("default and job set gives job", func() {
		logRetention := NewBuildLogRetentionCalculator(
			5, // default builds to retain
			0, // max builds to retain
			4, // default days to retain
			0, // max days to retain
		).BuildLogsToRetain(makeJob(
			6, // builds to retain
			3, // days to retain
			0, // min success to retain
		))
		Expect(logRetention.Builds).To(Equal(6))
		Expect(logRetention.Days).To(Equal(3))
		Expect(logRetention.MinimumSucceededBuilds).To(Equal(0))
	})
	It("default, max, and job set, gives max if lower", func() {
		logRetention := NewBuildLogRetentionCalculator(
			5, // default builds to retain
			6, // max builds to retain
			5, // default days to retain
			6, // max days to retain
		).BuildLogsToRetain(makeJob(
			10, // builds to retain
			9,  // days to retain
			0,  // min success to retain
		))
		Expect(logRetention.Builds).To(Equal(6))
		Expect(logRetention.Days).To(Equal(6))
		Expect(logRetention.MinimumSucceededBuilds).To(Equal(0))
	})
	It("max only set gives max", func() {
		logRetention := NewBuildLogRetentionCalculator(
			0, // default builds to retain
			4, // max builds to retain
			0, // default days to retain
			3, // max days to retain
		).BuildLogsToRetain(makeJob(
			0, // builds to retain
			0, // days to retain
			0, // min success to retain
		))
		Expect(logRetention.Builds).To(Equal(4))
		Expect(logRetention.Days).To(Equal(3))
		Expect(logRetention.MinimumSucceededBuilds).To(Equal(0))
	})
	It("mix of count and days with max", func() {
		logRetention := NewBuildLogRetentionCalculator(
			2, // default builds to retain
			4, // max builds to retain
			3, // default days to retain
			2, // max days to retain
		).BuildLogsToRetain(makeJob(
			5, // builds to retain
			5, // days to retain
			8, // min success to retain
		))
		Expect(logRetention.Builds).To(Equal(4))
		Expect(logRetention.Days).To(Equal(2))
		Expect(logRetention.MinimumSucceededBuilds).To(Equal(0))
	})
	It("min success builds equals to builds", func() {
		logRetention := NewBuildLogRetentionCalculator(
			2,  // default builds to retain
			10, // max builds to retain
			3,  // default days to retain
			0,  // max days to retain
		).BuildLogsToRetain(makeJob(
			5, // builds to retain
			0, // days to retain
			5, // min success to retain
		))
		Expect(logRetention.Builds).To(Equal(5))
		Expect(logRetention.Days).To(Equal(3))
		Expect(logRetention.MinimumSucceededBuilds).To(Equal(5))
	})
	It("min success builds greater than builds", func() {
		logRetention := NewBuildLogRetentionCalculator(
			2,  // default builds to retain
			10, // max builds to retain
			3,  // default days to retain
			0,  // max days to retain
		).BuildLogsToRetain(makeJob(
			5, // builds to retain
			0, // days to retain
			8, // min success to retain
		))
		Expect(logRetention.Builds).To(Equal(5))
		Expect(logRetention.Days).To(Equal(3))
		Expect(logRetention.MinimumSucceededBuilds).To(Equal(0))
	})
	It("when only max builds is set and job build and days are set", func() {
		logRetention := NewBuildLogRetentionCalculator(
			0, // default builds to retain
			7, // max builds to retain
			0, // default days to retain
			0, // max days to retain
		).BuildLogsToRetain(makeJob(
			5, // builds to retain
			7, // days to retain
			0, // min success to retain
		))
		Expect(logRetention.Builds).To(Equal(5))
		Expect(logRetention.Days).To(Equal(7))
		Expect(logRetention.MinimumSucceededBuilds).To(Equal(0))
	})
	It("when only max days is set and job build and days are set", func() {
		logRetention := NewBuildLogRetentionCalculator(
			0, // default builds to retain
			0, // max builds to retain
			0, // default days to retain
			7, // max days to retain
		).BuildLogsToRetain(makeJob(
			7, // builds to retain
			5, // days to retain
			0, // min success to retain
		))
		Expect(logRetention.Builds).To(Equal(7))
		Expect(logRetention.Days).To(Equal(5))
		Expect(logRetention.MinimumSucceededBuilds).To(Equal(0))
	})
})

func makeJob(retainAmount, retainAmountDays, retainMinSuccessAmount int) atc.JobConfig {
	return atc.JobConfig{
		BuildLogRetention: &atc.BuildLogRetention{
			Builds:                 retainAmount,
			Days:                   retainAmountDays,
			MinimumSucceededBuilds: retainMinSuccessAmount,
		},
	}
}
