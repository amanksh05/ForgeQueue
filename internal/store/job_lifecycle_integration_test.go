package store

import (
	"context"
	"errors"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// These integration tests use DATABASE_URL and a real PostgreSQL database.
// Run them against a dedicated test database with the worker stopped.
func lifecycleTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	if os.Getenv("DATABASE_URL") == "" {
		t.Fatal("DATABASE_URL must be set to run store integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := NewPool(ctx)
	if err != nil {
		t.Fatalf("connect to database: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func insertLifecycleTestJob(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	status string,
	attemptCount, maxAttempts int,
	availableOffset, leaseOffset time.Duration,
	claimToken uuid.UUID,
) uuid.UUID {
	t.Helper()

	id := uuid.New()
	_, err := pool.Exec(ctx, `
		INSERT INTO jobs (
			id, job_type, payload, status, attempt_count, max_attempts,
			available_at, claim_token, lease_expires_at
		)
		VALUES (
			$1, 'echo', '{"message":"lifecycle integration test"}', $2,
			$3, $4, NOW() + ($5 * INTERVAL '1 second'),
			NULLIF($6::uuid, '00000000-0000-0000-0000-000000000000'::uuid),
			CASE
				WHEN $7 = 0 THEN NULL
				ELSE NOW() + ($7 * INTERVAL '1 second')
			END
		)
	`, id, status, attemptCount, maxAttempts,
		availableOffset.Seconds(), claimToken, leaseOffset.Seconds())
	if err != nil {
		t.Fatalf("insert test job: %v", err)
	}

	t.Cleanup(func() {
		_, cleanupErr := pool.Exec(context.Background(), `DELETE FROM jobs WHERE id = $1`, id)
		if cleanupErr != nil {
			t.Errorf("clean up test job %s: %v", id, cleanupErr)
		}
	})
	return id
}

func TestRetryDelay_BoundedExponentialBackoff(t *testing.T) {
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{attempt: 1, want: time.Second},
		{attempt: 2, want: 2 * time.Second},
		{attempt: 3, want: 4 * time.Second},
		{attempt: 4, want: 8 * time.Second},
		{attempt: 10, want: 5 * time.Minute},
		{attempt: 100, want: 5 * time.Minute},
	}

	for _, tt := range tests {
		t.Run(strconv.Itoa(tt.attempt), func(t *testing.T) {
			if got := retryDelay(tt.attempt); got != tt.want {
				t.Errorf("retryDelay(%d) = %v, want %v", tt.attempt, got, tt.want)
			}
		})
	}
}

func TestClaimJob_OnlyClaimsAvailableJobsAndIncrementsAttempt(t *testing.T) {
	pool := lifecycleTestPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	id := insertLifecycleTestJob(
		t, ctx, pool, "queued", 0, 3,
		5*time.Minute, 0, uuid.Nil,
	)

	_, err := ClaimJob(ctx, pool)
	if !errors.Is(err, ErrorNoJobAvailable) {
		t.Fatalf("ClaimJob with only a future-available job: err = %v, want %v", err, ErrorNoJobAvailable)
	}

	if _, err := pool.Exec(ctx, `UPDATE jobs SET available_at = NOW() WHERE id = $1`, id); err != nil {
		t.Fatalf("make test job available: %v", err)
	}

	job, err := ClaimJob(ctx, pool)
	if err != nil {
		t.Fatalf("ClaimJob: %v", err)
	}
	if job.ID != id.String() {
		t.Fatalf("claimed id = %s, want %s; use a dedicated test database with no other ready jobs", job.ID, id)
	}
	if job.Status != "running" {
		t.Errorf("status = %q, want running", job.Status)
	}
	if job.AttemptCount != 1 {
		t.Errorf("attempt_count = %d, want 1", job.AttemptCount)
	}
	if job.ClaimToken == uuid.Nil {
		t.Error("claim_token should be set for a claimed job")
	}
	if !job.LeaseExpiresAt.After(time.Now()) {
		t.Errorf("lease should be in the future, got %v", job.LeaseExpiresAt)
	}
}

func TestRecoverExpiredJobs_RetryAndDeadTransitions(t *testing.T) {
	pool := lifecycleTestPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	retryID := insertLifecycleTestJob(
		t, ctx, pool, "running", 1, 3,
		0, -time.Minute, uuid.New(),
	)
	deadID := insertLifecycleTestJob(
		t, ctx, pool, "running", 3, 3,
		0, -time.Minute, uuid.New(),
	)

	if err := RecoverExpiredJobs(ctx, pool); err != nil {
		t.Fatalf("RecoverExpiredJobs: %v", err)
	}

	assertRecovered := func(id uuid.UUID, wantStatus string, wantCompleted bool) {
		t.Helper()
		var status string
		var claimToken *uuid.UUID
		var leaseExpiresAt, completedAt *time.Time
		var lastError *string

		err := pool.QueryRow(ctx, `
			SELECT status, claim_token, lease_expires_at, completed_at, last_error
			FROM jobs WHERE id = $1
		`, id).Scan(&status, &claimToken, &leaseExpiresAt, &completedAt, &lastError)
		if err != nil {
			t.Fatalf("read recovered job %s: %v", id, err)
		}

		if status != wantStatus {
			t.Errorf("job %s status = %q, want %q", id, status, wantStatus)
		}
		if claimToken != nil {
			t.Errorf("job %s claim_token = %v, want NULL", id, claimToken)
		}
		if leaseExpiresAt != nil {
			t.Errorf("job %s lease_expires_at = %v, want NULL", id, leaseExpiresAt)
		}
		if wantCompleted && completedAt == nil {
			t.Errorf("job %s completed_at should be set", id)
		}
		if !wantCompleted && completedAt != nil {
			t.Errorf("job %s completed_at = %v, want NULL", id, completedAt)
		}
		if lastError == nil || *lastError == "" {
			t.Errorf("job %s should have a recovery explanation in last_error", id)
		}
	}

	assertRecovered(retryID, "retry_wait", false)
	assertRecovered(deadID, "dead", true)
}

func TestFailJob_RetryUsesBackoffAndClearsLease(t *testing.T) {
	pool := lifecycleTestPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	token := uuid.New()
	id := insertLifecycleTestJob(
		t, ctx, pool, "running", 1, 3,
		0, time.Minute, token,
	)

	job := Job{
		ID:           id.String(),
		ClaimToken:   token,
		AttemptCount: 1,
		MaxAttempts:  3,
	}
	if err := FailJob(ctx, pool, job, errors.New("test execution failure")); err != nil {
		t.Fatalf("FailJob: %v", err)
	}

	var status string
	var availableAt time.Time
	var claimToken *uuid.UUID
	var leaseExpiresAt *time.Time
	var lastError *string
	err := pool.QueryRow(ctx, `
		SELECT status, available_at, claim_token, lease_expires_at, last_error
		FROM jobs WHERE id = $1
	`, id).Scan(&status, &availableAt, &claimToken, &leaseExpiresAt, &lastError)
	if err != nil {
		t.Fatalf("read failed job: %v", err)
	}

	if status != "retry_wait" {
		t.Errorf("status = %q, want retry_wait", status)
	}
	if time.Until(availableAt) < 500*time.Millisecond {
		t.Errorf("available_at should be delayed by approximately 1 second, got %v", availableAt)
	}
	if claimToken != nil {
		t.Errorf("claim_token = %v, want NULL after failure", claimToken)
	}
	if leaseExpiresAt != nil {
		t.Errorf("lease_expires_at = %v, want NULL after failure", leaseExpiresAt)
	}
	if lastError == nil || *lastError != "test execution failure" {
		t.Errorf("last_error = %v, want the execution error", lastError)
	}
}

func TestFailJob_ExhaustedAttemptsBecomeDead(t *testing.T) {
	pool := lifecycleTestPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	token := uuid.New()
	id := insertLifecycleTestJob(
		t, ctx, pool, "running", 3, 3,
		0, time.Minute, token,
	)
	job := Job{
		ID:           id.String(),
		ClaimToken:   token,
		AttemptCount: 3,
		MaxAttempts:  3,
	}

	if err := FailJob(ctx, pool, job, errors.New("final failure")); err != nil {
		t.Fatalf("FailJob: %v", err)
	}

	var status string
	var completedAt *time.Time
	err := pool.QueryRow(ctx, `SELECT status, completed_at FROM jobs WHERE id = $1`, id).
		Scan(&status, &completedAt)
	if err != nil {
		t.Fatalf("read dead job: %v", err)
	}
	if status != "dead" {
		t.Errorf("status = %q, want dead", status)
	}
	if completedAt == nil {
		t.Error("completed_at should be set when the job becomes dead")
	}
}

func TestFailJob_RejectsStaleClaim(t *testing.T) {
	pool := lifecycleTestPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	currentToken := uuid.New()
	id := insertLifecycleTestJob(
		t, ctx, pool, "running", 1, 3,
		0, time.Minute, currentToken,
	)

	staleJob := Job{
		ID:           id.String(),
		ClaimToken:   uuid.New(),
		AttemptCount: 1,
		MaxAttempts:  3,
	}
	if err := FailJob(ctx, pool, staleJob, errors.New("stale worker failure")); err == nil {
		t.Fatal("FailJob with a stale claim token succeeded; want an error")
	}

	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM jobs WHERE id = $1`, id).Scan(&status); err != nil {
		t.Fatalf("read job status: %v", err)
	}
	if status != "running" {
		t.Errorf("status = %q, want running because the stale claim must not update it", status)
	}
}

func TestUpdateJobStatus_RejectsExpiredLease(t *testing.T) {
	pool := lifecycleTestPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	token := uuid.New()
	id := insertLifecycleTestJob(
		t, ctx, pool, "running", 1, 3,
		0, -time.Minute, token,
	)
	job := Job{ID: id.String(), ClaimToken: token}

	_, err := UpdateJobStatus(ctx, pool, job)
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("UpdateJobStatus with expired lease: err = %v, want pgx.ErrNoRows", err)
	}

	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM jobs WHERE id = $1`, id).Scan(&status); err != nil {
		t.Fatalf("read job status: %v", err)
	}
	if status != "running" {
		t.Errorf("status = %q, want running because an expired lease must not complete the job", status)
	}
}
