.PHONY: build test lint check clean install release-mac

BUILD_DATE := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
GIT_HEIGHT := $(shell git rev-list --count HEAD 2>/dev/null || echo 0)

GIT_DESC := $(shell git describe --always)
ifneq ($(shell git status --porcelain),)
    GIT_DESC := $(GIT_DESC)-dirty
endif

LDFLAGS := -X main.buildVersion=B$(GIT_HEIGHT) -X main.buildCommit=$(GIT_DESC) -X main.buildDate=$(BUILD_DATE)

build:
	go build -ldflags "$(LDFLAGS)" -o .build/aperture ./cmd/aperture

test:
	go test ./...

# Nothing here needs installing: gofmt and vet ship with the toolchain, so a
# clean checkout can run this.
lint:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt:"; echo "$$out"; exit 1; fi
	go vet ./...
	go mod tidy -diff

# The gate. Differs from test in the two ways that matter here: it runs the
# suite under the race detector, and it builds. A bridge is several goroutines
# racing a control plane, so a data race is the failure this project actually
# has, and `go test` will not find one.
check: lint build
	go test -race ./...

install:
	go install -ldflags "$(LDFLAGS)" ./cmd/aperture

clean:
	rm -rf .build/

# First Developer ID Application identity in the keychain; override when there
# is more than one: make release-mac SIGN_IDENTITY="Developer ID Application: Name (TEAMID)"
SIGN_IDENTITY ?= $(shell security find-identity -v -p codesigning 2>/dev/null | sed -n 's/.*"\(Developer ID Application[^"]*\)".*/\1/p' | head -1)

# Signed macOS release binaries in .build/release/. Runs on the Mac that holds
# the cert; codesign and the identity do not exist elsewhere.
release-mac:
	@if [ -z "$(SIGN_IDENTITY)" ]; then echo "no Developer ID Application identity in the keychain"; exit 1; fi
	GOOS=darwin GOARCH=arm64 go build -ldflags "-s -w $(LDFLAGS)" -o .build/release/aperture_darwin_arm64 ./cmd/aperture
	GOOS=darwin GOARCH=amd64 go build -ldflags "-s -w $(LDFLAGS)" -o .build/release/aperture_darwin_amd64 ./cmd/aperture
	codesign --options runtime --timestamp --sign "$(SIGN_IDENTITY)" .build/release/aperture_darwin_arm64 .build/release/aperture_darwin_amd64
	codesign --verify --verbose=2 .build/release/aperture_darwin_arm64 .build/release/aperture_darwin_amd64
