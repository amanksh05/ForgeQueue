# ForgeQueue

A lightweight job queue built with Go and PostgreSQL. ForgeQueue accepts jobs through a REST API and processes them asynchronously using a background worker. It supports retryable failures, bounded exponential backoff, attempt limits, and recovery of jobs abandoned after a worker lease expires.

## Features

- REST API for creating, retrieving, and listing jobs
- PostgreSQL-backed persistent job storage
- Background worker for asynchronous execution
- Atomic job claiming with PostgreSQL transactions and `FOR UPDATE SKIP LOCKED`
- Lease-based job ownership and stale-claim protection
- Automatic recovery of expired worker leases
- Retry delays using bounded exponential backoff
- Maximum-attempt enforcement and dead-job handling
- Docker Compose development environment
- Unit, integration, reliability, and API/worker smoke tests

## Architecture

```text
                 ┌──────────────────┐
                 │      Client      │
                 └────────┬─────────┘
                          │ HTTP
                          ▼
                 ┌──────────────────┐
                 │    REST API      │
                 │   Go / :8080     │
                 └────────┬─────────┘
                          │
                          ▼
                 ┌──────────────────┐
                 │   PostgreSQL     │
                 │ Persistent jobs  │
                 └────────▲─────────┘
                          │ Claim / Update
                          │
                 ┌────────┴─────────┐
                 │ Background Worker│
                 │ Execute / Recover│
                 └──────────────────┘
```

The API stores jobs in PostgreSQL. The worker claims eligible jobs, executes them, and updates their status. PostgreSQL transactions and row locking help coordinate job claims when multiple workers compete for work.

## Technology stack

- **Language:** Go
- **Database:** PostgreSQL
- **Database driver/pool:** pgx / pgxpool
- **Database migrations:** Goose
- **Local environment:** Docker Compose
- **Testing:** Go tests and shell-based smoke tests

## Prerequisites

Install the following tools:

- Go, matching the version specified in `go.mod`
- Docker Engine and Docker Compose
- Git
- `curl` for the API smoke checks

The commands below assume a Linux or WSL shell.

## Getting started

### 1. Clone the repository

```bash
git clone <your-repository-url>
cd forgeQueue
```

If you already have the project locally, enter its directory instead.

### 2. Configure environment variables

Create your local environment file using the example:

```bash
cp .env.example .env
```

Edit `.env` and configure the database connection details required by the application and Docker Compose.

The application uses `DATABASE_URL` to connect to PostgreSQL. The Go integration tests use `DATABASE_URL` as well.

For host-side integration tests, configure `TEST_DATABASE_URL` to connect to the Docker database through its published host port. In the current development setup, PostgreSQL is published on port `5433`.

Example connection URL format:

```text
postgres://<user>:<password>@localhost:5433/<database>?sslmode=disable
```

Use the actual credentials and database name from your environment. Do not commit `.env` or real credentials to Git.

### 3. Start PostgreSQL

```bash
docker compose up -d db
```

Check database readiness:

```bash
docker compose exec -T db pg_isready
```

### 4. Start the application

```bash
docker compose up -d app
```

Check the running services:

```bash
docker compose ps
```

Check the health endpoint:

```bash
curl --fail --silent http://localhost:8080/health
echo
```

The application should be reachable at `http://localhost:8080`.

> Database migration execution is part of the project setup. Confirm the configured migration service or startup process in the Compose configuration before initializing a completely fresh database.

## Job lifecycle

The queue uses the following main statuses:

| Status | Meaning |
|---|---|
| `queued` | Job is waiting for its first execution. |
| `running` | A worker has claimed the job and holds a lease. |
| `retry_wait` | Execution failed or a lease expired; the job is waiting to become eligible again. |
| `succeeded` | The job completed successfully. |
| `dead` | The job cannot be retried because its attempt limit was exhausted. |

### Claiming jobs

The worker selects eligible `queued` or `retry_wait` jobs whose `available_at` timestamp has passed.

Claiming uses a transaction and `FOR UPDATE SKIP LOCKED` so concurrent workers can claim different available jobs without waiting on the same locked row.

When a job is claimed, the worker:

1. Changes the status to `running`.
2. Generates a new claim token.
3. Sets a lease expiration.
4. Increments the attempt count.

The current implementation uses a 30-second lease.

### Successful execution

When execution succeeds, the worker attempts to mark the job as `succeeded`. The update is guarded by the claim token, running status, and an unexpired lease. A stale worker must not be allowed to complete a job after losing ownership.

### Failed execution and retries

When execution fails, ForgeQueue records the error and clears the active claim token and lease.

If attempts remain, the job moves to `retry_wait`. Its next eligible execution is delayed using bounded exponential backoff:

| Attempt | Delay |
|---:|---:|
| 1 | 1 second |
| 2 | 2 seconds |
| 3 | 4 seconds |
| 4 | 8 seconds |
| Higher attempts | Increases up to 5 minutes |

The delay is capped at five minutes.

When the maximum number of attempts has been reached, the job moves to `dead` instead of being scheduled for another retry.

### Expired lease recovery

If a worker crashes or stops before finishing a job, the job may remain in `running` after its lease expires.

The recovery process checks expired leases and:

- Moves jobs with attempts remaining to `retry_wait`.
- Moves jobs with exhausted attempts to `dead`.
- Clears the old claim token and lease.
- Records an explanatory error and updates the job timestamps.

A recovered job becomes eligible for claiming again according to its `available_at` value.

## Job types

The current implementation includes these job types:

- `echo` — processes a message payload.
- `sleep` — waits for a configured duration.
- `fail_once` — supports testing retry behavior.

Check the job executor implementation for the exact payload fields, validation rules, and behavior of each type before using these examples in production.

## API

The API provides job creation, retrieval, listing, and a health endpoint.

| Operation | Purpose |
|---|---|
| Health check | Verify that the API is responding. |
| Create job | Submit a job for asynchronous execution. |
| Get job | Retrieve a job by ID. |
| List jobs | List jobs with pagination and optional status filtering. |

Health check:

```bash
curl --fail --silent http://localhost:8080/health
```

The exact job endpoint paths, accepted request fields, query parameters, response structures, and HTTP status codes should match the handlers registered in the current source code. Consult the API router and handler implementations before adding request examples here.

## Running tests

### Run all Go tests

```bash
set -a
source .env
set +a

env DATABASE_URL="$TEST_DATABASE_URL" go test -count=1 ./...
```

Use a dedicated test database. Integration tests insert and modify real database rows.

### Run reliability and smoke tests

```bash
chmod +x scripts/test_reliability.sh
./scripts/test_reliability.sh
```

This script checks required reliability test coverage, starts PostgreSQL, runs the Go test suite, starts the application, checks API health, and runs the API/worker smoke tests.

The current test setup includes coverage for:

- Bounded exponential retry backoff
- Available-job filtering and attempt increments
- Expired-lease recovery and retry/dead transitions
- Retry behavior and exhausted attempts
- Stale claim rejection
- Rejection of completion after lease expiry
- Recovery-to-claim behavior
- Concurrent worker claims

The smoke-test script also cleans up its marked fixture jobs.

### Inspect logs

```bash
docker compose logs --tail=100 db
docker compose logs --tail=100 app
```

Check service status:

```bash
docker compose ps
```

## Development notes

- Keep `.env` out of version control.
- Use `.env.example` to document required environment variable names without real secrets.
- Run `gofmt` on changed Go files.
- Run the full Go suite and smoke tests after changing job lifecycle behavior.
- Avoid using a production database for integration tests.
- Avoid `docker compose down -v` unless you deliberately intend to delete the persistent database volume.

## Current project status

The reliability runner and API/worker smoke tests have passed in the current development environment. This demonstrates that the tested scenarios pass; it is not a guarantee that all production failure modes have been covered.

## Future improvements

Potential next steps include:

- Graceful worker shutdown and restart testing
- Fresh-database migration testing
- Structured logging and operational metrics
- Configurable worker concurrency and lease duration
- More extensive load and failure-injection tests
- CI automation for Go tests and Docker-based integration tests
