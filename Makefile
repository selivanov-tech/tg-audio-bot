.PHONY: run build test vet

run:
	set -a; [ -f .env ] && . ./.env; set +a; go run ./cmd/media-bot

build:
	go build ./cmd/media-bot

test:
	go test ./internal/mediabot/...

vet:
	go vet ./...
