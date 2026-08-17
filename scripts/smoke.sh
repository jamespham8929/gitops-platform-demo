#!/usr/bin/env bash
# Post-deploy smoke test. Probes the endpoints a caller actually depends on and
# fails loudly if any of them regressed, so a promotion that synced cleanly but
# broke behaviour still gets caught.
#
#   scripts/smoke.sh <base-url> [expected-version]
#
# The version argument, when given, asserts the deployed build is the one that
# was promoted rather than a stale ReplicaSet still serving traffic.
#
# Set SMOKE_HEADER to send an extra header on every request. The promotion gate
# uses it to pin requests to the canary ReplicaSet through the canary ingress,
# so the suite reports on the new build rather than on whichever pod the load
# balancer happened to pick.
set -euo pipefail

BASE_URL="${1:?usage: smoke.sh <base-url> [expected-version]}"
EXPECTED_VERSION="${2:-}"
SMOKE_HEADER="${SMOKE_HEADER:-}"
FAILURES=0

CURL_OPTS=(-sS --max-time 10)
if [[ -n "$SMOKE_HEADER" ]]; then
  CURL_OPTS+=(-H "$SMOKE_HEADER")
  echo "Sending header: ${SMOKE_HEADER}"
fi

fail() {
  echo "FAIL: $*" >&2
  FAILURES=$((FAILURES + 1))
}

pass() {
  echo "ok: $*"
}

# request METHOD PATH [BODY] -> prints "status<newline>body"
request() {
  local method="$1" path="$2" body="${3:-}"
  local args=("${CURL_OPTS[@]}" -o /tmp/smoke-body -w '%{http_code}' -X "$method" "${BASE_URL}${path}")
  if [[ -n "$body" ]]; then
    args+=(-H 'Content-Type: application/json' -d "$body")
  fi
  local code
  code="$(curl "${args[@]}")"
  printf '%s\n' "$code"
  cat /tmp/smoke-body
}

expect_status() {
  local name="$1" method="$2" path="$3" want="$4" body="${5:-}"
  local out got
  out="$(request "$method" "$path" "$body")"
  got="$(head -n1 <<<"$out")"
  if [[ "$got" == "$want" ]]; then
    pass "$name ($method $path -> $got)"
  else
    fail "$name: expected $want from $method $path, got $got: $(tail -n +2 <<<"$out")"
  fi
}

echo "Smoke testing ${BASE_URL}"

expect_status "liveness"  GET  /health      200
expect_status "readiness" GET  /ready       200
expect_status "info"      GET  /api/v1/info 200

# A valid order must price and return 201.
expect_status "order accepted" POST /api/v1/orders 201 \
  '{"customer_id":"smoke","items":[{"sku":"widget","quantity":2,"unit_cents":500}]}'

# Validation must still reject bad input rather than 500 on it. A canary that
# turns 400s into 500s passes a liveness probe and fails here.
expect_status "empty order rejected" POST /api/v1/orders 400 \
  '{"customer_id":"smoke","items":[]}'
expect_status "unknown coupon rejected" POST /api/v1/orders 422 \
  '{"customer_id":"smoke","items":[{"sku":"widget","quantity":1,"unit_cents":100}],"coupon":"NOPE"}'

# The analysis query is only as good as the metric it reads, so check the series
# is actually being exported.
metrics="$(curl "${CURL_OPTS[@]}" "${BASE_URL}/metrics" || true)"
if grep -q '^http_requests_total{' <<<"$metrics"; then
  pass "http_requests_total exported"
else
  fail "http_requests_total missing from /metrics; canary analysis would have no signal"
fi

if [[ -n "$EXPECTED_VERSION" ]]; then
  deployed="$(curl "${CURL_OPTS[@]}" "${BASE_URL}/api/v1/info" | sed -n 's/.*"version":"\([^"]*\)".*/\1/p')"
  if [[ "$deployed" == "$EXPECTED_VERSION" ]]; then
    pass "serving version ${deployed}"
  else
    fail "expected version ${EXPECTED_VERSION}, service reports '${deployed}'"
  fi
fi

if (( FAILURES > 0 )); then
  echo "${FAILURES} smoke check(s) failed" >&2
  exit 1
fi
echo "All smoke checks passed"
