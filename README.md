# macklebox

An MIT-licensed tool for keeping application settings in sync across machines:
it keeps the one real copy of each config file in a folder that already syncs
between your machines (a cloud-drive folder or any replicated directory) and
wires each application to read it from there.

This repository is a clean-room reimplementation built **only** from the
behavioral specification in [`appspec/`](appspec/) — a black-box description of
an existing config-sync tool's observable behavior, written so an independent
team can rebuild a behaviorally-equivalent, command-line-compatible program
without reference to any other implementation.

## Status

Specification complete; implementation in progress. Start at
[`appspec/00-overview.md`](appspec/00-overview.md) — the spec reads top-down
through altitudes (product contract → architecture → boundary detail).

The command line boundary (invocation grammar, options, dispatch order, exit
codes) is in place; the resolvers and sync operations behind it are not yet,
and every subcommand reports itself as unimplemented.

## Implementation

Written in **Go**, with no dependencies outside the standard library. The spec
([`appspec/00-overview.md`](appspec/00-overview.md), "Platform assumptions")
states that a reimplementation in any language reproducing the observable
behavior is conformant; Go was chosen because the whole contract is a process
boundary, and a single static binary makes that boundary cheap to exercise
exactly as a user would — build once, run it under a throwaway `HOME`, and
assert on stdout, stderr, the exit code, and the resulting filesystem.

The built command is named `mackup`, not `macklebox`: the observable surface —
command name, grammar, output text, `~/.mackup.cfg`, the `Mackup <version>`
string — is the specification's. `macklebox` is the project and module name
only.

```sh
make build        # -> bin/mackup   (release builds stamp VERSION=x.y.z)
make check        # go vet, go test (unit + conformance), gofmt check
make conformance  # the black-box suite alone (needs -tags conformance)
```

`make check` is the gate: [`.github/workflows/ci.yml`](.github/workflows/ci.yml)
runs exactly that command on every pull request and every push to `master`.

### The conformance suite

`test/conformance/` builds the command and observes it the way
[`appspec/00-overview.md`](appspec/00-overview.md) says the specification
itself was written: running the real program under a throwaway home directory
and watching its boundary — stdout, stderr, the exit code, and the filesystem
it leaves behind. Nothing in it reaches inside the program.

Because the spec promises things no single command states — that `--help`
touches nothing, that a rejected run makes no filesystem change, that
`--dry-run` mutates nothing — a case can snapshot the scratch root and assert
it is unchanged, not merely that the output looked right. The whole root, not
just the home directory inside it: `appspec/04`'s `file_system` engine takes an
arbitrary path, so the Mackup folder need not live under `HOME`. A case that
can only be checked by calling an internal function belongs in that package's
own tests instead.

The suite is behind the **`conformance` build tag** and always runs with
**`-count=1`**. The package imports nothing from `cmd/` or `internal/` — it
shells out to `go build` — so Go's test cache cannot see the implementation,
and a cached `ok` will happily outlive a program that has since been broken.
`-count=1` defeats that in the Makefile; the tag is what defeats it everywhere
else, by keeping the package out of the default build so that a plain
`go test ./...` — an IDE, `gopls`, another CI job — cannot report a stale pass
for it either. Do not remove either one.

Every case must be able to fail for the reason it claims. Two rules follow:
assert what the program *did*, never merely that it did not print a usage
error; and never pin help, usage, or warning wording, which `appspec/02`
declares human-facing and not contract — the suite knows that wording in one
named constant, `usageMarker`, and nowhere else.

`--version` reports the package's own version when one was stamped in, and the
fallback token `unknown` otherwise, per the spec's provenance rule.

## Layout

| Path | What it is |
|------|------------|
| `appspec/` | The functional specification that drives the build (source of truth) |
| `cmd/mackup/` | The command entry point |
| `internal/cli/` | argv grammar, options, usage errors (`appspec/02`) |
| `internal/app/` | The startup pipeline and subcommand dispatch (`appspec/01` §4) |
| `internal/ui/` | The two output streams (`appspec/07`) |
| `internal/version/` | Version-string resolution (`appspec/00` provenance) |
| `test/conformance/` | Black-box suite: runs the built command under a throwaway `HOME` |
| `LICENSE`  | MIT |
