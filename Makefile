BIN := bin/asana-extractor

.PHONY: build run run-once run-30s fmt fmt-check vet test race check live-test clean

build:
	go build -o $(BIN) ./cmd/asana-extractor

run: build
	./$(BIN)

run-once: build
	./$(BIN) --once

run-30s: build
	./$(BIN) --interval=30s

fmt:
	gofmt -w .

fmt-check:
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

vet:
	go vet ./...

test:
	go test ./...

race:
	go test -race ./...

check: fmt-check vet test race
	go build ./...

# Hits the real Asana API; requires ASANA_ACCESS_TOKEN and ASANA_WORKSPACE_GID in the environment or .env.
# Note: this target sources .env with the shell, so .env values override the environment here.
live-test:
	@set -a; if [ -f .env ]; then . ./.env; fi; set +a; \
	ASANA_LIVE_TEST=1 go test -count=1 ./internal/asana -run TestLive -v

clean:
	rm -rf bin
