#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")"

docker compose -f debug-compose.yml up --build
