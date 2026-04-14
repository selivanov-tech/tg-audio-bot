.PHONY: run build test vet deploy logs ssh sync-allowlist

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
	flyctl logs -a <fly-app>

ssh:
	flyctl ssh console -a <fly-app>

# Push allowed_chat_ids.txt to the Fly secret MEDIABOT_ALLOWED_CHAT_IDS.
# Strips comments (# ...) and blank lines; joins remaining IDs with commas.
# Fails (without calling flyctl) when the file is missing or has zero IDs.
sync-allowlist:
	@test -f allowed_chat_ids.txt || { echo "allowed_chat_ids.txt not found"; exit 1; }
	@ids=$$(sed -e 's/#.*//' -e 's/[[:space:]]//g' allowed_chat_ids.txt | grep -v '^$$' | paste -sd, -); \
	if [ -z "$$ids" ]; then echo "allowed_chat_ids.txt has no usable IDs"; exit 1; fi; \
	echo "Syncing allowlist: $$ids"; \
	flyctl secrets set MEDIABOT_ALLOWED_CHAT_IDS="$$ids" -c .infra/prod/fly.toml
