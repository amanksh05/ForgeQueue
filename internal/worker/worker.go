package worker

import (
	"context"
	"errors"
	"log"
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
	errorWait := pollInterval
	for {
		if ctx.Err() != nil {
			return
		}

		err := store.RecoverExpiredJobs(ctx, pool)

		if err != nil {
			log.Printf("Failed to recover expired jobs: %v", err)

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
			log.Printf("Error claim_job: %v", err)
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

		log.Printf("Claimed job id=%s type=%s", job.ID, job.Type)

		if executionErr := ExecuteJob(ctx, job); executionErr != nil {
			log.Printf("Job execution failed: %v", executionErr)
			if err := store.FailJob(ctx, pool, job, executionErr); err != nil {
				log.Printf(
					"Failed to record job failure: id=%s error=%v",
					job.ID,
					err,
				)
			}

			continue
		}

		job, err = store.UpdateJobStatus(ctx, pool, job)

		if errors.Is(err, pgx.ErrNoRows) {
			log.Printf(
				"Worker claim is not valid: id=%s",
				job.ID,
			)
			continue
		}

		if err != nil {
			log.Printf("Failed to update job status. Error: %v", err)
			continue
		}

		log.Printf("Job execution successful! id=%s type=%s", job.ID, job.Type)

	}
}
