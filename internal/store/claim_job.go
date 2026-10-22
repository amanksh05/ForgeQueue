package store

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrorNoJobAvailable = errors.New("no job available")

type Job struct {
	ID             string          `json:"id"`
	Type           string          `json:"type"`
	Payload        json.RawMessage `json:"payload"`
	Status         string          `json:"status"`
	CreatedAt      time.Time       `json:"created_at"`
	ClaimToken     uuid.UUID       `json:"claim_token"`
	CompletedAt    time.Time       `json:"completed_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
	LeaseExpiresAt time.Time       `json:"lease_expires_at"`
	AttemptCount   int             `json:"attempt_count"`
	MaxAttempts    int             `json:"max_attempts"`
}

func ClaimJob(ctx context.Context, pool *pgxpool.Pool) (Job, error) {
	var job Job

	tx, err := pool.Begin(ctx)
	if err != nil {
		log.Printf("ERROR claim_job: begin transaction failed: %v", err)
		return job, err
	}
	defer tx.Rollback(context.Background())

	err = tx.QueryRow(ctx, `
	SELECT id, job_type, payload, status, created_at, attempt_count, max_attempts
	FROM jobs
	WHERE (
    (status = 'queued' OR status = 'retry_wait')
    AND available_at <= NOW()
)
	ORDER BY available_at ASC
	LIMIT 1
			FOR UPDATE SKIP LOCKED
			`).Scan(
		&job.ID,
		&job.Type,
		&job.Payload,
		&job.Status,
		&job.CreatedAt,
		&job.AttemptCount,
		&job.MaxAttempts,
	)

	if errors.Is(err, pgx.ErrNoRows) {
		return job, ErrorNoJobAvailable
	}

	if err != nil {
		return job, err
	}

	claimToken := uuid.New()
	err = tx.QueryRow(ctx, `
			UPDATE jobs
			SET status = 'running'
			, claim_token = $1
			, lease_expires_at = NOW() + INTERVAL '30 seconds'
			, attempt_count = attempt_count + 1
			WHERE id = $2
  				AND status IN ('queued', 'retry_wait')
  				AND available_at <= NOW()
			RETURNING status, claim_token, lease_expires_at, attempt_count
		`, claimToken, job.ID).Scan(
		&job.Status,
		&job.ClaimToken,
		&job.LeaseExpiresAt,
		&job.AttemptCount,
	)

	if err != nil {
		return job, err
	}

	if err := tx.Commit(ctx); err != nil {
		return job, err
	}

	return job, nil

}
