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

func wait(ctx context.Context) bool {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func RunWorker(ctx context.Context, pool *pgxpool.Pool) {
	for {
		if ctx.Err() != nil {
			return
		}
		job, err := store.ClaimJob(ctx, pool)

		if errors.Is(err, store.ErrorNoJobAvailable) {
			if !wait(ctx) {
				return
			}
			continue
		}
		if err != nil {
			log.Printf("Error claim_job: %v", err)
			if !wait(ctx) {
				return
			}
			continue
		}

		log.Printf("Claimed job id=%s type=%s", job.ID, job.Type)

		if err := ExecuteJob(ctx, job); err != nil {
			log.Printf("Job execution failed: %v", err)
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
