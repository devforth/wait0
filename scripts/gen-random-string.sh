#!/usr/bin/env bash
set -euo pipefail

# Generate a random string from lower/upper letters, digits, and symbol set.
#
# Usage:
#   ./scripts/gen-random-string.sh
#   ./scripts/gen-random-string.sh 48
#   ./scripts/gen-random-string.sh 48 '!@#$%_-'
#
# Defaults:
#   length  = 32
#   symbols = !@#$%^&*()-_=+[]{}.,:;?

DEFAULT_LENGTH=32
DEFAULT_SYMBOLS='!@#$%^&*()-_=+[]{}.,:;?'

LOWER='abcdefghijklmnopqrstuvwxyz'
UPPER='ABCDEFGHIJKLMNOPQRSTUVWXYZ'
DIGITS='0123456789'

usage() {
  cat <<USAGE
Generate random string.

Usage:
  $(basename "$0") [length] [symbols]

Arguments:
  length   Positive integer. Default: ${DEFAULT_LENGTH}
  symbols  Symbol set to include. Default: ${DEFAULT_SYMBOLS}

Notes:
  - For length >= 4, output guarantees at least one lowercase, uppercase,
    digit, and symbol character.
  - For length < 4, output is sampled from the full charset without guarantees.
USAGE
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  usage
  exit 0
fi

length="${1:-$DEFAULT_LENGTH}"
symbols="${2:-$DEFAULT_SYMBOLS}"

if ! [[ "$length" =~ ^[0-9]+$ ]] || [[ "$length" -le 0 ]]; then
  echo "length must be a positive integer" >&2
  exit 2
fi

if [[ -z "$symbols" ]]; then
  echo "symbols must be non-empty" >&2
  exit 2
fi

full_charset="${LOWER}${UPPER}${DIGITS}${symbols}"

rand_u32() {
  od -An -N4 -tu4 /dev/urandom | tr -d ' \n'
}

rand_index() {
  local n="$1"
  local r
  r="$(rand_u32)"
  echo $(( r % n ))
}

pick_from() {
  local set="$1"
  local idx
  idx="$(rand_index "${#set}")"
  printf '%s' "${set:idx:1}"
}

shuffle_chars() {
  local -n arr_ref=$1
  local i j tmp
  for (( i=${#arr_ref[@]}-1; i>0; i-- )); do
    j="$(rand_index $((i+1)))"
    tmp="${arr_ref[i]}"
    arr_ref[i]="${arr_ref[j]}"
    arr_ref[j]="$tmp"
  done
}

out_chars=()

if (( length >= 4 )); then
  out_chars+=("$(pick_from "$LOWER")")
  out_chars+=("$(pick_from "$UPPER")")
  out_chars+=("$(pick_from "$DIGITS")")
  out_chars+=("$(pick_from "$symbols")")
fi

while (( ${#out_chars[@]} < length )); do
  out_chars+=("$(pick_from "$full_charset")")
done

shuffle_chars out_chars

printf '%s\n' "${out_chars[*]}" | tr -d ' '
