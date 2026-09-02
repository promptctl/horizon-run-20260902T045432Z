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

# BINARY is quoted: the conformance suite builds through this target and passes
# a path derived from TMPDIR, which is not always space-free.
build:
	go build $(LDFLAGS) -o "$(BINARY)" $(PKG)

# Every package but the conformance suite, which is run separately below; see
# the conformance target for why. An empty package list would make `go test`
# fall through to the current directory and report success having run nothing,
# so the list is checked before it is used.
#
# go list runs on its own line because a pipeline reports the exit status of
# its LAST command: written as `go list ./... | grep -v ...`, a go list that
# failed was reported as grep's success. It can fail while still printing
# packages -- an unresolvable import in one package exits 1 and lists the rest
# -- which would have run `go test` over a silently short list.
test:
	@all="$$(go list ./...)" || { echo "make: go list failed" >&2; exit 1; }; \
		packages="$$(printf '%s\n' "$$all" | grep -v '/test/conformance$$')"; \
		test -n "$$packages" || { echo "make: go list produced no packages" >&2; exit 1; }; \
		go test $$packages
	@$(MAKE) --no-print-directory conformance

# The black-box suite on its own: it builds the command and observes it under a
# throwaway home directory. `make test` runs it too, as its own step.
#
# -count=1 is load-bearing, not a habit, but the gap it closes is narrower
# than this comment used to claim and the claim is corrected here rather than
# deleted. It said nothing about the implementation reaches the test cache key,
# citing a reword of "Usage:" in internal/cli/usage.go that served a cached
# PASS. That no longer reproduces: readImplementationSources opens every file
# under the module root from inside a case, so cmd/go records the reads and
# that reword now fails without -count=1. Re-verified after the walk landed.
#
# What remains is the limit of the mechanism itself. cmd/go's hashOpen folds
# in size, mode and mtime and explicitly not content, so an edit that moves
# neither is invisible to it. Verified, and it is not a thought experiment:
# rewriting "Usage:" as "Usage!" -- the same byte length -- and putting the
# nanosecond mtime back with utime left `go test -tags conformance
# ./test/conformance/` reporting "ok (cached)" over a program whose banner was
# broken, while the same tree under -count=1 failed. (Restore the stamp with
# nanosecond precision or the experiment lies: utime given float seconds
# truncates, the mtime differs, and the run misses the cache for that reason
# instead.)
#
# The tag belongs in that command -- without it the package is excluded from
# the build and the run fails on setup, which says nothing about caching
# either way.
#
# This flag only covers what runs through this recipe. Two other mechanisms
# cover what does not, and the header of test/conformance/harness_test.go says
# which is which. Never drop any of the three.
conformance:
	go test -count=1 -tags conformance ./test/conformance/

# Tagged, so the conformance package is vetted too rather than skipped along
# with the rest of the default build.
vet:
	go vet -tags conformance ./...

fmt:
	gofmt -l -w ./cmd ./internal ./test

# build is deliberately NOT a prerequisite here, and the reasoning it replaces
# is recorded because it was wrong twice over.
#
# The claim was that nothing else exercises the build recipe. The conformance
# suite runs it on every case, twice: `make build BINARY=<tmpdir> VERSION=`
# for the development binary and the same with VERSION=<n> for a release one,
# so the recipe, both LDFLAGS branches and the -X symbol are all covered. The
# only thing a default-path build adds is the literal value of BINARY.
#
# The claim's evidence was 6ac9abc, "a release build the rig had broken while
# the whole gate stayed green". That commit changed no Makefile, and a build
# prerequisite would not have caught its bug: restoring that bug (the inner
# make no longer pinning VERSION, MAKEFLAGS left in the child environment) and
# running `make check` WITH build as a prerequisite exits 0. What catches it is
# the conformance suite under `make check VERSION=0.1.0`, which fails
# TestVersionReportsTheFallbackTokenForAnUninstalledBuild. Both observed.
#
# Against that, the cost is real: BINARY defaults to bin/mackup and build is
# .PHONY, so a gate that builds overwrites a stamped release artifact with an
# unstamped one. Observed: `make build VERSION=9.9.9` reports Mackup 9.9.9,
# and a `make check` after it reports Mackup unknown -- shipping the gate's
# binary rather than the release. The default path is built in CI instead,
# where the checkout is fresh and there is nothing to clobber.
# The gofmt status is checked, not just its output -- the same defect the test
# target's go list guard describes, in the other direction. `gofmt -l` writes
# its diagnostics to stderr and nothing to stdout when it fails, so the old
# `test -z "$$(gofmt -l ...)"` spelling passed on a gofmt that had not
# formatted anything: verified with `gofmt -l ./cmd ./internal ./nope`, which
# exits 2 with empty stdout and satisfied the guard. A later ticket renaming
# one of these directories would have turned the formatting check off and left
# the gate green.
check: vet test
	@unformatted="$$(gofmt -l ./cmd ./internal ./test)" || { echo "make: gofmt failed" >&2; exit 1; }; \
		test -z "$$unformatted" || { echo "gofmt needed:"; printf '%s\n' "$$unformatted"; exit 1; }

clean:
	rm -rf bin
