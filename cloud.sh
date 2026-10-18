#!/bin/bash
set -euo pipefail
if [ "$#" -eq 0 ]; then set -- restart; fi
exec "$HOME/laxcode_deploy/deploy.sh" "$@"
