#!/usr/bin/env bash
# Smoke-validate the stateless streamable HTTP transport against a real server.
#
# Builds (or reuses) the binary, serves it on a local port, and asserts the
# guarantees a stateless deployment makes over the wire — the ones a unit test
# can only approximate, because they depend on the process actually listening:
#
#   1. GET /health           → 200 JSON status/version/commit/started_at/uptime_seconds
#      GET server-card.json  → 200 JSON serverInfo/tools/prompts (discovery is unaffected)
#      GET /server-card      → 200 application/mcp-server-card+json, byte-identical
#                              (the current location; the .well-known one is legacy)
#   2. POST / tools/list     → 200, no Mcp-Session-Id, lists `search`
#                              (a bare POST is a complete request: no initialize,
#                               no session handshake)
#   3. GET /                 → 405 with `Allow: POST`  (SEP-2567 / RFC 9110), and
#                              only on the MCP endpoint
#   4. GET /nope             → 404 JSON naming the endpoint, not the 405 a catch-all
#                              used to give every unknown path
#   5. the five security headers on every response, asserted where an inner layer
#      writes the response itself (the 404) as well as on a plain route
#   6. POST with --json-response → Content-Type: application/json
#
# Usage: validate-http-stateless.sh [binary|docker] [port]
#
#   binary   (default) build with `go build` and run the binary directly
#   docker   build the image and run it exactly as the Dockerfile entrypoint does
#   [port]   host port to serve on (default: 18080)
#
# Exit status is 0 only when every assertion holds.

set -euo pipefail

MODE="${1:-binary}"
PORT="${2:-18080}"
BASE="http://127.0.0.1:${PORT}"
IMAGE="libgen-mcp:validate-stateless"
BIN="dist/libgen-mcp-validate"
SERVER_PID=""
CONTAINER=""
FAILURES=0

cleanup() {
  if [ -n "$SERVER_PID" ]; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
  if [ -n "$CONTAINER" ]; then
    docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

fail() {
  echo "  FAIL: $1"
  FAILURES=$((FAILURES + 1))
}

pass() {
  echo "  ok: $1"
}

# start_server launches the server with the given extra flags and blocks until
# GET /health answers, so no assertion races the listener.
start_server() {
  case "$MODE" in
    binary)
      "./$BIN" --http ":${PORT}" "$@" >/dev/null 2>&1 &
      SERVER_PID=$!
      ;;
    docker)
      CONTAINER=$(docker run -d -p "${PORT}:8080" "$IMAGE" --http :8080 "$@")
      ;;
    *)
      echo "unknown mode: $MODE (want 'binary' or 'docker')" >&2
      exit 2
      ;;
  esac

  for _ in $(seq 1 50); do
    if [ "$(curl -fsS -o /dev/null -w '%{http_code}' "${BASE}/health" 2>/dev/null)" = "200" ]; then
      return 0
    fi
    sleep 0.2
  done
  echo "server did not become healthy on ${BASE}/health" >&2
  exit 1
}

# stop_server tears the current server down so the next case starts clean.
stop_server() {
  cleanup
  SERVER_PID=""
  CONTAINER=""
}

# post_mcp sends a JSON-RPC body to the MCP endpoint with the headers the
# streamable transport requires, writing the response headers to $1 and the body
# to stdout.
post_mcp() {
  local headers_file="$1" body="$2"
  curl -sS -D "$headers_file" \
    -H 'Content-Type: application/json' \
    -H 'Accept: application/json, text/event-stream' \
    -d "$body" \
    "${BASE}/"
}

# header_value extracts a response header's value (case-insensitive), trimming
# the trailing CR curl leaves in place. An absent header yields the empty string
# rather than a failure — under `set -e` with pipefail, grep's no-match status
# would otherwise abort the run on exactly the case we are asserting.
header_value() {
  local headers_file="$1" name="$2"
  grep -i "^${name}:" "$headers_file" | head -n1 | cut -d: -f2- | tr -d '\r' | sed 's/^ *//' || true
}

# assert_security_headers checks the headers the outermost middleware sets on
# every response against the dump in $1, naming the response in $2. Passing a
# dump written by a route the middleware does not itself answer is the point: the
# headers have to survive whatever inner layer produced the response.
assert_security_headers() {
  local headers_file="$1" what="$2" name expected got
  while IFS='|' read -r name expected; do
    [ -n "$name" ] || continue
    got=$(header_value "$headers_file" "$name")
    if [ "$got" = "$expected" ]; then
      pass "$what carries ${name}: ${expected}"
    else
      fail "$what has $name '${got:-absent}', want '${expected}'"
    fi
  done <<'HEADERS_EOF'
X-Content-Type-Options|nosniff
X-Frame-Options|DENY
Referrer-Policy|no-referrer
Content-Security-Policy|default-src 'none'; frame-ancestors 'none'
Cache-Control|no-store
HEADERS_EOF
}

echo "Building (mode: $MODE)..."
case "$MODE" in
  binary) go build -o "$BIN" ./cmd/server ;;
  docker) docker build -q -t "$IMAGE" . >/dev/null ;;
esac

TOOLS_LIST='{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}'
HEADERS=$(mktemp)
trap 'cleanup; rm -f "$HEADERS"' EXIT

echo
echo "Stateless defaults (${BASE}):"
start_server

# The body is JSON carrying status/version/commit, the same contract the sibling
# gitlab-mcp-server serves, so an external probe can read both the same way.
HEALTH=$(curl -fsS "${BASE}/health")
if printf '%s' "$HEALTH" | grep -q '"status":"ok"'; then
  pass "GET /health reports status ok"
else
  fail "GET /health did not report status ok (body: ${HEALTH})"
fi
if printf '%s' "$HEALTH" | grep -qE '"version":"[^"]+"' && printf '%s' "$HEALTH" | grep -q '"commit":'; then
  pass "GET /health carries version and commit"
else
  fail "GET /health is missing version or commit (body: ${HEALTH})"
fi

# started_at lets a monitor spot a restart; uptime_seconds is the derived value.
# The uptime pattern ends at a non-digit on purpose: an unanchored [0-9]+ also
# matches the integer part of a decimal, so it would accept 1.5 and never catch
# the field turning into a float.
if printf '%s' "$HEALTH" | grep -qE '"started_at":"[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9:]+Z"' \
  && printf '%s' "$HEALTH" | grep -qE '"uptime_seconds":[0-9]+([,}]|$)'; then
  pass "GET /health carries started_at and uptime_seconds"
else
  fail "GET /health is missing started_at or uptime_seconds (body: ${HEALTH})"
fi

# The server card is a separate route, so stateless mode's 405 on the MCP
# endpoint must not reach it: a scanner fetches this with a plain GET.
CARD=$(curl -fsS "${BASE}/.well-known/mcp/server-card.json" 2>/dev/null || true)
if printf '%s' "$CARD" | grep -q '"serverInfo"'; then
  pass "GET /.well-known/mcp/server-card.json serves the card"
else
  fail "server card not served (body: ${CARD})"
fi
if printf '%s' "$CARD" | grep -q '"prompts"'; then
  pass "the card enumerates prompts"
else
  fail "the card does not enumerate prompts"
fi
if printf '%s' "$CARD" | grep -q '"capabilities"'; then
  pass "the card states the negotiated capabilities"
else
  fail "the card does not state capabilities"
fi

# The card moved to /server-card on 2026-06-08 (ext-server-card); the .well-known
# path above is kept because scanners already fetch it. Both are served, and a
# client that follows either must get the same surface — so compare the bytes, not
# just the status.
CARD_CURRENT=$(curl -fsS "${BASE}/server-card" 2>/dev/null || true)
if [ -n "$CARD_CURRENT" ] && [ "$CARD_CURRENT" = "$CARD" ]; then
  pass "GET /server-card serves the same document as the legacy path"
else
  fail "GET /server-card did not serve the legacy path's bytes"
fi

# The media type differs by location on purpose: the extension gives the document
# its own type, while the legacy path keeps the application/json its existing
# readers compare literally.
curl -sS -o /dev/null -D "$HEADERS" "${BASE}/server-card"
CARD_TYPE=$(header_value "$HEADERS" "Content-Type")
case "$CARD_TYPE" in
  application/mcp-server-card+json*) pass "GET /server-card declares the server-card media type" ;;
  *) fail "GET /server-card Content-Type was '${CARD_TYPE:-absent}', want application/mcp-server-card+json" ;;
esac

# The card is the one route that overrides the no-store above: it changes only
# with a release, so a scanner may hold on to it.
CARD_CACHE=$(header_value "$HEADERS" "Cache-Control")
if [ "$CARD_CACHE" = "public, max-age=3600" ]; then
  pass "the card overrides Cache-Control with its own lifetime"
else
  fail "server card Cache-Control is '${CARD_CACHE:-absent}', want 'public, max-age=3600'"
fi

# A browser-based directory reads the card cross-origin, which no unit test can
# assert end to end: the header has to survive the real handler and the wire.
CARD_ORIGIN=$(curl -fsSI "${BASE}/.well-known/mcp/server-card.json" 2>/dev/null | tr -d '\r' | awk -F': ' 'tolower($1)=="access-control-allow-origin" {print $2}')
if [ "$CARD_ORIGIN" = "*" ]; then
  pass "the card is readable cross-origin (Access-Control-Allow-Origin: *)"
else
  fail "server card Access-Control-Allow-Origin is '${CARD_ORIGIN}', want *"
fi

BODY=$(post_mcp "$HEADERS" "$TOOLS_LIST")
STATUS=$(head -n1 "$HEADERS" | awk '{print $2}')
if [ "$STATUS" = "200" ]; then
  pass "POST tools/list without initialize returns 200"
else
  fail "POST tools/list returned $STATUS, want 200 (body: $BODY)"
fi

SESSION=$(header_value "$HEADERS" "Mcp-Session-Id")
if [ -z "$SESSION" ]; then
  pass "no Mcp-Session-Id header"
else
  fail "server handed out Mcp-Session-Id: $SESSION"
fi

# The transport SHOULD, asserted where it is actually observable: the unit test
# proves the handler sets it, only this proves it survives to the wire.
BUFFERING=$(header_value "$HEADERS" "X-Accel-Buffering")
if [ "$BUFFERING" = "no" ]; then
  pass "the SSE response tells proxies not to buffer"
else
  fail "X-Accel-Buffering is '${BUFFERING:-absent}', want 'no'"
fi

# The default refuses cross-origin browser calls, and must keep letting through
# a client that sends no Origin at all — the regression that would break every
# CLI and desktop client while the browser cases looked fine.
CROSS=$(curl -sS -o /dev/null -w '%{http_code}' -X POST "${BASE}/" \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -H 'Origin: https://evil.example' -H 'Sec-Fetch-Site: cross-site' \
  -d "$TOOLS_LIST" || true)
if [ "$CROSS" = "403" ]; then
  pass "a cross-origin browser POST is refused without --trusted-origins"
else
  fail "cross-origin POST returned $CROSS, want 403"
fi

case "$BODY" in
  *'"search"'*) pass "tools/list advertises search" ;;
  *) fail "tools/list did not advertise search (body: $BODY)" ;;
esac

STATUS=$(curl -sS -o /dev/null -D "$HEADERS" -w '%{http_code}' "${BASE}/")
ALLOW=$(header_value "$HEADERS" "Allow")
if [ "$STATUS" = "405" ]; then
  pass "GET on the MCP endpoint returns 405"
else
  fail "GET on the MCP endpoint returned $STATUS, want 405"
fi
if [ "$ALLOW" = "POST" ]; then
  pass "405 carries Allow: POST"
else
  fail "405 Allow header was '$ALLOW', want 'POST'"
fi

# That 405 belongs to the MCP endpoint and to nothing else. It used to be the
# answer for every path, because the MCP handler was mounted as a catch-all — so a
# probe for a route this server does not implement was told the route exists and
# that another method would work, neither of which was true. OAuth discovery is
# the probe that actually arrives, so it is the one asserted.
for path in /nope-not-a-route /.well-known/oauth-protected-resource; do
  NOT_FOUND=$(curl -sS -D "$HEADERS" "${BASE}${path}" || true)
  STATUS=$(head -n1 "$HEADERS" | awk '{print $2}')
  if [ "$STATUS" = "404" ]; then
    pass "GET ${path} returns 404"
  else
    fail "GET ${path} returned $STATUS, want 404"
  fi
  CONTENT_TYPE=$(header_value "$HEADERS" "Content-Type")
  case "$CONTENT_TYPE" in
    application/json*) pass "the 404 for ${path} is JSON" ;;
    *) fail "the 404 for ${path} was '${CONTENT_TYPE:-absent}', want application/json" ;;
  esac
  # The body names the endpoint the caller should have asked for, which is what
  # makes the 404 diagnosable from the client side rather than only from the logs.
  case "$NOT_FOUND" in
    *'"mcp_endpoint"'*) pass "the 404 for ${path} names the MCP endpoint" ;;
    *) fail "the 404 for ${path} did not name the MCP endpoint (body: ${NOT_FOUND})" ;;
  esac
done

# $HEADERS still holds the last 404, which the mux answered without the MCP
# handler ever running: exactly the response an outermost middleware is easiest to
# get wrong on.
assert_security_headers "$HEADERS" "the 404"

curl -sS -o /dev/null -D "$HEADERS" "${BASE}/health"
assert_security_headers "$HEADERS" "GET /health"

stop_server

echo
echo "With --json-response:"
start_server --json-response

post_mcp "$HEADERS" "$TOOLS_LIST" >/dev/null
CONTENT_TYPE=$(header_value "$HEADERS" "Content-Type")
case "$CONTENT_TYPE" in
  application/json*) pass "Content-Type is application/json" ;;
  *) fail "Content-Type was '$CONTENT_TYPE', want application/json" ;;
esac

stop_server

echo
if [ "$FAILURES" -eq 0 ]; then
  echo "PASS: stateless streamable HTTP behaves as documented."
else
  echo "FAIL: $FAILURES assertion(s) did not hold."
  exit 1
fi
