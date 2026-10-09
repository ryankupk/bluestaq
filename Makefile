.PHONY: run build test test-unit test-e2e cover up down lint smoke

run:
	go run ./cmd/notesd

build:
	go build -o bin/notesd ./cmd/notesd

test:
	go test ./...

test-unit:
	go test -short ./...

# E2E tests need a live MongoDB; default to the local/compose one.
test-e2e:
	MONGO_URI=$${MONGO_URI:-mongodb://localhost:27017} go test ./test/e2e/...

cover:
	go test -coverpkg=./... -coverprofile=coverage.out ./...
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
