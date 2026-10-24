#!/bin/bash
set -euo pipefail
if [ "$#" -eq 0 ]; then set -- restart; fi
exec python3 "$HOME/laxcode_deploy/laxcode/deploy.py" "$@"
