VERSION ?= dev
# Release tags start with v; the version the binary reports does not.
BUILD_VERSION := $(patsubst v%,%,$(VERSION))
LDFLAGS := -s -w -X main.version=$(BUILD_VERSION)
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64
NATIVE_SUFFIX := $(if $(filter windows,$(shell go env GOOS)),.exe)

.PHONY: build build-all test race lint docs docs-check conformance integration clean

build:
	mkdir -p bin
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/mailmd$(NATIVE_SUFFIX) ./cmd/mailmd

build-all:
	mkdir -p dist
	rm -f dist/mailmd-* dist/checksums.txt
	@set -e; for platform in $(PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		asset=mailmd-$$os-$$arch; \
		if [ "$$os" = windows ]; then asset=$$asset.exe; fi; \
		echo "Building $$asset"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o "dist/$$asset" ./cmd/mailmd; \
	done
	@cd dist && if command -v sha256sum >/dev/null 2>&1; then \
		sha256sum mailmd-* > checksums.txt; \
	else shasum -a 256 mailmd-* > checksums.txt; fi

test:
	go test ./...

race:
	go test -race ./...

lint:
	@test -z "$$(gofmt -l cmd internal scripts)" || { gofmt -l cmd internal scripts; exit 1; }
	go vet ./...

docs:
	go run ./internal/cli/generate

docs-check:
	go test ./internal/cli -run '^TestGeneratedDocumentationMatchesCatalog$$' -count=1
	python3 scripts/docs-links.py

integration:
	./scripts/integration.sh

conformance:
	go test ./internal/cli -run '^TestOfflineConformance$$' -count=1 -v

clean:
	rm -rf bin dist
