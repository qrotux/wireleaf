#!/usr/bin/env bash
# Тулчейн библиотеки: одноразовый контейнер golang, кэши — в именованных volume.
set -euo pipefail
cd "$(dirname "$0")/.."
exec docker run --rm \
  -v "$PWD":/src -w /src \
  -v wireleaf-modcache:/go/pkg/mod \
  -v wireleaf-gocache:/root/.cache/go-build \
  golang:1.26 go "$@"
