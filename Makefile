.PHONY: run build build-ort test test-ort lint clean

VAULT ?= ~/notes
BINARY = bin/weft

run:
	go run ./cmd/weft serve $(VAULT)

build:
	@mkdir -p bin
	go build -o $(BINARY) ./cmd/weft
	@echo "built $(BINARY)"

# build-ort enables the real bge-small embedder via ONNX Runtime.
# Requires: libonnxruntime + libtokenizers.a — see README "Embeddings".
build-ort:
	@mkdir -p bin
	go build -tags ORT -o $(BINARY) ./cmd/weft
	@echo "built $(BINARY) (ORT enabled)"

test:
	go test ./...

test-ort:
	go test -tags ORT ./...

lint:
	gofmt -s -w .
	golangci-lint run

clean:
	rm -rf bin/

# First-time setup: fetch SQLite driver (requires network)
deps:
	go get modernc.org/sqlite
	go mod tidy
