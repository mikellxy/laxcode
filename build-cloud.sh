#!/bin/bash
set -euo pipefail
exec "$(dirname "$0")/deploy/build.sh" "$@"
