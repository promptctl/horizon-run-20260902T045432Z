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
make build     # -> bin/mackup   (release builds stamp VERSION=x.y.z)
make check     # go vet, go test, gofmt check
```

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
| `LICENSE`  | MIT |
