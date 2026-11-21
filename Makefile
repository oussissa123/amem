# Go tourne dans un conteneur (scripts/go.sh) : aucune installation de Go requise sur l'hôte.
GO := ./scripts/go.sh
TEST_DB ?= postgres://amem:amem@127.0.0.1:5433/postgres?sslmode=disable

.PHONY: build test test-unit vet db

build:
	$(GO) build -o bin/amem ./cmd/amem

test:
	AMEM_TEST_DATABASE_URL="$(TEST_DB)" $(GO) test -race -count=1 ./...

test-unit:
	$(GO) test -count=1 ./...

vet:
	$(GO) vet ./...

db:
	docker volume create amem-db >/dev/null
	docker compose up -d db
