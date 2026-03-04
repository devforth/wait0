#!/usr/bin/env bash
set -euo pipefail

THRESHOLD="${1:-80}"
PROFILE="${COVERPROFILE:-coverage.out}"
FILTERED="${PROFILE%.out}.internal.filtered.out"

EXCLUDE_REGEX='/(proc_linux|proc_other)\.go:'
INTERNAL_PATH_REGEX='/internal/wait0/'

printf '[INFO] running coverage profile generation\n'
go test ./... -coverprofile="${PROFILE}" -covermode=atomic >/tmp/wait0-coverage-test.log

printf '[INFO] filtering profile to internal/wait0 (excluding proc_*.go)\n'
{
  head -n 1 "${PROFILE}"
  tail -n +2 "${PROFILE}" | grep "${INTERNAL_PATH_REGEX}" | grep -Ev "${EXCLUDE_REGEX}" || true
} > "${FILTERED}"

if [ "$(wc -l < "${FILTERED}")" -le 1 ]; then
  echo "[ERROR] filtered coverage profile is empty"
  exit 1
fi

SUMMARY="$(go tool cover -func="${FILTERED}")"
printf '%s\n' "${SUMMARY}" > coverage-summary.txt
TOTAL_LINE="$(printf '%s\n' "${SUMMARY}" | awk '/^total:/ {print}')"
TOTAL_PCT="$(printf '%s\n' "${TOTAL_LINE}" | awk '{gsub(/%/,"",$3); print $3}')"

printf '[INFO] effective internal/wait0 coverage (exclusions applied): %s%%\n' "${TOTAL_PCT}"
printf '[INFO] threshold: %s%%\n' "${THRESHOLD}"
printf '[INFO] exclusions: internal/wait0/proc_linux.go, internal/wait0/proc_other.go\n'

awk -v total="${TOTAL_PCT}" -v threshold="${THRESHOLD}" 'BEGIN {
  if (total + 0 < threshold + 0) {
    printf("[ERROR] coverage %.1f%% is below threshold %.1f%%\n", total, threshold);
    exit 1;
  }
  printf("[INFO] coverage gate passed: %.1f%% >= %.1f%%\n", total, threshold);
}'
