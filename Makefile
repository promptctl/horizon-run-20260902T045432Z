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

test:
	go test ./...

# The black-box suite on its own: it builds the command and observes it under a
# throwaway home directory. `make test` runs it too, as one package among many.
conformance:
	go test ./test/conformance/

vet:
	go vet ./...

fmt:
	gofmt -l -w ./cmd ./internal ./test

check: vet test
	@test -z "$$(gofmt -l ./cmd ./internal ./test)" || { echo "gofmt needed:"; gofmt -l ./cmd ./internal ./test; exit 1; }

clean:
	rm -rf bin
