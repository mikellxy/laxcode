#!/usr/bin/env bash
set -euo pipefail
pipeline_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
exec uv run --project "$pipeline_dir" laxcode-knowledge "$@"
