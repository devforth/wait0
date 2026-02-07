#!/usr/bin/env bash
set -euo pipefail

docker compose -f debug-compose.yml up --build
