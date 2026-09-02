# macklebox -- build and check targets.
#
# The built command is named mackup: that is the observable command name the
# specification in appspec/ contracts for. "macklebox" is the project name.

BINARY  := bin/mackup
PKG     := ./cmd/mackup

# The Go sources the formatting targets act on: the whole module minus what the
# toolchain itself excludes. Written once and shared, because the two targets
# that use it must agree -- `make fmt` REWRITES what this matches, and a check
# target testing a different set than fmt rewrites is worse than either.
#
# Completed with -print to list them or -exec to act on them, never by
# expanding a captured list. `gofmt -l $$files` word-split and globbed its own
# argument: a path component holding a space became two arguments that do not
# exist, and one holding a glob metacharacter was expanded against the working
# directory -- the same hazard the build target quotes "$(BINARY)" for and that
# TestTheReaperFindsDirectoriesUnderAPathHoldingAGlobMetacharacter pins
# elsewhere on this branch. (The repository's OWN path is not the exposure:
# find is given ".", so what it prints is relative. A file or directory INSIDE
# the repository is.)
#
# -exec with + also declines to run gofmt at all when nothing matches, which
# matters: gofmt with no file arguments reads stdin and would hang the gate
# rather than fail it. The emptiness guard is kept anyway and made explicit,
# since silently checking nothing is the failure it exists to prevent.
GO_SOURCES := find . \( -name '.?*' -o -name '_*' -o -name vendor -o -name testdata -o -path ./bin \) -prune -o -name '*.go'

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
# the conformance target for why.
#
# The `conformance` build tag is what actually excludes it here, not the grep:
# every file in that package is behind the tag, so `go list ./...` untagged
# reports five packages and omits it silently, exiting 0. Verified, against
# `go list -tags conformance ./...`, which reports six. The grep therefore
# matches nothing today and the guard after it has never fired. Both stay, and
# are described as what they are -- cover for a tree where the tag is dropped
# or narrowed, in which case this cached `go test` run would start executing
# the conformance suite, which is the single thing the three mechanisms in
# harness_test.go's header exist to prevent. Do not delete them as dead code,
# and do not read them as the reason the package is absent.
#
# An empty package list is checked for before it is used. The reason recorded
# here was wrong and is corrected rather than deleted: it claimed `go test`
# would "fall through to the current directory and report success having run
# nothing". It does fall through to the current directory, but at THIS module
# root that exits 1 with "no Go files in <root>" -- verified, rc=1 -- so an
# empty list fails today rather than passing. What the guard buys is a message
# that names the cause, and cover for a tree that later grows a package at the
# module root, where the fall-through would silently test that one package
# instead of all of them. That is a weaker warrant than the gofmt guard below,
# whose stated hazard is real and immediate, and the two are not equivalent.
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
#
# Run twice, the second time under -trimpath, because the suite finds its own
# files at runtime and -trimpath is what breaks that. moduleRoot says so in its
# own comment -- "-trimpath rewrites the compiled-in file path to a
# module-relative one" -- and that claim went unchecked until a case added
# beside it used runtime.Caller anyway and shipped: `go test -trimpath` failed
# with "open github.com/promptctl/macklebox/test/conformance: no such file or
# directory", while this gate stayed green. An unenforced claim is decoration.
#
# It is a real invocation people make, not a hypothetical: -trimpath is the
# standard flag for reproducible builds, and a machine carrying
# GOFLAGS=-trimpath in its go env applies it to this recipe without anyone
# typing it. That is the same go env file the forced-stamp GOFLAGS merge reads
# from elsewhere on this branch.
#
# The whole suite, not a -run subset naming the cases that resolve paths
# today. Narrowing a walk or a list to where the problem is believed to live is
# the mistake this branch has now made in four separate places, and it costs
# 1.7s warm -- measured against 1.7s for the plain run, not estimated -- so
# there is nothing to buy by narrowing it.
conformance:
	go test -count=1 -tags conformance ./test/conformance/
	go test -trimpath -count=1 -tags conformance ./test/conformance/

# Tagged, so the conformance package is vetted too rather than skipped along
# with the rest of the default build.
#
# And vetted for a second GOOS, which is not gold-plating: harness_unix_test.go
# is behind `conformance && unix` because it makes a FIFO, and a case in the
# untagged argv_test.go was written calling a helper that lived there. The
# package stopped compiling on every non-unix GOOS and this gate could not see
# it, because `go vet` without GOOS vets the host and the host is unix. A
# Windows contributor running this same target would have met an
# undefined-symbol error instead of a suite that simply does not apply.
#
# windows is the cheapest GOOS that excludes the `unix` constraint -- plan9
# would do as well but has no plan9/arm64 pair, so it is not portable as a
# fixed choice. Vetting, not building: this only has to answer "does the
# untagged half of the package still compile without the unix half", and it
# does that without a toolchain for that platform.
#
# Scoped to that package and not ./..., which is the difference between the
# invariant and a trap. This is a dotfile manager: appspec/01's permission
# rules and appspec/05's link engine bring syscall and x/sys/unix into
# internal/ soon enough, and the first such file would fail this gate on every
# developer machine for a platform the project does not target. The conformance
# package is where a build tag is load-bearing, so it is the only place the
# question is worth asking.
vet:
	go vet -tags conformance ./...
	GOOS=windows go vet -tags conformance ./test/conformance/

# The same GO_SOURCES as the check target's gofmt step -- one definition, not
# two copies -- for the reason given where it is defined. This comment used to
# say the list was "spelled out twice ... so change both", which was true until
# the definition was shared and then described code that no longer existed and
# sent the next editor after a second copy there isn't one of.
#
# The other half of that sentence was wrong too, and worth being exact about
# since the difference is real: this target does NOT guard gofmt's exit status
# the way check does. It relies on find being the recipe's last command, so a
# failing gofmt fails the target through make's own exit-status handling. check
# cannot do that -- it has to capture the output to test it for emptiness --
# which is why the explicit `|| { echo "make: gofmt failed"; exit 1; }` is
# there and not here.
fmt:
	@$(GO_SOURCES) -print | grep -q . || { echo "make: found no Go files to format" >&2; exit 1; }; \
		$(GO_SOURCES) -exec gofmt -l -w {} +

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
#
# The whole module is searched rather than ./cmd ./internal ./test, for the
# reason TestEveryDocCommentNamesWhatItDocuments now walks the module root: a
# renamed directory failed loudly, but a NEW top-level package silently escaped
# the formatting gate.
#
# The prune list matters more here than in that walk, because `make fmt` shares
# it and fmt REWRITES what it finds. bin is the Makefile's own build output.
# vendor is third-party source carrying no gofmt guarantee: left in, the first
# `go mod vendor` turns this gate permanently red over code the project does
# not own, and `make fmt` rewrites those dependencies in place -- which is a
# good deal worse than a red gate.
#
# Names beginning with "." or "_" are pruned for the same reason, and that
# corrects a call made one commit earlier. The claim then was that such
# directories "hold the project's own code when they hold anything", so a loud
# failure over them was the useful kind. That is not what they hold: a
# `git worktree add .worktrees/x` holds another branch's source, and .direnv,
# .gopath and .tools hold third-party trees. The Go toolchain excludes both
# prefixes outright -- `go vet ./...` and `go list ./...` ignore a
# .scratchcheck/x.go this find happily listed, observed -- so the gate was
# stricter than the compiler over files the compiler does not consider part of
# the module, and `make fmt` would rewrite another branch's checkout in place.
# The vendor argument applies unchanged: a loud failure is only useful over
# code someone here can fix.
#
# ".?*" and not ".*", so that find does not prune the "." it was told to start
# from and match nothing at all.
#
# testdata is excluded, for the reason `go build` excludes it: nothing under it
# is part of the build, so it is where a package keeps fixtures that are
# deliberately not valid Go. `gofmt -l` does not skip testdata on its own --
# it exits 2 on such a file with empty stdout, which the guard above turns into
# a failed gate naming formatting for a file no formatting applies to.
# TestEveryDocCommentNamesWhatItDocuments had the same hole and skipping it
# only there would have moved the red rather than removed it; both exclusions
# are needed and neither is sufficient. test/conformance/testdata holds the
# fixture that keeps both exercised.
#
# The file list is checked for emptiness before it is used, and that guard is
# load-bearing in a way the go list one is not: `gofmt -l` with no arguments
# reads STDIN, so a find that matched nothing would hang the gate rather than
# fail it. Verified.
check: vet test
	@$(GO_SOURCES) -print | grep -q . || { echo "make: found no Go files to format-check" >&2; exit 1; }; \
		unformatted="$$($(GO_SOURCES) -exec gofmt -l {} +)" || { echo "make: gofmt failed" >&2; exit 1; }; \
		test -z "$$unformatted" || { echo "gofmt needed:"; printf '%s\n' "$$unformatted"; exit 1; }

clean:
	rm -rf bin
