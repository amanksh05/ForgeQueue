
#!/usr/bin/env bash

set -uo pipefail

# Run from the ForgeQueue repository root.
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

RESULT_NAMES=()
RESULT_STATUSES=()

pass_count=0
fail_count=0

run_step() {
    local name="$1"
    shift

    echo
    echo "=================================================="
    echo "TEST: $name"
    echo "=================================================="

    if "$@"; then
        echo "[PASS] $name"
        RESULT_NAMES+=("$name")
        RESULT_STATUSES+=("PASS")
        ((pass_count += 1))
    else
        local rc=$?
        echo "[FAIL] $name (exit code: $rc)"
        RESULT_NAMES+=("$name")
        RESULT_STATUSES+=("FAIL")
        ((fail_count += 1))
    fi
}

echo "ForgeQueue full test runner"
echo "Repository: $ROOT_DIR"

# Load local environment without printing secrets.
if [[ ! -f .env ]]; then
    echo "[FAIL] .env file not found"
    exit 1
fi

set -a
source .env
set +a

if [[ -z "${TEST_DATABASE_URL:-}" ]]; then
    echo "[FAIL] TEST_DATABASE_URL is missing from .env"
    echo "It should point to your Docker PostgreSQL database via localhost:5433."
    exit 1
fi

if ! command -v docker >/dev/null 2>&1; then
    echo "[FAIL] docker command not found"
    exit 1
fi

if ! command -v go >/dev/null 2>&1; then
    echo "[FAIL] go command not found"
    exit 1
fi

if ! command -v curl >/dev/null 2>&1; then
    echo "[FAIL] curl command not found; needed for the API health check"
    exit 1
fi

if [[ ! -f ./forgequeue_test.sh ]]; then
    echo "[FAIL] ./forgequeue_test.sh not found"
    exit 1
fi

# Stop the worker before store integration tests so it cannot race
# with tests that deliberately insert expired running jobs.
echo
echo "Stopping the app temporarily for isolated store tests..."
docker compose stop app >/dev/null 2>&1 || true

run_step "Start PostgreSQL container" docker compose up -d db

echo
echo "Waiting for PostgreSQL..."
db_ready=0

for _ in $(seq 1 30); do
    if docker compose exec -T db pg_isready >/dev/null 2>&1; then
        db_ready=1
        break
    fi
    sleep 1
done

if [[ "$db_ready" -ne 1 ]]; then
    echo "[FAIL] PostgreSQL did not become ready within 30 seconds."
    echo "Inspect with: docker compose logs db"
    exit 1
fi

echo "[PASS] PostgreSQL is ready"

# Go integration tests use DATABASE_URL. Override it only for this
# command so Docker Compose still gets its normal app DATABASE_URL.
run_step "Go unit and integration tests" \
    env DATABASE_URL="$TEST_DATABASE_URL" go test -count=1 ./...

# Start the app only after the Go tests, to avoid worker interference.
run_step "Start application container" docker compose up -d app

echo
echo "Waiting for API health endpoint..."
api_ready=0

for _ in $(seq 1 30); do
    if curl --silent --fail http://localhost:8080/health >/dev/null; then
        api_ready=1
        break
    fi
    sleep 1
done

if [[ "$api_ready" -ne 1 ]]; then
    echo "[FAIL] API did not become healthy within 30 seconds."
    echo "Inspect with: docker compose logs --tail=100 app"
    RESULT_NAMES+=("API health check")
    RESULT_STATUSES+=("FAIL")
    ((fail_count += 1))
else
    echo "[PASS] API health check"

    # Existing script handles API/worker behavior and its own fixtures.
    run_step "Existing API and worker smoke tests" bash ./forgequeue_test.sh
fi

echo
echo "=================================================="
echo "FORGEQUEUE TEST SUMMARY"
echo "=================================================="

for i in "${!RESULT_NAMES[@]}"; do
    printf '[%s] %s\n' "${RESULT_STATUSES[$i]}" "${RESULT_NAMES[$i]}"
done

echo
echo "Passed: $pass_count"
echo "Failed: $fail_count"

if [[ "$fail_count" -gt 0 ]]; then
    echo
    echo "Some checks failed. Review the FIRST failing test output above."
    echo "Useful diagnostics:"
    echo "  docker compose ps"
    echo "  docker compose logs --tail=100 db"
    echo "  docker compose logs --tail=100 app"
    exit 1
fi

echo
echo "All checks passed!"
exit 0
