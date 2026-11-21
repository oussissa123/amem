#!/usr/bin/env bash
# Exécute la chaîne Go dans le conteneur golang (Go n'est pas installé sur l'hôte).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
docker volume create amem-gocache >/dev/null
exec docker run --rm -i --network host \
  -u "$(id -u):$(id -g)" \
  -e HOME=/tmp -e GOCACHE=/cache/build -e GOMODCACHE=/cache/mod -e GOFLAGS=-buildvcs=false \
  -e AMEM_TEST_DATABASE_URL="${AMEM_TEST_DATABASE_URL:-}" \
  -v amem-gocache:/cache -v "$ROOT":/src -w /src \
  golang:1.26 go "$@"
