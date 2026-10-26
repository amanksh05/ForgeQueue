
package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestRecoverExpiredJobs_RetryWhenAttemptsRemain(t *testing.T) {
	if os.Getenv("DATABASE_URL") == "" {
		t.Fatal("DATABASE_URL must be set to run store integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := NewPool(ctx)
	if err != nil {
		t.Fatalf("connect to database: %v", err)
	}
	defer pool.Close()

	id := uuid.New()
	claimToken := uuid.New()

	_, err = pool.Exec(ctx, `
		INSERT INTO jobs (
			id, job_type, payload, status,
			attempt_count, max_attempts,
			available_at, claim_token, lease_expires_at
		)
		VALUES (
			$1, 'echo', '{"message":"lease recovery test"}',
			'running', 1, 3,
			NOW(), $2, NOW() - INTERVAL '1 minute'
		)
	`, id, claimToken)
	if err != nil {
		t.Fatalf("insert test job: %v", err)
	}

	// Clean up only the row created by this test.
	defer func() {
		_, err := pool.Exec(context.Background(),
			`DELETE FROM jobs WHERE id = $1`, id)
		if err != nil {
			t.Errorf("clean up test job: %v", err)
		}
	}()

	if err := RecoverExpiredJobs(ctx, pool); err != nil {
		t.Fatalf("recover expired jobs: %v", err)
	}

	var status string
	var storedClaimToken *uuid.UUID
	var leaseExpiresAt *time.Time
	var completedAt *time.Time
	var lastError *string

	err = pool.QueryRow(ctx, `
		SELECT status, claim_token, lease_expires_at,
		       completed_at, last_error
		FROM jobs
		WHERE id = $1
	`, id).Scan(
		&status,
		&storedClaimToken,
		&leaseExpiresAt,
		&completedAt,
		&lastError,
	)
	if err != nil {
		t.Fatalf("read recovered job: %v", err)
	}

	if status != "retry_wait" {
		t.Errorf("status = %q, want %q", status, "retry_wait")
	}
	if storedClaimToken != nil {
		t.Errorf("claim_token = %v, want NULL", storedClaimToken)
	}
	if leaseExpiresAt != nil {
		t.Errorf("lease_expires_at = %v, want NULL", leaseExpiresAt)
	}
	if completedAt != nil {
		t.Errorf("completed_at = %v, want NULL", completedAt)
	}
	if lastError == nil || *lastError == "" {
		t.Error("last_error should explain why the lease was recovered")
	}
}
