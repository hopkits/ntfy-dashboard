#!/usr/bin/env bash
# 构建 ntfy-dashboard 单二进制。
#
# 用法：
#   ./build.sh                 # 构建当前平台，输出到 bin/ntfy-dashboard
#   GOOS=linux GOARCH=arm64 ./build.sh
#   VERSION=v1.0.0 ./build.sh
set -euo pipefail

cd "$(dirname "$0")"

VERSION="${VERSION:-$(git describe --tags --always 2>/dev/null || echo dev)}"
OUT_DIR="${OUT_DIR:-bin}"
BIN="${BIN:-ntfy-dashboard}"

mkdir -p "$OUT_DIR"

LDFLAGS="-s -w -X main.version=${VERSION}"

echo "==> 构建 ${BIN} (${GOOS:-$(go env GOOS)}/${GOARCH:-$(go env GOARCH)})，版本 ${VERSION}"
CGO_ENABLED=0 go build \
  -trimpath \
  -ldflags "$LDFLAGS" \
  -o "${OUT_DIR}/${BIN}" \
  .

echo "==> 完成：${OUT_DIR}/${BIN}"
ls -lh "${OUT_DIR}/${BIN}"
