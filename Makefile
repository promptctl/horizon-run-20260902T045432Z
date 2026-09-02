# macklebox -- build and check targets.
#
# The built command is named mackup: that is the observable command name the
# specification in appspec/ contracts for. "macklebox" is the project name.

BINARY  := bin/mackup
PKG     := ./cmd/mackup

# Leave VERSION empty for a development build: the program then reports the
# fallback token appspec/00-overview.md specifies for an uninstalled tree.
# Release builds stamp their own version, e.g. `make build VERSION=0.1.0`.
VERSION ?=
LDFLAGS := $(if $(VERSION),-ldflags "-X github.com/promptctl/macklebox/internal/version.value=$(VERSION)")

.PHONY: all build test conformance vet fmt check clean

all: check build

build:
	go build $(LDFLAGS) -o $(BINARY) $(PKG)

# Every package but the conformance suite, which is run separately below; see
# the conformance target for why. An empty package list would make `go test`
# fall through to the current directory and report success having run nothing,
# so the list is checked before it is used.
test:
	@packages="$$(go list ./... | grep -v '/test/conformance$$')"; \
		test -n "$$packages" || { echo "make: go list produced no packages" >&2; exit 1; }; \
		go test $$packages
	@$(MAKE) --no-print-directory conformance

# The black-box suite on its own: it builds the command and observes it under a
# throwaway home directory. `make test` runs it too, as its own step.
#
# -count=1 is load-bearing, not a habit. This package imports nothing from
# cmd/ or internal/ -- it shells out to `go build` -- so the test cache key
# does not change when the implementation changes, and a cached PASS survives
# a broken program. Verified: replacing "Usage:" in internal/cli/usage.go left
# `go test ./test/conformance/` reporting "ok (cached)" while -count=1 gave 10
# failures.
#
# The `conformance` build tag is what enforces that, since a Makefile cannot:
# it keeps the package out of the default build, so a plain `go test ./...`
# -- an IDE, gopls, another CI job -- cannot report a cached pass for it
# either. Never drop either the flag or the tag.
conformance:
	go test -count=1 -tags conformance ./test/conformance/

# Tagged, so the conformance package is vetted too rather than skipped along
# with the rest of the default build.
vet:
	go vet -tags conformance ./...

fmt:
	gofmt -l -w ./cmd ./internal ./test

check: vet test
	@test -z "$$(gofmt -l ./cmd ./internal ./test)" || { echo "gofmt needed:"; gofmt -l ./cmd ./internal ./test; exit 1; }

clean:
	rm -rf bin
