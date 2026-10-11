#!/usr/bin/env bash
# Compile this checkout for the local Docker architecture. Compose builds the
# runtime image from this binary; it never pulls an upstream ARTEX image.
set -euo pipefail
cd "$(cd "$(dirname "$0")/.." && pwd)"
command -v go >/dev/null
command -v npm >/dev/null
arch="$(docker info --format '{{.Architecture}}')"
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) printf 'Unsupported Docker architecture: %s\n' "$arch" >&2; exit 1 ;;
esac
(cd web && npm ci --ignore-scripts && npm run build:static)
if [ -d server/webui/dist ]; then
  mkdir -p dist
  previous_ui="$(mktemp -d dist/previous-ui.XXXXXX)"
  mv server/webui/dist "$previous_ui/webui"
fi
mkdir -p server/webui/dist "dist/$arch"
cp -R web/out/. server/webui/dist/
CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -tags embedui -trimpath \
  -ldflags '-X main.version=0.3.12-hardening' -o "dist/$arch/artex" ./cmd/artex
