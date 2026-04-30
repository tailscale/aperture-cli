.PHONY: build test clean install

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

install:
	go install -ldflags "$(LDFLAGS)" ./cmd/aperture

clean:
	rm -rf .build/
