# hlp: the Hyperliquid terminal for vaults.
# Uses the Go toolchain at /usr/local/opt/go/bin when present (the go on PATH
# may be ancient). Override with `make GO=go`.

GO      ?= $(shell [ -x /usr/local/opt/go/bin/go ] && echo /usr/local/opt/go/bin/go || echo go)
BIN     := bin/hlp
PKG     := ./...
# No leading v, matching GoReleaser ({{ .Version }}) and `go install` builds.
VERSION ?= $(shell v=$$(git describe --tags --always --dirty 2>/dev/null) && echo "$${v\#v}" || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

.PHONY: all build run test test-race test-live vet fmt fmt-check lint tidy check clean stubs golden chart-preview gifs \
	cross vuln licenses license-check

all: check build

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/hlp

run: build
	./$(BIN) $(ARGS)

# Unit tests never touch the network: fixtures live in testdata/.
test:
	TZ=UTC $(GO) test $(PKG)

test-race:
	TZ=UTC $(GO) test -race $(PKG)

# Live smoke tests against public read-only endpoints (opt-in).
test-live:
	HLP_LIVE=1 TZ=UTC $(GO) test -run Live $(PKG)

# Regenerate golden files (charts, cards, theme preview) after an
# intentional rendering change. Review the diff before committing.
golden:
	TZ=UTC $(GO) test ./internal/chart ./internal/tui/cards ./internal/theme -update

# Write ANSI chart frames (and PNG renders) for eyeballing: cat the file.
PREVIEW ?= $(or $(TMPDIR),/tmp)/hlp-chart-preview.ansi
chart-preview:
	HLP_CHART_PREVIEW=$(PREVIEW) TZ=UTC $(GO) test ./internal/chart -run TestPreview -count=1 -v
	@echo "cat $(PREVIEW)"

vet:
	$(GO) vet $(PKG)

fmt:
	gofmt -w cmd internal

fmt-check:
	@out=$$(gofmt -l cmd internal); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

# Keeps every pinned dependency (internal/deps uses the "deps" build tag).
tidy:
	$(GO) mod tidy
	$(GO) build -tags deps ./internal/deps

check: fmt-check vet test

# Every release target, CGO off, as GoReleaser builds them (RELEASE R7).
TARGETS := darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64 windows/arm64
cross:
	@for t in $(TARGETS); do echo "build $$t"; GOOS=$${t%/*} GOARCH=$${t#*/} CGO_ENABLED=0 $(GO) build -o /dev/null ./cmd/hlp || exit 1; done

# Known-vulnerability scan of reachable code (SECURITY S12).
vuln:
	$(GO) run golang.org/x/vuln/cmd/govulncheck@latest $(PKG)

# Third-party licence and NOTICE files into ./third_party_licenses (RELEASE R3).
licenses:
	PATH="$$(dirname $(GO)):$$PATH" sh scripts/licenses.sh

# Refuse any dependency whose licence is not permissive (PLAN D2), on every OS.
# The tool is built for the host first; GOOS only selects the files analysed.
ALLOWED_LICENSES := MIT,Apache-2.0,BSD-2-Clause,BSD-3-Clause,ISC
license-check:
	@tools=$$(mktemp -d) && trap 'rm -rf "$$tools"' EXIT && \
	GOBIN="$$tools" $(GO) install github.com/google/go-licenses/v2@v2.0.1 && \
	for os in darwin linux windows; do echo "go-licenses check ($$os)"; \
		PATH="$$(dirname $(GO)):$$PATH" GOOS=$$os "$$tools/go-licenses" check ./cmd/hlp --allowed_licenses=$(ALLOWED_LICENSES) || exit 1; done

clean:
	rm -rf bin dist third_party_licenses completions

# Render the README GIFs from docs/tapes (needs vhs and a built hlp on PATH;
# not part of check). Each tape writes docs/gifs/<name>.gif.
gifs: build
	@mkdir -p docs/gifs
	@for t in docs/tapes/*.tape; do [ "$$(basename $$t)" = settings.tape ] && continue; echo "vhs $$t"; PATH="$(CURDIR)/bin:$$PATH" vhs "$$t" || exit 1; done
