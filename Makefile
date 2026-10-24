BIN := bin/backpack

.PHONY: run build test check fixtures screenshots live-check

build:
	go build -o $(BIN) ./cmd/backpack

run: build
	./$(BIN)

test:
	go test ./...

check:
	@out="$$(gofmt -l cmd internal)"; if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi
	go vet -tags integration ./...
	go test -race -tags integration ./...

fixtures:
	scripts/fixtures.sh

screenshots:
	go test -tags screenshots -run TestScreenshots -count=1 -v ./internal/server

live-check:
	scripts/live-check.sh
