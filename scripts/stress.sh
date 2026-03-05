#!/usr/bin/env bash
set -euo pipefail

# --- config (defaults) ---
DEFAULT_BASE_URL="http://127.0.0.1:8082"
DEFAULT_RPS="500"
DEFAULT_DURATION="20"     # seconds
DEFAULT_PREFIX="/rand"
DEFAULT_SEED_PATHS=""     # comma-separated paths, e.g. "/,/health,/api"

# Usage:
#   BASE_URL=http://127.0.0.1:8080 RPS=50 DURATION=30 ./debug/stress.sh
#   ./debug/stress.sh http://127.0.0.1:8080 50 30 /p
#
# Env/args:
#   BASE_URL  - origin to hit (default: http://127.0.0.1:8080)
#   RPS       - requests per second (default: 25)
#   DURATION  - seconds to run (default: 10)
#   PREFIX    - path prefix (default: /rand)
#   SEED_PATHS- optional comma-separated fixed paths to sample from

BASE_URL="${1:-${BASE_URL:-$DEFAULT_BASE_URL}}"
RPS="${2:-${RPS:-$DEFAULT_RPS}}"
DURATION="${3:-${DURATION:-$DEFAULT_DURATION}}"
PREFIX="${4:-${PREFIX:-$DEFAULT_PREFIX}}"
SEED_PATHS="${SEED_PATHS:-$DEFAULT_SEED_PATHS}"

if ! [[ "$RPS" =~ ^[0-9]+$ ]] || [ "$RPS" -le 0 ]; then
  echo "RPS must be a positive integer" >&2
  exit 2
fi
if ! [[ "$DURATION" =~ ^[0-9]+$ ]] || [ "$DURATION" -le 0 ]; then
  echo "DURATION must be a positive integer (seconds)" >&2
  exit 2
fi

interval="$(awk -v rps="$RPS" 'BEGIN { printf "%.6f", 1.0/rps }')"
end_epoch="$(( $(date +%s) + DURATION ))"

# Build a small array of seed paths if provided: "a,b,c"
IFS=',' read -r -a seed_arr <<< "${SEED_PATHS}"

rand_hex() {
  # Prefer openssl; fall back to /dev/urandom
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -hex 6
  else
    od -An -N6 -tx1 /dev/urandom | tr -d ' \n'
  fi
}

pick_path() {
  if [ "${#seed_arr[@]}" -gt 0 ] && [ -n "${seed_arr[0]-}" ]; then
    # Randomly pick from provided list
    local idx=$(( RANDOM % ${#seed_arr[@]} ))
    printf "%s" "${seed_arr[$idx]}"
  else
    # Random path: /prefix/<hex>
    local a b
    a="$(rand_hex)"
    b="$(rand_hex)"
    printf "%s/%s" "$PREFIX" "$a" "$b"
  fi
}

inflight=0
launched=0

echo "BASE_URL=$BASE_URL RPS=$RPS DURATION=${DURATION}s PREFIX=$PREFIX"
echo "interval=${interval}s (approx), stop_at=$(date -d "@$end_epoch" 2>/dev/null || true)"

while [ "$(date +%s)" -lt "$end_epoch" ]; do
  path="$(pick_path)"
  url="${BASE_URL}${path}"

  # Fire and forget; keep output compact.
  # Prints: "<status> <time_total> <path>"
  curl -sS -o /dev/null \
    -w "%{http_code} %{time_total} ${path}\n" \
    --max-time 10 \
    "$url" &

  inflight=$(( inflight + 1 ))
  launched=$(( launched + 1 ))

  # Avoid unlimited background jobs.
  if [ "$inflight" -ge $((RPS * 2)) ]; then
    wait
    inflight=0
  fi

  sleep "$interval"
done

wait
echo "launched=$launched"
