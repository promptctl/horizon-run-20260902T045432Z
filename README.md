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
codes) is in place, and so are the three stages behind it: the built-in
application catalog, config discovery plus storage-location resolution, and
the application database that layers the three definition directories. So
every subcommand except `--help` and `--version` now reads your real
`~/.mackup.cfg`, resolves your real storage location, and assembles the
database from `~/.mackup/`, `$XDG_CONFIG_HOME/mackup/applications/` and the
built-in set before it does anything else, and aborts there if any of the
three is wrong. The environment gate and the sync operations themselves are
not yet, and nothing prints the database: a subcommand that gets past
assembly still reports itself as unimplemented.

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
runs it on every pull request and every push to `master`, then `make build` --
which `check` deliberately does not depend on, for the reason given above that
target in the Makefile.

### The built-in application catalog

`internal/catalog/applications/` holds one `.cfg` definition per application —
the lowest-precedence of the three definition directories in `appspec/05`, the
one "that ships with the program". It is embedded in the binary, because there
is no install step that could place a directory beside it.

The application **keys** are the specification's:
[`appspec/appendix-application-names.md`](appspec/appendix-application-names.md)
records all 614 of them, and a test fails if the shipped set and the appendix
disagree in either direction. The **display names and file sets are not** — the
appendix records keys and nothing else, and says so — so every one of them was
authored for this project from general knowledge of where each program keeps
its configuration. No definition data was copied from any other implementation;
the reference build's data files carry its own license, and this repository is
MIT.

A definition with an empty file set is valid per `appspec/05` and still lists
and shows, so a key whose paths are not yet authored is not a gap in
conformance. File-set coverage grows by editing these files; the key set does
not, and the parity test is what says so.

### Config and storage resolution

`internal/config/` is the second stage of the pipeline, and `appspec/02` puts
every subcommand except `--help` and `--version` behind it: the config is
loaded before dispatch, so a broken one aborts `list` and `show` too, even
though those "otherwise touch no storage". It implements `appspec/03` —
three-candidate discovery (`~/.mackup.cfg`, then `$MACKUP_CONFIG`, then the
XDG candidate), the `-c/--config-file` override that skips discovery, the
home-directory containment rule, the INI dialect the spec describes, the
`[storage]` keys, and the refusal to run against a config still using the old
section names.

Resolution is **eager and total**. `Load` returns either a fully-resolved
config or an error; there is no partially-resolved one, because `appspec/03`
says that state cannot exist. The storage root is resolved as part of the
load, which is why a machine with no Dropbox install fails at `list` rather
than at the first file operation.

`internal/storage/` holds the four engines of `appspec/04` behind one
`Resolver` interface — `dropbox`, `google_drive`, `icloud`, `file_system` —
with `Engine` a closed enum that can only be constructed by naming it, so an
unknown `engine =` value is refused where the user wrote it rather than
defaulting silently. `file_system` is the odd one out on purpose: it does
**not** check that its path exists. `appspec/04` clause 2 says so, and tells a
reimplementer not to "fix" it — the uniform existence guarantee belongs to the
environment gate, and moving the message earlier would put it at the wrong
stage. There is a test whose entire job is to fail if someone adds the stat.

`internal/sqlite/` is a dependency-free, read-only reader for the one query
`appspec/04` names for the `google_drive` engine. It is a reader, not a
database: no SQL, no writing, no locking. It walks one table's b-tree —
interior pages and the right-most child included — follows overflow chains,
and decodes the record format. Its fixtures under `testdata/` were produced by
real `sqlite3`, not by this package, because a fixture it generated itself
would only prove it agrees with itself. It does **not** read a `-wal`
sidecar, which is a named non-support in the package doc: an un-checkpointed
Google Drive database reads as its last checkpointed state.

`internal/fault/` carries the split `appspec/01` §6 and `appspec/02` draw
between *guarded* failures — the conditions the spec gives a sentence for —
and *unguarded* ones, the "uncaught config error" rows. Both spec sections
**permit** collapsing the unguarded rows into clean exits; this program
declines, because a distinction nothing can observe is not one. Guarded prints
`Error: <the spec's sentence>` (or the bare multi-line block for the provider
and legacy-config rows); unguarded prints `mackup: <text naming the offending
value>`, which is what `appspec/02` asks of that regime. Both exit 1 —
deliberately, since the reference exits 1 on an uncaught exception too, and a
second exit code would be a contract this program invented. The reasoning is
written out in `internal/fault/fault.go`; argue with it there.

### The application database

`internal/appdb/` is the third stage: `appspec/05`'s map from application key
to display name and home-relative file set. It is assembled before dispatch,
so a single bad definition aborts `list` and `show` exactly as it aborts a
sync command. Nothing prints it yet — `dispatch` receives it, and the three
enumeration lookups `appspec/05` names (all keys, display name, file set) wait
on `list`.

Precedence is decided **by filename, before any file is read**. The three
directories — `~/.mackup/`, `$XDG_CONFIG_HOME/mackup/applications/`, and the
built-in set — arrive as three `fs.FS` values, are scanned for their `*.cfg`
names, and only the winner for each key is opened. So a user's `vim.cfg` does
not merely override the shipped one, it keeps it from ever being parsed: a
shadowed definition that would abort the program on its own is silent, and the
conformance suite asserts both halves of that. The built-in directory is not
privileged — `internal/catalog` hands over a filesystem rather than a parsed
map, so one implementation of the rule covers all three.

Two rejections are contract rather than input hygiene, because `appspec/05`
says the sync engine of `appspec/06` relies on them "without re-checking": an
absolute path in either path section, and an `$XDG_CONFIG_HOME` outside the
home directory. The XDG base is checked **up front**, not when the first
`[xdg_configuration_files]` entry needs relativizing — `appspec/05` says it
blocks every command, and a lazy check only blocks the commands whose winning
definitions happen to carry that section. A unit case and a conformance case
both pin the order, by racing it against the absolute-path refusal.

There is a third condition this package deliberately does **not** make fatal.
A `..` element in a definition path escapes the home directory just as a
leading `/` does, so the home-relativity guarantee is weaker than its wording
— but `appspec/05` enumerates its rejections and `appspec/00` makes the
reference the contract, so a third one would abort a run the reference
completes. The exposure is recorded as a ticket rather than closed by
inventing contract; the reasoning is in the package doc, and that is where to
argue with it.

`internal/ini/` and `internal/homepath/` came out of `internal/config/` when
this stage landed, and each holds exactly one rule that two stages share.
`ini` is the dialect both file kinds use, with the case of keys as its only
parameter — `LowercaseKeys` for the config's application lists, `ExactKeys`
for a definition. That is the case-policy pair `appspec/03` and `appspec/05`
state as one paired rule, expressed as one argument rather than as two parsers
asked to keep agreeing about comments and whitespace forever. `homepath` holds
`~`-expansion, resolution against home, the `$XDG_CONFIG_HOME` default, and
the containment question — which `appspec/03` asks of the config file and
`appspec/05` asks of the XDG base. A copy in each package would let the two
answer differently for the same path, and the program would then read its
config from one directory while relativizing definitions against another.

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

The package imports nothing from `cmd/` or `internal/` — it shells out to
`go build` — so nothing about the implementation reaches Go's test cache key on
its own, and a cached `ok` will happily outlive a program that has since been
broken. Three separate mechanisms close that, because no one of them closes all
of it, and **all three must stay**:

- The suite **reads every implementation source file** while a case is running,
  which is what puts the program into the cache key. cmd/go records the files a
  test binary opens; a changed implementation then invalidates the cached
  result on its own, under any invocation that runs a case.
- The **`conformance` build tag** keeps the package out of untagged builds, so
  a plain `go test ./...` — an IDE, `gopls`, another CI job — reports nothing
  for it rather than something stale. The tag does *not* make tagged runs
  honest: `go test -tags conformance ./...` caches like any other package, and
  that is the invocation `gopls` and GoLand use.
- **`-count=1`**, which `make conformance` passes, needs neither of the above
  to be right — but only covers what goes through the Makefile.

The header of `test/conformance/harness_test.go` carries the same division next
to the code that implements it.

Every case must be able to fail for the reason it claims. Two rules follow:
assert what the program *did*, never merely that it did not print a usage
error; and never pin help, usage, or warning wording, which `appspec/02`
declares human-facing and not contract.

The suite holds implementation-owned wording in exactly two places, both
deliberate and both temporary. `usageMarker` is the one token of the usage
block any case matches on. `ExpectNotImplemented` matches the dispatch stub's
`Error: <cmd> is not implemented yet.`, so that "argv accepts this form" can be
asserted positively rather than as the absence of a usage error; each use is
replaced by an assertion on the command's real behavior as that command's
ticket lands. Reword either and the suite fails — which is the point, but know
where to look.

`--version` reports the package's own version when one was stamped in, and the
fallback token `unknown` otherwise, per the spec's provenance rule.

## Layout

| Path | What it is |
|------|------------|
| `appspec/` | The functional specification that drives the build (source of truth) |
| `cmd/mackup/` | The command entry point |
| `internal/cli/` | argv grammar, options, usage errors (`appspec/02`) |
| `internal/app/` | The startup pipeline and subcommand dispatch (`appspec/01` §4) |
| `internal/catalog/` | The built-in application definitions that ship with the program (`appspec/05`) |
| `internal/config/` | Config discovery, storage-location resolution, the application lists (`appspec/03`) |
| `internal/storage/` | The four storage engines behind one resolver (`appspec/04`) |
| `internal/sqlite/` | Read-only reader for the one `google_drive` query (`appspec/04`) |
| `internal/appdb/` | The application database: definition format, discovery, precedence (`appspec/05`) |
| `internal/ini/` | The INI dialect both file kinds share (`appspec/03`, `appspec/05`) |
| `internal/homepath/` | Home-relative path rules shared by config and definitions (`appspec/03`, `appspec/05`) |
| `internal/fault/` | The guarded / unguarded failure regimes (`appspec/01` §6, `appspec/02`) |
| `internal/ui/` | The two output streams (`appspec/07`) |
| `internal/version/` | Version-string resolution (`appspec/00` provenance) |
| `test/conformance/` | Black-box suite: runs the built command under a throwaway `HOME` |
| `LICENSE`  | MIT |
