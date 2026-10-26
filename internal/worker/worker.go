package worker

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/amanksh05/forgeQueue/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	pollInterval = time.Second
	maxErrorWait = 30 * time.Second
)

func wait(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()

	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func RunWorker(ctx context.Context, pool *pgxpool.Pool) {
	logger := slog.Default()
	errorWait := pollInterval

	logger.Info("Worker started")

	defer logger.Info("Worker stopped")

	for {
		if ctx.Err() != nil {
			return
		}

		err := store.RecoverExpiredJobs(ctx, pool)
		if err != nil {
			if ctx.Err() != nil {
				return
			}

			logger.Error("Failed to recover expired jobs",
				"error", err,
			)

			if !wait(ctx, errorWait) {
				return
			}

			errorWait *= 2
			if errorWait > maxErrorWait {
				errorWait = maxErrorWait
			}
			continue
		}

		job, err := store.ClaimJob(ctx, pool)

		if errors.Is(err, store.ErrorNoJobAvailable) {
			errorWait = pollInterval

			if !wait(ctx, pollInterval) {
				return
			}
			continue
		}

		if err != nil {
			if ctx.Err() != nil {
				return
			}

			logger.Error("Failed to claim job",
				"error", err,
			)

			if !wait(ctx, errorWait) {
				return
			}

			errorWait *= 2
			if errorWait > maxErrorWait {
				errorWait = maxErrorWait
			}
			continue
		}

		errorWait = pollInterval

		logger.Info("Job claimed",
			"job_id", job.ID,
			"job_type", job.Type,
			"attempt", job.AttemptCount,
			"max_attempts", job.MaxAttempts,
		)

		startedAt := time.Now()
		executionErr := ExecuteJob(ctx, job)
		duration := time.Since(startedAt)

		if executionErr != nil {
			logger.Warn("Job execution failed",
				"job_id", job.ID,
				"job_type", job.Type,
				"attempt", job.AttemptCount,
				"duration", duration,
				"error", executionErr,
			)

			if err := store.FailJob(ctx, pool, job, executionErr); err != nil {
				logger.Error("Failed to record job failure",
					"job_id", job.ID,
					"error", err,
				)
			}

			continue
		}

		jobID := job.ID
		jobType := job.Type

		job, err = store.UpdateJobStatus(ctx, pool, job)

		if errors.Is(err, pgx.ErrNoRows) {
			logger.Warn("Job completion rejected; claim or lease is no longer valid",
				"job_id", jobID,
				"job_type", jobType,
			)
			continue
		}

		if err != nil {
			logger.Error("Failed to mark job as succeeded",
				"job_id", jobID,
				"job_type", jobType,
				"error", err,
			)
			continue
		}

		logger.Info("Job completed successfully",
			"job_id", job.ID,
			"job_type", job.Type,
			"duration", duration,
		)
	}
}
