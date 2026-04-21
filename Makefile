.PHONY: build run test lint fmt generate migrate-up migrate-down mock docker-up docker-down

# --- Build & Run ---

build:
	go build -ldflags="-s -w" -trimpath -o bin/bot cmd/bot/main.go

run:
	go run cmd/bot/main.go

# --- Code Quality ---

lint:
	golangci-lint run ./...

fmt:
	gofmt -w .
	goimports -w -local github.com/yurii-merker/commute-tracker .

# --- Testing ---

test:
	go test -race -count=1 ./...

test-v:
	go test -race -count=1 -v ./...

test-cover:
	go test -race -count=1 -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

# --- Code Generation ---

generate: sqlc mock

sqlc:
	sqlc generate

mock:
	go generate ./...

# --- Database ---

migrate-up:
	migrate -path migrations -database "$$DATABASE_URL" up

migrate-down:
	migrate -path migrations -database "$$DATABASE_URL" down 1

migrate-create:
	@read -p "Migration name: " name; \
	migrate create -ext sql -dir migrations -seq $$name

# --- Docker ---

docker-up:
	docker compose up -d

docker-down:
	docker compose down

docker-logs:
	docker compose logs -f

recreate:
	docker compose build --no-cache && docker compose up -d --remove-orphans
