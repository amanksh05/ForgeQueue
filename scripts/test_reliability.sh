
#!/usr/bin/env bash
set -uo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

PASS=0
FAIL=0
MISSING=0

record() {
    local status="$1"
    local name="$2"
    printf '[%s] %s\n' "$status" "$name"

    if [[ "$status" == "PASS" ]]; then
        ((PASS += 1))
    else
        ((FAIL += 1))
    fi
}

run_check() {
    local name="$1"
    shift

    echo
    echo "========== $name =========="

    if "$@"; then
        record PASS "$name"
    else
        record FAIL "$name"
    fi
}

echo "ForgeQueue Reliability Test Runner"
echo "Repository: $ROOT_DIR"

if [[ ! -f .env ]]; then
    echo "[FAIL] .env not found"
    exit 1
fi

set -a
source .env
set +a

if [[ -z "${TEST_DATABASE_URL:-}" ]]; then
    echo "[FAIL] TEST_DATABASE_URL is missing from .env"
    exit 1
fi

for command in go docker curl grep; do
    if ! command -v "$command" >/dev/null 2>&1; then
        echo "[FAIL] Required command not found: $command"
        exit 1
    fi
done

if [[ ! -f forgequeue_test.sh ]]; then
    echo "[FAIL] forgequeue_test.sh not found"
    exit 1
fi

# --------------------------------------------------
# 1. Verify required reliability test coverage exists.
# These names define the test coverage contract.
# Missing tests are reported separately from code failures.
# --------------------------------------------------


declare -a REQUIRED_TESTS=(
    "TestRetryDelay_BoundedExponentialBackoff"
    "TestClaimJob_OnlyClaimsAvailableJobsAndIncrementsAttempt"
    "TestRecoverExpiredJobs_RetryAndDeadTransitions"
    "TestRecoverExpiredJobs_ClaimableAfterRecovery"
    "TestFailJob_RetryUsesBackoffAndClearsLease"
    "TestFailJob_ExhaustedAttemptsBecomeDead"
    "TestClaimJob_ConcurrentWorkersClaimDifferentJobs"
)


echo
echo "========== Reliability coverage audit =========="

for test_name in "${REQUIRED_TESTS[@]}"; do
    if grep -R -E "func ${test_name}\(" \
        --include='*_test.go' internal >/dev/null 2>&1; then
        record PASS "Test exists: $test_name"
    else
        echo "[MISSING] $test_name"
        ((MISSING += 1))
        ((FAIL += 1))
    fi
done

# --------------------------------------------------
# 2. Start PostgreSQL and wait for readiness.
# --------------------------------------------------

run_check "Start PostgreSQL" docker compose up -d db

db_ready=0
for _ in $(seq 1 30); do
    if docker compose exec -T db pg_isready >/dev/null 2>&1; then
        db_ready=1
        break
    fi
    sleep 1
done

if [[ "$db_ready" -eq 1 ]]; then
    record PASS "PostgreSQL readiness"
else
    record FAIL "PostgreSQL readiness"
    echo "Inspect: docker compose logs --tail=100 db"
fi

# --------------------------------------------------
# 3. Run targeted reliability tests that exist.
# The full suite below also runs these tests.
# --------------------------------------------------

EXISTING_PATTERN=""

for test_name in "${REQUIRED_TESTS[@]}"; do
    if grep -R -E "func ${test_name}\(" \
        --include='*_test.go' internal >/dev/null 2>&1; then
        if [[ -n "$EXISTING_PATTERN" ]]; then
            EXISTING_PATTERN+="|"
        fi
        EXISTING_PATTERN+="$test_name"
    fi
done

# The app's worker could interfere with tests that manipulate
# running jobs, so stop it before the Go tests.
echo
echo "Stopping app container before database integration tests..."
docker compose stop app >/dev/null 2>&1 || true

if [[ -n "$EXISTING_PATTERN" ]]; then
    run_check "Targeted reliability tests" \
        env DATABASE_URL="$TEST_DATABASE_URL" \
        go test -count=1 -v ./... -run "$EXISTING_PATTERN"
else
    echo "[SKIP] No named reliability tests exist yet."
fi

run_check "Complete Go test suite" \
    env DATABASE_URL="$TEST_DATABASE_URL" go test -count=1 ./...

# --------------------------------------------------
# 4. Start the app and check its health endpoint.
# --------------------------------------------------

run_check "Start application" docker compose up -d app

api_ready=0
for _ in $(seq 1 30); do
    if curl --silent --fail http://localhost:8080/health >/dev/null; then
        api_ready=1
        break
    fi
    sleep 1
done

if [[ "$api_ready" -eq 1 ]]; then
    record PASS "API health check"

    run_check "API and worker smoke tests" \
        bash ./forgequeue_test.sh
else
    record FAIL "API health check"
    echo "Inspect: docker compose logs --tail=100 app"
fi

# --------------------------------------------------
# 5. Summary and diagnostics.
# --------------------------------------------------

echo
echo "=================================================="
echo "RELIABILITY TEST SUMMARY"
echo "=================================================="
echo "Passed: $PASS"
echo "Failed: $FAIL"
echo "Missing reliability tests: $MISSING"

if [[ "$FAIL" -gt 0 ]]; then
    echo
    echo "RESULT: INCOMPLETE OR FAILING"
    echo "Missing tests indicate missing coverage, not necessarily a code bug."
    echo
    echo "Diagnostics:"
    echo "  docker compose ps"
    echo "  docker compose logs --tail=100 db"
    echo "  docker compose logs --tail=100 app"
    exit 1
fi

echo
echo "RESULT: ALL CHECKS PASSED"
exit 0
