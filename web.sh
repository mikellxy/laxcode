#!/usr/bin/env bash

set -Eeuo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
instance_file="${HOME}/.laxcode/sse-code.instance"
frontend_url="http://127.0.0.1:5173"
backend_pid=""
vite_pid=""

cleanup() {
  trap - EXIT INT TERM HUP
  if [[ -n "${vite_pid}" ]] && kill -0 "${vite_pid}" 2>/dev/null; then
    kill "${vite_pid}" 2>/dev/null || true
  fi
  if [[ -n "${backend_pid}" ]] && kill -0 "${backend_pid}" 2>/dev/null; then
    kill "${backend_pid}" 2>/dev/null || true
  fi
  [[ -z "${vite_pid}" ]] || wait "${vite_pid}" 2>/dev/null || true
  [[ -z "${backend_pid}" ]] || wait "${backend_pid}" 2>/dev/null || true
}
trap cleanup EXIT INT TERM HUP

for command_name in pnpm make curl open; do
  if ! command -v "${command_name}" >/dev/null 2>&1; then
    echo "required command not found: ${command_name}" >&2
    exit 1
  fi
done

pnpm --dir "${repo_root}/web" install --frozen-lockfile
make -C "${repo_root}" build

"${repo_root}/bin/laxcode" -sse -mode=code -addr=127.0.0.1:0 &
backend_pid=$!

backend_url=""
for _ in $(seq 1 300); do
  if ! kill -0 "${backend_pid}" 2>/dev/null; then
    wait "${backend_pid}" || true
    echo "LaxCode backend exited before becoming ready" >&2
    exit 1
  fi

  instance_version=""
  instance_pid=""
  instance_url=""
  if [[ -r "${instance_file}" ]] && IFS=$'\t' read -r instance_version instance_pid instance_url 2>/dev/null < "${instance_file}"; then
    if [[ "${instance_version}" == "v1" && "${instance_pid}" == "${backend_pid}" && -n "${instance_url}" ]]; then
      backend_url="${instance_url}"
      if curl --silent --fail --max-time 1 "${backend_url}/healthz" >/dev/null 2>&1; then
        break
      fi
    fi
  fi
  sleep 0.1
done

if [[ -z "${backend_url}" ]] || ! curl --silent --fail --max-time 1 "${backend_url}/healthz" >/dev/null 2>&1; then
  echo "LaxCode backend did not become healthy within 30 seconds" >&2
  exit 1
fi

LAXCODE_PROXY_TARGET="${backend_url}" pnpm --dir "${repo_root}/web" dev &
vite_pid=$!

frontend_ready=false
for _ in $(seq 1 300); do
  if ! kill -0 "${backend_pid}" 2>/dev/null; then
    echo "LaxCode backend exited while starting the web UI" >&2
    exit 1
  fi
  if ! kill -0 "${vite_pid}" 2>/dev/null; then
    wait "${vite_pid}" || true
    echo "Vite exited before becoming ready; port 5173 may already be in use" >&2
    exit 1
  fi
  if curl --silent --fail --max-time 1 "${frontend_url}" >/dev/null 2>&1; then
    frontend_ready=true
    break
  fi
  sleep 0.1
done

if [[ "${frontend_ready}" != true ]]; then
  echo "Web UI did not become healthy within 30 seconds" >&2
  exit 1
fi

echo "LaxCode Web is ready: ${frontend_url} (backend: ${backend_url})"
open "${frontend_url}"

# Bash 3.2 (the macOS default) has no wait -n. Monitor both long-running
# children so either failure tears down the other process through the trap.
while kill -0 "${backend_pid}" 2>/dev/null && kill -0 "${vite_pid}" 2>/dev/null; do
  sleep 1
done

if ! kill -0 "${backend_pid}" 2>/dev/null; then
  echo "LaxCode backend stopped" >&2
else
  echo "Vite web server stopped" >&2
fi
exit 1
