#!/bin/sh

SCRIPT_PATH=$(realpath "$0")
SCRIPT_DIR=$(dirname "$SCRIPT_PATH")

echo "scp $SCRIPT_DIR/{deploy.sh,laxcode.tar.gz} $ECS_USER_IP:/root/laxcode_deploy/"

scp $SCRIPT_DIR/{deploy.sh,laxcode.tar.gz} \
$ECS_USER_IP:/root/laxcode_deploy/