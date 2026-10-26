#!/bin/sh
set -eu
: "${ECS_USER_IP:?Set ECS_USER_IP, for example root@127.0.0.1}"
cd "$(dirname "$0")/.."
scp release/laxcode.tar.gz "$ECS_USER_IP:/root/laxcode_deploy/laxcode.tar.gz"
