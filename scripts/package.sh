#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GO_DIR="$ROOT_DIR/go"
DIST_DIR="$ROOT_DIR/dist/voice-type"

echo "==> Running Go tests (with race detector)..."
cd "$GO_DIR"
go test -race ./...

echo "==> Building portable Go binary (rpath=\$ORIGIN/lib)..."
rm -rf "$DIST_DIR"
mkdir -p "$DIST_DIR/lib"
go build -ldflags="-s -w -r '\$ORIGIN/lib'" -o "$DIST_DIR/voice-type" ./cmd/voice-type

echo "==> Bundling sherpa-onnx & onnxruntime shared libraries..."
SHERPA_LIB_DIR="$(go list -f '{{.Dir}}' github.com/k2-fsa/sherpa-onnx-go-linux)/lib/x86_64-unknown-linux-gnu"
cp "$SHERPA_LIB_DIR"/libsherpa-onnx-c-api.so "$DIST_DIR/lib/"
cp "$SHERPA_LIB_DIR"/libonnxruntime.so "$DIST_DIR/lib/"

echo "==> Creating release tarball..."
cd "$ROOT_DIR/dist"
tar -czf voice-type-linux-amd64.tar.gz voice-type

echo "==> Done:"
ls -lh "$DIST_DIR" "$DIST_DIR/lib" "$ROOT_DIR/dist/voice-type-linux-amd64.tar.gz"
