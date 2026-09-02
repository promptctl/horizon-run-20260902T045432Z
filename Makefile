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

# The conformance suite is excluded from the plain `go test ./...` run and
# invoked separately; see the conformance target for why.
UNIT_PKGS = $$(go list ./... | grep -v '/test/conformance$$')

.PHONY: all build test conformance vet fmt check clean

all: check build

build:
	go build $(LDFLAGS) -o $(BINARY) $(PKG)

test:
	go test $(UNIT_PKGS)
	@$(MAKE) --no-print-directory conformance

# The black-box suite on its own: it builds the command and observes it under a
# throwaway home directory. `make test` runs it too, as its own step.
#
# -count=1 is load-bearing, not a habit. This package imports nothing from
# cmd/ or internal/ -- it shells out to `go build` -- so the test cache key
# does not change when the implementation changes, and a cached PASS survives
# a broken program. Verified: replacing "Usage:" in internal/cli/usage.go left
# `go test ./test/conformance/` reporting "ok (cached)" while -count=1 gave 10
# failures. Never drop the flag, and never fold this package back into the
# cached `go test ./...` run above.
conformance:
	go test -count=1 ./test/conformance/

vet:
	go vet ./...

fmt:
	gofmt -l -w ./cmd ./internal ./test

check: vet test
	@test -z "$$(gofmt -l ./cmd ./internal ./test)" || { echo "gofmt needed:"; gofmt -l ./cmd ./internal ./test; exit 1; }

clean:
	rm -rf bin
