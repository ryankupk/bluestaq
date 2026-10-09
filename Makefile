.PHONY: run build test test-unit test-e2e cover up down lint smoke

run:
	go run ./cmd/notesd

build:
	go build -o bin/notesd ./cmd/notesd

# MongoDB-backed tests (store conformance, e2e) run against the local/compose
# MongoDB by default; set MONGO_URI to point elsewhere.
MONGO_URI ?= mongodb://localhost:27017
export MONGO_URI

test:
	go test -race -count=1 ./...

# -short skips every test that needs MongoDB.
test-unit:
	go test -short ./...

test-e2e:
	go test -race -count=1 ./test/e2e/...

# coverpkg is limited to internal/ so the thin cmd mains do not drag the number down.
cover:
	go test -count=1 -coverpkg=./internal/... -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	go tool cover -func=coverage.out | tail -1

up:
	docker compose up --build -d

down:
	docker compose down

lint:
	go vet ./...

smoke:
	bash scripts/smoke.sh
