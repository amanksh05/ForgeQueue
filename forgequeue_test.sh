#!/usr/bin/env bash
# ForgeQueue smoke tests. Run from project root after starting API and worker.
# Requires curl, python3, psql, and DATABASE_URL or DB_URL (environment or .env).
# Only deletes rows tagged by this script; never use against production.
set -u
BASE_URL="${BASE_URL:-http://localhost:8080}"
MARKER="forgequeue_test.sh"
TMP="$(mktemp -d)"
PASS=0
FAIL=0
WARN=0
pass() {
	echo "[PASS] $1"
	PASS=$((PASS + 1))
}
fail() {
	echo "[FAIL] $1"
	FAIL=$((FAIL + 1))
}
warn() {
	echo "[WARN] $1"
	WARN=$((WARN + 1))
}
header() { printf '\n== %s ==\n' "$1"; }
for c in curl python3 psql; do
	if ! command -v "$c" >/dev/null 2>&1; then
		echo "Missing required command: $c"
		[[ "$c" != psql ]] || echo "Install with: sudo apt install postgresql-client"
		exit 1
	fi
done
if [[ -z "${DATABASE_URL:-}" && -z "${DB_URL:-}" && -f .env ]]; then
	ENV_URL="$(
		python3 - <<'PY'
from pathlib import Path
for line in Path('.env').read_text().splitlines():
    line=line.strip()
    if line and not line.startswith('#') and '=' in line:
        k,v=line.split('=',1)
        if k.strip() in ('DATABASE_URL','DB_URL'):
            v=v.strip()
            if len(v)>=2 and v[0]==v[-1] and v[0] in ('\"', "'"): v=v[1:-1]
            print(v)
PY
	)"
	[[ -z "$ENV_URL" ]] || DATABASE_URL="$ENV_URL"
fi
DB_CONN="${TEST_DATABASE_URL:-${DB_CONN:-}}"

if [[ -z "$DB_CONN" ]]; then
    echo "[FAIL] TEST_DATABASE_URL is not configured"
    exit 1
fi
request() {
	HTTP_CODE="$(curl -sS --connect-timeout 3 --max-time 15 -o "$TMP/body" -w '%{http_code}' "$@" 2>"$TMP/curlerr")" || {
		HTTP_CODE=000
		cat "$TMP/curlerr"
		return 1
	}
}
field() {
	python3 - "$1" "$TMP/body" <<'PY'
import json,sys
try:
    x=json.load(open(sys.argv[2],encoding='utf-8'))
    for k in sys.argv[1].split('.'): x=x[k]
    print('' if x is None else x)
except Exception: print('')
PY
}
show() {
	python3 -m json.tool "$TMP/body" 2>/dev/null || cat "$TMP/body"
	echo
}
db_cleanup() { psql "$DB_CONN" -v ON_ERROR_STOP=1 -q -c "DELETE FROM jobs WHERE payload->>'_forgequeue_test_fixture' = '$MARKER';" >"$TMP/dbout" 2>"$TMP/dberr"; }
cleanup() {
	code=$?
	trap - EXIT
	if [[ -n "${DB_CONN:-}" ]] && command -v psql >/dev/null 2>&1; then
		if db_cleanup; then
			echo
			echo "Cleaned up marked ForgeQueue test jobs."
		else
			echo "[WARN] Database cleanup failed:"
			cat "$TMP/dberr" 2>/dev/null || true
		fi
	fi
	rm -rf "$TMP"
	exit "$code"
}
trap cleanup EXIT
echo "ForgeQueue smoke tests | API: $BASE_URL"
echo "Only jobs marked _forgequeue_test_fixture=$MARKER will be deleted."
echo "Use a development/test database only."
header "Clean stale fixtures from previous run"
if db_cleanup; then pass "Removed stale marked fixtures"; else
	fail "Cannot clean fixtures; stopping before creating jobs"
	cat "$TMP/dberr"
	exit 1
fi
header "Health"
if request "$BASE_URL/health"; then
	[[ "$HTTP_CODE" == 200 ]] && pass "GET /health -> 200" || fail "GET /health -> $HTTP_CODE"
	show
else
	fail "Cannot connect to API at $BASE_URL"
	exit 1
fi
header "Seed fresh jobs through POST /jobs"
if request -X POST "$BASE_URL/jobs" -H 'Content-Type: application/json' -d "{\"type\":\"echo\",\"payload\":{\"message\":\"ForgeQueue fixture echo A\",\"_forgequeue_test_fixture\":\"$MARKER\"}}"; then
	[[ "$HTTP_CODE" == 202 ]] && pass "Created echo A" || fail "Echo A returned HTTP $HTTP_CODE"
	show
else fail "Echo A request failed"; fi
ECHO_ID="$(field id)"
[[ -z "$ECHO_ID" ]] && ECHO_ID="$(field job.id)"
if request -X POST "$BASE_URL/jobs" -H 'Content-Type: application/json' -d "{\"type\":\"echo\",\"payload\":{\"message\":\"ForgeQueue fixture echo B\",\"_forgequeue_test_fixture\":\"$MARKER\"}}"; then
	[[ "$HTTP_CODE" == 202 ]] && pass "Created echo B" || fail "Echo B returned HTTP $HTTP_CODE"
	show
else fail "Echo B request failed"; fi
if request -X POST "$BASE_URL/jobs" -H 'Content-Type: application/json' -d "{\"type\":\"sleep\",\"payload\":{\"duration_seconds\":8,\"_forgequeue_test_fixture\":\"$MARKER\"}}"; then
	[[ "$HTTP_CODE" == 202 ]] && pass "Created 8-second sleep job" || fail "Sleep job returned HTTP $HTTP_CODE"
	show
else fail "Sleep job request failed"; fi
SLEEP_ID="$(field id)"
[[ -z "$SLEEP_ID" ]] && SLEEP_ID="$(field job.id)"
[[ -n "$ECHO_ID" ]] && pass "Echo ID: $ECHO_ID" || warn "Could not parse echo job ID"
[[ -n "$SLEEP_ID" ]] && pass "Sleep ID: $SLEEP_ID" || warn "Could not parse sleep job ID"

header "Idempotency"

IDEMPOTENCY_KEY="forgequeue-idempotency-${MARKER}"
IDEMPOTENCY_PAYLOAD="{\"message\":\"idempotency test\",\"_forgequeue_test_fixture\":\"$MARKER\"}"

# Test 1: A new key should create a job.
if request -X POST "$BASE_URL/jobs" \
	-H 'Content-Type: application/json' \
	-H "Idempotency-Key: $IDEMPOTENCY_KEY" \
	-d "{\"type\":\"echo\",\"payload\":$IDEMPOTENCY_PAYLOAD}"; then

	IDEMPOTENCY_ID="$(field id)"

	if [[ "$HTTP_CODE" == 202 && -n "$IDEMPOTENCY_ID" ]]; then
		pass "New idempotency key creates a job"
	else
		fail "Initial idempotent request returned HTTP $HTTP_CODE or no ID"
		show
	fi
else
	fail "Initial idempotent request failed"
	IDEMPOTENCY_ID=""
fi

# Test 2: Same key + same request should return the original job.
if [[ -n "$IDEMPOTENCY_ID" ]]; then
	if request -X POST "$BASE_URL/jobs" \
		-H 'Content-Type: application/json' \
		-H "Idempotency-Key: $IDEMPOTENCY_KEY" \
		-d "{\"type\":\"echo\",\"payload\":$IDEMPOTENCY_PAYLOAD}"; then

		REPLAY_ID="$(field id)"

		if [[ "$HTTP_CODE" == 200 && "$REPLAY_ID" == "$IDEMPOTENCY_ID" ]]; then
			pass "Same key and payload return the original job"
		else
			fail "Replay did not return the original job (HTTP $HTTP_CODE)"
			show
		fi
	else
		fail "Replay request failed"
	fi

	# Test 3: Same key + different payload should return 409.
	if request -X POST "$BASE_URL/jobs" \
		-H 'Content-Type: application/json' \
		-H "Idempotency-Key: $IDEMPOTENCY_KEY" \
		-d "{\"type\":\"echo\",\"payload\":{\"message\":\"different\",\"_forgequeue_test_fixture\":\"$MARKER\"}}"; then

		if [[ "$HTTP_CODE" == 409 ]]; then
			pass "Same key with different payload returns 409"
		else
			fail "Expected HTTP 409 for different payload; got $HTTP_CODE"
			show
		fi
	else
		fail "Conflicting idempotency request failed"
	fi
fi
header "List jobs"

header "Concurrent idempotency"

CONCURRENT_KEY="forgequeue-concurrent-${MARKER}"
CONCURRENT_PAYLOAD="{\"message\":\"concurrent idempotency test\",\"_forgequeue_test_fixture\":\"$MARKER\"}"
CONCURRENT_REQUESTS=10
CONCURRENT_DIR="$TMP/concurrent"
mkdir -p "$CONCURRENT_DIR"

# Launch all requests before collecting their results.
for i in $(seq 1 "$CONCURRENT_REQUESTS"); do
	(
		CODE="$(curl -sS --connect-timeout 3 --max-time 15 \
			-o "$CONCURRENT_DIR/body_$i" \
			-w '%{http_code}' \
			-X POST "$BASE_URL/jobs" \
			-H 'Content-Type: application/json' \
			-H "Idempotency-Key: $CONCURRENT_KEY" \
			-d "{\"type\":\"echo\",\"payload\":$CONCURRENT_PAYLOAD}" \
			2>"$CONCURRENT_DIR/error_$i")" || CODE=000

		printf '%s\n' "$CODE" >"$CONCURRENT_DIR/code_$i"
	) &
done

# Wait for every background request to finish.
wait

# Check that every request succeeded and returned a job ID.
CONCURRENT_IDS="$TMP/concurrent_ids"
: >"$CONCURRENT_IDS"
CONCURRENT_BAD=0

for i in $(seq 1 "$CONCURRENT_REQUESTS"); do
	CODE="$(cat "$CONCURRENT_DIR/code_$i" 2>/dev/null || echo 000)"
	BODY="$CONCURRENT_DIR/body_$i"

	ID="$(
		python3 - "$BODY" <<'PY'
import json,sys
try:
    data=json.load(open(sys.argv[1], encoding="utf-8"))
    print(data.get("id", ""))
except Exception:
    print("")
PY
	)"

	if [[ ("$CODE" == 200 || "$CODE" == 202) && -n "$ID" ]]; then
		printf '%s\n' "$ID" >>"$CONCURRENT_IDS"
	else
		CONCURRENT_BAD=$((CONCURRENT_BAD + 1))
		echo "[WARN] Concurrent request $i returned HTTP $CODE or no job ID"
		cat "$BODY" 2>/dev/null || true
		echo
	fi
done

if [[ "$CONCURRENT_BAD" == 0 ]] &&
	[[ "$(wc -l <"$CONCURRENT_IDS" | tr -d '[:space:]')" == "$CONCURRENT_REQUESTS" ]]; then
	pass "All concurrent requests returned successful responses and job IDs"
else
	fail "Some concurrent idempotency requests failed"
fi

# All successful responses must identify the same job.
UNIQUE_IDS="$(sort -u "$CONCURRENT_IDS" | wc -l | tr -d '[:space:]')"

if [[ "$CONCURRENT_BAD" == 0 && "$UNIQUE_IDS" == 1 ]]; then
	pass "Concurrent requests all returned the same job ID"
else
	fail "Concurrent requests returned $UNIQUE_IDS distinct job IDs"
	sort -u "$CONCURRENT_IDS"
fi

# The database must contain exactly one row for this idempotency key.
if CONCURRENT_COUNT="$(psql "$DB_CONN" -v ON_ERROR_STOP=1 -tA \
	-c "SELECT COUNT(*) FROM jobs WHERE idempotency_key = '$CONCURRENT_KEY';" \
	2>"$TMP/concurrent_db_err")"; then

	CONCURRENT_COUNT="$(echo "$CONCURRENT_COUNT" | tr -d '[:space:]')"

	if [[ "$CONCURRENT_COUNT" == "1" ]]; then
		pass "Concurrent requests created exactly one database row"
	else
		fail "Expected one row for concurrent key; found ${CONCURRENT_COUNT:-unknown}"
	fi
else
	fail "Could not verify concurrent idempotency row count"
	cat "$TMP/concurrent_db_err"
fi

# Test 4: Verify PostgreSQL contains exactly one job for this key.
if IDEMPOTENCY_COUNT="$(psql "$DB_CONN" -v ON_ERROR_STOP=1 -tA \
	-c "SELECT COUNT(*) FROM jobs WHERE idempotency_key = '$IDEMPOTENCY_KEY';" \
	2>"$TMP/dberr")"; then

	IDEMPOTENCY_COUNT="$(echo "$IDEMPOTENCY_COUNT" | tr -d '[:space:]')"

	if [[ "$IDEMPOTENCY_COUNT" == "1" ]]; then
		pass "Database contains exactly one job for the idempotency key"
	else
		fail "Expected one job for the key; found ${IDEMPOTENCY_COUNT:-unknown}"
	fi
else
	fail "Could not verify idempotency count in PostgreSQL"
	cat "$TMP/dberr"
fi

header "List jobs"
if request "$BASE_URL/jobs"; then
	[[ "$HTTP_CODE" == 200 ]] && pass "GET /jobs -> 200" || fail "GET /jobs -> $HTTP_CODE"
	show
else fail "GET /jobs failed"; fi
header "Pagination"
if request "$BASE_URL/jobs?limit=5&offset=0"; then
	[[ "$HTTP_CODE" == 200 ]] && pass "Pagination -> 200" || fail "Pagination -> $HTTP_CODE"
	show
else fail "Pagination failed"; fi
header "Status filters"
for st in queued running retry_wait succeeded dead; do
	if request "$BASE_URL/jobs?status=$st"; then
		[[ "$HTTP_CODE" == 200 ]] && pass "status=$st -> 200" || fail "status=$st -> $HTTP_CODE"
		show
	else fail "status=$st request failed"; fi
done
header "Get seeded echo by ID"
if [[ -n "$ECHO_ID" ]]; then
	if request "$BASE_URL/jobs/$ECHO_ID"; then
		[[ "$HTTP_CODE" == 200 ]] && pass "GET /jobs/{id} -> 200" || fail "GET by ID -> $HTTP_CODE"
		show
	else fail "GET by ID failed"; fi
else warn "Skipped GET by ID"; fi
header "Observe sleep state"
if [[ -n "$SLEEP_ID" ]]; then
	echo "Polling up to 20 seconds. Worker must be running in another terminal."
	SEEN_RUNNING=0
	STATUS=""
	for _ in $(seq 1 40); do
		if request "$BASE_URL/jobs/$SLEEP_ID"; then
			STATUS="$(field status)"
			[[ -z "$STATUS" ]] && STATUS="$(field job.status)"
			if [[ "$STATUS" == running && "$SEEN_RUNNING" == 0 ]]; then
				SEEN_RUNNING=1
				echo 'Observed state: running'
				show
			fi
			[[ "$STATUS" == succeeded || "$STATUS" == dead ]] && break
		fi
		sleep 0.5
	done
	((SEEN_RUNNING)) && pass "Observed running state" || warn "Did not observe running state; check worker startup/polling"
	[[ "$STATUS" == succeeded ]] && pass "Sleep job reached succeeded" || warn "Final sleep status: ${STATUS:-unknown}"
	request "$BASE_URL/jobs/$SLEEP_ID" && show
else warn "Skipped sleep polling"; fi


header "Expired-lease recovery"

# Generate the job UUID before inserting the fixture.
RECOVERY_ID="$(python3 -c 'import uuid; print(uuid.uuid4())')"

# Create a running job with an expired lease and exhausted attempts.
if psql "$DB_CONN" -v ON_ERROR_STOP=1 -tA -c "
    INSERT INTO jobs (
        id,
        job_type,
        payload,
        status,
        attempt_count,
        max_attempts,
        available_at,
        claim_token,
        lease_expires_at
    ) VALUES (
        '$RECOVERY_ID',
        'echo',
        jsonb_build_object(
            'message', 'lease recovery test',
            '_forgequeue_test_fixture', '$MARKER'
        ),
        'running',
        3,
        3,
        NOW() - INTERVAL '1 minute',
        gen_random_uuid(),
        NOW() - INTERVAL '1 minute'
    );
" 2>"$TMP/recovery_err"; then
    pass "Created expired-lease fixture: $RECOVERY_ID"
else
    fail "Could not create expired-lease fixture"
    cat "$TMP/recovery_err"
    RECOVERY_ID=""
fi

if [[ -n "$RECOVERY_ID" ]]; then
    # Wait for the worker to detect and recover the expired lease.
    RECOVERY_STATE=""
    for _ in $(seq 1 40); do
        RECOVERY_STATE="$(
            psql "$DB_CONN" -v ON_ERROR_STOP=1 -tA -F '|' -c "
                SELECT
                    status,
                    (lease_expires_at IS NULL)::text,
                    (claim_token IS NULL)::text,
                    (last_error LIKE '%attempts were exhausted%')::text
                FROM jobs
                WHERE id = '$RECOVERY_ID';
            " 2>"$TMP/recovery_err"
        )" || {
            fail "Could not query recovered fixture"
            cat "$TMP/recovery_err"
            break
        }

        [[ "$RECOVERY_STATE" == "dead|true|true|true" ]] && break
        sleep 0.25
    done

    if [[ "$RECOVERY_STATE" == "dead|true|true|true" ]]; then
        pass "Expired lease with exhausted attempts becomes dead"
        pass "Recovery clears claim token and lease"
        pass "Recovery records the exhausted-attempts explanation"
    else
        fail "Expired lease was not recovered as expected"
        echo "Observed state: ${RECOVERY_STATE:-unknown}"
    fi
fi


header "Summary"
printf 'Passed: %d | Failed: %d | Warnings: %d\n' "$PASS" "$FAIL" "$WARN"
((FAIL == 0)) || exit 1
echo "Test finished. Marked fixtures are removed automatically on exit."
