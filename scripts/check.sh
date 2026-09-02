#!/usr/bin/env bash
# Полная проверка репозитория ОДНОЙ командой: gofmt + vet + tests по всем пяти
# модулям. Модули независимы (нет go.work), поэтому обходим их по списку.
set -euo pipefail
cd "$(dirname "$0")/.."

MODULES=(. reflector apidoc/crosscheck adapters/huma examples)

fmt=$(gofmt -l . | grep -v '^\.superpowers/' || true)
if [ -n "$fmt" ]; then
  echo "gofmt required:" >&2
  echo "$fmt" >&2
  exit 1
fi

for m in "${MODULES[@]}"; do
  echo "== $m"
  go vet -C "$m" ./...
  go test -C "$m" -count=1 "$@" ./...
done
echo "OK: fmt + vet + tests, all modules"
