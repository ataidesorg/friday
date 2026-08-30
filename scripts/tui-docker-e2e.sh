#!/usr/bin/env bash
# Practical Linux binary smoke in Docker. Not a go test: the daemon is a
# real service. Skips when docker is missing. Never passes API keys in.
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
if ! command -v docker >/dev/null || ! docker info >/dev/null 2>&1; then
  echo "skip: docker not available"
  exit 0
fi
arch="$(docker version --format '{{.Server.Arch}}' 2>/dev/null || echo arm64)"
case "$arch" in
  amd64) goarch=amd64 ;;
  arm64) goarch=arm64 ;;
  *) echo "skip: unsupported docker arch $arch"; exit 0 ;;
esac
out="$(mktemp -d)"
trap 'rm -rf "$out"' EXIT
GOOS=linux GOARCH="$goarch" CGO_ENABLED=0 go build -o "$out/ink" "$root/cmd/ink"
docker run --rm --network none -v "$out/ink:/ink:ro" alpine:3.20 /ink version
docker run --rm --network none -v "$out/ink:/ink:ro" alpine:3.20 /ink help >/dev/null
echo "ok: linux ink runs in alpine ($goarch)"
