.PHONY: run build test vet deploy logs ssh

run:
	set -a; [ -f .env ] && . ./.env; set +a; go run ./cmd/media-bot

build:
	go build ./cmd/media-bot

test:
	go test ./internal/mediabot/...

vet:
	go vet ./...

deploy:
	flyctl deploy --remote-only -c .infra/prod/fly.toml

logs:
	flyctl logs -a tg-audio-bot

ssh:
	flyctl ssh console -a tg-audio-bot
