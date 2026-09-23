SHELL := /bin/bash

.PHONY: \
	build \
	test \
	lint \
	check \
	clean \
	install \
	release-mac \
	notarize-mac \
	verify-mac \
	release-mac-notarized \
	release-macos-notarized \
	upload-mac

BUILD_DATE := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
GIT_HEIGHT := $(shell git rev-list --count HEAD 2>/dev/null || echo 0)

GIT_DESC := $(shell git describe --always)
ifneq ($(shell git status --porcelain),)
GIT_DESC := $(GIT_DESC)-dirty
endif

# Metadata for ordinary development builds.
LDFLAGS := \
	-X main.buildVersion=B$(GIT_HEIGHT) \
	-X main.buildCommit=$(GIT_DESC) \
	-X main.buildDate=$(BUILD_DATE)

# Pass VERSION explicitly for a release, e.g.:
#
#   make release-mac-notarized VERSION=v0.0.13
#
VERSION ?= $(shell git describe --tags --exact-match 2>/dev/null || git describe --tags --always)

# Metadata embedded in release builds.
RELEASE_LDFLAGS := \
	-s -w \
	-X main.buildVersion=$(VERSION) \
	-X main.buildCommit=$(GIT_DESC) \
	-X main.buildDate=$(BUILD_DATE)

# First usable Developer ID Application identity in the current keychain.
# Override this if there is more than one, e.g.:
#
#   make release-mac-notarized VERSION=v0.0.13 \
#     SIGN_IDENTITY='Developer ID Application: Tailscale Inc. (W5364U7YZB)'
#
SIGN_IDENTITY ?= $(shell security find-identity -v -p codesigning 2>/dev/null | sed -n 's/.*"\(Developer ID Application[^"]*\)".*/\1/p' | head -1)

# Name passed to `xcrun notarytool store-credentials` on this Mac.
NOTARY_PROFILE ?= tailscale-notary

RELEASE_DIR := .build/release
ARM64_BIN := $(RELEASE_DIR)/aperture_darwin_arm64
AMD64_BIN := $(RELEASE_DIR)/aperture_darwin_amd64
ARM64_ARCHIVE := $(RELEASE_DIR)/aperture_$(VERSION)_darwin_arm64.zip
AMD64_ARCHIVE := $(RELEASE_DIR)/aperture_$(VERSION)_darwin_amd64.zip
CHECKSUMS := $(RELEASE_DIR)/aperture_$(VERSION)_darwin_checksums.txt

build:
	mkdir -p .build
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

# Build and sign the final architecture-specific macOS executables. Nothing
# must modify these files after this target finishes.
release-mac:
	@set -euo pipefail; \
	if [ -z "$(SIGN_IDENTITY)" ]; then \
		echo "error: no Developer ID Application identity found in the keychain"; \
		echo "run: security find-identity -v -p codesigning"; \
		exit 1; \
	fi; \
	mkdir -p "$(RELEASE_DIR)"; \
	echo "==> Building arm64 macOS release binary"; \
	GOOS=darwin GOARCH=arm64 go build \
		-ldflags "$(RELEASE_LDFLAGS)" \
		-o "$(ARM64_BIN)" \
		./cmd/aperture; \
	echo "==> Building amd64 macOS release binary"; \
	GOOS=darwin GOARCH=amd64 go build \
		-ldflags "$(RELEASE_LDFLAGS)" \
		-o "$(AMD64_BIN)" \
		./cmd/aperture; \
	echo "==> Checking binary architectures"; \
	lipo -archs "$(ARM64_BIN)"; \
	lipo -archs "$(AMD64_BIN)"; \
	echo "==> Signing as: $(SIGN_IDENTITY)"; \
	codesign --force --options runtime --timestamp \
		--sign "$(SIGN_IDENTITY)" "$(ARM64_BIN)"; \
	codesign --force --options runtime --timestamp \
		--sign "$(SIGN_IDENTITY)" "$(AMD64_BIN)"; \
	echo "==> Verifying binary signatures"; \
	codesign --verify --strict --verbose=4 "$(ARM64_BIN)"; \
	codesign --verify --strict --verbose=4 "$(AMD64_BIN)"

# Package the signed binaries and wait for Apple to notarize each archive.
# A ZIP is not itself code-signed: it contains the signed executable.
notarize-mac:
	@set -euo pipefail; \
	test -x "$(ARM64_BIN)"; \
	test -x "$(AMD64_BIN)"; \
	echo "==> Checking notarization credentials"; \
	xcrun notarytool history --keychain-profile "$(NOTARY_PROFILE)" >/dev/null; \
	echo "==> Rechecking signed binaries before packaging"; \
	codesign --verify --strict --verbose=4 "$(ARM64_BIN)"; \
	codesign --verify --strict --verbose=4 "$(AMD64_BIN)"; \
	echo "==> Creating release archives"; \
	rm -f "$(ARM64_ARCHIVE)" "$(AMD64_ARCHIVE)" "$(CHECKSUMS)"; \
	ditto -c -k --keepParent "$(ARM64_BIN)" "$(ARM64_ARCHIVE)"; \
	ditto -c -k --keepParent "$(AMD64_BIN)" "$(AMD64_ARCHIVE)"; \
	echo "==> arm64 archive contents"; \
	unzip -l "$(ARM64_ARCHIVE)"; \
	echo "==> amd64 archive contents"; \
	unzip -l "$(AMD64_ARCHIVE)"; \
	echo "==> Submitting arm64 archive for notarization"; \
	xcrun notarytool submit "$(ARM64_ARCHIVE)" \
		--keychain-profile "$(NOTARY_PROFILE)" \
		--wait; \
	echo "==> Submitting amd64 archive for notarization"; \
	xcrun notarytool submit "$(AMD64_ARCHIVE)" \
		--keychain-profile "$(NOTARY_PROFILE)" \
		--wait; \
	echo "==> Writing checksums"; \
	(cd "$(RELEASE_DIR)" && shasum -a 256 \
		"$(notdir $(ARM64_ARCHIVE))" \
		"$(notdir $(AMD64_ARCHIVE))" \
		> "$(notdir $(CHECKSUMS))")

# Extract and assess the exact executables that users receive. The find
# commands intentionally locate the binary by its known filename rather than
# assuming a particular ditto/unzip archive layout.
verify-mac:
	@set -euo pipefail; \
	test -f "$(ARM64_ARCHIVE)"; \
	test -f "$(AMD64_ARCHIVE)"; \
	ARM64_TEST_DIR="$$(mktemp -d /tmp/aperture-arm64.XXXXXX)"; \
	AMD64_TEST_DIR="$$(mktemp -d /tmp/aperture-amd64.XXXXXX)"; \
	trap 'rm -rf "$$ARM64_TEST_DIR" "$$AMD64_TEST_DIR"' EXIT; \
	echo "==> Extracting and verifying arm64 artifact"; \
	ditto -x -k "$(ARM64_ARCHIVE)" "$$ARM64_TEST_DIR"; \
	ARM64_EXTRACTED="$$(find "$$ARM64_TEST_DIR" -type f -name "$(notdir $(ARM64_BIN))" -print -quit)"; \
	if [ -z "$$ARM64_EXTRACTED" ]; then \
		echo "error: $(notdir $(ARM64_BIN)) was not found in $(ARM64_ARCHIVE)"; \
		find "$$ARM64_TEST_DIR" -maxdepth 4 -print; \
		exit 1; \
	fi; \
	echo "  found: $$ARM64_EXTRACTED"; \
	test -x "$$ARM64_EXTRACTED"; \
	codesign --verify --strict --verbose=4 "$$ARM64_EXTRACTED"; \
	echo "==> Extracting and verifying amd64 artifact"; \
	ditto -x -k "$(AMD64_ARCHIVE)" "$$AMD64_TEST_DIR"; \
	AMD64_EXTRACTED="$$(find "$$AMD64_TEST_DIR" -type f -name "$(notdir $(AMD64_BIN))" -print -quit)"; \
	if [ -z "$$AMD64_EXTRACTED" ]; then \
		echo "error: $(notdir $(AMD64_BIN)) was not found in $(AMD64_ARCHIVE)"; \
		find "$$AMD64_TEST_DIR" -maxdepth 4 -print; \
		exit 1; \
	fi; \
	echo "  found: $$AMD64_EXTRACTED"; \
	test -x "$$AMD64_EXTRACTED"; \
	codesign --verify --strict --verbose=4 "$$AMD64_EXTRACTED"; \
	echo "==> Verification passed"; \
	echo "==> Checksums: $(CHECKSUMS)"

# Full local macOS release flow: build, sign, archive, notarize, then verify.
release-mac-notarized: release-mac notarize-mac verify-mac
	@echo "==> Complete"; \
	echo "  $(ARM64_ARCHIVE)"; \
	echo "  $(AMD64_ARCHIVE)"; \
	echo "  $(CHECKSUMS)"; \
	echo "  next, once the tag's goreleaser run has published:"; \
	echo "    make upload-mac VERSION=$(VERSION)"

# Replace the unsigned darwin assets on the GitHub release for VERSION with
# the signed, notarized archives, and rewrite checksums.txt to match: the
# goreleaser assets are named aperture_darwin_<arch>.tar.gz while the signed
# ones are zips, so an upload alone would leave both on the release and the
# checksums pointing at the unsigned tarballs. Run only after the tag's
# goreleaser workflow has published. -R is explicit so this works from a
# checkout whose origin is not github.com.
upload-mac:
	@set -euo pipefail; \
	test -f "$(ARM64_ARCHIVE)"; \
	test -f "$(AMD64_ARCHIVE)"; \
	test -f "$(CHECKSUMS)"; \
	REPO=tailscale/aperture-cli; \
	echo "==> Deleting unsigned darwin assets from $(VERSION)"; \
	while read -r asset; do \
		case "$$asset" in \
			*darwin*) \
				echo "  delete: $$asset"; \
				gh release delete-asset "$(VERSION)" "$$asset" -R "$$REPO" --yes ;; \
		esac; \
	done < <(gh release view "$(VERSION)" -R "$$REPO" --json assets -q '.assets[].name'); \
	echo "==> Uploading signed archives"; \
	gh release upload "$(VERSION)" -R "$$REPO" \
		"$(ARM64_ARCHIVE)" "$(AMD64_ARCHIVE)"; \
	echo "==> Rewriting checksums.txt"; \
	WORK="$$(mktemp -d /tmp/aperture-checksums.XXXXXX)"; \
	trap 'rm -rf "$$WORK"' EXIT; \
	gh release download "$(VERSION)" -R "$$REPO" \
		--pattern checksums.txt --dir "$$WORK" --clobber; \
	grep -v darwin "$$WORK/checksums.txt" > "$$WORK/merged" || [ $$? -eq 1 ]; \
	cat "$(CHECKSUMS)" >> "$$WORK/merged"; \
	mv "$$WORK/merged" "$$WORK/checksums.txt"; \
	gh release upload "$(VERSION)" -R "$$REPO" "$$WORK/checksums.txt" --clobber; \
	echo "==> Release $(VERSION) now carries the signed darwin builds"

# Compatibility alias for the earlier target spelling.
release-macos-notarized: release-mac-notarized
