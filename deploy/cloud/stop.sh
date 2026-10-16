#!/bin/sh
set -eu

bundle_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
cd "$bundle_dir"
supervisor_pid=""; backend_pid=""; web_pid=""; saved_port=""
if [ -r server.state ]; then read -r supervisor_pid backend_pid web_pid saved_port < server.state || true; fi
port=${LAXCODE_CLOUD_PORT:-${saved_port:-5173}}
case "$port" in ''|*[!0-9]*) echo 'LAXCODE_CLOUD_PORT must be a port number.' >&2; exit 1;; esac
[ "$port" -ge 1 ] && [ "$port" -le 65535 ] || { echo 'Port must be between 1 and 65535.' >&2; exit 1; }

is_supervisor() {
  case "$1" in ''|*[!0-9]*) return 1;; esac
  kill -0 "$1" 2>/dev/null || return 1
  command_line=$(tr '\000' '\n' 2>/dev/null < "/proc/$1/cmdline") || return 1
  printf '%s\n' "$command_line" | grep -Fxq -- "$bundle_dir/start.sh" &&
    printf '%s\n' "$command_line" | grep -Fxq -- --run-server
}

is_laxcode() {
  case "$1" in ''|*[!0-9]*) return 1;; esac
  executable=$(readlink "/proc/$1/exe" 2>/dev/null) || return 1
  case "$executable" in
    "$bundle_dir/laxcode"|"$bundle_dir/laxcode-web"|"$bundle_dir/laxcode (deleted)"|"$bundle_dir/laxcode-web (deleted)") return 0;;
  esac
  return 1
}

targets=""
supervisor_pid=$(cat server.pid 2>/dev/null || true)
if [ -n "$saved_port" ] && [ "$saved_port" != "$port" ]; then supervisor_pid=""; fi
if ! is_supervisor "$supervisor_pid"; then supervisor_pid=""; fi
if [ "$saved_port" = "$port" ]; then
  for pid in "$web_pid" "$backend_pid"; do
    if is_laxcode "$pid"; then targets="$targets $pid"; fi
  done
fi
listener=""
if command -v ss >/dev/null 2>&1; then
  listener=$(ss -H -ltnp "sport = :$port")
  port_pids=$(printf '%s\n' "$listener" | grep -o 'pid=[0-9]*' | cut -d= -f2 | sort -u)
  for pid in $port_pids; do
    if is_laxcode "$pid"; then targets="$targets $pid"; fi
  done
elif [ -z "$targets$supervisor_pid" ]; then
  echo 'No saved process state; the ss command is needed to find an older service by port.' >&2
  exit 1
fi

# Older foreground deployments have no PID state: tie their backend to the Web command line.
if [ -n "$targets" ] && [ "$saved_port" != "$port" ]; then
  version=""; instance_pid=""; instance_url=""
  tab=$(printf '\t')
  instance_file="$HOME/.laxcode/sse-code.instance"
  if [ -r "$instance_file" ] && IFS="$tab" read -r version instance_pid instance_url < "$instance_file" && [ "$version" = v1 ] && is_laxcode "$instance_pid"; then
    for pid in $targets; do
      command_line=$(tr '\000' '\n' < "/proc/$pid/cmdline" 2>/dev/null || true)
      if printf '%s\n' "$command_line" | grep -Fxq -- "-backend=$instance_url"; then targets="$targets $instance_pid"; break; fi
    done
  fi
fi

if is_supervisor "$supervisor_pid"; then
  kill "$supervisor_pid" 2>/dev/null || true
else
  supervisor_pid=""
  for pid in $targets; do
    if is_laxcode "$pid"; then kill "$pid" 2>/dev/null || true; fi
  done
fi

attempt=0
while [ "$attempt" -lt 25 ]; do
  alive=false
  for pid in $targets; do if is_laxcode "$pid"; then alive=true; fi; done
  if [ -n "$supervisor_pid" ] && is_supervisor "$supervisor_pid"; then alive=true; fi
  [ "$alive" = true ] || break
  attempt=$((attempt + 1))
  sleep 1
done
for pid in $targets; do if is_laxcode "$pid"; then kill -KILL "$pid" 2>/dev/null || true; fi; done
if [ -n "$supervisor_pid" ] && is_supervisor "$supervisor_pid"; then kill -KILL "$supervisor_pid" 2>/dev/null || true; fi
if [ -n "$supervisor_pid" ]; then
  [ "$(cat server.pid 2>/dev/null || true)" != "$supervisor_pid" ] || rm -f server.pid
  owner=""
  if [ -r server.state ] && read -r owner _ < server.state && [ "$owner" = "$supervisor_pid" ]; then rm -f server.state; fi
fi

if [ -z "$targets$supervisor_pid" ]; then
  if [ -n "$listener" ]; then
    echo "Port $port is occupied by another process; nothing was killed." >&2
    exit 1
  fi
  echo "No LaxCode service found on port $port."
else
  echo "LaxCode on port $port and its backend have been stopped."
fi
