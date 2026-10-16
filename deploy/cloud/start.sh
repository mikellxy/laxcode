#!/bin/sh
set -eu

bundle_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
cd "$bundle_dir"
backend_pid=""
web_pid=""
image=${LAXCODE_CLOUD_BASH_IMAGE:-laxcode-cloud-bash:nas-amd64}
port=${LAXCODE_CLOUD_PORT:-5173}

cleanup() {
  trap - EXIT INT TERM HUP
  for child in "$web_pid" "$backend_pid"; do
    [ -z "$child" ] || kill "$child" 2>/dev/null || true
  done
  for child in "$web_pid" "$backend_pid"; do
    [ -z "$child" ] || wait "$child" 2>/dev/null || true
  done
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM HUP

[ "$(uname -m)" = x86_64 ] || { echo 'This bundle requires Linux x86_64.' >&2; exit 1; }
[ "$(id -u)" != 0 ] || { echo 'Run as a non-root user with Docker access.' >&2; exit 1; }
command -v docker >/dev/null 2>&1 || { echo 'Docker CLI is required.' >&2; exit 1; }
docker info >/dev/null || { echo 'Docker is unavailable for this user.' >&2; exit 1; }
[ "$(docker image inspect "$image" --format '{{.Os}}/{{.Architecture}}')" = linux/amd64 ] || {
  echo "Load the AMD64 image first: docker load -i cloud-bash-amd64.tar" >&2
  exit 1
}
[ -x ./laxcode ] && [ -x ./laxcode-web ] || {
  echo 'Run: chmod +x laxcode laxcode-web start.sh' >&2; exit 1
}
case "$port" in ''|*[!0-9]*) echo 'LAXCODE_CLOUD_PORT must be a port number.' >&2; exit 1;; esac

./laxcode -cloud -addr=127.0.0.1:0 -cloud_bash_image="$image" \
  -conf_copy_dir="$bundle_dir/defaults" "$@" &
backend_pid=$!

# The backend publishes its URL after listening; match its PID to avoid stale instances.
instance_file="$HOME/.laxcode/sse-code.instance"
tab=$(printf '\t')
backend_url=""
attempt=0
while [ "$attempt" -lt 30 ]; do
  kill -0 "$backend_pid" 2>/dev/null || { echo 'Backend exited during startup.' >&2; exit 1; }
  version=""; instance_pid=""; instance_url=""
  if [ -r "$instance_file" ] && IFS="$tab" read -r version instance_pid instance_url < "$instance_file"; then
    if [ "$version" = v1 ] && [ "$instance_pid" = "$backend_pid" ] && [ -n "$instance_url" ]; then
      backend_url=$instance_url
      break
    fi
  fi
  attempt=$((attempt + 1))
  sleep 1
done
[ -n "$backend_url" ] || { echo 'Backend did not publish its URL within 30 seconds.' >&2; exit 1; }

./laxcode-web -cloud -addr="0.0.0.0:$port" -backend="$backend_url" &
web_pid=$!
sleep 1
kill -0 "$web_pid" 2>/dev/null || { echo 'Web server failed to start.' >&2; exit 1; }
echo "LaxCode cloud server started. Open http://<NAS-IP>:$port"
echo "User data: $HOME/laxcode/; application logs: $bundle_dir/log/"
while kill -0 "$backend_pid" 2>/dev/null && kill -0 "$web_pid" 2>/dev/null; do
  sleep 1
done
echo 'A server process stopped; stopping the other process.' >&2
exit 1
