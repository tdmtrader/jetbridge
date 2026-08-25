package gc

import (
	"math"

	"github.com/concourse/concourse/atc"
)

type BuildLogRetentionCalculator interface {
	BuildLogsToRetain(atc.JobConfig) atc.BuildLogRetention
}

type buildLogRetentionCalculator struct {
	defaultBuildLogsToRetain     uint64
	maxBuildLogsToRetain         uint64
	defaultDaysToRetainBuildLogs uint64
	maxDaysToRetainBuildLogs     uint64
}

func NewBuildLogRetentionCalculator(
	defaultBuildLogsToRetain uint64,
	maxBuildLogsToRetain uint64,
	defaultDaysToRetainBuildLogs uint64,
	maxDaysToRetainBuildLogs uint64,
) BuildLogRetentionCalculator {
	return &buildLogRetentionCalculator{
		defaultBuildLogsToRetain:     defaultBuildLogsToRetain,
		maxBuildLogsToRetain:         maxBuildLogsToRetain,
		defaultDaysToRetainBuildLogs: defaultDaysToRetainBuildLogs,
		maxDaysToRetainBuildLogs:     maxDaysToRetainBuildLogs,
	}
}

// MaxRetentionDays is the largest day count the retention decision will act on.
// The bound is chosen for the CONSUMER of Days, not for the int carrying it.
//
// Days is read in exactly one place: reapLogsOfJob turns it into an instant with
// build.EndTime().AddDate(0, 0, Days). time.Duration is an int64 count of
// NANOSECONDS, so the largest delay derivable from that instant is 2^63-1 ns ==
// 106751.99 days == 292.47 years. Rounding DOWN to whole days gives 106751, and
// the 0.99 day discarded is real headroom: a build whose end_time is up to ~23
// hours ahead of this process's clock still yields a delay that fits.
//
// Saturating to math.MaxInt the way clampBuildsFlag does would be WRONG here,
// and wrong in the direction that deletes: AddDate overflows `day + days` before
// time.Date normalizes, landing an arbitrary instant that reads as expired. That
// is the same silent total deletion a negative Days causes, reached from the
// other end. 292 years is indistinguishable from "forever" for a build log, so
// an operator who asks for more gets a policy that outlives their deployment
// rather than one that deletes everything.
const MaxRetentionDays = 106751

// clampBuildsFlag turns an operator's uint64 build-count knob into the int the
// retention decision works in. A plain conversion TRUNCATES, so any value from
// 2^63 up arrives negative -- and a negative budget is not a smaller budget. It
// makes `retainedBuilds >= logRetention.Builds` true for the very first
// candidate, so every build's logs are reaped, and then the over-retention
// correction in reapLogsOfJob computes delta = 0 - Builds > 0 against an empty
// keep list and indexes [-1], killing the build-reaper goroutine.
//
// Saturating a COUNT to MaxInt is safe because nothing does arithmetic with it
// -- it is only ever compared against a running total.
func clampBuildsFlag(flag uint64) int {
	if flag > math.MaxInt {
		return math.MaxInt
	}
	return int(flag)
}

// clampDaysFlag turns an operator's uint64 day knob into the int the retention
// decision works in, bounded by MaxRetentionDays. The same truncation has a
// worse consequence here: a negative Days makes
// `EndTime().AddDate(0, 0, Days).Before(time.Now())` true for every build ever
// run, so the days arm reaps a job's entire log history in one pass, with no
// panic and no error.
func clampDaysFlag(flag uint64) int {
	if flag > MaxRetentionDays {
		return MaxRetentionDays
	}
	return int(flag)
}

// boundDays applies the same ceiling to a JOB's own build_log_retention.days.
// configvalidate rejects a negative value there and bounds it no further, so a
// pipeline declaring math.MaxInt days passes both write paths (the config API
// and the set_pipeline step) and reaches the same overflowing AddDate.
//
// Negative values are deliberately NOT folded to zero here: configvalidate owns
// that end of the range for the job declaration, and quietly rewriting a value
// it would have rejected would hide the day validation stopped running.
func boundDays(days int) int {
	if days > MaxRetentionDays {
		return MaxRetentionDays
	}
	return days
}

func (blrc *buildLogRetentionCalculator) BuildLogsToRetain(jobConfig atc.JobConfig) atc.BuildLogRetention {
	// What does the job want?
	var daysToRetainBuildLogs = 0
	var buildLogsToRetain = 0
	var minSuccessBuildLogsToRetain = 0
	if jobConfig.BuildLogRetention != nil {
		daysToRetainBuildLogs = boundDays(jobConfig.BuildLogRetention.Days)
		buildLogsToRetain = jobConfig.BuildLogRetention.Builds
		minSuccessBuildLogsToRetain = jobConfig.BuildLogRetention.MinimumSucceededBuilds
	} else {
		buildLogsToRetain = jobConfig.BuildLogsToRetain
	}

	// If not specified, set to default
	if buildLogsToRetain == 0 {
		buildLogsToRetain = clampBuildsFlag(blrc.defaultBuildLogsToRetain)
	}
	if daysToRetainBuildLogs == 0 {
		daysToRetainBuildLogs = clampDaysFlag(blrc.defaultDaysToRetainBuildLogs)
	}

	var logRetention atc.BuildLogRetention

	// If we don't have a max set, then we're done
	if blrc.maxBuildLogsToRetain == 0 && blrc.maxDaysToRetainBuildLogs == 0 {
		logRetention.Builds = buildLogsToRetain
		// Apply the SAME bound as the max branch below: min-succeeded is
		// honoured only up to buildLogsToRetain. The two branches disagreeing
		// was the defect -- this one copied the declaration verbatim, so a job
		// declaring more min-succeeded than builds made reapLogsOfJob's
		// over-retention correction index off the front of its keep list and
		// panic the build-reaper goroutine. configvalidate does not stand in
		// front of it: its min_success_builds > builds check is gated on
		// builds > 0, so a job that declares only min_success_builds and takes
		// its count from --default-build-logs-to-retain reaches here.
		if minSuccessBuildLogsToRetain >= 0 && minSuccessBuildLogsToRetain <= logRetention.Builds {
			logRetention.MinimumSucceededBuilds = minSuccessBuildLogsToRetain
		}
		logRetention.Days = daysToRetainBuildLogs
		return logRetention
	}

	logRetention.Builds = clampBuildsFlag(blrc.maxBuildLogsToRetain)
	logRetention.Days = clampDaysFlag(blrc.maxDaysToRetainBuildLogs)

	if logRetention.Builds > 0 {
		// current value will be the max, only override if it's less than the current value
		if buildLogsToRetain > 0 && (buildLogsToRetain < logRetention.Builds) {
			logRetention.Builds = buildLogsToRetain
		}
	} else {
		logRetention.Builds = buildLogsToRetain
	}

	if logRetention.Days > 0 {
		// current value will be the max, only override if it's less than the current value
		if daysToRetainBuildLogs > 0 && daysToRetainBuildLogs < logRetention.Days {
			logRetention.Days = daysToRetainBuildLogs
		}
	} else {
		logRetention.Days = daysToRetainBuildLogs
	}

	// successBuildLogsToRetain defaults to 0, and up to buildLogsToRetain.
	if minSuccessBuildLogsToRetain >= 0 && minSuccessBuildLogsToRetain <= logRetention.Builds {
		logRetention.MinimumSucceededBuilds = minSuccessBuildLogsToRetain
	}

	return logRetention

}
