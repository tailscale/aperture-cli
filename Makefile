.PHONY: build test lint check clean install

BUILD_DATE := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
GIT_VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

GIT_DESC := $(shell git describe --always)
ifneq ($(shell git status --porcelain),)
    GIT_DESC := $(GIT_DESC)-dirty
endif

LDFLAGS := -X main.buildVersion=$(GIT_VERSION) -X main.buildCommit=$(GIT_DESC) -X main.buildDate=$(BUILD_DATE)

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
