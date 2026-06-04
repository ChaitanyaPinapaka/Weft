.PHONY: run build build-ort release test test-ort lint clean app app-run ios ios-sim

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
	@cd dist && { sha256sum weft-* 2>/dev/null || shasum -a 256 weft-*; } > SHA256SUMS
	@echo "built dist/ for darwin, linux, windows (+ SHA256SUMS)"
	@echo "publish: upload dist/* to your host under <version>/ and latest/ (see site/DEPLOY.md)"

# app builds the native macOS surface (SwiftUI + WKWebView) into a signed
# .app bundle. Needs Xcode/Swift. The app is a thin client over the daemon —
# run `make run` (or `weft serve`) alongside it.
app:
	./macos/build.sh release

# app-run builds and launches the app (daemon must be running).
app-run:
	./macos/run.sh

# ios gomobile-binds the Weft engine (vault, sqlite index, E2EE sync) into an
# iOS xcframework, so the iPhone app is a full offline sync peer — no daemon. The
# pure-Go default build (no ORT); semantic surfacing uses a native iOS embedder.
# Needs Xcode + the iOS SDK. gomobile/gobind are auto-installed if missing.
ios:
	@mkdir -p ios/Frameworks
	@PATH="$$PATH:$$(go env GOPATH)/bin"; \
	 command -v gomobile >/dev/null || go install golang.org/x/mobile/cmd/gomobile@latest; \
	 command -v gobind   >/dev/null || go install golang.org/x/mobile/cmd/gobind@latest; \
	 gomobile bind -target=ios -o ios/Frameworks/WeftMobile.xcframework ./mobile
	@echo "built ios/Frameworks/WeftMobile.xcframework"

# ios-sim builds the iOS app and runs it in the iPhone simulator — one command
# for local testing. Run `make ios` first if the Go engine changed.
SIM ?= iPhone 17 Pro
ios-sim:
	cd ios && xcodegen generate
	xcodebuild -project ios/Weft.xcodeproj -scheme Weft -sdk iphonesimulator \
		-configuration Debug -destination 'platform=iOS Simulator,name=$(SIM)' \
		CODE_SIGNING_ALLOWED=NO build
	-xcrun simctl boot "$(SIM)"
	open -a Simulator
	xcrun simctl install booted "$$(find $$HOME/Library/Developer/Xcode/DerivedData -path '*Build/Products/Debug-iphonesimulator/Weft.app' | head -1)"
	xcrun simctl launch booted app.tryweft.ios

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
