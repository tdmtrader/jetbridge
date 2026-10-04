package scheduler

import (
	"context"
	"fmt"
	"sync"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/atc/util"
	"github.com/concourse/concourse/tracing"
	"go.opentelemetry.io/otel/attribute"
)

//counterfeiter:generate . BuildScheduler
type BuildScheduler interface {
	Schedule(
		ctx context.Context,
		logger lager.Logger,
		job db.SchedulerJob,
	) (ScheduleResult, error)
}

type Runner struct {
	logger        lager.Logger
	jobFactory    db.JobFactory
	scheduler     BuildScheduler
	notifications db.NotificationsBus

	guardJobScheduling chan struct{}
	running            *sync.Map
}

func NewRunner(logger lager.Logger, jobFactory db.JobFactory, scheduler BuildScheduler, maxJobs uint64, notifications db.NotificationsBus) *Runner {
	return &Runner{
		logger:        logger,
		jobFactory:    jobFactory,
		scheduler:     scheduler,
		notifications: notifications,

		guardJobScheduling: make(chan struct{}, maxJobs),
		running:            &sync.Map{},
	}
}

func (s *Runner) Run(ctx context.Context) error {
	sLog := s.logger.Session("run")

	sLog.Debug("start")
	defer sLog.Debug("done")

	jobs, err := s.jobsToSchedule(ctx)
	if err != nil {
		return fmt.Errorf("find jobs to schedule: %w", err)
	}

	for _, j := range jobs {
		active := s.claimJob(j)
		if active == nil {
			continue
		}

		s.guardJobScheduling <- struct{}{}

		jLog := sLog.Session("job", lager.Data{"job": j.Name()})

		go func(job db.SchedulerJob, active *jobScheduling) {
			defer func() {
				err := util.DumpPanic(recover(), "scheduling job %d", job.ID())
				if err != nil {
					jLog.Error("panic-in-scheduler-run", err)
				}
			}()

			defer s.finishScheduling(ctx, jLog, job.ID(), active)

			schedulingLock, acquired, err := job.AcquireSchedulingLock(sLog)
			if err != nil {
				jLog.Error("failed-to-acquire-lock", err)
				return
			}

			if !acquired {
				return
			}

			defer schedulingLock.Release()

			err = s.scheduleJob(ctx, sLog, job)
			if err != nil {
				jLog.Error("failed-to-schedule-job", err)
			}
		}(j, active)
	}

	return nil
}

// jobsToSchedule returns all jobs that need scheduling. We always perform a
// full scan rather than targeting specific job IDs from the NOTIFY payload
// because the notification channel can overflow (non-blocking send with
// capacity 1), causing dropped notifications. A full scan ensures that any
// notification—even for a different job—will pick up all pending work.
func (s *Runner) jobsToSchedule(ctx context.Context) (db.SchedulerJobs, error) {
	return s.jobFactory.JobsToSchedule()
}

func (s *Runner) scheduleJob(ctx context.Context, logger lager.Logger, job db.SchedulerJob) error {
	metric.Metrics.JobsScheduling.Inc()
	defer metric.Metrics.JobsScheduling.Dec()
	defer metric.Metrics.JobsScheduled.Inc()

	logger = logger.Session("schedule-job", lager.Data{"job": job.Name()})
	spanCtx, span := tracing.StartSpan(ctx, "schedule-job", tracing.Attrs{
		"team":     job.TeamName(),
		"pipeline": job.PipelineName(),
		"job":      job.Name(),
	})
	defer span.End()

	logger.Debug("schedule")

	// Grabs out the requested time that triggered off the job schedule in
	// order to set the last scheduled to the exact time of this triggering
	// request
	requestedTime := job.ScheduleRequestedTime()

	found, err := job.Reload()
	if err != nil {
		return fmt.Errorf("reload job: %w", err)
	}

	if !found {
		logger.Debug("could-not-find-job-to-reload")
		return nil
	}

	jStart := time.Now()

	result, err := s.scheduler.Schedule(
		spanCtx,
		logger,
		job,
	)
	if err != nil {
		return fmt.Errorf("schedule job: %w", err)
	}

	span.SetAttributes(attribute.Bool("needs-retry", result.NeedsRetry))
	if !result.NeedsRetry {
		err = job.ConsumeScheduleRequest(requestedTime)
		if err != nil {
			logger.Error("failed-to-consume-schedule-request", err, lager.Data{"job": job.Name()})
			return fmt.Errorf("consume schedule request: %w", err)
		}
	}

	metric.SchedulingJobDuration{
		PipelineName: job.PipelineName(),
		JobName:      job.Name(),
		JobID:        job.ID(),
		Duration:     time.Since(jStart),
	}.Emit(logger)

	return nil
}

// A signal consumed while a job is running must remain actionable after that
// pass releases its scheduling lock. Record only a newer request token: waking
// again for the same unresolved request could busy-loop on a blocked build.
type jobScheduling struct {
	requested    time.Time
	mu           sync.Mutex
	retired      bool
	newerRequest bool
}

func (s *Runner) claimJob(job db.SchedulerJob) *jobScheduling {
	requested := job.ScheduleRequestedTime()
	next := &jobScheduling{requested: requested}
	for {
		value, loaded := s.running.LoadOrStore(job.ID(), next)
		if !loaded {
			return next
		}
		active := value.(*jobScheduling)
		active.mu.Lock()
		if !requested.After(active.requested) {
			active.mu.Unlock()
			return nil
		}
		if active.retired {
			// The old pass removed its map entry while this caller was waiting for
			// its mutex. Claim the new request instead of recording it on dead state.
			active.mu.Unlock()
			continue
		}
		active.newerRequest = true
		active.mu.Unlock()
		return nil
	}
}

func (s *Runner) finishScheduling(ctx context.Context, logger lager.Logger, jobID int, active *jobScheduling) {
	<-s.guardJobScheduling
	active.mu.Lock()
	s.running.Delete(jobID)
	active.retired = true
	notify := active.newerRequest
	active.mu.Unlock()
	// This defer runs after the job lock is released. The next full scan keeps
	// all admission, cancellation and max-in-flight checks in their normal path.
	// The original periodic fallback still covers failed notification delivery.
	if notify && s.notifications != nil && ctx.Err() == nil {
		if err := s.notifications.Notify(atc.ComponentScheduler); err != nil {
			logger.Error("failed-to-notify-scheduler-after-overlap", err)
		}
	}
}
