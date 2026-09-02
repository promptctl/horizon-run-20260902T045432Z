#!/usr/bin/env python3
"""Mutation battery for the conformance rig.

A rig that shells out to a built binary can report green over a program it no
longer covers, and it has done so on this branch more than once. Injection is
the only thing that proves it can fail: each mutation below is a defect the
suite is supposed to catch, and every one of them MUST fail `make check`. A
mutation that survives is a hole in the rig, not a curiosity.

Run it from anywhere:  python3 test/mutation/battery.py [mutation name ...]

It is deliberately NOT wired into `make check` or CI. It rewrites source files
in place and restores them afterwards, which is not something a gate should do
to a working tree. Run it by hand whenever the rig changes.

Three rules it enforces, each of which it once got wrong:

  * A mutation must COMPILE before its failure counts. `go vet` runs first; a
    mutation that merely breaks the build also fails the gate and proves
    nothing about the rig.

  * A mutation to the program itself is put to `make conformance` separately.
    `make check` runs the unit packages first and stops there on failure, so a
    mutation the unit tests kill never reaches the conformance suite and says
    nothing about whether the RIG can see it. Reported as RIG-BLIND.

  * A mutation may assert the diagnostic it expects, not just a non-zero exit,
    so that a kill by some unrelated case does not count as coverage.

The tree must be committed before running this. It refuses to start on a dirty
one, and restores in a finally, so an interrupt or an exception puts the
sources back rather than leaving a mutation in the tree.
"""

import atexit, os, shutil, signal, subprocess, sys, tempfile

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
BACKUP = tempfile.mkdtemp(prefix="macklebox-mutation-backup-")
# Registered here rather than at the end of the happy path: the dirty-tree
# refusal below exits before the run starts, and an exception can leave through
# the middle, and both used to leak a directory holding copies of every tracked
# source file. Ten aborted runs left ten of them.
atexit.register(lambda: shutil.rmtree(BACKUP, ignore_errors=True))
FILES = ["test/conformance/harness_test.go", "test/conformance/argv_test.go",
         "test/conformance/harness_unix_test.go",
         "internal/cli/usage.go", "internal/app/dispatch.go", "internal/app/app.go",
         "internal/cli/parse.go", "Makefile",
         "internal/version/version.go",
         "internal/ui/color.go",
         "internal/config/config.go", "internal/storage/storage.go",
         "internal/fault/fault.go"]

H = "test/conformance/harness_test.go"
C = "internal/ui/color.go"
U = "internal/cli/usage.go"
D = "internal/app/dispatch.go"
A = "internal/app/app.go"
# No constant for test/conformance/harness_unix_test.go, and no mutation edits
# it directly. That file is a safety net over Snapshot -- a bounded call, a recording
# reporter, a timeout arm -- and a net is only observable when the thing it
# catches misbehaves, so it is exercised by injecting into Snapshot rather than
# into itself. "the FIFO guard is removed" drives its bound and timeout arm end
# to end; "Snapshot complains instead of failing" drives its recorder branch.
# Both of those had never executed in this tree before those entries existed.
# It stays in FILES anyway, so an entry that does edit it later is backed up.
P = "internal/cli/parse.go"
V = "internal/version/version.go"
G = "test/conformance/argv_test.go"
CF = "internal/config/config.go"
ST = "internal/storage/storage.go"
FA = "internal/fault/fault.go"

# SURVIVES marks an entry that must NOT break the gate. Most entries are
# defects the suite has to catch; these are the opposite -- correct code that a
# guard once rejected. The doc guard demanded a bare name and so failed on
# "// A World is one throwaway environment", which is idiomatic Go and how the
# standard library writes doc comments. A guard that reddens the gate on
# correct code is a defect too, and nothing expressed it until this existed.
SURVIVES = object()


def repl(f, old, new):
    return ("repl", f, old, new)

def tail(f, marker, new):
    """Replace from `marker` to end of file with `new`.

    Only means what it says while the marker's declaration is the LAST one in
    the file, so apply() checks that rather than trusting it. It was true when
    the one entry using this was written and nothing said so; adding a function
    after ExpectUnchanged would have made that entry delete it too and then
    report either a phantom DOES-NOT-COMPILE or a kill credited to the wrong
    mutation.
    """
    return ("tail", f, marker, new)

def cut(f, start, end):
    """Remove everything from `start` up to, but not including, `end`.

    For deleting a whole switch arm or block. Replacing the BODY of an arm and
    calling it deleted is what left the FIFO branch standing while its entry
    reported a kill, so the shape that removes the arm itself has its own kind
    rather than a hand-written repl each time.

    Both markers are matched at the START OF A LINE, which is what makes the
    end anchor safe rather than lucky. A plain substring search for the end
    "\t\tdefault:" also matches inside a NESTED "\t\t\tdefault:", so an arm
    that later grew an inner switch would have been cut short -- leaving a
    mutation that still compiles but is not the one the entry names, and a kill
    credited to the wrong defect. That is the same failure the tail guard was
    added for one commit earlier, and it was left in place here.

    Line anchoring closes it because gofmt guarantees the indentation: a nested
    construct carries strictly more tabs, so a marker written at one
    indentation cannot match at a deeper one. "\t\tdefault:" already occurs
    twice in harness_test.go, so requiring a globally unique end -- the rule
    repl uses -- would reject the one entry that is correct today.
    """
    return ("cut", f, start, end)

MUTATIONS = [
 ("ExpectUnchanged gutted", [tail(H, "func (w *World) ExpectUnchanged(before Snapshot) {",
   "func (w *World) ExpectUnchanged(before Snapshot) {\n\tw.t.Helper()\n\t_ = before\n}\n")]),

 ("Snapshot content dropped", [repl(H,
   'snapshot[relative] = fmt.Sprintf("file %04o @%d %q", info.Mode().Perm(), stamp, content)',
   '_ = content\n\t\t\tsnapshot[relative] = fmt.Sprintf("file %04o @%d", info.Mode().Perm(), stamp)')]),

 ("Snapshot mtime dropped", [repl(H,
   "stamp := info.ModTime().UnixNano()", "stamp := int64(0)")]),

 ("ExpectUnchanged created branch deleted", [repl(H,
   '\tfor path, got := range after {\n\t\tif _, ok := before[path]; !ok {\n\t\t\tw.t.Errorf("%s was created; it holds %s", path, got)\n\t\t}\n\t}\n', '')]),

 ("ExpectUnchanged removed branch deleted", [repl(H,
   '\t\tgot, ok := after[path]\n\t\tif !ok {\n\t\t\tw.t.Errorf("%s was removed; it held %s", path, want)\n\t\t\tcontinue\n\t\t}\n',
   '\t\tgot, ok := after[path]\n\t\tif !ok {\n\t\t\tcontinue\n\t\t}\n')]),

 ("ExpectUnchanged changed branch deleted", [repl(H,
   '\t\tif got != want {\n\t\t\tw.t.Errorf("%s changed:\\n  before: %s\\n   after: %s", path, want, got)\n\t\t}\n',
   '\t\t_ = got\n')]),

 ("reaper Lstat -> Stat", [repl(H, "info, err := os.Lstat(path)", "info, err := os.Stat(path)")]),

 ("reaper no-op", [repl(H, "\t\tos.RemoveAll(path)\n", "\t\t_ = path\n")]),

 ("reaper ReadDir -> Glob", [repl(H,
   '\tentries, err := os.ReadDir(within)\n\tif err != nil {\n\t\treturn\n\t}\n\tfor _, entry := range entries {\n\t\tif !strings.HasPrefix(entry.Name(), buildDirPrefix) {\n\t\t\tcontinue\n\t\t}\n\t\tpath := filepath.Join(within, entry.Name())\n',
   '\tmatches, err := filepath.Glob(filepath.Join(within, buildDirPrefix+"*"))\n\tif err != nil {\n\t\treturn\n\t}\n\tfor _, path := range matches {\n')]),

 ("cmd.Env assignment deleted", [repl(H, "\tcmd.Env = w.environ()\n", "\t_ = w.environ()\n")]),

 ("Result copies a stale reporter", [
   repl(H, "\tw    *World\n\tArgs []string\n", "\tw    *World\n\tt    reporter\n\tArgs []string\n"),
   repl(H, "\treturn Result{\n\t\tw:    w,\n\t\tArgs: args,\n", "\treturn Result{\n\t\tw:    w,\n\t\tt:    w.t,\n\t\tArgs: args,\n"),
   repl(H, 'func (r Result) ExpectExit(code int) Result {\n\tr.w.t.Helper()\n\tif r.ExitCode != code {\n\t\tr.w.t.Errorf(',
           'func (r Result) ExpectExit(code int) Result {\n\tr.t.Helper()\n\tif r.ExitCode != code {\n\t\tr.t.Errorf('),
 ]),

 ("TMPDIR dropped from world env", [repl(H, '\t\t\t"TMPDIR": tmp,\n', '')]),

 ("doc reattached to a function", [repl(H,
   "func moduleRoot() (string, error) {", "func inserted() {}\n\nfunc moduleRoot() (string, error) {")],
   'the doc comment on inserted opens with "moduleRoot"'),

 ("doc reattached to a var block", [repl(H,
   "var readSourcesOnce sync.Once",
   "var (\n\tunrelatedA = 1\n\tunrelatedB = 2\n)\n\nvar readSourcesOnce sync.Once")],
   'opens with "readSourcesOnce", which is the name of the var declared elsewhere'),

 ("usage banner reworded", [repl(U, "\nUsage:\n", "\nUsage!\n")]),

 ("dispatch stub reworded", [repl(D, "is not implemented yet.", "is not implemented.")]),

 ("usage error exits 0", [repl(A,
   "\t\t\tstreams.Errln(cli.Usage)\n\t\t\treturn ExitFailure\n",
   "\t\t\tstreams.Errln(cli.Usage)\n\t\t\treturn ExitOK\n")]),

 ("method doc orphaned onto a var block", [repl(H,
   "// UseBinary switches this world to another of the builds under test.\nfunc (w *World) UseBinary(path string) { w.bin = path }",
   "// UseBinary switches this world to another of the builds under test.\nvar (\n\torphanedMethodDocA = 1\n\torphanedMethodDocB = 2\n)\n\nfunc (w *World) UseBinary(path string) { w.bin = path }")],
   'opens with "UseBinary", which is the name of the method declared elsewhere'),

 ("type doc orphaned onto a var block", [repl(H,
   "// the battery's \"Snapshot mtime dropped\" entry kills a record without it.\ntype Snapshot map[string]string",
   "// the battery's \"Snapshot mtime dropped\" entry kills a record without it.\nvar (\n\torphanedTypeDocA = 1\n\torphanedTypeDocB = 2\n)\n\ntype Snapshot map[string]string")],
   'opens with "Snapshot", which is the name of the type declared elsewhere'),

 ("Snapshot failure surfaces as a failure, not a timeout", [repl(H,
   '\t\t\tsnapshot[relative] = fmt.Sprintf("%s %04o @%d", info.Mode().Type(), info.Mode().Perm(), stamp)',
   '\t\t\treturn fmt.Errorf("synthetic harness failure on %s", relative)')],
   'the snapshot failed: snapshotting the scratch root: synthetic harness failure'),

 ("config-file stops taking an argument", [repl(P,
   '\t"config-file": true,', '\t"config-file": false,')],
   None),

 ("--version with extra argv prints usage", [repl(A,
   "\tcase inv.Opts.Version:\n\t\tstreams.Say(ui.Progress, version.Banner())\n\t\treturn ExitOK\n",
   "\tcase inv.Opts.Version:\n\t\tif len(argv) > 1 {\n\t\t\tstreams.Outln(cli.Usage)\n\t\t\treturn ExitOK\n\t\t}\n\t\tstreams.Say(ui.Progress, version.Banner())\n\t\treturn ExitOK\n")],
   None),

 ("the argv scan does not stop at --help", [repl(A,
   "\tcase inv.Opts.Help:\n\t\tstreams.Outln(cli.Usage)\n\t\treturn ExitOK\n",
   "\tcase inv.Opts.Help:\n\t\tif len(argv) > 1 {\n\t\t\tstreams.Sayf(ui.Fatal, \"mackup: unrecognized option: %s\", argv[len(argv)-1])\n\t\t}\n\t\tstreams.Outln(cli.Usage)\n\t\treturn ExitOK\n")],
   None),

 ("declaration slid under a spec-level doc comment", [repl(H,
   "\t// mackupVCSBuildErr says why mackupVCSBin is empty, when it is.\n\tmackupVCSBuildErr error",
   "\t// mackupVCSBuildErr says why mackupVCSBin is empty, when it is.\n\tinsertedUnderTheComment error\n\n\tmackupVCSBuildErr error")],
   'the doc comment on insertedUnderTheComment opens with "mackupVCSBuildErr"'),

 # `ok && bi != nil`, and the nil check is the whole point of the entry rather
 # than defensive noise. Without it this mutation dereferenced a nil
 # *debug.BuildInfo in TestStringFallsBackWhenTheBuildCarriesNoBuildInfo --
 # which feeds readBuildInfo exactly (nil, true) -- and PANICKED the
 # internal/version test binary. A panic aborts the run, so
 # TestAnExplicitStampSurvivesAWorkingTreeBuild, the case written for this
 # regression and declared after it in the file, never executed. The entry
 # reported "killed" on a crash in an unrelated case: gutting the case this is
 # supposed to prove still left it green. That is rule 3 of the docstring
 # above, broken by the entry meant to exercise it, which is why the expected
 # diagnostic is no longer None.
 ("working-tree provenance outranks the linker stamp", [repl(V,
   "\tif value != \"\" {\n\t\treturn normalize(value)\n\t}\n",
   "\tif bi, ok := readBuildInfo(); ok && bi != nil {\n\t\tfor _, setting := range bi.Settings {\n\t\t\tif setting.Key == \"vcs.revision\" {\n\t\t\t\treturn Fallback\n\t\t\t}\n\t\t}\n\t}\n\tif value != \"\" {\n\t\treturn normalize(value)\n\t}\n")],
   'want the stamped "0.11.1" even though the build came from a working tree'),

 ("Snapshot mode dropped", [repl(H,
   'snapshot[relative] = fmt.Sprintf("file %04o @%d %q", info.Mode().Perm(), stamp, content)',
   'snapshot[relative] = fmt.Sprintf("file @%d %q", stamp, content)')],
   None),

 ("touchBuildDir no-op", [repl(H,
   "func touchBuildDir(dir string) {\n\tnow := time.Now()\n\tos.Chtimes(dir, now, now)\n}",
   "func touchBuildDir(dir string) {\n\t_ = dir\n}")],
   None),

 ("idiomatic article before the name is accepted", [repl(H,
   "// World is one throwaway environment: a home directory, an environment",
   "// A World is one throwaway environment: a home directory, an environment")],
   SURVIVES),

 ("a one-name parenthesized block may carry a collective comment", [repl(H,
   "// buildDirPrefix names this suite's build directories, so that a later run can\n// recognize one an earlier run abandoned.\nconst buildDirPrefix = \"macklebox-conformance-bin-\"",
   "// Naming of this suite's build directories, so that a later run can\n// recognize one an earlier run abandoned.\nconst (\n\tbuildDirPrefix = \"macklebox-conformance-bin-\"\n)")],
   SURVIVES),

 # The doc guard holds a test entry point to a weaker rule than everything else
 # -- it may open with any word the file does not declare -- because demanding
 # the function's own name reddened the gate on idiomatic Go. These two pin
 # both directions of that, because the obvious way to fix the false positive
 # (exempt test functions, or skip _test.go) would have made the first one
 # invisible, and all three defects the guard was written for were in a
 # _test.go file.
 # Both injected functions call NewWorld, and the empty bodies they had before
 # are why: TestEveryCaseThatBuildsNoWorldIsAccountedFor holds every case in
 # this package to building a world or carrying an allowlist entry, so an
 # injected `func TestOrdinaryComment(t *testing.T) {}` is not the "correct
 # code" this SURVIVES entry claims it is -- it reddened the gate, and the
 # entry reported FALSE-POSITIVE the first time both existed together. The
 # stranded-comment entry below it had the opposite problem, still killed but
 # now by two guards at once, which is the attribution rule 3 exists for. A
 # body that builds a world makes both injections ordinary cases again and
 # leaves each entry testing the one guard it names.
 ("doc stranded on an inserted test function", [repl(H,
   "func moduleRoot() (string, error) {",
   "func TestInserted(t *testing.T) { NewWorld(t) }\n\nfunc moduleRoot() (string, error) {")],
   'the doc comment on the test TestInserted opens with "moduleRoot"'),

 ("a test function may carry an ordinary explanatory comment", [repl(H,
   "// moduleRoot is the directory holding go.mod, found by walking up from the",
   "// Regression for the round-9 argv scan bug.\nfunc TestOrdinaryComment(t *testing.T) { NewWorld(t) }\n\n// moduleRoot is the directory holding go.mod, found by walking up from the")],
   SURVIVES),

 # The other half of the same record: recorded, but recorded RAW. The target
 # sits at the end of the record, which is where the blindness scan matches
 # contentsUnreadable with HasSuffix, so an unquoted target ending in that
 # marker turns every ExpectUnchanged in the world into a spurious "make the
 # fixture readable" over a file that already is.
 # --- appspec/07 stream routing and colour (macklebox-foundation-waw.3) ------
 #
 # Every entry here is killable by the CONFORMANCE suite, not only by the unit
 # tests, which is the bar an internal/ mutation has to clear: `make check` runs
 # the unit packages first and stops there, so a mutation the unit tests catch
 # says nothing about whether the rig can see it, and the battery reports
 # RIG-BLIND for one that cannot.
 #
 # One appspec/07 property is deliberately NOT here: reset-safety, the
 # re-application of a colour after an embedded reset. No message the program
 # emits today contains an embedded reset -- the composed shapes arrive with
 # the diff detail and the per-app verbose header in later tickets -- so the
 # rig genuinely cannot observe it yet, and an entry for it would report
 # RIG-BLIND accurately. internal/ui's unit tests carry it until a composed
 # message exists; add the entry with that message.

 # Each `expect` here names the UNIT diagnostic, not the conformance one, and
 # that is not a compromise -- it is what the string is matched against. The
 # battery matches expect against `make check`, and `make check` runs the unit
 # packages FIRST and stops at the first failure, so a mutation to internal/
 # that the unit tests kill never produces a line of conformance output for an
 # expect to match. Every one of these six was first written with the wording
 # of the conformance case it was really aimed at and every one of them
 # reported WRONG-DIAGNOSTIC on a mutation the gate had killed correctly. The
 # rig half of the claim is not lost by writing it this way: it is carried by
 # the separate `make conformance` run below, which is what turns a
 # unit-tests-only kill into RIG-BLIND, and the [rig: ...] note on a kill line
 # names the conformance case that saw it.

 ("colour is not emitted at all", [repl(C,
   '\topen := escape + spec.sgr + "m"\n\treturn open + embeddedReset.ReplaceAllString(text, reset+open) + reset',
   '\t_ = spec\n\treturn text')],
   'with no colour to a non-terminal stream'),

 ("a coloured string is not terminated with a reset", [repl(C,
   '\treturn open + embeddedReset.ReplaceAllString(text, reset+open) + reset',
   '\treturn open + embeddedReset.ReplaceAllString(text, reset+open)')],
   'want the newline after the reset'),

 # appspec/07 gives fatal errors bright red and non-fatal copy failures red,
 # and colour alone conveys the level -- so flattening the two erases the only
 # thing that says whether the program stopped.
 ("a fatal error takes the non-fatal colour", [repl(C,
   'Fatal:       {sgr: "91"', 'Fatal:       {sgr: "31"')],
   'appspec/07 distinguishes bright red 91 from red 31'),

 # The stream is contract, not cosmetics. This is that defect in its purest
 # form: the message is right, the colour is right, the stream is wrong.
 ("a fatal diagnostic is routed to stdout", [repl(C,
   'Fatal:       {sgr: "91", on: toStderr', 'Fatal:       {sgr: "91", on: toStdout')],
   'Fatal stream = 0, want 1'),

 ("the version banner loses its colour", [repl(A,
   'streams.Say(ui.Progress, version.Banner())', 'streams.Outln(version.Banner())')],
   'want the banner coloured even though the stream is not a terminal'),

 # The opposite mistake to the one above, and the reason the decision is
 # written down in the program and in the case: appspec/07 lists no level for
 # the argument parser's usage text, so colouring it invents one.
 ("the usage block is coloured", [repl(A,
   '\tcase inv.Opts.Help:\n\t\tstreams.Outln(cli.Usage)',
   '\tcase inv.Opts.Help:\n\t\tstreams.Say(ui.Progress, cli.Usage)')],
   'carries colour; the usage block has no level in appspec/07'),

 # appspec/07: the program does not condition colour on whether stdout is a
 # TTY. The rig runs every process down a pipe, so a program that consulted a
 # terminal would emit nothing here.
 ("colour is conditioned on a terminal", [repl(A,
   'func runArgv(argv []string, streams *ui.IO) int {',
   'func stdoutIsTerminal() bool {\n\tinfo, err := os.Stdout.Stat()\n\treturn err == nil && info.Mode()&os.ModeCharDevice != 0\n}\n\nfunc runArgv(argv []string, streams *ui.IO) int {'),
   repl(A, 'import (\n\t"errors"', 'import (\n\t"errors"\n\t"os"')],
   'names "ModeCharDevice"'),

 ("the symlink target is recorded unquoted", [repl(H,
   'snapshot[relative] = fmt.Sprintf("symlink %04o @%d -> %q", info.Mode().Perm(), stamp, target)',
   'snapshot[relative] = fmt.Sprintf("symlink %04o @%d -> %s", info.Mode().Perm(), stamp, target)')],
   'nothing here is blind'),

 ("the symlink target is not recorded", [repl(H,
   '\t\t\ttarget, err := os.Readlink(path)\n\t\t\tif err != nil {\n\t\t\t\treturn err\n\t\t\t}\n',
   '\t\t\ttarget := "constant"\n\t\t\t_ = os.Readlink\n')],
   # The tail of that Errorf rather than its %q-rendered want, which now
   # carries Go's own escaping of the quoted target and would have to be
   # re-encoded here every time the record's quoting changes. This clause is
   # unique to the one assertion the mutation has to trip.
   'without the target a re-pointed link is indistinguishable from an untouched one'),

 # Removes the arm, not its body. An earlier entry replaced the body and so
 # left the branch itself standing, which is why the 30s bound, the recording
 # reporter and the timeout arm in harness_unix_test.go had never once
 # executed. This is the defect that whole file exists for: Snapshot opens a
 # FIFO nobody is writing to and BLOCKS, and the kill is the bound firing.
 # Slow on purpose -- it takes the full 30s -- and the only entry that does.
 ("the FIFO guard is removed", [cut(H,
   "\t\tcase !info.Mode().IsRegular():", "\t\tdefault:")],
   'so it blocked rather than failed'),

 ("Snapshot complains instead of failing", [repl(H,
   '\t\t\tsnapshot[relative] = fmt.Sprintf("%s %04o @%d", info.Mode().Type(), info.Mode().Perm(), stamp)',
   '\t\t\tw.t.Errorf("snapshot: unexpected file type at %s", relative)\n\t\t\tsnapshot[relative] = fmt.Sprintf("%s %04o @%d", info.Mode().Type(), info.Mode().Perm(), stamp)')],
   'the snapshot returned but reported'),

 # readImplementationSources is the rig's primary cache-honesty mechanism and
 # the one harness_test.go's header says must never be lost, and nothing
 # injected at it. Both of its recorded past failures are injectable: the walk
 # narrowed back to a guess at where the program lives, and the call moved out
 # of a case to where cmd/go does not record its reads.
 ("the source walk is narrowed to internal", [repl(H,
   "\tread := map[string]bool{}",
   '\troot = filepath.Join(root, "internal")\n\tread := map[string]bool{}')],
   "so the cache key does not track the program"),

 # The suite compiles on the host on every run, and the host is unix, so a case
 # in the untagged half reaching into the unix-tagged half was invisible to the
 # whole gate until a contributor on another GOOS met it. That is what the
 # second GOOS in the vet target is for, and this is what proves it fires. The
 # host vet this battery runs first passes, exactly as the gate's first vet
 # did; the kill comes from the cross-GOOS one.
 ("an untagged case reaches into the unix-only harness", [repl(G,
   '\tlink := world.Path("link")',
   '\t_ = snapshotBound\n\tlink := world.Path("link")')],
   "undefined: snapshotBound"),

 ("the forced-stamp GOFLAGS drops what go env carries", [repl(H,
   '\treturn strings.TrimSpace(strings.TrimSpace(string(reported)) + " -buildvcs=true"), nil',
   '\t_ = reported\n\treturn "-buildvcs=true", nil')],
   "dropped -mod=mod from `go env GOFLAGS`"),

 # Two mechanisms, one contract, and until these existed only one of them was
 # falsifiable: touchBuildDir had a case, the loop and the wiring had none.
 ("the build directory refresher is never started", [repl(H,
   "\tkeepBuildDirFresh(dir)\n", "\t_ = keepBuildDirFresh\n")],
   "want the directory holding its binaries"),

 ("the build directory refresher touches once and stops", [repl(H,
   "\tfor {\n\t\tselect {\n\t\tcase <-stop:\n\t\t\treturn\n\t\tcase <-time.After(interval):\n\t\t\ttouchBuildDir(dir)\n\t\t}\n\t}\n",
   "\tselect {\n\tcase <-stop:\n\tcase <-time.After(interval):\n\t\ttouchBuildDir(dir)\n\t}\n")],
   "it is not looping"),

 ("the version banner gains a suffix", [repl(V,
   'return "Mackup " + String()', 'return "Mackup " + String() + "-extra"')],
   None),

 # The two halves of runningUnderCI, separately. Before it existed this was two
 # inline switches over the raw string, so CI=False and CI=no hard-failed the
 # gate the "false" case had been added to keep green -- a defect that lived in
 # code nothing drove, since neither switch is reachable on a machine that has
 # a VCS-stamped build. Narrowing either half must now be caught.
 ("runningUnderCI stops normalizing its value", [repl(H,
   "strings.ToLower(strings.TrimSpace(value))", "value")],
   "was read as a CI run"),

 ("runningUnderCI narrows its falsy set", [repl(H,
   '\tcase "", "false", "0", "no", "off":', '\tcase "", "false", "0":')],
   "was read as a CI run"),

 # casesThatBuildNoWorld replaced a comment that named which cases skip
 # readImplementationSources, and went stale twice doing it. Both directions of
 # the guard that replaced it are pinned, because a list that only rejects
 # additions rots the same way the comment did.
 # The key is renamed rather than the line deleted, deliberately: gofmt pads
 # the map's keys to align its values, so an anchor carrying that padding
 # breaks -- loudly, but for a reason having nothing to do with what the entry
 # tests -- the next time an entry is added. A key substring carries no
 # padding. Renaming trips both directions at once; the expected diagnostic
 # names the one this entry is for.
 ("the no-world allowlist loses an entry", [repl(G,
   '"TestTheReaperJudgesTheEntryItRemoves":',
   '"TestTheReaperJudgesTheEntryItRemovedOnce":')],
   "never calls NewWorld"),

 ("the no-world scan sees a world in every case", [repl(G,
   'identifier.Name == "NewWorld"', 'identifier.Name != ""')],
   "no longer exists or now calls NewWorld"),

 # Both of these are killed only by the SECOND conformance run, the -trimpath
 # one. Under the plain run they pass, which is exactly how the guard's own
 # runtime.Caller shipped: moduleRoot has refused runtime.Caller by name since
 # before either entry existed, and nothing checked. The pair covers both
 # sites, because the claim is about the suite finding its files, not about one
 # function.
 # Two edits each: the import as well as the body, because neither file
 # imports runtime any more and a mutation that does not compile proves
 # nothing (rule 1).
 ("the no-world guard finds its directory by runtime.Caller", [
   repl(G, '\t"path/filepath"\n', '\t"path/filepath"\n\t"runtime"\n'),
   repl(G, '\tdir, err := os.Getwd()\n\tif err != nil {\n\t\tt.Fatalf("locating the package directory: %v", err)\n\t}',
      '\t_, thisFile, _, _ := runtime.Caller(0)\n\tdir := filepath.Dir(thisFile)')],
   "github.com/promptctl/macklebox/test/conformance"),

 # Snapshot's permission branch had no case at all, so ExpectUnchanged
 # compared two records of an unlistable directory that were equal because
 # both were blind. Dropping the report leaves the collection loop standing
 # and says nothing, which is what the defect actually looked like.
 #
 # Written first as Errorf -> Logf, which is the obvious spelling and does
 # not compile: w.t is the reporter interface captureReport swaps in, and it
 # carries Helper, Errorf and Fatalf but no Logf. Rule 1 caught it as
 # DOES-NOT-COMPILE, which is what rule 1 is for.
 ("the blind-directory report is dropped", [repl(H,
   'w.t.Errorf("%s", message)', '_ = message')],
   "no report names home/locked"),

 # Snapshot's third permission shape: a 0400 parent, whose children ReadDir
 # lists and lstat cannot touch. This branch did not exist -- the walk
 # returned the error, WalkDir aborted, and the harness fataled -- so the
 # mutation is the code as it was, and the kill is the case that shape now
 # has. The two entries are separate because they fail differently: dropping
 # the branch fatals the harness, dropping the report leaves it silent.
 ("Snapshot aborts on an entry it cannot stat", [repl(H,
   '\t\t\tif errors.Is(err, fs.ErrPermission) {\n\t\t\t\tsnapshot[relative] = entryUnstatable\n\t\t\t\treturn nil\n\t\t\t}\n\t\t\treturn err',
   '\t\t\treturn err')],
   "snapshotting the scratch root"),

 # Anchored on the predicate rather than the whole row: the row carries a
 # long message and a closure, both of which have already been rewritten once
 # (BROKEN-ANCHOR, the next full run after they were), while the predicate is
 # the part that decides anything. `false` disables this shape alone and
 # leaves the other one reporting, which is what separates this entry from
 # "the blindness scan matches anywhere in the record".
 ("the unstatable-entry blindness goes unreported", [repl(H,
   'strings.HasPrefix(record, entryUnstatable)', 'false')],
   "no report names home/sealed/cfg"),

 # The unreadable-file record's size, and the anchoring of the blindness
 # scan. Both were added because mode-and-stamp alone, and Contains, are the
 # obvious spellings -- so both are the shape a later edit drifts back to.
 ("the unreadable-file record drops its size", [repl(H,
   'fmt.Sprintf("file %04o %dB @%d <unreadable>", info.Mode().Perm(), info.Size(), stamp)',
   'fmt.Sprintf("file %04o @%d <unreadable>", info.Mode().Perm(), stamp)')],
   "no report names home/secret"),

 ("the blindness scan matches anywhere in the record", [
   repl(H, 'strings.HasPrefix(record, entryUnstatable)', 'strings.Contains(record, entryUnstatable)'),
   repl(H, 'strings.HasSuffix(record, contentsUnreadable)', 'strings.Contains(record, contentsUnreadable)')],
   "over a readable file whose CONTENT holds the marker text"),

 ("moduleRoot walks up from runtime.Caller", [
   repl(H, '\t"path/filepath"\n', '\t"path/filepath"\n\t"runtime"\n'),
   repl(H, '\tdir, err := os.Getwd()\n\tif err != nil {\n\t\treturn "", fmt.Errorf("locating the module root: %v", err)\n\t}',
      '\t_, thisFile, _, _ := runtime.Caller(0)\n\tdir := filepath.Dir(thisFile)')],
   "no go.mod above"),

 # --- appspec/03 config and appspec/04 storage (macklebox-resolvers-5iw.2) ---
 #
 # Entries here, and not a deferral banner like the catalog one below, because
 # config load is the one stage that IS observable today: appspec/02 puts every
 # subcommand except --help and --version behind it, so a config defect aborts
 # `list` at the boundary with a diagnostic the rig reads. Each of the five
 # below was applied in a copied tree and put to `make conformance` separately
 # before it was written down; none reported RIG-BLIND.
 #
 # Each `expect` names the diagnostic `make check` actually prints for that
 # mutation, which for the first three is a UNIT one -- internal/config and
 # internal/storage run before the conformance suite and stop it -- and for the
 # last two is the conformance one, because there the unit packages pass. That
 # asymmetry is not a slip; it is the rule the appspec/07 banner above states,
 # applied per entry rather than assumed. The [rig: ...] note on each kill line
 # is what carries the conformance half of the claim.
 #
 # One property is deliberately NOT here: Config.Scope's "the denylist wins
 # over the allowlist". No non-test caller invokes Scope yet -- `list` is wired
 # to the catalog by macklebox-resolvers-5iw.4 -- so flipping it is killed by
 # internal/config alone and reports RIG-BLIND, accurately and uselessly. This
 # is the same call as the catalog deferral below, for the same reason. Add it
 # with .4, alongside the two catalog entries that banner names.

 # appspec/03 "Home-directory containment". Deleting the check is the shape a
 # reimplementation reaches for when the check looks like a redundant guard on
 # a path the program itself computed -- it is not, because two of the three
 # discovery candidates come from the environment.
 ("the home-containment check is deleted", [repl(CF,
   '\tif !insideHome(path, home) {\n'
   '\t\t// appspec/03 "Home-directory containment": checked at construction\n'
   '\t\t// and applied to a discovered path as much as to an explicitly named\n'
   '\t\t// one, which is why this sits outside configPath\'s two branches\n'
   '\t\t// rather than inside the explicit one.\n'
   '\t\treturn nil, fault.Guardedf("The config file \'%s\' is not in your home directory. Aborting.", path)\n'
   '\t}\n', '')],
   'Load succeeded, want "Error: The config file '),

 # appspec/03 "Discovery and precedence": ~/.mackup.cfg "always wins if
 # present". Prepending rather than appending is a one-word slip that leaves
 # all three candidates in play and every single-candidate case passing, so it
 # is only visible to a case that sets TWO of them at once.
 ("MACKUP_CONFIG is checked before the home config", [repl(CF,
   "\t\tcandidates = append(candidates, absolute(expandTilde(env.MackupConfig, home)))",
   "\t\tcandidates = append([]string{absolute(expandTilde(env.MackupConfig, home))}, candidates...)")],
   'the config read was "from-mackup-config", want the home directory\'s ~/.mackup.cfg'),

 # The appspec/04 trap, and the only entry here for a mutation that ADDS
 # correct-looking code rather than removing it. appspec/04 clause 2 says the
 # file_system engine must NOT check that its path exists, because the uniform
 # "Unable to find the storage folder: <path>" belongs to the environment gate
 # -- so a stat here does not break a postcondition, it moves a message to the
 # wrong stage. Nothing in the tree but the two tests this kills says so.
 ("the file_system engine gains an existence check", [repl(ST,
   '\tif filepath.IsAbs(f.path) {\n'
   '\t\treturn f.path, nil\n'
   '\t}\n'
   '\treturn filepath.Join(f.home, f.path), nil\n'
   '}',
   '\troot := f.path\n'
   '\tif !filepath.IsAbs(root) {\n'
   '\t\troot = filepath.Join(f.home, root)\n'
   '\t}\n'
   '\tif info, err := os.Stat(root); err != nil || !info.IsDir() {\n'
   '\t\treturn "", fault.Guardedf("Unable to find the storage folder: %s", root)\n'
   '\t}\n'
   '\treturn root, nil\n'
   '}')],
   'want the path itself: appspec/04 forbids an existence check in this engine'),

 # appspec/07 requires every coloured string to end in a reset, and two guarded
 # rows -- the provider block and the legacy-config refusal -- are multi-line.
 # One Say per diagnostic colours the first line and leaves the rest bare, and
 # the collapse is invisible to every single-line failure, which is most of
 # them. The import goes with it because compiling is a precondition of the
 # mutation counting: an unused "strings" is a build break, and a build break
 # proves nothing about the rig.
 ("reportFatal writes a multi-line diagnostic in one Say", [
   repl(A, '\tfor _, line := range strings.Split(fault.Diagnostic(err), "\\n") {\n'
           '\t\tstreams.Say(ui.Fatal, line)\n'
           '\t}',
           "\tstreams.Say(ui.Fatal, fault.Diagnostic(err))"),
   repl(A, '\t"errors"\n\t"strings"\n', '\t"errors"\n')],
   'want it to end in a reset; appspec/07: every colored string is terminated with a reset'),

 # The regime split of appspec/01 section 6 and appspec/02, which both PERMIT
 # collapsing the unguarded rows into clean exits. This program declines that
 # permission and keeps the two shapes apart -- "Error: <sentence>" for
 # guarded, "mackup: <text naming the offending value>" for unguarded -- and
 # the reasoning is written out in internal/fault/fault.go. A split nothing can
 # observe is not a split, so this entry is what makes the decision hold:
 # giving the unguarded regime the guarded opener has to redden the gate.
 ("the unguarded regime takes the guarded opener", [repl(FA,
   '\treturn &Error{Regime: Unguarded, Message: "mackup: " + fmt.Sprintf(format, args...)}',
   '\treturn &Error{Regime: Unguarded, Message: "Error: " + fmt.Sprintf(format, args...)}')],
   'want a shape distinct from the guarded rows'),

 # --- appspec/05 the built-in application catalog (macklebox-resolvers-5iw.1) -
 #
 # No entries, deliberately, and this banner is here so the absence is a
 # decision rather than an oversight.
 #
 # macklebox-resolvers-5iw.1 ships internal/catalog: 614 definition files
 # embedded in the binary, and unit tests that pin them against
 # appspec/appendix-application-names.md -- the key set, the file naming, the
 # absolute-path rejection appspec/05 calls load-bearing, and the mackup
 # self-definition whole-Mackup mode needs. Every one of those is real and
 # every one of them was checked by hand, in a copied tree, by making the
 # mutation and reading the diagnostic.
 #
 # None of it belongs here YET, for the reason stated at the top of the
 # appspec/07 banner above: the bar for an internal/ entry is that the
 # CONFORMANCE suite can kill it, and nothing in the catalog reaches the
 # program's boundary until `list` and `show` are wired to it. Until then a
 # mutation to a .cfg file is killed by the unit tests alone and reports
 # RIG-BLIND -- accurately, and uselessly. This is the same call the
 # reset-safety deferral above records, for the same reason.
 #
 # Add the entries with macklebox-resolvers-5iw.4, which is what makes the
 # catalog observable. The obvious two: mackup.cfg losing ".mackup.cfg" (the
 # `show mackup` claim) and a definition file deleted or renamed (the 614-key
 # `list` claim). Both need internal/catalog paths added to FILES.
]





def snapshot_tree():
    for f in FILES:
        dst = os.path.join(BACKUP, f)
        os.makedirs(os.path.dirname(dst), exist_ok=True)
        shutil.copyfile(os.path.join(REPO, f), dst)


def restore():
    for f in FILES:
        shutil.copyfile(os.path.join(BACKUP, f), os.path.join(REPO, f))

# Bounded, because one entry below injects a defect whose whole signature is
# that the suite BLOCKS -- the FIFO guard removed -- and it is killed by the
# harness's own 30s bound firing. If a later change breaks that bound, the
# unbounded form hung the battery itself rather than reporting the entry, and
# an overnight run came back with nothing. The bound here is far above any
# honest gate run; it exists to turn a hang into a result.
RUN_BOUND = 900

def run(cmd):
    # start_new_session puts the shell and everything it spawns in their own
    # process group, so the timeout below can kill the whole tree.
    #
    # subprocess.run's timeout kills only the direct child -- /bin/sh -- and
    # cmd here is a make invocation, so the surviving descendants are `go build`
    # and a running test binary. They kept compiling sources that the next
    # entry was already rewriting underneath them: a phantom DOES-NOT-COMPILE,
    # or a kill credited to a mutation that was never in the tree when the
    # compiler read it. That is the same misattribution the tail and cut guards
    # exist to prevent, arriving by a different road. Orphans also strand
    # macklebox-conformance-bin-* directories that only the harness's one-hour
    # reaper reclaims.
    #
    # Popen rather than subprocess.run because start_new_session plus a
    # killpg needs the pid, and subprocess.run does not hand it out.
    p = subprocess.Popen(cmd, shell=True, cwd=REPO, text=True,
                         stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                         start_new_session=True)
    try:
        out, err = p.communicate(timeout=RUN_BOUND)
        return p.returncode, out + err
    except subprocess.TimeoutExpired:
        try:
            os.killpg(os.getpgid(p.pid), signal.SIGKILL)
        except (ProcessLookupError, PermissionError):
            # The group is already gone: the shell exited between the timeout
            # firing and this call. Nothing to kill, and the communicate below
            # still collects what was written.
            p.kill()
        # Reachable only because the group is dead: every writer of these pipes
        # was in it, so the reads see EOF rather than blocking on a descendant
        # that outlived the shell -- which is the deadlock the unkilled version
        # would have had here.
        out, err = p.communicate()
        return 124, (out or "") + (err or "") + "\nbattery: %r did not finish within %ds" % (cmd, RUN_BOUND)

def apply(edits, write=True):
    """Resolve every edit's anchors and, unless write is False, apply them.

    write=False is what --anchors uses. It has to touch nothing: the moment an
    anchor check is worth running is right after editing a file in FILES, when
    the tree is dirty by definition, and a mode that rewrote sources there
    would be asking to lose uncommitted work to an interrupt. Resolving is all
    the check needs, and resolution does not depend on writing because both
    modes resolve against the same in-memory buffer below -- that independence
    is a property this function has to arrange, not one it can assume.
    """
    # One entry's edits accumulate in memory and are written, if at all, only
    # once every anchor has resolved. Re-reading the file per edit made
    # resolution depend on writing: with write=True the previous edit was on
    # disk so the next one saw it, and with write=False it did not -- so
    # --anchors resolved every edit after the first against the PRISTINE file
    # and was blind to exactly the class it was added for, an anchor that
    # stops matching once an earlier edit in the same entry lands. Both modes
    # share this buffer rather than one path each, because two code paths for
    # "what does the next edit see" is the drift the check exists to catch.
    #
    # A side effect worth naming: an entry that fails on its second edit now
    # writes nothing at all, where before it left the first edit on disk for
    # restore() to undo on the next iteration.
    buffered = {}
    touched = set()
    for kind, f, a, b in edits:
        path = os.path.join(REPO, f)
        src = buffered.get(path)
        if src is None:
            src = open(path).read()
        if kind == "repl":
            n = src.count(a)
            if n != 1:
                return None, "anchor %r occurs %d times in %s" % (a[:60], n, f)
            src = src.replace(a, b)
        elif kind == "cut":
            anchored, ending = "\n" + a, "\n" + b
            n = src.count(anchored)
            if n != 1:
                return None, "cut start %r occurs %d times at the start of a line in %s" % (a[:60], n, f)
            i = src.find(anchored) + 1
            j = src.find(ending, i)
            if j < 0:
                return None, "cut end %r not found at the start of a line after the start in %s" % (b[:60], f)
            src = src[:i] + src[j + 1:]
        else:
            i = src.find(a)
            if i < 0:
                return None, "marker %r not found in %s" % (a[:60], f)
            strays = [line for line in src[i + len(a):].splitlines()
                      if line.startswith(("func ", "type ", "var ", "const "))]
            if strays:
                return None, ("tail from %r would also delete %d later top-level "
                              "declaration(s) in %s, starting with %r; this edit only "
                              "means what it names while its marker's declaration is "
                              "the last in the file"
                              % (a[:40], len(strays), f, strays[0][:60]))
            src = src[:i] + b
        buffered[path] = src
        touched.add(f)
    if write:
        for path, src in buffered.items():
            open(path, "w").write(src)
    return touched, None

# apply() writes to whatever path a mutation names, but only paths in FILES are
# copied at startup and put back afterwards -- so a mutation naming a file
# outside that list stays in the working tree for good, while the run still
# prints "tree restored". Nothing tied the two together, so tie them here.
untracked = sorted({edit[1] for entry in MUTATIONS for edit in entry[1]} - set(FILES))
if untracked:
    sys.exit("battery: these mutations edit files that are not in FILES, so they "
             "would be neither backed up nor restored: " + ", ".join(untracked))

# --anchors resolves every edit against the clean tree and exits, without
# running the gate once. Seconds rather than an hour.
#
# It exists because an anchor breaking is not a rare event: the two rounds
# before this one each rewrote a line some entry pointed at, and both times
# the tree was already committed and pushed before the full run reported
# BROKEN-ANCHOR. Nothing in `make check` knows this file exists -- it is not
# in FILES, and mutating it dirties the tree -- so there is no other moment
# that reminds you. Run it after any edit to a file in FILES.
#
# It reuses apply() rather than reimplementing the anchor rules, because a
# second copy of "the anchor must occur exactly once" is a copy that drifts,
# and drifting is what this mode is for. It passes write=False, so it touches
# no file and needs neither a clean tree nor a restore -- which matters,
# because the moment this check is worth running is right after an edit, when
# the tree is dirty by definition. It therefore runs BEFORE the dirty-tree
# refusal below, deliberately.
only = sys.argv[1:]
if "--anchors" in only:
    broken = []
    for entry in MUTATIONS:
        touched, why = apply(entry[1], write=False)
        if touched is None:
            broken.append((entry[0], why))
    for name, why in broken:
        print("BROKEN-ANCHOR  %s: %s" % (name, why))
    print("=== %d entries, %d with a broken anchor ===" % (len(MUTATIONS), len(broken)))
    sys.exit(1 if broken else 0)

# A dirty tree cannot be told apart from a run this script corrupted, and
# restore() would overwrite uncommitted work with the startup copy. Refuse.
dirty = subprocess.run(["git", "status", "--porcelain"], cwd=REPO,
                       capture_output=True, text=True).stdout.strip()
if dirty:
    sys.exit("battery: the working tree is dirty; commit or stash first, since "
             "this rewrites sources in place and restores from a copy taken now:\n" + dirty)

snapshot_tree()

# A name that matches nothing used to run zero mutations and still print
# "NOT KILLED: 0" and exit 0 -- a clean battery run that proved nothing, which
# is the exact vacuous-pass shape the rest of this branch exists to remove. A
# typo, or a wrapper naming a mutation that has since been renamed, got a green
# light. Both halves are needed: the name check catches the typo, and the empty
# check catches a MUTATIONS list that has been emptied or filtered to nothing.
known = {entry[0] for entry in MUTATIONS}
unknown = [name for name in only if name not in known]
if unknown:
    sys.exit("battery: no mutation is named " + ", ".join(repr(u) for u in unknown) +
             "\nknown mutations:\n  " + "\n  ".join(sorted(known)))
results = []
try:
  for entry in MUTATIONS:
    name, edits = entry[0], entry[1]
    expect = entry[2] if len(entry) > 2 else None
    if only and name not in only:
        continue
    restore()
    touched, err = apply(edits)
    if err:
        results.append((name, "BROKEN-ANCHOR", err)); print("BROKEN-ANCHOR  %s: %s" % (name, err), flush=True); continue
    for f in touched:
        run("gofmt -w " + f)
    rc, out = run("go vet -tags conformance ./...")
    if rc != 0:
        results.append((name, "DOES-NOT-COMPILE", out[-400:])); print("DOES-NOT-COMPILE  %s" % name, flush=True); print(out[-800:], flush=True); continue
    rc, out = run("make check")
    if expect is SURVIVES:
        if rc != 0:
            broke = sorted({l.split()[2] for l in out.splitlines() if l.strip().startswith("--- FAIL:")})
            results.append((name, "FALSE-POSITIVE", ",".join(broke)))
            print("FALSE-POSITIVE  %s: the gate rejected correct code (%s)" % (name, ",".join(broke)), flush=True)
        else:
            results.append((name, "survives", "as required"))
            print("survives  %-42s (required: the gate must not reject this)" % name, flush=True)
        continue
    if rc == 0:
        results.append((name, "SURVIVED", "")); print("SURVIVED (BATTERY FAILURE)  %s" % name, flush=True); continue
    failed = sorted({l.split()[2] for l in out.splitlines() if l.strip().startswith("--- FAIL:")})
    if expect and expect not in out:
        results.append((name, "WRONG-DIAGNOSTIC", expect)); print("WRONG-DIAGNOSTIC  %s: expected %r" % (name, expect), flush=True); continue
    # `make check` runs the unit packages first and stops there on a failure,
    # so a mutation the unit tests kill never reaches `make conformance` and
    # says NOTHING about whether the rig can see it. For a mutation to the
    # program itself, ask the rig separately. Without this the battery reports
    # a kill the conformance suite never made.
    conf = ""
    if any(f.startswith(("internal/", "cmd/")) for f in touched):
        crc, cout = run("make conformance")
        cfailed = sorted({l.split()[2] for l in cout.splitlines() if l.strip().startswith("--- FAIL:") and "/" not in l.split()[2]})
        if crc == 0:
            results.append((name, "RIG-BLIND", "")); print("RIG-BLIND  %s: killed only outside the conformance suite" % name, flush=True); continue
        conf = "  [rig: %s]" % ",".join(cfailed)
    results.append((name, "killed", ",".join(failed) + conf))
    print("killed  %-42s by %s%s" % (name, ",".join(failed) or "(build/gate)", conf), flush=True)

finally:
  # In a finally, so Ctrl-C during a run that takes minutes -- the expected way
  # to abort one -- puts the sources back instead of leaving "Usage!" in
  # internal/cli/usage.go for someone to find later.
  restore()

restored_rc, _ = run("make check")
print("\n=== tree restored, make check exit=%d ===" % restored_rc, flush=True)
if restored_rc != 0:
    print("BATTERY LEFT THE TREE BROKEN: restore did not put every mutation back", flush=True)
print("\n=== SUMMARY: %d run ===" % len(results), flush=True)
if not results:
    print("BATTERY RAN NOTHING: a run that exercises no mutation proves nothing", flush=True)
bad = [r for r in results if r[1] not in ("killed", "survives")]
for r in results:
    print("  %-16s %s" % (r[1], r[0]), flush=True)
print("\nNOT KILLED: %d" % len(bad), flush=True)
# The restore check is part of the verdict. Printing it and exiting 0 anyway
# let a wrapper record a clean run over a tree this script had broken.
sys.exit(1 if (bad or restored_rc != 0 or not results) else 0)
