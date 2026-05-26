.PHONY: run build test lint clean

VAULT ?= ~/notes
BINARY = bin/weft

run:
	go run ./cmd/weft serve $(VAULT)

build:
	@mkdir -p bin
	go build -o $(BINARY) ./cmd/weft
	@echo "built $(BINARY)"

test:
	go test ./...

lint:
	gofmt -s -w .
	golangci-lint run

clean:
	rm -rf bin/

# First-time setup: fetch SQLite driver (requires network)
deps:
	go get modernc.org/sqlite
	go mod tidy
