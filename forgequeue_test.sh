#!/usr/bin/env bash
# ForgeQueue smoke tests. Run from project root after starting API and worker.
# Requires curl, python3, psql, and DATABASE_URL or DB_URL (environment or .env).
# Only deletes rows tagged by this script; never use against production.
set -u
BASE_URL="${BASE_URL:-http://localhost:8080}"
MARKER="forgequeue_test.sh"
TMP="$(mktemp -d)"
PASS=0; FAIL=0; WARN=0
pass(){ echo "[PASS] $1"; PASS=$((PASS+1)); }
fail(){ echo "[FAIL] $1"; FAIL=$((FAIL+1)); }
warn(){ echo "[WARN] $1"; WARN=$((WARN+1)); }
header(){ printf '\n== %s ==\n' "$1"; }
for c in curl python3 psql; do
  if ! command -v "$c" >/dev/null 2>&1; then
    echo "Missing required command: $c"
    [[ "$c" != psql ]] || echo "Install with: sudo apt install postgresql-client"
    exit 1
  fi
done
if [[ -z "${DATABASE_URL:-}" && -z "${DB_URL:-}" && -f .env ]]; then
  ENV_URL="$(python3 - <<'PY'
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
DB_CONN="${DATABASE_URL:-${DB_URL:-}}"
if [[ -z "$DB_CONN" ]]; then
  echo "Set DB_URL or DATABASE_URL in .env or environment. No jobs created."
  exit 1
fi
request(){
  HTTP_CODE="$(curl -sS --connect-timeout 3 --max-time 15 -o "$TMP/body" -w '%{http_code}' "$@" 2>"$TMP/curlerr")" || { HTTP_CODE=000; cat "$TMP/curlerr"; return 1; }
}
field(){
  python3 - "$1" "$TMP/body" <<'PY'
import json,sys
try:
    x=json.load(open(sys.argv[2],encoding='utf-8'))
    for k in sys.argv[1].split('.'): x=x[k]
    print('' if x is None else x)
except Exception: print('')
PY
}
show(){ python3 -m json.tool "$TMP/body" 2>/dev/null || cat "$TMP/body"; echo; }
db_cleanup(){ psql "$DB_CONN" -v ON_ERROR_STOP=1 -q -c "DELETE FROM jobs WHERE payload->>'_forgequeue_test_fixture' = '$MARKER';" >"$TMP/dbout" 2>"$TMP/dberr"; }
cleanup(){
  code=$?; trap - EXIT
  if [[ -n "${DB_CONN:-}" ]] && command -v psql >/dev/null 2>&1; then
    if db_cleanup; then echo; echo "Cleaned up marked ForgeQueue test jobs."
    else echo "[WARN] Database cleanup failed:"; cat "$TMP/dberr" 2>/dev/null || true; fi
  fi
  rm -rf "$TMP"; exit "$code"
}
trap cleanup EXIT
echo "ForgeQueue smoke tests | API: $BASE_URL"
echo "Only jobs marked _forgequeue_test_fixture=$MARKER will be deleted."
echo "Use a development/test database only."
header "Clean stale fixtures from previous run"
if db_cleanup; then pass "Removed stale marked fixtures"; else fail "Cannot clean fixtures; stopping before creating jobs"; cat "$TMP/dberr"; exit 1; fi
header "Health"
if request "$BASE_URL/health"; then [[ "$HTTP_CODE" == 200 ]] && pass "GET /health -> 200" || fail "GET /health -> $HTTP_CODE"; show
else fail "Cannot connect to API at $BASE_URL"; exit 1; fi
header "Seed fresh jobs through POST /jobs"
if request -X POST "$BASE_URL/jobs" -H 'Content-Type: application/json' -d "{\"type\":\"echo\",\"payload\":{\"message\":\"ForgeQueue fixture echo A\",\"_forgequeue_test_fixture\":\"$MARKER\"}}"; then
  [[ "$HTTP_CODE" == 202 ]] && pass "Created echo A" || fail "Echo A returned HTTP $HTTP_CODE"; show
else fail "Echo A request failed"; fi
ECHO_ID="$(field id)"; [[ -z "$ECHO_ID" ]] && ECHO_ID="$(field job.id)"
if request -X POST "$BASE_URL/jobs" -H 'Content-Type: application/json' -d "{\"type\":\"echo\",\"payload\":{\"message\":\"ForgeQueue fixture echo B\",\"_forgequeue_test_fixture\":\"$MARKER\"}}"; then
  [[ "$HTTP_CODE" == 202 ]] && pass "Created echo B" || fail "Echo B returned HTTP $HTTP_CODE"; show
else fail "Echo B request failed"; fi
if request -X POST "$BASE_URL/jobs" -H 'Content-Type: application/json' -d "{\"type\":\"sleep\",\"payload\":{\"duration_seconds\":8,\"_forgequeue_test_fixture\":\"$MARKER\"}}"; then
  [[ "$HTTP_CODE" == 202 ]] && pass "Created 8-second sleep job" || fail "Sleep job returned HTTP $HTTP_CODE"; show
else fail "Sleep job request failed"; fi
SLEEP_ID="$(field id)"; [[ -z "$SLEEP_ID" ]] && SLEEP_ID="$(field job.id)"
[[ -n "$ECHO_ID" ]] && pass "Echo ID: $ECHO_ID" || warn "Could not parse echo job ID"
[[ -n "$SLEEP_ID" ]] && pass "Sleep ID: $SLEEP_ID" || warn "Could not parse sleep job ID"
header "List jobs"
if request "$BASE_URL/jobs"; then [[ "$HTTP_CODE" == 200 ]] && pass "GET /jobs -> 200" || fail "GET /jobs -> $HTTP_CODE"; show
else fail "GET /jobs failed"; fi
header "Pagination"
if request "$BASE_URL/jobs?limit=5&offset=0"; then [[ "$HTTP_CODE" == 200 ]] && pass "Pagination -> 200" || fail "Pagination -> $HTTP_CODE"; show
else fail "Pagination failed"; fi
header "Status filters"
for st in queued running retry_wait succeeded dead; do
  if request "$BASE_URL/jobs?status=$st"; then [[ "$HTTP_CODE" == 200 ]] && pass "status=$st -> 200" || fail "status=$st -> $HTTP_CODE"; show
  else fail "status=$st request failed"; fi
done
header "Get seeded echo by ID"
if [[ -n "$ECHO_ID" ]]; then
  if request "$BASE_URL/jobs/$ECHO_ID"; then [[ "$HTTP_CODE" == 200 ]] && pass "GET /jobs/{id} -> 200" || fail "GET by ID -> $HTTP_CODE"; show
  else fail "GET by ID failed"; fi
else warn "Skipped GET by ID"; fi
header "Observe sleep state"
if [[ -n "$SLEEP_ID" ]]; then
  echo "Polling up to 20 seconds. Worker must be running in another terminal."
  SEEN_RUNNING=0; STATUS=""
  for _ in $(seq 1 40); do
    if request "$BASE_URL/jobs/$SLEEP_ID"; then
      STATUS="$(field status)"; [[ -z "$STATUS" ]] && STATUS="$(field job.status)"
      if [[ "$STATUS" == running && "$SEEN_RUNNING" == 0 ]]; then SEEN_RUNNING=1; echo 'Observed state: running'; show; fi
      [[ "$STATUS" == succeeded || "$STATUS" == dead ]] && break
    fi
    sleep 0.5
  done
  (( SEEN_RUNNING )) && pass "Observed running state" || warn "Did not observe running state; check worker startup/polling"
  [[ "$STATUS" == succeeded ]] && pass "Sleep job reached succeeded" || warn "Final sleep status: ${STATUS:-unknown}"
  request "$BASE_URL/jobs/$SLEEP_ID" && show
else warn "Skipped sleep polling"; fi
header "Summary"
printf 'Passed: %d | Failed: %d | Warnings: %d\n' "$PASS" "$FAIL" "$WARN"
(( FAIL == 0 )) || exit 1
echo "Test finished. Marked fixtures are removed automatically on exit."
