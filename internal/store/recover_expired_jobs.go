package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

func RecoverExpiredJobs(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())

	var jobs []Job

	rows, err := tx.Query(ctx, `SELECT id, attempt_count, max_attempts
		FROM jobs
		WHERE status = 'running'
		AND lease_expires_at <= NOW()
		ORDER BY lease_expires_at ASC
		LIMIT 10
		FOR UPDATE SKIP LOCKED;`)

	if err != nil {
		return err
	}

	for rows.Next() {
		var job Job
		err := rows.Scan(
			&job.ID,
			&job.AttemptCount,
			&job.MaxAttempts,
		)

		if err != nil {
			rows.Close()
			return err
		}

		jobs = append(jobs, job)
	}

	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}

	rows.Close()

	if len(jobs) == 0 {
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		return nil
	}

	for _, job := range jobs {
		nextStatus := "retry_wait"
		lastError := "Lease expired; job will be retried"

		if job.AttemptCount >= job.MaxAttempts {
			nextStatus = "dead"
			lastError = "Lease expired and attempts were exhausted"
		}

		tag, err := tx.Exec(ctx, `
        UPDATE jobs
        SET
            status = $1,
            available_at = CASE
                WHEN $1 = 'retry_wait' THEN NOW()
                ELSE available_at
            END,
            completed_at = CASE
                WHEN $1 = 'dead' THEN NOW()
                ELSE NULL
            END,
            last_error = $2,
            claim_token = NULL,
            lease_expires_at = NULL,
            updated_at = NOW()
        WHERE id = $3
          AND status = 'running'
          AND lease_expires_at <= NOW()
    `, nextStatus, lastError, job.ID)

		if err != nil {
			return err
		}

		if tag.RowsAffected() != 1 {
			return fmt.Errorf(
				"recover job %v: expected 1 updated row, got %d",
				job.ID,
				tag.RowsAffected(),
			)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}

	return nil
}
