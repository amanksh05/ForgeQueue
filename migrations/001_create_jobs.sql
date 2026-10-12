
-- +goose Up
CREATE TABLE jobs (
    id UUID PRIMARY KEY,
    job_type TEXT NOT NULL,
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,

    status TEXT NOT NULL DEFAULT 'queued'
        CHECK (status IN (
            'queued',
            'running',
            'retry_wait',
            'succeeded',
            'dead'
        )),

    attempt_count INTEGER NOT NULL DEFAULT 0
        CHECK (attempt_count >= 0),
    max_attempts INTEGER NOT NULL DEFAULT 3
        CHECK (max_attempts > 0),

    available_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,

    lease_expires_at TIMESTAMPTZ,
    claim_token UUID,
    last_error TEXT,

    idempotency_key TEXT,
    request_fingerprint TEXT
);

CREATE UNIQUE INDEX idx_jobs_idempotency_key
    ON jobs (idempotency_key)
    WHERE idempotency_key IS NOT NULL;

CREATE INDEX idx_jobs_ready
    ON jobs (available_at, created_at, id)
    WHERE status IN ('queued', 'retry_wait');

CREATE INDEX idx_jobs_expired_leases
    ON jobs (lease_expires_at)
    WHERE status = 'running';

-- +goose Down
DROP TABLE jobs;
