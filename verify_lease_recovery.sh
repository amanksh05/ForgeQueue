#!/usr/bin/env bash
set -euo pipefail

set -a
source .env
set +a

DB="${DATABASE_URL:?DATABASE_URL is not set}"

RETRY_ID=$(uuidgen)
DEAD_ID=$(uuidgen)
TOKEN1=$(uuidgen)
TOKEN2=$(uuidgen)

cleanup() {
  psql "$DB" -v ON_ERROR_STOP=1 -q \
    -c "DELETE FROM jobs WHERE id IN ('$RETRY_ID', '$DEAD_ID');" \
    >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "Creating expired running jobs..."

psql "$DB" -v ON_ERROR_STOP=1 <<SQL
INSERT INTO jobs (
    id, job_type, payload, status,
    attempt_count, max_attempts,
    available_at, claim_token, lease_expires_at
) VALUES
(
    '$RETRY_ID', 'echo', '{"message":"lease-retry-test"}',
    'running', 1, 3,
    NOW(), '$TOKEN1', NOW() - INTERVAL '1 minute'
),
(
    '$DEAD_ID', 'echo', '{"message":"lease-dead-test"}',
    'running', 3, 3,
    NOW(), '$TOKEN2', NOW() - INTERVAL '1 minute'
);
SQL

echo
echo "Waiting for the worker to recover the expired leases..."

for i in $(seq 1 20); do
  RESULT=$(psql "$DB" -At -F '|' -v ON_ERROR_STOP=1 <<SQL
SELECT
  (SELECT status FROM jobs WHERE id = '$RETRY_ID'),
  (SELECT status FROM jobs WHERE id = '$DEAD_ID');
SQL
)
  if [[ "$RESULT" == "retry_wait|dead" ]]; then
    break
  fi
  sleep 1
done

echo
echo "Verification results:"
psql "$DB" -v ON_ERROR_STOP=1 <<SQL
SELECT
    id,
    status,
    attempt_count,
    (claim_token IS NULL) AS claim_token_cleared,
    (lease_expires_at IS NULL) AS lease_cleared,
    (completed_at IS NOT NULL) AS completed_at_set,
    (last_error IS NOT NULL AND last_error <> '') AS error_recorded
FROM jobs
WHERE id IN ('$RETRY_ID', '$DEAD_ID')
ORDER BY id;
SQL

RETRY_STATUS=$(psql "$DB" -At -v ON_ERROR_STOP=1 \
  -c "SELECT status FROM jobs WHERE id = '$RETRY_ID';")
DEAD_STATUS=$(psql "$DB" -At -v ON_ERROR_STOP=1 \
  -c "SELECT status FROM jobs WHERE id = '$DEAD_ID';")

if [[ "$RETRY_STATUS" == "retry_wait" && "$DEAD_STATUS" == "dead" ]]; then
  echo
  echo "PASS: expired jobs with attempts remaining enter retry_wait."
  echo "PASS: expired jobs with exhausted attempts enter dead."
else
  echo
  echo "FAIL: expected retry_wait and dead; got '$RETRY_STATUS' and '$DEAD_STATUS'."
  echo "Check worker logs and ensure the worker is running."
  exit 1
fi
