#!/bin/sh
set -eu
umask 077
cd "$(dirname "$0")/.."
export GOCACHE="${GOCACHE:-${TMPDIR:-/tmp}/laxcode-go-cache}"
if [ "${1:-}" = --prepare-images ]; then
  docker build --platform=linux/amd64 -f deploy/Dockerfile.tools -t laxcode-tools:go1.26.6-v2 deploy
  for name in supervisor sandbox; do
    docker pull --platform=linux/amd64 "ghcr.io/nvidia/openshell/$name:0.1.4"
  done
  exit 0
fi
[ "$#" -eq 0 ] || { echo 'Usage: build.sh [--prepare-images]' >&2; exit 1; }
guide_version="$(cat deploy/guide-version.txt)"
case "$guide_version" in
  ''|*[!a-zA-Z0-9._-]*) echo 'Invalid deploy/guide-version.txt' >&2; exit 1 ;;
esac
[ "${#guide_version}" -le 64 ] || { echo 'Guide version must be at most 64 characters' >&2; exit 1; }
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
mkdir -p release
build_dir="$(mktemp -d release/.build.XXXXXX)"
trap 'rm -rf "$build_dir"' EXIT
stage="$build_dir/laxcode"
mkdir -p "$stage/bin"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -tags=webdist -trimpath -ldflags="-s -w -X main.guideVersion=$guide_version" -o "$stage/bin/laxcode-proxy" ./laxcode-proxy
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o "$stage/bin/laxcode-cloud" ./cmd/main
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o "$stage/bin/laxcode-manager" ./laxcode-manager
cp deploy/defaults/settings.json deploy/defaults/network.json deploy/configure.py deploy/deploy.py deploy/reset_quota.py deploy/Dockerfile "$stage/"
cp -R deploy/defaults/skills "$stage/skills"
python3 - "$stage" <<'PY'
import hashlib, json, os, pathlib, sys, tarfile
root = pathlib.Path(sys.argv[1])
files = {}
for path in sorted(root.rglob('*')):
    if path.is_symlink() or not (path.is_dir() or path.is_file()):
        sys.exit('Package must contain only regular files and directories')
    if path.is_file():
        files[str(path.relative_to(root))] = hashlib.sha256(path.read_bytes()).hexdigest()
(root / 'manifest.json').write_text(json.dumps({'format': 2, 'os': 'linux', 'arch': 'amd64', 'openshell': '0.1.4', 'base_image': 'laxcode-tools:go1.26.6-v2', 'files': files}, indent=2))
os.umask(0o077)
output = pathlib.Path('release/laxcode.tar.gz')
temp = output.with_suffix('.gz.tmp')
try:
    with tarfile.open(temp, 'w:gz') as archive:
        # GNU tar --unlink-first cannot replace nonempty directory entries.
        # File paths create parents on first install and preserve them on updates.
        for path in sorted(root.rglob('*')):
            if path.is_file():
                archive.add(path, arcname='laxcode/' + str(path.relative_to(root)))
    temp.chmod(0o600)
    temp.replace(output)
finally:
    temp.unlink(missing_ok=True)
PY
printf 'Built release/laxcode.tar.gz (Linux amd64)\n'
