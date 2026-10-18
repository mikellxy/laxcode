#!/bin/bash
set -euo pipefail
cd "$(dirname "$0")/.."
export GOCACHE="${GOCACHE:-${TMPDIR:-/tmp}/laxcode-go-cache}"
os="${TARGETOS:-$(go env GOOS)}"
arch="${TARGETARCH:-$(go env GOARCH)}"
case "$os/$arch" in darwin/arm64|darwin/amd64|linux/arm64|linux/amd64) ;; *) echo 'Unsupported host OS/architecture' >&2; exit 1 ;; esac
if [ "${1:-}" = --prepare-images ]; then
  docker build --platform="linux/$arch" -f deploy/Dockerfile.tools -t laxcode-tools:go1.26.6-v1 deploy
  for name in supervisor sandbox; do
    docker pull --platform="linux/$arch" "ghcr.io/nvidia/openshell/$name:0.1.4"
  done
  exit 0
fi
[ "$#" -eq 0 ] || { echo 'Usage: build.sh [--prepare-images]' >&2; exit 1; }
[ -f deploy/defaults/settings.json ] || { echo 'Create deploy/defaults/settings.json from settings.example.json with real credentials' >&2; exit 1; }
# Validate without printing credentials or writing them into frontend build output.
python3 - <<'PY'
import json, sys
sys.path.insert(0, 'deploy')
from configure import prepare
try:
    prepare(json.load(open('deploy/defaults/settings.json')))
except Exception:
    sys.exit('Invalid deployment settings')
PY
pnpm --dir web install --frozen-lockfile
pnpm --dir web build
stage="$(mktemp -d "${TMPDIR:-/tmp}/laxcode-package.XXXXXX")"
trap 'rm -rf "$stage"' EXIT
mkdir -p "$stage/bin"
CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -tags=webdist -trimpath -ldflags='-s -w' -o "$stage/bin/laxcode-proxy" ./laxcode-proxy
# The cloud executable always runs in a Linux sandbox, including on macOS.
CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -ldflags='-s -w' -o "$stage/bin/laxcode-cloud" ./cmd/main
cp deploy/defaults/settings.json deploy/configure.py deploy/Dockerfile "$stage/"
cp -R deploy/defaults/skills "$stage/skills"
python3 - "$stage" "$os" "$arch" <<'PY'
import hashlib, json, os, pathlib, sys, tarfile
root = pathlib.Path(sys.argv[1])
files = {}
for path in sorted(root.rglob('*')):
    if path.is_symlink() or not (path.is_dir() or path.is_file()):
        sys.exit('Package must contain only regular files and directories')
    if path.is_file():
        files[str(path.relative_to(root))] = hashlib.sha256(path.read_bytes()).hexdigest()
(root / 'manifest.json').write_text(json.dumps({'format': 1, 'os': sys.argv[2], 'arch': sys.argv[3], 'openshell': '0.1.4', 'base_image': 'laxcode-tools:go1.26.6-v1', 'files': files}, indent=2))
os.umask(0o077)
output = pathlib.Path('deploy/laxcode.tar.gz')
temp = output.with_suffix('.gz.tmp')
try:
    with tarfile.open(temp, 'w:gz') as archive:
        for path in sorted(root.iterdir()):
            archive.add(path, arcname=path.name)
    temp.chmod(0o600)
    temp.replace(output)
finally:
    temp.unlink(missing_ok=True)
PY
printf 'Built deploy/laxcode.tar.gz (proxy %s/%s, cloud linux/%s)\n' "$os" "$arch" "$arch"
