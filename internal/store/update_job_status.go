package store

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

func UpdateJobStatus(ctx context.Context, pool *pgxpool.Pool, job Job) (Job, error) {

	err := pool.QueryRow(ctx,
		`UPDATE jobs 
		SET status = 'succeeded',
			completed_at = NOW(),
			updated_at = NOW()
		WHERE id = $1 
			AND claim_token = $2 
			AND status = 'running'
			AND lease_expires_at > NOW()
			RETURNING status, completed_at, updated_at`,
		job.ID,
		job.ClaimToken,
	).Scan(
		&job.Status,
		&job.CompletedAt,
		&job.UpdatedAt,
	)

	if err != nil {
		return job, err
	}

	return job, nil
}
