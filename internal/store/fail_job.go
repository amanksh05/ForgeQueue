package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	retryBaseDelay = time.Second
	retryMaxDelay  = 5 * time.Minute
)

func retryDelay(attemptCount int) time.Duration {
	delay := retryBaseDelay

	for i := 1; i < attemptCount && delay < retryMaxDelay; i++ {
		if delay > retryMaxDelay/2 {
			delay = retryMaxDelay
			break
		}
		delay *= 2
	}
	return delay
}

func FailJob(
	ctx context.Context,
	pool *pgxpool.Pool,
	job Job,
	executionErr error,
) error {
	if executionErr == nil {
		return errors.New("FailJob called with nil execution error")
	}

	nextStatus := "retry_wait"
	var delay time.Duration
	if job.AttemptCount >= job.MaxAttempts {
		nextStatus = "dead"
	} else {
		delay = retryDelay(job.AttemptCount)
	}

	err := pool.QueryRow(
		ctx,
		`UPDATE jobs
		SET
			updated_at = NOW(),
			completed_at = CASE
				WHEN $2 = 'dead' THEN NOW()
				ELSE completed_at
			END,
			available_at = CASE
				WHEN $2 = 'retry_wait' THEN NOW() + ($1 * INTERVAL '1 second')
				ELSE available_at
			END,
			status = $2,
			last_error = $3
		WHERE id = $4
			AND claim_token = $5
			AND status = 'running'
			AND lease_expires_at > NOW()
		RETURNING status, updated_at`,
		delay.Seconds(),
		nextStatus,
		executionErr.Error(),
		job.ID,
		job.ClaimToken,
	).Scan(
		&job.Status,
		&job.UpdatedAt,
	)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf(
				"job %v could not be updated: claim may be stale or lease expired",
				job.ID,
			)
		}

		return fmt.Errorf("update failed for job %v: %w", job.ID, err)
	}

	return nil
}
