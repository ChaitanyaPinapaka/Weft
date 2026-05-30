.PHONY: run build build-ort release test test-ort lint clean

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
# CGO_LDFLAGS points at /opt/homebrew/lib for brew-installed libs on Apple Silicon.
build-ort:
	@mkdir -p bin
	CGO_LDFLAGS="-L/opt/homebrew/lib" \
		go build -tags ORT -o $(BINARY) ./cmd/weft
	@echo "built $(BINARY) (ORT enabled)"

# release cross-compiles the pure-Go binary for every common device, so you can
# drop `weft` on any machine and `weft sync join`. These builds omit ORT
# embeddings (CGO + per-platform libs); semantic surfacing is an optional local
# upgrade via `make build-ort`. Sync + the brain's recency/backlink/co-access
# signals work fully without it.
release:
	@mkdir -p dist
	@for target in darwin/arm64 darwin/amd64 linux/arm64 linux/amd64 windows/amd64; do \
		os=$${target%/*}; arch=$${target#*/}; ext=""; \
		[ "$$os" = "windows" ] && ext=".exe"; \
		echo "  $$os/$$arch"; \
		GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 go build -o dist/weft-$$os-$$arch$$ext ./cmd/weft || exit 1; \
	done
	@echo "built dist/ for darwin, linux, windows"

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
