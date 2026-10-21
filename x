#!/bin/sh
# Shared local/CI task entry point. Only fmt edits source; no task publishes.
set -eu
cd "$(dirname "$0")"

# Reject unsupported task arguments instead of silently ignoring them.
usage() {
  echo 'usage: ./x [fmt [--check]|check|test|integration|prove --laptop HOST --server HOST [--browser] [--report PATH]|compatibility|docs|build|release-check [vX.Y.Z]|dist vX.Y.Z]' >&2
  exit 2
}

task=${1:-check}
if [ "$#" -gt 0 ]; then shift; fi
case "$task" in
  fmt)
    [ "$#" -le 1 ] || usage
    if [ "$#" -eq 0 ]; then
      find cmd -name '*.go' -exec gofmt -w {} +
    elif [ "$1" = --check ]; then
      files=$(find cmd -name '*.go' -exec gofmt -l {} +)
      if [ -n "$files" ]; then printf 'Run ./x fmt:\n%s\n' "$files" >&2; exit 1; fi
    else usage; fi
    ;;
  check)
    [ "$#" -eq 0 ] || usage
    ./x fmt --check
    go vet ./...
    ./x test
    ./x docs
    ./x build
    ;;
  test)
    [ "$#" -eq 0 ] || usage
    go test -race ./...
    python3 -m unittest discover -s scripts -p 'test_*.py'
    ;;
  integration)
    [ "$#" -eq 0 ] || usage
    ./x build
    exec python3 scripts/integration.py
    ;;
  prove)
    exec python3 scripts/prove.py "$@"
    ;;
  compatibility)
    [ "$#" -eq 0 ] || usage
    exec python3 scripts/compatibility.py
    ;;
  docs)
    [ "$#" -eq 0 ] || usage
    exec python3 scripts/check_docs.py
    ;;
  build)
    [ "$#" -eq 0 ] || usage
    mkdir -p bin
    exec go build -trimpath -o bin/in ./cmd/in
    ;;
  release-check)
    [ "$#" -le 1 ] || usage
    exec python3 scripts/release.py check "$@"
    ;;
  dist)
    [ "$#" -eq 1 ] || usage
    exec python3 scripts/release.py dist "$1"
    ;;
  *) usage ;;
esac
