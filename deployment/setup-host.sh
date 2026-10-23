#!/bin/sh
# Dedicated Ubuntu host only. Application listeners remain loopback-only.
set -eu
test "$(id -u)" -eq 0
. /etc/os-release
test "$ID" = ubuntu
test "$VERSION_ID" = 24.04
test "$(uname -m)" = x86_64
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends ca-certificates docker.io docker-compose-v2 docker-buildx
systemctl enable --now docker
docker version
docker compose version
