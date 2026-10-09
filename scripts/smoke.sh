#!/usr/bin/env bash
# Walks the main API paths with curl and fails loudly on any unexpected status.
#
# Tip: run `go run ./cmd/notes-watch` in another terminal to see live events.
#
# Usage: bash scripts/smoke.sh        (BASE_URL defaults to http://localhost:8080)
set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:8080}"
API="$BASE_URL/api/v1"

# Unique per run so reruns do not hit 409 on the same path.
RUN_ID="$(date +%s)-$$"
NOTE_PATH="eng/runbooks/deploy-$RUN_ID.md"

HEADERS="$(mktemp)"
BODY="$(mktemp)"
trap 'rm -f "$HEADERS" "$BODY"' EXIT

step() { printf '\n== %s\n' "$*"; }

# request METHOD URL EXPECTED_STATUS [curl args...]
# Leaves response headers in $HEADERS and the body in $BODY.
request() {
  local method="$1" url="$2" expected="$3"
  shift 3
  local status
  status="$(curl -sS -o "$BODY" -D "$HEADERS" -w '%{http_code}' -X "$method" "$url" "$@")"
  if [[ "$status" != "$expected" ]]; then
    echo "FAIL: $method $url returned $status, expected $expected" >&2
    cat "$BODY" >&2
    echo >&2
    exit 1
  fi
  echo "$method ${url#"$BASE_URL"} -> $status"
}

header() { grep -i "^$1:" "$HEADERS" | head -n1 | cut -d' ' -f2- | tr -d '\r'; }

json_string() { sed -n "s/.*\"$1\":\"\\([^\"]*\\)\".*/\\1/p" "$BODY" | head -n1; }

JSON=(-H 'Content-Type: application/json')

step "healthz"
request GET "$BASE_URL/healthz" 200
cat "$BODY"

step "create $NOTE_PATH as alice"
request POST "$API/notes" 201 "${JSON[@]}" -H 'X-User-ID: alice' \
  -d "{\"path\":\"$NOTE_PATH\",\"body\":\"# Deploy\",\"tags\":[\"runbook\"]}"
NOTE_ID="$(json_string id)"
[[ -n "$NOTE_ID" ]] || { echo "FAIL: no id in create response" >&2; exit 1; }
echo "id=$NOTE_ID ETag=$(header ETag)"

step "get (show ETag)"
request GET "$API/notes/$NOTE_ID" 200
ETAG_V1="$(header ETag)"
echo "ETag=$ETAG_V1"

step "update with If-Match $ETAG_V1 as bob"
request PUT "$API/notes/$NOTE_ID" 200 "${JSON[@]}" -H 'X-User-ID: bob' -H "If-Match: $ETAG_V1" \
  -d "{\"path\":\"$NOTE_PATH\",\"body\":\"# Deploy, revised\",\"tags\":[\"runbook\"]}"
ETAG_V2="$(header ETag)"
echo "new ETag=$ETAG_V2"

step "stale update (If-Match $ETAG_V1) must be rejected with 412"
request PUT "$API/notes/$NOTE_ID" 412 "${JSON[@]}" -H 'X-User-ID: alice' -H "If-Match: $ETAG_V1" \
  -d "{\"path\":\"$NOTE_PATH\",\"body\":\"stale write\"}"
cat "$BODY"; echo

step "update without If-Match must be rejected with 428"
request PUT "$API/notes/$NOTE_ID" 428 "${JSON[@]}" \
  -d "{\"path\":\"$NOTE_PATH\",\"body\":\"no precondition\"}"

step "list ?prefix=eng/"
request GET "$API/notes?prefix=eng/" 200
grep -q "$NOTE_PATH" "$BODY" || { echo "FAIL: $NOTE_PATH not in list" >&2; exit 1; }
cat "$BODY"; echo

step "folders ?prefix=eng/"
request GET "$API/folders?prefix=eng/" 200
cat "$BODY"; echo

step "delete with If-Match $ETAG_V2"
request DELETE "$API/notes/$NOTE_ID" 204 -H 'X-User-ID: alice' -H "If-Match: $ETAG_V2"

step "get after delete must be 404"
request GET "$API/notes/$NOTE_ID" 404

printf '\nAll smoke steps passed.\n'
