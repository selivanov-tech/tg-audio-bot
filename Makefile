.PHONY: run build test vet deploy logs ssh sync-allowlist require-app

# The Fly app name is deliberately not committed, so `fly.toml` has no `app`
# key and every flyctl call must pass `-a`. Set FLY_APP in .env (gitignored)
# or export it in the environment.
FLY_APP ?= $(shell [ -f .env ] && sed -n 's/^FLY_APP=//p' .env)

require-app:
	@test -n "$(FLY_APP)" || { echo "FLY_APP is not set — add it to .env or export it"; exit 1; }

run:
	set -a; [ -f .env ] && . ./.env; set +a; go run ./cmd/media-bot

build:
	go build ./cmd/media-bot

test:
	go test ./internal/mediabot/...

vet:
	go vet ./...

deploy: require-app
	flyctl deploy --remote-only -c .infra/prod/fly.toml -a $(FLY_APP)

logs: require-app
	flyctl logs -a $(FLY_APP)

ssh: require-app
	flyctl ssh console -a $(FLY_APP)

# Push allowed_chat_ids.txt to the Fly secret MEDIABOT_ALLOWED_CHAT_IDS.
# Strips comments (# ...) and blank lines; joins remaining IDs with commas.
# Fails (without calling flyctl) when the file is missing or has zero IDs.
sync-allowlist: require-app
	@test -f allowed_chat_ids.txt || { echo "allowed_chat_ids.txt not found"; exit 1; }
	@ids=$$(sed -e 's/#.*//' -e 's/[[:space:]]//g' allowed_chat_ids.txt | grep -v '^$$' | paste -sd, -); \
	if [ -z "$$ids" ]; then echo "allowed_chat_ids.txt has no usable IDs"; exit 1; fi; \
	echo "Syncing allowlist: $$ids"; \
	flyctl secrets set MEDIABOT_ALLOWED_CHAT_IDS="$$ids" -c .infra/prod/fly.toml -a $(FLY_APP)
