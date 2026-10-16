#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
cd "$repo_root"
out="$repo_root/nas"
image=laxcode-cloud-bash:nas-amd64
mkdir -p "$out/defaults/skills"

cleanup() {
  rm -f "$out/laxcode.tmp" "$out/laxcode-web.tmp" "$out/cloud-bash-amd64.tar.tmp" \
    "$out/BUILD_INFO.json.tmp" "$out/SHA256SUMS.tmp" "$out/.cloud-files.tmp"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM HUP

CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o "$out/laxcode.tmp" ./cmd/main
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -tags=webdist -trimpath -ldflags='-s -w' -o "$out/laxcode-web.tmp" ./cmd/web
docker build --platform=linux/amd64 -t "$image" docker/cloud-bash
docker save -o "$out/cloud-bash-amd64.tar.tmp" "$image"

# Keep administrator templates, including any explicitly configured credentials.
if [ ! -e "$out/defaults/settings.json" ]; then
  printf '%s\n' '{"model":"","provider_list":[]}' > "$out/defaults/settings.json"
fi
cp deploy/cloud/start.sh "$out/start.sh"
cp deploy/cloud/stop.sh "$out/stop.sh"
cp deploy/cloud/README.md "$out/README.md"

revision=$(git rev-parse HEAD 2>/dev/null || printf unknown)
modified=true
if [ -z "$(git status --porcelain 2>/dev/null)" ]; then modified=false; fi
image_id=$(docker image inspect "$image" --format '{{.Id}}')
cat > "$out/BUILD_INFO.json.tmp" <<EOF
{
  "built_at_utc": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "source_commit": "$revision",
  "source_modified": $modified,
  "platform": "linux/amd64",
  "cgo_enabled": false,
  "frontend_embedded": true,
  "go_version": "$(go env GOVERSION)",
  "bash_image": "$image",
  "bash_image_id": "$image_id"
}
EOF

mv "$out/laxcode.tmp" "$out/laxcode"
mv "$out/laxcode-web.tmp" "$out/laxcode-web"
mv "$out/cloud-bash-amd64.tar.tmp" "$out/cloud-bash-amd64.tar"
mv "$out/BUILD_INFO.json.tmp" "$out/BUILD_INFO.json"
chmod +x "$out/laxcode" "$out/laxcode-web" "$out/start.sh" "$out/stop.sh"

if command -v sha256sum >/dev/null 2>&1; then
  checksum=sha256sum
else
  checksum=shasum
fi
(
  cd "$out"
  find laxcode laxcode-web cloud-bash-amd64.tar start.sh stop.sh README.md BUILD_INFO.json defaults -type f > .cloud-files.tmp
  LC_ALL=C sort -o .cloud-files.tmp .cloud-files.tmp
  while IFS= read -r file; do
    if [ "$checksum" = shasum ]; then
      shasum -a 256 "$file"
    else
      sha256sum "$file"
    fi
  done < .cloud-files.tmp
) > "$out/SHA256SUMS.tmp"
mv "$out/SHA256SUMS.tmp" "$out/SHA256SUMS"
echo "Built Linux amd64 cloud bundle: $out"
echo "Copy nas/ to your NAS, load cloud-bash-amd64.tar, then run ./start.sh."
