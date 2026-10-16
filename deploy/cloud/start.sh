#!/bin/sh
set -eu

bundle_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
cd "$bundle_dir"
umask 077
image=${LAXCODE_CLOUD_BASH_IMAGE:-laxcode-cloud-bash:nas-amd64}
port=${LAXCODE_CLOUD_PORT:-5173}
worker=false
if [ "${1:-}" = --run-server ]; then worker=true; shift; fi

is_supervisor() {
  case "$1" in ''|*[!0-9]*) return 1;; esac
  kill -0 "$1" 2>/dev/null || return 1
  command_line=$(tr '\000' '\n' 2>/dev/null < "/proc/$1/cmdline") || return 1
  printf '%s\n' "$command_line" | grep -Fxq -- "$bundle_dir/start.sh" &&
    printf '%s\n' "$command_line" | grep -Fxq -- --run-server
}

[ "$(uname -m)" = x86_64 ] || { echo 'This bundle requires Linux x86_64.' >&2; exit 1; }
[ "$(id -u)" != 0 ] || { echo 'Run as a non-root user with Docker access.' >&2; exit 1; }
command -v docker >/dev/null 2>&1 || { echo 'Docker CLI is required.' >&2; exit 1; }
docker info >/dev/null || { echo 'Docker is unavailable for this user.' >&2; exit 1; }
[ "$(docker image inspect "$image" --format '{{.Os}}/{{.Architecture}}')" = linux/amd64 ] || {
  echo "Load the AMD64 image first: docker load -i cloud-bash-amd64.tar" >&2
  exit 1
}
[ -x ./laxcode ] && [ -x ./laxcode-web ] || {
  echo 'Run: chmod +x laxcode laxcode-web start.sh stop.sh' >&2; exit 1
}
case "$port" in ''|*[!0-9]*) echo 'LAXCODE_CLOUD_PORT must be a port number.' >&2; exit 1;; esac
[ "$port" -ge 1 ] && [ "$port" -le 65535 ] || { echo 'Port must be between 1 and 65535.' >&2; exit 1; }

if [ "$worker" = false ]; then
  existing_pid=$(cat server.pid 2>/dev/null || true)
  if is_supervisor "$existing_pid"; then
    echo "LaxCode is already running (PID $existing_pid). Use ./stop.sh before restarting."
    exit 0
  fi
  if command -v ss >/dev/null 2>&1 && [ -n "$(ss -H -ltn "sport = :$port")" ]; then
    echo "Port $port is already in use. Stop the existing service first." >&2
    exit 1
  fi
  rm -f server.state
  nohup "$bundle_dir/start.sh" --run-server "$@" >> "$bundle_dir/server.log" 2>&1 < /dev/null &
  supervisor_pid=$!
  printf '%s\n' "$supervisor_pid" > server.pid
  attempt=0
  while [ "$attempt" -lt 35 ] && kill -0 "$supervisor_pid" 2>/dev/null; do
    ready_pid=""
    if [ -r server.state ] && read -r ready_pid _ < server.state && [ "$ready_pid" = "$supervisor_pid" ]; then
      echo "LaxCode is running in the background (PID $supervisor_pid). Open http://<NAS-IP>:$port"
      echo "Console log: $bundle_dir/server.log; application logs: $bundle_dir/log/"
      echo 'Stop with: ./stop.sh'
      exit 0
    fi
    attempt=$((attempt + 1))
    sleep 1
  done
  if is_supervisor "$supervisor_pid"; then kill "$supervisor_pid" 2>/dev/null || true; fi
  wait "$supervisor_pid" 2>/dev/null || true
  [ "$(cat server.pid 2>/dev/null || true)" != "$supervisor_pid" ] || rm -f server.pid
  echo "LaxCode failed to start. Check $bundle_dir/server.log" >&2
  exit 1
fi

backend_pid=""
web_pid=""
cleanup() {
  trap - EXIT INT TERM HUP
  for child in "$web_pid" "$backend_pid"; do
    [ -z "$child" ] || kill "$child" 2>/dev/null || true
  done
  for child in "$web_pid" "$backend_pid"; do
    [ -z "$child" ] || wait "$child" 2>/dev/null || true
  done
  [ "$(cat server.pid 2>/dev/null || true)" != "$$" ] || rm -f server.pid
  owner=""
  if [ -r server.state ] && read -r owner _ < server.state && [ "$owner" = "$$" ]; then rm -f server.state; fi
  rm -f "server.state.$$"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM HUP

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
printf '%s %s %s %s\n' "$$" "$backend_pid" "$web_pid" "$port" > "server.state.$$"
mv "server.state.$$" server.state
echo "LaxCode cloud server started. Open http://<NAS-IP>:$port"
echo "User data: $HOME/laxcode/; application logs: $bundle_dir/log/"
while kill -0 "$backend_pid" 2>/dev/null && kill -0 "$web_pid" 2>/dev/null; do
  sleep 1
done
echo 'A server process stopped; stopping the other process.' >&2
exit 1
