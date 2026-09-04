#!/usr/bin/env bash
set -euo pipefail

script_dir="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
repo_root="$(CDPATH= cd -- "$script_dir/.." && pwd)"

docker buildx create --use
docker buildx build \
	--platform=linux/amd64,linux/arm64 \
	--tag "devforth/wait0:latest" \
	--tag "devforth/wait0:1.4.1" \
	--push \
	--file "$repo_root/Dockerfile" \
	"$repo_root"
